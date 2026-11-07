package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/db"
)

// Ported from crates/forgebase-http/tests/sites.rs (the parts that only need host routing and the
// resources): several sites behind one handler (M7.1, ADR 0014): each Host reaches its own database, and
// nothing of one site is visible on another. Two real databases, one handler.

const otherPassword = "a completely different one"

// pair is two sites, a.test and b.test, each with its own database and a user alice (Writer): the same
// username, with the password testPassword on A and otherPassword on B.
type pair struct {
	a, b *env
	h    http.Handler
}

func newPair(t *testing.T) *pair {
	t.Helper()
	a, b := newEnv(t), newEnv(t)
	a.user("alice", "Writer")
	if _, err := db.CreateUser(context.Background(), b.db.Pool, "alice", otherPassword, []string{"Writer"}); err != nil {
		t.Fatal(err)
	}
	sites := ConfiguredSites()
	if err := sites.AddSite([]string{"a.test"}, a.state); err != nil {
		t.Fatal(err)
	}
	if err := sites.AddSite([]string{"b.test", "b2.test"}, b.state); err != nil {
		t.Fatal(err)
	}
	return &pair{a: a, b: b, h: sites}
}

func (p *pair) send(t *testing.T, host string, r *req) reply {
	t.Helper()
	return sendTo(t, p.h, r.onHost(host))
}

func (p *pair) login(t *testing.T, host, password string) reply {
	t.Helper()
	return p.send(t, host, post("/api/v1/login").json(J{"username": "alice", "password": password}))
}

func (p *pair) session(t *testing.T, host, password string) session {
	t.Helper()
	rep := p.login(t, host, password)
	wantStatus(t, rep, http.StatusOK)
	// The cookie is host-scoped: no Domain attribute, so a browser never sends it to another host.
	if strings.Contains(strings.ToLower(rep.setCookies()[0]), "domain") {
		t.Error(rep.setCookies())
	}
	return sessionOf(t, rep)
}

// Rust: sites.rs::each_password_works_only_on_its_own_site
func TestEachPasswordWorksOnlyOnItsOwnSite(t *testing.T) {
	p := newPair(t)
	wantStatus(t, p.login(t, "a.test", testPassword), http.StatusOK)
	wantStatus(t, p.login(t, "b.test", otherPassword), http.StatusOK)
	// Both hosts of site B reach B.
	wantStatus(t, p.login(t, "b2.test", otherPassword), http.StatusOK)
	for _, c := range [][2]string{{"a.test", otherPassword}, {"b.test", testPassword}} {
		wantCode(t, p.login(t, c[0], c[1]), http.StatusUnauthorized, "unauthenticated", c[0])
	}
}

// Rust: sites.rs::a_session_of_one_site_is_refused_on_the_other
func TestASessionOfOneSiteIsRefusedOnTheOther(t *testing.T) {
	p := newPair(t)
	onA := p.session(t, "a.test", testPassword)
	onB := p.session(t, "b.test", otherPassword)
	wantStatus(t, p.send(t, "a.test", get("/api/v1/meta").cookie(onA)), http.StatusOK)
	wantStatus(t, p.send(t, "b.test", get("/api/v1/meta").cookie(onB)), http.StatusOK)
	// The cookie of A on B, and the other way round: the user alice exists on both, but the session lives
	// in the database of its own site.
	wantStatus(t, p.send(t, "b.test", get("/api/v1/meta").cookie(onA)), http.StatusUnauthorized)
	wantStatus(t, p.send(t, "a.test", get("/api/v1/meta").cookie(onB)), http.StatusUnauthorized)
	// A write with A's cookie and CSRF token is refused on B before anything is written.
	wantStatus(t, p.send(t, "b.test", post("/api/v1/resource/note").session(onA).json(J{"title": "x"})), http.StatusUnauthorized)
	// Logging out on A does not end the session on B.
	wantStatus(t, p.send(t, "a.test", post("/api/v1/logout").session(onA)), http.StatusNoContent)
	wantStatus(t, p.send(t, "b.test", get("/api/v1/meta").cookie(onB)), http.StatusOK)
}

// Rust: sites.rs::an_api_token_of_one_site_is_refused_on_the_other
func TestAnAPITokenOfOneSiteIsRefusedOnTheOther(t *testing.T) {
	p := newPair(t)
	token := p.a.apiToken("alice")
	wantStatus(t, p.send(t, "a.test", get("/api/v1/meta").token(token)), http.StatusOK)
	wantStatus(t, p.send(t, "b.test", get("/api/v1/meta").token(token)), http.StatusUnauthorized)
}

