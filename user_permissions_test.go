package forgebase_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// User permissions end to end (M10.3, ADR 0025), over HTTP with forgebase/testing: the Administrator-only API
// (add, list, remove, validation, the limit, CSRF), that a change applies on the user's next request, that a
// method reads as its caller, that two sites keep their own rows, and that a restricted user's requests (get,
// list, summary, export, a write that links to a hidden target) see only what the rows allow. Ported from
// crates/forgebase/tests/user_permissions.rs.

const upGroup = `
name = "customer_group"
label = "Customer Group"
naming = { field = "title" }
[[fields]]
name = "title"
type = "data"
required = true
unique = true
[[permissions]]
role = "Sales"
read = true
write = true
create = true
export = true
`

const upCustomerDT = `
name = "customer"
label = "Customer"
naming = { field = "title" }
[[fields]]
name = "title"
type = "data"
required = true
unique = true
[[fields]]
name = "customer_group"
type = "link"
target = "customer_group"
[[permissions]]
role = "Sales"
read = true
write = true
create = true
delete = true
export = true
`

// upCustomers is crm.customers: the names of the customers the caller can list, in order.
func upCustomers(ctx context.Context, m *forgebase.MethodContext) (any, error) {
	found, err := m.Documents().List(ctx, "customer", forgebase.NewListQuery().OrderBy("title", false))
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, d := range found {
		names = append(names, d.Name)
	}
	return names, nil
}

// upCustomer is crm.customer: whether the caller can get the customer named by title.
func upCustomer(ctx context.Context, m *forgebase.MethodContext) (any, error) {
	var body struct {
		Title string `json:"title"`
	}
	_ = m.DecodeBody(&body)
	found, err := m.Documents().Get(ctx, "customer", body.Title)
	if err != nil {
		return nil, err
	}
	return map[string]any{"found": found != nil}, nil
}

func upBinary() *forgebase.Binary {
	return forgebase.New().App(forgebase.NewApp("crm").
		DocTypes(docTypes("doctypes/customer_group.toml", upGroup, "doctypes/customer.toml", upCustomerDT)).
		Method("customers", forgebase.NewMethod(upCustomers).Roles("Sales")).
		Method("customer", forgebase.NewMethod(upCustomer).Roles("Sales")))
}

// upSite is a migrated site with ann and pat (Sales), bob (no role that reads) and root (Administrator), two
// groups and three customers: c_retail, c_whole and c_none (the group Retail, Wholesale and none).
func upSite(t *testing.T, binary *forgebase.Binary) *fbtest.DB {
	t.Helper()
	db := fbtest.NewDB(t)
	if _, err := db.Migrate(binary, false); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][2]string{{"ann", "Sales"}, {"pat", "Sales"}, {"bob", "Stranger"}, {"root", "Administrator"}} {
		db.CreateUser(u[0], u[1])
	}
	root := db.Client(binary).Login("root")
	for _, group := range []string{"Retail", "Wholesale"} {
		cuStatus(t, root.Post("/api/v1/resource/customer_group", map[string]any{"title": group}), 201)
	}
	for _, c := range [][2]string{{"c_retail", "Retail"}, {"c_whole", "Wholesale"}, {"c_none", ""}} {
		body := map[string]any{"title": c[0]}
		if c[1] != "" {
			body["customer_group"] = c[1]
		}
		cuStatus(t, root.Post("/api/v1/resource/customer", body), 201)
	}
	return db
}

func upNames(t *testing.T, reply *fbtest.Reply) []string {
	t.Helper()
	cuStatus(t, reply, 200)
	names := []string{}
	for _, d := range reply.Data().([]any) {
		names = append(names, d.(map[string]any)["name"].(string))
	}
	sort.Strings(names)
	return names
}

func upBody(user, doctype, value string) map[string]any {
	return map[string]any{"user": user, "doctype": doctype, "value": value}
}

