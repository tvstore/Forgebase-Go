package db_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Saved list views (M8.3) against a real PostgreSQL: privacy, the Administrator rule for shared views,
// validation of the definition, labels, limits, versions and the cascade with the user. Ported from
// crates/forgebase-db/tests/views.rs.

const viewOrder = `
name = "order"
label = "Order"
submittable = true
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "status"
type = "select"
options = ["draft", "open"]
[[fields]]
name = "note"
type = "text"
[[fields]]
name = "qty"
type = "int"
[[fields]]
name = "lines"
type = "table"
target = "order_line"

[[permissions]]
role = "Clerk"
read = true
write = true
create = true
[[permissions]]
role = "Own"
read = true
if_owner = true
`

const viewLine = `
name = "order_line"
label = "Order Line"
is_child = true
[[fields]]
name = "qty"
type = "int"
`

const viewOther = `
name = "other"
label = "Other"
[[fields]]
name = "title"
type = "data"
[[permissions]]
role = "Clerk"
read = true
`

func viewSetup(t *testing.T) (*dbtest.DB, *core.Registry) {
	t.Helper()
	d := dbtest.Migrated(t)
	for _, name := range []string{"ann", "bob", "root"} {
		d.Exec(fmt.Sprintf("INSERT INTO fb_user (id, username, password_hash) VALUES (gen_random_uuid(), '%s', 'x')", name))
	}
	return d, registry(t, viewOrder, viewLine, viewOther)
}

func vAnn() core.Caller  { return user("ann", "Clerk") }
func vBob() core.Caller  { return user("bob", "Clerk") }
func vRoot() core.Caller { return user("root", core.Administrator) }

// viewDef is a definition with the given JSON text and parsed query.
func viewDef(jsonText string, q db.ListQuery) *db.ViewDefinition {
	return &db.ViewDefinition{JSON: json.RawMessage(jsonText), Query: q}
}

// def is a definition with one filter (status = draft), one sort and the given columns.
func def(columns ...string) *db.ViewDefinition {
	cols, _ := json.Marshal(columns)
	if columns == nil {
		cols = []byte("[]")
	}
	return viewDef(`{"filters":[{"field":"status","op":"=","value":"draft"}],"sort":["title:desc"],"columns":`+string(cols)+`}`,
		db.ListQuery{Filters: []db.Filter{{Field: "status", Op: db.Eq(text("draft"))}}, Sort: []db.Sort{db.Desc("title")}})
}

func emptyDef() *db.ViewDefinition {
	return viewDef(`{"filters":[],"sort":[],"columns":[]}`, db.ListQuery{})
}

func createView(d *dbtest.DB, reg *core.Registry, caller core.Caller, label string, shared bool) (*db.View, error) {
	return db.CreateView(bg, d.Pool, reg, "order", caller, label, def(), shared)
}

func viewLabels(t *testing.T, d *dbtest.DB, reg *core.Registry, caller core.Caller) []string {
	t.Helper()
	views, err := db.ListViews(bg, d.Pool, reg, "order", caller)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, v := range views {
		out = append(out, v.Label)
	}
	return out
}

func updateView(d *dbtest.DB, reg *core.Registry, caller core.Caller, id uuid.UUID, version int64, changes db.ViewChanges) (*db.View, error) {
	return db.UpdateView(bg, d.Pool, reg, "order", id, version, caller, changes)
}

func deleteView(d *dbtest.DB, reg *core.Registry, caller core.Caller, id uuid.UUID, version int64) error {
	return db.DeleteView(bg, d.Pool, reg, "order", id, version, caller)
}

func renamed(label string) db.ViewChanges { return db.ViewChanges{Label: &label} }

func mustCreateView(t *testing.T, d *dbtest.DB, reg *core.Registry, caller core.Caller, label string, shared bool) *db.View {
	t.Helper()
	v, err := createView(d, reg, caller, label, shared)
	if err != nil {
		t.Fatalf("create %q: %v", label, err)
	}
	return v
}

func invalidView(t *testing.T, err error) string {
	t.Helper()
	return wantKind(t, err, db.ErrInvalidView).Message
}

func sameJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("definition %s, want %s", got, want)
	}
}

