package httpapi

import (
	"net/http"
	"time"
)

// NewServer is the http.Server that serves h (the Sites of the process). The caller listens, serves and
// shuts it down (internal/app does, for the serve command).
//
// Timeouts: only the header read is bounded, by a deliberate deviation from Rust (30 s from the accept of
// a new connection, so a client that never sends a header cannot hold a connection for ever; axum::serve
// with hyper sets no timer, so Rust has no limit). A body and a response have no limit (a login or a long
// list must not be cut), and an idle keep-alive connection has none either: net/http's idle timeout falls
// back to ReadTimeout (0), not to ReadHeaderTimeout, and the deadline is cleared before the next request
// is awaited, as with hyper.
//
// DisableGeneralOptionsHandler: net/http would answer "OPTIONS *" itself with a bare 200; here it goes
// through the handler like every request (the Host check, then the 404 of an unknown route).
func NewServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:                      h,
		ReadHeaderTimeout:            30 * time.Second,
		DisableGeneralOptionsHandler: true,
	}
}
