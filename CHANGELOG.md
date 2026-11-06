# Changelog

All notable changes to this project are documented in this file. The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The project follows [Semantic Versioning](https://semver.org/), with the 0.x caveat that the public API may change.

This repository is the Go implementation of Forgebase ([ADR 0026](docs/adr/0026-go-implementation.md)). The changelog of the Rust implementation, up to M10, is in the frozen repository <https://github.com/tvstore/Forgebase>.

## [Unreleased]

### Added

- **P0 Foundations of the Go port (ADR 0026).**
  - The Go module `github.com/tvstore/forgebase-go` (`go 1.26.0`, `toolchain go1.27.3`, no dependencies) with the root package `forgebase` (`Version = "0.0.0"`), the `internal/core`, `internal/db`, `internal/httpapi` and `internal/app` packages (documentation only; the import direction is stated in each), and the placeholder package `forgebase/testing`.
  - `forgebase --version` and `forgebase -V` print `forgebase 0.0.0`; any other command prints a usage to standard error and exits with status 2.
  - CI (`.github/workflows/ci.yml`): the jobs `changes` (path classifier with the key `go`, unit-tested), `docs` (offline link check), `check` (gofmt, `go vet`, staticcheck v0.8.2, govulncheck v1.9.0, `go test -race`, `go build`, `go mod verify`, `go mod tidy -diff`, with a PostgreSQL 16 service), `licenses` (a standard-library licence checker, `.github/ci/licensecheck`) and `ci-result`. Go is installed from the official tarball with a verified sha256 (`.github/ci/install-go.sh`); no setup action is used. Module, build and tool caches use `actions/cache`.
  - `ci-result` **fails on a draft pull request** with a clear message, so that a draft run can never satisfy the required check (G8). The workflow also runs on `converted_to_draft`.
  - Documentation carried over from the Rust repository: `AGENTS.md` (adapted to Go), `docs/owner-decisions.md` (with the Go port decisions G1 to G8), the blueprint, the guides (with a note that code examples still show the Rust API until their phase), ADRs 0001 to 0025 unchanged, the new [ADR 0026](docs/adr/0026-go-implementation.md), `README.md`, `CONTRIBUTING.md` and the assets (with a Go badge).
