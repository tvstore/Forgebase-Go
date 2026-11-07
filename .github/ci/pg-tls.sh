#!/usr/bin/env bash
# A throwaway PostgreSQL 16 with TLS for the M9.0 tests (ADR 0018), the same locally and in CI.
#
#   .github/ci/pg-tls.sh up DIR    generate the certificates into DIR, start the server, wait for it
#   .github/ci/pg-tls.sh down      remove the server (the certificates are left for the caller)
#
# `up` creates a CA, a server certificate for it (SAN DNS:localhost and IP:127.0.0.1) and a second,
# unrelated CA for the wrong-CA test. It starts the Docker container `forgebase-pg-tls`
# (image $PG_TLS_IMAGE, default postgres:16-bookworm) with ssl=on on 127.0.0.1:5433. The image's
# default pg_hba.conf accepts password authentication (scram-sha-256) over TCP with and without
# TLS, so one server serves `sslmode=require` and `sslmode=disable`.
# The environment variables the tests read are printed, and appended to $GITHUB_ENV when it is set:
#   FORGEBASE_TEST_TLS_URL       postgres://forgebase:forgebase@localhost:5433/postgres
#   FORGEBASE_TEST_TLS_CA        DIR/ca.pem        (signs the server certificate)
#   FORGEBASE_TEST_TLS_WRONG_CA  DIR/wrong-ca.pem  (signs nothing)
# Besides the superuser `forgebase`, the server has two roles that pg_hba.conf lets in over TLS
# only (a connection without TLS is rejected for them), so a test that uses one proves TLS:
#   tls_only (password tls-only, CREATEDB)   the backup test: pg_dump and pg_restore must use TLS
#   qmark    (password p?w, a `?`)            the test that the raw `?` is refused before the first `@` of the URL
#                                              (like a raw `/` or `#`), and that the encoded `p%3Fw` connects over TLS
# Needs openssl and Docker. The credentials are throwaway; the keys never leave DIR.
set -euo pipefail

name=forgebase-pg-tls
image=${PG_TLS_IMAGE:-postgres:16-bookworm}
port=5433

case "${1:-}" in
  down)
    docker rm -f "$name" > /dev/null 2>&1 || true
    exit 0
    ;;
  up) ;;
  *)
    echo "usage: $0 up DIR | down" >&2
    exit 2
    ;;
esac
dir=${2:?usage: $0 up DIR}
mkdir -p "$dir"
dir=$(cd "$dir" && pwd)

make_ca() { # make_ca NAME
  openssl ecparam -name prime256v1 -genkey -noout -out "$dir/$1.key"
  openssl req -new -x509 -key "$dir/$1.key" -out "$dir/$1.pem" -days 3 -subj "/CN=Forgebase test $1" \
    -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign"
}
make_ca ca
make_ca wrong-ca

openssl ecparam -name prime256v1 -genkey -noout -out "$dir/server.key"
openssl req -new -key "$dir/server.key" -out "$dir/server.csr" -subj "/CN=localhost"
printf '%s\n' \
  "basicConstraints=CA:FALSE" \
  "keyUsage=digitalSignature" \
  "extendedKeyUsage=serverAuth" \
  "subjectAltName=DNS:localhost,IP:127.0.0.1" > "$dir/server.ext"
openssl x509 -req -in "$dir/server.csr" -CA "$dir/ca.pem" -CAkey "$dir/ca.key" -CAcreateserial \
  -out "$dir/server.pem" -days 3 -extfile "$dir/server.ext" 2> /dev/null
chmod 0644 "$dir"/*.pem
chmod 0600 "$dir"/*.key

cat > "$dir/roles.sh" <<'ROLES'
# Sourced by the image's entrypoint while it initialises the database (as the postgres user).
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres <<'SQL'
CREATE ROLE tls_only LOGIN CREATEDB PASSWORD 'tls-only';
CREATE ROLE qmark LOGIN CREATEDB PASSWORD 'p?w';
SQL
{ echo "hostnossl all tls_only,qmark all reject"; cat "$PGDATA/pg_hba.conf"; } > /tmp/pg_hba.new
cat /tmp/pg_hba.new > "$PGDATA/pg_hba.conf"
ROLES
chmod 0644 "$dir/roles.sh"

docker rm -f "$name" > /dev/null 2>&1 || true
# The server key must belong to the postgres user and have mode 0600, so the container copies it
# (as root) before it starts the normal entrypoint. The mount is read-only.
docker run -d --name "$name" \
  -e POSTGRES_USER=forgebase -e POSTGRES_PASSWORD=forgebase \
  -p "127.0.0.1:$port:5432" \
  -v "$dir:/tls-src:ro" \
  -v "$dir/roles.sh:/docker-entrypoint-initdb.d/10-roles.sh:ro" \
  --entrypoint bash "$image" -c '
    set -e
    install -o postgres -g postgres -m 0600 /tls-src/server.key /var/lib/postgresql/server.key
    install -o postgres -g postgres -m 0644 /tls-src/server.pem /var/lib/postgresql/server.pem
    exec docker-entrypoint.sh postgres -c ssl=on \
      -c ssl_cert_file=/var/lib/postgresql/server.pem -c ssl_key_file=/var/lib/postgresql/server.key
  ' > /dev/null

# The image starts a temporary server for its initialisation first, so wait for TCP with the
# real credentials over TLS, twice in a row.
ok=0
for _ in $(seq 1 60); do
  if docker exec "$name" pg_isready -h 127.0.0.1 -U forgebase > /dev/null 2>&1 \
    && docker exec -e PGPASSWORD=forgebase "$name" psql -h 127.0.0.1 -U forgebase -tAc 'SHOW ssl' 2> /dev/null | grep -qx on; then
    ok=$((ok + 1))
    [ "$ok" -ge 2 ] && break
  else
    ok=0
  fi
  sleep 1
done
if [ "$ok" -lt 2 ]; then
  docker logs "$name" >&2 || true
  echo "the TLS PostgreSQL did not start" >&2
  exit 1
fi

vars=$(printf '%s\n' \
  "FORGEBASE_TEST_TLS_URL=postgres://forgebase:forgebase@localhost:$port/postgres" \
  "FORGEBASE_TEST_TLS_CA=$dir/ca.pem" \
  "FORGEBASE_TEST_TLS_WRONG_CA=$dir/wrong-ca.pem")
echo "$vars"
if [ -n "${GITHUB_ENV:-}" ]; then
  echo "$vars" >> "$GITHUB_ENV"
fi
