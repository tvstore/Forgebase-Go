package forgebase_test

import (
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// Fixtures end to end (M9.4, ADR 0022): `migrate` applies the records an app ships, through the document
// pipeline, with a checksum per record in fb_fixture. Real PostgreSQL (forgebase/testing). Ported from
// crates/forgebase/tests/fixtures.rs.
//
// Two apps: `base` has `customer_group` and `price_list` (with `price_line` rows), `ext` depends on `base` and
// has `region`, which links to a `customer_group`. A new version of a fixture is another file system built from
// another string.

const fxGroup = `
name = "customer_group"
label = "Customer group"
naming = { field = "title" }
[[fields]]
name = "title"
type = "data"
required = true
unique = true
[[fields]]
name = "description"
type = "data"
max_length = 20
[[fields]]
name = "rank"
type = "int"
`

const fxLine = `
name = "price_line"
label = "Price line"
is_child = true
[[fields]]
name = "item"
type = "data"
[[fields]]
name = "price"
type = "int"
`

const fxList = `
name = "price_list"
label = "Price list"
naming = { field = "code" }
[[fields]]
name = "code"
type = "data"
required = true
unique = true
[[fields]]
name = "group"
type = "link"
target = "customer_group"
[[fields]]
name = "lines"
type = "table"
target = "price_line"
`

const fxRegion = `
name = "region"
label = "Region"
naming = { field = "code" }
[[fields]]
name = "code"
type = "data"
required = true
unique = true
[[fields]]
name = "group"
type = "link"
target = "customer_group"
[[fields]]
name = "label"
type = "data"
`

// ---- fixture versions ---------------------------------------------------------------------------

const fxGroupsV1 = `{"doctype": "customer_group", "records": [
    {"title": "Retail", "description": "d1"},
    {"title": "Wholesale", "description": "w1", "rank": 2}
]}`

// Retail changes (and loses rank, which it never had), Wholesale is the same with other whitespace and key
// order.
const fxGroupsV2 = `{"records": [
    {"description": "d2", "title": "Retail"},
    {"rank": 2,"description":"w1","title":"Wholesale"}
], "doctype": "customer_group"}`

// Wholesale left the file.
const fxGroupsV3 = `{"doctype": "customer_group", "records": [
    {"title": "Retail", "description": "d2"}
]}`

// A new record next to a changed one.
const fxGroupsV4 = `{"doctype": "customer_group", "records": [
    {"title": "Retail", "description": "d3"},
    {"title": "Wholesale", "description": "w1", "rank": 2},
    {"title": "Online"}
]}`

const fxGroupsBadSecond = `{"doctype": "customer_group", "records": [
    {"title": "Retail"},
    {"title": "Wholesale", "description": "this value is much too long for the field"}
]}`

const fxListsV1 = `{"doctype": "price_list", "records": [
    {"code": "std", "group": "Retail", "lines": [{"item": "a", "price": 1}, {"item": "b", "price": 2}]}
]}`

const fxListsV2 = `{"doctype": "price_list", "records": [
    {"code": "std", "lines": [{"item": "c", "price": 3}]}
]}`

const fxListsBadLink = `{"doctype": "price_list", "records": [
    {"code": "std", "group": "No such group"}
]}`

const fxRegionsV1 = `{"doctype": "region", "records": [
    {"code": "north", "group": "Retail", "label": "North"}
]}`

// fxFiles is an fs.FS with fixture files, from (path, json) pairs.
func fxFiles(pairs ...string) fs.FS {
	m := fstest.MapFS{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = &fstest.MapFile{Data: []byte(pairs[i+1])}
	}
	return m
}

func fxGroups(json string) fs.FS { return fxFiles("fixtures/customer_group.json", json) }
func fxLists(json string) fs.FS  { return fxFiles("fixtures/price_list.json", json) }
func fxNoFiles() fs.FS           { return fxFiles() }

// fxRank99 sets rank to 99 on a group that has none: proof that the hooks run for a fixture.
type fxRank99 struct{ forgebase.BaseHooks }

func (fxRank99) Validate(_ forgebase.HookContext, doc *forgebase.Document) error {
	if v, ok := doc.Values["rank"]; !ok || v.Kind == forgebase.KindNull {
		doc.Values["rank"] = forgebase.Int(99)
	}
	return nil
}

// fxShout upper-cases the naming field: a record would be tracked under a name its document lacks.
type fxShout struct{ forgebase.BaseHooks }

func (fxShout) Validate(_ forgebase.HookContext, doc *forgebase.Document) error {
	if v, ok := doc.Values["title"]; ok && v.Kind == forgebase.KindText {
		doc.Values["title"] = forgebase.Text(strings.ToUpper(v.Text))
	}
	return nil
}

func fxBase(groups, lists fs.FS) *forgebase.App {
	return forgebase.NewApp("base").
		DocTypes(docTypes("doctypes/customer_group.toml", fxGroup, "doctypes/price_line.toml", fxLine, "doctypes/price_list.toml", fxList)).
		Migrations(migrations(mig{1, "seed", "CREATE TABLE base_seed (entry text)"})).
		Fixtures(groups).
		Fixtures(lists)
}

func fxExt(regions fs.FS) *forgebase.App {
	return forgebase.NewApp("ext").DependsOn("base").
		DocTypes(docTypes("doctypes/region.toml", fxRegion)).
		Fixtures(regions)
}

func fxBinary(groups fs.FS) *forgebase.Binary {
	return forgebase.New().App(fxBase(groups, fxNoFiles())).App(fxExt(fxFiles("fixtures/region.json", fxRegionsV1)))
}

// fxSummary is "app inserted updated kept unchanged" of each app that has fixtures.
func fxSummary(m *fbtest.Migrated) []string {
	out := []string{}
	for _, f := range m.Fixtures {
		out = append(out, fmt.Sprintf("%s %d %d %d %d", f.App, f.Inserted, f.Updated, f.Kept, f.Unchanged))
	}
	return out
}

func fxMigrate(t *testing.T, db *fbtest.DB, b *forgebase.Binary) *fbtest.Migrated {
	t.Helper()
	return mustMigrate(t, db, b, false)
}

// fxGroupRows are the groups as title|description|rank|version|modified_by, by title.
func fxGroupRows(db *fbtest.DB) []string {
	out := []string{}
	for _, r := range db.Rows("SELECT concat_ws('|', title, coalesce(description, '-'), coalesce(rank::text, '-'), " +
		"version::text, modified_by) FROM doc_customer_group ORDER BY title") {
		out = append(out, r[0])
	}
	return out
}

func fxScalar(db *fbtest.DB, sql string) string { return db.Rows(sql)[0][0] }

func fxChecksum(db *fbtest.DB, name string) string {
	return fxScalar(db, "SELECT checksum FROM fb_fixture WHERE doctype = 'customer_group' AND name = '"+name+"'")
}

// fxSnapshot is everything a write would change: the documents, the fixture table and the job table, with
// the row versions of PostgreSQL (xmin), so that even a write of the same values shows.
func fxSnapshot(db *fbtest.DB) [][]string {
	return db.Rows("SELECT 'group', name, version::text, modified_at::text, xmin::text FROM doc_customer_group " +
		"UNION ALL SELECT 'region', name, version::text, modified_at::text, xmin::text FROM doc_region " +
		"UNION ALL SELECT 'fixture', doctype || '/' || name, checksum, applied_at::text, xmin::text FROM fb_fixture " +
		"UNION ALL SELECT 'job', id::text, name, status, xmin::text FROM fb_job " +
		"ORDER BY 1, 2")
}

func wantRows(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// Rust: fixtures.rs::the_first_migrate_inserts_the_records_and_counts_them
func TestTheFirstMigrateInsertsTheRecordsAndCountsThem(t *testing.T) {
	db := fbtest.NewDB(t)
	migrated := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	wantRows(t, fxSummary(migrated), []string{"base 2 0 0 0", "ext 1 0 0 0"})
	wantRows(t, fxGroupRows(db), []string{"Retail|d1|-|1|fixture:base", "Wholesale|w1|2|1|fixture:base"})
	// `ext` came after `base`: its record links to a `base` record.
	got := db.Rows(`SELECT r.code, (SELECT g.name FROM doc_customer_group g WHERE g.id = r."group"), r.label, r.modified_by FROM doc_region r`)
	if !reflect.DeepEqual(got, [][]string{{"north", "Retail", "North", "fixture:ext"}}) {
		t.Errorf("%v", got)
	}
	// One row per record, with the app and a SHA-256.
	got = db.Rows("SELECT doctype, name, app, length(checksum)::text FROM fb_fixture ORDER BY doctype, name")
	if !reflect.DeepEqual(got, [][]string{{"customer_group", "Retail", "base", "64"}, {"customer_group", "Wholesale", "base", "64"},
		{"region", "north", "ext", "64"}}) {
		t.Errorf("%v", got)
	}
	// The app migrations ran before the fixtures and stay.
	if got := fxScalar(db, "SELECT to_regclass('base_seed')::text"); got != "base_seed" {
		t.Error(got)
	}
}

// Rust: fixtures.rs::the_second_migrate_changes_nothing
func TestTheSecondMigrateChangesNothing(t *testing.T) {
	db := fbtest.NewDB(t)
	// A webhook on the DocType: every fixture write would enqueue a delivery.
	fxMigrate(t, db, forgebase.New())
	db.Exec("INSERT INTO fb_webhook (name, doctype, events, url, secret) " +
		"VALUES ('w', 'customer_group', '{insert,update}', 'https://example.com/x', 's')")
	b := fxBinary(fxGroups(fxGroupsV1))
	fxMigrate(t, db, b)
	// The first migrate wrote two deliveries, one per inserted group.
	if got := fxScalar(db, "SELECT count(*)::text FROM fb_job"); got != "2" {
		t.Fatal(got)
	}
	before := fxSnapshot(db)
	again := fxMigrate(t, db, b)
	wantRows(t, fxSummary(again), []string{"base 0 0 0 2", "ext 0 0 0 1"})
	if !reflect.DeepEqual(fxSnapshot(db), before) {
		t.Error("the second migrate wrote something")
	}
}

// Rust: fixtures.rs::a_site_edit_is_kept_until_the_fixture_changes
func TestASiteEditIsKeptUntilTheFixtureChanges(t *testing.T) {
	db := fbtest.NewDB(t)
	fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	db.Exec("UPDATE doc_customer_group SET description = 'mine', rank = 7, version = version + 1 WHERE title = 'Retail'")
	again := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	if got := fxSummary(again)[0]; got != "base 0 0 0 2" {
		t.Error(got)
	}
	if got := fxGroupRows(db)[0]; got != "Retail|mine|7|2|fixture:base" {
		t.Errorf("the site's edit was overwritten: %s", got)
	}
	// V2 changes Retail only (Wholesale differs in whitespace and key order, which is no change).
	v2 := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV2)))
	if got := fxSummary(v2)[0]; got != "base 0 1 0 1" {
		t.Error(got)
	}
}