// Rust: views.rs::a_private_view_can_be_created_listed_updated_and_deleted
func TestAPrivateViewCanBeCreatedListedUpdatedAndDeleted(t *testing.T) {
	d, reg := viewSetup(t)
	view := mustCreateView(t, d, reg, vAnn(), "Open orders", false)
	if view.Label != "Open orders" || view.Owner != "ann" || view.Shared() || view.Version != 1 || view.DocType != "order" {
		t.Fatalf("%+v", view)
	}
	sameJSON(t, view.Definition, string(def().JSON))

	listed, err := db.ListViews(bg, d.Pool, reg, "order", vAnn())
	if err != nil || len(listed) != 1 || listed[0].ID != view.ID || listed[0].Label != view.Label {
		t.Fatal(listed, err)
	}

	next := def("title", "qty")
	changed, err := updateView(d, reg, vAnn(), view.ID, 1, db.ViewChanges{Label: ptr("Drafts"), Definition: next})
	if err != nil || changed.Label != "Drafts" || changed.Version != 2 {
		t.Fatal(changed, err)
	}
	sameJSON(t, changed.Definition, string(next.JSON))
	if changed.ModifiedAt.Before(view.ModifiedAt) {
		t.Fatal("modified_at went back")
	}

	// Only the label changes: the definition is kept.
	again, err := updateView(d, reg, vAnn(), view.ID, 2, renamed("Drafts 2"))
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, again.Definition, string(next.JSON))

	if err := deleteView(d, reg, vAnn(), view.ID, 3); err != nil {
		t.Fatal(err)
	}
	if got := viewLabels(t, d, reg, vAnn()); len(got) != 0 {
		t.Fatal(got)
	}
	wantKind(t, deleteView(d, reg, vAnn(), view.ID, 3), db.ErrNotFound)
}

func ptr[T any](v T) *T { return &v }

// Rust: views.rs::another_users_private_view_is_invisible_and_cannot_be_changed
func TestAnotherUsersPrivateViewIsInvisibleAndCannotBeChanged(t *testing.T) {
	d, reg := viewSetup(t)
	view := mustCreateView(t, d, reg, vAnn(), "Mine", false)

	// Not listed for bob, nor for an Administrator.
	for _, c := range []core.Caller{vBob(), vRoot(), sys("cli")} {
		if got := viewLabels(t, d, reg, c); len(got) != 0 {
			t.Fatal(got)
		}
	}
	// NotFound, not a refusal, and before the version is compared.
	for _, caller := range []core.Caller{vBob(), vRoot(), sys("cli")} {
		for _, version := range []int64{1, 99} {
			_, err := updateView(d, reg, caller, view.ID, version, renamed("Stolen"))
			wantKind(t, err, db.ErrNotFound)
			wantKind(t, deleteView(d, reg, caller, view.ID, version), db.ErrNotFound)
		}
	}
	// Unchanged.
	strsEqual(t, viewLabels(t, d, reg, vAnn()), []string{"Mine"})
	// The same label is free for another user.
	mustCreateView(t, d, reg, vBob(), "Mine", false)
}

// Rust: views.rs::a_view_of_another_doctype_is_not_found
func TestAViewOfAnotherDoctypeIsNotFound(t *testing.T) {
	d, reg := viewSetup(t)
	view := mustCreateView(t, d, reg, vAnn(), "Mine", false)
	wantKind(t, db.DeleteView(bg, d.Pool, reg, "other", view.ID, 1, vAnn()), db.ErrNotFound)
}

