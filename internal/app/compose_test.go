package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Ported from the unit tests of crates/forgebase/src/compose.rs (the app model parts, since P6 the report
// tests and, since P7.3, the overlay tests at the end of the file).

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

// ---- overlays (M10.1, ADR 0023) ---------------------------------------------------------------------------

const composeTier = "[[fields]]\nname = \"tier\"\ntype = \"select\"\noptions = [\"a\", \"b\"]\n"

// composeExt is an app that depends on depends and extends target with the overlay files (path, toml pairs).
func composeExt(name string, depends []string, target string, overlays ...string) *App {
	return NewApp(name).DependsOn(depends...).ExtendDocType(target, files(overlays...))
}

func composeProblems(t *testing.T, apps ...*App) []string {
	t.Helper()
	_, startup := compose(apps, "")
	if startup == nil {
		t.Fatal("the apps were accepted")
	}
	return startup.Problems()
}

func hasProblem(found []string, needle string) bool {
	for _, p := range found {
		if strings.Contains(p, needle) {
			return true
		}
	}
	return false
}

// Rust: compose.rs::an_overlay_adds_its_fields_after_the_doctype_in_app_order_then_file_order
func TestAnOverlayAddsItsFieldsAfterTheDocTypeInAppOrderThenFileOrder(t *testing.T) {
	c, startup := compose([]*App{
		composeCRM(),
		composeExt("first", []string{"crm"}, "customer",
			"overlays/a1.toml", composeTier, "overlays/a2.toml", "[[fields]]\nname = \"note\"\ntype = \"text\"\n"),
		composeExt("second", []string{"crm"}, "customer",
			"overlays/b.toml", "[[fields]]\nname = \"rank\"\ntype = \"int\"\nunique = true\nrequired = true\n"),
	}, "")
	if startup != nil {
		t.Fatal(startup)
	}
	customer := c.registry.Get("customer")
	var names []string
	for _, f := range customer.Fields {
		names = append(names, f.Name)
	}
	if !reflect.DeepEqual(names, []string{"tier", "note", "rank"}) {
		t.Fatalf("%v", names)
	}
	if !customer.Field("rank").Unique {
		t.Error("rank is not unique")
	}
	if owner, ok := c.registry.FieldOwner("customer", "tier"); !ok || owner != "first" {
		t.Errorf("tier: %q", owner)
	}
	if owner, ok := c.registry.FieldOwner("customer", "rank"); !ok || owner != "second" {
		t.Errorf("rank: %q", owner)
	}
}

// Rust: compose.rs::an_overlay_may_target_a_transitive_dependency
func TestAnOverlayMayTargetATransitiveDependency(t *testing.T) {
	mid := NewApp("mid").DependsOn("crm")
	top := composeExt("top", []string{"mid"}, "customer", "overlays/o.toml", composeTier)
	if _, startup := compose([]*App{composeCRM(), mid, top}, ""); startup != nil {
		t.Fatal(startup)
	}
}

// Rust: compose.rs::overlay_problems_name_the_app_and_the_file
func TestOverlayProblemsNameTheAppAndTheFile(t *testing.T) {
	o := []string{"overlays/o.toml", composeTier}
	found := composeProblems(t, composeCRM(), composeExt("e", []string{"crm"}, "nope", o...))
	if len(found) != 1 || !strings.HasPrefix(found[0], "app `e`: overlays/o.toml: overlay for unknown DocType `nope`") {
		t.Fatalf("%v", found)
	}

	// Not a dependency: e does not depend on crm.
	found = composeProblems(t, composeCRM(), composeExt("e", nil, "customer", o...))
	if !strings.Contains(found[0], "app `e`: overlays/o.toml: overlay for `customer`, which belongs to app `crm`; app `e` does not depend on `crm`") {
		t.Fatalf("%v", found)
	}

	// The app's own DocType.
	own := composeCRM().ExtendDocType("customer", files(o...))
	found = composeProblems(t, own)
	if !strings.Contains(found[0], "app `crm`: overlays/o.toml: overlay for `customer`, a DocType of app `crm` itself") {
		t.Fatalf("%v", found)
	}

	// A child DocType.
	parent := NewApp("p").DocTypes(files(
		"doctypes/parent.toml", "name = \"parent\"\nlabel = \"P\"\n[[fields]]\nname = \"rows\"\ntype = \"table\"\ntarget = \"child\"\n",
		"doctypes/child.toml", "name = \"child\"\nlabel = \"C\"\nis_child = true\n"))
	found = composeProblems(t, parent, composeExt("e", []string{"p"}, "child", o...))
	if !strings.Contains(found[0], "overlay for `child`, a child DocType") {
		t.Fatalf("%v", found)
	}

	// A Table field, the reserved prefix.
	found = composeProblems(t, composeCRM(), composeExt("e", []string{"crm"}, "customer",
		"overlays/t.toml", "[[fields]]\nname = \"rows\"\ntype = \"table\"\ntarget = \"customer\"\n"))
	if !strings.Contains(found[0], "app `e`: overlays/t.toml: field `rows` has type table") {
		t.Fatalf("%v", found)
	}
	found = composeProblems(t, composeCRM(), composeExt("e", []string{"crm"}, "customer",
		"overlays/p.toml", "[[fields]]\nname = \"custom_x\"\ntype = \"int\"\n"))
	if !strings.Contains(found[0], "app `e`: overlays/p.toml: field `custom_x` starts with `custom_`") {
		t.Fatalf("%v", found)
	}

	// Files that do not parse, or hold more than fields: file problems naming the file.
	for _, c := range []struct{ path, text, expected string }{
		{"overlays/x.toml", "", "app `e`: overlays/x.toml: the overlay has no [[fields]]"},
		{"overlays/s.toml", "label = \"x\"\n[[fields]]\nname = \"a\"\ntype = \"int\"\n", "app `e`: overlays/s.toml: cannot parse the definition"},
	} {
		_, startup := compose([]*App{composeCRM(), composeExt("e", []string{"crm"}, "customer", c.path, c.text)}, "")
		if startup == nil {
			t.Fatalf("%s was accepted", c.path)
		}
		if text := startup.Error(); !strings.Contains(text, "cannot load DocType files") || !strings.Contains(text, c.expected) {
			t.Errorf("%s", text)
		}
	}

	// A field with its own problem (the same check as a DocType file).
	found = composeProblems(t, composeCRM(), composeExt("e", []string{"crm"}, "customer",
		"overlays/b.toml", "[[fields]]\nname = \"a\"\ntype = \"select\"\n"))
	if !strings.Contains(found[0], "app `e`: overlays/b.toml:") {
		t.Fatalf("%v", found)
	}
}

// Rust: compose.rs::a_collision_names_both_apps
func TestAnOverlayCollisionNamesBothApps(t *testing.T) {
	found := composeProblems(t, composeCRM(),
		composeExt("one", []string{"crm"}, "customer", "overlays/one.toml", composeTier),
		composeExt("two", []string{"crm"}, "customer", "overlays/two.toml", composeTier))
	if len(found) != 1 || !strings.Contains(found[0],
		"app `two`: overlays/two.toml: field `tier` of doctype `customer` is already defined by app `one` (overlays/one.toml)") {
		t.Fatalf("%v", found)
	}
	// With a field of the DocType itself.
	crmWithEmail := NewApp("crm").DocTypes(files("doctypes/customer.toml",
		"name = \"customer\"\nlabel = \"C\"\n[[fields]]\nname = \"email\"\ntype = \"data\"\n"))
	found = composeProblems(t, crmWithEmail, composeExt("one", []string{"crm"}, "customer",
		"overlays/own.toml", "[[fields]]\nname = \"email\"\ntype = \"data\"\n"))
	if !strings.Contains(found[0], "already defined by app `crm` (doctypes/customer.toml); app `one` cannot add a field of the same name") {
		t.Fatalf("%v", found)
	}
	// Twice in one app, and twice in one file.
	found = composeProblems(t, composeCRM(), composeExt("one", []string{"crm"}, "customer",
		"overlays/a.toml", composeTier, "overlays/b.toml", composeTier))
	if !strings.Contains(found[0], "already defined by app `one` (overlays/a.toml)") {
		t.Fatalf("%v", found)
	}
	found = composeProblems(t, composeCRM(), composeExt("one", []string{"crm"}, "customer",
		"overlays/t.toml", "[[fields]]\nname = \"x\"\ntype = \"int\"\n[[fields]]\nname = \"x\"\ntype = \"int\"\n"))
	if !strings.Contains(found[0], "overlays/t.toml") || !strings.Contains(found[0], "already exists") {
		t.Fatalf("%v", found)
	}
}

// Rust: compose.rs::an_overlay_link_follows_the_app_dependency_rule
func TestAnOverlayLinkFollowsTheAppDependencyRule(t *testing.T) {
	other := NewApp("other").DocTypes(files("doctypes/thing.toml", "name = \"thing\"\nlabel = \"T\"\n"))
	link := func(target string) []string {
		return []string{"overlays/o.toml", "[[fields]]\nname = \"buddy\"\ntype = \"link\"\ntarget = \"" + target + "\"\n"}
	}
	// e extends crm but does not depend on other: the link is refused at the overlay.
	found := composeProblems(t, composeCRM(), other, composeExt("e", []string{"crm"}, "customer", link("thing")...))
	if !hasProblem(found, "app `e`: overlays/o.toml: field `buddy` of doctype `customer` refers to `thing`, which belongs to app `other`") {
		t.Fatalf("%v", found)
	}
	// A target nobody defines is reported at the overlay file.
	g := []string{"overlays/g.toml", "[[fields]]\nname = \"buddy\"\ntype = \"link\"\ntarget = \"ghost\"\n"}
	found = composeProblems(t, composeCRM(), composeExt("e", []string{"crm"}, "customer", g...))
	ok := false
	for _, p := range found {
		ok = ok || (strings.Contains(p, "overlays/g.toml") && strings.Contains(p, "ghost"))
	}
	if !ok {
		t.Fatalf("%v", found)
	}
	// A link into the extended app is fine.
	if _, startup := compose([]*App{composeCRM(), composeExt("e", []string{"crm"}, "customer", link("customer")...)}, ""); startup != nil {
		t.Fatalf("a link into the extended app is fine: %v", startup)
	}
}

// Rust: compose.rs::the_prefix_custom_is_reserved_for_the_fields_of_app_doctypes_too
func TestThePrefixCustomIsReservedForTheFieldsOfAppDocTypesToo(t *testing.T) {
	app := NewApp("a").DocTypes(files("doctypes/t.toml",
		"name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"custom_x\"\ntype = \"int\"\n"))
	found := composeProblems(t, app)
	if !strings.Contains(found[0], "app `a`: doctypes/t.toml: field `custom_x` of doctype `t` starts with `custom_`") {
		t.Fatalf("%v", found)
	}
}

type composeNoop struct{ core.BaseHooks }

// Rust: compose.rs::hooks_of_the_owning_app_see_the_overlay_fields
func TestHooksOfTheOwningAppSeeTheOverlayFields(t *testing.T) {
	// The hook registry is checked against the extended DocType: a hook may be registered for the overlay's
	// target.
	app := composeExt("e", []string{"crm"}, "customer", "overlays/o.toml", composeTier).Hooks("customer", composeNoop{})
	c, startup := compose([]*App{composeCRM(), app}, "")
	if startup != nil {
		t.Fatal(startup)
	}
	if n := len(c.registry.Hooks("customer")); n != 1 {
		t.Fatalf("%d hook sets", n)
	}
}

// Not in Rust: a nil Param or Column of a report is a startup error, like a nil report.
func TestANilReportParamOrColumnIsAStartupError(t *testing.T) {
	for want, report := range map[string]*Report{
		"app `a`: report `r`: Param is given nil":  NewReport("SELECT 1").Roles("A").Param(nil),
		"app `a`: report `r`: Column is given nil": NewReport("SELECT 1").Roles("A").Column(nil),
	} {
		_, startup := compose([]*App{NewApp("a").Report("r", report)}, "")
		if startup == nil || !strings.Contains(startup.Error(), want) {
			t.Fatal(want, startup)
		}
	}
}
