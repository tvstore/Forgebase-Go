package forgebase_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// Query reports through the public API (M8.4, ADR 0016), written with forgebase/testing: a fixture app that
// defines reports, the role gate over HTTP, the static check of the SQL at migrate (a parameter count, a column
// count and a type that do not fit), the reports of an app that is not installed, and the effect of a report
// that tries to write. Ported from crates/forgebase/tests/reports.rs.

const reportCustomer = "name = \"customer\"\nlabel = \"Customer\"\nnaming = { field = \"code\" }\n" +
	"title_field = \"full_name\"\n" +
	"[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n" +
	"[[fields]]\nname = \"full_name\"\ntype = \"data\"\n" +
	"[[permissions]]\nrole = \"Sales Manager\"\nread = true\n"

const reportSeed = "CREATE TABLE sale (customer text, orders int, total numeric, day date); " +
	"INSERT INTO sale VALUES ('c-1', 2, 30.5, '2026-10-02'), ('c-2', 5, 80, '2026-10-09'), ('c-1', 1, 4.25, '2026-11-01')"

const byCustomerSQL = "SELECT customer, sum(orders)::int8 AS orders, sum(total) AS total " +
	"FROM sale WHERE day >= $1 AND day <= $2 AND ($3::text IS NULL OR customer = $3) " +
	"GROUP BY customer ORDER BY total DESC;\n"

func reportCRM() *forgebase.App {
	return forgebase.NewApp("crm").
		DocTypes(docTypes("doctypes/customer.toml", reportCustomer)).
		Migrations(migrations(mig{1, "sales", reportSeed}))
}

func salesReport() *forgebase.Report {
	return forgebase.NewReport(byCustomerSQL).
		Label("Sales by customer").
		Roles("Sales Manager").
		Param(forgebase.DateParam("from").Required()).
		Param(forgebase.DateParam("to").Required()).
		Param(forgebase.LinkParam("customer", "customer")).
		Column(forgebase.LinkColumn("customer", "customer")).
		Column(forgebase.IntColumn("orders")).
		Column(forgebase.DecimalColumn("total"))
}

func reportSales() *forgebase.App {
	return forgebase.NewApp("sales").DependsOn("crm").
		Report("by_customer", salesReport()).
		Report("defaults", forgebase.NewReport("SELECT $1::text, $2::int8").
			Roles("Sales Manager").
			Timeout(5*time.Second).
			MaxRows(5).
			Param(forgebase.SelectParam("period", "month", "year").DefaultValue(forgebase.Text("year"))).
			Param(forgebase.IntParam("limit").Required().DefaultValue(forgebase.Int(10))).
			Column(forgebase.DataColumn("period")).
			Column(forgebase.IntColumn("limit"))).
		Report("danger", forgebase.NewReport("DELETE FROM sale RETURNING orders").
			Roles("Sales Manager").
			Column(forgebase.IntColumn("orders")))
}

func reportBinary() *forgebase.Binary { return forgebase.New().App(reportCRM()).App(reportSales()) }

func reportMigrated(t *testing.T, b *forgebase.Binary) *fbtest.DB {
	t.Helper()
	db := fbtest.NewDB(t)
	if _, err := db.Migrate(b, false); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func jsonAt(v any, path ...any) any {
	for _, step := range path {
		switch s := step.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[s]
		case int:
			l, ok := v.([]any)
			if !ok || s >= len(l) {
				return nil
			}
			v = l[s]
		}
	}
	return v
}

