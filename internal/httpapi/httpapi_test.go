package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Ported from the unit tests of crates/forgebase-http/src/lib.rs, plus tests of the router that the
// Rust crate gets from axum.

// Rust: lib.rs::tests::login_permits_never_take_every_connection
func TestLoginPermitsNeverTakeEveryConnection(t *testing.T) {
	for _, c := range [][3]int{
		{8, 16, 8}, {8, 8, 4}, {8, 9, 4}, {8, 4, 2},
		// A pool of one or two still allows one login at a time.
		{8, 2, 1}, {8, 1, 1}, {8, 0, 1},
		// 0 configured still means 1; a small setting is kept.
		{0, 16, 1}, {2, 64, 2},
	} {
		if got := loginPermits(c[0], c[1]); got != c[2] {
			t.Errorf("loginPermits(%d, %d) = %d, want %d", c[0], c[1], got, c[2])
		}
	}
}

// lazyPool is a pool that never connects: pgxpool creates connections when they are needed.
func lazyPool(t testing.TB, maxSize int32) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig("postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = maxSize
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func lazyState(t testing.TB) *State {
	return NewState(lazyPool(t, 2), core.NewRegistry(), DefaultConfig())
}

// Rust: lib.rs::tests::a_login_over_the_bound_is_503_without_touching_the_database
func TestALoginOverTheBoundIs503WithoutTouchingTheDatabase(t *testing.T) {
	config := DefaultConfig()
	config.MaxConcurrentLogins = 1
	state := NewState(lazyPool(t, 2), core.NewRegistry(), config)
	if cap(state.logins) != 1 {
		t.Fatalf("permits %d", cap(state.logins))
	}
	state.logins <- struct{}{} // the only permit
	rep := sendTo(t, ImplicitSites(state), post("/api/v1/login").json(J{"username": "a", "password": "b"}))
	wantStatus(t, rep, http.StatusServiceUnavailable)
	if rep.header.Get("Retry-After") != "1" || !strings.Contains(rep.text, "too_many_logins") {
		t.Errorf("%v %s", rep.header, rep.text)
	}
}

// probe sends GET /api/v1/nothing with the given Host (nil: none) and extra headers.
func probe(t testing.TB, h http.Handler, host *string, extra ...string) (int, string) {
	t.Helper()
	r := get("/api/v1/nothing")
	if host == nil {
		r.noHost()
	} else {
		r.onHost(*host)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		r.header(extra[i], extra[i+1])
	}
	rep := sendTo(t, h, r)
	// Every answer, the refusals included, carries the response headers.
	if rep.header.Get("Cache-Control") != "no-store" || rep.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers %v", rep.header)
	}
	return rep.status, rep.text
}

// Rust: lib.rs::tests::the_implicit_site_answers_400_to_every_host_it_does_not_own
func TestTheImplicitSiteAnswers400ToEveryHostItDoesNotOwn(t *testing.T) {
	h := ImplicitSites(lazyState(t))
	// An owned host reaches the site's routes (an unknown route is its own 404).
	code, body := probe(t, h, ptr("LocalHost:8000"))
	if code != http.StatusNotFound || !strings.Contains(body, "no such route") {
		t.Errorf("%d %s", code, body)
	}
	for _, host := range []*string{nil, ptr("evil.example"), ptr("bad host"), ptr("localhost:80@evil.com")} {
		code, body := probe(t, h, host)
		if code != http.StatusBadRequest || !strings.Contains(body, "invalid_host") {
			t.Errorf("%v: %d %s", host, code, body)
		}
	}
}

