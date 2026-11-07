#!/usr/bin/env bash
# Two sites on one server, and a backup restored into a new database (M7.3, ADR 0014).
#
# Called by the `sites` job of .github/workflows/ci.yml; run it locally the same way (see
# examples/sites/README.md). It proves, with the composed binary of examples/sales_extension:
#
#   1. two sites with different apps, reached by their Host, keep their users and DocTypes apart;
#   2. an unknown host is 404;
#   3. `site backup shop`, restored into the new empty database of `restore_check`, gives the same
#      documents (and the same login);
#   4. (P6, M8.5) the approval workflow of `sales_order` and a shared saved view over HTTP: a Sales User
#      sends an order for approval, an Administrator saves the view "Pending approval" for everyone, the
#      Sales User sees it, filters with it, and cannot delete it;
#   5. (P7.1, M9.5) a webhook on `sales_order` for the event `submit`, delivered by the worker to the
#      receiver of examples/sites/receiver.py (Python standard library, started and stopped by this
#      script): the delivery is signed, the receiver verifies the signature with the recipe of
#      docs/guide/webhooks.md and records it, exactly one `submit` delivery arrives for the order that
#      was approved, a forged signature is refused, and `webhook test` succeeds;
#   6. (P7.2, M9.4) the fixture of the sales app: the first `migrate` of a site kept the two customer
#      groups of the SQL migration and inserted the third, and a second `migrate` finds all three
#      unchanged;
#   7. (P7.3, M10.1) a site custom field, added to `customer` of the site `field` by `customize field
#      add` with the server running: it appears in that site's metadata within 15 s without a restart,
#      not in `shop`'s, takes a value over the API, disappears with `customize field disable` (the data
#      is kept) and returns with it after `enable`;
#   8. (P7.3, M10.3) user permissions: `user permission add` restricts the Sales User bob on `shop` to
#      the customer group Retail, so that the list shows the Retail and the no-group customers and not
#      the Wholesale one (404 by name), only one group is listed and `customer_group` offers no create
#      (also to a manager); `user permission list` shows the row and `remove` makes the Wholesale
#      customer visible again.
#
# Needs: the binary built (cd examples/sales_extension && go build -o ../../bin/sales_extension
# ./cmd/sales_extension), PostgreSQL 16 with a role that has CREATEDB, the PostgreSQL client tools
# (pg_dump, pg_restore) of the server's major version (FORGEBASE_PG_BIN names their directory when they
# are not first on PATH), curl, jq and python3 (the receiver). The three database URLs are read from the
# environment and never printed; the three databases must not exist yet (they are created, and left in place when the script
# ends: a local run drops them with DROP DATABASE).
#
#   SHOP_DATABASE_URL=postgres://user:password@localhost:5432/fb_sites_shop
#   FIELD_DATABASE_URL=postgres://user:password@localhost:5432/fb_sites_field
#   RESTORE_DATABASE_URL=postgres://user:password@localhost:5432/fb_sites_restore
#
# Optional: FORGEBASE_BIN (default bin/sales_extension), SITES_PORT (default 8000), RECEIVER_PORT (the
# webhook receiver, default 8001).
set -euo pipefail
cd "$(dirname "$0")/../.."

for variable in SHOP_DATABASE_URL FIELD_DATABASE_URL RESTORE_DATABASE_URL; do
  if [ -z "${!variable:-}" ]; then
    echo "error: $variable is not set (see the header of examples/sites/ci.sh)" >&2
    exit 1
  fi
done
bin="${FORGEBASE_BIN:-bin/sales_extension}"
if [ ! -x "$bin" ]; then
  echo "error: $bin not found; run: cd examples/sales_extension && go build -o ../../bin/sales_extension ./cmd/sales_extension" >&2
  exit 1
fi
port="${SITES_PORT:-8000}"
rport="${RECEIVER_PORT:-8001}"
base="http://127.0.0.1:$port"
work="$(mktemp -d)"
server=""
receiver=""
worker=""
cleanup() {
  # Each background process is stopped by its PID.
  for pid in "$server" "$receiver" "$worker"; do
    if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  done
  rm -rf "$work"
}
trap cleanup EXIT
fail() {
  echo "FAIL: $*" >&2
  if [ -f "$work/server.log" ]; then echo "--- server.log" >&2; cat "$work/server.log" >&2; fi
  # The worker's log is the first thing to read when a delivery step fails.
  if [ -f "$work/worker.log" ]; then echo "--- worker.log" >&2; cat "$work/worker.log" >&2; fi
  exit 1
}

