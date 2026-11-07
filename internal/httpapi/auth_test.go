package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/tvstore/forgebase-go/internal/db"
)

// Ported from crates/forgebase-http/tests/auth.rs: login, sessions, CSRF, API tokens and Host
// validation over HTTP, against a real PostgreSQL.

func envWithUsers(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.user("alice", "Writer")
	e.user("bob", "Writer")
	return e
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if unhex(s[i]) < 0 {
			return false
		}
	}
	return true
}

// Rust: auth.rs::login_sets_the_cookie_with_the_exact_attributes
func TestLoginSetsTheCookieWithTheExactAttributes(t *testing.T) {
	e := envWithUsers(t)
	rep := e.tryLogin("alice", testPassword)
	wantStatus(t, rep, http.StatusOK)
	cookies := rep.setCookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies %v", cookies)
	}
	value, attributes, ok := strings.Cut(cookies[0], "; ")
	if !ok {
		t.Fatalf("no attributes in %q", cookies[0])
	}
	token, ok := strings.CutPrefix(value, "fb_session=")
	if !ok || len(token) != 64 || !isHex(token) {
		t.Fatalf("cookie %q", value)
	}
	if want := "HttpOnly; Secure; SameSite=Lax; Path=/; Max-Age=604800"; attributes != want {
		t.Errorf("attributes %q, want %q", attributes, want)
	}
	data := rep.dataObj()
	if data["username"] != "alice" {
		t.Error(data)
	}
	eqJSON(t, data["roles"], `["Writer"]`)
	if csrf, _ := data["csrf_token"].(string); len(csrf) != 64 {
		t.Error(data)
	}
	// Nothing else in the body: neither the password nor the session token.
	if len(data) != 3 || strings.Contains(rep.text, token) || strings.Contains(rep.text, testPassword) {
		t.Errorf("body %s", rep.text)
	}
	if rep.header.Get("Cache-Control") != "no-store" {
		t.Error(rep.header)
	}
}

// Rust: auth.rs::dev_mode_omits_secure
func TestDevModeOmitsSecure(t *testing.T) {
	config := DefaultConfig()
	config.SecureCookies = false
	e := newEnvWith(t, config)
	e.user("alice", "Writer")
	cookie := e.tryLogin("alice", testPassword).setCookies()[0]
	if !strings.HasSuffix(cookie, "; HttpOnly; SameSite=Lax; Path=/; Max-Age=604800") || strings.Contains(cookie, "Secure") {
		t.Error(cookie)
	}
}

// Rust: auth.rs::a_failed_login_is_the_same_401_for_every_reason
func TestAFailedLoginIsTheSame401ForEveryReason(t *testing.T) {
	e := envWithUsers(t)
	e.user("carol")
	e.db.Exec("UPDATE fb_user SET enabled = false WHERE username = 'carol'")
	wrong := e.tryLogin("alice", "wrong password")
	unknown := e.tryLogin("nobody", testPassword)
	disabled := e.tryLogin("carol", testPassword)
	malformed := e.tryLogin("", "x")
	for _, rep := range []reply{wrong, unknown, disabled, malformed} {
		wantStatus(t, rep, http.StatusUnauthorized)
		if len(rep.setCookies()) != 0 {
			t.Error("cookie on a failed login")
		}
	}
	if wrong.text != unknown.text || wrong.text != disabled.text || wrong.text != malformed.text {
		t.Errorf("different answers: %q %q %q %q", wrong.text, unknown.text, disabled.text, malformed.text)
	}
	if wrong.code() != "unauthenticated" {
		t.Error(wrong.text)
	}
}

// Rust: auth.rs::lockout_works_over_http
func TestLockoutWorksOverHTTP(t *testing.T) {
	e := envWithUsers(t)
	for i := 0; i < 5; i++ {
		wantStatus(t, e.tryLogin("alice", "wrong password"), http.StatusUnauthorized)
	}
	// Locked: even the right password fails, with the very same answer.
	locked := e.tryLogin("alice", testPassword)
	wrong := e.tryLogin("alice", "wrong password")
	wantStatus(t, locked, http.StatusUnauthorized)
	if locked.text != wrong.text {
		t.Errorf("%q vs %q", locked.text, wrong.text)
	}
	// Other users are not affected.
	wantStatus(t, e.tryLogin("bob", testPassword), http.StatusOK)
}

// Rust: auth.rs::login_bodies_that_are_not_credentials_are_400
func TestLoginBodiesThatAreNotCredentialsAre400(t *testing.T) {
	e := envWithUsers(t)
	for _, body := range []any{
		J{},
		J{"username": "alice"},
		J{"username": 1, "password": "x"},
		J{"username": "alice", "password": "x", "extra": 1},
		[]any{},
	} {
		rep := e.send(post("/api/v1/login").json(body))
		wantCode(t, rep, http.StatusBadRequest, "bad_request", body)
	}
}