// Rust: lib.rs::tests::with_sites_an_unknown_host_is_404_and_a_malformed_one_is_400
func TestWithSitesAnUnknownHostIs404AndAMalformedOneIs400(t *testing.T) {
	sites := ConfiguredSites()
	if err := sites.AddSite([]string{"a.test", "A2.test"}, lazyState(t)); err != nil {
		t.Fatal(err)
	}
	if err := sites.AddSite([]string{"b.test"}, lazyState(t)); err != nil {
		t.Fatal(err)
	}
	// Both sites' hosts reach a site (whose unknown route is "no such route").
	for _, host := range []string{"a.test", "a2.test:8000", "B.TEST"} {
		code, body := probe(t, sites, &host)
		if code != http.StatusNotFound || !strings.Contains(body, "no such route") {
			t.Errorf("%s: %d %s", host, code, body)
		}
	}
	// No site owns it: a generic 404 envelope.
	for _, host := range []string{"c.test", "localhost", "[::1]", "10.0.0.1:80"} {
		code, body := probe(t, sites, &host)
		if code != http.StatusNotFound || !strings.Contains(body, `"not_found"`) || !strings.Contains(body, "no such site") {
			t.Errorf("%s: %d %s", host, code, body)
		}
	}
	// Missing, malformed and non-UTF-8 hosts stay 400.
	for _, host := range []*string{nil, ptr(""), ptr("bad host"), ptr("a.test:80@evil.com"), ptr("a.test/x"), ptr("a.test\xff")} {
		code, body := probe(t, sites, host)
		if code != http.StatusBadRequest || !strings.Contains(body, "invalid_host") {
			t.Errorf("%v: %d %s", host, code, body)
		}
	}
	// Forwarding headers never choose the site.
	code, body := probe(t, sites, ptr("c.test"), "X-Forwarded-Host", "a.test", "Forwarded", "host=a.test")
	if code != http.StatusNotFound || !strings.Contains(body, `"not_found"`) || strings.Contains(body, "no such route") {
		t.Errorf("%d %s", code, body)
	}
}

// Rust: lib.rs::tests::one_host_is_one_site
func TestOneHostIsOneSite(t *testing.T) {
	sites := ConfiguredSites()
	if err := sites.AddSite([]string{"a.test"}, lazyState(t)); err != nil {
		t.Fatal(err)
	}
	err := sites.AddSite([]string{"A.test"}, lazyState(t))
	if err == nil || !strings.Contains(err.Error(), "a.test") {
		t.Errorf("%v", err)
	}
}

// Rust: lib.rs::tests::host_part_drops_port_and_case
func TestHostPartDropsPortAndCase(t *testing.T) {
	for in, want := range map[string]string{
		"Example.COM":           "example.com",
		"example.com:8000":      "example.com",
		"[::1]":                 "[::1]",
		"[::1]:8000":            "[::1]",
		"localhost:80@evil.com": "localhost:80@evil.com", // not a port: stays whole and so never matches
		"localhost:":            "localhost:",
		"[::1]x":                "[::1]x",
	} {
		if got := hostPart(in); got != want {
			t.Errorf("hostPart(%q) = %q, want %q", in, got, want)
		}
	}
}

// A panic in a handler is answered with the generic 500 envelope and does not stop the server.
func TestAPanicInAHandlerIsAGeneric500(t *testing.T) {
	state := lazyState(t)
	state.routes = append(state.routes, &route{
		segments: pattern("/boom"), allow: "GET,HEAD",
		methods: map[string]handlerFunc{"GET": func(*State, http.ResponseWriter, *request) error { panic("secret detail") }},
	})
	var logged strings.Builder
	logMu.Lock()
	old := logWriter
	logWriter = &logged
	logMu.Unlock()
	defer func() {
		logMu.Lock()
		logWriter = old
		logMu.Unlock()
	}()
	rep := sendTo(t, ImplicitSites(state), get("/boom"))
	assertEnvelope(t, rep, http.StatusInternalServerError, "internal")
	if strings.Contains(rep.text, "secret detail") || !strings.Contains(logged.String(), "secret detail") {
		t.Errorf("%s / %s", rep.text, logged.String())
	}
}

