package dbtest

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TLS is the PostgreSQL with TLS of the TLS tests (ADR 0018): start it with
// `.github/ci/pg-tls.sh up DIR` (see CONTRIBUTING.md); the script prints the variables below. A
// throwaway CA signs the server certificate (SAN DNS:localhost and IP:127.0.0.1), a second, unrelated
// CA signs nothing.
type TLS struct {
	// URL is postgres://forgebase:forgebase@localhost:5433/postgres, without a query.
	URL string
	// CA and WrongCA are the paths of the PEM files of the two CAs.
	CA, WrongCA string
}

// TLSEnv reads FORGEBASE_TEST_TLS_URL, FORGEBASE_TEST_TLS_CA and FORGEBASE_TEST_TLS_WRONG_CA. When one is
// missing the test is skipped with a notice, except when CI=true: then it fails, so CI never skips the
// TLS tests without saying so.
func TLSEnv(t testing.TB) TLS {
	t.Helper()
	get := func(name string) string { return os.Getenv(name) }
	tls := TLS{URL: get("FORGEBASE_TEST_TLS_URL"), CA: get("FORGEBASE_TEST_TLS_CA"), WrongCA: get("FORGEBASE_TEST_TLS_WRONG_CA")}
	if tls.URL != "" && tls.CA != "" && tls.WrongCA != "" {
		return tls
	}
	if os.Getenv("CI") == "true" {
		t.Fatal("CI=true but FORGEBASE_TEST_TLS_URL, FORGEBASE_TEST_TLS_CA and FORGEBASE_TEST_TLS_WRONG_CA " +
			"are not all set: run .github/ci/pg-tls.sh up DIR first. These tests are never skipped in CI.")
	}
	t.Skip("SKIPPED: FORGEBASE_TEST_TLS_URL, FORGEBASE_TEST_TLS_CA and FORGEBASE_TEST_TLS_WRONG_CA are not set " +
		"(.github/ci/pg-tls.sh up DIR)")
	return TLS{}
}

// Encode percent-encodes a path for a URL query value (what the Rust tests do for sslrootcert).
func Encode(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '/', c == '.', c == '-', c == '_':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// With is the server URL with these query parameters.
func (s TLS) With(query string) string { return s.URL + "?" + query }

// Verified is the URL with sslmode=mode and the right CA as the root.
func (s TLS) Verified(mode string) string {
	return s.With("sslmode=" + mode + "&sslrootcert=" + Encode(s.CA))
}

// WithWrongCA is the URL with sslmode=require and the wrong CA as the root.
func (s TLS) WithWrongCA() string {
	return s.With("sslmode=require&sslrootcert=" + Encode(s.WrongCA))
}

// ForDatabase is the URL of another database on the TLS server, without a query.
func (s TLS) ForDatabase(name string) string { return withDatabase(s.URL, name) }