// Rust: fixtures.rs::a_changed_fixture_updates_only_the_fields_it_has
func TestAChangedFixtureUpdatesOnlyTheFieldsItHas(t *testing.T) {
	db := fbtest.NewDB(t)
	fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	before := fxChecksum(db, "Retail")
	wholesale := fxChecksum(db, "Wholesale")
	// The site sets a field the fixture does not have, and changes one it has.
	db.Exec("UPDATE doc_customer_group SET rank = 5, description = 'mine', version = version + 1 WHERE title = 'Retail'")
	v2 := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV2)))
	if got := fxSummary(v2)[0]; got != "base 0 1 0 1" {
		t.Error(got)
	}
	// description is the new version's, rank is the site's, and the version moved on by one (the site's edit
	// made it 2).
	if got := fxGroupRows(db)[0]; got != "Retail|d2|5|3|fixture:base" {
		t.Error(got)
	}
	if fxChecksum(db, "Retail") == before || fxChecksum(db, "Wholesale") != wholesale {
		t.Error("the checksums")
	}
	// The record that did not change was not written.
	if got := fxGroupRows(db)[1]; got != "Wholesale|w1|2|1|fixture:base" {
		t.Error(got)
	}
	again := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV2)))
	if got := fxSummary(again)[0]; got != "base 0 0 0 2" {
		t.Error(got)
	}
	if got := fxGroupRows(db)[0]; got != "Retail|d2|5|3|fixture:base" {
		t.Error(got)
	}
}

