package forgebase_test

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tvstore/forgebase-go"
)

// Startup errors of the app model (M5.1, ADR 0012, ADR 0028): every way the apps of a binary can be wrong is
// reported by Binary.Check, all problems together. No database needed. Ported from
// crates/forgebase/tests/startup.rs.

const startupCustomer = `
name = "customer"
label = "Customer"
[[fields]]
name = "title"
type = "data"
`

// startupOrder links to customer.
const startupOrder = `
name = "order"
label = "Order"
[[fields]]
name = "customer"
type = "link"
target = "customer"
`

// startupInvoice links to order (of a dependency) and to customer (of a dependency of a dependency).
const startupInvoice = `
name = "invoice"
label = "Invoice"
[[fields]]
name = "order"
type = "link"
target = "order"
[[fields]]
name = "customer"
type = "link"
target = "customer"
`

const startupChild = `
name = "order_line"
label = "Order Line"
is_child = true
`

type nothing struct{ forgebase.BaseHooks }

func startupCRM() *forgebase.App {
	return forgebase.NewApp("crm").DocTypes(docTypes("doctypes/customer.toml", startupCustomer))
}

func startupSales() *forgebase.App {
	return forgebase.NewApp("sales").DependsOn("crm").DocTypes(docTypes("doctypes/order.toml", startupOrder))
}

// problems are the problems of a binary, one string per problem.
func problems(b *forgebase.Binary) []string {
	err := b.Check()
	if err == nil {
		return nil
	}
	startup, ok := err.(*forgebase.StartupError)
	if !ok {
		panic("Check returned a " + reflect.TypeOf(err).String())
	}
	return startup.Problems()
}

func only(t *testing.T, b *forgebase.Binary) string {
	t.Helper()
	all := problems(b)
	if len(all) != 1 {
		t.Fatalf("%d problems: %#v", len(all), all)
	}
	return all[0]
}

// Rust: startup.rs::a_valid_setup_has_no_problem
func TestAValidSetupHasNoProblem(t *testing.T) {
	if p := problems(forgebase.New()); p != nil {
		t.Errorf("%v", p)
	}
	if p := problems(forgebase.New().App(startupCRM()).App(startupSales())); p != nil {
		t.Errorf("%v", p)
	}
	// Check returns a nil error, not a typed nil.
	if err := forgebase.New().Check(); err != nil {
		t.Errorf("%#v", err)
	}
}

// Rust: startup.rs::an_invalid_app_name_is_reported
func TestAnInvalidAppNameIsReported(t *testing.T) {
	for _, bad := range []string{"Sales", "my app", "", "1st", "sales-app"} {
		message := only(t, forgebase.New().App(forgebase.NewApp(bad)))
		if !strings.Contains(message, "app name "+quote(bad)+" ") {
			t.Errorf("%q: %s", bad, message)
		}
	}
	// local names the --doctypes directory.
	if message := only(t, forgebase.New().App(forgebase.NewApp("local"))); !strings.Contains(message, "`local` is reserved") {
		t.Error(message)
	}
	// forgebase is the framework's own (its jobs, P5).
	if message := only(t, forgebase.New().App(forgebase.NewApp("forgebase"))); !strings.Contains(message, "`forgebase` is reserved") {
		t.Error(message)
	}
}

func quote(s string) string { return `"` + s + `"` }

// Rust: startup.rs::a_duplicate_app_name_is_reported
func TestADuplicateAppNameIsReported(t *testing.T) {
	if got := only(t, forgebase.New().App(startupCRM()).App(forgebase.NewApp("crm"))); got != "app `crm` is registered more than once" {
		t.Error(got)
	}
}

// Rust: startup.rs::a_missing_dependency_is_reported
func TestAMissingDependencyIsReported(t *testing.T) {
	if got := problems(forgebase.New().App(startupSales()))[0]; got != "app `sales` depends on `crm`, which is not registered" {
		t.Error(got)
	}
}

// Rust: startup.rs::a_dependency_registered_after_its_dependent_is_reported_and_never_sorted
func TestADependencyRegisteredAfterItsDependentIsReportedAndNeverSorted(t *testing.T) {
	all := problems(forgebase.New().App(startupSales()).App(startupCRM()))
	if !strings.HasPrefix(all[0], "app `sales` depends on `crm`, which is registered after it;") {
		t.Errorf("%v", all)
	}
}

