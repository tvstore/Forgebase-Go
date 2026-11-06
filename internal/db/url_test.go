package db

import (
	"errors"
	"math"
	"net"
	"slices"
	"strings"
	"testing"
	"time"
)

func parsedURL(t *testing.T, url string) *urlConfig {
	t.Helper()
	cfg, err := parseURLConfig(url)
	if err != nil {
		t.Fatalf("%q: %v", url, err)
	}
	return cfg
}

func badURL(t *testing.T, url string) string {
	t.Helper()
	_, err := parseURLConfig(url)
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrCreatePool {
		t.Fatalf("%q: expected an ErrCreatePool, got %v", url, err)
	}
	return e.Message
}

// Not in Rust: the URL is parsed like tokio_postgres::Config (config.rs, UrlParser).
func TestParseURLConfig(t *testing.T) {
	cfg := parsedURL(t, "postgres://us%40er:pa%3Ass@h1:5433,h2/my%20db?application_name=a%20b&connect_timeout=7&keepalives=0&options=-c%20x%3D1")
	if deref(cfg.user) != "us@er" || deref(cfg.password) != "pa:ss" || deref(cfg.dbname) != "my db" ||
		deref(cfg.applicationName) != "a b" || deref(cfg.options) != "-c x=1" {
		t.Errorf("%+v", cfg)
	}
	if !slices.Equal(cfg.hosts, []string{"h1", "h2"}) || !slices.Equal(cfg.ports, []uint16{5433, 5432}) {
		t.Errorf("%v %v", cfg.hosts, cfg.ports)
	}
	if cfg.connectTimeout != 7*time.Second || cfg.keepalives {
		t.Errorf("%+v", cfg)
	}

	cfg = parsedURL(t, "postgres://u@[::1]:6543/db?host=%2Fvar%2Frun%2Fpostgresql")
	if !slices.Equal(cfg.hosts, []string{"::1", "/var/run/postgresql"}) || !slices.Equal(cfg.ports, []uint16{6543}) {
		t.Errorf("%v %v", cfg.hosts, cfg.ports)
	}
	if cfg.password != nil {
		t.Error("a password from nowhere")
	}

	// No host in the URL: the error of tokio_postgres::connect, when the connection is opened.
	cfg = parsedURL(t, "postgres:///db")
	err := cfg.checkHosts()
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrPostgres || !strings.Contains(e.Message, "both host and hostaddr are missing") {
		t.Errorf("%v", err)
	}
	cfg = parsedURL(t, "postgres://a,b,c/db?port=1,2")
	if err := cfg.checkHosts(); err == nil || !strings.Contains(err.Error(), "invalid number of ports") {
		t.Errorf("%v", err)
	}
}

func TestUnknownAndUnsupportedKeysAreRefused(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"postgres://h/db?search_path=x", "unknown option `search_path`"},
		{"postgres://h/db?timezone=UTC", "unknown option `timezone`"},
		{"postgres://h/db?passfile=/x", "unknown option `passfile`"},
		{"postgres://h/db?service=s", "unknown option `service`"},
		{"postgres://h/db?pool_max_conns=3", "unknown option `pool_max_conns`"},
		{"postgres://h/db?port=x", "invalid value for option `port`"},
		{"postgres://h/db?port=70000", "invalid value for option `port`"},
		{"postgres://h/db?connect_timeout=x", "invalid value for option `connect_timeout`"},
		{"postgres://h/db?target_session_attrs=primary", "invalid value for option `target_session_attrs`"},
		{"postgres://h/db?hostaddr=127.0.0.1", "`hostaddr` is not supported"},
		{"postgres://h/db?load_balance_hosts=random", "is not supported"},
		{"postgres://h/db?tcp_user_timeout=5", "is not supported"},
		{"postgres://h/db?application_name", "unterminated parameter"},
		{"postgres://[::1/db", "invalid value for option `host`"},
		{"postgres://u%ff@h/db", "UTF-8"},
	} {
		if msg := badURL(t, c.url); !strings.Contains(msg, c.want) {
			t.Errorf("%q: %q does not contain %q", c.url, msg, c.want)
		}
	}
	// A value of the URL is never in the message.
	if msg := badURL(t, "postgres://u:s3cret@h/db?port=s3cret"); strings.Contains(msg, "s3cret") {
		t.Errorf("%q", msg)
	}
	// Known keys with valid values pass.
	for _, ok := range []string{
		"postgres://h/db?keepalives_idle=5&keepalives_interval=3&keepalives_retries=2&channel_binding=disable",
		"postgres://h/db?target_session_attrs=read-write&load_balance_hosts=disable&tcp_user_timeout=0",
		"postgres://h/db?sslnegotiation=direct",
	} {
		parsedURL(t, ok)
	}
}

