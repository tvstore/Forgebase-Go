package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The request pipeline of the HTTP API (the Rust module lib.rs; standard library net/http only, no
// framework).
//
//  1. Host routing (Sites) runs first, on every route and every error, including login, unknown paths
//     and wrong methods. A missing, repeated or malformed Host is 400 invalid_host. The Host then
//     selects the site, and the request is handled by that site's own routes, state and database: with
//     a config file a well-formed host that no site owns is 404 not_found ("no such site"); in the
//     implicit single-site mode (ImplicitSites, no config file) a host outside Config.AllowedHosts
//     stays 400 invalid_host, the protection against DNS rebinding. X-Forwarded-Host and Forwarded are
//     never read: a reverse proxy must pass the original Host.
//  2. Authentication (resource routes, meta routes and logout): "Authorization: token <key>:<secret>" is
//     checked with db.ValidateAPIToken; if the header is present the cookie is ignored. Without it the
//     fb_session cookie is checked with db.ValidateSession. Anything else is the same 401
//     unauthenticated.
//  3. CSRF: a cookie-authenticated request that is not GET or HEAD must carry the session's
//     X-Forgebase-CSRF header (403 csrf_failed). Token-authenticated requests are exempt.
//  4. The user becomes a core.Caller of kind user; the HTTP layer never builds the system caller.
//
// No CORS headers are ever sent. Every error, including the rejections of what the Rust axum
// extractors reject (bad JSON, a body that is not JSON, a body over the limit, a path that is not
// UTF-8), uses the envelope {"error": {"code", "message", "details"?}}; internal errors are logged to
// standard error and answered with a generic message.
//
// Routes (prefix /api/v1):
//
//	POST /login                                    200 {"data": {"username", "roles", "csrf_token"}} and Set-Cookie
//	POST /logout (cookie session only)             204, cookie cleared
//	GET  /session (cookie session only, header     200 {"data": {"username", "roles", "csrf_token"}}, a new CSRF
//	     X-Forgebase-CSRF: refresh, else 400)      token (the old one stops working)
//	GET  /meta                                     200 {"data": [{"name", "label", "app", "icon", "submittable", "rights"}]}
//	GET  /meta/{doctype}                           200 {"data": {"name", "label", "app", "icon", "naming", "submittable",
//	                                               "title_field", "list_fields", "fields", "rights", "workflow"}}
//	GET  /resource/{doctype}                       200 {"data": [..]} (filters, sort, limit, offset, link_titles=1)
//	POST /resource/{doctype}                       201 {"data": doc}
//	GET  /resource/{doctype}/{name}                200 {"data": doc} (link_titles=1)
//	PATCH /resource/{doctype}/{name}               body {"version", "data"}: 200 {"data": doc}
//	DELETE /resource/{doctype}/{name}?version=N    204
//	POST /resource/{doctype}/{name}/submit         body {"version"}: 200 {"data": doc}; /cancel the same
//	POST /resource/{doctype}/{name}/amend          body {"version"}: 201 {"data": new doc}
//	POST /method/{app}.{method}                    body: a JSON object, optional (M9.1, ADR 0019): 200 {"data": ..}
//
// Anything else is 404, a wrong method is 405 with Allow. The routes of later phases (export, import,
// customize and user-permissions) answer 404 like any unknown path until the phase that ports them.
//
// With Config.DeskDir set, GET /desk/, /desk/index.html and /desk/assets/{name} serve the built desk
// from that directory (see desk.go for the allow-list and the headers). The Host routing applies to
// them like to every route.

// Config is the settings of the HTTP API.
type Config struct {
	// AllowedHosts are the host names the implicit single site answers to (case-insensitive, port
	// ignored), for example localhost or erp.example.com. An IPv6 address is written with brackets
	// ([::1]). Read only by ImplicitSites; with a config file each site has its own hosts (see
	// Sites.AddSite).
	AllowedHosts []string
	// SecureCookies adds Secure to the session cookie. false is for local development over plain HTTP
	// only.
	SecureCookies bool
	// BodyLimitBytes is the largest request body in bytes; a larger one is 413 payload_too_large.
	BodyLimitBytes int64
	// MaxConcurrentLogins is how many logins may hash a password at the same time; more are 503
	// too_many_logins with Retry-After. Argon2 uses about 19 MiB each, so this bounds memory. The
	// effective limit is at most half of the pool's maximum size (at least 1), whatever is set here, so
	// that with a pool of 2 or more connections logins can never hold all of them.
	MaxConcurrentLogins int
	// DeskDir is the directory of the built desk (desk/dist) to serve under /desk/, or "" (the default)
	// to serve no desk: /desk/* is then a 404.
	DeskDir string
}

// DefaultConfig is the Rust AppConfig::default() of the settings that exist so far.
func DefaultConfig() Config {
	return Config{
		AllowedHosts:        []string{"localhost", "127.0.0.1"},
		SecureCookies:       true,
		BodyLimitBytes:      1024 * 1024,
		MaxConcurrentLogins: 8,
	}
}

// State is the state of one site: its connection pool, the DocType registry and the settings. The
// routes of a State can only reach that site's database.
type State struct {
	pool *pgxpool.Pool
	// snapshot gives the registry of the site as it is now. A request calls it once, at its start, and
	// uses the result to the end, so it never sees two registries. P7 replaces the constant by the
	// holder that rebuilds the registry when a custom field changes (ADR 0023).
	snapshot func() *core.Registry
	config   Config
	logins   chan struct{}
	name     string
	routes   []*route
	// methods runs the app methods (M9.1); nil when the binary has none (every method call is a 404).
	methods MethodDispatch
}

// NewState creates the state of a site. The registry must be the one the database schema was synced
// with.
func NewState(pool *pgxpool.Pool, registry *core.Registry, config Config) *State {
	return NewStateFunc(pool, func() *core.Registry { return registry }, config)
}

// NewStateFunc is like NewState for a registry that changes while the server runs (the seam of the
// live reload of custom fields, P7): snapshot is called once per request.
func NewStateFunc(pool *pgxpool.Pool, snapshot func() *core.Registry, config Config) *State {
	s := &State{
		pool:     pool,
		snapshot: snapshot,
		config:   config,
		logins:   make(chan struct{}, loginPermits(config.MaxConcurrentLogins, int(pool.Config().MaxConns))),
		name:     "default",
	}
	s.routes = s.buildRoutes()
	return s
}

// loginPermits is the number of concurrent logins: configured, but at most half of the pool's maximum
// size and at least 1, so that with a pool of 2 or more logins never take all the connections other
// requests need.
func loginPermits(configured, poolMaxSize int) int {
	return max(min(configured, max(poolMaxSize/2, 1)), 1)
}

// Sites is the sites a server answers for, by Host (M7.1, ADR 0014). Every site has its own State:
// pool, registry and login limit. There is no shared pool, so a request for one site can only reach
// that site's database.
type Sites struct {
	// routes maps a lower-cased host name to its site.
	routes      map[string]*State
	unknownHost unknownHost
}

type unknownHost int

const (
	// hostInvalid is the implicit single-site mode: 400 invalid_host (the DNS-rebinding allow-list).
	hostInvalid unknownHost = iota
	// hostNotFound is the config file: 404 not_found, "no such site".
	hostNotFound
)

// ImplicitSites is the implicit single site (no config file): it answers to state's
// Config.AllowedHosts, and any other host is 400 invalid_host.
func ImplicitSites(state *State) *Sites {
	s := &Sites{routes: map[string]*State{}, unknownHost: hostInvalid}
	for _, host := range state.config.AllowedHosts {
		s.routes[strings.ToLower(strings.TrimSpace(host))] = state
	}
	return s
}

// ConfiguredSites has no site yet, for a config file: add them with AddSite. A well-formed host that
// no site owns is 404 not_found.
func ConfiguredSites() *Sites {
	return &Sites{routes: map[string]*State{}, unknownHost: hostNotFound}
}

// AddSite adds a site that answers to hosts (case-insensitive, no port). A host that another site
// already owns is an error: one host is one site.
func (s *Sites) AddSite(hosts []string, state *State) error {
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if _, taken := s.routes[host]; taken {
			return fmt.Errorf("host %s belongs to more than one site", host)
		}
		s.routes[host] = state
	}
	return nil
}

