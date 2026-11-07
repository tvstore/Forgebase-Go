package forgebase_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// Field permission levels end to end (M10.2, ADR 0024), with forgebase/testing: an app overlay field and a
// site custom field that carry a perm_level, how they behave for callers with and without the level, after a
// live reload and when they are disabled and enabled, and the documents handle of an app method (the caller's
// rights apply there too). Ported from crates/forgebase/tests/field_levels.rs.

// flCustomer: Manager reads and writes level 1, Agent is a plain clerk, Auditor reads level 1.
const flCustomer = `
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
[[permissions]]
role = "Manager"
read = true
write = true
create = true
[[permissions]]
role = "Manager"
level = 1
read = true
write = true
[[permissions]]
role = "Auditor"
read = true
[[permissions]]
role = "Auditor"
level = 1
read = true
`

// flOverlay is the overlay of the app ext: a field at level 1.
const flOverlay = `
[[fields]]
name = "credit_limit"
type = "int"
perm_level = 1
`

// flPeek is crm.peek: the keys of the customer name as the caller reads it.
func flPeek(ctx context.Context, m *forgebase.MethodContext) (any, error) {
	var body struct {
		Name string `json:"name"`
	}
	_ = m.DecodeBody(&body)
	found, err := m.Documents().Get(ctx, "customer", body.Name)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return map[string]any{"keys": nil}, nil
	}
	keys := []string{}
	for k := range found.Document.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return map[string]any{"keys": keys}, nil
}

func flCRM() *forgebase.App {
	return forgebase.NewApp("crm").
		DocTypes(docTypes("doctypes/customer.toml", flCustomer)).
		Method("peek", forgebase.NewMethod(flPeek).AnyUser())
}

func flExt() *forgebase.App {
	return forgebase.NewApp("ext").DependsOn("crm").ExtendDocType("customer", docTypes("overlays/customer.toml", flOverlay))
}

func flBinary() *forgebase.Binary { return forgebase.New().App(flCRM()).App(flExt()) }
func flPlain() *forgebase.Binary  { return forgebase.New().App(flCRM()) }

func flSite(t *testing.T, binary *forgebase.Binary) *fbtest.DB {
	t.Helper()
	db := fbtest.NewDB(t)
	if _, err := db.Migrate(binary, false); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		name  string
		roles []string
	}{
		{"agent", []string{"Agent"}},
		{"manager", []string{"Manager"}},
		{"auditor", []string{"Auditor", "Agent"}},
		{"root", []string{"Administrator"}},
	} {
		db.CreateUser(u.name, u.roles...)
	}
	return db
}

func flKeys(data map[string]any) []string {
	out := []string{}
	for k := range data {
		out = append(out, k)
	}
	return out
}

func flMetaNames(t *testing.T, client *fbtest.Client) []string {
	t.Helper()
	return cuMetaFields(t, client)
}

// Rust: field_levels.rs::an_overlay_field_with_a_level_follows_the_rules_of_the_target
func TestAnOverlayFieldWithALevelFollowsTheRulesOfTheTarget(t *testing.T) {
	binary := flBinary()
	db := flSite(t, binary)
	server := db.Client(binary)
	agent, manager, auditor, root := server.Login("agent"), server.Login("manager"), server.Login("auditor"), server.Login("root")
	created := manager.Post("/api/v1/resource/customer", map[string]any{"email": "a@x.test", "region": "n", "credit_limit": 500})
	cuStatus(t, created, 201)
	if created.DataObject()["credit_limit"] != float64(500) {
		t.Fatalf("%v", created.Body)
	}
	name := created.DataObject()["name"].(string)
	version := created.DataObject()["version"]

	// The agent has no level 1: nothing of the field anywhere.
	got := agent.Get("/api/v1/resource/customer/" + name)
	if cuHas(flKeys(got.DataObject()), "credit_limit") {
		t.Fatalf("%v", got.Body)
	}
	expect(t, flMetaNames(t, agent), []string{"email", "region"})
	expect(t, flMetaNames(t, manager), []string{"email", "region", "credit_limit"})
	refused := agent.Post("/api/v1/resource/customer", map[string]any{"email": "b@x.test", "credit_limit": 1})
	cuStatus(t, refused, 422)
	if refused.ErrorCode() != "validation_failed" {
		t.Fatalf("%v", refused.Body)
	}
	expect(t, refused.Body.(map[string]any)["error"].(map[string]any)["details"], []any{"unknown field `credit_limit`"})
	// An update by the agent leaves the value.
	updated := agent.Patch("/api/v1/resource/customer/"+name, map[string]any{"version": version, "data": map[string]any{"region": "s"}})
	cuStatus(t, updated, 200)
	if cuHas(flKeys(updated.DataObject()), "credit_limit") {
		t.Fatalf("%v", updated.Body)
	}
	if all := root.Get("/api/v1/resource/customer/" + name); all.DataObject()["credit_limit"] != float64(500) {
		t.Fatalf("%v", all.Body)
	}

	// The auditor reads but may not write the field.
	if seen := auditor.Get("/api/v1/resource/customer/" + name); seen.DataObject()["credit_limit"] != float64(500) {
		t.Fatalf("%v", seen.Body)
	}
	denied := auditor.Patch("/api/v1/resource/customer/"+name, map[string]any{"version": 2, "data": map[string]any{"credit_limit": 1}})
	cuStatus(t, denied, 422)
	if denied.ErrorCode() != "field_not_permitted" {
		t.Fatalf("%v", denied.Body)
	}
}