// The routing as axum does it: the raw path is matched, a parameter is a whole segment (it may be empty
// when a "/" follows it, as in matchit, but not as the last segment), a trailing slash is an unknown path
// and the Allow header lists the methods.
func TestRoutingAsAxumDoesIt(t *testing.T) {
	h := ImplicitSites(lazyState(t))
	for _, path := range []string{
		"/api/v1/resource/x/", "/api/v1/resource/", "/api/v1/resource//", "/api/v1/resource/x/y/z/submit/",
		"/api/v1/resource/x/y/unknown", "/API/v1/login", "/api/v1/login/", "//api/v1/login", "/api/v1/%6Cogin",
	} {
		rep := sendTo(t, h, get(path))
		assertEnvelope(t, rep, http.StatusNotFound, "not_found")
	}
	for path, allow := range map[string]string{
		"/api/v1/login":                 "POST",
		"/api/v1/session":               "GET,HEAD",
		"/api/v1/resource/note":         "GET,HEAD,POST",
		"/api/v1/resource/note/x":       "GET,HEAD,PATCH,DELETE",
		"/api/v1/resource/note/x/amend": "POST",
		// A percent-encoded slash stays inside the segment.
		"/api/v1/resource/a%2Fb": "GET,HEAD,POST",
		// An empty parameter followed by "/" is a parameter (matchit): the route of the name.
		"/api/v1/resource//x":        "GET,HEAD,PATCH,DELETE",
		"/api/v1/resource/x//submit": "POST",
		"/api/v1/resource//x/amend":  "POST",
	} {
		rep := sendTo(t, h, reqOf(http.MethodPut, path))
		assertEnvelope(t, rep, http.StatusMethodNotAllowed, "method_not_allowed")
		if got := rep.header.Get("Allow"); got != allow {
			t.Errorf("%s: Allow %q, want %q", path, got, allow)
		}
	}
	// The routes of the later phases are unknown paths until then.
	for _, path := range []string{
		"/api/v1/resource/note/x/transition", "/api/v1/resource/note/x/transitions", "/api/v1/resource/note/x/history",
		"/api/v1/views/note", "/api/v1/summary/note", "/api/v1/report", "/api/v1/export/note", "/api/v1/import/note",
		"/api/v1/method/a.b", "/api/v1/customize/note/fields", "/api/v1/user-permissions",
	} {
		assertEnvelope(t, sendTo(t, h, get(path)), http.StatusNotFound, "not_found")
	}
}

