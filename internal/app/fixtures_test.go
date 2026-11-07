package app

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

// The startup checks of the fixtures (ADR 0022): every way a fixture file can be wrong is a startup error that
// names the app, the file and the record. No database needed. Ported from the tests of
// crates/forgebase/src/fixtures.rs (the checksum tests are in internal/httpapi/canonical_test.go, next to the
// code).

const fxItem = "name = \"item\"\nlabel = \"Item\"\nnaming = { field = \"code\" }\n" +
	"[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n" +
	"[[fields]]\nname = \"title\"\ntype = \"data\"\n" +
	"[[fields]]\nname = \"stamp\"\ntype = \"data\"\nread_only = true\n"
const fxRandom = "name = \"random\"\nlabel = \"R\"\n[[fields]]\nname = \"title\"\ntype = \"data\"\n"
const fxSeries = "name = \"series\"\nlabel = \"S\"\nnaming = { series = \"N-.####\" }\n[[fields]]\nname = \"title\"\ntype = \"data\"\n"
const fxLocked = "name = \"locked\"\nlabel = \"L\"\nsubmittable = true\n[[fields]]\nname = \"title\"\ntype = \"data\"\n"
const fxFlow = "name = \"flow\"\nlabel = \"F\"\nnaming = { field = \"code\" }\n" +
	"[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n" +
	"[workflow]\n[[workflow.states]]\nname = \"Open\"\ndocstatus = 0\n" +
	"[[workflow.states]]\nname = \"Done\"\ndocstatus = 0\n" +
	"[[workflow.transitions]]\naction = \"Finish\"\nfrom = \"Open\"\nto = \"Done\"\nroles = [\"Admin\"]\n"
const fxParent = "name = \"parent\"\nlabel = \"P\"\nnaming = { field = \"code\" }\n" +
	"[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n" +
	"[[fields]]\nname = \"rows\"\ntype = \"table\"\ntarget = \"row\"\n" +
	"[[fields]]\nname = \"size\"\ntype = \"int\"\n"
const fxRow = "name = \"row\"\nlabel = \"Row\"\nis_child = true\n[[fields]]\nname = \"qty\"\ntype = \"int\"\n"
const fxItemOfStranger = "name = \"item2\"\nlabel = \"I\"\nnaming = { field = \"code\" }\n" +
	"[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n"

func mapFS(pairs ...string) fstest.MapFS {
	m := fstest.MapFS{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = &fstest.MapFile{Data: []byte(pairs[i+1])}
	}
	return m
}

// fxCore is the app `core` with every DocType above.
func fxCore() *App {
	return NewApp("core").DocTypes(mapFS("doctypes/item.toml", fxItem, "doctypes/random.toml", fxRandom, "doctypes/series.toml", fxSeries,
		"doctypes/locked.toml", fxLocked, "doctypes/flow.toml", fxFlow, "doctypes/parent.toml", fxParent, "doctypes/row.toml", fxRow))
}

// fxShop is an app `shop` that depends on `core` and has these fixture files (path, json).
func fxShop(files ...string) *App { return NewApp("shop").DependsOn("core").Fixtures(mapFS(files...)) }

// fxProblems is the startup error of `core` and `shop`, as text.
func fxProblems(files ...string) string {
	if _, err := compose([]*App{fxCore(), fxShop(files...)}, ""); err != nil {
		return err.Error()
	}
	return ""
}

func fxRefused(t *testing.T, files []string, expected ...string) {
	t.Helper()
	text := fxProblems(files...)
	if text == "" {
		t.Fatalf("no problem found for %q", files)
	}
	for _, part := range expected {
		if !strings.Contains(text, part) {
			t.Errorf("`%s` is not in:\n%s", part, text)
		}
	}
	if !strings.Contains(text, "app `shop`: fixtures/f.json") {
		t.Errorf("the problem does not name app and file:\n%s", text)
	}
}

func fxFile(json string) []string { return []string{"fixtures/f.json", json} }

// Rust: fixtures.rs::tests::valid_fixtures_compose_and_are_in_file_and_record_order
func TestValidFixturesComposeAndAreInFileAndRecordOrder(t *testing.T) {
	composed, err := compose([]*App{fxCore(), fxShop(
		"fixtures/f.json", `{"doctype": "item", "records": [{"code": "b"}, {"code": "a", "title": "A"}]}`,
		"fixtures/p.json", `{"doctype": "parent", "records": [{"code": "p", "size": 3, "rows": [{"qty": 1}]}]}`,
	)}, "")
	if err != nil {
		t.Fatal(err)
	}
	var records []string
	for _, r := range composed.fixtureRecords("shop") {
		records = append(records, fmt.Sprintf("%s %s %d", r.DocType, r.Name, r.Index))
	}
	if got := strings.Join(records, ", "); got != "item b 1, item a 2, parent p 1" {
		t.Error(got)
	}
	if n := len(composed.fixtureRecords("core")); n != 0 {
		t.Error(n)
	}
	if n := len(composed.fixtureRecords("shop")[2].Document.Tables["rows"]); n != 1 {
		t.Error(n)
	}
}