export FORGEBASE_CONFIG="examples/sites/forgebase.toml"
export FORGEBASE_LISTEN="127.0.0.1:$port"
# The test talks plain HTTP: no Secure attribute on the session cookie.
export FORGEBASE_DEV_INSECURE_COOKIES=1
shop_password="the password of the shop"
field_password="the password of the field"

echo "== create the sites"
"$bin" site list
# `site create` migrates: the first migrate of `shop` applies the fixture of the sales app.
"$bin" site create shop | tee "$work/shop-create.log" > /dev/null
"$bin" site create field > /dev/null
"$bin" site create restore_check --empty > /dev/null

echo "== one user per site"
printf '%s\n' "$shop_password" | "$bin" user create ann --site shop --role Administrator
printf '%s\n' "$field_password" | "$bin" user create ann --site field --role Administrator

echo "== serve both sites"
"$bin" serve > "$work/server.log" 2>&1 &
server=$!
for _ in $(seq 1 30); do
  if curl -s -o /dev/null -H "Host: shop.localhost" "$base/api/v1/meta"; then break; fi
  sleep 1
done
curl -s -o /dev/null -H "Host: shop.localhost" "$base/api/v1/meta" || fail "the server did not answer"

# login HOST PASSWORD [JAR]: writes the cookie jar $work/JAR.jar (default HOST), prints the HTTP
# status.
login() {
  curl -s -o /dev/null -w '%{http_code}' -c "$work/${3:-$1}.jar" -H "Host: $1" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"ann\",\"password\":\"$2\"}" "$base/api/v1/login"
}
[ "$(login shop.localhost "$shop_password")" = 200 ] || fail "login on shop"
[ "$(login field.localhost "$field_password")" = 200 ] || fail "login on field"
# The password of one site is no password of the other.
[ "$(login shop.localhost "$field_password" refused)" = 401 ] || fail "the field password worked on shop"
[ "$(login field.localhost "$shop_password" refused)" = 401 ] || fail "the shop password worked on field"

# doctypes HOST: the DocType names of the metadata API, one per line.
doctypes() {
  curl -fsS -b "$work/$1.jar" -H "Host: $1" "$base/api/v1/meta" | jq -r '.data[].name'
}
# Capture first, so that a failing request is an error and not "no such DocType" (under pipefail a
# negative grep would otherwise pass on a failed command).
shop_types="$(doctypes shop.localhost)" || fail "reading the metadata of shop"
field_types="$(doctypes field.localhost)" || fail "reading the metadata of field"
printf '%s\n' "$shop_types" | grep -qx customer || fail "shop has no customer"
if printf '%s\n' "$shop_types" | grep -qx customer_visit; then fail "shop has customer_visit"; fi
printf '%s\n' "$field_types" | grep -qx customer_visit || fail "field has no customer_visit"
code="$(curl -s -o /dev/null -w '%{http_code}' -H "Host: nope.localhost" "$base/api/v1/meta")"
[ "$code" = 404 ] || fail "an unknown host gave $code, not 404"
echo "ok: two sites, own users, own apps, unknown host is 404"

echo "== documents on shop"
for n in 1 2 3; do
  "$bin" doc insert customer --site shop \
    --data "{\"email\":\"c$n@example.com\",\"full_name\":\"Customer $n\",\"loyalty_points\":$n}" > /dev/null
done
for n in 1 2; do
  "$bin" doc insert sales_order --site shop --data \
    "{\"customer\":\"c$n@example.com\",\"order_date\":\"2026-10-0$n\",\"items\":[{\"description\":\"Pen\",\"qty\":$n,\"rate\":\"1.25\"},{\"description\":\"Ink\",\"qty\":1,\"rate\":\"9.00\"}]}" > /dev/null
done