// Rust: (axum) a Path extractor percent-decodes a parameter and rejects what is not UTF-8.
func TestPercentDecode(t *testing.T) {
	for in, want := range map[string]string{
		"a":         "a",
		"a%20b":     "a b",
		"a+b":       "a+b",
		"%2F":       "/",
		"%e2%82%ac": "€",
		"100%":      "100%",
		"%zz":       "%zz",
		"%4":        "%4",
		"note%00":   "note\x00",
	} {
		got, ok := percentDecode(in)
		if !ok || got != want {
			t.Errorf("percentDecode(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"%FF", "%C3", "a%80"} {
		if _, ok := percentDecode(bad); ok {
			t.Errorf("%q is not UTF-8", bad)
		}
	}
}

// Rust: (axum Query over serde_urlencoded) pairs in order, "+" is a space, bad UTF-8 becomes U+FFFD.
func TestQueryPairs(t *testing.T) {
	got := queryPairs("a=1&b=x+y&c&&d=%41%2b&e=%FF&a=2&=v")
	want := [][2]string{{"a", "1"}, {"b", "x y"}, {"c", ""}, {"d", "A+"}, {"e", "�"}, {"a", "2"}, {"", "v"}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %v, want %v", i, got[i], want[i])
		}
	}
	if len(queryPairs("")) != 0 {
		t.Error("empty query")
	}
}

func TestRawPath(t *testing.T) {
	for _, c := range [][2]string{
		{"/a/b?x=1", "/a/b"}, {"/a%2Fb", "/a%2Fb"}, {"http://h.test/a/b?x", "/a/b"}, {"http://h.test", "/"},
		{"/a/../b", "/a/../b"}, {"*", "*"},
	} {
		r, _ := http.NewRequest(http.MethodGet, "http://h.test/", nil)
		r.RequestURI = c[0]
		if got := rawPath(r); got != c[1] {
			t.Errorf("rawPath(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

// A JSON body as the axum Json extractor takes it: the content type, the size and the syntax are
// checked in this order, and what serde_json refuses (invalid UTF-8, a lone surrogate, text after the
// value) is refused here too.
func TestReadJSON(t *testing.T) {
	state := lazyState(t)
	state.config.BodyLimitBytes = 64
	h := ImplicitSites(state)
	// The login route reads the body before it touches the database; a body that does not parse never
	// reaches it.
	for _, c := range []struct {
		contentType string
		body        string
		status      int
		code        string
	}{
		{"application/json", `{`, 400, "bad_request"},
		{"application/json", ``, 400, "bad_request"},
		{"application/json", `{} {}`, 400, "bad_request"},
		{"application/json", "{\"username\":\"\xff\"}", 400, "bad_request"},
		{"application/json", `{"username":"\ud800"}`, 400, "bad_request"},
		{"application/json", `{"username":"\udc00"}`, 400, "bad_request"},
		{"application/json", `{"username":"\ud800x"}`, 400, "bad_request"},
		{"application/json", `[1]`, 400, "bad_request"},
		{"application/json", `{"username":1,"password":"x"}`, 400, "bad_request"},
		{"text/plain", `{}`, 415, "unsupported_media_type"},
		{"", `{}`, 415, "unsupported_media_type"},
		{"application/jsonx", `{}`, 415, "unsupported_media_type"},
		{"application/json", `{"username":"` + strings.Repeat("x", 100) + `"}`, 413, "payload_too_large"},
		// The content type is checked before the size.
		{"text/plain", strings.Repeat("x", 100), 415, "unsupported_media_type"},
	} {
		r := post("/api/v1/login")
		if c.contentType != "" {
			r.raw(c.contentType, []byte(c.body))
		} else {
			r.body = []byte(c.body)
		}
		rep := sendTo(t, h, r)
		if rep.status != c.status || rep.code() != c.code {
			t.Errorf("%q %.20q: %d %s", c.contentType, c.body, rep.status, rep.text)
		}
	}
	// application/json with a parameter and the +json suffix are JSON.
	for _, ct := range []string{"application/json; charset=utf-8", "Application/JSON", "application/vnd.api+json"} {
		if !jsonContentType(http.Header{"Content-Type": {ct}}) {
			t.Errorf("%q", ct)
		}
	}
	for _, ct := range []string{"", "text/json", "application/", "json", "application/json;;;="} {
		if jsonContentType(http.Header{"Content-Type": {ct}}) && ct != "application/json;;;=" {
			t.Errorf("%q", ct)
		}
	}
}

func TestMarshalIsSerdeLike(t *testing.T) {
	bs := "\\"
	b, err := marshal(map[string]any{"b": "<&>" + string(rune(0x2028)), "a": []any{1, nil, true}, "c": bs + "u2028"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":[1,null,true],"b":"<&>` + string(rune(0x2028)) + `","c":"` + bs + bs + `u2028"}`
	if got := string(b); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// An empty parameter followed by "/" reaches the handler: authentication first (401), then the unknown
// DocType of the empty name (404 unknown doctype), and x//submit reaches submit.
func TestAnEmptyParameterReachesTheHandler(t *testing.T) {
	h := ImplicitSites(lazyState(t))
	assertEnvelope(t, sendTo(t, h, get("/api/v1/resource//x")), http.StatusUnauthorized, "unauthenticated")
	assertEnvelope(t, sendTo(t, h, post("/api/v1/resource/x//submit").json(J{"version": 1})), http.StatusUnauthorized, "unauthenticated")
}

// A panic after the answer has started cannot be answered with the 500 envelope: the connection is
// dropped (http.ErrAbortHandler), the answer is not finished with a second body.
func TestAPanicAfterTheAnswerStartedDropsTheConnection(t *testing.T) {
	state := lazyState(t)
	state.routes = append(state.routes, &route{
		segments: pattern("/half"), allow: "GET,HEAD",
		methods: map[string]handlerFunc{"GET": func(_ *State, w http.ResponseWriter, _ *request) error {
			w.WriteHeader(http.StatusOK)
			panic("late")
		}},
	})
	logMu.Lock()
	old := logWriter
	logWriter = &strings.Builder{}
	logMu.Unlock()
	defer func() {
		logMu.Lock()
		logWriter = old
		logMu.Unlock()
	}()
	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", p)
		}
	}()
	sendTo(t, ImplicitSites(state), get("/half"))
	t.Error("no panic")
}