// Rust: reports.rs::the_roles_gate_the_list_and_the_run_and_the_administrator_always_passes
func TestTheRolesGateTheListAndTheRunAndTheAdministratorAlwaysPasses(t *testing.T) {
	b := reportBinary()
	db := reportMigrated(t, b)
	db.CreateUser("mia", "Sales Manager")
	db.CreateUser("cat", "Clerk")
	db.CreateUser("root", "Administrator")
	anon := db.Client(b)
	mia, cat, root := anon.Login("mia"), anon.Login("cat"), anon.Login("root")

	listed := mia.Get("/api/v1/report")
	if listed.Status != 200 {
		t.Fatalf("%s", listed.Text)
	}
	var names []string
	for _, r := range listed.DataList() {
		names = append(names, jsonAt(r, "name").(string))
	}
	// By label: "Danger", "Defaults", "Sales by customer".
	if !reflect.DeepEqual(names, []string{"sales.danger", "sales.defaults", "sales.by_customer"}) {
		t.Errorf("%v", names)
	}
	if jsonAt(listed.Data(), 1, "params", 0, "default") != "year" ||
		!reflect.DeepEqual(jsonAt(listed.Data(), 1, "params", 0, "options"), []any{"month", "year"}) ||
		jsonAt(listed.Data(), 1, "params", 1, "default") != float64(10) {
		t.Errorf("%s", listed.Text)
	}
	if got := len(root.Get("/api/v1/report").DataList()); got != 3 {
		t.Errorf("%d", got)
	}
	// The Clerk: no report listed, and a run is the same 404 as for a report that is not there.
	if got := cat.Get("/api/v1/report").DataList(); len(got) != 0 {
		t.Errorf("%v", got)
	}
	body := map[string]any{"params": map[string]any{"from": "2026-10-01", "to": "2026-10-31"}}
	refused := cat.Post("/api/v1/report/sales.by_customer", body)
	unknown := cat.Post("/api/v1/report/sales.nothing", body)
	if refused.Status != 404 || unknown.Status != 404 || refused.Text != unknown.Text {
		t.Errorf("%d %s / %d %s", refused.Status, refused.Text, unknown.Status, unknown.Text)
	}

	reply := mia.Post("/api/v1/report/sales.by_customer", body)
	if reply.Status != 200 {
		t.Fatalf("%s", reply.Text)
	}
	want := []any{[]any{"c-2", float64(5), "80"}, []any{"c-1", float64(2), "30.5"}}
	if !reflect.DeepEqual(jsonAt(reply.Data(), "rows"), want) || jsonAt(reply.Data(), "truncated") != false {
		t.Errorf("%s", reply.Text)
	}
	if admin := root.Post("/api/v1/report/sales.by_customer", body); admin.Status != 200 {
		t.Errorf("%s", admin.Text)
	}
}

// Rust: reports.rs::defaults_fill_absent_params_and_limits_are_the_reports_own
func TestDefaultsFillAbsentParamsAndLimitsAreTheReportsOwn(t *testing.T) {
	b := reportBinary()
	db := reportMigrated(t, b)
	db.CreateUser("mia", "Sales Manager")
	mia := db.Client(b).Login("mia")
	reply := mia.Post("/api/v1/report/sales.defaults", map[string]any{})
	if reply.Status != 200 || !reflect.DeepEqual(jsonAt(reply.Data(), "rows"), []any{[]any{"year", float64(10)}}) {
		t.Errorf("%d %s", reply.Status, reply.Text)
	}
	given := mia.Post("/api/v1/report/sales.defaults", map[string]any{"params": map[string]any{"period": "month", "limit": 3}})
	if !reflect.DeepEqual(jsonAt(given.Data(), "rows"), []any{[]any{"month", float64(3)}}) {
		t.Errorf("%s", given.Text)
	}
}

// Rust: reports.rs::a_report_that_writes_fails_and_the_data_is_unchanged
func TestAReportThatWritesFailsAndTheDataIsUnchanged(t *testing.T) {
	b := reportBinary()
	db := reportMigrated(t, b)
	db.CreateUser("mia", "Sales Manager")
	mia := db.Client(b).Login("mia")
	reply := mia.Post("/api/v1/report/sales.danger", map[string]any{})
	if reply.Status != 500 || reply.ErrorCode() != "internal" {
		t.Errorf("%d %s", reply.Status, reply.Text)
	}
	if got := db.Rows("SELECT count(*)::text FROM sale"); !reflect.DeepEqual(got, [][]string{{"3"}}) {
		t.Errorf("%v", got)
	}
}

