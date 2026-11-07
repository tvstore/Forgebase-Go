package httpapi

import (
	"net/http"
	"strings"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// Authentication and CSRF for the routes that need a user (see the package documentation; the Rust
// module auth.rs).

// sessionCookie is the name of the session cookie.
const sessionCookie = "fb_session"

// csrfHeader is the name of the CSRF header.
const csrfHeader = "X-Forgebase-CSRF"

// authenticated is a request that proved who it is.
type authenticated struct {
	user db.AuthUser
	// session is the session token when the request authenticated with the cookie, "" for an API
	// token.
	session string
}

// caller is the caller for the document functions. Always a user, never the system context.
func (a *authenticated) caller() core.Caller { return a.user.Caller() }

// authenticate does the whole check, so a handler that has the result needs nothing else:
// authentication first, then CSRF.
func (s *State) authenticate(r *request) (*authenticated, error) {
	// An Authorization header decides alone: a bad one is a 401, never a fallback to the cookie.
	if _, present := r.Header["Authorization"]; present {
		key, secret, ok := tokenCredentials(r.Header)
		if !ok {
			return nil, errUnauthenticated()
		}
		user, err := db.ValidateAPIToken(r.Context(), s.pool, key, secret)
		if err != nil {
			return nil, err
		}
		if user == nil {
			return nil, errUnauthenticated()
		}
		return &authenticated{user: *user}, nil
	}
	token, ok := sessionToken(r.Header)
	if !ok {
		return nil, errUnauthenticated()
	}
	user, err := db.ValidateSession(r.Context(), s.pool, token)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errUnauthenticated()
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// Exactly one value, like the Authorization header.
		values := r.Header.Values(csrfHeader)
		if len(values) != 1 || !visibleASCII(values[0]) {
			return nil, errCSRF()
		}
		valid, err := db.CheckCSRF(r.Context(), s.pool, token, values[0])
		if err != nil {
			return nil, err
		}
		if !valid {
			return nil, errCSRF()
		}
	}
	return &authenticated{user: *user, session: token}, nil
}

// tokenCredentials are the key and the secret of exactly one "Authorization: token <key>:<secret>"
// header.
func tokenCredentials(h http.Header) (key, secret string, ok bool) {
	values := h.Values("Authorization")
	if len(values) != 1 || !visibleASCII(values[0]) {
		return "", "", false
	}
	scheme, rest, found := strings.Cut(values[0], " ")
	if !found || !strings.EqualFold(scheme, "token") {
		return "", "", false
	}
	return strings.Cut(strings.TrimSpace(rest), ":")
}

// sessionToken is the value of the first fb_session cookie.
func sessionToken(h http.Header) (string, bool) {
	for _, line := range h.Values("Cookie") {
		if !visibleASCII(line) {
			continue
		}
		for _, pair := range strings.Split(line, ";") {
			name, value, found := strings.Cut(strings.TrimSpace(pair), "=")
			if found && name == sessionCookie {
				return value, true
			}
		}
	}
	return "", false
}
