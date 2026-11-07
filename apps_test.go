package forgebase_test

import (
	"embed"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// The app model against a real PostgreSQL (M5.1, ADR 0012, ADR 0028), written with forgebase/testing:
// embedded files, hooks across apps, ownership in the metadata API, app migrations. Ported from
// crates/forgebase/tests/apps.rs.
//
// Each test gets its own throwaway fb_test_app_* database and fails loudly (never skips) when
// FORGEBASE_TEST_DATABASE_URL is not set; see CONTRIBUTING.md.

// docTypes is an fs.FS with DocType files, from (path, toml) pairs.
func docTypes(pairs ...string) fs.FS {
	m := fstest.MapFS{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = &fstest.MapFile{Data: []byte(pairs[i+1])}
	}
	return m
}

// mig is a migration with a made-up file: migrations/NNNN_name.sql.
type mig struct {
	version int
	name    string
	sql     string
}

func migrations(list ...mig) fs.FS {
	m := fstest.MapFS{}
	for _, x := range list {
		m[fmt.Sprintf("migrations/%04d_%s.sql", x.version, x.name)] = &fstest.MapFile{Data: []byte(x.sql)}
	}
	return m
}

func rows(list ...[]string) [][]string {
	if list == nil {
		return [][]string{}
	}
	return list
}

func row(values ...string) []string { return values }

func applied(list ...fbtest.AppliedMigration) []fbtest.AppliedMigration { return list }

func am(app string, version int32, name string) fbtest.AppliedMigration {
	return fbtest.AppliedMigration{App: app, Version: version, Name: name}
}

func mustMigrate(t *testing.T, db *fbtest.DB, b *forgebase.Binary, dryRun bool) *fbtest.Migrated {
	t.Helper()
	done, err := db.Migrate(b, dryRun)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return done
}

func status(t *testing.T, r *fbtest.Reply, want int) {
	t.Helper()
	if r.Status != want {
		t.Fatalf("status %d, want %d: %s", r.Status, want, r.Text)
	}
}

// ---- embedded files -------------------------------------------------------------------------

//go:embed testdata/apps/doctypes/*.toml
var fixtureDocTypes embed.FS

//go:embed testdata/apps/migrations/*.sql
var fixtureMigrations embed.FS

// fixtureApp is the app of the fixture files, embedded with //go:embed: paths are relative to this package.
func fixtureApp() *forgebase.App {
	return forgebase.NewApp("fixture").DocTypes(fixtureDocTypes).Migrations(fixtureMigrations)
}

// Rust: apps.rs::the_macros_embed_files_relative_to_the_crate_that_calls_them
func TestEmbeddedFilesAreReadFromTheFileSystemWithTheirPaths(t *testing.T) {
	// Both accept an empty file system, and any number of calls.
	_ = forgebase.NewApp("empty").DocTypes(fstest.MapFS{}).Migrations(fstest.MapFS{})
	if err := forgebase.New().App(fixtureApp()).Check(); err != nil {
		t.Fatal(err)
	}
	// The embedded files are the ones on disk, under the paths the embed directive gives them.
	fbtest.AssertDocTypesListed(t, fixtureApp(), "testdata/apps/doctypes")
}

// Rust: apps.rs::an_app_with_embedded_files_migrates_and_serves
func TestAnAppWithEmbeddedFilesMigratesAndServes(t *testing.T) {
	binary := forgebase.New().App(fixtureApp())
	db := fbtest.NewDB(t)
	done := mustMigrate(t, db, binary, false)
	if want := applied(am("fixture", 1, "seed")); !reflect.DeepEqual(done.AppMigrations, want) {
		t.Fatalf("%+v", done.AppMigrations)
	}
	if got, want := db.Rows("SELECT name FROM fb_doctype ORDER BY name"), rows(row("gizmo"), row("widget")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if got, want := db.Rows("SELECT entry FROM fixture_log"), rows(row("seeded")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	// No version was declared: fb_app has the app with a null version.
	if got, want := db.Rows("SELECT name, version FROM fb_app"), rows(row("fixture", "NULL")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}

	db.CreateUser("ann", "Fixture User")
	ann := db.Client(binary).Login("ann")
	status(t, ann.Post("/api/v1/resource/widget", map[string]any{"code": "w-1", "title": "A widget"}), 201)
	reply := ann.Get("/api/v1/resource/widget/w-1")
	if reply.DataObject()["title"] != "A widget" {
		t.Errorf("%s", reply.Text)
	}
}

// ---- hooks across apps ------------------------------------------------------------------------

const customerDoc = `
name = "customer"
label = "Customer"
naming = { field = "code" }
[[fields]]
name = "code"
type = "data"
required = true
unique = true
[[fields]]
name = "title"
type = "data"
`

const orderDoc = `
name = "order"
label = "Order"
[[fields]]
name = "customer"
type = "link"
target = "customer"
`

type hookLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *hookLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *hookLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.lines...)
}

func (l *hookLog) clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = nil
}

