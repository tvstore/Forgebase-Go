package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/db"
)

// The customize field and user permission commands (M10.1 and M10.3) against a real PostgreSQL, run in process,
// and the tables they print. Ported from crates/forgebase/tests/cli.rs (the customize_*, user_permission_* and
// the_data_and_doc_* tests) and the unit tests of customize_cmd.rs and user_permission_cmd.rs.

func (e *env) scratchFile(name, content string) string {
	e.t.Helper()
	path := filepath.Join(e.t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return path
}

func (e *env) addField(field, stdin string) result {
	return e.runStdin(stdin, "customize", "field", "add", "--doctype", "customer", "--field", field, "--doctypes", doctypes)
}

func jsonOf(t *testing.T, r result) map[string]any {
	t.Helper()
	return r.json(t)
}

// Rust: cli.rs::customize_field_adds_lists_disables_and_enables
func TestCustomizeFieldAddsListsDisablesAndEnables(t *testing.T) {
	e := newEnv(t).migrated()
	file := e.scratchFile("vip.toml", "name = \"custom_vip\"\nlabel = \"VIP\"\ntype = \"check\"\n")

	// From a file: the column exists at once.
	added := e.addField(file, "")
	expectCode(t, added, 0, "add from a file: "+added.stderr)
	if !strings.HasPrefix(added.stdout, "added custom field customer.custom_vip (check)") {
		t.Fatalf("%s", added.stdout)
	}
	if got := e.scalar("SELECT data_type FROM information_schema.columns WHERE table_name = 'doc_customer' AND column_name = 'custom_vip'"); got != "boolean" {
		t.Fatalf("%s", got)
	}
	if got := e.scalar("SELECT version::text FROM fb_meta_version"); got != "1" {
		t.Fatalf("%s", got)
	}

	// From standard input, with a type parameter and unique.
	fromStdin := e.addField("-", "name = \"custom_rank\"\ntype = \"int\"\nunique = true\n")
	expectCode(t, fromStdin, 0, "add from stdin: "+fromStdin.stderr)

	// list: a table with every field, then one DocType's.
	list := e.run("customize", "field", "list")
	expectCode(t, list, 0, list.stderr)
	lines := strings.Split(strings.TrimSuffix(list.stdout, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%s", list.stdout)
	}
	if !strings.HasPrefix(lines[0], "DOCTYPE") || !strings.Contains(lines[0], "ENABLED") {
		t.Errorf("%s", list.stdout)
	}
	// In the order they were added.
	if !strings.Contains(lines[1], "custom_vip") || !strings.Contains(lines[1], "VIP") || !strings.Contains(lines[1], "yes") || !strings.HasSuffix(strings.TrimRight(lines[1], " "), "cli") {
		t.Errorf("%s", list.stdout)
	}
	if !strings.Contains(lines[2], "custom_rank") || !strings.Contains(lines[2], "Custom Rank") {
		t.Errorf("%s", list.stdout)
	}
	none := e.run("customize", "field", "list", "--doctype", "customer_group")
	if n := len(strings.Split(strings.TrimSuffix(none.stdout, "\n"), "\n")); n != 1 {
		t.Errorf("%s", none.stdout)
	}

	// The commands that read documents show the field.
	expectCode(t, e.doc("insert", "customer", "--data", `{"email": "a@x.test", "full_name": "A"}`), 0, "insert")
	e.d.Exec("UPDATE doc_customer SET custom_vip = true, custom_rank = 3")
	get := e.doc("get", "customer", "a@x.test")
	expectCode(t, get, 0, get.stderr)
	doc := jsonOf(t, get)
	if doc["custom_vip"] != true || doc["custom_rank"] != float64(3) {
		t.Errorf("%v", doc)
	}

	// Disable: hidden, column and data kept.
	disabled := e.run("customize", "field", "disable", "--doctype", "customer", "custom_vip")
	expectCode(t, disabled, 0, disabled.stderr)
	expectContains(t, disabled.stdout, "its column and data are kept")
	doc = jsonOf(t, e.doc("get", "customer", "a@x.test"))
	if _, there := doc["custom_vip"]; there || doc["custom_rank"] != float64(3) {
		t.Errorf("%v", doc)
	}
	if got := e.scalar("SELECT custom_vip::text FROM doc_customer"); got != "true" {
		t.Errorf("%s", got)
	}
	list = e.run("customize", "field", "list", "--doctype", "customer")
	found := false
	for _, l := range strings.Split(list.stdout, "\n") {
		if strings.Contains(l, "custom_vip") {
			for _, w := range strings.Fields(l) {
				found = found || w == "no"
			}
		}
	}
	if !found {
		t.Errorf("%s", list.stdout)
	}

	// Enable: shown again with its data.
	enabled := e.run("customize", "field", "enable", "--doctype", "customer", "custom_vip")
	expectCode(t, enabled, 0, enabled.stderr)
	doc = jsonOf(t, e.doc("get", "customer", "a@x.test"))
	if doc["custom_vip"] != true {
		t.Errorf("%v", doc)
	}
	if got := e.scalar("SELECT version::text FROM fb_meta_version"); got != "4" {
		t.Errorf("%s", got)
	}
}

// Rust: cli.rs::customize_field_refusals_change_nothing
func TestCustomizeFieldRefusalsChangeNothing(t *testing.T) {
	e := newEnv(t).migrated()
	add := func(doctype, text string) result {
		return e.runStdin(text, "customize", "field", "add", "--doctype", doctype, "--field", "-", "--doctypes", doctypes)
	}
	for _, c := range []struct{ doctype, text, expected string }{
		{"customer", "name = \"vip\"\ntype = \"check\"\n", "must start with `custom_`"},
		{"customer", "name = \"custom_a\"\ntype = \"check\"\nrequired = true\n", "cannot be required"},
		{"customer", "name = \"custom_a\"\ntype = \"check\"\nread_only = true\n", "cannot be read_only"},
		{"customer", "name = \"custom_a\"\ntype = \"table\"\ntarget = \"sales_order_item\"\n", "cannot add Table fields"},
		{"customer", "name = \"custom_a\"\ntype = \"check\"\ncolour = 1\n", "cannot parse the definition"},
		{"customer", "not toml at all", "cannot parse the definition"},
		{"sales_order_item", "name = \"custom_a\"\ntype = \"check\"\n", "child DocType"},
		{"ghost", "name = \"custom_a\"\ntype = \"check\"\n", "unknown doctype `ghost`"},
		{"customer", "name = \"email\"\ntype = \"check\"\n", "must start with `custom_`"},
	} {
		out := add(c.doctype, c.text)
		if out.code != 1 || !strings.Contains(out.stderr, c.expected) {
			t.Errorf("%s: %d %s", c.text, out.code, out.stderr)
		}
	}
	// A file that is not there is refused before the database is used.
	out := e.run("customize", "field", "add", "--doctype", "customer", "--field", "/no/such/file.toml", "--doctypes", doctypes)
	expectCode(t, out, 1, "a missing file")
	expectContains(t, out.stderr, "cannot read")
	// A duplicate, and a field that is not there.
	expectCode(t, add("customer", "name = \"custom_a\"\ntype = \"check\"\n"), 0, "the first add")
	again := add("customer", "name = \"custom_a\"\ntype = \"int\"\n")
	expectCode(t, again, 1, "a duplicate")
	expectContains(t, again.stderr, "already exists")
	missing := e.run("customize", "field", "disable", "--doctype", "customer", "custom_nope")
	expectCode(t, missing, 1, "a missing field")
	expectContains(t, missing.stderr, "not found")
	// Only the one good field was written.
	if got := e.scalar("SELECT count(*)::text FROM fb_custom_field"); got != "1" {
		t.Errorf("%s", got)
	}
	if got := e.scalar("SELECT version::text FROM fb_meta_version"); got != "1" {
		t.Errorf("%s", got)
	}
}

// Rust: cli.rs::the_data_and_doc_commands_see_enabled_custom_fields_and_refuse_disabled_ones
func TestTheDataAndDocCommandsSeeEnabledCustomFieldsAndRefuseDisabledOnes(t *testing.T) {
	e := newEnv(t).migrated()
	added := e.addField("-", "name = \"custom_vip\"\ntype = \"check\"\n")
	expectCode(t, added, 0, added.stderr)
	run := func(args ...string) result { return e.run(append(args, "--doctypes", doctypes)...) }

	// doc insert and update take the field, and doc get shows it.
	out := run("doc", "insert", "customer", "--data", `{"email":"a@x.test","full_name":"A","custom_vip":true}`)
	expectCode(t, out, 0, out.stderr)
	doc := jsonOf(t, out)
	if doc["custom_vip"] != true {
		t.Fatalf("%v", doc)
	}
	version := strings.TrimSuffix(strings.TrimSpace(mustMarshal(t, doc["version"])), "\n")
	out = run("doc", "update", "customer", "a@x.test", "--version", version, "--data", `{"custom_vip":false}`)
	expectCode(t, out, 0, out.stderr)
	out = run("doc", "list", "customer", "--filter", `[{"field":"custom_vip","op":"=","value":false}]`)
	expectCode(t, out, 0, out.stderr)
	var listed []any
	if err := json.Unmarshal([]byte(out.stdout), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("%v %s", err, out.stdout)
	}

	// A CSV export has the column, --fields and --filter work on it, and an import sets it.
	out = run("data", "export", "customer", "--format", "csv")
	expectCode(t, out, 0, out.stderr)
	if header := strings.SplitN(out.stdout, "\n", 2)[0]; !strings.Contains(header, "custom_vip") {
		t.Errorf("%s", out.stdout)
	}
	out = run("data", "export", "customer", "--format", "csv", "--fields", "name,custom_vip",
		"--filter", `[{"field":"custom_vip","op":"=","value":false}]`)
	expectCode(t, out, 0, out.stderr)
	if got := strings.ReplaceAll(out.stdout, "\r\n", "\n"); got != "name,custom_vip\na@x.test,false\n" {
		t.Errorf("%q", got)
	}
	csv := e.scratchFile("in.csv", "email,full_name,custom_vip\nb@x.test,B,true\n")
	out = run("data", "import", "customer", csv, "--format", "csv")
	expectCode(t, out, 0, out.stderr)
	if got := e.scalar("SELECT custom_vip::text FROM doc_customer WHERE name = 'b@x.test'"); got != "true" {
		t.Errorf("%s", got)
	}

	// Disabled: unknown in input, import, --fields and --filter; the column and data stay.
	off := e.run("customize", "field", "disable", "--doctype", "customer", "custom_vip")
	expectCode(t, off, 0, off.stderr)
	for _, args := range [][]string{
		{"doc", "insert", "customer", "--data", `{"email":"c@x.test","full_name":"C","custom_vip":true}`},
		{"doc", "list", "customer", "--filter", `[{"field":"custom_vip","op":"=","value":true}]`},
		{"data", "export", "customer", "--fields", "custom_vip"},
		{"data", "export", "customer", "--filter", `[{"field":"custom_vip","op":"=","value":true}]`},
		{"data", "import", "customer", csv, "--format", "csv", "--upsert"},
	} {
		out := run(args...)
		if out.code != 1 || !strings.Contains(out.stderr, "custom_vip") {
			t.Errorf("%v: %d %s %s", args, out.code, out.stdout, out.stderr)
		}
	}
	out = run("data", "export", "customer", "--format", "csv")
	if strings.Contains(out.stdout, "custom_vip") {
		t.Errorf("%s", out.stdout)
	}
	if got := e.scalar("SELECT count(*)::text FROM doc_customer WHERE custom_vip"); got != "1" {
		t.Errorf("%s", got)
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (e *env) permission(action, user, doctype, value string) result {
	args := []string{"user", "permission", action, "--user", user, "--doctype", doctype, "--value", value}
	// Only add checks the DocType against the DocType directory.
	if action == "add" {
		args = append(args, "--doctypes", doctypes)
	}
	return e.run(args...)
}

func (e *env) userWith(name, role string) {
	e.t.Helper()
	out := e.runStdin("a long enough password\n", "user", "create", name, "--role", role)
	expectCode(e.t, out, 0, out.stderr)
}

// Rust: cli.rs::user_permission_adds_lists_and_removes
func TestUserPermissionAddsListsAndRemoves(t *testing.T) {
	e := newEnv(t).migrated()
	e.userWith("alice", "Sales User")
	e.userWith("bob", "Sales User")
	for _, title := range []string{"retail", "wholesale"} {
		out := e.doc("insert", "customer_group", "--data", `{"title":"`+title+`"}`)
		expectCode(t, out, 0, out.stderr)
	}

	// Add: said once, then "already".
	out := e.permission("add", "alice", "customer_group", "retail")
	expectCode(t, out, 0, out.stderr)
	if !strings.HasPrefix(out.stdout, "restricted user alice to customer_group retail;") {
		t.Errorf("%s", out.stdout)
	}
	if out = e.permission("add", "alice", "customer_group", "retail"); out.stdout != "user alice is restricted to customer_group retail already\n" {
		t.Errorf("%s", out.stdout)
	}
	for _, c := range [][2]string{{"alice", "wholesale"}, {"bob", "retail"}} {
		expectCode(t, e.permission("add", c[0], "customer_group", c[1]), 0, "add "+c[0])
	}
	if got := e.scalar("SELECT count(*)::text FROM fb_user_permission"); got != "3" {
		t.Errorf("%s", got)
	}

	// List: a sorted table, filtered by user or DocType; empty is just the header.
	list := e.run("user", "permission", "list")
	expectCode(t, list, 0, list.stderr)
	var lines []string
	for _, l := range strings.Split(strings.TrimSuffix(list.stdout, "\n"), "\n") {
		lines = append(lines, strings.Join(strings.Fields(l)[:3], " "))
	}
	want := []string{"USER DOCTYPE VALUE", "alice customer_group retail", "alice customer_group wholesale", "bob customer_group retail"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("%s", list.stdout)
	}
	list = e.run("user", "permission", "list", "--user", "bob")
	if n := strings.Count(list.stdout, "\n"); n != 2 {
		t.Errorf("%s", list.stdout)
	}
	list = e.run("user", "permission", "list", "--doctype", "customer")
	if n := strings.Count(list.stdout, "\n"); n != 1 {
		t.Errorf("%s", list.stdout)
	}

	// Remove: said once, then "not restricted".
	out = e.permission("remove", "alice", "customer_group", "retail")
	if !strings.HasPrefix(out.stdout, "removed the restriction of user alice to customer_group retail;") {
		t.Errorf("%s", out.stdout)
	}
	out = e.permission("remove", "alice", "customer_group", "retail")
	expectCode(t, out, 0, "remove again")
	if out.stdout != "user alice is not restricted to customer_group retail\n" {
		t.Errorf("%s", out.stdout)
	}
	if got := e.scalar("SELECT count(*)::text FROM fb_user_permission"); got != "2" {
		t.Errorf("%s", got)
	}
}

// Rust: cli.rs::user_permission_refusals_change_nothing
func TestUserPermissionRefusalsChangeNothing(t *testing.T) {
	e := newEnv(t).migrated()
	e.userWith("alice", "Sales User")
	out := e.doc("insert", "customer_group", "--data", `{"title":"retail"}`)
	expectCode(t, out, 0, out.stderr)

	for _, c := range []struct {
		args     [4]string
		expected string
	}{
		{[4]string{"add", "nobody", "customer_group", "retail"}, "user `nobody` not found"},
		{[4]string{"add", "alice", "ghost", "x"}, "invalid user permission: unknown DocType `ghost`"},
		{[4]string{"add", "alice", "customer_group", "missing"}, "no `customer_group` document named `missing`"},
		{[4]string{"add", "alice", "customer_group", ""}, "the value must be a document name"},
		{[4]string{"add", "bad name", "customer_group", "retail"}, "invalid username"},
		{[4]string{"remove", "nobody", "customer_group", "retail"}, "user `nobody` not found"},
	} {
		out := e.permission(c.args[0], c.args[1], c.args[2], c.args[3])
		if out.code != 1 || !strings.Contains(out.stderr, c.expected) {
			t.Errorf("%v: %d %s %s", c.args, out.code, out.stdout, out.stderr)
		}
	}
	// A child DocType cannot be restricted.
	out = e.run("user", "permission", "add", "--user", "alice", "--doctype", "sales_order_item", "--value", "x", "--doctypes", doctypes)
	expectCode(t, out, 1, "a child DocType")
	expectContains(t, out.stderr, "child DocType")
	// An unknown user in list is an error too.
	expectCode(t, e.run("user", "permission", "list", "--user", "nobody"), 1, "list of an unknown user")
	// A missing option is a usage error.
	expectCode(t, e.run("user", "permission", "add", "--user", "alice"), 2, "a missing option")
	if got := e.scalar("SELECT count(*)::text FROM fb_user_permission"); got != "0" {
		t.Errorf("%s", got)
	}

	// The limit of 1000 documents per user and DocType, from the command line.
	e.d.Exec("INSERT INTO fb_user_permission (user_id, doctype, value) " +
		"SELECT u.id, 'customer_group', 'n' || g FROM fb_user u, generate_series(1, 1000) g")
	out = e.run("user", "permission", "add", "--user", "alice", "--doctype", "customer_group", "--value", "retail", "--doctypes", doctypes)
	expectCode(t, out, 1, "the limit")
	expectContains(t, out.stderr, "1000", "customer_group")
}

// Rust: customize_cmd.rs::tests::the_list_shows_one_line_per_field
func TestTheCustomFieldListShowsOneLinePerField(t *testing.T) {
	field := &db.CustomField{
		DocType:    "customer",
		Name:       "custom_vip",
		Definition: json.RawMessage(`{"name": "custom_vip", "label": "VIP", "type": "check"}`),
		Enabled:    false,
		CreatedAt:  time.Unix(0, 0),
		ModifiedAt: time.Unix(0, 0),
		ModifiedBy: "cli",
	}
	text := customFieldTable([]*db.CustomField{field})
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "DOCTYPE") {
		t.Fatalf("%s", text)
	}
	for _, part := range []string{"custom_vip", "check", "VIP", "no", "1970-01-01T00:00:00Z"} {
		if !strings.Contains(lines[1], part) {
			t.Errorf("%q is missing in %s", part, text)
		}
	}
}

// Rust: user_permission_cmd.rs::tests::the_list_shows_one_line_per_row_under_a_header
func TestTheUserPermissionListShowsOneLinePerRowUnderAHeader(t *testing.T) {
	row := db.UserPermission{User: "ann", DocType: "customer_group", Value: "Retail", CreatedAt: time.Unix(0, 0)}
	text := userPermissionTable([]db.UserPermission{row})
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "USER") {
		t.Fatalf("%s", text)
	}
	if lines[1] != "ann   customer_group  Retail  1970-01-01T00:00:00Z" {
		t.Errorf("%q", lines[1])
	}
	if n := strings.Count(userPermissionTable(nil), "\n"); n != 1 {
		t.Errorf("%d lines", n)
	}
}