// Rust: fixtures.rs::a_document_that_existed_before_the_fixture_is_adopted
func TestADocumentThatExistedBeforeTheFixtureIsAdopted(t *testing.T) {
	db := fbtest.NewDB(t)
	// The site has the app without fixtures and creates "Retail" itself.
	fxMigrate(t, db, forgebase.New().App(fxBase(fxNoFiles(), fxNoFiles())).App(fxExt(fxNoFiles())))
	db.Exec("INSERT INTO doc_customer_group (id, name, owner, created_at, modified_at, modified_by, version, title, description) " +
		"VALUES (gen_random_uuid(), 'Retail', 'ann', now(), now(), 'ann', 1, 'Retail', 'site made')")
	migrated := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	// Retail is kept as it is; Wholesale is new.
	if got := fxSummary(migrated)[0]; got != "base 1 0 1 0" {
		t.Error(got)
	}
	wantRows(t, fxGroupRows(db), []string{"Retail|site made|-|1|ann", "Wholesale|w1|2|1|fixture:base"})
	if len(fxChecksum(db, "Retail")) != 64 {
		t.Error("no checksum")
	}
	// From now on it is tracked like the others: unchanged, until the app ships a new version.
	again := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	if got := fxSummary(again)[0]; got != "base 0 0 0 2" {
		t.Error(got)
	}
	v2 := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV2)))
	if got := fxSummary(v2)[0]; got != "base 0 1 0 1" {
		t.Error(got)
	}
	if got := fxGroupRows(db)[0]; got != "Retail|d2|-|2|fixture:base" {
		t.Error(got)
	}
}

