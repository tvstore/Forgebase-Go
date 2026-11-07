package app

import (
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Ported from the unit tests of crates/forgebase/src/reports.rs.

// Rust: reports.rs::labels_default_to_the_humanized_name
func TestLabelsDefaultToTheHumanizedName(t *testing.T) {
	report := NewReport("SELECT 1").
		Roles("A").
		Param(DateParam("from_date")).
		Column(IntColumn("order_count").Label("Orders")).
		Column(DataColumn("customer_name"))
	def := report.definition("sales", "sales_by_customer")
	if def.Label != "Sales By Customer" || def.Params[0].Label != "From Date" || def.Columns[0].Label != "Orders" ||
		def.Columns[1].Label != "Customer Name" || def.FullName() != "sales.sales_by_customer" {
		t.Fatalf("%+v", def)
	}
}

// Rust: reports.rs::the_sql_loses_its_trailing_semicolons_and_whitespace
func TestTheSQLLosesItsTrailingSemicolonsAndWhitespace(t *testing.T) {
	for _, c := range [][2]string{
		{"SELECT 1;\n", "SELECT 1"},
		{"  SELECT 1 ; ;  \n", "SELECT 1"},
		{"SELECT ';' ;", "SELECT ';'"},
		{"SELECT 1", "SELECT 1"},
	} {
		if got := NewReport(c[0]).definition("a", "r").SQL; got != c[1] {
			t.Errorf("%q: %q, want %q", c[0], got, c[1])
		}
	}
}

// Rust: reports.rs::the_builders_set_the_definition
func TestTheBuildersSetTheDefinition(t *testing.T) {
	def := NewReport("SELECT 1").
		Label("Totals").
		Roles("A", "B").
		Roles("C").
		Timeout(5*time.Second).
		MaxRows(7).
		Param(SelectParam("kind", "x", "y").Required()).
		Param(LinkParam("customer", "customer").DefaultValue(core.Text("C"))).
		Column(LinkColumn("customer", "customer")).
		definition("sales", "totals")
	if len(def.Roles) != 3 || def.Roles[0] != "A" || def.Roles[2] != "C" || def.Timeout != 5*time.Second || def.MaxRows != 7 {
		t.Fatalf("%+v", def)
	}
	if !def.Params[0].Required || def.Params[1].Default == nil || !def.Params[1].Default.Equal(core.Text("C")) ||
		def.Params[1].Kind.Name() != "link" || def.Columns[0].Kind.Name() != "link" {
		t.Fatalf("%+v", def)
	}
	if problems := def.Problems(); len(problems) != 0 {
		t.Fatalf("%v", problems)
	}
}
