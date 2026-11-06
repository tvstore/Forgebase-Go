// Package httpapi holds the HTTP layer: routes, middleware, the REST API and serving of the desk.
// It corresponds to the Rust crate forgebase-http.
//
// Import direction: httpapi may import internal/db and internal/core. It must not import
// internal/app or the root package forgebase.
package httpapi