// Rust: auth.rs::concurrent_logins_are_bounded
func TestConcurrentLoginsAreBounded(t *testing.T) {
	config := DefaultConfig()
	config.MaxConcurrentLogins = 1
	e := newEnvWith(t, config)
	e.user("alice", "Writer")
	// Hold the only permit's competitors back until all eight are running, so that the excess is
	// certain to meet the busy permit.
	var wg, ready sync.WaitGroup
	start := make(chan struct{})
	replies := make([]reply, 8)
	ready.Add(len(replies))
	for i := range replies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			replies[i] = e.tryLogin("alice", testPassword)
		}()
	}
	ready.Wait()
	close(start)
	wg.Wait()
	ok, busy := 0, 0
	for _, rep := range replies {
		switch rep.status {
		case http.StatusOK:
			ok++
		case http.StatusServiceUnavailable:
			busy++
			if rep.code() != "too_many_logins" || rep.header.Get("Retry-After") != "1" || len(rep.setCookies()) != 0 {
				t.Errorf("busy answer %v %s", rep.header, rep.text)
			}
		default:
			t.Fatalf("unexpected %d: %s", rep.status, rep.text)
		}
	}
	if ok < 1 {
		t.Error("at least one login runs")
	}
	if busy < 1 {
		t.Error("the excess is turned away")
	}
	// The permit is free again afterwards.
	wantStatus(t, e.tryLogin("alice", testPassword), http.StatusOK)
}

// Rust: auth.rs::a_cookie_session_reads_and_needs_csrf_to_change_things
func TestACookieSessionReadsAndNeedsCSRFToChangeThings(t *testing.T) {
	e := envWithUsers(t)
	alice, bob := e.login("alice"), e.login("bob")
	body := J{"title": "t"}
	// GET needs no CSRF.
	wantStatus(t, e.send(get("/api/v1/resource/note").cookie(alice)), http.StatusOK)
	// POST without, with a wrong, and with another session's CSRF token.
	without := e.send(post("/api/v1/resource/note").cookie(alice).json(body))
	wrong := e.send(post("/api/v1/resource/note").cookie(alice).csrf(strings.Repeat("0", 64)).json(body))
	other := e.send(post("/api/v1/resource/note").cookie(alice).csrf(bob.csrf).json(body))
	garbage := e.send(post("/api/v1/resource/note").cookie(alice).csrf("not hex").json(body))
	// A repeated header is refused even when every value is the right one.
	repeated := e.send(post("/api/v1/resource/note").cookie(alice).csrf(alice.csrf).csrf(alice.csrf).json(body))
	for _, rep := range []reply{without, wrong, other, garbage, repeated} {
		wantCode(t, rep, http.StatusForbidden, "csrf_failed")
	}
	// Nothing was created.
	eqJSON(t, e.send(get("/api/v1/resource/note").cookie(alice)).data(), `[]`)
	// The right token works, and PATCH and DELETE need it too.
	created := e.send(post("/api/v1/resource/note").session(alice).json(body))
	wantStatus(t, created, http.StatusCreated)
	uri := "/api/v1/resource/note/" + str(created.data(), "name")
	patch := J{"version": 1, "data": J{"body": "x"}}
	wantCode(t, e.send(reqOf(http.MethodPatch, uri).cookie(alice).json(patch)), http.StatusForbidden, "csrf_failed")
	wantStatus(t, e.send(reqOf(http.MethodPatch, uri).session(alice).json(patch)), http.StatusOK)
	wantStatus(t, e.send(reqOf(http.MethodDelete, uri+"?version=2").cookie(alice)), http.StatusForbidden)
	rep := e.send(reqOf(http.MethodDelete, uri+"?version=2").session(alice))
	wantStatus(t, rep, http.StatusNoContent)
	if rep.text != "" {
		t.Errorf("body %q", rep.text)
	}
}

// Rust: auth.rs::logout_ends_the_session_and_clears_the_cookie
func TestLogoutEndsTheSessionAndClearsTheCookie(t *testing.T) {
	e := envWithUsers(t)
	alice := e.login("alice")
	// CSRF is required.
	wantStatus(t, e.send(post("/api/v1/logout").cookie(alice)), http.StatusForbidden)
	wantStatus(t, e.send(get("/api/v1/resource/note").cookie(alice)), http.StatusOK)
	rep := e.send(post("/api/v1/logout").session(alice))
	wantStatus(t, rep, http.StatusNoContent)
	cookies := rep.setCookies()
	if len(cookies) != 1 || cookies[0] != "fb_session=; HttpOnly; Secure; SameSite=Lax; Path=/; Max-Age=0" {
		t.Errorf("cookies %q", cookies)
	}
	wantStatus(t, e.send(get("/api/v1/resource/note").cookie(alice)), http.StatusUnauthorized)
	// A second logout with the dead session is a 401 like any request.
	wantStatus(t, e.send(post("/api/v1/logout").session(alice)), http.StatusUnauthorized)
}

