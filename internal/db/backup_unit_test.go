package db

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// Ported from the unit tests of crates/forgebase-db/src/backup.rs and database.rs.

func envOf(t *testing.T, url string) []EnvVar {
	t.Helper()
	env, err := PgEnvironment(url)
	if err != nil {
		t.Fatalf("%q: %v", url, err)
	}
	return env
}

func envErr(t *testing.T, url string) string {
	t.Helper()
	_, err := PgEnvironment(url)
	if err == nil {
		t.Fatalf("%q was accepted", url)
	}
	return err.Error()
}

func hasVar(env []EnvVar, name, value string) bool { return slices.Contains(env, EnvVar{name, value}) }

// Rust: backup.rs::the_url_becomes_pg_variables
func TestTheURLBecomesPgVariables(t *testing.T) {
	env := envOf(t, "postgres://ann:s3cret%20pw@db.example.com:6543/shop_1")
	want := []EnvVar{{"PGHOST", "db.example.com"}, {"PGPORT", "6543"}, {"PGUSER", "ann"}, {"PGPASSWORD", "s3cret pw"},
		{"PGDATABASE", "shop_1"}, {"PGSSLMODE", "disable"}}
	if !slices.Equal(env, want) {
		t.Errorf("%v", env)
	}
	// Parts the URL lacks are left out; TLS is off unless the URL asks for it (ADR 0018).
	env = envOf(t, "postgres:///shop?sslmode=disable")
	if !slices.Equal(env, []EnvVar{{"PGDATABASE", "shop"}, {"PGSSLMODE", "disable"}}) {
		t.Errorf("%v", env)
	}
	if !hasVar(envOf(t, "postgres://h/shop"), "PGSSLMODE", "disable") {
		t.Error("no sslmode: disable")
	}
	// `require` and the stricter modes verify the host name, so pg_dump gets verify-full.
	for _, mode := range []string{"require", "verify-ca", "verify-full"} {
		env := envOf(t, "postgres://h/shop?sslmode="+mode)
		if !hasVar(env, "PGSSLMODE", "verify-full") || !hasVar(env, "PGSSLROOTCERT", "system") {
			t.Errorf("%s: %v", mode, env)
		}
	}
	if env := envOf(t, "postgres://h/shop?sslmode=require&sslrootcert=/etc/ca%20x.pem"); !hasVar(env, "PGSSLROOTCERT", "/etc/ca x.pem") {
		t.Errorf("%v", env)
	}
	// prefer is refused, with a message that names neither the URL nor the password.
	msg := envErr(t, "postgres://u:pw@h/shop?sslmode=prefer")
	if !strings.Contains(msg, "`sslmode=prefer` is not supported") || strings.Contains(msg, "pw") {
		t.Errorf("%q", msg)
	}
}

// Rust: backup.rs::tls_over_a_unix_socket_or_without_a_host_is_refused_like_a_connection
func TestTLSOverAUnixSocketOrWithoutAHostIsRefusedLikeAConnection(t *testing.T) {
	for _, url := range []string{
		"postgres://u:pw@/shop?host=/tmp&sslmode=require",
		"postgres://u:pw@/shop?host=/var/run/postgresql&sslmode=verify-full",
		"postgres:///shop?sslmode=require",
	} {
		msg := envErr(t, url)
		if !strings.Contains(msg, "TLS is not supported over a Unix socket host") && !strings.Contains(msg, "TLS needs a host name") {
			t.Errorf("%q: %q", url, msg)
		}
		if strings.Contains(msg, "pw") || strings.Contains(msg, "/tmp") {
			t.Errorf("%q leaks: %q", url, msg)
		}
	}
	// TLS off keeps working over a socket: pg_dump may use it.
	env := envOf(t, "postgres://u@/shop?host=/tmp&sslmode=disable")
	if !hasVar(env, "PGHOST", "/tmp") || !hasVar(env, "PGSSLMODE", "disable") {
		t.Errorf("%v", env)
	}
}

// Rust: backup.rs::tls_is_found_behind_an_encoded_question_mark_and_an_encoded_key
func TestTLSIsFoundBehindAnEncodedQuestionMarkAndAnEncodedKey(t *testing.T) {
	for _, url := range []string{
		"postgres://u:a%3Fb@h/shop?sslmode=require",
		"postgres://u:x%3Fsslmode=disable@h/shop?sslmode=require",
		"postgres://u@h/shop?%73slmode=require",
	} {
		if env := envOf(t, url); !hasVar(env, "PGSSLMODE", "verify-full") {
			t.Errorf("%q: %v", url, env)
		}
	}
	if env := envOf(t, "postgres://u:a%3Fb%2Fc%23d@h/shop?sslmode=require"); !hasVar(env, "PGPASSWORD", "a?b/c#d") {
		t.Errorf("%v", env)
	}
	if msg := envErr(t, "postgres://u:pw@h/shop?%73slcert=x"); !strings.Contains(msg, "`sslcert`") || strings.Contains(msg, "pw") {
		t.Errorf("%q", msg)
	}
}

// Rust: backup.rs::raw_special_characters_before_the_first_at_are_refused_for_the_backup_tools_too
func TestRawSpecialCharactersBeforeTheFirstAtAreRefusedForTheBackupToolsToo(t *testing.T) {
	for _, url := range []string{
		"postgres://db.example.com/app?sslmode=verify-full&application_name=svc@db.example.com",
		"postgres://db.example.com?sslmode=verify-full&application_name=svc@db.example.com",
		"postgres://u:pw?x@h/shop?sslmode=require",
		"postgres://u:pw/x@h/shop?sslmode=require",
		"postgres://u:pw#x@h/shop?sslmode=require",
	} {
		msg := envErr(t, url)
		if !strings.Contains(msg, "before its first `@`") || strings.Contains(msg, "pw") {
			t.Errorf("%q: %q", url, msg)
		}
	}
}

