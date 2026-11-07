package httpapi

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The routes of one site and their matching. The router is written for this project instead of using
// http.ServeMux because the API must route like the Rust axum router: the raw request path is matched
// (a "%2F" in a segment stays inside the segment, "/a//b" and "/a/b/" are not cleaned or redirected),
// a parameter is a whole segment (empty unless it is the last one) that is percent-decoded only when a handler reads it
// (a decoded value that is not UTF-8 is 400 bad_request "invalid path", after authentication), an
// unknown path is 404 and a known path with another method is 405 with the Allow header.

// handlerFunc handles a matched request. It returns the error to answer with, or nil after it wrote
// the response.
type handlerFunc func(s *State, w http.ResponseWriter, r *request) error

type route struct {
	// segments are the segments of the path pattern; "{}" is a parameter.
	segments []string
	methods  map[string]handlerFunc
	// allow is the Allow header of a 405, in the order axum writes it (a GET route allows HEAD too).
	allow string
}

// request is an incoming request with what the router found out about it.
type request struct {
	*http.Request
	// rawPath is the request path as sent, not decoded.
	rawPath string
	// params are the raw (not decoded) values of the parameters of the route, in order.
	params []string
}

// param is parameter i, percent-decoded. ok is false when the value is not UTF-8 after decoding.
func (r *request) param(i int) (string, bool) { return percentDecode(r.params[i]) }

// pathParams are the first n parameters decoded, or the 400 "invalid path" of the first that is not
// UTF-8.
func (r *request) pathParams(n int) ([]string, error) {
	out := make([]string, n)
	for i := range out {
		v, ok := r.param(i)
		if !ok {
			return nil, errBadRequest("invalid path")
		}
		out[i] = v
	}
	return out, nil
}

// percentDecode decodes %XX sequences ("+" stays); a "%" that is not followed by two hex digits stays
// as it is (as in axum). ok is false when the result is not valid UTF-8. A target whose "%" is not
// followed by two hex digits (such as /a/%zz) never reaches the router of a real server: net/http's
// parser refuses it first (see the CHANGELOG, "Differences from Rust"); handlers called directly get it.
func percentDecode(s string) (string, bool) {
	if !strings.Contains(s, "%") {
		return s, true
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && unhex(s[i+1]) >= 0 && unhex(s[i+2]) >= 0 {
			out = append(out, byte(unhex(s[i+1])<<4|unhex(s[i+2])))
			i += 2
			continue
		}
		out = append(out, s[i])
	}
	if !utf8.Valid(out) {
		return "", false
	}
	return string(out), true
}

func unhex(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}

// rawPath is the path of the request target as it was sent.
func rawPath(r *http.Request) string {
	uri := r.RequestURI
	if uri == "" {
		return r.URL.EscapedPath()
	}
	if i := strings.IndexAny(uri, "?#"); i >= 0 {
		uri = uri[:i]
	}
	if strings.HasPrefix(uri, "/") {
		return uri
	}
	// Absolute form: scheme://authority/path.
	if i := strings.Index(uri, "://"); i >= 0 {
		rest := uri[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return rest[j:]
		}
		return "/"
	}
	return uri
}

// match is the route whose pattern fits path, and the raw parameter values.
func (s *State) match(path string) (*route, []string) {
	if !strings.HasPrefix(path, "/") {
		return nil, nil
	}
	segments := strings.Split(path[1:], "/")
routes:
	for _, rt := range s.routes {
		if len(rt.segments) != len(segments) {
			continue
		}
		var params []string
		for i, want := range rt.segments {
			switch {
			case want == "{}":
				// matchit (tree.rs, the Param branch) takes an empty parameter when a "/" follows it; only
				// an empty last segment finds no route.
				if segments[i] == "" && i == len(segments)-1 {
					continue routes
				}
				params = append(params, segments[i])
			case want != segments[i]:
				continue routes
			}
		}
		return rt, params
	}
	return nil, nil
}

// pattern is a path pattern as segments.
func pattern(path string) []string {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, s := range segments {
		if strings.HasPrefix(s, "{") {
			segments[i] = "{}"
		}
	}
	return segments
}

