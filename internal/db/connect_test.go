package db

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func refusedTLS(t *testing.T, url string) string {
	t.Helper()
	_, err := checkTLSSettings(url)
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrTLS {
		t.Fatalf("%q: expected an ErrTLS refusal, got %v", url, err)
	}
	return e.Message
}

// Rust: tls.rs::no_sslmode_and_disable_mean_no_tls (the part that this phase has)
func TestNoSslmodeAndDisableMeanNoTLS(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"postgres://u:p@h:5432/db", "postgres://u:p@h:5432/db"},
		{"postgresql://u:p@h/db", "postgresql://u:p@h/db"},
		{"postgres://u:p@h/db?sslmode=disable", "postgres://u:p@h/db"},
		{"POSTGRES://u:p@h/db?sslmode=disable", "POSTGRES://u:p@h/db"},
		{"postgres://u:p@h/db?sslmode=%64isable", "postgres://u:p@h/db"},
	} {
		got, err := checkTLSSettings(c.in)
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

// Rust: tls.rs::other_query_parameters_are_kept_in_order
func TestOtherQueryParametersAreKeptInOrder(t *testing.T) {
	got, err := checkTLSSettings("postgres://u:p@h/db?application_name=x&sslmode=disable&connect_timeout=5&sslnegotiation=direct")
	if err != nil {
		t.Fatal(err)
	}
	if want := "postgres://u:p@h/db?application_name=x&connect_timeout=5&sslnegotiation=direct"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Rust: tls.rs::require_and_the_verify_modes_mean_full_verification (here: refused until P5)
func TestTLSModesAreRefusedUntilP5(t *testing.T) {
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		msg := refusedTLS(t, "postgres://u:p@h/db?sslmode="+mode)
		if !strings.Contains(msg, "P5") {
			t.Errorf("%s: %q does not say that TLS arrives with P5", mode, msg)
		}
	}
	if msg := refusedTLS(t, "postgres://u:p@h/db?sslmode=require&sslrootcert=/ca.pem"); !strings.Contains(msg, "P5") {
		t.Errorf("%q", msg)
	}
}

// Rust: tls.rs::prefer_allow_and_unknown_modes_are_refused
func TestPreferAllowAndUnknownModesAreRefused(t *testing.T) {
	for _, mode := range []string{"prefer", "allow"} {
		if msg := refusedTLS(t, "postgres://u:p@h/db?sslmode="+mode); !strings.Contains(msg, "not supported") {
			t.Errorf("%s: %q", mode, msg)
		}
	}
	if msg := refusedTLS(t, "postgres://u:p@h/db?sslmode=bogus"); !strings.Contains(msg, "is not supported") {
		t.Errorf("%q", msg)
	}
}

// Rust: tls.rs::client_certificates_and_other_ssl_options_are_refused
func TestClientCertificatesAndOtherSSLOptionsAreRefused(t *testing.T) {
	for _, q := range []string{"sslcert=a", "sslkey=b", "sslpassword=c", "requiressl=1", "%73slcert=a"} {
		if msg := refusedTLS(t, "postgres://u:p@h/db?"+q); !strings.Contains(msg, "TLS option") {
			t.Errorf("%s: %q", q, msg)
		}
	}
	// Another spelling is not a Rust-style TLS option, but the standard library's view of the URL
	// sees one, and the URL is refused rather than risk a TLS choice that goes unseen.
	refusedTLS(t, "postgres://u:p@h/db?SSLCERT=a")
	if msg := refusedTLS(t, "postgres://u:p@h/db?sslrootcert=/ca.pem"); !strings.Contains(msg, "TLS is off") {
		t.Errorf("%q", msg)
	}
}

// Rust: tls.rs::repeated_parameters_and_other_url_forms_are_refused
func TestRepeatedParametersAndOtherURLFormsAreRefused(t *testing.T) {
	refusedTLS(t, "postgres://u:p@h/db?sslmode=disable&sslmode=disable")
	refusedTLS(t, "host=h user=u dbname=d")
	// Not a PostgreSQL URL at all: an invalid URL, not a TLS refusal (Rust: CreatePool).
	_, err := checkTLSSettings("mysql://u:p@h/db")
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrCreatePool {
		t.Errorf("got %v", err)
	}
}

// Rust: tls.rs::special_characters_in_the_credentials_must_be_percent_encoded
func TestSpecialCharactersInTheCredentialsMustBePercentEncoded(t *testing.T) {
	// A raw '/', '?' or '#' before the first '@' could hide the query from this scan.
	for _, url := range []string{
		"postgres://u:pa/ss@h/db?sslmode=disable",
		"postgres://u:pa?ss@h/db",
		"postgres://u:pa#ss@h/db",
	} {
		if msg := refusedTLS(t, url); !strings.Contains(msg, "before its first `@`") {
			t.Errorf("%q: %q", url, msg)
		}
	}
	// Encoded, they are fine.
	if _, err := checkTLSSettings("postgres://u:pa%2Fss%3F%23@h/db?sslmode=disable"); err != nil {
		t.Error(err)
	}
	// An '@' in a query value would make another parser read different credentials.
	refusedTLS(t, "postgres://h/db?x=a@b&sslmode=require")
}

// Rust: tls.rs::encoded_keys_are_decoded_before_they_are_matched
func TestEncodedKeysAreDecodedBeforeTheyAreMatched(t *testing.T) {
	refusedTLS(t, "postgres://u:p@h/db?%73slmode=require")
	if got, err := checkTLSSettings("postgres://u:p@h/db?%73slmode=disable"); err != nil || got != "postgres://u:p@h/db" {
		t.Errorf("%q %v", got, err)
	}
}

// Rust: tls.rs::percent_encoding_needs_two_hex_digits
func TestPercentEncodingNeedsTwoHexDigits(t *testing.T) {
	for _, bad := range []string{"%", "%4", "%zz", "%+1", "%ff"} {
		if _, ok := percentDecode(bad); ok {
			t.Errorf("%q decoded", bad)
		}
	}
	if got, ok := percentDecode("a%20b+c"); !ok || got != "a b+c" {
		t.Errorf("%q %v", got, ok)
	}
	refusedTLS(t, "postgres://u:p@h/db?sslmode=%zz")
}

// Rust: tls.rs::errors_do_not_contain_the_url_or_the_password
func TestErrorsDoNotContainTheURLOrThePassword(t *testing.T) {
	for _, url := range []string{
		"postgres://u:s3cret@h/db?sslmode=require",
		"postgres://u:s3cret@h/db?sslmode=prefer",
		"postgres://u:s3cret@h/db?sslcert=x",
		"postgres://u:s3cret/@h/db",
		"host=h password=s3cret",
	} {
		if msg := refusedTLS(t, url); strings.Contains(msg, "s3cret") || strings.Contains(msg, "u:") {
			t.Errorf("%q leaks the URL: %q", url, msg)
		}
	}
}

// Not in Rust: the cross-check against the standard library's view of the URL.
func TestSameTLSView(t *testing.T) {
	if !sameTLSView("postgres://u:p@h/db?sslmode=disable") || !sameTLSView("postgres://u:p@h/db") {
		t.Error("a plain URL is refused")
	}
	if sameTLSView("postgres://u:p@h/db?SSLMODE=require") || sameTLSView("postgres://u:p@h/db?sslcert=x") {
		t.Error("a TLS option went unseen")
	}
}

// Not in Rust: with no TLS the pool must never try TLS, whatever the configuration asked for.
func TestForceNoTLS(t *testing.T) {
	// sslmode=prefer is what pgx would use for a URL without sslmode; it is set here so that the
	// assertion holds whatever the environment says.
	config, err := pgxpool.ParseConfig("postgres://u:p@h1,h2/db?sslmode=prefer")
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.TLSConfig == nil {
		t.Fatal("the test configuration does not ask for TLS: it proves nothing")
	}
	forceNoTLS(config)
	if config.ConnConfig.TLSConfig != nil {
		t.Error("TLS on the first host")
	}
	for _, fb := range config.ConnConfig.Fallbacks {
		if fb.TLSConfig != nil {
			t.Errorf("TLS on the fallback %s", fb.Host)
		}
	}
	if n := len(config.ConnConfig.Fallbacks); n != 1 {
		t.Errorf("%d fallbacks, want one (the second host): the plain duplicate of the first is dropped", n)
	}
}