// Rust: fixtures.rs::a_document_the_site_deleted_comes_back_only_with_a_new_version
func TestADocumentTheSiteDeletedComesBackOnlyWithANewVersion(t *testing.T) {
	db := fbtest.NewDB(t)
	fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	// `region` links to Retail: delete the region first, then the group.
	db.Exec("DELETE FROM doc_region; DELETE FROM doc_customer_group WHERE title = 'Retail'")
	again := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	if got := fxSummary(again)[0]; got != "base 0 0 0 2" {
		t.Error(got)
	}
	wantRows(t, fxGroupRows(db), []string{"Wholesale|w1|2|1|fixture:base"}) // an unchanged fixture brought a deleted document back
	// The app ships a new version of the record: it is inserted again.
	v2 := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV2)))
	if got := fxSummary(v2)[0]; got != "base 1 0 0 1" {
		t.Error(got)
	}
	wantRows(t, fxGroupRows(db), []string{"Retail|d2|-|1|fixture:base", "Wholesale|w1|2|1|fixture:base"})
}

// Rust: fixtures.rs::a_record_removed_from_the_fixture_is_never_deleted
func TestARecordRemovedFromTheFixtureIsNeverDeleted(t *testing.T) {
	db := fbtest.NewDB(t)
	fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	checksum := fxChecksum(db, "Wholesale")
	v3 := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV3)))
	if got := fxSummary(v3)[0]; got != "base 0 1 0 0" {
		t.Error(got)
	}
	wantRows(t, fxGroupRows(db), []string{"Retail|d2|-|2|fixture:base", "Wholesale|w1|2|1|fixture:base"})
	// The row stays, with the checksum it had.
	if fxChecksum(db, "Wholesale") != checksum {
		t.Error("the checksum changed")
	}
	// And a later version that has it again (same content) finds it unchanged.
	v1 := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	if got := fxSummary(v1)[0]; got != "base 0 1 0 1" {
		t.Error(got)
	}
}

// Rust: fixtures.rs::a_new_record_is_inserted_next_to_a_changed_one
func TestANewRecordIsInsertedNextToAChangedOne(t *testing.T) {
	db := fbtest.NewDB(t)
	fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	v4 := fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV4)))
	if got := fxSummary(v4)[0]; got != "base 1 1 0 1" {
		t.Error(got)
	}
	if n := len(fxGroupRows(db)); n != 3 {
		t.Error(n)
	}
}

// Rust: fixtures.rs::table_rows_are_inserted_and_replaced_when_the_record_changes
func TestTableRowsAreInsertedAndReplacedWhenTheRecordChanges(t *testing.T) {
	db := fbtest.NewDB(t)
	withLists := func(groups, lists fs.FS) *forgebase.Binary { return forgebase.New().App(fxBase(groups, lists)) }
	fxMigrate(t, db, withLists(fxGroups(fxGroupsV1), fxLists(fxListsV1)))
	lines := func() [][]string {
		return db.Rows("SELECT string_agg(l.item || ':' || l.price, ',' ORDER BY l.idx) FROM doc_price_list p " +
			"JOIN doc_price_line l ON l.parent_id = p.id WHERE p.code = 'std'")
	}
	if got := lines(); !reflect.DeepEqual(got, [][]string{{"a:1,b:2"}}) {
		t.Error(got)
	}
	// V2 gives other rows and no `group`: the rows are replaced, the link stays as it was.
	v2 := fxMigrate(t, db, withLists(fxGroups(fxGroupsV1), fxLists(fxListsV2)))
	if got := fxSummary(v2)[0]; got != "base 0 1 0 2" {
		t.Error(got)
	}
	if got := lines(); !reflect.DeepEqual(got, [][]string{{"c:3"}}) {
		t.Error(got)
	}
	if got := fxScalar(db, `SELECT g.name FROM doc_price_list p JOIN doc_customer_group g ON g.id = p."group" WHERE p.code = 'std'`); got != "Retail" {
		t.Error(got)
	}
}

