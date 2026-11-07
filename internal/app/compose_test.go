package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// Ported from the unit tests of crates/forgebase/src/compose.rs (the app model parts; the overlay tests
// arrive with P7, the report tests with P6).

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