// Not in Rust: tokio-postgres decodes with percent_decode, which keeps a bad escape as it is.
func TestLenientDecode(t *testing.T) {
	for in, want := range map[string]string{"a%20b": "a b", "%zz": "%zz", "%4": "%4", "100%": "100%", "%e2%82%ac": "€"} {
		if got, err := lenientDecode(in, true); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
}

// The keyword/value string handed to pgx sets every setting that pgx could take elsewhere, and
// quotes what needs it.
func TestPgxSettingsQuoting(t *testing.T) {
	cfg := parsedURL(t, "postgres://u:%27%5C%20x@h/db")
	got := cfg.pgxSettings("u")
	if !strings.Contains(got, `password='\'\\ x'`) {
		t.Errorf("%s", got)
	}
	for _, key := range []string{"host=", "port=", "database=", "user=", "password=", "passfile=", "application_name=", "options=",
		"timezone=", "connect_timeout=", "sslmode='disable'", "servicefile=", "require_auth=", "min_protocol_version="} {
		if !strings.Contains(got, key) {
			t.Errorf("%s is not set explicitly: %s", key, got)
		}
	}
}

// Not in Rust: integer options accept a leading '+' like str::parse::<u16/u32/u64>, and a huge
// number of seconds does not overflow a Duration.
func TestIntegerOptions(t *testing.T) {
	cfg := parsedURL(t, "postgres://h:+6543/db?keepalives=+1&keepalives_idle=+5&keepalives_interval=+3&keepalives_retries=+2&connect_timeout=+7")
	if !slices.Equal(cfg.ports, []uint16{6543}) || !cfg.keepalives || cfg.keepalivesIdle != 5*time.Second ||
		cfg.keepalivesInterval != 3*time.Second || cfg.keepalivesRetries != 2 || cfg.connectTimeout != 7*time.Second {
		t.Errorf("%+v", cfg)
	}
	for _, bad := range []string{"postgres://h:+/db", "postgres://h:++5/db", "postgres://h:-5/db",
		"postgres://h/db?keepalives=-1", "postgres://h/db?keepalives_retries=-1", "postgres://h/db?keepalives=++1"} {
		badURL(t, bad)
	}
	huge := parsedURL(t, "postgres://h/db?connect_timeout=9223372036854775807&keepalives_idle=9223372036854775807")
	if huge.connectTimeout != time.Duration(math.MaxInt64) || huge.keepalivesIdle != time.Duration(math.MaxInt64) {
		t.Errorf("%v %v", huge.connectTimeout, huge.keepalivesIdle)
	}
	badURL(t, "postgres://h/db?connect_timeout=9223372036854775808")
}

// Not in Rust: without keepalive settings in the URL the operating system's interval and count are
// kept, as tokio-postgres does.
func TestKeepAliveConfig(t *testing.T) {
	got := parsedURL(t, "postgres://h/db").keepAliveConfig()
	want := net.KeepAliveConfig{Enable: true, Idle: 2 * time.Hour, Interval: -time.Nanosecond, Count: -1}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	got = parsedURL(t, "postgres://h/db?keepalives_idle=10&keepalives_interval=4&keepalives_retries=3").keepAliveConfig()
	want = net.KeepAliveConfig{Enable: true, Idle: 10 * time.Second, Interval: 4 * time.Second, Count: 3}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Not in Rust: hosts that tokio-postgres fails to resolve at connect time are refused at connect
// time, and a host= value with a comma is one host.
func TestEmptyAndCommaHostsFailAtConnectTime(t *testing.T) {
	for _, url := range []string{"postgres://a,,b/db", "postgres://h/db?host=", "postgres://h/db?host=a%2Cb", "postgres://,/db"} {
		cfg := parsedURL(t, url)
		err := cfg.checkHosts()
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrPostgres || !strings.Contains(e.Message, "failed to lookup address information") {
			t.Errorf("%q: %v", url, err)
		}
	}
	// The comma stays in the host: it is not split into two.
	cfg := parsedURL(t, "postgres://h/db?host=a%2Cb")
	if !slices.Equal(cfg.hosts, []string{"h", "a,b"}) {
		t.Errorf("%v", cfg.hosts)
	}
}

// Not in Rust: an empty user in the URL is an empty user (the server refuses it), not "no user".
func TestEmptyUserIsNotNoUser(t *testing.T) {
	if cfg := parsedURL(t, "postgres://:pw@h/db"); cfg.user == nil || *cfg.user != "" {
		t.Errorf("%v", cfg.user)
	}
	if cfg := parsedURL(t, "postgres://h/db"); cfg.user != nil {
		t.Errorf("%v", cfg.user)
	}
	_, err := parsePoolConfig("postgres://:pw@h/db")
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrPostgres || !strings.Contains(e.Message, "28000") || strings.Contains(e.Message, "pw") {
		t.Errorf("%v", err)
	}
}

// Not in Rust: a set PGSERVICE is refused, with no change of the process environment.
func TestPGSERVICEIsRefused(t *testing.T) {
	t.Setenv("PGSERVICE", "svc")
	_, err := parsePoolConfig("postgres://u:p@h/db")
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrCreatePool ||
		e.Message != "Forgebase reads the connection settings only from the database URL; unset PGSERVICE" {
		t.Errorf("%v", err)
	}
	t.Setenv("PGSERVICE", "")
	if _, err := parsePoolConfig("postgres://u:p@h/db"); err != nil {
		t.Errorf("an empty PGSERVICE is not a service: %v", err)
	}
}