// Rust: auth.rs::expired_unknown_and_tampered_cookies_are_the_same_401
func TestExpiredUnknownAndTamperedCookiesAreTheSame401(t *testing.T) {
	e := envWithUsers(t)
	alice := e.login("alice")
	tampered := alice.token[:63]
	if alice.token[63] == '0' {
		tampered += "1"
	} else {
		tampered += "0"
	}
	replies := []reply{
		e.send(get("/api/v1/resource/note")),
		e.send(get("/api/v1/resource/note").cookieValue(tampered)),
		e.send(get("/api/v1/resource/note").cookieValue("garbage")),
		e.send(get("/api/v1/resource/note").cookieValue("")),
	}
	e.db.Exec("UPDATE fb_session SET expires_at = now() - interval '1 second'")
	replies = append(replies, e.send(get("/api/v1/resource/note").cookie(alice)))
	for _, rep := range replies {
		wantStatus(t, rep, http.StatusUnauthorized)
		if rep.text != replies[0].text {
			t.Errorf("%q vs %q", rep.text, replies[0].text)
		}
	}
	if replies[0].code() != "unauthenticated" {
		t.Error(replies[0].text)
	}
	// Idle for more than 24 hours is expired too.
	bob := e.login("bob")
	e.db.Exec("UPDATE fb_session SET last_seen_at = now() - interval '25 hours'")
	wantStatus(t, e.send(get("/api/v1/resource/note").cookie(bob)), http.StatusUnauthorized)
}

// Rust: auth.rs::a_disabled_user_loses_the_session
func TestADisabledUserLosesTheSession(t *testing.T) {
	e := envWithUsers(t)
	alice := e.login("alice")
	e.db.Exec("UPDATE fb_user SET enabled = false WHERE username = 'alice'")
	wantStatus(t, e.send(get("/api/v1/resource/note").cookie(alice)), http.StatusUnauthorized)
}

// Rust: auth.rs::an_api_token_needs_no_csrf
func TestAnAPITokenNeedsNoCSRF(t *testing.T) {
	e := envWithUsers(t)
	token := e.apiToken("alice")
	created := e.send(post("/api/v1/resource/note").token(token).json(J{"title": "t"}))
	wantStatus(t, created, http.StatusCreated)
	if created.dataObj()["owner"] != "alice" {
		t.Error(created.text)
	}
	rep := e.send(get("/api/v1/resource/note").token(token))
	wantStatus(t, rep, http.StatusOK)
	if len(rep.dataList()) != 1 {
		t.Error(rep.text)
	}
	// The scheme is case-insensitive.
	wantStatus(t, e.send(get("/api/v1/resource/note").header("Authorization", "TOKEN "+token)), http.StatusOK)
	// Logout is for sessions.
	wantCode(t, e.send(post("/api/v1/logout").token(token)), http.StatusBadRequest, "bad_request")
}

// Rust: auth.rs::bad_tokens_are_the_same_401
func TestBadTokensAreTheSame401(t *testing.T) {
	e := envWithUsers(t)
	token := e.apiToken("alice")
	key, secret, _ := strings.Cut(token, ":")
	revoked := e.apiToken("alice")
	revokedKey, _, _ := strings.Cut(revoked, ":")
	if err := db.RevokeAPIToken(context.Background(), e.db.Pool, revokedKey); err != nil {
		t.Fatal(err)
	}
	headers := []string{
		"token " + key + ":" + strings.Repeat("0", 64),
		"token " + revoked,
		"token " + key,
		"token :" + secret,
		"token",
		"token ",
		"Bearer " + token,
		"Basic " + token,
		token,
		"",
	}
	var texts []string
	for _, header := range headers {
		rep := e.send(get("/api/v1/resource/note").header("Authorization", header))
		wantStatus(t, rep, http.StatusUnauthorized, header)
		texts = append(texts, rep.text)
	}
	for _, text := range texts {
		if text != texts[0] {
			t.Errorf("%q vs %q", text, texts[0])
		}
	}
	// A header that is not valid text.
	wantStatus(t, e.send(get("/api/v1/resource/note").header("Authorization", "token é")), http.StatusUnauthorized)
}

