#!/usr/bin/env bash
# Prepares a database for the Playwright smoke test (desk/e2e/smoke.spec.ts). Run it from the
# repository root with an EMPTY (scratch) database in FORGEBASE_DATABASE_URL; the URL is read by
# the forgebase tool from the environment and is never printed here.
#
#   FORGEBASE_DATABASE_URL=postgres://... desk/e2e/seed.sh
#
# It writes the DocTypes the test serves to $E2E_DIR/doctypes (default target/e2e): the DocTypes of
# the example app examples/sales plus a role "Sales Clerk" that may read, write and create sales
# orders but not submit them (no example role is like that). That e2e-only role is why the test
# uses --doctypes, and not the binary of the sales app (whose DocTypes are embedded and cannot take
# the extra role). The binary is the example `e2e_app` of the forgebase crate (the stock command plus
# one app that defines the query report "Sales by customer", M8.4: a report can only come from an app
# compiled into the binary; build it with `cargo build -p forgebase --example e2e_app`). The sales
# app's hooks do not run in this test: `amount` and `total` (read_only, computed by those hooks) stay empty here. It then migrates, and creates:
#   - e2e_manager, role "Sales Manager": every right on sales orders (and customers);
#   - e2e_clerk, role "Sales Clerk": no submit, cancel, amend or delete right;
#   - e2e_admin, role "Administrator": the only user who may share a saved view (M8.3);
#   - the customer "ada@example.com" (full name "Ada Lovelace", the customer's `title_field`) for the orders:
#     a Link to it shows that title in the list and the form (UX.4, link titles).
#   - a separate DocType `purchase_request` with a workflow (M8.2), so that the sales order flow keeps its plain
#     Submit: Draft -> Pending Approval (Sales Clerk or Sales Manager), Pending Approval -> Approved (Sales Manager,
#     submits), Pending Approval -> Draft "Reject" (Sales Manager), Approved -> Cancelled (Sales Manager, cancels).
# M10.4 (desk/e2e/restrict.spec.ts) adds, with the stock commands of the same binary:
#   - e2e_sales, role "Sales User" with no restriction (it must not see level 1, `credit_limit`);
#   - e2e_retail, role "Sales User", user permission customer_group = Retail; e2e_retail_manager, role "Sales Manager", the
#     same restriction (a manager who may create groups, until restricted);
#   - e2e_auditor, role "Credit Auditor" (e2e-only, written below): may read and write customers at level 0 but only read
#     level 1 (`credit_limit`), so the field is read-only for it;
#   - the customer groups Retail and Wholesale, and the customers "Rita Retail" (Retail, credit limit 500.00) and
#     "Walt Wholesale" (Wholesale, credit limit 9000.00), next to Ada, who has no group (credit limit 1250.50);
#   - a site custom field `custom_region` (data) on customer, added with `customize field add`.
# Passwords come from E2E_PASSWORD (default below): a throw-away password of a throw-away database.
# Start the server with:  target/debug/examples/e2e_app serve --doctypes "$E2E_DIR/doctypes"
set -euo pipefail

: "${FORGEBASE_DATABASE_URL:?set FORGEBASE_DATABASE_URL to an empty scratch database}"
BIN="${FORGEBASE_BIN:-target/debug/examples/e2e_app}"
E2E_DIR="${E2E_DIR:-target/e2e}"
PASSWORD="${E2E_PASSWORD:-e2e-test-password}"
DOCTYPES="$E2E_DIR/doctypes"

