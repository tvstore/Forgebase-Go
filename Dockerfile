# Forgebase image: the built desk plus one application binary (default: the `sales` example),
# PostgreSQL 16 client tools for `site backup` / `site restore`, running as a non-root user.
#
#   docker build -t forgebase-sales .                                       # the `sales` example (demo)
#   docker build --build-arg APP_DIR=. --build-arg APP_PKG=./cmd/forgebase \
#       -t forgebase-stock .                                                # the stock binary (no apps)
#   docker build --build-arg APP_DIR=myapp --build-arg APP_PKG=./cmd/myapp -t myapp .
#
# APP_DIR is the directory of the Go module that holds the binary (relative to the build context) and
# APP_PKG the main package inside it. An app module's go.mod `replace github.com/tvstore/forgebase-go => ...`
# must be a relative path inside the build context (`=> ..` for an app at `<clone>/myapp`); `forgebase
# new-app` writes an absolute path, which does not exist in the build stage. Guide: docs/guide/deploy.md. Nothing in this file is specific to
# one CPU architecture: every base image below is multi-arch (amd64 and arm64), so build on the machine
# that will run it.
#
# Base images are pinned by digest (the multi-arch index digest). To update one:
#   docker pull <image>:<tag>
#   docker inspect --format '{{index .RepoDigests 0}}' <image>:<tag>
# then replace the `@sha256:...` below (keep the tag, it says what the digest is) and rebuild. The
# postgres digest also appears in deploy/compose.yaml and must stay equal to this one.
# See docs/blueprint/maintenance-policy.md ("Container images").

ARG APP_DIR=examples/sales
ARG APP_PKG=./cmd/sales

# ---- Stage 1: the desk (React app) --------------------------------------------------------------
FROM node:22-bookworm-slim@sha256:5ec23b7c622b00547753bc6ffd78085dd868a68aeb41c8e0a1418f033e13d96d AS desk
WORKDIR /desk
# desk/.npmrc has ignore-scripts=true: dependency install scripts do not run.
COPY desk/package.json desk/package-lock.json desk/.npmrc ./
RUN npm ci
COPY desk/ ./
# Writes dist/, including THIRD-PARTY-LICENSES.txt (it is in desk/public).
RUN npm run build && test -f dist/index.html && test -f dist/THIRD-PARTY-LICENSES.txt

# ---- Stage 2: the application binary ------------------------------------------------------------
# The toolchain of the `toolchain` line of go.mod. GOTOOLCHAIN=local keeps `go` from downloading
# another one; the check below fails the build if this image's Go and that line ever differ. The
# binary is static (CGO_ENABLED=0): the Go dependencies are pure Go, no C library is linked.
FROM golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0 AS build
ARG APP_DIR
ARG APP_PKG
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOFLAGS=-mod=readonly
WORKDIR /src
COPY . .
RUN test "$(go env GOVERSION)" = "$(sed -n 's/^toolchain[[:space:]]\{1,\}\(go[0-9][0-9.]*\)[[:space:]]*$/\1/p' go.mod)"
RUN cd "${APP_DIR}" && go build -trimpath -ldflags='-s -w' -o /forgebase-app "${APP_PKG}"

# ---- Stage 3: runtime ---------------------------------------------------------------------------
# postgres:16-bookworm is the base only for pg_dump and pg_restore 16, which must match the server's
# major version (`site backup` runs them). It needs no apt repository. The cost: the image is about
# 650 MB (a Debian slim base plus the PGDG client package would be about 200 MB), and the base
# declares `VOLUME /var/lib/postgresql/data`, so Docker creates an empty anonymous volume for each
# container of this image (`docker run --rm` removes it, and it is never used).
FROM postgres:16-bookworm@sha256:5264710be20f409750ac6dc4e5706dcef929c87e241605463f0f9aefa932a8d9
# The base image has no CA store, and TLS to PostgreSQL (`sslmode=require`, ADR 0018) checks the
# server's certificate against the operating system's roots (a Neon database needs this). The build
# stage has the Debian bundle (the `ca-certificates` package, as of its pinned digest), so it is
# copied: the final stage then needs no network at build time.
# The base also ships Debian's "snakeoil" test certificate in /etc/ssl/certs. The `ssl-cert` package
# generates it, and its private key, when the base image is built, so the key is in every copy of the
# public image. It must never be a trusted root, so both files and the dangling hash link are
# removed. SSL_CERT_FILE makes Go's crypto/x509 read the bundle only. libpq (pg_dump, pg_restore)
# reads SSL_CERT_FILE and also the hashed files of /etc/ssl/certs, so that directory must hold
# nothing but the bundle: CI checks it.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN rm -f /etc/ssl/certs/ssl-cert-snakeoil.pem /etc/ssl/private/ssl-cert-snakeoil.key \
    && find /etc/ssl/certs -xtype l -delete \
    && useradd --uid 10001 --user-group --no-create-home --shell /usr/sbin/nologin forgebase \
    && install -d -o forgebase -g forgebase /app /backups
COPY --from=build /forgebase-app /usr/local/bin/forgebase-app
COPY --from=desk /desk/dist /app/desk
COPY deploy/entrypoint.sh /usr/local/bin/entrypoint.sh
ENV SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt \
    FORGEBASE_DESK_DIR=/app/desk \
    FORGEBASE_PG_BIN=/usr/lib/postgresql/16/bin
USER 10001:10001
EXPOSE 8000
# The postgres base image sets SIGINT; both processes of `all` mode also stop on SIGTERM.
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["serve"]