// Rust: field_levels.rs::a_site_custom_field_with_a_level_behaves_the_same_before_and_after_a_reload
func TestASiteCustomFieldWithALevelBehavesTheSameBeforeAndAfterAReload(t *testing.T) {
	binary := flPlain()
	db := flSite(t, binary)
	// This server polls every 100 ms; a second one never does.
	fast := db.ClientPolling(binary, 100*time.Millisecond)
	still := db.ClientPolling(binary, time.Hour)
	agent, manager := fast.Login("agent"), fast.Login("manager")
	slowManager := still.Login("manager")
	expect(t, flMetaNames(t, manager), []string{"email", "region"})

	db.AddCustomField(binary, "customer", "name = \"custom_limit\"\ntype = \"int\"\nperm_level = 1\n")
	// The server that does not poll still has the registry of the apps.
	expect(t, flMetaNames(t, slowManager), []string{"email", "region"})
	if !cuUntil(10*time.Second, func() bool { return cuHas(flMetaNames(t, manager), "custom_limit") }) {
		t.Fatal("the polling server never saw the field")
	}
	still.ReloadMetadata()
	expect(t, flMetaNames(t, slowManager), []string{"email", "region", "custom_limit"})

	// Hidden for the agent, readable and writable for the manager.
	expect(t, flMetaNames(t, agent), []string{"email", "region"})
	created := manager.Post("/api/v1/resource/customer", map[string]any{"email": "a@x.test", "custom_limit": 40})
	cuStatus(t, created, 201)
	name := created.DataObject()["name"].(string)
	if got := agent.Get("/api/v1/resource/customer/" + name); cuHas(flKeys(got.DataObject()), "custom_limit") {
		t.Fatalf("%v", got.Body)
	}
	refused := agent.Post("/api/v1/resource/customer", map[string]any{"email": "b@x.test", "custom_limit": 1})
	cuStatus(t, refused, 422)
	expect(t, refused.Body.(map[string]any)["error"].(map[string]any)["details"], []any{"unknown field `custom_limit`"})
	filters := "%5B%7B%22field%22%3A%22custom_limit%22%2C%22op%22%3A%22%3D%22%2C%22value%22%3A40%7D%5D"
	list := agent.Get("/api/v1/resource/customer?filters=" + filters)
	cuStatus(t, list, 400)
	if list.ErrorCode() != "invalid_query" {
		t.Fatalf("%v", list.Body)
	}
	list = manager.Get("/api/v1/resource/customer?filters=" + filters)
	cuStatus(t, list, 200)
	if len(list.Data().([]any)) != 1 {
		t.Fatalf("%v", list.Body)
	}

	// Disabled: hidden for everybody, administrator included. Enabled: back, with its data.
	db.SetCustomFieldEnabled("customer", "custom_limit", false)
	fast.ReloadMetadata()
	root := fast.Login("root")
	expect(t, flMetaNames(t, root), []string{"email", "region"})
	db.SetCustomFieldEnabled("customer", "custom_limit", true)
	fast.ReloadMetadata()
	expect(t, flMetaNames(t, root), []string{"email", "region", "custom_limit"})
	if all := root.Get("/api/v1/resource/customer/" + name); all.DataObject()["custom_limit"] != float64(40) {
		t.Fatalf("%v", all.Body)
	}
}

// Rust: field_levels.rs::the_custom_field_command_and_api_accept_a_level_and_refuse_a_bad_one
func TestTheCustomFieldCommandAndAPIAcceptALevelAndRefuseABadOne(t *testing.T) {
	binary := flPlain()
	db := flSite(t, binary)
	server := db.Client(binary)
	root := server.Login("root")
	cuStatus(t, root.Post("/api/v1/customize/customer/fields", map[string]any{"name": "custom_a", "type": "int", "perm_level": 2}), 201)
	cuStatus(t, root.Post("/api/v1/customize/customer/fields", map[string]any{"name": "custom_b", "type": "int", "perm_level": 10}), 422)
	// required stays refused with a level.
	cuStatus(t, root.Post("/api/v1/customize/customer/fields", map[string]any{"name": "custom_c", "type": "int", "perm_level": 1, "required": true}), 422)
	// Level 2 has no rule on this DocType: only the administrator sees the field.
	manager := server.Login("manager")
	if fields := flMetaNames(t, manager); cuHas(fields, "custom_a") {
		t.Fatalf("%v", fields)
	}
	listed := root.Get("/api/v1/customize/customer/fields")
	if level := listed.Data().([]any)[0].(map[string]any)["definition"].(map[string]any)["perm_level"]; level != float64(2) {
		t.Fatalf("%v", listed.Body)
	}
}

// Rust: field_levels.rs::a_method_reads_documents_as_its_caller
func TestAMethodReadsDocumentsAsItsCaller(t *testing.T) {
	binary := flBinary()
	db := flSite(t, binary)
	server := db.Client(binary)
	agent, manager := server.Login("agent"), server.Login("manager")
	created := manager.Post("/api/v1/resource/customer", map[string]any{"email": "a@x.test", "credit_limit": 5})
	name := created.DataObject()["name"].(string)
	asAgent := agent.Post("/api/v1/method/crm.peek", map[string]any{"name": name})
	cuStatus(t, asAgent, 200)
	seen := asAgent.DataObject()["keys"].([]any)
	if cuHas(anyStrings(seen), "credit_limit") || !cuHas(anyStrings(seen), "email") {
		t.Fatalf("%v", seen)
	}
	asManager := manager.Post("/api/v1/method/crm.peek", map[string]any{"name": name})
	if seen := anyStrings(asManager.DataObject()["keys"].([]any)); !cuHas(seen, "credit_limit") {
		t.Fatalf("%v", seen)
	}
}

func anyStrings(items []any) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i], _ = item.(string)
	}
	return out
}
