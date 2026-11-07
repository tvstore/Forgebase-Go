package forgebase_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// Metadata overlays end to end (M10.1, ADR 0023): the fields an app adds to a dependency's DocType, and the
// custom fields a site adds to its own database. Real PostgreSQL, written with forgebase/testing, over HTTP
// where possible. Ported from crates/forgebase/tests/customize.rs.
//
// The same checks run for both kinds of field, because "every path sees the field" is the property: insert and
// update, get, list filter and sort, summary, export and import, hooks, the permission rules, the metadata API
// and webhook snapshots. Then the parts that belong to custom fields only: the Administrator-only API, disable
// and enable, two sites that differ, live reload, the swap under load, and a stored field that no longer
// validates.

const cuCustomer = `
name = "customer"
label = "Customer"
[[fields]]
name = "email"
type = "data"
required = true
unique = true
[[fields]]
name = "region"
type = "data"

[[permissions]]
role = "Agent"
read = true
write = true
create = true
delete = true
export = true
import = true

[[permissions]]
role = "Viewer"
read = true
`

const cuNote = `
name = "note"
label = "Note"
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "lines"
type = "table"
target = "note_line"

[[permissions]]
role = "Agent"
read = true
write = true
create = true
`

const cuNoteLine = `
name = "note_line"
label = "Note Line"
is_child = true
[[fields]]
name = "text"
type = "data"
`

// cuOverlay is the overlay of the app ext: three fields on customer, the last written by a hook only.
const cuOverlay = `
[[fields]]
name = "tier"
type = "select"
options = ["bronze", "silver", "gold"]
[[fields]]
name = "points"
type = "int"
[[fields]]
name = "badge"
type = "data"
read_only = true
`

// cuBadge is a hook of the app that owns the overlay: it sees tier and points and sets badge.
type cuBadge struct{ forgebase.BaseHooks }

func (cuBadge) Validate(_ forgebase.HookContext, doc *forgebase.Document) error {
	gold := doc.Values["tier"].Equal(forgebase.Text("gold"))
	points := doc.Values["points"]
	many := points.Kind == forgebase.KindInt && points.Int >= 100
	if gold && many {
		doc.Values["badge"] = forgebase.Text("VIP")
	} else {
		doc.Values["badge"] = forgebase.Null()
	}
	return nil
}

func cuCRM() *forgebase.App {
	return forgebase.NewApp("crm").DocTypes(docTypes(
		"doctypes/customer.toml", cuCustomer, "doctypes/note.toml", cuNote, "doctypes/note_line.toml", cuNoteLine))
}

func cuExt() *forgebase.App {
	return forgebase.NewApp("ext").DependsOn("crm").
		ExtendDocType("customer", docTypes("overlays/customer.toml", cuOverlay)).
		Hooks("customer", cuBadge{})
}

// cuWithOverlay is the apps with the overlay.
func cuWithOverlay() *forgebase.Binary { return forgebase.New().App(cuCRM()).App(cuExt()) }

// cuPlain is the apps without it.
func cuPlain() *forgebase.Binary { return forgebase.New().App(cuCRM()) }

func cuFilter(field, op string, value any) string {
	text, _ := json.Marshal([]map[string]any{{"field": field, "op": op, "value": value}})
	return url.QueryEscape(string(text))
}

// cuSite is a migrated database with agent (role Agent), viewer (Viewer), nobody (no role that reads) and root
// (Administrator).
func cuSite(t *testing.T, binary *forgebase.Binary) *fbtest.DB {
	t.Helper()
	db := fbtest.NewDB(t)
	if _, err := db.Migrate(binary, false); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][2]string{{"agent", "Agent"}, {"viewer", "Viewer"}, {"nobody", "Stranger"}, {"root", "Administrator"}} {
		db.CreateUser(u[0], u[1])
	}
	return db
}

// cuColumns are the column names of a table.
func cuColumns(db *fbtest.DB, table string) []string {
	var out []string
	for _, r := range db.Rows(fmt.Sprintf("SELECT column_name::text FROM information_schema.columns "+
		"WHERE table_schema = current_schema() AND table_name = '%s' ORDER BY ordinal_position", table)) {
		out = append(out, r[0])
	}
	return out
}