// Rust: startup.rs::an_app_cannot_depend_on_itself
func TestAnAppCannotDependOnItself(t *testing.T) {
	if got := only(t, forgebase.New().App(forgebase.NewApp("crm").DependsOn("crm"))); got != "app `crm` depends on itself" {
		t.Error(got)
	}
}

// Rust: startup.rs::a_doctype_defined_by_two_apps_names_both
func TestADocTypeDefinedByTwoAppsNamesBoth(t *testing.T) {
	other := forgebase.NewApp("other").DocTypes(docTypes("doctypes/customer.toml", startupCustomer))
	message := only(t, forgebase.New().App(startupCRM()).App(other))
	for _, part := range []string{"app `other`", "doctype `customer` is already defined by app `crm`", "doctypes/customer.toml"} {
		if !strings.Contains(message, part) {
			t.Errorf("missing %q in %s", part, message)
		}
	}
}

// Rust: startup.rs::a_doctype_defined_twice_in_one_app_names_both_files
func TestADocTypeDefinedTwiceInOneAppNamesBothFiles(t *testing.T) {
	app := forgebase.NewApp("crm").DocTypes(docTypes("doctypes/a.toml", startupCustomer, "doctypes/b.toml", startupCustomer))
	message := only(t, forgebase.New().App(app))
	if !strings.Contains(message, "doctypes/b.toml") || !strings.Contains(message, "already defined in doctypes/a.toml") {
		t.Error(message)
	}
}

// Rust: startup.rs::a_broken_doctype_file_names_the_app_and_the_file
func TestABrokenDocTypeFileNamesTheAppAndTheFile(t *testing.T) {
	app := forgebase.NewApp("crm").DocTypes(docTypes("doctypes/ok.toml", startupCustomer, "doctypes/broken.toml", "name = "))
	if message := only(t, forgebase.New().App(app)); !strings.HasPrefix(message, "app `crm`: doctypes/broken.toml: ") {
		t.Error(message)
	}
}

// Rust: startup.rs::hooks_for_an_unknown_doctype_are_reported
func TestHooksForAnUnknownDocTypeAreReported(t *testing.T) {
	if got := only(t, forgebase.New().App(startupCRM().Hooks("nope", nothing{}))); got != "app `crm`: hooks registered for unknown DocType `nope`" {
		t.Error(got)
	}
}

// Rust: startup.rs::hooks_for_a_doctype_of_an_app_that_is_not_a_dependency_are_reported
func TestHooksForADocTypeOfAnAppThatIsNotADependencyAreReported(t *testing.T) {
	// other is registered, but sales does not depend on it.
	other := forgebase.NewApp("other").DocTypes(docTypes("doctypes/customer.toml", startupCustomer))
	b := forgebase.New().App(other).App(forgebase.NewApp("sales").Hooks("customer", nothing{}))
	want := "app `sales`: hooks registered for DocType `customer` of app `other`, which `sales` does not depend on"
	if got := only(t, b); got != want {
		t.Error(got)
	}
}

// Rust: startup.rs::hooks_for_a_doctype_of_a_dependency_are_allowed_also_through_another_app
func TestHooksForADocTypeOfADependencyAreAllowedAlsoThroughAnotherApp(t *testing.T) {
	// sales -> crm, and a third app that depends on sales only may hook crm's DocType.
	third := forgebase.NewApp("third").DependsOn("sales").Hooks("customer", nothing{}).Hooks("order", nothing{})
	b := forgebase.New().
		App(startupCRM().Hooks("customer", nothing{})).
		App(startupSales().Hooks("customer", nothing{})).
		App(third)
	if p := problems(b); p != nil {
		t.Errorf("%v", p)
	}
}

// Rust: startup.rs::hooks_for_a_child_doctype_are_reported
func TestHooksForAChildDocTypeAreReported(t *testing.T) {
	app := forgebase.NewApp("crm").DocTypes(docTypes(
		"doctypes/order.toml", "name = \"order\"\nlabel = \"O\"\n[[fields]]\nname = \"lines\"\ntype = \"table\"\ntarget = \"order_line\"\n",
		"doctypes/order_line.toml", startupChild)).Hooks("order_line", nothing{})
	message := only(t, forgebase.New().App(app))
	if !strings.HasPrefix(message, "app `crm`: hooks for `order_line`: ") || !strings.Contains(message, "child") {
		t.Error(message)
	}
}