// Rust: reports.rs::migrate_checks_every_report_and_names_the_report_and_the_problem
func TestMigrateChecksEveryReportAndNamesTheReportAndTheProblem(t *testing.T) {
	broken := func(r *forgebase.Report) *forgebase.Binary {
		return forgebase.New().App(reportCRM()).App(forgebase.NewApp("sales").DependsOn("crm").Report("r", r))
	}
	for _, c := range []struct {
		report   *forgebase.Report
		expected string
	}{
		{
			// The SQL has three parameters, two are declared.
			forgebase.NewReport("SELECT customer FROM sale WHERE day >= $1 AND day <= $2 AND customer = $3").Roles("A").
				Param(forgebase.DateParam("from")).Param(forgebase.DateParam("to")).Column(forgebase.DataColumn("customer")),
			"report `sales.r`: the SQL has 3 parameter(s) but 2 are declared",
		},
		{
			forgebase.NewReport("SELECT customer, orders FROM sale").Roles("A").Column(forgebase.DataColumn("customer")),
			"report `sales.r`: the SQL returns 2 column(s) but 1 are declared",
		},
		{
			forgebase.NewReport("SELECT customer, orders FROM sale").Roles("A").
				Column(forgebase.DataColumn("customer")).Column(forgebase.DataColumn("orders")),
			"column 2 (`orders`) is declared data, but the SQL returns int4",
		},
		{
			forgebase.NewReport("SELECT customer FROM sale WHERE orders > $1").Roles("A").
				Param(forgebase.DateParam("min")).Column(forgebase.DataColumn("customer")),
			"parameter $1 (`min`) is declared date, but the SQL expects int4",
		},
	} {
		b := broken(c.report)
		db := fbtest.NewDB(t)
		_, err := db.Migrate(b, false)
		if err == nil || !strings.Contains(err.Error(), c.expected) {
			t.Fatalf("%s in %v", c.expected, err)
		}
		// The migrations were applied before the check: the table of the app migration exists.
		if got := db.Rows("SELECT count(*)::text FROM sale"); !reflect.DeepEqual(got, [][]string{{"3"}}) {
			t.Errorf("%v", got)
		}
		// The helper gives the same messages without failing a migrate.
		found := fbtest.CheckReports(b, db)
		if len(found) != 1 || !strings.Contains(found[0], c.expected) {
			t.Errorf("%v", found)
		}
	}
}

// Rust: reports.rs::a_dry_run_does_not_check_and_a_report_that_fits_passes
func TestADryRunDoesNotCheckAndAReportThatFitsPasses(t *testing.T) {
	b := reportBinary()
	db := fbtest.NewDB(t)
	// Nothing exists yet, so the SQL could not be prepared: a dry run does not try.
	if _, err := db.Migrate(b, true); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := db.Migrate(b, false); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if problems := fbtest.CheckReports(b, db); len(problems) != 0 {
		t.Errorf("%v", problems)
	}
	// A second migrate checks again and passes.
	if _, err := db.Migrate(b, false); err != nil {
		t.Fatalf("migrate again: %v", err)
	}
}

// Rust: reports.rs::the_reports_of_an_app_that_is_not_installed_are_neither_listed_nor_run
func TestTheReportsOfAnAppThatIsNotInstalledAreNeitherListedNorRun(t *testing.T) {
	// A site that installs only crm composes only crm: the registry has no report of sales.
	full := reportBinary()
	onlyCRM := forgebase.New().App(reportCRM())
	db := reportMigrated(t, full)
	db.CreateUser("mia", "Sales Manager")
	db.CreateUser("root", "Administrator")
	for _, who := range []string{"mia", "root"} {
		client := db.Client(onlyCRM).Login(who)
		if got := client.Get("/api/v1/report").DataList(); len(got) != 0 {
			t.Errorf("%s: %v", who, got)
		}
		reply := client.Post("/api/v1/report/sales.by_customer", map[string]any{"params": map[string]any{"from": "2026-10-01", "to": "2026-10-31"}})
		if reply.Status != 404 {
			t.Errorf("%s: %d", who, reply.Status)
		}
	}
	if got := len(db.Client(full).Login("mia").Get("/api/v1/report").DataList()); got != 3 {
		t.Errorf("%d", got)
	}
}

// Rust: reports.rs::a_startup_error_names_the_report
func TestAStartupErrorNamesTheReport(t *testing.T) {
	// A report with no roles and no columns is a startup error that names it, among the others.
	if err := forgebase.New().App(reportCRM()).Check(); err != nil {
		t.Fatal(err)
	}
	bad := forgebase.New().App(reportCRM()).App(forgebase.NewApp("sales").DependsOn("crm").Report("r", forgebase.NewReport("SELECT 1")))
	err := bad.Check()
	if err == nil || !strings.Contains(err.Error(), "report `r`: no roles") || !strings.Contains(err.Error(), "report `r`: no columns") {
		t.Fatalf("%v", err)
	}
}