func cuNames(fields any) []string {
	out := []string{}
	for _, f := range fields.([]any) {
		out = append(out, f.(map[string]any)["name"].(string))
	}
	return out
}

// cuMetaFields are the fields of GET /meta/customer.
func cuMetaFields(t *testing.T, client *fbtest.Client) []string {
	t.Helper()
	reply := client.Get("/api/v1/meta/customer")
	if reply.Status != 200 {
		t.Fatalf("%d %v", reply.Status, reply.Body)
	}
	return cuNames(reply.DataObject()["fields"])
}

func cuHas(list []string, item string) bool {
	for _, s := range list {
		if s == item {
			return true
		}
	}
	return false
}

func cuStatus(t *testing.T, reply *fbtest.Reply, want int) {
	t.Helper()
	if reply.Status != want {
		t.Fatalf("status %d, want %d: %v", reply.Status, want, reply.Body)
	}
}

// cuExercise runs every path that reads or writes a customer, with one field numeric (an int), one choice (a
// select with bronze, silver and gold) and, for the overlay, a hook that sets badge. withHook says whether the
// hook is there.
func cuExercise(t *testing.T, db *fbtest.DB, server *fbtest.Client, numeric, choice string, withHook bool) {
	t.Helper()
	agent, viewer, nobody := server.Login("agent"), server.Login("viewer"), server.Login("nobody")
	a := func(email string, points int, tier string) map[string]any {
		return map[string]any{"email": email, "region": "north", numeric: points, choice: tier}
	}

	// The metadata API lists the fields, after the DocType's own.
	fields := cuMetaFields(t, agent)
	if fields[0] != "email" || fields[1] != "region" || !cuHas(fields, numeric) || !cuHas(fields, choice) {
		t.Fatalf("%v", fields)
	}

	// Insert, get, update.
	created := agent.Post("/api/v1/resource/customer", a("a@x.test", 120, "gold"))
	cuStatus(t, created, 201)
	name := created.DataObject()["name"].(string)
	if created.DataObject()[numeric] != float64(120) || created.DataObject()[choice] != "gold" {
		t.Fatalf("%v", created.Body)
	}
	if withHook && created.DataObject()["badge"] != "VIP" {
		// The hook of the app that owns the overlay saw both fields and set the third.
		t.Fatalf("%v", created.Body)
	}
	for _, c := range []struct {
		email  string
		points int
		tier   string
	}{{"b@x.test", 30, "silver"}, {"c@x.test", 60, "silver"}} {
		cuStatus(t, agent.Post("/api/v1/resource/customer", a(c.email, c.points, c.tier)), 201)
	}
	got := agent.Get("/api/v1/resource/customer/" + name)
	if got.DataObject()[numeric] != float64(120) {
		t.Fatalf("%v", got.Body)
	}
	version := got.DataObject()["version"]
	updated := agent.Patch("/api/v1/resource/customer/"+name, map[string]any{"version": version, "data": map[string]any{numeric: 150}})
	cuStatus(t, updated, 200)
	if updated.DataObject()[numeric] != float64(150) {
		t.Fatalf("%v", updated.Body)
	}
	if withHook && updated.DataObject()["badge"] != "VIP" {
		t.Fatalf("%v", updated.Body)
	}
	// Validation applies like for any field: a value that is not an option is refused.
	cuStatus(t, agent.Post("/api/v1/resource/customer", a("d@x.test", 1, "platinum")), 422)

	// List: filter, sort.
	list := agent.Get(fmt.Sprintf("/api/v1/resource/customer?filters=%s&sort=%s", cuFilter(numeric, ">=", 50), url.QueryEscape(numeric+":desc")))
	cuStatus(t, list, 200)
	var points []float64
	for _, d := range list.Data().([]any) {
		points = append(points, d.(map[string]any)[numeric].(float64))
	}
	expect(t, points, []float64{150, 60})

	// Summary: group by the select, sum the int.
	summary := agent.Get(fmt.Sprintf("/api/v1/summary/customer?group_by=%s&aggregates=%s", choice, url.QueryEscape("sum:"+numeric)))
	cuStatus(t, summary, 200)
	var silver map[string]any
	for _, g := range summary.DataObject()["groups"].([]any) {
		if g.(map[string]any)["key"] == "silver" {
			silver = g.(map[string]any)
		}
	}
	if silver == nil || silver["count"] != float64(2) || silver["values"].(map[string]any)["sum_"+numeric] != float64(90) {
		t.Fatalf("%v", summary.Body)
	}

	// Export includes the field; an import sets it.
	export := agent.Get("/api/v1/export/customer?format=json&sort=" + url.QueryEscape(numeric+":asc"))
	cuStatus(t, export, 200)
	rows := export.Body.([]any)
	if len(rows) != 3 || rows[0].(map[string]any)[numeric] != float64(30) {
		t.Fatalf("%v", export.Body)
	}
	body, _ := json.Marshal([]map[string]any{{"email": "i@x.test", numeric: 77, choice: "bronze"}})
	cuStatus(t, agent.PostRaw("/api/v1/import/customer?format=json&mode=insert", "application/json", body), 200)
	imported := agent.Get("/api/v1/resource/customer?filters=" + cuFilter(numeric, "=", 77))
	if len(imported.Data().([]any)) != 1 {
		t.Fatalf("%v", imported.Body)
	}

	// The permission rules apply as for any field: a Viewer reads it and cannot write it, a caller with no
	// right on the DocType gets nothing.
	seen := viewer.Get("/api/v1/resource/customer/" + name)
	if seen.DataObject()[numeric] != float64(150) {
		t.Fatalf("%v", seen.Body)
	}
	cuStatus(t, viewer.Patch("/api/v1/resource/customer/"+name, map[string]any{"version": 99, "data": map[string]any{numeric: 1}}), 403)
	cuStatus(t, nobody.Get("/api/v1/resource/customer/"+name), 403)
	cuStatus(t, nobody.Get("/api/v1/meta/customer"), 403)

	// A webhook snapshot has the field.
	db.Exec("INSERT INTO fb_webhook (name, doctype, events, url, secret, allow_private, insecure_http) " +
		"VALUES ('hook', 'customer', '{insert}', 'http://127.0.0.1:9/h', 'secret', true, true)")
	cuStatus(t, agent.Post("/api/v1/resource/customer", a("w@x.test", 5, "bronze")), 201)
	snapshot := db.Rows(fmt.Sprintf("SELECT payload::jsonb->'data'->>'%s' FROM fb_job WHERE name = 'forgebase.deliver_webhook'", numeric))
	expect(t, snapshot, [][]string{{"5"}})
}