rm -rf "$DOCTYPES"
mkdir -p "$DOCTYPES"
cp examples/sales/doctypes/*.toml "$DOCTYPES/"
# e2e only: the example sales order has an approval workflow (M8.5), which refuses the plain Submit and
# Cancel that the smoke test uses on sales orders. Strip the [workflow] table and its [[workflow.states]] and
# [[workflow.transitions]] tables, so the sales order stays a plain submittable DocType here; the workflow
# is tested on the separate DocType purchase_request below. awk drops every line from a table header that
# starts with [workflow or [[workflow. up to the next table header that does not.
awk '
  /^[[:space:]]*\[/ { skip = ($0 ~ /^[[:space:]]*\[\[?workflow[].]/) }
  !skip
' "$DOCTYPES/sales_order.toml" > "$DOCTYPES/sales_order.toml.tmp"
mv "$DOCTYPES/sales_order.toml.tmp" "$DOCTYPES/sales_order.toml"
cat >> "$DOCTYPES/sales_order.toml" <<'TOML'

# e2e only: may work on orders but not submit them.
[[permissions]]
role = "Sales Clerk"
read = true
write = true
create = true
TOML
cat >> "$DOCTYPES/customer.toml" <<'TOML'

# e2e only: needed to read the Link target of a sales order.
[[permissions]]
role = "Sales Clerk"
read = true

# e2e only (M10.4): reads and writes customers, but reads level 1 (`credit_limit`) only: the field is read-only for it.
[[permissions]]
role = "Credit Auditor"
read = true
write = true

[[permissions]]
role = "Credit Auditor"
level = 1
read = true
TOML

# e2e only: a DocType with a workflow. The clerk may write but not submit; the transition roles narrow
# what the rights allow (ADR 0015): "Approve" needs the role Sales Manager and the submit right.
cat > "$DOCTYPES/purchase_request.toml" <<'TOML'
name = "purchase_request"
label = "Purchase Request"
submittable = true
naming = { series = "PR-.YYYY.-.#####" }
title_field = "title"
icon = "clipboard-list"
list_fields = ["title", "amount"]

[[fields]]
name = "title"
type = "data"
required = true

[[fields]]
name = "amount"
type = "decimal"
precision = 12
scale = 2

[[permissions]]
role = "Sales Clerk"
read = true
write = true
create = true

[[permissions]]
role = "Sales Manager"
read = true
write = true
create = true
delete = true
submit = true
cancel = true
amend = true

[workflow]

[[workflow.states]]
name = "Draft"
docstatus = 0
style = "neutral"

[[workflow.states]]
name = "Pending Approval"
docstatus = 0
style = "warning"

[[workflow.states]]
name = "Approved"
docstatus = 1
style = "success"

[[workflow.states]]
name = "Cancelled"
docstatus = 2
style = "danger"

[[workflow.transitions]]
action = "Submit for Approval"
from = "Draft"
to = "Pending Approval"
roles = ["Sales Clerk", "Sales Manager"]

[[workflow.transitions]]
action = "Approve"
from = "Pending Approval"
to = "Approved"
roles = ["Sales Manager"]

[[workflow.transitions]]
action = "Reject"
from = "Pending Approval"
to = "Draft"
roles = ["Sales Manager"]

[[workflow.transitions]]
action = "Cancel Approval"
from = "Approved"
to = "Cancelled"
roles = ["Sales Manager"]
TOML

"$BIN" migrate --doctypes "$DOCTYPES" > /dev/null
printf '%s\n' "$PASSWORD" | "$BIN" user create e2e_manager --role "Sales Manager" > /dev/null
printf '%s\n' "$PASSWORD" | "$BIN" user create e2e_clerk --role "Sales Clerk" > /dev/null
printf '%s\n' "$PASSWORD" | "$BIN" user create e2e_admin --role "Administrator" > /dev/null
# M10.4: the customer groups (the sales app's migration is not part of this binary), customers with a credit
# limit (level 1), restricted users and a site custom field.
for group in Retail Wholesale; do
  "$BIN" doc insert customer_group --doctypes "$DOCTYPES" --data "{\"title\":\"$group\"}" > /dev/null
done
"$BIN" doc insert customer --doctypes "$DOCTYPES" \
  --data '{"email":"ada@example.com","full_name":"Ada Lovelace","credit_limit":"1250.50"}' > /dev/null
"$BIN" doc insert customer --doctypes "$DOCTYPES" \
  --data '{"email":"rita@example.com","full_name":"Rita Retail","customer_group":"Retail","credit_limit":"500.00"}' > /dev/null
"$BIN" doc insert customer --doctypes "$DOCTYPES" \
  --data '{"email":"walt@example.com","full_name":"Walt Wholesale","customer_group":"Wholesale","credit_limit":"9000.00"}' > /dev/null
printf '%s\n' "$PASSWORD" | "$BIN" user create e2e_auditor --role "Credit Auditor" > /dev/null
printf '%s\n' "$PASSWORD" | "$BIN" user create e2e_sales --role "Sales User" > /dev/null
printf '%s\n' "$PASSWORD" | "$BIN" user create e2e_retail --role "Sales User" > /dev/null
printf '%s\n' "$PASSWORD" | "$BIN" user create e2e_retail_manager --role "Sales Manager" > /dev/null
for user in e2e_retail e2e_retail_manager; do
  "$BIN" user permission add --user "$user" --doctype customer_group --value Retail --doctypes "$DOCTYPES" > /dev/null
done
printf 'name = "custom_region"\nlabel = "Region"\ntype = "data"\n' \
  | "$BIN" customize field add --doctype customer --field - --doctypes "$DOCTYPES" > /dev/null
echo "seeded: users e2e_manager, e2e_clerk, e2e_admin, e2e_auditor, e2e_sales, e2e_retail and e2e_retail_manager, customers ada@example.com, rita@example.com and walt@example.com, DocTypes in $DOCTYPES"