// recorder records the hooks it sees, tagged with its name; may reject a customer titled banned.
type recorder struct {
	forgebase.BaseHooks
	who           string
	log           *hookLog
	rejectsBanned bool
}

func (r recorder) BeforeInsert(ctx forgebase.HookContext, _ *forgebase.Document) error {
	r.log.add(fmt.Sprintf("%s:before_insert:%s", r.who, ctx.DocType))
	return nil
}

func (r recorder) Validate(ctx forgebase.HookContext, doc *forgebase.Document) error {
	op := "other"
	switch ctx.Operation {
	case forgebase.OpInsert:
		op = "insert"
	case forgebase.OpUpdate:
		op = "update"
	}
	r.log.add(fmt.Sprintf("%s:validate:%s:%s", r.who, ctx.DocType, op))
	if r.rejectsBanned && doc.Values["title"].Equal(forgebase.Text("banned")) {
		return forgebase.NewHookError("a banned customer is not allowed")
	}
	return nil
}

// hookedBinary: crm owns customer; sales owns order and extends customer with hooks of its own.
func hookedBinary(log *hookLog) *forgebase.Binary {
	crm := forgebase.NewApp("crm").
		DocTypes(docTypes("doctypes/customer.toml", customerDoc)).
		Hooks("customer", recorder{who: "crm1", log: log}).
		Hooks("customer", recorder{who: "crm2", log: log})
	sales := forgebase.NewApp("sales").
		DependsOn("crm").
		DocTypes(docTypes("doctypes/order.toml", orderDoc)).
		Hooks("customer", recorder{who: "sales", log: log, rejectsBanned: true})
	return forgebase.New().App(crm).App(sales)
}