// Rust: customize.rs::an_app_overlay_field_is_seen_by_every_path
func TestAnAppOverlayFieldIsSeenByEveryPath(t *testing.T) {
	binary := cuWithOverlay()
	db := cuSite(t, binary)
	// migrate added the columns.
	columns := cuColumns(db, "doc_customer")
	for _, c := range []string{"tier", "points", "badge"} {
		if !cuHas(columns, c) {
			t.Fatalf("%v", columns)
		}
	}
	server := db.Client(binary)
	cuExercise(t, db, server, "points", "tier", true)

	// The field badge is written by the hook only: caller input for it is refused.
	agent := server.Login("agent")
	cuStatus(t, agent.Post("/api/v1/resource/customer", map[string]any{"email": "z@x.test", "badge": "VIP"}), 422)
	// The meta API has the owner app of the DocType, as before.
	if app := agent.Get("/api/v1/meta/customer").DataObject()["app"]; app != "crm" {
		t.Fatalf("%v", app)
	}
}

// Rust: customize.rs::without_the_overlay_nothing_changes
func TestWithoutTheOverlayNothingChanges(t *testing.T) {
	binary := cuPlain()
	db := cuSite(t, binary)
	agent := db.Client(binary).Login("agent")
	expect(t, cuMetaFields(t, agent), []string{"email", "region"})
	if cuHas(cuColumns(db, "doc_customer"), "tier") {
		t.Fatal("the column exists")
	}
	cuStatus(t, agent.Post("/api/v1/resource/customer", map[string]any{"email": "a@x.test", "tier": "gold"}), 422)
}

// Rust: customize.rs::a_site_custom_field_is_seen_by_every_path
func TestASiteCustomFieldIsSeenByEveryPath(t *testing.T) {
	binary := cuPlain()
	db := cuSite(t, binary)
	server := db.Client(binary)
	root := server.Login("root")
	for _, body := range []map[string]any{
		{"name": "custom_points", "type": "int"},
		{"name": "custom_tier", "type": "select", "options": []string{"bronze", "silver", "gold"}},
	} {
		cuStatus(t, root.Post("/api/v1/customize/customer/fields", body), 201)
	}
	// The server that took the change sees it at once, with no reload interval to wait for.
	cuExercise(t, db, server, "custom_points", "custom_tier", false)
}

// Rust: customize.rs::a_disabled_field_is_hidden_everywhere_and_its_data_is_kept
func TestADisabledFieldIsHiddenEverywhereAndItsDataIsKept(t *testing.T) {
	binary := cuPlain()
	db := cuSite(t, binary)
	server := db.Client(binary)
	root, agent := server.Login("root"), server.Login("agent")
	cuStatus(t, root.Post("/api/v1/customize/customer/fields", map[string]any{"name": "custom_vip", "type": "check"}), 201)
	created := agent.Post("/api/v1/resource/customer", map[string]any{"email": "a@x.test", "custom_vip": true})
	cuStatus(t, created, 201)
	name := created.DataObject()["name"].(string)

	disabled := root.Post("/api/v1/customize/customer/fields/custom_vip/disable", map[string]any{})
	cuStatus(t, disabled, 200)
	if disabled.DataObject()["enabled"] != false {
		t.Fatalf("%v", disabled.Body)
	}

	// Hidden: the meta API, the document, input, filters, sort, summary and export.
	if cuHas(cuMetaFields(t, agent), "custom_vip") {
		t.Fatal("the meta API shows the disabled field")
	}
	got := agent.Get("/api/v1/resource/customer/" + name)
	if _, there := got.DataObject()["custom_vip"]; there {
		t.Fatalf("%v", got.Body)
	}
	version := got.DataObject()["version"]
	cuStatus(t, agent.Patch("/api/v1/resource/customer/"+name, map[string]any{"version": version, "data": map[string]any{"custom_vip": false}}), 422)
	cuStatus(t, agent.Post("/api/v1/resource/customer", map[string]any{"email": "b@x.test", "custom_vip": true}), 422)
	filtered := agent.Get("/api/v1/resource/customer?filters=" + cuFilter("custom_vip", "=", true))
	cuStatus(t, filtered, 400)
	if filtered.ErrorCode() != "invalid_query" {
		t.Fatalf("%v", filtered.Body)
	}
	cuStatus(t, agent.Get("/api/v1/resource/customer?sort=custom_vip"), 400)
	cuStatus(t, agent.Get("/api/v1/summary/customer?group_by=custom_vip"), 400)
	export := agent.Get("/api/v1/export/customer?format=json")
	cuStatus(t, export, 200)
	if _, there := export.Body.([]any)[0].(map[string]any)["custom_vip"]; there {
		t.Fatalf("%v", export.Body)
	}
	cuStatus(t, agent.Get("/api/v1/export/customer?format=json&fields=custom_vip"), 400)
	// The data is still in the column.
	expect(t, db.Rows("SELECT custom_vip::text FROM doc_customer"), [][]string{{"true"}})
	// The list still shows it, disabled.
	listed := root.Get("/api/v1/customize/customer/fields")
	if listed.Data().([]any)[0].(map[string]any)["enabled"] != false {
		t.Fatalf("%v", listed.Body)
	}

	// Enabled again: the old value is back.
	cuStatus(t, root.Post("/api/v1/customize/customer/fields/custom_vip/enable", map[string]any{}), 200)
	got = agent.Get("/api/v1/resource/customer/" + name)
	if got.DataObject()["custom_vip"] != true {
		t.Fatalf("%v", got.Body)
	}
	if !cuHas(cuMetaFields(t, agent), "custom_vip") {
		t.Fatal("the meta API does not show the enabled field")
	}
}