// Rust: sites.rs::documents_of_one_site_are_invisible_on_the_other
func TestDocumentsOfOneSiteAreInvisibleOnTheOther(t *testing.T) {
	p := newPair(t)
	onA := p.session(t, "a.test", testPassword)
	onB := p.session(t, "b.test", otherPassword)
	created := p.send(t, "a.test", post("/api/v1/resource/note").session(onA).json(J{"title": "only on A"}))
	wantStatus(t, created, http.StatusCreated)
	name := str(created.data(), "name")

	if n := len(p.send(t, "a.test", get("/api/v1/resource/note").cookie(onA)).dataList()); n != 1 {
		t.Errorf("%d notes on A", n)
	}
	listB := p.send(t, "b.test", get("/api/v1/resource/note").cookie(onB))
	wantStatus(t, listB, http.StatusOK)
	if len(listB.dataList()) != 0 {
		t.Error(listB.text)
	}
	target := "/api/v1/resource/note/" + name
	wantStatus(t, p.send(t, "b.test", get(target).cookie(onB)), http.StatusNotFound)
	wantStatus(t, p.send(t, "a.test", get(target).cookie(onA)), http.StatusOK)
	// And the data really is in one database only.
	if a, b := p.a.db.Rows("SELECT count(*)::text FROM doc_note")[0][0], p.b.db.Rows("SELECT count(*)::text FROM doc_note")[0][0]; a != "1" || b != "0" {
		t.Errorf("counts %s %s", a, b)
	}
}

// Rust: sites.rs::the_meta_api_works_on_every_site
func TestTheMetaAPIWorksOnEverySite(t *testing.T) {
	p := newPair(t)
	for _, c := range [][2]string{{"a.test", testPassword}, {"b.test", otherPassword}} {
		s := p.session(t, c[0], c[1])
		rep := p.send(t, c[0], get("/api/v1/meta/note").cookie(s))
		wantStatus(t, rep, http.StatusOK, c[0])
		eqJSON(t, rep.dataObj()["name"], `"note"`)
	}
}

// Rust: sites.rs::unknown_hosts_are_404_malformed_ones_400_and_forwarding_headers_are_ignored
func TestUnknownHostsAre404MalformedOnes400AndForwardingHeadersAreIgnored(t *testing.T) {
	p := newPair(t)
	for _, host := range []string{"c.test", "localhost", "a.test.evil.test"} {
		rep := p.send(t, host, get("/api/v1/meta"))
		wantCode(t, rep, http.StatusNotFound, "not_found", host)
		if rep.header.Get("Cache-Control") != "no-store" {
			t.Error(rep.header)
		}
		// Login on an unknown host is the same 404: no site, no database.
		wantStatus(t, p.send(t, host, post("/api/v1/login").json(J{"username": "alice", "password": testPassword})), http.StatusNotFound, host)
	}
	for _, host := range []string{"a.test:80@evil.test", "a b"} {
		wantCode(t, p.send(t, host, get("/api/v1/meta")), http.StatusBadRequest, "invalid_host", host)
	}
	wantCode(t, sendTo(t, p.h, get("/api/v1/meta").noHost()), http.StatusBadRequest, "invalid_host")

	// X-Forwarded-Host and Forwarded do not choose the site: A's password still does not work on B's
	// Host, and an unknown Host stays unknown.
	forwarded := func(r *req) *req { return r.header("X-Forwarded-Host", "a.test").header("Forwarded", "host=a.test") }
	rep := p.send(t, "b.test", forwarded(post("/api/v1/login").json(J{"username": "alice", "password": testPassword})))
	wantStatus(t, rep, http.StatusUnauthorized)
	wantStatus(t, p.send(t, "c.test", forwarded(get("/api/v1/meta"))), http.StatusNotFound)
}

// Rust: sites.rs::an_absolute_form_target_must_agree_with_the_host_header
//
// LOW-3 of the M7.1 review: a request target in absolute form must name the host of the Host header
// (case and port aside); a different one is 400 invalid_host, never a request routed by one host and
// answered for the other.
func TestAnAbsoluteFormTargetMustAgreeWithTheHostHeader(t *testing.T) {
	p := newPair(t)
	// The same host (case and port ignored) is fine: the request reaches the site.
	for _, c := range [][2]string{
		{"http://a.test/api/v1/meta", "a.test"}, {"http://A.TEST:8000/api/v1/meta", "a.test:9000"}, {"http://a.test/api/v1/meta", "A.Test"},
	} {
		// Authentication comes after routing: a 401 shows the site was reached.
		wantStatus(t, p.send(t, c[1], get(c[0])), http.StatusUnauthorized, c)
	}
	// Another host in the target, on a known or an unknown host: refused.
	for _, c := range [][2]string{
		{"http://b.test/api/v1/meta", "a.test"}, {"http://a.test/api/v1/meta", "b.test"},
		{"http://c.test/api/v1/meta", "a.test"}, {"http://a.test.evil.test/api/v1/meta", "a.test"},
	} {
		wantCode(t, p.send(t, c[1], get(c[0])), http.StatusBadRequest, "invalid_host", c)
	}
}

// The routing of an absolute-form target reaches the same path as the origin form, also with a real
// server (net/http lets the authority of the target replace the Host header).
func TestAnAbsoluteFormTargetIsRoutedByItsPath(t *testing.T) {
	p := newPair(t)
	hr := httptest.NewRequest(http.MethodGet, "http://a.test/api/v1/meta?x=1", nil)
	hr.RequestURI = "http://a.test/api/v1/meta?x=1"
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, hr)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}