// buildRoutes is the table of the routes of a site (the Rust site_router).
func (s *State) buildRoutes() []*route {
	h := func(m map[string]handlerFunc, allow string, path string) *route {
		return &route{segments: pattern(path), methods: m, allow: allow}
	}
	routes := []*route{
		h(map[string]handlerFunc{"POST": handleLogin}, "POST", "/api/v1/login"),
		h(map[string]handlerFunc{"POST": handleLogout}, "POST", "/api/v1/logout"),
		h(map[string]handlerFunc{"GET": handleSession}, "GET,HEAD", "/api/v1/session"),
		h(map[string]handlerFunc{"GET": metaList}, "GET,HEAD", "/api/v1/meta"),
		h(map[string]handlerFunc{"GET": metaRead}, "GET,HEAD", "/api/v1/meta/{doctype}"),
		h(map[string]handlerFunc{"GET": handleList, "POST": handleCreate}, "GET,HEAD,POST", "/api/v1/resource/{doctype}"),
		h(map[string]handlerFunc{"GET": handleRead, "PATCH": handleUpdate, "DELETE": handleDelete}, "GET,HEAD,PATCH,DELETE",
			"/api/v1/resource/{doctype}/{name}"),
		h(map[string]handlerFunc{"POST": handleSubmit}, "POST", "/api/v1/resource/{doctype}/{name}/submit"),
		h(map[string]handlerFunc{"POST": handleCancel}, "POST", "/api/v1/resource/{doctype}/{name}/cancel"),
		h(map[string]handlerFunc{"POST": handleAmend}, "POST", "/api/v1/resource/{doctype}/{name}/amend"),
		h(map[string]handlerFunc{"POST": handleTransition}, "POST", "/api/v1/resource/{doctype}/{name}/transition"),
		h(map[string]handlerFunc{"GET": handleTransitions}, "GET,HEAD", "/api/v1/resource/{doctype}/{name}/transitions"),
		h(map[string]handlerFunc{"GET": handleHistory}, "GET,HEAD", "/api/v1/resource/{doctype}/{name}/history"),
		h(map[string]handlerFunc{"GET": handleViewList, "POST": handleViewCreate}, "GET,HEAD,POST", "/api/v1/views/{doctype}"),
		h(map[string]handlerFunc{"PATCH": handleViewUpdate, "DELETE": handleViewDelete}, "PATCH,DELETE", "/api/v1/views/{doctype}/{id}"),
		h(map[string]handlerFunc{"GET": handleSummary}, "GET,HEAD", "/api/v1/summary/{doctype}"),
		h(map[string]handlerFunc{"GET": handleReportList}, "GET,HEAD", "/api/v1/report"),
		h(map[string]handlerFunc{"POST": handleReportRun}, "POST", "/api/v1/report/{name}"),
	}
	// The desk routes exist only when a directory is configured.
	if s.config.DeskDir != "" {
		routes = append(routes,
			h(map[string]handlerFunc{"GET": deskIndex}, "GET,HEAD", "/desk/"),
			h(map[string]handlerFunc{"GET": deskIndex}, "GET,HEAD", "/desk/index.html"),
			h(map[string]handlerFunc{"GET": deskAsset}, "GET,HEAD", "/desk/assets/{name}"),
		)
	}
	return routes
}

// serve runs the request through the routes of the site.
func (s *State) serve(w http.ResponseWriter, hr *http.Request) {
	req := &request{Request: hr, rawPath: rawPath(hr)}
	rt, params := s.match(req.rawPath)
	if rt == nil {
		errNotFound("no such route").write(w)
		return
	}
	req.params = params
	method := hr.Method
	handler := rt.methods[method]
	// HEAD is answered by the GET handler (the server drops the body).
	if handler == nil && method == http.MethodHead {
		handler = rt.methods[http.MethodGet]
	}
	if handler == nil {
		w.Header().Set("Allow", rt.allow)
		newError(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed").write(w)
		return
	}
	if err := handler(s, w, req); err != nil {
		fromError(err).write(w)
	}
}

// ---- request bodies and query strings ---------------------------------------------------------

// jsonContentType is true for application/json and application/*+json, the content types axum's Json
// extractor accepts (parameters such as charset are allowed).
func jsonContentType(h http.Header) bool {
	value := h.Get("Content-Type")
	if value == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	typ, sub, ok := strings.Cut(mt, "/")
	if !ok || typ != "application" {
		return false
	}
	return sub == "json" || strings.HasSuffix(sub, "+json")
}

// readJSON reads the request body as one JSON value (what the Rust ApiJson extractor does): 415 for
// another content type (checked before the body is read), 413 over the body limit, 400 for a body that
// cannot be read or is not valid JSON. Numbers are jsonNumber.
func (s *State) readJSON(w http.ResponseWriter, r *request) (any, error) {
	if !jsonContentType(r.Header) {
		return nil, newError(http.StatusUnsupportedMediaType, "unsupported_media_type",
			"the body must be JSON (Content-Type: application/json)")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.config.BodyLimitBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, newError(http.StatusRequestEntityTooLarge, "payload_too_large", "the request body is too large")
		}
		return nil, errBadRequest("the body is not valid JSON")
	}
	// serde_json::from_slice refuses a body that is not UTF-8; the parser (jsonparse.go) takes a string and
	// has serde_json's other refusals (lone surrogates, the nesting limit of 128).
	if !utf8.Valid(body) {
		return nil, errBadRequest("the body is not valid JSON")
	}
	v, err := decodeJSON(string(body))
	if err != nil {
		return nil, errBadRequest("the body is not valid JSON")
	}
	return v, nil
}

// queryPairs is the query string as ordered pairs, so that a repeated parameter is still visible
// (the Rust Query<Vec<(String, String)>> of serde_urlencoded): pairs are split at "&", name and value
// at the first "=", "+" is a space, a "%XX" is decoded, and bytes that are not UTF-8 become U+FFFD.
// An empty pair is skipped.
func queryPairs(raw string) [][2]string {
	var pairs [][2]string
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		pairs = append(pairs, [2]string{formDecode(name), formDecode(value)})
	}
	return pairs
}

func formDecode(s string) string {
	s = strings.ReplaceAll(s, "+", " ")
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && unhex(s[i+1]) >= 0 && unhex(s[i+2]) >= 0 {
			out = append(out, byte(unhex(s[i+1])<<4|unhex(s[i+2])))
			i += 2
			continue
		}
		out = append(out, s[i])
	}
	return core.LossyUTF8(string(out))
}