// Rust: customize.rs::the_customize_api_is_for_administrators_and_validates
func TestTheCustomizeAPIIsForAdministratorsAndValidates(t *testing.T) {
	binary := cuPlain()
	db := cuSite(t, binary)
	server := db.Client(binary)
	root := server.Login("root")
	post := func(client *fbtest.Client, doctype string, body any) *fbtest.Reply {
		return client.Post("/api/v1/customize/"+doctype+"/fields", body)
	}

	// A non-administrator, with a right on the DocType or not, gets 403 on every route; the cookie of an
	// administrator without the CSRF header gets 403 too.
	ok := map[string]any{"name": "custom_a", "type": "check"}
	for _, user := range []string{"agent", "viewer", "nobody"} {
		client := server.Login(user)
		list := client.Get("/api/v1/customize/customer/fields")
		if list.Status != 403 || list.ErrorCode() != "permission_denied" {
			t.Errorf("%s: %d %v", user, list.Status, list.Body)
		}
		add := post(client, "customer", ok)
		if add.Status != 403 || add.ErrorCode() != "permission_denied" {
			t.Errorf("%s: %d %v", user, add.Status, add.Body)
		}
		for _, action := range []string{"disable", "enable"} {
			if r := client.Post("/api/v1/customize/customer/fields/custom_a/"+action, map[string]any{}); r.Status != 403 {
				t.Errorf("%s %s: %d", user, action, r.Status)
			}
		}
	}
	noCSRF := post(root.WithoutCSRF(), "customer", ok)
	if noCSRF.Status != 403 || noCSRF.ErrorCode() != "csrf_failed" {
		t.Fatalf("%d %v", noCSRF.Status, noCSRF.Body)
	}
	// No credentials: 401.
	cuStatus(t, db.Client(binary).Get("/api/v1/customize/customer/fields"), 401)
	// Nothing was added by any of the above.
	if n := len(root.Get("/api/v1/customize/customer/fields").Data().([]any)); n != 0 {
		t.Fatalf("%d fields", n)
	}

	// An API token of an administrator works like a session, without CSRF.
	byToken := db.Client(binary).WithToken(db.CreateToken("root"))
	reply := post(byToken, "customer", map[string]any{"name": "custom_t", "type": "int"})
	cuStatus(t, reply, 201)
	if reply.DataObject()["modified_by"] != "root" || reply.DataObject()["enabled"] != true || reply.DataObject()["type"] != "int" {
		t.Fatalf("%v", reply.Body)
	}

	// The administrator's rules.
	for _, c := range []struct {
		doctype string
		body    any
		status  int
	}{
		{"customer", map[string]any{"name": "vip", "type": "check"}, 422},
		{"customer", map[string]any{"name": "custom_", "type": "check"}, 422},
		{"customer", map[string]any{"name": "custom_a", "type": "table", "target": "note_line"}, 422},
		{"customer", map[string]any{"name": "custom_a", "type": "check", "required": true}, 422},
		{"customer", map[string]any{"name": "custom_a", "type": "check", "read_only": true}, 422},
		{"customer", map[string]any{"name": "custom_a", "type": "link", "target": "ghost"}, 422},
		{"customer", map[string]any{"name": "custom_a", "type": "link", "target": "note_line"}, 422},
		{"customer", map[string]any{"name": "custom_a", "type": "nope"}, 422},
		{"customer", map[string]any{"name": "custom_a", "type": "check", "colour": "red"}, 422},
		{"customer", map[string]any{"type": "check"}, 422},
		{"customer", []any{1, 2}, 422},
		{"note_line", map[string]any{"name": "custom_a", "type": "check"}, 422},
		{"ghost", map[string]any{"name": "custom_a", "type": "check"}, 404},
		{"customer", map[string]any{"name": "custom_t", "type": "check"}, 409},
		{"customer", map[string]any{"name": "email", "type": "check"}, 422},
	} {
		reply := post(root, c.doctype, c.body)
		if reply.Status != c.status {
			t.Errorf("%s %v: %d %v", c.doctype, c.body, reply.Status, reply.Body)
		}
	}
	// A disabled field still holds its name.
	root.Post("/api/v1/customize/customer/fields/custom_t/disable", map[string]any{})
	cuStatus(t, post(root, "customer", map[string]any{"name": "custom_t", "type": "check"}), 409)
	// Unknown things are 404.
	cuStatus(t, root.Post("/api/v1/customize/customer/fields/custom_nope/disable", map[string]any{}), 404)
	cuStatus(t, root.Get("/api/v1/customize/ghost/fields"), 404)
	// A child DocType can be listed (it exists) and has no fields.
	cuStatus(t, root.Get("/api/v1/customize/note_line/fields"), 200)
	// The refused ones left nothing behind.
	expect(t, cuNames(root.Get("/api/v1/customize/customer/fields").Data()), []string{"custom_t"})
}