// Rust: fixtures.rs::tests::bad_json_is_refused
func TestBadJSONIsRefused(t *testing.T) {
	fxRefused(t, fxFile("{\"doctype\": "), "invalid JSON")
	fxRefused(t, fxFile("[]"), "must be a JSON object")
	fxRefused(t, fxFile(`{"records": []}`), "needs a `doctype`")
	fxRefused(t, fxFile(`{"doctype": "item"}`), "needs `records`")
	fxRefused(t, fxFile(`{"doctype": "item", "records": [], "extra": 1}`), "unknown key `extra`")
}

// Rust: fixtures.rs::tests::an_unknown_doctype_is_refused
func TestAnUnknownDoctypeOfAFixtureIsRefused(t *testing.T) {
	fxRefused(t, fxFile(`{"doctype": "nothing", "records": []}`), "doctype `nothing` is not defined by any app")
}

// Rust: fixtures.rs::tests::a_doctype_of_an_app_that_is_not_a_dependency_is_refused
func TestADoctypeOfAnAppThatIsNotADependencyIsRefused(t *testing.T) {
	stranger := NewApp("stranger").DocTypes(mapFS("doctypes/item2.toml", fxItemOfStranger))
	shop := NewApp("shop").Fixtures(mapFS("fixtures/f.json", `{"doctype": "item2", "records": [{"code": "a"}]}`))
	_, err := compose([]*App{stranger, shop}, "")
	want := "app `shop`: fixtures/f.json: doctype `item2` belongs to app `stranger`, which app `shop` does not depend on"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%v", err)
	}
}

// Rust: fixtures.rs::tests::random_and_series_naming_are_refused_with_the_reason
func TestRandomAndSeriesNamingAreRefusedWithTheReason(t *testing.T) {
	for _, name := range []string{"random", "series"} {
		fxRefused(t, fxFile(fmt.Sprintf(`{"doctype": %q, "records": [{"title": "x"}]}`, name)), "the same on every site", "use field naming")
	}
	fxRefused(t, fxFile(`{"doctype": "random", "records": []}`), "doctype `random` uses random naming")
	fxRefused(t, fxFile(`{"doctype": "series", "records": []}`), "doctype `series` uses a naming series")
}

// Rust: fixtures.rs::tests::submittable_workflow_and_child_doctypes_are_refused
func TestSubmittableWorkflowAndChildDoctypesAreRefused(t *testing.T) {
	fxRefused(t, fxFile(`{"doctype": "locked", "records": []}`), "doctype `locked` is submittable", "document lifecycle")
	fxRefused(t, fxFile(`{"doctype": "flow", "records": []}`), "doctype `flow` has a workflow", "owns the state")
	fxRefused(t, fxFile(`{"doctype": "row", "records": []}`), "doctype `row` is a child DocType", "table field")
}

// Rust: fixtures.rs::tests::a_record_must_be_an_object_with_the_naming_field
func TestARecordMustBeAnObjectWithTheNamingField(t *testing.T) {
	fxRefused(t, fxFile(`{"doctype": "item", "records": [{"code": "a"}, 5]}`), "record 2: must be a JSON object")
	fxRefused(t, fxFile(`{"doctype": "item", "records": [{"title": "no code"}]}`), "record 1: must have a non-empty text value for `code`")
	for _, bad := range []string{`{"code": ""}`, `{"code": "  "}`, `{"code": null}`, `{"code": 5}`} {
		fxRefused(t, fxFile(fmt.Sprintf(`{"doctype": "item", "records": [%s]}`, bad)), "non-empty text value for `code`")
	}
}

// Rust: fixtures.rs::tests::a_record_with_unknown_read_only_or_mistyped_values_is_refused
func TestARecordWithUnknownReadOnlyOrMistypedValuesIsRefused(t *testing.T) {
	fxRefused(t, fxFile(`{"doctype": "item", "records": [{"code": "a", "nope": 1}]}`), "record 1 (`a`)", "`nope` is not a field of `item`")
	fxRefused(t, fxFile(`{"doctype": "item", "records": [{"code": "a", "stamp": "x"}]}`), "`stamp` is a read_only field")
	fxRefused(t, fxFile(`{"doctype": "parent", "records": [{"code": "a", "size": "big"}]}`), "record 1 (`a`)", "size")
}

