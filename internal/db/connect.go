package db

import (
	"context"
	"errors"
	neturl "net/url"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect creates a connection pool for databaseURL (postgres://user:password@host:port/database)
// and checks that a connection can be made. The pool holds at most four connections per CPU, like
// the Rust pool.
//
// The URL is the only source of connection settings, as in tokio-postgres: the PG* environment
// variables and the password file have no effect (url.go); a set PGSERVICE, which pgx cannot be told
// to ignore, is refused. TLS follows the sslmode of the URL (ADR 0018, tls.go): a URL without sslmode
// or with sslmode=disable connects without TLS; require, verify-ca and verify-full connect over TLS and
// always verify the certificate chain and the host name; prefer, allow and every other value is refused
// with ErrTLS. There is no connect timeout by default; give ctx a deadline or add
// connect_timeout=<seconds> to the URL to bound it.
//
// No error contains the URL or the password.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return ConnectSized(ctx, databaseURL, 0)
}

// ConnectSized is like Connect, with the largest number of connections the pool opens. A job worker
// needs one connection for each job it runs at the same time, so it sets the size from its
// concurrency.
//
// Intentional difference from Rust, where the size is an Option<usize>: here a maxSize of 0 or less
// means the default size (four per CPU), because Go has no None.
func ConnectSized(ctx context.Context, databaseURL string, maxSize int) (*pgxpool.Pool, error) {
	config, err := parsePoolConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if maxSize <= 0 {
		maxSize = runtime.NumCPU() * 4
	}
	config.MaxConns = int32(min(maxSize, 1<<31-1))
	// deadpool-postgres (RecyclingMethod::Fast, no timeouts) never retires a healthy connection: it
	// has no maximum lifetime and no idle time, and checks only that the connection is open. pgxpool
	// closes connections after an hour and after 30 idle minutes by default; both are turned off
	// here to match (a lifetime of 0 means none; the idle time has no off switch, so it is made
	// longer than any process). The health-check period (one minute) stays: it only replaces
	// connections that broke, which deadpool does when the connection is next taken.
	config.MaxConnLifetime = 0
	config.MaxConnIdleTime = 100 * 365 * 24 * time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, &Error{Kind: ErrCreatePool, Message: "cannot create the pool"}
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		return nil, acquireError(err)
	}
	conn.Release()
	return pool, nil
}

// parsePoolConfig builds the pool configuration from a database URL: its TLS settings are checked and
// taken out first (checkTLSSettings), then the rest is parsed. The URL is the only source of settings
// (see url.go). No error contains a value of the URL.
func parsePoolConfig(url string) (*pgxpool.Config, error) {
	cfg, err := prepareURL(url)
	if err != nil {
		return nil, err
	}
	return cfg.poolConfig()
}

// prepareURL is the URL as the settings of one connection: the TLS parameters taken out and checked,
// the rest parsed (Rust: tls::prepare, up to the connector).
func prepareURL(url string) (*urlConfig, error) { return prepareURLRecording(url, false) }

// prepareURLRecording is prepareURL; with record the settings that cannot be passed on are noted
// (see urlConfig.record).
func prepareURLRecording(url string, record bool) (*urlConfig, error) {
	rest, ssl, err := checkTLSSettings(url)
	if err != nil {
		return nil, err
	}
	cfg, err := parseURLConfigRecording(rest, record)
	if err != nil {
		return nil, err
	}
	cfg.ssl = ssl
	return cfg, nil
}

// acquireError maps the failure of the first connection: a server or connection error keeps the
// server's message and SQLSTATE (for example 28P01 for a wrong password, 3D000 for a missing
// database); a cancelled or expired context is ErrPool.
func acquireError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: ErrPool, Message: err.Error(), Err: err}
	}
	return wrap(err)
}

// forceNoTLS makes every connection attempt plain: pgx defaults to sslmode=prefer (TLS first, plain
// as the fallback), and a URL without TLS must never try it.
func forceNoTLS(config *pgxpool.Config) {
	cc := &config.ConnConfig.Config
	cc.TLSConfig = nil
	type hostPort struct {
		host string
		port uint16
	}
	seen := map[hostPort]bool{{cc.Host, cc.Port}: true}
	kept := cc.Fallbacks[:0]
	for _, fb := range cc.Fallbacks {
		key := hostPort{fb.Host, fb.Port}
		if seen[key] {
			continue
		}
		seen[key] = true
		fb.TLSConfig = nil
		kept = append(kept, fb)
	}
	cc.Fallbacks = kept
}