// Rust: customize.rs::two_sites_with_different_custom_fields_stay_apart
func TestTwoSitesWithDifferentCustomFieldsStayApart(t *testing.T) {
	binary := cuPlain()
	a, b := cuSite(t, binary), cuSite(t, binary)
	rootA, rootB := a.Client(binary).Login("root"), b.Client(binary).Login("root")
	cuStatus(t, rootA.Post("/api/v1/customize/customer/fields", map[string]any{"name": "custom_a", "type": "int"}), 201)
	cuStatus(t, rootB.Post("/api/v1/customize/customer/fields", map[string]any{"name": "custom_b", "type": "data"}), 201)
	agentA, agentB := a.Client(binary).Login("agent"), b.Client(binary).Login("agent")
	// The clients above started before nothing else changed: their registries are theirs.
	agentA.ReloadMetadata()
	agentB.ReloadMetadata()
	expect(t, cuMetaFields(t, agentA), []string{"email", "region", "custom_a"})
	expect(t, cuMetaFields(t, agentB), []string{"email", "region", "custom_b"})
	inA := agentA.Post("/api/v1/resource/customer", map[string]any{"email": "x@x.test", "custom_a": 1})
	cuStatus(t, inA, 201)
	if _, there := inA.DataObject()["custom_b"]; there {
		t.Fatalf("%v", inA.Body)
	}
	// A field of site A is unknown on site B, in input and in filters.
	cuStatus(t, agentB.Post("/api/v1/resource/customer", map[string]any{"email": "x@x.test", "custom_a": 1}), 422)
	cuStatus(t, agentB.Get("/api/v1/resource/customer?filters="+cuFilter("custom_a", "=", 1)), 400)
	cuStatus(t, agentB.Post("/api/v1/resource/customer", map[string]any{"email": "x@x.test", "custom_b": "hi"}), 201)
	// Their lists show only their own fields.
	first := func(c *fbtest.Client) map[string]any {
		return c.Get("/api/v1/resource/customer").Data().([]any)[0].(map[string]any)
	}
	listA, listB := first(agentA), first(agentB)
	if _, ok := listA["custom_a"]; !ok {
		t.Errorf("%v", listA)
	}
	if _, ok := listA["custom_b"]; ok {
		t.Errorf("%v", listA)
	}
	if _, ok := listB["custom_b"]; !ok {
		t.Errorf("%v", listB)
	}
	if _, ok := listB["custom_a"]; ok {
		t.Errorf("%v", listB)
	}
	// The columns are in the right database only.
	expect(t, a.Rows("SELECT count(*)::text FROM information_schema.columns WHERE table_name = 'doc_customer' AND column_name LIKE 'custom_%'"), [][]string{{"1"}})
}

