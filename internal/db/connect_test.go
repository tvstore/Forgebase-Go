package db

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func refusedTLS(t *testing.T, url string) string {
	t.Helper()
	_, _, err := checkTLSSettings(url)
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrTLS {
		t.Fatalf("%q: expected an ErrTLS refusal, got %v", url, err)
	}
	return e.Message
}

// Rust: tls.rs::no_sslmode_and_disable_mean_no_tls
func TestNoSslmodeAndDisableMeanNoTLS(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"postgres://u:p@h:5432/db", "postgres://u:p@h:5432/db"},
		{"postgresql://u:p@h/db", "postgresql://u:p@h/db"},
		{"postgres://u:p@h/db?sslmode=disable", "postgres://u:p@h/db"},
		{"POSTGRES://u:p@h/db?sslmode=disable", "POSTGRES://u:p@h/db"},
		{"postgres://u:p@h/db?sslmode=%64isable", "postgres://u:p@h/db"},
	} {
		got, ssl, err := checkTLSSettings(c.in)
		if err != nil || got != c.want || ssl.verify {
			t.Errorf("%q: got %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

// Rust: tls.rs::other_query_parameters_are_kept_in_order
func TestOtherQueryParametersAreKeptInOrder(t *testing.T) {
	got, _, err := checkTLSSettings("postgres://u:p@h/db?application_name=x&sslmode=disable&connect_timeout=5&sslnegotiation=direct")
	if err != nil {
		t.Fatal(err)
	}
	if want := "postgres://u:p@h/db?application_name=x&connect_timeout=5&sslnegotiation=direct"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Rust: tls.rs::require_and_the_verify_modes_mean_full_verification
func TestRequireAndTheVerifyModesMeanFullVerification(t *testing.T) {
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		rest, ssl, err := checkTLSSettings("postgres://u@h/db?sslmode=" + mode)
		if err != nil || rest != "postgres://u@h/db" || ssl != (tlsSetting{verify: true}) {
			t.Errorf("%s: %q %+v %v", mode, rest, ssl, err)
		}
	}
}

// Rust: tls.rs::sslrootcert_is_a_path_or_system
func TestSslrootcertIsAPathOrSystem(t *testing.T) {
	_, ssl, err := checkTLSSettings("postgres://u@h/db?sslmode=require&sslrootcert=/etc/ca%20x.pem")
	if err != nil || ssl != (tlsSetting{verify: true, rootFile: "/etc/ca x.pem"}) {
		t.Errorf("%+v %v", ssl, err)
	}
	_, ssl, err = checkTLSSettings("postgres://u@h/db?sslmode=require&sslrootcert=system")
	if err != nil || ssl != (tlsSetting{verify: true}) {
		t.Errorf("%+v %v", ssl, err)
	}
	if msg := refusedTLS(t, "postgres://u@h/db?sslrootcert=/x.pem"); !strings.Contains(msg, "TLS is off") {
		t.Errorf("%q", msg)
	}
	if msg := refusedTLS(t, "postgres://u@h/db?sslmode=require&sslrootcert="); !strings.Contains(msg, "empty") {
		t.Errorf("%q", msg)
	}
}

// Rust: tls.rs::other_query_parameters_are_kept_in_order (the TLS part)
func TestTLSParametersAreTakenOutAndTheRestKept(t *testing.T) {
	rest, ssl, err := checkTLSSettings("postgres://u@h/db?connect_timeout=5&sslmode=require&sslrootcert=system&application_name=x")
	if err != nil || rest != "postgres://u@h/db?connect_timeout=5&application_name=x" || !ssl.verify {
		t.Errorf("%q %+v %v", rest, ssl, err)
	}
	rest, _, err = checkTLSSettings("postgres://u@h/db?sslnegotiation=direct&sslmode=require")
	if err != nil || rest != "postgres://u@h/db?sslnegotiation=direct" {
		t.Errorf("%q %v", rest, err)
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
	if msg := refusedTLS(t, "postgres://u@h/db?sslmode=require&sslmode=disable"); !strings.Contains(msg, "more than once") {
		t.Errorf("%q", msg)
	}
	refusedTLS(t, "host=h user=u dbname=d")
	// Not a PostgreSQL URL at all: an invalid URL, not a TLS refusal (Rust: CreatePool).
	_, _, err := checkTLSSettings("mysql://u:p@h/db")
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
	if _, _, err := checkTLSSettings("postgres://u:pa%2Fss%3F%23@h/db?sslmode=disable"); err != nil {
		t.Error(err)
	}
	// An '@' in a query value would make another parser read different credentials.
	refusedTLS(t, "postgres://h/db?x=a@b&sslmode=require")
	// Rust: with TLS asked for, the encoded forms keep their TLS choice and stay in the password.
	for _, c := range []struct{ url, rest string }{
		{"postgres://u:a%3Fb@h/db?sslmode=require", "postgres://u:a%3Fb@h/db"},
		{"postgres://u:a%23b@h/db?sslmode=require", "postgres://u:a%23b@h/db"},
		{"postgres://u:a%2Fb@h/db?sslmode=require", "postgres://u:a%2Fb@h/db"},
		{"postgresql://u:x%3Fsslmode=disable@h/db?sslmode=require", "postgresql://u:x%3Fsslmode=disable@h/db"},
		{"postgres://u:a%3Fb@h/db?connect_timeout=5&sslmode=require", "postgres://u:a%3Fb@h/db?connect_timeout=5"},
	} {
		rest, ssl, err := checkTLSSettings(c.url)
		if err != nil || rest != c.rest || !ssl.verify {
			t.Errorf("%q: %q %+v %v", c.url, rest, ssl, err)
		}
	}
	for _, url := range []string{
		"postgres://u:a?b@h/db?sslmode=require",
		"postgres://u:x?sslmode=disable@h/db?sslmode=require",
		"postgres://u:a?b@h/db",
	} {
		if msg := refusedTLS(t, url); !strings.Contains(msg, "before its first `@`") || strings.Contains(msg, "a?b") {
			t.Errorf("%q: %q", url, msg)
		}
		if _, err := parsePoolConfig(url); err == nil {
			t.Errorf("%q was accepted", url)
		}
	}
}

// Rust: tls.rs::an_at_sign_after_the_host_is_refused
func TestAnAtSignAfterTheHostIsRefused(t *testing.T) {
	for _, url := range []string{
		"postgres://db.example.com/app?sslmode=verify-full&application_name=svc@db.example.com",
		"postgres://db.example.com?sslmode=verify-full&application_name=svc@db.example.com",
		"postgres://h/db?sslmode=require&x=a@b",
	} {
		if msg := refusedTLS(t, url); !strings.Contains(msg, "`@` in a query value as `%40`") {
			t.Errorf("%q: %q", url, msg)
		}
		if _, err := parsePoolConfig(url); err == nil {
			t.Errorf("%q was accepted", url)
		}
	}
	for _, url := range []string{
		"postgres://db.example.com/app?sslmode=verify-full&application_name=svc%40db",
		"postgres://db.example.com?sslmode=verify-full&application_name=svc%40db",
	} {
		if _, ssl, err := checkTLSSettings(url); err != nil || !ssl.verify {
			t.Errorf("%q: %v", url, err)
		}
	}
}

// Rust: tls.rs::encoded_keys_are_decoded_before_they_are_matched
func TestEncodedKeysAreDecodedBeforeTheyAreMatched(t *testing.T) {
	if _, ssl, err := checkTLSSettings("postgres://u@h/db?%73slmode=require"); err != nil || !ssl.verify {
		t.Errorf("%+v %v", ssl, err)
	}
	if _, ssl, err := checkTLSSettings("postgres://u@h/db?sslmode=require&%73slrootcert=%2Fca.pem"); err != nil || ssl.rootFile != "/ca.pem" {
		t.Errorf("%+v %v", ssl, err)
	}
	refusedTLS(t, "postgres://u@h/db?%73slcert=x")
	refusedTLS(t, "postgres://u@h/db?sslmode=require&%73slkey=x")
	refusedTLS(t, "postgres://u@h/db?sslmode=disable&%73slmode=require")
	refusedTLS(t, "postgres://u:a%3Fb@h/db?%73slcert=x")
	if got, _, err := checkTLSSettings("postgres://u:p@h/db?%73slmode=disable"); err != nil || got != "postgres://u:p@h/db" {
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

// Rust: tls.rs::errors_do_not_contain_the_url_or_the_password (the errors of a URL that asks for TLS)
func TestTLSErrorsOfAnAcceptedURLDoNotContainThePassword(t *testing.T) {
	_, err := parsePoolConfig("postgres://u:s3cret@h/db?sslmode=require&sslrootcert=/nonexistent/ca.pem")
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("%v", err)
	}
}

// Not in Rust: the cross-check against the standard library's view of the URL.
func TestSameTLSView(t *testing.T) {
	if !sameTLSView("postgres://u:p@h/db?sslmode=disable", tlsSetting{}) || !sameTLSView("postgres://u:p@h/db", tlsSetting{}) {
		t.Error("a plain URL is refused")
	}
	if sameTLSView("postgres://u:p@h/db?SSLMODE=require", tlsSetting{}) || sameTLSView("postgres://u:p@h/db?sslcert=x", tlsSetting{}) {
		t.Error("a TLS option went unseen")
	}
	// With TLS asked for, only the exact spelling of the two accepted keys is seen as the scan's own.
	on := tlsSetting{verify: true}
	if !sameTLSView("postgres://u:p@h/db?sslmode=verify-full&sslrootcert=%2Fca.pem", on) {
		t.Error("an accepted TLS URL is refused")
	}
	if sameTLSView("postgres://u:p@h/db?sslmode=require&SSLMODE=disable", on) || sameTLSView("postgres://u:p@h/db?sslmode=disable", on) {
		t.Error("a TLS choice that differs went unseen")
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