// checkTLSSettings takes the TLS parameters out of the URL (ADR 0018): it returns the URL without them
// and what they ask for (tlsSetting). sslmode=disable is the same as none. It follows the
// Rust parser (tls.rs): only postgres:// and postgresql:// URLs; the credentials end at the first
// '@'; a '/', '?' or '#' before it is refused so that the credentials never hide the query; keys
// are percent-decoded before they are compared; no other ssl* option is accepted.
func checkTLSSettings(url string) (string, tlsSetting, error) {
	refuse := func(message string) (string, tlsSetting, error) {
		return "", tlsSetting{}, &Error{Kind: ErrTLS, Message: message}
	}
	lower := strings.ToLower(url[:min(len(url), 13)])
	schemeLen := 0
	switch {
	case strings.HasPrefix(lower, "postgresql://"):
		schemeLen = len("postgresql://")
	case strings.HasPrefix(lower, "postgres://"):
		schemeLen = len("postgres://")
	default:
		// Anything that is not a URL: if the driver can parse it (the key=value form) it is refused
		// because its sslmode could not be checked here; if not, it is simply an invalid URL.
		if _, err := pgconn.ParseConfig(url); err != nil {
			return "", tlsSetting{}, &Error{Kind: ErrCreatePool, Message: "the database URL is not a valid PostgreSQL URL"}
		}
		return refuse("the database URL must start with postgres:// or postgresql://")
	}
	credsEnd := schemeLen
	if at := strings.IndexByte(url[schemeLen:], '@'); at >= 0 {
		if strings.ContainsAny(url[schemeLen:schemeLen+at], "/?#") {
			return refuse("the database URL has a `/`, `?` or `#` before its first `@`: percent-encode " +
				"them in the user name or password (`%2F`, `%3F`, `%23`), and an `@` in a query " +
				"value as `%40`")
		}
		credsEnd = schemeLen + at + 1
	}
	base, query, hasQuery := url, "", false
	if q := strings.IndexByte(url[credsEnd:], '?'); q >= 0 {
		base, query, hasQuery = url[:credsEnd+q], url[credsEnd+q+1:], true
	}
	var mode, root *string
	var kept []string
	if hasQuery {
		for _, param := range strings.Split(query, "&") {
			if param == "" {
				continue
			}
			rawKey, value, _ := strings.Cut(param, "=")
			key, ok := percentDecode(rawKey)
			if !ok {
				return refuse("a query parameter of the database URL is not valid percent-encoding")
			}
			switch {
			case key == "sslmode":
				v, ok := percentDecode(value)
				if !ok {
					return refuse("a query parameter of the database URL is not valid percent-encoding")
				}
				if mode != nil {
					return refuse("the database URL sets `sslmode` more than once")
				}
				mode = &v
			case key == "sslrootcert":
				v, ok := percentDecode(value)
				if !ok {
					return refuse("a query parameter of the database URL is not valid percent-encoding")
				}
				if root != nil {
					return refuse("the database URL sets `sslrootcert` more than once")
				}
				root = &v
			case key == "sslnegotiation":
				kept = append(kept, param)
			case strings.HasPrefix(key, "ssl") || strings.EqualFold(key, "requiressl"):
				return refuse("the database URL uses the TLS option `" + key + "`, which is not supported; " +
					"supported are `sslmode` and `sslrootcert`")
			default:
				kept = append(kept, param)
			}
		}
	}
	var ssl tlsSetting
	switch {
	case mode == nil || *mode == "disable":
		if root != nil {
			return refuse("the database URL has `sslrootcert` but TLS is off; " +
				"set `sslmode=require` or remove `sslrootcert`")
		}
	case *mode == "require" || *mode == "verify-ca" || *mode == "verify-full":
		ssl.verify = true
		switch {
		case root == nil || *root == "system":
		case *root == "":
			return refuse("the `sslrootcert` of the database URL is empty")
		default:
			ssl.rootFile = *root
		}
	case *mode == "prefer" || *mode == "allow":
		return refuse("`sslmode=prefer` is not supported: use `disable` or `require` " +
			"(`allow` is not supported either)")
	default:
		return refuse("the `sslmode` of the database URL is not supported: use `disable`, `require`, " +
			"`verify-ca` or `verify-full`")
	}
	if !sameTLSView(url, ssl) {
		return refuse("the database URL has an `sslmode` that could not be read safely; " +
			"check the URL (a `/`, `?`, `#`, `@` or `%` in the password must be percent-encoded)")
	}
	rest := base
	if len(kept) > 0 {
		rest += "?" + strings.Join(kept, "&")
	}
	return rest, ssl, nil
}

// percentDecode decodes %XX escapes (exactly two hex digits each); '+' stays '+'. It reports false
// for a bad escape or a result that is not UTF-8.
func percentDecode(value string) (string, bool) {
	var out []byte
	for i := 0; i < len(value); i++ {
		if value[i] != '%' {
			out = append(out, value[i])
			continue
		}
		if i+2 >= len(value) {
			return "", false
		}
		hi, ok1 := hexValue(value[i+1])
		lo, ok2 := hexValue(value[i+2])
		if !ok1 || !ok2 {
			return "", false
		}
		out = append(out, hi<<4|lo)
		i += 2
	}
	if !utf8.Valid(out) {
		return "", false
	}
	return string(out), true
}

func hexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// sameTLSView is the belt and braces of checkTLSSettings: the standard library's URL parser must
// see the same TLS choice as the scan above, and no TLS option that the scan did not accept (it
// would be one the scan missed). A URL that net/url cannot parse is left to the driver's own
// parser, which cannot see TLS settings either, because the scan found none and they are removed
// from the URL it gets.
func sameTLSView(raw string, ssl tlsSetting) bool {
	u, err := neturl.Parse(raw)
	if err != nil {
		return true
	}
	for key, values := range u.Query() {
		k := strings.ToLower(key)
		if k == "sslnegotiation" || !(strings.HasPrefix(k, "ssl") || k == "requiressl") {
			continue
		}
		// Only the exact spelling is accepted: another one (SSLMODE) is one that the scan above did not
		// see, and libpq-style parsers may read it.
		if key == "sslmode" && len(values) == 1 {
			if values[0] == "disable" && !ssl.verify {
				continue
			}
			if ssl.verify && (values[0] == "require" || values[0] == "verify-ca" || values[0] == "verify-full") {
				continue
			}
		}
		if key == "sslrootcert" && ssl.verify && len(values) == 1 {
			continue
		}
		return false
	}
	return true
}