echo "== the fixture of the sales app (M9.4)"
# The first migrate (of `site create shop`) kept Retail and Wholesale, which the SQL migration of
# the sales app made, and inserted Online; a second migrate finds the three unchanged.
grep -qx 'fixtures: sales: 1 inserted, 0 updated, 2 kept, 0 unchanged' "$work/shop-create.log" \
  || { cat "$work/shop-create.log" >&2; fail "the first migrate did not insert 1 and keep 2 groups"; }
"$bin" migrate --site shop > "$work/shop-migrate.log"
grep -qx 'fixtures: sales: 0 inserted, 0 updated, 0 kept, 3 unchanged' "$work/shop-migrate.log" \
  || { cat "$work/shop-migrate.log" >&2; fail "the second migrate did not find the groups unchanged"; }
[ "$("$bin" doc list customer_group --site shop --limit 500 | jq -r '[.[].name] | sort | join(",")')" = "Online,Retail,Wholesale" ] \
  || fail "shop does not have the three customer groups"
echo "ok: the first migrate kept 2 customer groups and inserted 1, the second found them unchanged"

echo "== backup shop, restore into restore_check, migrate"
"$bin" site backup shop --out "$work/shop.dump"
"$bin" site restore restore_check --from "$work/shop.dump"
"$bin" migrate --site restore_check > /dev/null

echo "== compare the documents"
# dump SITE: every customer and sales order of the site as `doc list` and `doc get` print them
# (child rows included), in name order.
dump() {
  for doctype in customer sales_order; do
    "$bin" doc list "$doctype" --site "$1" --limit 500 > "$work/$1.$doctype.list"
    cat "$work/$1.$doctype.list"
    jq -r '.[].name' "$work/$1.$doctype.list" | while IFS= read -r name; do
      "$bin" doc get "$doctype" "$name" --site "$1"
    done
  done
}
dump shop > "$work/shop.docs"
dump restore_check > "$work/restore_check.docs"
[ "$(jq length "$work/shop.customer.list")" = 3 ] || fail "shop does not have 3 customers"
[ "$(jq length "$work/shop.sales_order.list")" = 2 ] || fail "shop does not have 2 sales orders"
diff "$work/shop.docs" "$work/restore_check.docs" || fail "the restored documents differ"
# The restored site has the users of the backup: shop's password logs in on restore_check.
[ "$(login restore.localhost "$shop_password")" = 200 ] || fail "login on restore_check"
echo "ok: the restored database has the same documents and users"

echo "== a workflow transition and a shared saved view on shop (M8.5)"
# The Sales User bob works with an API token (no cookie and no CSRF token to carry around); the
# Administrator ann does too. The tokens are kept in variables and never printed.
printf '%s\n' "the password of bob" | "$bin" user create bob --site shop --role "Sales User"
admin_token="$("$bin" token create ann --site shop)"
bob_token="$("$bin" token create bob --site shop)"
# call TOKEN METHOD PATH [JSON]: one request to shop. The body of the reply goes to
# $work/reply.json and the HTTP status is printed.
call() {
  local args=(-s --max-time 10 -o "$work/reply.json" -w '%{http_code}' -X "$2" -H "Host: shop.localhost" \
    -H "Authorization: token $1")
  if [ -n "${4:-}" ]; then args+=(-H 'Content-Type: application/json' -d "$4"); fi
  curl "${args[@]}" "$base$3"
}
order="$(jq -r '.[0].name' "$work/shop.sales_order.list")"
# `sales_order` has a workflow: plain submit is refused, the order moves by transitions. A Sales
# User may send an order for approval ("Draft" -> "Pending Approval") ...
[ "$(call "$bob_token" POST "/api/v1/resource/sales_order/$order/submit" '{"version":1}')" = 409 ] \
  || fail "a plain submit was not refused"
[ "$(jq -r .error.code "$work/reply.json")" = workflow_required ] || fail "the refusal is not workflow_required"
[ "$(call "$bob_token" POST "/api/v1/resource/sales_order/$order/transition" \
  '{"action":"Submit for Approval","version":1}')" = 200 ] || fail "bob could not send the order for approval"
[ "$(jq -r .data.workflow_state "$work/reply.json")" = "Pending Approval" ] || fail "the order is not pending"
# ... but not reject it: that is for a Sales Manager.
[ "$(call "$bob_token" POST "/api/v1/resource/sales_order/$order/transition" \
  '{"action":"Reject","version":2}')" = 403 ] || fail "bob could reject an order"

