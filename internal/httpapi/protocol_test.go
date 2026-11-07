package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// Ported from crates/forgebase-http/tests/protocol.rs: the error envelope for every kind of failure,
// body limits, no CORS, no secrets or internals in responses, against a real PostgreSQL.

func envWithWriter(t *testing.T) (*env, session) {
	t.Helper()
	e := newEnv(t)
	e.user("writer", "Writer")
	return e, e.login("writer")
}

// Rust: protocol.rs::every_kind_of_failure_uses_the_envelope
func TestEveryKindOfFailureUsesTheEnvelope(t *testing.T) {
	e, s := envWithWriter(t)
	big := []byte("aaaaaaaaaa")
	// Unknown routes, wrong methods.
	for _, uri := range []string{"/", "/api", "/api/v1", "/api/v1/method", "/api/v1/resource"} {
		assertEnvelope(t, e.send(get(uri).session(s)), http.StatusNotFound, "not_found")
	}
	assertEnvelope(t, e.send(reqOf(http.MethodPut, "/api/v1/resource/note").session(s)), http.StatusMethodNotAllowed, "method_not_allowed")
	// The route of the app methods (P7) is not there yet: an unknown path, 404, even without a credential.
	assertEnvelope(t, e.send(get("/api/v1/method/frappe.ping").session(s)), http.StatusNotFound, "not_found")
	assertEnvelope(t, e.send(post("/api/v1/method/frappe.ping").json(map[string]any{})), http.StatusNotFound, "not_found")
	assertEnvelope(t, e.send(get("/api/v1/login")), http.StatusMethodNotAllowed, "method_not_allowed")
	assertEnvelope(t, e.send(reqOf(http.MethodOptions, "/api/v1/resource/note")), http.StatusMethodNotAllowed, "method_not_allowed")
	// Malformed JSON, another content type, no content type, a path that is not UTF-8.
	assertEnvelope(t, e.send(post("/api/v1/resource/note").session(s).raw("application/json", []byte("{"))), http.StatusBadRequest, "bad_request")
	assertEnvelope(t, e.send(post("/api/v1/resource/note").session(s).raw("application/json", []byte(""))), http.StatusBadRequest, "bad_request")
	assertEnvelope(t, e.send(post("/api/v1/resource/note").session(s).raw("text/plain", big)), http.StatusUnsupportedMediaType, "unsupported_media_type")
	assertEnvelope(t, e.send(post("/api/v1/resource/note").session(s)), http.StatusUnsupportedMediaType, "unsupported_media_type")
	assertEnvelope(t, e.send(post("/api/v1/login").raw("application/json", []byte("["))), http.StatusBadRequest, "bad_request")
	assertEnvelope(t, e.send(get("/api/v1/resource/%FF").session(s)), http.StatusBadRequest, "bad_request")
	assertEnvelope(t, e.send(get("/api/v1/resource/note?limit=%FF").session(s)), http.StatusBadRequest, "invalid_query")
	// Authentication comes before the body: no credential and a bad body is a 401.
	assertEnvelope(t, e.send(post("/api/v1/resource/note").raw("application/json", []byte("{"))), http.StatusUnauthorized, "unauthenticated")
	assertEnvelope(t, e.send(post("/api/v1/resource/note").raw("text/plain", big)), http.StatusUnauthorized, "unauthenticated")
}

// Rust: protocol.rs::the_body_limit_is_413_in_the_envelope
func TestTheBodyLimitIs413InTheEnvelope(t *testing.T) {
	config := DefaultConfig()
	config.BodyLimitBytes = 256
	e := newEnvWith(t, config)
	e.user("writer", "Writer")
	s := e.login("writer")
	rep := e.send(post("/api/v1/resource/note").session(s).json(J{"title": "ok"}))
	wantStatus(t, rep, http.StatusCreated)
	large := J{"title": strings.Repeat("x", 1000)}
	assertEnvelope(t, e.send(post("/api/v1/resource/note").session(s).json(large)), http.StatusRequestEntityTooLarge, "payload_too_large")
	// On login too, and for PATCH.
	assertEnvelope(t, e.send(post("/api/v1/login").json(J{"username": "writer", "password": strings.Repeat("x", 1000)})),
		http.StatusRequestEntityTooLarge, "payload_too_large")
	assertEnvelope(t, e.send(reqOf(http.MethodPatch, "/api/v1/resource/note/x").session(s).json(large)),
		http.StatusRequestEntityTooLarge, "payload_too_large")
}

// Rust: protocol.rs::no_cors_headers_anywhere
func TestNoCORSHeadersAnywhere(t *testing.T) {
	e, s := envWithWriter(t)
	token := e.apiToken("writer")
	const origin = "https://evil.example"
	replies := []reply{
		e.send(get("/api/v1/resource/note").session(s).header("Origin", origin)),
		e.send(get("/api/v1/resource/note").header("Origin", origin)),
		e.send(get("/api/v1/resource/nope").token(token).header("Origin", origin)),
		e.send(post("/api/v1/login").header("Origin", origin).json(J{"username": "writer", "password": testPassword})),
		e.send(post("/api/v1/login").header("Origin", origin).json(J{"username": "writer", "password": "wrong password"})),
		e.send(reqOf(http.MethodOptions, "/api/v1/resource/note").header("Origin", origin).
			header("Access-Control-Request-Method", "POST").header("Access-Control-Request-Headers", "x-forgebase-csrf")),
		e.send(get("/api/v1/nothing").header("Origin", origin)),
		e.send(get("/api/v1/nothing").onHost("evil.example").header("Origin", origin)),
	}
	e.mu.Lock()
	all := append(append([]reply{}, e.log...), replies...)
	e.mu.Unlock()
	for _, rep := range all {
		for name := range rep.header {
			if strings.HasPrefix(strings.ToLower(name), "access-control-") {
				t.Errorf("%s in %s", name, rep.text)
			}
		}
		if strings.Contains(strings.ToLower(rep.header.Get("Vary")), "origin") {
			t.Errorf("Vary: %s", rep.header.Get("Vary"))
		}
	}
	// Every response is also marked uncacheable and not sniffable.
	for _, rep := range replies {
		if rep.header.Get("Cache-Control") != "no-store" || rep.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("headers %v", rep.header)
		}
	}
}