// Rust: fixtures.rs::a_record_that_fails_validation_rolls_back_the_fixtures_of_its_app
func TestARecordThatFailsValidationRollsBackTheFixturesOfItsApp(t *testing.T) {
	db := fbtest.NewDB(t)
	_, err := db.Migrate(forgebase.New().App(fxBase(fxGroups(fxGroupsBadSecond), fxNoFiles())), false)
	if err == nil {
		t.Fatal("migrate must fail")
	}
	// It names the app, the file, the record and the problem.
	message := err.Error()
	for _, want := range []string{"app `base`", "fixtures/customer_group.json", "record 2 (`Wholesale`)", "description"} {
		if !strings.Contains(message, want) {
			t.Errorf("%q is not in: %s", want, message)
		}
	}
	// The first record was valid, but nothing from the app's fixtures was kept.
	if fxScalar(db, "SELECT count(*)::text FROM doc_customer_group") != "0" || fxScalar(db, "SELECT count(*)::text FROM fb_fixture") != "0" {
		t.Error("something was kept")
	}
	// The migrations before it stay applied, as they do for any later failure.
	if fxScalar(db, "SELECT to_regclass('base_seed')::text") != "base_seed" ||
		fxScalar(db, "SELECT count(*)::text FROM fb_app_migration WHERE app = 'base'") != "1" {
		t.Error("the migrations were rolled back")
	}
	// A link to a record nobody has fails the same way, and `base` is rolled back as a whole.
	_, err = db.Migrate(forgebase.New().App(fxBase(fxGroups(fxGroupsV1), fxLists(fxListsBadLink))), false)
	if err == nil || !strings.Contains(err.Error(), "price_list.json") {
		t.Errorf("%v", err)
	}
	if fxScalar(db, "SELECT count(*)::text FROM doc_customer_group") != "0" {
		t.Error("something was kept")
	}
	// Fixed, it applies.
	fxMigrate(t, db, fxBinary(fxGroups(fxGroupsV1)))
	if got := fxScalar(db, "SELECT count(*)::text FROM doc_customer_group"); got != "2" {
		t.Error(got)
	}
}

// Rust: fixtures.rs::each_app_has_its_own_transaction
func TestEachAppHasItsOwnTransaction(t *testing.T) {
	db := fbtest.NewDB(t)
	badRegions := fxFiles("fixtures/region.json", `{"doctype": "region", "records": [{"code": "n", "group": "Nowhere"}]}`)
	b := forgebase.New().App(fxBase(fxGroups(fxGroupsV1), fxNoFiles())).App(fxExt(badRegions))
	_, err := db.Migrate(b, false)
	if err == nil || !strings.Contains(err.Error(), "app `ext`") {
		t.Fatalf("%v", err)
	}
	// `base` came first and committed; `ext` kept nothing.
	if fxScalar(db, "SELECT count(*)::text FROM doc_customer_group") != "2" || fxScalar(db, "SELECT count(*)::text FROM doc_region") != "0" {
		t.Error("the transactions are not per app")
	}
	if got := db.Rows("SELECT DISTINCT app FROM fb_fixture"); !reflect.DeepEqual(got, [][]string{{"base"}}) {
		t.Errorf("%v", got)
	}
}

// Rust: fixtures.rs::fixtures_go_through_the_hooks_and_fire_webhooks
func TestFixturesGoThroughTheHooksAndFireWebhooks(t *testing.T) {
	db := fbtest.NewDB(t)
	fxMigrate(t, db, forgebase.New())
	db.Exec("INSERT INTO fb_webhook (name, doctype, events, url, secret) " +
		"VALUES ('w', 'customer_group', '{insert,update}', 'https://example.com/x', 's')")
	hooked := func(groups fs.FS) *forgebase.Binary {
		return forgebase.New().App(fxBase(groups, fxNoFiles()).Hooks("customer_group", fxRank99{}))
	}
	fxMigrate(t, db, hooked(fxGroups(fxGroupsV1)))
	// The validate hook filled rank of Retail (Wholesale has its own).
	wantRows(t, fxGroupRows(db), []string{"Retail|d1|99|1|fixture:base", "Wholesale|w1|2|1|fixture:base"})
	// One delivery per insert, as for any write.
	const deliveries = "SELECT count(*)::text FROM fb_job WHERE name = 'forgebase.deliver_webhook'"
	if got := fxScalar(db, deliveries); got != "2" {
		t.Error(got)
	}
	// An update of a record is a write too.
	fxMigrate(t, db, hooked(fxGroups(fxGroupsV2)))
	if got := fxScalar(db, deliveries); got != "3" {
		t.Error(got)
	}
}