# The Administrator saves a view for everyone: a shared view.
view='{"label":"Pending approval","shared":true,"definition":{"filters":[{"field":"workflow_state","op":"=","value":"Pending Approval"}]}}'
[ "$(call "$admin_token" POST /api/v1/views/sales_order "$view")" = 201 ] || fail "ann could not create the shared view"
view_id="$(jq -r .data.id "$work/reply.json")"
# A Sales User cannot create a shared view ...
[ "$(call "$bob_token" POST /api/v1/views/sales_order "$view")" = 403 ] || fail "bob created a shared view"
# ... sees the Administrator's view ...
[ "$(call "$bob_token" GET /api/v1/views/sales_order)" = 200 ] || fail "bob could not list the views"
[ "$(jq -r '[.data[] | select(.label == "Pending approval" and .shared)] | length' "$work/reply.json")" = 1 ] \
  || fail "bob does not see the shared view"
# ... can run its filter ...
filters="$(jq -c '.data[] | select(.label == "Pending approval") | .definition.filters' "$work/reply.json")"
code="$(curl -s -o "$work/reply.json" -w '%{http_code}' -G -H "Host: shop.localhost" \
  -H "Authorization: token $bob_token" --data-urlencode "filters=$filters" "$base/api/v1/resource/sales_order")"
[ "$code" = 200 ] || fail "the view's filter was refused ($code)"
[ "$(jq -r '.data | length' "$work/reply.json")" = 1 ] || fail "the view does not show exactly the pending order"
# ... and cannot delete it. Only the Administrator can.
[ "$(call "$bob_token" DELETE "/api/v1/views/sales_order/$view_id?version=1")" = 403 ] || fail "bob deleted a shared view"
[ "$(call "$admin_token" DELETE "/api/v1/views/sales_order/$view_id?version=1")" = 204 ] || fail "ann could not delete the shared view"
echo "ok: a Sales User sent an order for approval, the Administrator's shared view is visible to bob and not his to delete"

echo "== a webhook on the submit of a sales order, delivered to a test receiver (M9.5)"
# The receiver (examples/sites/receiver.py) verifies the signature of every delivery with the
# recipe of docs/guide/webhooks.md and records the verified ones. The secret is generated here, handed
# to the receiver by its environment and to `webhook add` by standard input; it is never printed.
secret="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
records="$work/deliveries.jsonl"
: > "$records"
WEBHOOK_SECRET="$secret" python3 -I examples/sites/receiver.py "$rport" "$records" &
receiver=$!
for _ in $(seq 1 30); do
  if curl -s -o /dev/null "http://127.0.0.1:$rport/"; then break; fi
  sleep 1
done
curl -s -o /dev/null "http://127.0.0.1:$rport/" || fail "the receiver did not answer"
# A request with a bad signature is refused and not recorded (the receiver really verifies).
code="$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  -H 'X-Forgebase-Timestamp: 1' -H 'X-Forgebase-Signature: sha256=00' -d '{}' "http://127.0.0.1:$rport/hook")"
[ "$code" = 400 ] || fail "the receiver accepted a forged delivery ($code)"
# `--allow-private` and `--insecure-http` are development flags: the receiver is on 127.0.0.1 and
# speaks plain HTTP, which the SSRF guard and the https-only rule refuse by default. Do not use
# them in production.
# The secret is matched from a file descriptor, never as an argument (an argument shows in `ps`).
printf '%s\n' "$secret" | "$bin" webhook add sales_order_submit --site shop --doctype sales_order \
  --events submit --url "http://127.0.0.1:$rport/hook" --allow-private --insecure-http --secret-stdin \
  > "$work/webhook-add.out"
if grep -qF -f <(printf '%s\n' "$secret") "$work/webhook-add.out"; then fail "webhook add printed the secret"; fi
if "$bin" webhook list --site shop | grep -qF -f <(printf '%s\n' "$secret"); then fail "webhook list printed the secret"; fi
# One signed test delivery, now, without the queue: exit 0 on a 2xx answer.
"$bin" webhook test sales_order_submit --site shop || fail "webhook test failed"
# Approve the order that is pending (see above): the transition submits it, which fires
# `transition` and `submit`; the webhook listens to `submit` only.
[ "$(call "$bob_token" POST "/api/v1/resource/sales_order/$order/transition" \
  '{"action":"Approve","version":2}')" = 200 ] || fail "bob could not approve the order"