// Rust: protocol.rs::no_secret_appears_in_a_response_except_where_it_is_issued
func TestNoSecretAppearsInAResponseExceptWhereItIsIssued(t *testing.T) {
	e := newEnv(t)
	e.user("writer", "Writer")
	e.user("admin", "Administrator")
	login := e.tryLogin("writer", testPassword)
	sessionToken, _, _ := strings.Cut(strings.TrimPrefix(login.setCookies()[0], "fb_session="), ";")
	csrf := str(login.data(), "csrf_token")
	s := session{token: sessionToken, csrf: csrf}
	api := e.apiToken("writer")
	apiKey, apiSecret, _ := strings.Cut(api, ":")

	// A mixed bag of successful and failing requests.
	created := e.send(post("/api/v1/resource/note").session(s).json(J{"title": "t"}))
	name := str(created.data(), "name")
	e.send(get("/api/v1/resource/note").session(s))
	e.send(get("/api/v1/resource/note/" + name).token(api))
	e.send(post("/api/v1/resource/note").token(api).json(J{"nope": 1}))
	e.send(post("/api/v1/resource/note").cookie(s).json(J{"title": "t"}))
	e.send(get("/api/v1/resource/note").token(apiKey + ":" + strings.Repeat("0", 64)))
	e.send(post("/api/v1/resource/secret").session(s).json(J{"x": "y"}))
	e.send(post("/api/v1/login").json(J{"username": "writer", "password": "wrong password"}))
	e.send(post("/api/v1/logout").session(s))
	e.send(get("/api/v1/resource/note").session(s))

	e.mu.Lock()
	defer e.mu.Unlock()
	csrfCount := 0
	for _, rep := range e.log {
		for _, secret := range []string{testPassword, "wrong password", apiSecret, "postgres://"} {
			if strings.Contains(rep.text, secret) {
				t.Errorf("%s in %s", secret, rep.text)
			}
			for name, values := range rep.header {
				if strings.Contains(strings.Join(values, ","), secret) {
					t.Errorf("%s in header %s", secret, name)
				}
			}
		}
		// The session token only in Set-Cookie; the CSRF token only in the login body.
		if strings.Contains(rep.text, sessionToken) {
			t.Errorf("session token in %s", rep.text)
		}
		for name, values := range rep.header {
			if name != "Set-Cookie" && strings.Contains(strings.Join(values, ","), sessionToken) {
				t.Errorf("session token in header %s", name)
			}
		}
		if strings.Contains(rep.text, csrf) {
			csrfCount++
			if str(rep.data(), "csrf_token") != csrf {
				t.Errorf("csrf elsewhere: %s", rep.text)
			}
		}
		// The API key is not a secret but nothing needs to echo it either.
		if strings.Contains(rep.text, apiKey) {
			t.Errorf("api key in %s", rep.text)
		}
	}
	if csrfCount != 1 {
		t.Errorf("the CSRF token appears in %d responses", csrfCount)
	}
}

// Rust: protocol.rs::internal_errors_are_generic
func TestInternalErrorsAreGeneric(t *testing.T) {
	e, s := envWithWriter(t)
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
	// Break the storage of a DocType behind the API's back.
	e.db.Exec("ALTER TABLE doc_note RENAME TO doc_note_gone")
	rep := e.send(get("/api/v1/resource/note").session(s))
	assertEnvelope(t, rep, http.StatusInternalServerError, "internal")
	if got := str(rep.errorObj(), "message"); got != "internal server error" {
		t.Errorf("message %q", got)
	}
	for _, leak := range []string{"doc_note", "relation", "SQLSTATE", "postgres"} {
		if strings.Contains(rep.text, leak) {
			t.Errorf("%s in %s", leak, rep.text)
		}
	}
	rep = e.send(post("/api/v1/resource/note").session(s).json(J{"title": "t"}))
	assertEnvelope(t, rep, http.StatusInternalServerError, "internal")
	if strings.Contains(rep.text, "doc_note") {
		t.Errorf("leak: %s", rep.text)
	}
	// The real error goes to the log (standard error).
	logMu.Lock()
	text := logged.String()
	logMu.Unlock()
	if !strings.Contains(text, "internal error:") || !strings.Contains(text, "doc_note") {
		t.Errorf("log %q", text)
	}
	e.db.Exec("ALTER TABLE doc_note_gone RENAME TO doc_note")
}

// Rust: protocol.rs::a_unique_violation_does_not_echo_the_key
func TestAUniqueViolationDoesNotEchoTheKey(t *testing.T) {
	e := newEnv(t)
	e.user("clerk", "Clerk")
	clerk := e.apiToken("clerk")
	body := J{"code": "dup-key-1"}
	first := e.send(post("/api/v1/resource/item").token(clerk).json(body))
	wantStatus(t, first, http.StatusCreated)
	second := e.send(post("/api/v1/resource/item").token(clerk).json(body))
	assertEnvelope(t, second, http.StatusConflict, "unique_violation")
	if strings.Contains(second.text, "doc_item") || strings.Contains(second.text, "dup-key-1") {
		t.Errorf("leak: %s", second.text)
	}
}
