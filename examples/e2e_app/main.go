// Command e2e_app is the binary of the desk's Playwright tests (desk/e2e/seed.sh, the e2e job of CI): the
// forgebase command with one app, e2e, that defines a query report (M8.4, ADR 0016). It is the Go equivalent
// of the Rust example crates/forgebase/examples/e2e_app.rs.
//
// The stock forgebase binary has no apps, and a report can only come from an app compiled into the binary. The
// DocTypes of the tests still come from --doctypes DIR (the example app's DocTypes plus roles that only the
// tests use, see the seed script), so the report reads their tables (doc_sales_order and the others) by name;
// an app cannot link to the DocTypes of that directory, so its customer column is plain text. Build it with
//
//	go build -o bin/e2e_app ./examples/e2e_app
package main

import (
	"os"

	"github.com/tvstore/forgebase-go"
)

// salesByCustomer is the orders by customer in a date range: how many and their total, from the rows of the
// items.
const salesByCustomer = `
SELECT c.full_name AS customer,
       count(DISTINCT so.id) AS orders,
       COALESCE(sum(i.qty * i.rate), 0) AS total,
       max(so.order_date) AS last_order
FROM doc_sales_order so
JOIN doc_customer c ON c.id = so.customer
LEFT JOIN doc_sales_order_item i ON i.parent_id = so.id
WHERE so.order_date >= $1 AND so.order_date <= $2
  AND ($3::text IS NULL OR (CASE so.docstatus WHEN 0 THEN 'Draft' WHEN 1 THEN 'Submitted' ELSE 'Cancelled' END) = $3)
  AND (COALESCE($4::bool, false) OR so.docstatus <> 2)
GROUP BY c.full_name
ORDER BY c.full_name COLLATE "C";
`

func e2e() *forgebase.App {
	return forgebase.NewApp("e2e").Report("sales_by_customer",
		forgebase.NewReport(salesByCustomer).
			Label("Sales by customer").
			Roles("Sales Manager").
			Param(forgebase.DateParam("from").Label("From").Required()).
			Param(forgebase.DateParam("to").Label("To").Required()).
			Param(forgebase.SelectParam("status", "Draft", "Submitted", "Cancelled").Label("Status")).
			Param(forgebase.CheckParam("include_cancelled").Label("Include cancelled").DefaultValue(forgebase.Bool(false))).
			Column(forgebase.DataColumn("customer").Label("Customer")).
			Column(forgebase.IntColumn("orders").Label("Orders")).
			Column(forgebase.DecimalColumn("total").Label("Total")).
			Column(forgebase.DateColumn("last_order").Label("Last order")))
}

func main() { os.Exit(forgebase.New().App(e2e()).Run()) }
