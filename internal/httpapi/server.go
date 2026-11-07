package httpapi

import (
	"net/http"
	"time"
)

// NewServer is the http.Server that serves h (the Sites of the process). The caller listens, serves and
// shuts it down (internal/app does, for the serve command).
//
// Timeouts: only the header read is bounded (30 s: a client that never finishes its header cannot hold a
// connection for ever; the Rust server has no such limit). A body and a response have no limit (a login
// or a long list must not be cut), and an idle keep-alive connection is kept practically for ever, as
// hyper does (without IdleTimeout, net/http would use ReadHeaderTimeout, 30 s).
//
// DisableGeneralOptionsHandler: net/http would answer "OPTIONS *" itself with a bare 200; here it goes
// through the handler like every request (the Host check, then the 404 of an unknown route).
func NewServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:                      h,
		ReadHeaderTimeout:            30 * time.Second,
		IdleTimeout:                  24 * time.Hour,
		DisableGeneralOptionsHandler: true,
	}
}