// Rust: views.rs::only_an_administrator_creates_and_changes_shared_views
func TestOnlyAnAdministratorCreatesAndChangesSharedViews(t *testing.T) {
	d, reg := viewSetup(t)
	_, err := createView(d, reg, vAnn(), "Team", true)
	wantKind(t, err, db.ErrViewDenied)
	// The denial does not depend on the definition.
	_, err = db.CreateView(bg, d.Pool, reg, "order", vAnn(), "", emptyDef(), true)
	wantKind(t, err, db.ErrViewDenied)

	shared := mustCreateView(t, d, reg, vRoot(), "Team", true)
	if !shared.Shared() || shared.Owner != "" {
		t.Fatalf("%+v", shared)
	}
	// Everyone with read sees it, next to their own; shared views come first.
	mustCreateView(t, d, reg, vAnn(), "A mine", false)
	strsEqual(t, viewLabels(t, d, reg, vAnn()), []string{"Team", "A mine"})
	strsEqual(t, viewLabels(t, d, reg, vBob()), []string{"Team"})
	strsEqual(t, viewLabels(t, d, reg, user("zed", "Own")), []string{"Team"})

	// A non-administrator can see it and so is told no, not "not found".
	_, err = updateView(d, reg, vAnn(), shared.ID, 1, renamed("Mine now"))
	wantKind(t, err, db.ErrViewDenied)
	wantKind(t, deleteView(d, reg, vAnn(), shared.ID, 1), db.ErrViewDenied)

	// An Administrator and the system context may change and delete it.
	changed, err := updateView(d, reg, vRoot(), shared.ID, 1, renamed("Mine now"))
	if err != nil || changed.Version != 2 {
		t.Fatal(changed, err)
	}
	cli := sys("cli")
	bySystem := mustCreateView(t, d, reg, cli, "From the CLI", true)
	if err := deleteView(d, reg, cli, bySystem.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := deleteView(d, reg, vRoot(), shared.ID, 2); err != nil {
		t.Fatal(err)
	}
	if got := viewLabels(t, d, reg, vBob()); len(got) != 0 {
		t.Fatal(got)
	}
}

// Rust: views.rs::the_system_context_cannot_own_a_private_view
func TestTheSystemContextCannotOwnAPrivateView(t *testing.T) {
	d, reg := viewSetup(t)
	_, err := createView(d, reg, sys("cli"), "x", false)
	wantKind(t, err, db.ErrViewDenied)
}

// Rust: views.rs::the_read_right_on_the_doctype_is_needed
func TestTheReadRightOnTheDoctypeIsNeededForViews(t *testing.T) {
	d, reg := viewSetup(t)
	nobody := user("bob", "Nobody")
	_, err := db.ListViews(bg, d.Pool, reg, "order", nobody)
	wantKind(t, err, db.ErrPermissionDenied)
	_, err = createView(d, reg, nobody, "x", false)
	wantKind(t, err, db.ErrPermissionDenied)
	// Update and delete are refused the same way, before any view is looked up.
	view := mustCreateView(t, d, reg, vBob(), "x", false)
	wantKind(t, deleteView(d, reg, nobody, view.ID, 1), db.ErrPermissionDenied)
	// An unknown and a child DocType.
	_, err = db.ListViews(bg, d.Pool, reg, "nope", vAnn())
	wantKind(t, err, db.ErrUnknownDocType)
	_, err = db.ListViews(bg, d.Pool, reg, "order_line", vAnn())
	wantKind(t, err, db.ErrChildDocType)
}

// Rust: views.rs::the_definition_is_validated_like_a_list_query
func TestTheDefinitionIsValidatedLikeAListQuery(t *testing.T) {
	d, reg := viewSetup(t)
	create := func(definition *db.ViewDefinition) error {
		_, err := db.CreateView(bg, d.Pool, reg, "order", vAnn(), "v", definition, false)
		return err
	}
	one := func(field string) string { return fmt.Sprintf(`[{"field":%q,"op":"is null"}]`, field) }

	// An unknown field, a table field and a wrong operator in a filter.
	for _, c := range []struct {
		field  string
		op     db.FilterOp
		needle string
	}{
		{"nope", db.IsNull(true), "unknown field"},
		{"lines", db.IsNull(true), "table field"},
		{"qty", db.Like("1%"), "text fields only"},
	} {
		message := invalidView(t, create(viewDef(`{"filters":`+one(c.field)+`,"sort":[],"columns":[]}`,
			db.ListQuery{Filters: []db.Filter{{Field: c.field, Op: c.op}}})))
		if !strings.Contains(message, c.needle) {
			t.Errorf("%s: %s", c.field, message)
		}
	}
	// A bad sort.
	message := invalidView(t, create(viewDef(`{"filters":[],"sort":["nope"],"columns":[]}`, db.ListQuery{Sort: []db.Sort{db.Asc("nope")}})))
	if !strings.Contains(message, "unknown field") {
		t.Fatal(message)
	}
	// The JSON and its parsed form must agree.
	message = invalidView(t, create(viewDef(`{"filters":`+one("qty")+`,"sort":[],"columns":[]}`, db.ListQuery{})))
	if !strings.Contains(message, "do not match") {
		t.Fatal(message)
	}

	// Columns: unknown, Table, Text, duplicates, too many, standard columns that do not exist.
	for _, c := range [][2]string{
		{`["nope"]`, "unknown column"}, {`["lines"]`, "table field"}, {`["note"]`, "text field"}, {`["title","title"]`, "twice"},
		{`["workflow_state"]`, "unknown column"}, {`["version"]`, "unknown column"}, {`[1]`, "list of strings"}, {`"title"`, "must be a list"},
	} {
		message := invalidView(t, create(viewDef(`{"filters":[],"sort":[],"columns":`+c[0]+`}`, db.ListQuery{})))
		if !strings.Contains(message, c[1]) {
			t.Errorf("%s: %s", c[0], message)
		}
	}
	nine := []string{"name", "owner", "modified_by", "created_at", "modified_at", "docstatus", "title", "status", "qty"}
	nineJSON, _ := json.Marshal(nine)
	message = invalidView(t, create(viewDef(`{"filters":[],"sort":[],"columns":`+string(nineJSON)+`}`, db.ListQuery{})))
	if !strings.Contains(message, "at most 8") {
		t.Fatal(message)
	}
	// Eight of every kind that is allowed work.
	eightJSON, _ := json.Marshal(nine[:8])
	if err := create(viewDef(`{"filters":[],"sort":[],"columns":`+string(eightJSON)+`}`, db.ListQuery{})); err != nil {
		t.Fatal(err)
	}

	// Unknown keys and a missing key.
	for _, text := range []string{
		`{"filters":[],"sort":[],"columns":[],"limit":5}`, `{"filters":[],"sort":[]}`, `{"filters":{},"sort":[],"columns":[]}`, `[]`,
	} {
		wantKind(t, create(viewDef(text, db.ListQuery{})), db.ErrInvalidView)
	}
}

// Rust: views.rs::the_definition_has_a_size_cap
func TestTheDefinitionHasASizeCap(t *testing.T) {
	d, reg := viewSetup(t)
	big := strings.Repeat("x", db.MaxDefinitionBytes)
	definition := viewDef(`{"filters":[{"field":"title","op":"=","value":"`+big+`"}],"sort":[],"columns":[]}`,
		db.ListQuery{Filters: []db.Filter{{Field: "title", Op: db.Eq(text(big))}}})
	_, err := db.CreateView(bg, d.Pool, reg, "order", vAnn(), "big", definition, false)
	if !strings.Contains(invalidView(t, err), "16 KiB") {
		t.Fatal(err)
	}
}

// Rust: views.rs::labels_are_checked_and_unique
func TestLabelsAreCheckedAndUnique(t *testing.T) {
	d, reg := viewSetup(t)
	for _, label := range []string{"", " x", "x ", "a\nb", strings.Repeat("x", 65)} {
		_, err := createView(d, reg, vAnn(), label, false)
		wantKind(t, err, db.ErrInvalidView)
	}
	mustCreateView(t, d, reg, vAnn(), strings.Repeat("é", 64), false)

	// Private: unique per user and DocType; case matters.
	first := mustCreateView(t, d, reg, vAnn(), "Same", false)
	_, err := createView(d, reg, vAnn(), "Same", false)
	wantKind(t, err, db.ErrViewExists)
	mustCreateView(t, d, reg, vAnn(), "same", false)
	mustCreateView(t, d, reg, vBob(), "Same", false)
	// Another DocType has its own labels.
	if _, err := db.CreateView(bg, d.Pool, reg, "other", vAnn(), "Same", emptyDef(), false); err != nil {
		t.Fatal(err)
	}

	// Shared: unique per DocType.
	mustCreateView(t, d, reg, vRoot(), "Team", true)
	_, err = createView(d, reg, vRoot(), "Team", true)
	wantKind(t, err, db.ErrViewExists)
	// Renaming into a taken label is refused too.
	_, err = updateView(d, reg, vAnn(), first.ID, 1, renamed("same"))
	wantKind(t, err, db.ErrViewExists)
	_, err = updateView(d, reg, vAnn(), first.ID, 1, renamed(" bad"))
	wantKind(t, err, db.ErrInvalidView)
}

// Rust: views.rs::there_are_limits_per_user_and_for_shared_views
func TestThereAreLimitsPerUserAndForSharedViews(t *testing.T) {
	d, reg := viewSetup(t)
	// Fill by SQL: the limit is the point, not 100 round trips.
	d.Exec(fmt.Sprintf(`INSERT INTO fb_saved_view (id, doctype, label, owner_id, definition)
		SELECT gen_random_uuid(), 'order', 'v' || n, (SELECT id FROM fb_user WHERE username = 'ann'),
		'{"filters":[],"sort":[],"columns":[]}' FROM generate_series(1, %d) n`, db.MaxPrivateViews))
	_, err := createView(d, reg, vAnn(), "one too many", false)
	if message := invalidView(t, err); !strings.Contains(message, "at most 100 private views") {
		t.Fatal(message)
	}
	// Per user and per DocType.
	mustCreateView(t, d, reg, vBob(), "fine", false)
	if _, err := db.CreateView(bg, d.Pool, reg, "other", vAnn(), "fine", emptyDef(), false); err != nil {
		t.Fatal(err)
	}
	// Deleting one frees a slot.
	views, err := db.ListViews(bg, d.Pool, reg, "order", vAnn())
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteView(d, reg, vAnn(), views[0].ID, 1); err != nil {
		t.Fatal(err)
	}
	mustCreateView(t, d, reg, vAnn(), "one more", false)

	d.Exec(fmt.Sprintf(`INSERT INTO fb_saved_view (id, doctype, label, owner_id, definition)
		SELECT gen_random_uuid(), 'order', 's' || n, NULL,
		'{"filters":[],"sort":[],"columns":[]}' FROM generate_series(1, %d) n`, db.MaxSharedViews))
	_, err = createView(d, reg, vRoot(), "one too many", true)
	if message := invalidView(t, err); !strings.Contains(message, "at most 100 shared views") {
		t.Fatal(message)
	}
}

// Rust: views.rs::concurrent_creates_cannot_pass_the_limit
func TestConcurrentCreatesCannotPassTheLimit(t *testing.T) {
	d, reg := viewSetup(t)
	d.Exec(fmt.Sprintf(`INSERT INTO fb_saved_view (id, doctype, label, owner_id, definition)
		SELECT gen_random_uuid(), 'order', 'v' || n, (SELECT id FROM fb_user WHERE username = 'ann'),
		'{"filters":[],"sort":[],"columns":[]}' FROM generate_series(1, %d) n`, db.MaxPrivateViews-1))
	var wg sync.WaitGroup
	results := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = createView(d, reg, vAnn(), fmt.Sprintf("race %d", i), false)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatal(results)
	}
	rowsEqual(t, d.Rows("SELECT count(*)::text FROM fb_saved_view"), [][]string{{"100"}})
}