func upAdd(client *fbtest.Client, user, doctype, value string) int {
	return client.Post("/api/v1/user-permissions", upBody(user, doctype, value)).Status
}

// Rust: user_permissions.rs::the_api_is_for_administrators_and_needs_the_csrf_token
func TestTheUserPermissionAPIIsForAdministratorsAndNeedsTheCSRFToken(t *testing.T) {
	binary := upBinary()
	db := upSite(t, binary)
	server := db.Client(binary)
	root := server.Login("root")

	// A non-administrator, with the rights on the DocType or not, gets 403 on every route; the cookie of an
	// administrator without the CSRF header gets 403 csrf_failed; no credentials 401.
	for _, user := range []string{"ann", "bob"} {
		client := server.Login(user)
		list := client.Get("/api/v1/user-permissions")
		if list.Status != 403 || list.ErrorCode() != "permission_denied" {
			t.Errorf("%s: %d %v", user, list.Status, list.Body)
		}
		add := client.Post("/api/v1/user-permissions", upBody("ann", "customer_group", "Retail"))
		if add.Status != 403 || add.ErrorCode() != "permission_denied" {
			t.Errorf("%s: %d %v", user, add.Status, add.Body)
		}
		if remove := client.Post("/api/v1/user-permissions/remove", upBody("ann", "customer_group", "Retail")); remove.Status != 403 {
			t.Errorf("%s: %d", user, remove.Status)
		}
	}
	for _, path := range []string{"/api/v1/user-permissions", "/api/v1/user-permissions/remove"} {
		reply := root.WithoutCSRF().Post(path, upBody("ann", "customer_group", "Retail"))
		if reply.Status != 403 || reply.ErrorCode() != "csrf_failed" {
			t.Errorf("%s: %d %v", path, reply.Status, reply.Body)
		}
	}
	cuStatus(t, server.Get("/api/v1/user-permissions"), 401)
	// None of that changed anything.
	if n := len(root.Get("/api/v1/user-permissions").Data().([]any)); n != 0 {
		t.Fatalf("%d rows", n)
	}

	// An API token of an administrator works like a session, without CSRF.
	byToken := db.Client(binary).WithToken(db.CreateToken("root"))
	if s := upAdd(byToken, "ann", "customer_group", "Retail"); s != 201 {
		t.Fatalf("%d", s)
	}
}