// Rust: startup.rs::a_link_to_an_app_that_is_not_a_dependency_is_reported
func TestALinkToAnAppThatIsNotADependencyIsReported(t *testing.T) {
	// sales links to crm's customer but does not depend on crm.
	sales := forgebase.NewApp("sales").DocTypes(docTypes("doctypes/order.toml", startupOrder))
	message := only(t, forgebase.New().App(startupCRM()).App(sales))
	for _, part := range []string{"app `sales`", "doctypes/order.toml", "field `customer` of doctype `order` refers to `customer`",
		"belongs to app `crm`", "does not depend on `crm`"} {
		if !strings.Contains(message, part) {
			t.Errorf("missing %q in %s", part, message)
		}
	}
}

// Rust: startup.rs::a_link_to_a_dependency_of_a_dependency_is_allowed
func TestALinkToADependencyOfADependencyIsAllowed(t *testing.T) {
	// invoice -> order (sales) and, through sales, customer (crm) is reachable too.
	billing := forgebase.NewApp("billing").DependsOn("sales").DocTypes(docTypes("doctypes/invoice.toml", startupInvoice))
	if p := problems(forgebase.New().App(startupCRM()).App(startupSales()).App(billing)); p != nil {
		t.Errorf("%v", p)
	}
}

// Rust: startup.rs::unknown_link_targets_still_name_the_app_and_file
func TestUnknownLinkTargetsStillNameTheAppAndFile(t *testing.T) {
	app := forgebase.NewApp("sales").DocTypes(docTypes("doctypes/order.toml", startupOrder))
	message := only(t, forgebase.New().App(app))
	if !strings.HasPrefix(message, "app `sales`: doctypes/order.toml: ") || !strings.Contains(message, "links to unknown doctype `customer`") {
		t.Error(message)
	}
}

// migrationApp is an app with the migration files of fsys.
func migrationApp(fsys fs.FS) *forgebase.Binary {
	return forgebase.New().App(forgebase.NewApp("crm").Migrations(fsys))
}

func sqlFiles(names ...string) fs.FS {
	m := fstest.MapFS{}
	for _, name := range names {
		m[name] = &fstest.MapFile{Data: []byte("SELECT 1")}
	}
	return m
}

// Rust: startup.rs::migration_versions_must_be_positive_and_increasing. The versions come from the file
// names here, and a file system lists them in order: a listing that is out of order, or has a negative
// version, cannot exist; a repeated one and version 0 can, and a name that is not NNNN_name.sql is refused.
func TestMigrationVersionsMustBePositiveAndIncreasing(t *testing.T) {
	if p := problems(migrationApp(sqlFiles("migrations/0001_a.sql", "migrations/0002_b.sql"))); p != nil {
		t.Errorf("%v", p)
	}
	// Written out of order, the versions are still sorted.
	if p := problems(migrationApp(sqlFiles("migrations/0002_a.sql", "migrations/0001_b.sql"))); p != nil {
		t.Errorf("%v", p)
	}
	for _, c := range []struct {
		files []string
		want  string
	}{
		{[]string{"migrations/0001_a.sql", "migrations/0001_b.sql"},
			"app `crm`: migration versions must increase: 1 (b, migrations/0001_b.sql) comes after 1 (a)"},
		{[]string{"migrations/0000_a.sql"}, "app `crm`: migration version 0 (a, migrations/0000_a.sql) must be positive"},
		{[]string{"migrations/-3_a.sql"}, "app `crm`: migration file migrations/-3_a.sql must be named NNNN_name.sql (the digits are the version, for example 0001_backfill.sql)"},
		{[]string{"migrations/backfill.sql"}, "app `crm`: migration file migrations/backfill.sql must be named NNNN_name.sql (the digits are the version, for example 0001_backfill.sql)"},
		{[]string{"migrations/0001.sql"}, "app `crm`: migration file migrations/0001.sql must be named NNNN_name.sql (the digits are the version, for example 0001_backfill.sql)"},
		{[]string{"migrations/99999999999_a.sql"}, "app `crm`: migration file migrations/99999999999_a.sql: the version 99999999999 is too large (the most is 2147483647)"},
	} {
		if got := only(t, migrationApp(sqlFiles(c.files...))); got != c.want {
			t.Errorf("%v:\n got  %s\n want %s", c.files, got, c.want)
		}
	}
	// Other files are ignored: a README, a file in a hidden directory.
	if p := problems(migrationApp(sqlFiles("migrations/0001_a.sql", "migrations/.x/bad.sql"))); p != nil {
		t.Errorf("%v", p)
	}
	if p := problems(migrationApp(fstest.MapFS{"migrations/README.md": {Data: []byte("x")}})); p != nil {
		t.Errorf("%v", p)
	}
}

