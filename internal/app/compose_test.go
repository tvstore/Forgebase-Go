package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// Ported from the unit tests of crates/forgebase/src/compose.rs (the app model parts and, since P6, the report
// tests; the overlay tests arrive with P7).

const composeCustomer = "name = \"customer\"\nlabel = \"C\"\n"
const composeLinking = "name = \"{n}\"\nlabel = \"L\"\n[[fields]]\nname = \"f\"\ntype = \"link\"\ntarget = \"{t}\"\n"

func linkingTOML(name, target string) string {
	return strings.NewReplacer("{n}", name, "{t}", target).Replace(composeLinking)
}

// files is an fs.FS with DocType files, from (path, toml) pairs.
func files(pairs ...string) fstest.MapFS {
	m := fstest.MapFS{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = &fstest.MapFile{Data: []byte(pairs[i+1])}
	}
	return m
}

func composeCRM() *App {
	return NewApp("crm").DocTypes(files("doctypes/customer.toml", composeCustomer))
}

// scratchDir is a directory with files.
func scratchDir(t *testing.T, named map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range named {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Rust: compose.rs::the_doctypes_directory_is_the_app_local_and_may_link_to_every_app
func TestTheDocTypesDirectoryIsTheAppLocalAndMayLinkToEveryApp(t *testing.T) {
	dir := scratchDir(t, map[string]string{"order.toml": linkingTOML("order", "customer")})
	c, startup := compose([]*App{composeCRM()}, dir)
	if startup != nil {
		t.Fatal(startup)
	}
	if owner, _ := c.registry.Owner("customer"); owner != "crm" {
		t.Errorf("customer is owned by %q", owner)
	}
	if owner, _ := c.registry.Owner("order"); owner != localApp {
		t.Errorf("order is owned by %q", owner)
	}
	if c.appCount() != 1 {
		t.Errorf("%d apps", c.appCount())
	}
}

// Rust: compose.rs::an_app_cannot_link_to_the_doctypes_directory
func TestAnAppCannotLinkToTheDocTypesDirectory(t *testing.T) {
	// crm is registered first and cannot depend on local; a link to it is an error.
	dir := scratchDir(t, map[string]string{"extra.toml": "name = \"extra\"\nlabel = \"E\"\n"})
	crm := NewApp("crm").DocTypes(files("doctypes/order.toml", linkingTOML("order", "extra")))
	_, startup := compose([]*App{crm}, dir)
	if startup == nil {
		t.Fatal("refused")
	}
	text := startup.Error()
	if !strings.Contains(text, "belongs to app `local`") || !strings.Contains(text, "does not depend on `local`") {
		t.Error(text)
	}
}

// Rust: compose.rs::a_doctype_of_the_directory_cannot_reuse_the_name_of_an_app_doctype
func TestADocTypeOfTheDirectoryCannotReuseTheNameOfAnAppDocType(t *testing.T) {
	dir := scratchDir(t, map[string]string{"customer.toml": composeCustomer})
	_, startup := compose([]*App{composeCRM()}, dir)
	if startup == nil {
		t.Fatal("refused")
	}
	text := startup.Error()
	if !strings.HasPrefix(text, "cannot load DocType files:\n") ||
		!strings.Contains(text, "customer.toml: doctype `customer` is already defined by app `crm` (doctypes/customer.toml)") {
		t.Error(text)
	}
}

// Rust: compose.rs::a_cross_app_link_is_reported_together_with_other_reference_errors
func TestACrossAppLinkIsReportedTogetherWithOtherReferenceErrors(t *testing.T) {
	// sales links to crm's customer without depending on crm, and also to ghost, which nobody defines: both
	// problems are in one error.
	sales := NewApp("sales").DocTypes(files(
		"doctypes/a.toml", linkingTOML("a", "customer"),
		"doctypes/b.toml", linkingTOML("b", "ghost")))
	_, startup := compose([]*App{composeCRM(), sales}, "")
	if startup == nil {
		t.Fatal("refused")
	}
	text := startup.Error()
	if !strings.Contains(text, "invalid app setup:\n") || !strings.Contains(text, "app `sales` does not depend on `crm`") {
		t.Error(text)
	}
	if !strings.Contains(text, "cannot load DocType files:\n") || !strings.Contains(text, "app `sales`: doctypes/b.toml:") || !strings.Contains(text, "`ghost`") {
		t.Error(text)
	}
}

// Rust: compose.rs::startup_errors_print_the_app_list_before_the_file_list
func TestStartupErrorsPrintTheAppListBeforeTheFileList(t *testing.T) {
	e := &StartupError{apps: []string{"a"}, files: []string{"b", "c"}}
	if got, want := e.Error(), "invalid app setup:\n  - a\ncannot load DocType files:\n  - b\n  - c"; got != want {
		t.Errorf("%q", got)
	}
	if got := e.Problems(); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("%v", got)
	}
	only := &StartupError{files: []string{"b"}}
	if got := only.Error(); got != "cannot load DocType files:\n  - b" {
		t.Errorf("%q", got)
	}
}

// Rust: run.rs (StartupError::for_site): a problem found only for the apps of one site names the site.
func TestForSitePrefixesEveryProblem(t *testing.T) {
	e := (&StartupError{apps: []string{"a"}, files: []string{"b"}}).forSite("shop")
	if got, want := e.Error(), "invalid app setup:\n  - site `shop`: a\ncannot load DocType files:\n  - site `shop`: b"; got != want {
		t.Errorf("%q", got)
	}
}

// Not in Rust: hooks are registered in app order, and in registration order within an app, on the registry.
func TestHooksAreRegisteredInAppOrderThenRegistrationOrder(t *testing.T) {
	crm := composeCRM().Hooks("customer", &named{name: "crm1"}).Hooks("customer", &named{name: "crm2"})
	sales := NewApp("sales").DependsOn("crm").Hooks("customer", &named{name: "sales"})
	c, startup := compose([]*App{crm, sales}, "")
	if startup != nil {
		t.Fatal(startup)
	}
	var order []string
	for _, h := range c.registry.Hooks("customer") {
		order = append(order, h.(*named).name)
	}
	if !reflect.DeepEqual(order, []string{"crm1", "crm2", "sales"}) {
		t.Errorf("%v", order)
	}
}

func totalsReport() *Report {
	return NewReport("SELECT 1;").Roles("Manager").Param(LinkParam("customer", "customer")).Column(IntColumn("n"))
}

// Rust: compose.rs::reports_are_registered_by_full_name_with_the_sql_trimmed
func TestReportsAreRegisteredByFullNameWithTheSQLTrimmed(t *testing.T) {
	sales := NewApp("sales").DependsOn("crm").Report("totals", totalsReport())
	c, startup := compose([]*App{composeCRM(), sales}, "")
	if startup != nil {
		t.Fatal(startup)
	}
	report := c.registry.Report("sales.totals")
	if report == nil || report.SQL != "SELECT 1" || report.Label != "Totals" || len(c.registry.Reports()) != 1 || c.registry.Report("totals") != nil {
		t.Fatalf("%+v", report)
	}
	if c.reportCount() != 1 {
		t.Fatal(c.reportCount())
	}
}

// Rust: compose.rs::every_report_composition_error_is_reported_together
func TestEveryReportCompositionErrorIsReportedTogether(t *testing.T) {
	sales := NewApp("sales").DependsOn("crm").
		Report("Bad Name", totalsReport()).
		Report("dup", totalsReport()).
		Report("dup", totalsReport()).
		Report("nobody", NewReport("SELECT 1").Column(IntColumn("n"))).
		Report("links", NewReport("SELECT 1").Roles("A").Param(LinkParam("p", "ghost")).Param(DataParam("p")).Column(LinkColumn("c", "ghost"))).
		Report("empty", NewReport(" ; ").Roles("A").Param(IntParam("x"))).
		Report("limits", NewReport("SELECT 1").Roles("A").Column(IntColumn("n")).Timeout(301*time.Second).MaxRows(100_001))
	_, startup := compose([]*App{composeCRM(), sales}, "")
	if startup == nil {
		t.Fatal("refused")
	}
	text := startup.Error()
	for _, needle := range []string{
		"report `Bad Name`: name \"Bad Name\"",
		"report `dup` is registered more than once",
		"report `nobody`: no roles",
		"report `links`: param `p` links to unknown DocType `ghost`",
		"report `links`: column `c` links to unknown DocType `ghost`",
		"report `links`: param `p` is declared more than once",
		"report `empty`: the SQL is empty",
		"report `empty`: no columns",
		"report `limits`: timeout",
		"report `limits`: max_rows 100001",
	} {
		if !strings.Contains(text, needle) {
			t.Errorf("%s in\n%s", needle, text)
		}
	}
}

// Rust: compose.rs::a_report_may_only_link_to_its_own_app_and_its_dependencies
func TestAReportMayOnlyLinkToItsOwnAppAndItsDependencies(t *testing.T) {
	// sales does not depend on crm, so its report cannot name crm's customer.
	sales := NewApp("sales").Report("totals", totalsReport())
	_, startup := compose([]*App{composeCRM(), sales}, "")
	if startup == nil || !strings.Contains(startup.Error(),
		"report `totals`: param `customer` links to `customer`, which belongs to app `crm`; app `sales` does not depend on `crm`") {
		t.Fatal(startup)
	}
}

// Rust: compose.rs::a_report_may_not_link_to_a_child_doctype
func TestAReportMayNotLinkToAChildDoctype(t *testing.T) {
	app := NewApp("a").
		DocTypes(files(
			"doctypes/parent.toml", "name = \"parent\"\nlabel = \"P\"\n[[fields]]\nname = \"rows\"\ntype = \"table\"\ntarget = \"child\"\n",
			"doctypes/child.toml", "name = \"child\"\nlabel = \"C\"\nis_child = true\n")).
		Report("r", NewReport("SELECT 1").Roles("A").Param(LinkParam("c", "child")).Column(IntColumn("n")))
	_, startup := compose([]*App{app}, "")
	if startup == nil || !strings.Contains(startup.Error(), "a child DocType") {
		t.Fatal(startup)
	}
}

// Not in Rust: a nil report is a startup error, like a nil hook set.
func TestANilReportIsAStartupError(t *testing.T) {
	_, startup := compose([]*App{NewApp("a").Report("r", nil)}, "")
	if startup == nil || !strings.Contains(startup.Error(), "app `a`: report `r` is nil") {
		t.Fatal(startup)
	}
}