// Rust: user_permissions.rs::add_list_and_remove_with_their_validation
func TestAddListAndRemoveUserPermissionsWithTheirValidation(t *testing.T) {
	binary := upBinary()
	db := upSite(t, binary)
	root := db.Client(binary).Login("root")

	created := root.Post("/api/v1/user-permissions", upBody("ann", "customer_group", "Retail"))
	cuStatus(t, created, 201)
	expect(t, created.Data(), map[string]any{"user": "ann", "doctype": "customer_group", "value": "Retail", "added": true})
	// Adding it again succeeds and says it was there.
	again := root.Post("/api/v1/user-permissions", upBody("ann", "customer_group", "Retail"))
	cuStatus(t, again, 200)
	if again.DataObject()["added"] != false {
		t.Fatalf("%v", again.Body)
	}
	for _, c := range [][3]string{{"ann", "customer", "c_none"}, {"pat", "customer_group", "Wholesale"}} {
		if s := upAdd(root, c[0], c[1], c[2]); s != 201 {
			t.Fatalf("%v: %d", c, s)
		}
	}

	// The list: sorted, and filtered by user and DocType.
	rows := func(reply *fbtest.Reply) []string {
		t.Helper()
		cuStatus(t, reply, 200)
		out := []string{}
		for _, r := range reply.Data().([]any) {
			m := r.(map[string]any)
			out = append(out, fmt.Sprintf("%s %s %s", m["user"], m["doctype"], m["value"]))
		}
		return out
	}
	expect(t, rows(root.Get("/api/v1/user-permissions")),
		[]string{"ann customer c_none", "ann customer_group Retail", "pat customer_group Wholesale"})
	expect(t, rows(root.Get("/api/v1/user-permissions?user=ann&doctype=customer_group")), []string{"ann customer_group Retail"})
	expect(t, rows(root.Get("/api/v1/user-permissions?doctype=customer")), []string{"ann customer c_none"})
	first := root.Get("/api/v1/user-permissions?user=pat")
	if !strings.HasSuffix(first.Data().([]any)[0].(map[string]any)["created_at"].(string), "Z") {
		t.Fatalf("%v", first.Body)
	}
	cuStatus(t, root.Get("/api/v1/user-permissions?user=nobody"), 404)
	cuStatus(t, root.Get("/api/v1/user-permissions?bogus=1"), 400)

	// Validation, with nothing written.
	for _, c := range []struct {
		name, user, doctype, value string
		status                     int
	}{
		{"unknown user", "nobody", "customer_group", "Retail", 404},
		{"bad username", "bad name", "customer_group", "Retail", 422},
		{"unknown DocType", "ann", "ghost", "x", 422},
		{"missing document", "ann", "customer_group", "Nowhere", 422},
		{"empty value", "ann", "customer_group", "", 422},
	} {
		reply := root.Post("/api/v1/user-permissions", upBody(c.user, c.doctype, c.value))
		if reply.Status != c.status {
			t.Errorf("%s: %d %v", c.name, reply.Status, reply.Body)
		}
	}
	for _, bad := range []any{
		map[string]any{"user": "ann", "doctype": "customer"},
		map[string]any{"user": "ann", "doctype": "customer", "value": 3},
		map[string]any{"user": "ann", "doctype": "customer", "value": "c_none", "extra": 1},
		[]any{},
	} {
		if reply := root.Post("/api/v1/user-permissions", bad); reply.Status != 400 {
			t.Errorf("%v: %d %v", bad, reply.Status, reply.Body)
		}
	}
	if n := len(rows(root.Get("/api/v1/user-permissions"))); n != 3 {
		t.Fatalf("%d rows: nothing was written by the refusals", n)
	}

	// Remove: once true, then false; an unknown user is 404.
	removed := root.Post("/api/v1/user-permissions/remove", upBody("ann", "customer", "c_none"))
	cuStatus(t, removed, 200)
	if removed.DataObject()["removed"] != true {
		t.Fatalf("%v", removed.Body)
	}
	removed = root.Post("/api/v1/user-permissions/remove", upBody("ann", "customer", "c_none"))
	if removed.DataObject()["removed"] != false {
		t.Fatalf("%v", removed.Body)
	}
	cuStatus(t, root.Post("/api/v1/user-permissions/remove", upBody("nobody", "customer", "c_none")), 404)
}

// Rust: user_permissions.rs::a_user_can_hold_at_most_a_thousand_documents_per_doctype
func TestAUserCanHoldAtMostAThousandDocumentsPerDocTypeOverHTTP(t *testing.T) {
	binary := upBinary()
	db := upSite(t, binary)
	root := db.Client(binary).Login("root")
	db.Exec("INSERT INTO fb_user_permission (user_id, doctype, value) " +
		"SELECT u.id, 'customer_group', 'n' || g FROM fb_user u, generate_series(1, 999) g WHERE u.username = 'ann'")
	if s := upAdd(root, "ann", "customer_group", "Retail"); s != 201 {
		t.Fatalf("%d", s)
	}
	refused := root.Post("/api/v1/user-permissions", upBody("ann", "customer_group", "Wholesale"))
	cuStatus(t, refused, 422)
	if !strings.Contains(refused.Text, "1000") {
		t.Fatalf("%s", refused.Text)
	}
	// The same row again and another DocType are fine.
	if s := upAdd(root, "ann", "customer_group", "Retail"); s != 200 {
		t.Fatalf("%d", s)
	}
	if s := upAdd(root, "ann", "customer", "c_none"); s != 201 {
		t.Fatalf("%d", s)
	}
}

