package httpapi

import (
	"net/http"
	"time"
)

// NewServer is the http.Server that serves h (the Sites of the process). The caller listens, serves and
// shuts it down (internal/app does, for the serve command). Only the header read is bounded: a request
// body and a response have no timeout of their own, as with the Rust server (a login or a long list must
// not be cut), and a client that never finishes its header cannot hold a connection for ever.
func NewServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second}
}
