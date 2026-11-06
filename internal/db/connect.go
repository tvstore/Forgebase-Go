package db

import (
	"context"
	"errors"
	neturl "net/url"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect creates a connection pool for databaseURL (postgres://user:password@host:port/database)
// and checks that a connection can be made. The pool holds at most four connections per CPU, like
// the Rust pool.
//
// There is no TLS yet (it arrives with P5, ADR 0018): a URL without sslmode or with sslmode=disable
// connects without TLS, whatever the PG* environment variables say, and every other sslmode, and
// sslrootcert, is refused with ErrTLS. There is no connect timeout by default; give ctx a deadline
// or add connect_timeout=<seconds> to the URL to bound it.
//
// No error contains the URL or the password.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return ConnectSized(ctx, databaseURL, 0)
}

// ConnectSized is like Connect, with the largest number of connections the pool opens (0 or less:
// four per CPU). A job worker needs one connection for each job it runs at the same time, so it
// sets the size from its concurrency.
func ConnectSized(ctx context.Context, databaseURL string, maxSize int) (*pgxpool.Pool, error) {
	cleaned, err := checkTLSSettings(databaseURL)
	if err != nil {
		return nil, err
	}
	config, err := parsePoolConfig(cleaned)
	if err != nil {
		return nil, err
	}
	forceNoTLS(config)
	if maxSize <= 0 {
		maxSize = runtime.NumCPU() * 4
	}
	config.MaxConns = int32(min(maxSize, 1<<31-1))
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

// parsePoolConfig parses a URL that checkTLSSettings accepted. pgx's own message is not used: it can
// hold the text of the URL (a url.Error), and no error of this package contains the URL.
func parsePoolConfig(url string) (*pgxpool.Config, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, &Error{Kind: ErrCreatePool, Message: "the database URL is not a valid PostgreSQL URL"}
	}
	return config, nil
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
// as the fallback), and this version has no TLS at all.
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

// checkTLSSettings refuses the URLs that ask for TLS (not available before P5, ADR 0018) and
// returns the URL without its TLS parameters (sslmode=disable is the same as none). It follows the
// Rust parser (tls.rs): only postgres:// and postgresql:// URLs; the credentials end at the first
// '@'; a '/', '?' or '#' before it is refused so that the credentials never hide the query; keys
// are percent-decoded before they are compared; no other ssl* option is accepted.
func checkTLSSettings(url string) (string, error) {
	refuse := func(message string) (string, error) {
		return "", &Error{Kind: ErrTLS, Message: message}
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
			return "", &Error{Kind: ErrCreatePool, Message: "the database URL is not a valid PostgreSQL URL"}
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
	switch {
	case mode == nil || *mode == "disable":
		if root != nil {
			return refuse("the database URL has `sslrootcert` but TLS is off; " +
				"set `sslmode=require` or remove `sslrootcert`")
		}
	case *mode == "require" || *mode == "verify-ca" || *mode == "verify-full":
		return refuse("TLS to PostgreSQL (`sslmode=" + *mode + "`) is not available yet; " +
			"it arrives with phase P5 of the Go port: use `sslmode=disable` or no `sslmode`")
	case *mode == "prefer" || *mode == "allow":
		return refuse("`sslmode=prefer` is not supported: use `disable` or `require` " +
			"(`allow` is not supported either)")
	default:
		return refuse("the `sslmode` of the database URL is not supported: use `disable`, `require`, " +
			"`verify-ca` or `verify-full`")
	}
	if !sameTLSView(url) {
		return refuse("the database URL has an `sslmode` that could not be read safely; " +
			"check the URL (a `/`, `?`, `#`, `@` or `%` in the password must be percent-encoded)")
	}
	rest := base
	if len(kept) > 0 {
		rest += "?" + strings.Join(kept, "&")
	}
	return rest, nil
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
// not see a TLS option that the scan above did not accept (it would be one the scan missed). A URL
// that net/url cannot parse is left to the driver's own parser, which cannot see TLS settings
// either, because the scan found none and they are removed from the URL it gets.
func sameTLSView(raw string) bool {
	u, err := neturl.Parse(raw)
	if err != nil {
		return true
	}
	for key, values := range u.Query() {
		k := strings.ToLower(key)
		if k == "sslnegotiation" || !(strings.HasPrefix(k, "ssl") || k == "requiressl") {
			continue
		}
		if k != "sslmode" || len(values) != 1 || values[0] != "disable" {
			return false
		}
	}
	return true
}
