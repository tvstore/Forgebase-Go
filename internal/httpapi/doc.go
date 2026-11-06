// Package httpapi holds the HTTP layer: the request pipeline (Host routing, authentication, CSRF, the
// error envelope), the REST routes, the metadata routes and the serving of the desk. It corresponds to the
// Rust crate forgebase-http; httpapi.go has the description of the pipeline and of the routes.
//
// The input half of json.rs (JSON to core values: ParseInput, ParseFilters, ParseSort) is shared with the
// command line; the output half is db.DocumentToJSON.
//
// Import direction: httpapi may import internal/db and internal/core. It must not import
// internal/app or the root package forgebase.
package httpapi