// Rust: user_permissions.rs::a_change_applies_on_the_users_next_request
func TestAUserPermissionChangeAppliesOnTheUsersNextRequest(t *testing.T) {
	binary := upBinary()
	db := upSite(t, binary)
	server := db.Client(binary)
	root := server.Login("root")
	// One session of Ann, kept open across every change below.
	ann := server.Login("ann")
	list := func(client *fbtest.Client) []string {
		return upNames(t, client.Get("/api/v1/resource/customer?limit=100"))
	}
	expect(t, list(ann), []string{"c_none", "c_retail", "c_whole"})

	if s := upAdd(root, "ann", "customer_group", "Retail"); s != 201 {
		t.Fatalf("%d", s)
	}
	expect(t, list(ann), []string{"c_none", "c_retail"})
	// The documents she cannot see are 404, exactly as a missing one.
	hidden := ann.Get("/api/v1/resource/customer/c_whole")
	missing := ann.Get("/api/v1/resource/customer/c_nowhere")
	if hidden.Status != 404 || missing.Status != 404 {
		t.Fatalf("%d %d", hidden.Status, missing.Status)
	}
	expect(t, hidden.Body.(map[string]any)["error"].(map[string]any)["code"], missing.Body.(map[string]any)["error"].(map[string]any)["code"])
	cuStatus(t, ann.Get("/api/v1/resource/customer/c_retail"), 200)

	// Allowing a second group, then none: the next request sees it, with no reload.
	if s := upAdd(root, "ann", "customer_group", "Wholesale"); s != 201 {
		t.Fatalf("%d", s)
	}
	expect(t, list(ann), []string{"c_none", "c_retail", "c_whole"})
	for _, group := range []string{"Retail", "Wholesale"} {
		cuStatus(t, root.Post("/api/v1/user-permissions/remove", upBody("ann", "customer_group", group)), 200)
	}
	expect(t, list(ann), []string{"c_none", "c_retail", "c_whole"})
	// The same through an API token.
	byToken := server.WithToken(db.CreateToken("ann"))
	if s := upAdd(root, "ann", "customer_group", "Retail"); s != 201 {
		t.Fatalf("%d", s)
	}
	expect(t, list(byToken), []string{"c_none", "c_retail"})
}

// Rust: user_permissions.rs::a_restricted_user_sees_the_allowed_documents_on_every_route
func TestARestrictedUserSeesTheAllowedDocumentsOnEveryRoute(t *testing.T) {
	binary := upBinary()
	db := upSite(t, binary)
	server := db.Client(binary)
	root := server.Login("root")
	if s := upAdd(root, "ann", "customer_group", "Retail"); s != 201 {
		t.Fatalf("%d", s)
	}
	ann, pat := server.Login("ann"), server.Login("pat")

	// The restricted DocType, list and get.
	expect(t, upNames(t, ann.Get("/api/v1/resource/customer_group")), []string{"Retail"})
	expect(t, upNames(t, pat.Get("/api/v1/resource/customer_group")), []string{"Retail", "Wholesale"})
	cuStatus(t, ann.Get("/api/v1/resource/customer_group/Wholesale"), 404)
	// The group-by summary counts only visible customers.
	summary := ann.Get("/api/v1/summary/customer?group_by=customer_group")
	cuStatus(t, summary, 200)
	total := 0.0
	for _, g := range summary.DataObject()["groups"].([]any) {
		total += g.(map[string]any)["count"].(float64)
	}
	if total != 2 {
		t.Fatalf("%v", summary.Body)
	}
	// The export carries only the visible rows.
	exported := ann.Get("/api/v1/export/customer?format=json")
	cuStatus(t, exported, 200)
	var names []string
	for _, r := range exported.Body.([]any) {
		names = append(names, r.(map[string]any)["name"].(string))
	}
	if cuHas(names, "c_whole") || !cuHas(names, "c_retail") {
		t.Fatalf("%v", exported.Body)
	}

	// A write that links to a hidden target is a missing target, the same as a name that does not exist;
	// creating in the restricted DocType is refused.
	hidden := ann.Post("/api/v1/resource/customer", map[string]any{"title": "n1", "customer_group": "Wholesale"})
	missing := ann.Post("/api/v1/resource/customer", map[string]any{"title": "n2", "customer_group": "Nowhere"})
	if hidden.Status != 422 || missing.Status != 422 {
		t.Fatalf("%d %d %v", hidden.Status, missing.Status, hidden.Body)
	}
	if strings.ReplaceAll(hidden.Text, "Wholesale", "N") != strings.ReplaceAll(missing.Text, "Nowhere", "N") {
		t.Fatalf("%s\n%s", hidden.Text, missing.Text)
	}
	cuStatus(t, ann.Post("/api/v1/resource/customer", map[string]any{"title": "n3", "customer_group": "Retail"}), 201)
	create := ann.Post("/api/v1/resource/customer_group", map[string]any{"title": "Online"})
	if create.Status != 403 || create.ErrorCode() != "permission_denied" {
		t.Fatalf("%d %v", create.Status, create.Body)
	}
	// The Administrator is not restricted whatever rows there are.
	if s := upAdd(root, "root", "customer_group", "Retail"); s != 201 {
		t.Fatalf("%d", s)
	}
	root = server.Login("root")
	expect(t, upNames(t, root.Get("/api/v1/resource/customer_group")), []string{"Retail", "Wholesale"})
}