[ "$(jq -r .data.workflow_state "$work/reply.json")" = Approved ] || fail "the order is not approved"
# The delivery is a job: the worker sends it. Wait at most 30 s for it.
"$bin" worker --site shop > "$work/worker.log" 2>&1 &
worker=$!
for _ in $(seq 1 30); do
  if [ "$(jq -s '[.[] | select(.event == "submit")] | length' "$records")" -ge 1 ]; then break; fi
  sleep 1
done
kill "$worker" 2>/dev/null || true
wait "$worker" || true
worker=""
if grep -qF -f <(printf '%s\n' "$secret") "$work/worker.log"; then fail "the worker printed the secret"; fi
[ "$(jq -s --arg order "$order" \
  '[.[] | select(.event == "submit" and .doctype == "sales_order" and .name == $order)] | length' "$records")" = 1 ] \
  || { cat "$records" "$work/worker.log" >&2; fail "the receiver did not record exactly one verified submit delivery of the order"; }
[ "$(jq -s '[.[] | select(.event == "test")] | length' "$records")" = 1 ] || fail "the test delivery was not recorded once"
[ "$(jq -s '[.[] | select(.event != "submit" and .event != "test")] | length' "$records")" = 0 ] \
  || fail "the receiver got an event the webhook does not listen to"
[ "$(jq -s '[.[].delivery] | unique | length' "$records")" = 2 ] || fail "the delivery ids are not distinct"
echo "ok: the worker delivered exactly one verified submit of $order, signed with the secret, and webhook test answered 2xx"
kill "$receiver" 2>/dev/null || true
wait "$receiver" || true
receiver=""

echo "== a site custom field on field, live and per site (M10.1, ADR 0023)"
# The server is running: the field is added by the command line, stored in the database of the
# site `field` only, and the server picks it up by itself (a live reload, at most every 10 s).
# `field` installs sales_extension, `shop` does not; both have `customer` of the sales app.
field_token="$("$bin" token create ann --site field)"
# meta_has HOST TOKEN DOCTYPE FIELD: succeeds when the metadata of DOCTYPE lists FIELD. The reply
# is captured first so that a failing request is an error and not "no such field".
meta_has() {
  local names
  names="$(curl -fsS --max-time 5 -H "Host: $1" -H "Authorization: token $2" "$base/api/v1/meta/$3" | jq -r '.data.fields[].name')" \
    || fail "reading the metadata of $3 on $1"
  printf '%s\n' "$names" | grep -qx "$4"
}
# wait_meta HOST TOKEN DOCTYPE FIELD present|absent: waits at most 15 s (the reload interval is
# 10 s) for the metadata to list, or not list, the field.
wait_meta() {
  for _ in $(seq 1 15); do
    if meta_has "$1" "$2" "$3" "$4"; then [ "$5" = present ] && return 0; else [ "$5" = absent ] && return 0; fi
    sleep 1
  done
  fail "the metadata of $3 on $1 does not have $4 $5 after 15 s"
}
# callf TOKEN METHOD PATH [JSON]: one request to field; the body of the reply goes to
# $work/reply.json and the HTTP status is printed.
callf() {
  local args=(-s --max-time 5 -o "$work/reply.json" -w '%{http_code}' -X "$2" -H "Host: field.localhost" \
    -H "Authorization: token $1")
  if [ -n "${4:-}" ]; then args+=(-H 'Content-Type: application/json' -d "$4"); fi
  curl "${args[@]}" "$base$3"
}
meta_has field.localhost "$field_token" customer custom_region && fail "field has custom_region before it was added"
"$bin" doc insert customer --site field \
  --data '{"email":"ada@field.example","full_name":"Ada Lovelace"}' > /dev/null
# The definition is one TOML table body, read from standard input.
printf 'name = "custom_region"\nlabel = "Region"\ntype = "data"\n' \
  | "$bin" customize field add --site field --doctype customer --field - > "$work/field-add.out"
grep -q 'custom_region' "$work/field-add.out" || fail "customize field add did not name the field"
"$bin" customize field list --site field --doctype customer | grep -q 'custom_region' \
  || fail "customize field list does not show the field"
wait_meta field.localhost "$field_token" customer custom_region present
# Isolation: the other site's metadata, checked now (well after the reload interval of the server
# for the field site) does not have it, and its list of custom fields is empty.
sleep 1
meta_has shop.localhost "$admin_token" customer custom_region && fail "shop has the custom field of field"
shop_fields="$("$bin" customize field list --site shop)" || fail "customize field list on shop"
if printf '%s\n' "$shop_fields" | grep -q custom_region; then fail "shop lists the custom field of field"; fi
# Write a value through the API and read it back.
[ "$(callf "$field_token" PATCH /api/v1/resource/customer/ada@field.example \
  '{"version":1,"data":{"custom_region":"North"}}')" = 200 ] || { cat "$work/reply.json" >&2; fail "writing the custom field"; }
[ "$(callf "$field_token" GET /api/v1/resource/customer/ada@field.example)" = 200 ] || fail "reading the customer on field"
[ "$(jq -r .data.custom_region "$work/reply.json")" = North ] || fail "the custom field did not read back"
# Disable: gone from the metadata and the document (and refused as input), but the column and its
# data are kept, so enabling brings the value back.
"$bin" customize field disable --site field --doctype customer custom_region > /dev/null
wait_meta field.localhost "$field_token" customer custom_region absent
[ "$(callf "$field_token" GET /api/v1/resource/customer/ada@field.example)" = 200 ] || fail "reading the customer after disable"
[ "$(jq -r '.data | has("custom_region")' "$work/reply.json")" = false ] || fail "a disabled custom field is still in the document"
[ "$(callf "$field_token" PATCH /api/v1/resource/customer/ada@field.example \
  '{"version":2,"data":{"custom_region":"South"}}')" = 422 ] || fail "input for a disabled custom field was not refused"
"$bin" customize field enable --site field --doctype customer custom_region > /dev/null
wait_meta field.localhost "$field_token" customer custom_region present
[ "$(callf "$field_token" GET /api/v1/resource/customer/ada@field.example)" = 200 ] || fail "reading the customer after enable"
[ "$(jq -r .data.custom_region "$work/reply.json")" = North ] || fail "the data of the custom field was not kept"
echo "ok: custom_region was added on field, live within 15 s, absent on shop, written and read back, hidden by disable and back with its data after enable"

echo "== a Sales User restricted to the customer group Retail (M10.3, ADR 0025)"
# Customers in Retail, in Wholesale and in no group, and an order for each. bob (a Sales User, made
# above) is restricted to the group Retail; mia (a Sales Manager, who may create groups) shows what
# the desk and the API offer a restricted user.
printf '%s\n' "the password of mia" | "$bin" user create mia --site shop --role "Sales Manager"
mia_token="$("$bin" token create mia --site shop)"
"$bin" doc insert customer --site shop \
  --data '{"email":"retail@example.com","full_name":"Retail Customer","customer_group":"Retail"}' > /dev/null
"$bin" doc insert customer --site shop \
  --data '{"email":"wholesale@example.com","full_name":"Wholesale Customer","customer_group":"Wholesale"}' > /dev/null
for customer in retail@example.com wholesale@example.com c3@example.com; do
  "$bin" doc insert sales_order --site shop --data \
    "{\"customer\":\"$customer\",\"order_date\":\"2026-10-05\",\"items\":[{\"description\":\"Pen\",\"qty\":1,\"rate\":\"1.25\"}]}" > /dev/null
done
# customers_of TOKEN: the customer names a user sees, sorted, one per line.
customers_of() {
  [ "$(call "$1" GET "/api/v1/resource/customer?limit=100")" = 200 ] || fail "listing customers"
  jq -r '.data[].name' "$work/reply.json" | sort
}
before="$(customers_of "$bob_token")"
printf '%s\n' "$before" | grep -qx wholesale@example.com || fail "bob does not see the Wholesale customer before the restriction"
[ "$(call "$mia_token" GET /api/v1/meta/customer_group)" = 200 ] || fail "mia could not read the customer group meta"
[ "$(jq -r .data.rights.create "$work/reply.json")" = all ] || fail "mia cannot create customer groups before the restriction"