// Rust: apps.rs::hooks_run_in_app_order_then_registration_order_and_the_extending_app_can_reject
func TestHooksRunInAppOrderThenRegistrationOrderAndTheExtendingAppCanReject(t *testing.T) {
	log := &hookLog{}
	binary := hookedBinary(log)
	db := fbtest.NewDB(t)
	mustMigrate(t, db, binary, false)
	db.CreateUser("root", "Administrator")
	root := db.Client(binary).Login("root")

	status(t, root.Post("/api/v1/resource/customer", map[string]any{"code": "c-1", "title": "fine"}), 201)
	// The app that owns the DocType first (in registration order), then the app that extends it.
	want := []string{
		"crm1:before_insert:customer",
		"crm2:before_insert:customer",
		"sales:before_insert:customer",
		"crm1:validate:customer:insert",
		"crm2:validate:customer:insert",
		"sales:validate:customer:insert",
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("hooks ran %v", got)
	}

	// The extending app's hook rejects what the owning app accepted; nothing is stored.
	log.clear()
	reply := root.Post("/api/v1/resource/customer", map[string]any{"code": "c-2", "title": "banned"})
	status(t, reply, 422)
	if reply.ErrorCode() != "hook_rejected" || !strings.Contains(reply.ErrorMessage(), "a banned customer is not allowed") {
		t.Errorf("%s", reply.Text)
	}
	if n := len(log.snapshot()); n != 6 {
		t.Errorf("%d hook calls", n)
	}
	if got, want := db.Rows("SELECT name FROM doc_customer ORDER BY name"), rows(row("c-1")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

// ---- ownership --------------------------------------------------------------------------------

// Rust: apps.rs::the_metadata_api_says_which_app_owns_a_doctype
func TestTheMetadataAPISaysWhichAppOwnsADocType(t *testing.T) {
	binary := hookedBinary(&hookLog{})
	db := fbtest.NewDB(t)
	mustMigrate(t, db, binary, false)
	db.CreateUser("root", "Administrator")
	root := db.Client(binary).Login("root")

	reply := root.Get("/api/v1/meta")
	status(t, reply, 200)
	var owners [][2]string
	for _, item := range reply.DataList() {
		d := item.(map[string]any)
		owners = append(owners, [2]string{d["name"].(string), d["app"].(string)})
	}
	if want := [][2]string{{"customer", "crm"}, {"order", "sales"}}; !reflect.DeepEqual(owners, want) {
		t.Errorf("%v", owners)
	}
	if got := root.Get("/api/v1/meta/customer").DataObject()["app"]; got != "crm" {
		t.Errorf("customer is owned by %v", got)
	}
	if got := root.Get("/api/v1/meta/order").DataObject()["app"]; got != "sales" {
		t.Errorf("order is owned by %v", got)
	}
}

// ---- app migrations ---------------------------------------------------------------------------

var crmMigrations = []mig{
	{1, "create_log", "CREATE TABLE zz_log (id serial PRIMARY KEY, entry text NOT NULL); INSERT INTO zz_log (entry) VALUES ('crm 1')"},
	{2, "second", "INSERT INTO zz_log (entry) VALUES ('crm 2')"},
}

var salesMigrations = []mig{
	// Runs after the schema sync: the DocType table of crm exists.
	{1, "after_sync", "INSERT INTO zz_log (entry) SELECT 'sales 1, doc_customer has ' || count(*) FROM doc_customer"},
	{5, "gap_in_versions_is_fine", "INSERT INTO zz_log (entry) VALUES ('sales 5')"},
}

func crmWith(ms []mig) *forgebase.App {
	return forgebase.NewApp("crm").Version("1.0.0").
		DocTypes(docTypes("doctypes/customer.toml", customerDoc)).Migrations(migrations(ms...))
}

func salesWith(ms []mig) *forgebase.App {
	return forgebase.NewApp("sales").Version("2.0.0").DependsOn("crm").
		DocTypes(docTypes("doctypes/order.toml", orderDoc)).Migrations(migrations(ms...))
}

func migratingBinary() *forgebase.Binary {
	return forgebase.New().App(crmWith(crmMigrations)).App(salesWith(salesMigrations))
}

func logEntries(db *fbtest.DB) [][]string { return db.Rows("SELECT entry FROM zz_log ORDER BY id") }

func with(ms []mig, more ...mig) []mig { return append(append([]mig{}, ms...), more...) }

// Rust: apps.rs::app_migrations_run_once_in_app_order_and_are_recorded
func TestAppMigrationsRunOnceInAppOrderAndAreRecorded(t *testing.T) {
	db := fbtest.NewDB(t)
	binary := migratingBinary()
	done := mustMigrate(t, db, binary, false)
	want := applied(am("crm", 1, "create_log"), am("crm", 2, "second"), am("sales", 1, "after_sync"), am("sales", 5, "gap_in_versions_is_fine"))
	if !reflect.DeepEqual(done.AppMigrations, want) {
		t.Fatalf("%+v", done.AppMigrations)
	}
	if got, want := logEntries(db), rows(row("crm 1"), row("crm 2"), row("sales 1, doc_customer has 0"), row("sales 5")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	got := db.Rows("SELECT app, version::text, name, length(checksum)::text FROM fb_app_migration ORDER BY app, version")
	if want := rows(row("crm", "1", "create_log", "64"), row("crm", "2", "second", "64"), row("sales", "1", "after_sync", "64"),
		row("sales", "5", "gap_in_versions_is_fine", "64")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if got, want := db.Rows("SELECT name, version FROM fb_app ORDER BY name"), rows(row("crm", "1.0.0"), row("sales", "2.0.0")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}

	// A second migrate skips everything that was applied.
	if again := mustMigrate(t, db, binary, false); len(again.AppMigrations) != 0 {
		t.Errorf("%+v", again.AppMigrations)
	}
	if n := len(logEntries(db)); n != 4 {
		t.Errorf("%d entries", n)
	}

	// A new version of an app: its new migration runs, fb_app has the new version.
	more := with(crmMigrations, mig{3, "third", "INSERT INTO zz_log (entry) VALUES ('crm 3')"})
	newer := forgebase.New().App(crmWith(more).Version("1.1.0")).App(salesWith(salesMigrations))
	done = mustMigrate(t, db, newer, false)
	if want := applied(am("crm", 3, "third")); !reflect.DeepEqual(done.AppMigrations, want) {
		t.Errorf("%+v", done.AppMigrations)
	}
	if got, want := db.Rows("SELECT version FROM fb_app WHERE name = 'crm'"), rows(row("1.1.0")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

// Rust: apps.rs::a_dry_run_lists_the_pending_migrations_and_keeps_nothing
func TestADryRunListsThePendingMigrationsAndKeepsNothing(t *testing.T) {
	db := fbtest.NewDB(t)
	binary := migratingBinary()
	// On an empty database the framework migrations are pending too: every declared migration is listed,
	// and nothing exists afterwards.
	if dry := mustMigrate(t, db, binary, true); len(dry.AppMigrations) != 4 {
		t.Errorf("%+v", dry.AppMigrations)
	}
	if got, want := db.Rows("SELECT coalesce(to_regclass('fb_app')::text, 'NULL'), coalesce(to_regclass('zz_log')::text, 'NULL')"), rows(row("NULL", "NULL")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}

	mustMigrate(t, db, binary, false)
	// Up to date: nothing pending.
	if dry := mustMigrate(t, db, binary, true); len(dry.AppMigrations) != 0 {
		t.Errorf("%+v", dry.AppMigrations)
	}

	// A new migration is listed but not run, and fb_app is not touched.
	more := with(crmMigrations, mig{3, "third", "INSERT INTO zz_log (entry) VALUES ('crm 3')"})
	newer := forgebase.New().App(crmWith(more).Version("9.9.9")).App(salesWith(salesMigrations))
	dry := mustMigrate(t, db, newer, true)
	if want := applied(am("crm", 3, "third")); !reflect.DeepEqual(dry.AppMigrations, want) {
		t.Errorf("%+v", dry.AppMigrations)
	}
	if n := len(logEntries(db)); n != 4 {
		t.Errorf("%d entries", n)
	}
	if got, want := db.Rows("SELECT version FROM fb_app WHERE name = 'crm'"), rows(row("1.0.0")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

// Rust: apps.rs::a_changed_migration_is_refused_before_anything_else_is_applied
func TestAChangedMigrationIsRefusedBeforeAnythingElseIsApplied(t *testing.T) {
	db := fbtest.NewDB(t)
	mustMigrate(t, db, migratingBinary(), false)

	// crm 1 was edited; the same release adds a migration and a DocType.
	edited := []mig{
		{1, "create_log", "CREATE TABLE zz_log (id serial PRIMARY KEY, entry text NOT NULL); INSERT INTO zz_log (entry) VALUES ('crm 1, edited')"},
		crmMigrations[1],
		{3, "third", "INSERT INTO zz_log (entry) VALUES ('crm 3')"},
	}
	const widget = "name = \"widget\"\nlabel = \"W\"\n"
	binary := forgebase.New().
		App(crmWith(edited).DocTypes(docTypes("doctypes/widget.toml", widget))).
		App(salesWith(salesMigrations))
	for _, dryRun := range []bool{false, true} {
		_, err := db.Migrate(binary, dryRun)
		if err == nil || !strings.Contains(err.Error(), "migration 1 (create_log) of app `crm` was changed after it was applied") {
			t.Errorf("dry run %v: %v", dryRun, err)
		}
	}
	// Nothing was applied: no new migration, no schema sync, the app row has the old data.
	if n := len(logEntries(db)); n != 4 {
		t.Errorf("%d entries", n)
	}
	if got, want := db.Rows("SELECT count(*)::text FROM fb_app_migration"), rows(row("4")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if got, want := db.Rows("SELECT coalesce(to_regclass('doc_widget')::text, 'NULL')"), rows(row("NULL")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

// Rust: apps.rs::a_failing_migration_is_rolled_back_and_stops_the_run
func TestAFailingMigrationIsRolledBackAndStopsTheRun(t *testing.T) {
	broken := []mig{
		{1, "fine", "INSERT INTO zz_log (entry) VALUES ('sales 1')"},
		// Writes a row and then fails: the row must not stay.
		{2, "boom", "INSERT INTO zz_log (entry) VALUES ('sales 2'); SELECT 1 / 0"},
		{3, "never", "INSERT INTO zz_log (entry) VALUES ('sales 3')"},
	}
	db := fbtest.NewDB(t)
	binary := forgebase.New().App(crmWith(crmMigrations)).App(salesWith(broken))
	_, err := db.Migrate(binary, false)
	if err == nil {
		t.Fatal("must fail")
	}
	for _, part := range []string{"migration 2 (boom) of app `sales` failed and was rolled back", "earlier migrations stay applied", "division by zero"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("missing %q in %v", part, err)
		}
	}
	// The migrations before the failing one stay; the failing one left nothing; the next did not run.
	if got, want := logEntries(db), rows(row("crm 1"), row("crm 2"), row("sales 1")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if got, want := db.Rows("SELECT app, version::text FROM fb_app_migration ORDER BY app, version"), rows(row("crm", "1"), row("crm", "2"), row("sales", "1")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	// crm was done; sales was not, so its fb_app row is missing.
	if got, want := db.Rows("SELECT name FROM fb_app ORDER BY name"), rows(row("crm")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}

	// Fixed (the failed migration was never recorded, so its SQL may change): the run continues.
	fixed := []mig{broken[0], {2, "boom", "INSERT INTO zz_log (entry) VALUES ('sales 2')"}, broken[2]}
	done := mustMigrate(t, db, forgebase.New().App(crmWith(crmMigrations)).App(salesWith(fixed)), false)
	if want := applied(am("sales", 2, "boom"), am("sales", 3, "never")); !reflect.DeepEqual(done.AppMigrations, want) {
		t.Errorf("%+v", done.AppMigrations)
	}
	if n := len(logEntries(db)); n != 5 {
		t.Errorf("%d entries", n)
	}
	if got, want := db.Rows("SELECT name FROM fb_app ORDER BY name"), rows(row("crm"), row("sales")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

// Rust: apps.rs::a_migration_the_app_no_longer_declares_is_refused_and_absent_apps_are_left_alone
func TestAMigrationTheAppNoLongerDeclaresIsRefusedAndAbsentAppsAreLeftAlone(t *testing.T) {
	db := fbtest.NewDB(t)
	mustMigrate(t, db, migratingBinary(), false)

	// crm dropped its migration 2: the database is ahead of the app.
	behind := forgebase.New().App(crmWith(crmMigrations[:1])).App(salesWith(salesMigrations))
	_, err := db.Migrate(behind, false)
	if err == nil || !strings.Contains(err.Error(), "migration 2 (second) of app `crm`") || !strings.Contains(err.Error(), "does not declare") {
		t.Errorf("%v", err)
	}

	// A binary without sales leaves what sales recorded as it is.
	done := mustMigrate(t, db, forgebase.New().App(crmWith(crmMigrations)), false)
	if len(done.AppMigrations) != 0 {
		t.Errorf("%+v", done.AppMigrations)
	}
	if got, want := db.Rows("SELECT name FROM fb_app ORDER BY name"), rows(row("crm"), row("sales")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if got, want := db.Rows("SELECT count(*)::text FROM fb_app_migration WHERE app = 'sales'"), rows(row("2")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

// Rust: apps.rs::migrate_reports_startup_errors_without_touching_the_database
func TestMigrateReportsStartupErrorsWithoutTouchingTheDatabase(t *testing.T) {
	db := fbtest.NewDB(t)
	_, err := db.Migrate(forgebase.New().App(salesWith(salesMigrations)), false)
	var startup *forgebase.StartupError
	if err == nil || !strings.Contains(err.Error(), "app `sales` depends on `crm`, which is not registered") {
		t.Fatalf("%v", err)
	}
	if !asStartup(err, &startup) {
		t.Errorf("%T is not a *StartupError", err)
	}
	if got, want := db.Rows("SELECT coalesce(to_regclass('fb_migration')::text, 'NULL')"), rows(row("NULL")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
}

func asStartup(err error, target **forgebase.StartupError) bool {
	s, ok := err.(*forgebase.StartupError)
	*target = s
	return ok
}

// Rust: apps.rs::a_migration_and_its_record_commit_or_roll_back_together
func TestAMigrationAndItsRecordCommitOrRollBackTogether(t *testing.T) {
	// The SQL creates a table and then writes the very row the framework records the migration with
	// (same app, same version), so recording it fails with a unique violation. The table must not
	// survive: SQL and record are one transaction.
	clash := []mig{{1, "clash", "CREATE TABLE zz_atomic (n integer); INSERT INTO fb_app_migration (app, version, name, checksum) VALUES ('crm', 1, 'mine', 'x')"}}
	db := fbtest.NewDB(t)
	_, err := db.Migrate(forgebase.New().App(crmWith(clash)), false)
	if err == nil || !strings.Contains(err.Error(), "fb_app_migration_pkey") {
		t.Fatalf("%v", err)
	}
	if got, want := db.Rows("SELECT coalesce(to_regclass('zz_atomic')::text, 'NULL')"), rows(row("NULL")); !reflect.DeepEqual(got, want) {
		t.Errorf("the migration SQL was committed without its record: %v", got)
	}
	if got, want := db.Rows("SELECT count(*)::text FROM fb_app_migration"), rows(row("0")); !reflect.DeepEqual(got, want) {
		t.Errorf("something was recorded: %v", got)
	}
}

// Rust: apps.rs::a_pending_migration_below_an_applied_version_is_refused_and_nothing_is_applied
func TestAPendingMigrationBelowAnAppliedVersionIsRefusedAndNothingIsApplied(t *testing.T) {
	// crm has 1 and 3 applied (a gap is fine).
	appliedMigs := []mig{
		{1, "create_log", "CREATE TABLE zz_log (id serial PRIMARY KEY, entry text NOT NULL)"},
		{3, "three", "INSERT INTO zz_log (entry) VALUES ('crm 3')"},
	}
	db := fbtest.NewDB(t)
	mustMigrate(t, db, forgebase.New().App(crmWith(appliedMigs)), false)

	// The new release declares a migration 2, below the applied 3, and also new work that would
	// otherwise run: a later app's migration and a new DocType.
	withTwo := []mig{appliedMigs[0], {2, "two", "INSERT INTO zz_log (entry) VALUES ('crm 2')"}, appliedMigs[1]}
	const widget = "name = \"widget\"\nlabel = \"W\"\n"
	newer := forgebase.New().
		App(crmWith(withTwo).DocTypes(docTypes("doctypes/widget.toml", widget))).
		App(salesWith(salesMigrations))
	for _, dryRun := range []bool{false, true} {
		_, err := db.Migrate(newer, dryRun)
		if err == nil {
			t.Fatalf("dry run %v: must be refused", dryRun)
		}
		for _, part := range []string{"migration 2 (two) of app `crm` is not applied, but migration 3 of the app is", "renumber it above 3", "nothing was applied"} {
			if !strings.Contains(err.Error(), part) {
				t.Errorf("dry run %v: missing %q in %v", dryRun, part, err)
			}
		}
	}
	if got, want := logEntries(db), rows(row("crm 3")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if got, want := db.Rows("SELECT app, version::text FROM fb_app_migration ORDER BY app, version"), rows(row("crm", "1"), row("crm", "3")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if got, want := db.Rows("SELECT coalesce(to_regclass('doc_widget')::text, 'NULL')"), rows(row("NULL")); !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}

	// Renumbered above the applied versions it runs.
	renumbered := []mig{appliedMigs[0], appliedMigs[1], {4, "two", "INSERT INTO zz_log (entry) VALUES ('crm 2')"}}
	done := mustMigrate(t, db, forgebase.New().App(crmWith(renumbered)), false)
	if want := applied(am("crm", 4, "two")); !reflect.DeepEqual(done.AppMigrations, want) {
		t.Errorf("%+v", done.AppMigrations)
	}
}