// Rust: fixtures.rs::tests::the_rows_of_a_table_field_are_checked_against_the_child_doctype
func TestTheRowsOfATableFieldAreCheckedAgainstTheChildDoctype(t *testing.T) {
	fxRefused(t, fxFile(`{"doctype": "parent", "records": [{"code": "a", "rows": [{"qty": 1}, {"qty": 2, "nope": 3}]}]}`),
		"record 1 (`a`)", "`rows` row 2: `nope` is not a field of `row`")
}

// Rust: fixtures.rs::tests::a_number_that_could_parse_differently_is_a_startup_error_also_in_rows
func TestANumberThatCouldParseDifferentlyIsAStartupErrorAlsoInRows(t *testing.T) {
	fxRefused(t, fxFile(`{"doctype": "parent", "records": [{"code": "a", "size": -0}]}`), "record 1 (`a`)", "negative zero")
	fxRefused(t, fxFile(`{"doctype": "parent", "records": [{"code": "a", "rows": [{"qty": 1}, {"qty": 0.1234567890123456}]}]}`),
		"record 1 (`a`)", "cannot be represented the same way in every build")
	fxRefused(t, fxFile(`{"doctype": "parent", "records": [{"code": "a", "size": 18446744073709551616}]}`), "record 1 (`a`)", "number `")
}

// Rust: fixtures.rs::tests::a_duplicate_inside_an_app_is_refused
func TestAFixtureDuplicateInsideAnAppIsRefused(t *testing.T) {
	fxRefused(t, fxFile(`{"doctype": "item", "records": [{"code": "a"}, {"code": "a", "title": "again"}]}`),
		"record 2 (`a`): appears twice in this app")
}

// Rust: fixtures.rs::tests::two_files_for_one_doctype_are_refused. The files are read in the order of their
// names (the list of Rust's fixtures! is the order of the file system here), so the second file is f.json.
func TestTwoFilesForOneDoctypeAreRefused(t *testing.T) {
	text := fxProblems(
		"fixtures/a.json", `{"doctype": "item", "records": [{"code": "a"}]}`,
		"fixtures/f.json", `{"doctype": "item", "records": [{"code": "b"}]}`)
	want := "app `shop`: fixtures/f.json: doctype `item` already has fixtures in fixtures/a.json; use one file per DocType"
	if !strings.Contains(text, want) {
		t.Error(text)
	}
}

// Rust: fixtures.rs::tests::a_duplicate_across_apps_names_both_apps
func TestAFixtureDuplicateAcrossAppsNamesBothApps(t *testing.T) {
	record := `{"doctype": "item", "records": [{"code": "a"}]}`
	other := NewApp("other").DependsOn("core").Fixtures(mapFS("fixtures/f.json", record))
	_, err := compose([]*App{fxCore(), fxShop("fixtures/f.json", record), other}, "")
	want := "app `other`: fixtures/f.json: record 1 (`a`): `item a` is also a fixture of app `shop`; a record belongs to one app"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%v", err)
	}
}

// Not in Rust: the files are the .json files of the file system at any depth, and a file that is not UTF-8 or a
// name that starts with a dot does not count.
func TestFixturesReadsTheJSONFilesOfAFileSystem(t *testing.T) {
	fsys := mapFS(
		"fixtures/b.json", `{"doctype": "item", "records": [{"code": "b"}]}`,
		"fixtures/sub/a.json", `{"doctype": "parent", "records": [{"code": "a"}]}`,
		"fixtures/.hidden.json", `{`,
		"fixtures/README.md", `not json`)
	a := NewApp("shop").DependsOn("core").Fixtures(fsys)
	composed, err := compose([]*App{fxCore(), a}, "")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, r := range composed.fixtureRecords("shop") {
		files = append(files, r.File)
	}
	if got := strings.Join(files, " "); got != "fixtures/b.json fixtures/sub/a.json" {
		t.Error(got)
	}
	bad := NewApp("shop").DependsOn("core").Fixtures(fstest.MapFS{"fixtures/f.json": {Data: []byte("{\"doctype\": \"item\", \"records\": [], \"x\": \"\xff\"}")}})
	if _, err := compose([]*App{fxCore(), bad}, ""); err == nil || !strings.Contains(err.Error(), "fixtures/f.json: cannot read: stream did not contain valid UTF-8") {
		t.Errorf("%v", err)
	}
}