// Rust: user_permissions.rs::a_method_reads_as_the_caller_and_inherits_the_restriction
func TestAMethodReadsAsTheCallerAndInheritsTheRestriction(t *testing.T) {
	binary := upBinary()
	db := upSite(t, binary)
	server := db.Client(binary)
	root := server.Login("root")
	if s := upAdd(root, "ann", "customer_group", "Retail"); s != 201 {
		t.Fatalf("%d", s)
	}
	ann, pat := server.Login("ann"), server.Login("pat")

	customers := func(client *fbtest.Client) string {
		t.Helper()
		reply := client.Post("/api/v1/method/crm.customers", map[string]any{})
		cuStatus(t, reply, 200)
		text, _ := json.Marshal(reply.Data())
		return string(text)
	}
	expect(t, customers(ann), `["c_none","c_retail"]`)
	expect(t, customers(pat), `["c_none","c_retail","c_whole"]`)
	found := func(client *fbtest.Client, title string) any {
		return client.Post("/api/v1/method/crm.customer", map[string]any{"title": title}).DataObject()["found"]
	}
	expect(t, found(ann, "c_retail"), true)
	expect(t, found(ann, "c_whole"), false)
	expect(t, found(pat, "c_whole"), true)
}

// Rust: user_permissions.rs::two_sites_keep_their_own_permissions (crates/forgebase/tests, over HTTP)
func TestTwoSitesKeepTheirOwnUserPermissionsOverHTTP(t *testing.T) {
	binary := upBinary()
	one, two := upSite(t, binary), upSite(t, binary)
	rootOne := one.Client(binary).Login("root")
	if s := upAdd(rootOne, "ann", "customer_group", "Retail"); s != 201 {
		t.Fatalf("%d", s)
	}
	annOne, annTwo := one.Client(binary).Login("ann"), two.Client(binary).Login("ann")
	expect(t, upNames(t, annOne.Get("/api/v1/resource/customer?limit=100")), []string{"c_none", "c_retail"})
	expect(t, upNames(t, annTwo.Get("/api/v1/resource/customer?limit=100")), []string{"c_none", "c_retail", "c_whole"})
	rootTwo := two.Client(binary).Login("root")
	if n := len(rootTwo.Get("/api/v1/user-permissions").Data().([]any)); n != 0 {
		t.Fatalf("%d rows", n)
	}
}