// Rust: backup.rs::unsupported_options_are_errors_that_name_the_option_and_not_the_value
func TestUnsupportedOptionsAreErrorsThatNameTheOptionAndNotTheValue(t *testing.T) {
	for _, c := range []struct{ url, option string }{
		{"postgres://u:pw@h1,h2/db", "host (more than one)"},
		{"postgres://u:pw@h/db?options=-c%20xyzzy%3D1", "options"},
		{"postgres://u:pw@h/db?application_name=xyzzy", "application_name"},
		{"postgres://u:pw@h/db?connect_timeout=77", "connect_timeout"},
		{"postgres://u:pw@h/db?keepalives=0", "keepalives"},
		{"postgres://u:pw@h/db?target_session_attrs=read-write", "target_session_attrs"},
		// The settings that the Go implementation cannot pass to a connection are named too.
		{"postgres://u:pw@h/db?hostaddr=127.0.0.1", "hostaddr"},
		{"postgres://u:pw@h/db?tcp_user_timeout=5", "tcp_user_timeout"},
		{"postgres://u:pw@h/db?load_balance_hosts=random", "load_balance_hosts"},
		{"postgres://u:pw@h/db?sslnegotiation=direct", "sslnegotiation"},
		{"postgres://u:pw@h/db?keepalives_idle=5", "keepalives_idle"},
		{"postgres://u:pw@h/db?channel_binding=require", "channel_binding"},
	} {
		msg := envErr(t, c.url)
		if !strings.Contains(msg, "`"+c.option+"`") {
			t.Errorf("%q: %q", c.url, msg)
		}
		for _, secret := range []string{"xyzzy", "77", "pw", "read-write", "127.0.0.1"} {
			if strings.Contains(msg, secret) {
				t.Errorf("%q: %q has %q", c.url, msg, secret)
			}
		}
	}
	// The defaults are not options.
	envOf(t, "postgres://u@h/db?connect_timeout=0&keepalives=1&load_balance_hosts=disable&target_session_attrs=any&channel_binding=prefer&sslnegotiation=postgres")
}

// Rust: backup.rs::a_bad_url_or_database_name_is_an_error_without_the_text
func TestABadURLOrDatabaseNameIsAnErrorWithoutTheText(t *testing.T) {
	for _, url := range []string{"not a url ::", "postgres://u:pw@h/", "postgres://u:pw@h/a%20b", "postgres://u:pw@h/host%3Dxyzzy"} {
		msg := envErr(t, url)
		if strings.Contains(msg, "pw") || strings.Contains(msg, "not a url") {
			t.Errorf("%q: %q", url, msg)
		}
	}
	var e *Error
	if _, err := PgEnvironment("not a url ::"); !errors.As(err, &e) || e.Kind != ErrBackup || e.Message != "the database URL is not a valid PostgreSQL URL" {
		t.Errorf("%v", err)
	}
}

// Rust: backup.rs::the_major_version_comes_from_the_version_number
func TestTheMajorVersionComesFromTheVersionNumber(t *testing.T) {
	for _, c := range []struct {
		num  int32
		want uint32
	}{{160_004, 16}, {170_000, 17}, {0, 0}} {
		if got := (DatabaseInfo{ServerVersionNum: c.num}).ServerMajor(); got != c.want {
			t.Errorf("%d: %d", c.num, got)
		}
	}
}

// Rust: database.rs::database_names_are_checked
func TestDatabaseNamesAreChecked(t *testing.T) {
	for _, good := range []string{"a", "forgebase-test", "Acme_2", strings.Repeat("x", 63)} {
		if p := nameProblem(good); p != "" {
			t.Errorf("%q: %s", good, p)
		}
	}
	for _, bad := range []string{"", "a b", "a;b", "a\"b", "é", strings.Repeat("x", 64)} {
		if nameProblem(bad) == "" {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// Rust: database.rs (create_database: the errors of an unusable URL never show it)
func TestCreateDatabaseRefusesAnUnusableURLWithoutShowingIt(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"postgres://u:pw@h", "does not name a database"},
		{"postgres://u:pw@h/a%20b", "the database name \"a b\" of the URL must be 1 to 63 characters"},
		{"not a url :: pw", "not a valid PostgreSQL URL"},
		{"postgres://u:pw@h/db?sslmode=prefer", "`sslmode=prefer` is not supported"},
	} {
		_, err := CreateDatabase(t.Context(), c.url, "")
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "pw") && !strings.Contains(c.want, "pw") {
			t.Errorf("%q: %v", c.url, err)
		}
	}
}

// Rust: tls.rs::the_backup_tools_get_the_matching_tls_environment (no server needed: the environment
// is a function of the URL)
func TestTheBackupToolsGetTheMatchingTLSEnvironment(t *testing.T) {
	env := envOf(t, "postgres://u:pw@db.example.com/shop?sslmode=require")
	if !hasVar(env, "PGSSLMODE", "verify-full") || !hasVar(env, "PGSSLROOTCERT", "system") {
		t.Errorf("%v", env)
	}
	env = envOf(t, "postgres://u@h/shop?sslmode=verify-ca&sslrootcert=/tmp/a%20b.pem")
	if !hasVar(env, "PGSSLMODE", "verify-full") || !hasVar(env, "PGSSLROOTCERT", "/tmp/a b.pem") {
		t.Errorf("%v", env)
	}
	env = envOf(t, "postgres://u@h/shop")
	if !hasVar(env, "PGSSLMODE", "disable") {
		t.Errorf("%v", env)
	}
	for _, v := range env {
		if v.Name == "PGSSLROOTCERT" {
			t.Errorf("%v", env)
		}
	}
}
