# 0006 Extension model: compile-time apps, no runtime plugins

- Status: Accepted
- Date: 2026-10-28

## Context

Frappe apps are installed into a bench (per-site installation is Unverified) at runtime and can override controller classes or add Server Scripts (see [feature matrix](../blueprint/feature-matrix.md)). The owner's instructions say not to build dynamic libraries, WASM plugin runtimes or complex extension systems unless an approved requirement needs them.

## Decision

A Forgebase "app" is a Rust crate compiled into the user's binary. The user's `main` depends on `forgebase-*` and registers app DocTypes and hooks at startup through an explicit registration list (no global magic or inventory macros). Hooks are a Rust trait (`DocTypeHooks`) with default no-op methods, run in registration order; no class override mechanism. Server Scripts and other runtime-authored code are out of scope.

## Alternatives considered

- Dynamic libraries or WASM plugins: ABI/sandbox complexity and a large maintenance surface for no approved need.
- Runtime scripting (Server Scripts): security and versioning burden.
- Automatic registration via inventory/link-time magic: implicit, harder to debug.

## Consequences

- Type safety, no runtime plugin machinery, one build artifact.
- Changing hooks requires rebuild and redeploy; there is no `bench get-app` equivalent. This is an intentional difference from Frappe.
- Revisit only if an approved requirement cannot be met this way.
