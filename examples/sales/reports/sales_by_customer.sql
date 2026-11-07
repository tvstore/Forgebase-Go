-- The report "Sales by customer" (App in sales.go declares its parameters and columns).
--
-- Positional parameters, in the order the report declares them: $1 from, $2 to (both required,
-- dates, inclusive), $3 customer (optional: NULL when absent, then every customer).
-- Columns, in order: customer, orders, total, last_order.
--
-- A Link field is stored as the id of the target row (a uuid), and the name of the document is
-- in the column `name`: the join turns the link into the customer's name, which is what a Link
-- column and a Link parameter carry.
--
-- Only submitted, not cancelled orders count (docstatus = 1). Row-level rules do not apply in a
-- report: the roles named in the report decide who may see this.
SELECT c.name AS customer,
       count(*) AS orders,
       sum(o.total) AS total,
       max(o.order_date) AS last_order
FROM doc_sales_order o
JOIN doc_customer c ON c.id = o.customer
WHERE o.docstatus = 1
  AND o.order_date BETWEEN $1::date AND $2::date
  AND ($3::text IS NULL OR c.name = $3::text)
GROUP BY c.name
-- Biggest customers first; the name decides ties, in byte order so the result does not depend on
-- the database's locale.
ORDER BY sum(o.total) DESC, c.name COLLATE "C"