// cuUntil waits (at most limit) until condition holds.
func cuUntil(limit time.Duration, condition func() bool) bool {
	start := time.Now()
	for time.Since(start) < limit {
		if condition() {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// Rust: customize.rs::a_server_sees_a_field_added_elsewhere_within_the_reload_interval
func TestAServerSeesAFieldAddedElsewhereWithinTheReloadInterval(t *testing.T) {
	binary := cuPlain()
	db := cuSite(t, binary)
	// A long interval: this server does not look in the test's time, so the first half is deterministic. The
	// other polls every 100 ms.
	slow := db.ClientPolling(binary, time.Hour).Login("agent")
	fast := db.ClientPolling(binary, 100*time.Millisecond).Login("agent")
	expect(t, cuMetaFields(t, fast), []string{"email", "region"})

	// Another process (here: the command's function) adds the field.
	db.AddCustomField(binary, "customer", "name = \"custom_vip\"\ntype = \"check\"\n")

	expect(t, cuMetaFields(t, slow), []string{"email", "region"}) // no poll has run: the registry is unchanged
	cuStatus(t, slow.Post("/api/v1/resource/customer", map[string]any{"email": "a@x.test", "custom_vip": true}), 422)
	if !cuUntil(10*time.Second, func() bool { return cuHas(cuMetaFields(t, fast), "custom_vip") }) {
		t.Fatal("the polling server never saw the field")
	}
	cuStatus(t, fast.Post("/api/v1/resource/customer", map[string]any{"email": "a@x.test", "custom_vip": true}), 201)
	// The forced reload is the testing helper for the same thing.
	slow.ReloadMetadata()
	if !cuHas(cuMetaFields(t, slow), "custom_vip") {
		t.Fatal("ReloadMetadata did not bring the field")
	}

	// Disabling is seen the same way.
	db.SetCustomFieldEnabled("customer", "custom_vip", false)
	if !cuUntil(10*time.Second, func() bool { return !cuHas(cuMetaFields(t, fast), "custom_vip") }) {
		t.Fatal("the polling server never saw the field disabled")
	}
}

// Rust: customize.rs::swapping_the_registry_under_load_fails_no_request
func TestSwappingTheRegistryUnderLoadFailsNoRequest(t *testing.T) {
	binary := cuPlain()
	db := cuSite(t, binary)
	db.AddCustomField(binary, "customer", "name = \"custom_a\"\ntype = \"int\"\n")
	// A very short interval, so the poll swaps all the time besides the forced reloads.
	agent := db.ClientPolling(binary, 5*time.Millisecond).Login("agent")
	agent.ReloadMetadata()
	cuStatus(t, agent.Post("/api/v1/resource/customer", map[string]any{"email": "seed@x.test", "custom_a": 1}), 201)

	var stop atomic.Bool
	var wg sync.WaitGroup
	var total atomic.Uint64
	var failures atomic.Int32
	fail := func(format string, args ...any) {
		if failures.Add(1) <= 5 {
			t.Errorf(format, args...)
		}
	}
	for worker := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for requests := 0; !stop.Load(); requests++ {
				// A list: every row of the answer has the field or none has (one registry).
				list := agent.Get("/api/v1/resource/customer")
				if list.Status != 200 {
					fail("list %d %v", list.Status, list.Body)
					return
				}
				rows := list.Data().([]any)
				with := 0
				for _, r := range rows {
					if _, ok := r.(map[string]any)["custom_a"]; ok {
						with++
					}
				}
				if with != 0 && with != len(rows) {
					fail("mixed registry in one answer")
				}
				// The meta: the field is there or not, never half of anything.
				meta := agent.Get("/api/v1/meta/customer")
				if meta.Status != 200 {
					fail("meta %d %v", meta.Status, meta.Body)
					return
				}
				custom := 0
				for _, n := range cuNames(meta.DataObject()["fields"]) {
					if strings.HasPrefix(n, "custom_") {
						custom++
					}
				}
				if custom > 1 {
					fail("%d custom fields", custom)
				}
				// A write that does not use the field works whatever the registry is.
				if r := agent.Post("/api/v1/resource/customer", map[string]any{"email": fmt.Sprintf("w%d-%d@x.test", worker, requests)}); r.Status != 201 {
					fail("write %d %v", r.Status, r.Body)
					return
				}
				// A write with the field either succeeds (enabled) or is a clean 422 (disabled).
				if r := agent.Post("/api/v1/resource/customer", map[string]any{"email": fmt.Sprintf("f%d-%d@x.test", worker, requests), "custom_a": 2}); r.Status != 201 && r.Status != 422 {
					fail("field write %d %v", r.Status, r.Body)
					return
				}
				total.Add(1)
			}
		}()
	}
	for round := range 20 {
		db.SetCustomFieldEnabled("customer", "custom_a", round%2 == 1)
		agent.ReloadMetadata()
		time.Sleep(10 * time.Millisecond)
	}
	stop.Store(true)
	wg.Wait()
	if total.Load() == 0 {
		t.Fatal("no request ran")
	}
}

// Rust: customize.rs::a_stored_field_that_no_longer_validates_is_skipped_and_the_site_serves
func TestAStoredFieldThatNoLongerValidatesIsSkippedAndTheSiteServes(t *testing.T) {
	binary := cuPlain()
	db := cuSite(t, binary)
	db.AddCustomField(binary, "customer", "name = \"custom_ok\"\ntype = \"int\"\n")
	// Its Link target left the apps; written around the framework.
	db.Exec(`INSERT INTO fb_custom_field (doctype, name, definition, modified_by) VALUES ` +
		`('customer', 'custom_gone', '{"name":"custom_gone","type":"link","target":"ghost"}', 'sql')`)
	client := db.Client(binary).Login("agent")
	client.ReloadMetadata()
	expect(t, cuMetaFields(t, client), []string{"email", "region", "custom_ok"})
	cuStatus(t, client.Post("/api/v1/resource/customer", map[string]any{"email": "a@x.test", "custom_ok": 3}), 201)
	// migrate still works with the bad row in place.
	if _, err := db.Migrate(binary, false); err != nil {
		t.Fatal(err)
	}
}

// Rust: customize.rs::migrate_recreates_a_missing_custom_column
func TestMigrateRecreatesAMissingCustomColumn(t *testing.T) {
	binary := cuPlain()
	db := cuSite(t, binary)
	db.AddCustomField(binary, "customer", "name = \"custom_a\"\ntype = \"int\"\n")
	db.Exec("ALTER TABLE doc_customer DROP COLUMN custom_a")
	if _, err := db.Migrate(binary, false); err != nil {
		t.Fatal(err)
	}
	if columns := cuColumns(db, "doc_customer"); !cuHas(columns, "custom_a") {
		t.Fatalf("%v", columns)
	}
}

// cuTag is a job handler that writes a custom field: it sees the registry of the worker's site.
func cuTag(ctx context.Context, job *forgebase.JobContext) error {
	_, err := job.Documents().Insert(ctx, "customer", forgebase.Document{Values: map[string]forgebase.Value{
		"email": forgebase.Text("job@x.test"), "custom_a": forgebase.Int(9)}})
	return err
}

// Rust: customize.rs::a_job_sees_the_custom_fields_of_its_site
func TestAJobSeesTheCustomFieldsOfItsSite(t *testing.T) {
	binary := forgebase.New().App(cuCRM().Job("tag", cuTag))
	db := cuSite(t, binary)
	db.AddCustomField(binary, "customer", "name = \"custom_a\"\ntype = \"int\"\n")
	db.Exec("INSERT INTO fb_job (id, name, payload, status, enqueued_by) VALUES (gen_random_uuid(), 'crm.tag', '{}', 'queued', 'test')")
	run := fbtest.RunJobs(binary, db)
	if run.Done != 1 || run.Dead != 0 {
		t.Fatalf("%+v", run)
	}
	expect(t, db.Rows("SELECT custom_a::text FROM doc_customer WHERE email = 'job@x.test'"), [][]string{{"9"}})
}

// A schema change waits for the migrate lock; when migrate holds it longer than the lock timeout, the answer
// is 503 lock_timeout with Retry-After, nothing is written, and the server still works.
//
// Rust: customize.rs::a_busy_migrate_lock_is_a_503_that_changes_nothing
func TestABusyMigrateLockIsA503ThatChangesNothing(t *testing.T) {
	binary := cuPlain()
	db := cuSite(t, binary)
	server := db.Client(binary)
	root := server.Login("root")
	// Another session holds the lock of migrate (db.MigrateLockKey).
	ctx := context.Background()
	held, err := pgx.Connect(ctx, db.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close(ctx) }()
	if _, err := held.Exec(ctx, "SELECT pg_advisory_lock(5062693908242366465)"); err != nil {
		t.Fatal(err)
	}
	busy := root.Post("/api/v1/customize/customer/fields", map[string]any{"name": "custom_a", "type": "int"})
	cuStatus(t, busy, 503)
	if busy.ErrorCode() != "lock_timeout" || busy.Header("Retry-After") != "5" {
		t.Fatalf("%v %q", busy.Body, busy.Header("Retry-After"))
	}
	if _, err := held.Exec(ctx, "SELECT pg_advisory_unlock(5062693908242366465)"); err != nil {
		t.Fatal(err)
	}
	if n := len(root.Get("/api/v1/customize/customer/fields").Data().([]any)); n != 0 {
		t.Fatalf("%d fields", n)
	}
	// Once the lock is free the same call works.
	cuStatus(t, root.Post("/api/v1/customize/customer/fields", map[string]any{"name": "custom_a", "type": "int"}), 201)
}
