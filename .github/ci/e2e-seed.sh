#!/usr/bin/env bash
# TEMPORARY, phase-scoped seed of the end-to-end job (the Go port, ADR 0026). It is desk/e2e/seed.sh with the Go
# binary, and without the steps that need commands of a later phase. It is removed in P7, when desk/e2e/seed.sh
# runs unchanged: the Rust seed needs `customize field add` and `user permission add` (P7), for the site custom
# field and the restricted users of desk/e2e/restrict.spec.ts.
#
# The binary is examples/e2e_app (P6, the Go equivalent of the Rust example e2e_app): the stock command plus one
# app that defines the query report "Sales by customer" (M8.4), which can only come from an app compiled into the
# binary. Everything else is the same as in desk/e2e/seed.sh: the DocTypes of examples/sales/doctypes (the
# metadata files of the example app) with the same awk and role edits, the migration, the users and the customers,
# all made with the stock commands of that binary. The restricted users (e2e_retail, e2e_retail_manager) are
# created but have no user permission yet, so restrict.spec.ts does not run completely (see
# .github/workflows/ci.yml for the tests that do).
#
# Run it from the repository root with an EMPTY (scratch) database in FORGEBASE_DATABASE_URL; the URL is
# read by the forgebase tool from the environment and is never printed here.
#
#   go build -o bin/e2e_app ./examples/e2e_app
#   FORGEBASE_DATABASE_URL=postgres://... .github/ci/e2e-seed.sh
#
# Start the server with:  bin/e2e_app serve --doctypes "$E2E_DIR/doctypes"
# Passwords come from E2E_PASSWORD (default below): a throw-away password of a throw-away database.
set -euo pipefail

: "${FORGEBASE_DATABASE_URL:?set FORGEBASE_DATABASE_URL to an empty scratch database}"
BIN="${FORGEBASE_BIN:-bin/e2e_app}"
E2E_DIR="${E2E_DIR:-bin/e2e}"
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
# limit (level 1) and the users of the restrict tests.
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
# P7: `user permission add` (e2e_retail and e2e_retail_manager are restricted to Retail) and `customize field
# add` (the site custom field custom_region) come with desk/e2e/seed.sh.
echo "seeded (P6 subset): users e2e_manager, e2e_clerk, e2e_admin, e2e_auditor, e2e_sales, e2e_retail and e2e_retail_manager, customers ada@example.com, rita@example.com and walt@example.com, DocTypes in $DOCTYPES"