// pick is the site the request names, or the error response. A request target in absolute form
// (GET http://host/path) must name the same host as the Host: the site is chosen by Host alone, so a
// target that disagrees is refused (400 invalid_host) instead of being routed by one and answered for
// the other. (net/http already lets the authority of an absolute-form target replace the Host header
// of a real request; the check is for a request that carries both.)
func (s *Sites) pick(r *http.Request) (*State, *apiError) {
	// A Host header is promoted to Request.Host and removed from Header by the server; one still in the
	// map is a repeated one.
	if len(r.Header["Host"]) > 0 || r.Host == "" || !visibleASCII(r.Host) {
		return nil, errInvalidHost()
	}
	host := hostPart(r.Host)
	if r.URL != nil && r.URL.Host != "" && hostPart(authorityHost(r.URL)) != host {
		return nil, errInvalidHost()
	}
	if state, ok := s.routes[host]; ok {
		return state, nil
	}
	if s.unknownHost == hostNotFound && IsWellformedHost(host) {
		return nil, errNotFound("no such site")
	}
	return nil, errInvalidHost()
}

// authorityHost is the host of an absolute-form target, IPv6 with brackets.
func authorityHost(u *url.URL) string {
	host := u.Hostname()
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// visibleASCII is true when every byte is visible ASCII (or a tab), what the Rust HeaderValue::to_str
// requires.
func visibleASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if b := s[i]; !(b >= 0x20 && b < 0x7f || b == '\t') {
			return false
		}
	}
	return true
}

// hostPart is the host of a Host header value, lower-cased, without the port.
func hostPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if rest, ok := strings.CutPrefix(value, "["); ok {
		// [::1] or [::1]:8000
		address, port, found := strings.Cut(rest, "]")
		if found {
			if port == "" {
				return "[" + address + "]"
			}
			if p, ok := strings.CutPrefix(port, ":"); ok && isPort(p) {
				return "[" + address + "]"
			}
		}
		return value
	}
	if i := strings.LastIndexByte(value, ':'); i >= 0 && isPort(value[i+1:]) {
		return value[:i]
	}
	return value
}

func isPort(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

// ServeHTTP is the outermost layer, for every request: it picks the site by Host (before anything else
// runs, authentication included), hands the request to that site's routes and adds the headers every
// response gets (Cache-Control: no-store unless the handler chose a policy, which only the hashed desk
// assets do, and X-Content-Type-Options: nosniff).
func (s *Sites) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	tw := &trackingWriter{ResponseWriter: w}
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				panic(p)
			}
			e := errInternal(fmt.Sprintf("panic: %v", p))
			if tw.wrote {
				// The answer has started: it cannot be replaced, so drop the connection.
				panic(http.ErrAbortHandler)
			}
			e.write(tw)
		}
	}()
	state, err := s.pick(r)
	if err != nil {
		err.write(tw)
		return
	}
	state.serve(tw, r)
}

// trackingWriter remembers whether the response was started, so that a panic can still be answered.
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(status int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(status)
}

func (t *trackingWriter) Write(p []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (t *trackingWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }
