#!/usr/bin/env bash
# Two sites on one server, and a backup restored into a new database (M7.3, ADR 0014).
#
# Called by the `sites` job of .github/workflows/ci.yml; run it locally the same way (see
# examples/sites/README.md). It proves, with the composed binary of examples/sales_extension:
#
#   1. two sites with different apps, reached by their Host, keep their users and DocTypes apart;
#   2. an unknown host is 404;
#   3. `site backup shop`, restored into the new empty database of `restore_check`, gives the same
#      documents (and the same login).
#
#   4. (P6, M8.5) the approval workflow of `sales_order` and a shared saved view over HTTP: a Sales User
#      sends an order for approval, an Administrator saves the view "Pending approval" for everyone, the
#      Sales User sees it, filters with it, and cannot delete it.
#
# The Rust script (examples/sites/ci.sh of the reference implementation) has four more parts. They
# arrive in the Go port with the phase that ports what they use, and are added here then:
#
#   - (P7) the fixture of the sales app: the first `migrate` of a site kept the two customer groups of
#     the SQL migration and inserted the third, and a second `migrate` finds all three unchanged;
#   - (P7) a webhook on `sales_order` for the event `submit`, delivered by the worker to the receiver
#     of examples/sites/receiver.py (Python standard library, started and stopped by the script): the
#     delivery is signed and verified, exactly one `submit` delivery arrives, a forged signature is
#     refused, and `webhook test` succeeds. receiver.py and this part come with the webhooks (P7);
#   - (P7) a site custom field, added by `customize field add` with the server running: it appears in
#     that site's metadata within 15 s without a restart, not in `shop`'s, takes a value over the API,
#     disappears with `customize field disable` (the data is kept) and returns with `enable`;
#   - (P7) user permissions: `user permission add` restricts the Sales User bob on `shop` to the
#     customer group Retail, `user permission list` shows the row and `remove` lifts it.
#
# Needs: the binary built (cd examples/sales_extension && go build -o ../../bin/sales_extension
# ./cmd/sales_extension), PostgreSQL 16 with a role that has CREATEDB, the PostgreSQL client tools
# (pg_dump, pg_restore) of the server's major version (FORGEBASE_PG_BIN names their directory when they
# are not first on PATH), curl and jq. The three database URLs are read from the environment and never
# printed; the three databases must not exist yet (they are created, and left in place when the script
# ends: a local run drops them with DROP DATABASE).
#
#   SHOP_DATABASE_URL=postgres://user:password@localhost:5432/fb_sites_shop
#   FIELD_DATABASE_URL=postgres://user:password@localhost:5432/fb_sites_field
#   RESTORE_DATABASE_URL=postgres://user:password@localhost:5432/fb_sites_restore
#
# Optional: FORGEBASE_BIN (default bin/sales_extension), SITES_PORT (default 8000).
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
base="http://127.0.0.1:$port"
work="$(mktemp -d)"
server=""
cleanup() {
  # The background process is stopped by its PID.
  if [ -n "$server" ]; then kill "$server" 2>/dev/null || true; wait "$server" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT
fail() {
  echo "FAIL: $*" >&2
  if [ -f "$work/server.log" ]; then echo "--- server.log" >&2; cat "$work/server.log" >&2; fi
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
# `site create` migrates.
"$bin" site create shop > /dev/null
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

echo "== stop the server"
kill "$server" 2>/dev/null || true
wait "$server" || true
server=""