// Rust: startup.rs::migrations_added_in_several_calls_must_still_increase
func TestMigrationsAddedInSeveralCallsMustStillIncrease(t *testing.T) {
	app := forgebase.NewApp("crm").
		Migrations(sqlFiles("migrations/0002_a.sql")).
		Migrations(sqlFiles("migrations/0001_b.sql"))
	if message := only(t, forgebase.New().App(app)); !strings.Contains(message, "must increase") {
		t.Error(message)
	}
}

// Rust: startup.rs::every_problem_is_reported_together_and_printed_in_two_lists
func TestEveryProblemIsReportedTogetherAndPrintedInTwoLists(t *testing.T) {
	b := forgebase.New().
		App(forgebase.NewApp("Bad Name")).
		App(startupSales().Hooks("nope", nothing{})).
		App(forgebase.NewApp("broken").DocTypes(docTypes("doctypes/x.toml", "name = ")))
	err := b.Check()
	startup, ok := err.(*forgebase.StartupError)
	if !ok {
		t.Fatalf("%T", err)
	}
	text := startup.Error()
	if got := strings.Count(text, "\n  - "); got != len(startup.Problems()) {
		t.Errorf("%d bullets for %d problems:\n%s", got, len(startup.Problems()), text)
	}
	if len(startup.Problems()) < 3 {
		t.Errorf("%s", text)
	}
	if !strings.HasPrefix(text, "invalid app setup:\n  - app name \"Bad Name\" ") {
		t.Errorf("%s", text)
	}
	if !strings.Contains(text, "\ncannot load DocType files:\n  - app `broken`: doctypes/x.toml: ") {
		t.Errorf("%s", text)
	}
	// A broken file hides the reference and hook checks, which would only add follow-up noise.
	if strings.Contains(text, "hooks registered for unknown") {
		t.Errorf("%s", text)
	}
}

// Not in Rust: a nil app and a nil hook set are startup errors, not panics; a file system that cannot be
// read is a startup error naming the app.
func TestNilsAndUnreadableFileSystemsAreStartupErrors(t *testing.T) {
	if got := only(t, forgebase.New().App(nil)); got != "app #1 is nil" {
		t.Error(got)
	}
	if got := only(t, forgebase.New().App(forgebase.NewApp("crm").Hooks("customer", nil))); got != "app `crm`: hooks for `customer` are nil" {
		t.Error(got)
	}
	if got := only(t, forgebase.New().App(forgebase.NewApp("crm").DocTypes(nil))); got != "app `crm`: DocTypes: the file system is nil" {
		t.Error(got)
	}
	// The app still reports its other problems.
	all := problems(forgebase.New().App(forgebase.NewApp("Crm").Migrations(nil)))
	if len(all) != 2 {
		t.Errorf("%v", all)
	}
}

// Rust: startup.rs::run_from_returns_exit_codes_instead_of_exiting_the_process
func TestRunArgsReturnsExitCodesInsteadOfExitingTheProcess(t *testing.T) {
	// Help is a success, and bad usage is 2: both are returned, the test process goes on.
	stock := forgebase.New
	if code := stock().RunArgs("--help"); code != 0 {
		t.Errorf("--help: %d", code)
	}
	if code := stock().RunArgs("bogus"); code != 2 {
		t.Errorf("bogus: %d", code)
	}
	// No apps and no --doctypes.
	if code := stock().RunArgs("migrate"); code != 2 {
		t.Errorf("migrate: %d", code)
	}
	// With an app, --doctypes is not needed: the startup check comes first, so a broken app is exit code 1
	// before any database is touched.
	if code := forgebase.New().App(startupSales()).RunArgs("migrate"); code != 1 {
		t.Errorf("a broken binary: %d", code)
	}
}