// Rust: auth.rs::the_authorization_header_wins_over_the_cookie
func TestTheAuthorizationHeaderWinsOverTheCookie(t *testing.T) {
	e := envWithUsers(t)
	alice := e.login("alice")
	bobToken := e.apiToken("bob")
	// A bad header is a 401 even with a good session, with CSRF too.
	rep := e.send(post("/api/v1/resource/note").session(alice).header("Authorization", "token nonsense").json(J{"title": "t"}))
	wantStatus(t, rep, http.StatusUnauthorized)
	// A good token with a bad cookie works and acts as the token's user, without CSRF.
	rep = e.send(post("/api/v1/resource/note").cookieValue("garbage").token(bobToken).json(J{"title": "t"}))
	wantStatus(t, rep, http.StatusCreated)
	// With both, the token's user is the caller, not the cookie's.
	rep = e.send(post("/api/v1/resource/note").cookie(alice).token(bobToken).json(J{"title": "t2"}))
	wantStatus(t, rep, http.StatusCreated)
	if rep.dataObj()["owner"] != "bob" {
		t.Error(rep.text)
	}
}

// Rust: auth.rs::the_host_is_checked_first_on_every_route
func TestTheHostIsCheckedFirstOnEveryRoute(t *testing.T) {
	e := envWithUsers(t)
	alice := e.login("alice")
	loginBody := J{"username": "alice", "password": testPassword}
	hosts := []*string{nil, ptr("evil.example"), ptr("localhost.evil.example"), ptr("localhost:80@evil.example"), ptr(""), ptr("127.0.0.2")}
	for _, host := range hosts {
		requests := []*req{
			post("/api/v1/login").json(loginBody),
			get("/api/v1/resource/note").session(alice),
			post("/api/v1/logout").session(alice),
			get("/api/v1/nothing"),
			reqOf(http.MethodPut, "/api/v1/login"),
			// Not even a bad body or a bad credential gets past: the Host comes first.
			post("/api/v1/login").raw("text/plain", []byte("{")),
		}
		for _, r := range requests {
			if host == nil {
				r.noHost()
			} else {
				r.onHost(*host)
			}
			rep := e.send(r)
			wantCode(t, rep, http.StatusBadRequest, "invalid_host", host)
			if len(rep.setCookies()) != 0 {
				t.Error("cookie on a refused host")
			}
		}
	}
	// The session was not touched by the refused requests.
	wantStatus(t, e.send(get("/api/v1/resource/note").cookie(alice)), http.StatusOK)
	// Port and case are ignored; the other configured host works.
	for _, host := range []string{"LOCALHOST", "localhost:8000", "127.0.0.1", "127.0.0.1:3000"} {
		wantStatus(t, e.send(get("/api/v1/resource/note").cookie(alice).onHost(host)), http.StatusOK, host)
	}
}

func ptr[T any](v T) *T { return &v }

// Rust: auth.rs::a_repeated_host_header_is_refused
func TestARepeatedHostHeaderIsRefused(t *testing.T) {
	e := envWithUsers(t)
	// The test client sets one Host and the request adds a second.
	rep := e.send(get("/api/v1/nothing").header("Host", "localhost"))
	wantCode(t, rep, http.StatusBadRequest, "invalid_host")
}

// Rust: auth.rs::configured_hosts_are_used_and_ipv6_works
func TestConfiguredHostsAreUsedAndIPv6Works(t *testing.T) {
	config := DefaultConfig()
	config.AllowedHosts = []string{"Erp.Example.com", "[::1]"}
	e := newEnvWith(t, config)
	for host, status := range map[string]int{
		"erp.example.com":     http.StatusNotFound,
		"ERP.EXAMPLE.COM:443": http.StatusNotFound,
		"[::1]:8000":          http.StatusNotFound,
		"localhost":           http.StatusBadRequest,
	} {
		wantStatus(t, e.send(get("/api/v1/nothing").onHost(host)), status, host)
	}
}

// Rust: auth.rs::tests::token_header_parsing
func TestTokenHeaderParsing(t *testing.T) {
	key, secret, ok := tokenCredentials(http.Header{"Authorization": {"Token abc:def"}})
	if !ok || key != "abc" || secret != "def" {
		t.Errorf("%q %q %v", key, secret, ok)
	}
	for _, bad := range []string{"Bearer abc:def", "token abc", "token", "abc:def"} {
		if _, _, ok := tokenCredentials(http.Header{"Authorization": {bad}}); ok {
			t.Errorf("%q", bad)
		}
	}
	if _, _, ok := tokenCredentials(http.Header{"Authorization": {"token a:b", "token a:b"}}); ok {
		t.Error("a repeated header")
	}
}

// Rust: auth.rs::tests::cookie_parsing
func TestCookieParsing(t *testing.T) {
	for _, c := range []struct {
		lines []string
		want  string
		ok    bool
	}{
		{[]string{"a=1; fb_session=tok; b=2"}, "tok", true},
		{[]string{"xfb_session=tok"}, "", false},
		{[]string{"a=1", "fb_session=t2"}, "t2", true},
	} {
		got, ok := sessionToken(http.Header{"Cookie": c.lines})
		if got != c.want || ok != c.ok {
			t.Errorf("%v: %q %v", c.lines, got, ok)
		}
	}
}