"$bin" user permission add --site shop --user bob --doctype customer_group --value Retail
"$bin" user permission add --site shop --user mia --doctype customer_group --value Retail > /dev/null
"$bin" user permission list --site shop --user bob | grep -q 'customer_group.*Retail' || fail "user permission list does not show bob's row"
# It applies on the next request, no restart.
after="$(customers_of "$bob_token")"
printf '%s\n' "$after" | grep -qx retail@example.com || fail "bob does not see the Retail customer"
printf '%s\n' "$after" | grep -qx c3@example.com || fail "bob does not see a customer with no group"
if printf '%s\n' "$after" | grep -qx wholesale@example.com; then fail "bob sees the Wholesale customer"; fi
[ "$(call "$bob_token" GET /api/v1/resource/customer/wholesale@example.com)" = 404 ] || fail "bob got the Wholesale customer by name"
[ "$(call "$bob_token" GET /api/v1/resource/customer/retail@example.com)" = 200 ] || fail "bob cannot get the Retail customer"
# Groups: bob sees Retail only; the manager restricted to Retail may not create a group any more.
[ "$(call "$bob_token" GET "/api/v1/resource/customer_group?limit=100")" = 200 ] || fail "bob could not list the groups"
[ "$(jq -r '[.data[].name] | join(",")' "$work/reply.json")" = Retail ] || fail "bob sees more than the Retail group"
[ "$(call "$bob_token" GET /api/v1/meta/customer_group)" = 200 ] || fail "bob could not read the customer group meta"
[ "$(jq -r .data.rights.create "$work/reply.json")" = none ] || fail "the meta of customer_group offers bob create"
[ "$(call "$mia_token" GET /api/v1/meta/customer_group)" = 200 ] || fail "mia could not read the customer group meta"
[ "$(jq -r .data.rights.create "$work/reply.json")" = none ] || fail "the meta of customer_group offers mia create"
[ "$(call "$mia_token" POST /api/v1/resource/customer_group '{"title":"Partner"}')" = 403 ] \
  || fail "a manager restricted to Retail created a customer group"
# An order is not hidden because its customer is: only the customer's own group restricts (the
# orders have no Link to the group), but a new order cannot link to the hidden customer.
[ "$(call "$bob_token" GET "/api/v1/resource/sales_order?limit=100")" = 200 ] || fail "bob could not list orders"
[ "$(jq -r '.data | length' "$work/reply.json")" = "$("$bin" doc list sales_order --site shop --limit 500 | jq length)" ] \
  || fail "bob does not see every order"
[ "$(call "$bob_token" POST /api/v1/resource/sales_order \
  '{"customer":"wholesale@example.com","items":[{"description":"Pen","qty":1,"rate":"1.25"}]}')" = 422 ] \
  || fail "bob created an order for a customer he cannot see"
# The refusal is the one of a Link to a name that does not exist, not another validation error.
jq -e '.error.details | map(select(contains("customer") and contains("does not exist"))) | length > 0' "$work/reply.json" > /dev/null \
  || { cat "$work/reply.json" >&2; fail "the refusal does not say that the customer does not exist"; }
# Remove the restriction: the Wholesale customer is visible on the next request.
"$bin" user permission remove --site shop --user bob --doctype customer_group --value Retail
"$bin" user permission remove --site shop --user mia --doctype customer_group --value Retail > /dev/null
left="$("$bin" user permission list --site shop)" || fail "user permission list"
if printf '%s\n' "$left" | grep -q customer_group; then fail "a permission row is left"; fi
customers_of "$bob_token" | grep -qx wholesale@example.com || fail "bob does not see the Wholesale customer after the restriction was removed"
[ "$(call "$bob_token" GET /api/v1/resource/customer/wholesale@example.com)" = 200 ] || fail "bob cannot get the Wholesale customer after the removal"
echo "ok: bob, restricted to Retail, saw Retail and no-group customers, 404 for Wholesale, one group and no create; the removal showed Wholesale again"

echo "== stop the server"
kill "$server" 2>/dev/null || true
wait "$server" || true
server=""