// Rust: fixtures.rs::a_dry_run_reports_the_plan_and_writes_nothing
func TestADryRunReportsThePlanAndWritesNothing(t *testing.T) {
	db := fbtest.NewDB(t)
	b := fxBinary(fxGroups(fxGroupsV1))
	// On an empty database the framework migrations are pending: the fixtures are listed, not planned, and
	// nothing was created.
	dry := mustMigrate(t, db, b, true)
	if len(dry.Fixtures) != 2 || dry.FixturesPlanned {
		t.Errorf("%+v", dry)
	}
	if got := fxScalar(db, "SELECT to_regclass('fb_fixture')::text"); got != "" && got != "NULL" {
		t.Error(got)
	}
	// With only the framework tables, the DocType tables are missing too: all would be inserted.
	fxMigrate(t, db, forgebase.New())
	dry = mustMigrate(t, db, b, true)
	wantRows(t, fxSummary(dry), []string{"base 2 0 0 0", "ext 1 0 0 0"})
	if !dry.FixturesPlanned {
		t.Error("not planned")
	}
	if got := fxScalar(db, "SELECT to_regclass('doc_customer_group')::text"); got != "" && got != "NULL" {
		t.Error(got)
	}
	// After a real migrate: a new version and a site-made document.
	fxMigrate(t, db, b)
	db.Exec("DELETE FROM doc_region")
	before := fxSnapshot(db)
	v4 := func() *forgebase.Binary {
		return forgebase.New().App(fxBase(fxGroups(fxGroupsV4), fxNoFiles())).App(fxExt(fxFiles("fixtures/region.json", fxRegionsV1)))
	}
	dry = mustMigrate(t, db, v4(), true)
	// Retail changed, Online is new, Wholesale did not change; the region is unchanged for the checksum even
	// though its document is gone.
	wantRows(t, fxSummary(dry), []string{"base 1 1 0 1", "ext 0 0 0 1"})
	if !reflect.DeepEqual(fxSnapshot(db), before) {
		t.Error("the dry run wrote something")
	}
	if n := len(fxGroupRows(db)); n != 2 {
		t.Error(n)
	}
	// The real run does what the plan said.
	real := fxMigrate(t, db, v4())
	wantRows(t, fxSummary(real), fxSummary(dry))
}

// Rust: fixtures.rs::an_unchanged_fixture_is_not_compared_with_its_document
func TestAnUnchangedFixtureIsNotComparedWithItsDocument(t *testing.T) {
	// The checksum decides, not the document: a deleted document is no reason to write.
	db := fbtest.NewDB(t)
	b := fxBinary(fxGroups(fxGroupsV1))
	fxMigrate(t, db, b)
	db.Exec("DELETE FROM doc_region")
	again := fxMigrate(t, db, b)
	wantRows(t, fxSummary(again), []string{"base 0 0 0 2", "ext 0 0 0 1"})
}

// Rust: fixtures.rs::a_hook_that_changes_the_naming_field_fails_the_app_s_fixtures
func TestAHookThatChangesTheNamingFieldFailsTheAppsFixtures(t *testing.T) {
	db := fbtest.NewDB(t)
	shouting := forgebase.New().App(fxBase(fxGroups(fxGroupsV1), fxNoFiles()).Hooks("customer_group", fxShout{}))
	_, err := db.Migrate(shouting, false)
	if err == nil {
		t.Fatal("must fail")
	}
	for _, want := range []string{"a hook changed the naming field of fixture record `Retail` to `RETAIL`", "fixtures need a stable name",
		"record 1 (`Retail`)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q is not in: %v", want, err)
		}
	}
	if fxScalar(db, "SELECT count(*)::text FROM doc_customer_group") != "0" || fxScalar(db, "SELECT count(*)::text FROM fb_fixture") != "0" {
		t.Error("something was kept")
	}
}