// Rust: views.rs::versions_are_optimistic
func TestViewVersionsAreOptimistic(t *testing.T) {
	d, reg := viewSetup(t)
	view := mustCreateView(t, d, reg, vAnn(), "v", false)
	if _, err := updateView(d, reg, vAnn(), view.ID, 1, renamed("w")); err != nil {
		t.Fatal(err)
	}
	_, err := updateView(d, reg, vAnn(), view.ID, 1, renamed("w"))
	if e := wantKind(t, err, db.ErrConflict); e.ExpectedVersion != 1 || e.CurrentVersion != 2 {
		t.Fatal(e)
	}
	wantKind(t, deleteView(d, reg, vAnn(), view.ID, 1), db.ErrConflict)
	if err := deleteView(d, reg, vAnn(), view.ID, 2); err != nil {
		t.Fatal(err)
	}
	// No change at all.
	view = mustCreateView(t, d, reg, vAnn(), "v", false)
	_, err = updateView(d, reg, vAnn(), view.ID, 1, db.ViewChanges{})
	wantKind(t, err, db.ErrNoChanges)
}

// Rust: views.rs::a_stale_definition_is_returned_as_stored
func TestAStaleDefinitionIsReturnedAsStored(t *testing.T) {
	d, reg := viewSetup(t)
	// A view that names a field the DocType no longer has: written by hand.
	d.Exec(`INSERT INTO fb_saved_view (id, doctype, label, owner_id, definition)
		VALUES (gen_random_uuid(), 'order', 'Old', NULL,
		'{"filters":[{"field":"gone","op":"is null"}],"sort":[],"columns":["gone"]}')`)
	views, err := db.ListViews(bg, d.Pool, reg, "order", vBob())
	if err != nil || len(views) != 1 {
		t.Fatal(views, err)
	}
	var def struct{ Columns []string }
	if err := json.Unmarshal(views[0].Definition, &def); err != nil || len(def.Columns) != 1 || def.Columns[0] != "gone" {
		t.Fatal(string(views[0].Definition), err)
	}
}

// Rust: views.rs::labels_sort_by_bytes_within_each_group
func TestLabelsSortByBytesWithinEachGroup(t *testing.T) {
	d, reg := viewSetup(t)
	for _, label := range []string{"b", "B", "a", "_x", "Z"} {
		mustCreateView(t, d, reg, vAnn(), label, false)
	}
	mustCreateView(t, d, reg, vRoot(), "zz shared", true)
	strsEqual(t, viewLabels(t, d, reg, vAnn()), []string{"zz shared", "B", "Z", "_x", "a", "b"})
}

// Rust: views.rs::deleting_a_user_deletes_their_private_views_only
func TestDeletingAUserDeletesTheirPrivateViewsOnly(t *testing.T) {
	d, reg := viewSetup(t)
	mustCreateView(t, d, reg, vAnn(), "mine", false)
	mustCreateView(t, d, reg, vBob(), "his", false)
	mustCreateView(t, d, reg, vRoot(), "shared", true)
	d.Exec("DELETE FROM fb_user WHERE username = 'ann'")
	rowsEqual(t, d.Rows(`SELECT label FROM fb_saved_view ORDER BY label COLLATE "C"`), [][]string{{"his"}, {"shared"}})
}
