package db

import (
	"math"
	"net"
	"os"
	osuser "os/user"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The connection settings come from the database URL only, as they do in the Rust implementation
// (tokio-postgres 0.7.18 reads no PG* environment variable, no password file and no service
// file). pgx would fill every setting that the URL leaves out from those sources, and would send
// any unknown URL key to the server as a run-time parameter, so the URL is parsed here the way
// tokio-postgres parses it (config.rs, UrlParser and Config::param) and handed to pgx as a
// keyword/value string in which every setting that pgx can take from elsewhere is set explicitly.

// urlConfig is what tokio_postgres::Config holds, for the keys this package supports.
type urlConfig struct {
	user, password, dbname, options, applicationName *string
	hosts                                            []string
	ports                                            []uint16
	connectTimeout                                   time.Duration // 0: none
	keepalives                                       bool
	keepalivesIdle                                   time.Duration
	keepalivesInterval                               time.Duration // 0: the operating system's
	keepalivesRetries                                int           // 0: the operating system's
	targetSessionAttrs                               string
	channelBinding                                   string
}

func newURLConfig() *urlConfig {
	return &urlConfig{keepalives: true, keepalivesIdle: 2 * time.Hour, targetSessionAttrs: "any", channelBinding: "prefer"}
}

// urlError is a problem of the database URL. The text never contains a value of the URL, only the
// name of a key.
func urlError(detail string) error {
	return &Error{Kind: ErrCreatePool, Message: "the database URL is not valid: " + detail}
}

// seconds is time.Duration(n) * time.Second, clamped instead of overflowing (Rust: Duration::from_secs
// takes any u64; a value this large is "never" in practice).
func seconds(n int64) time.Duration {
	if n > int64(math.MaxInt64/time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(n) * time.Second
}

// parseUnsigned is str::parse::<uN>: digits with an optional leading '+', no sign otherwise.
func parseUnsigned(value string, bits int) (uint64, error) {
	return strconv.ParseUint(strings.TrimPrefix(value, "+"), 10, bits)
}

func invalidValue(key string) error { return urlError("invalid value for option `" + key + "`") }

func unsupported(what string) error {
	return urlError(what + " is not supported by the Go implementation")
}

// lenientDecode is percent_encoding::percent_decode followed by the UTF-8 check of tokio-postgres:
// an escape that is not two hex digits stays as it is.
func lenientDecode(s string, mustBeUTF8 bool) (string, error) {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := hexValue(s[i+1])
			lo, ok2 := hexValue(s[i+2])
			if ok1 && ok2 {
				out = append(out, hi<<4|lo)
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	if mustBeUTF8 && !utf8.Valid(out) {
		return "", urlError("a value is not valid UTF-8 after percent-decoding")
	}
	return string(out), nil
}

// parseURLConfig parses a postgres:// or postgresql:// URL like tokio-postgres (a sloppy parser
// that matches libpq: the credentials end at the first '@').
func parseURLConfig(url string) (*urlConfig, error) {
	s := strings.TrimPrefix(strings.TrimPrefix(url, "postgresql://"), "postgres://")
	if s == url {
		return nil, urlError("it must start with postgres:// or postgresql://")
	}
	cfg := newURLConfig()

	// Credentials.
	if at := strings.IndexByte(s, '@'); at >= 0 {
		creds := s[:at]
		s = s[at+1:]
		userPart, passwordPart, hasPassword := strings.Cut(creds, ":")
		user, err := lenientDecode(userPart, true)
		if err != nil {
			return nil, err
		}
		cfg.user = &user
		if hasPassword {
			password, _ := lenientDecode(passwordPart, false)
			cfg.password = &password
		}
	}

	// Host list: up to the first '/' or '?'.
	hostPart := s
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		hostPart, s = s[:i], s[i:]
	} else {
		s = ""
	}
	if hostPart != "" {
		for _, chunk := range strings.Split(hostPart, ",") {
			var host string
			var port *string
			if strings.HasPrefix(chunk, "[") {
				end := strings.IndexByte(chunk, ']')
				if end < 0 {
					return nil, invalidValue("host")
				}
				host = chunk[1:end]
				switch remaining := chunk[end+1:]; {
				case strings.HasPrefix(remaining, ":"):
					p := remaining[1:]
					port = &p
				case remaining == "":
				default:
					return nil, invalidValue("host")
				}
			} else {
				h, p, hasPort := strings.Cut(chunk, ":")
				host = h
				if hasPort {
					port = &p
				}
			}
			if err := cfg.addHost(host); err != nil {
				return nil, err
			}
			portText := "5432"
			if port != nil {
				portText = *port
			}
			decoded, err := lenientDecode(portText, true)
			if err != nil {
				return nil, err
			}
			if err := cfg.param("port", decoded); err != nil {
				return nil, err
			}
		}
	}

	// Database name.
	if strings.HasPrefix(s, "/") {
		s = s[1:]
		name := s
		if i := strings.IndexByte(s, '?'); i >= 0 {
			name, s = s[:i], s[i:]
		} else {
			s = ""
		}
		if name != "" {
			decoded, err := lenientDecode(name, true)
			if err != nil {
				return nil, err
			}
			cfg.dbname = &decoded
		}
	}

	// Parameters.
	if strings.HasPrefix(s, "?") {
		s = s[1:]
		for s != "" {
			eq := strings.IndexByte(s, '=')
			if eq < 0 {
				return nil, urlError("unterminated parameter")
			}
			key, err := lenientDecode(s[:eq], true)
			if err != nil {
				return nil, err
			}
			s = s[eq+1:]
			value := s
			if i := strings.IndexByte(s, '&'); i >= 0 {
				value, s = s[:i], s[i+1:]
			} else {
				s = ""
			}
			if key == "host" {
				if err := cfg.addHost(value); err != nil {
					return nil, err
				}
				continue
			}
			decoded, err := lenientDecode(value, true)
			if err != nil {
				return nil, err
			}
			if err := cfg.param(key, decoded); err != nil {
				return nil, err
			}
		}
	}
	return cfg, nil
}

// addHost is UrlParser::host_param: a percent-encoded host; one that starts with '/' is the
// directory of a Unix socket.
func (c *urlConfig) addHost(raw string) error {
	decoded, err := lenientDecode(raw, true)
	if err != nil {
		return err
	}
	c.hosts = append(c.hosts, decoded)
	return nil
}

// param is Config::param for the keys that this package supports. Unknown keys are refused, as
// tokio-postgres does ("unknown option"); keys that tokio-postgres knows and this package does not
// are refused with their own message.
func (c *urlConfig) param(key, value string) error {
	number := func() (int64, error) {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, invalidValue(key)
		}
		return n, nil
	}
	switch key {
	case "user":
		c.user = &value
	case "password":
		c.password = &value
	case "dbname":
		c.dbname = &value
	case "options":
		c.options = &value
	case "application_name":
		c.applicationName = &value
	case "sslnegotiation":
		// The TLS parameters are taken out of the URL by checkTLSSettings; this one is kept, and has
		// no effect without TLS.
		if value != "postgres" && value != "direct" {
			return invalidValue(key)
		}
	case "host":
		c.hosts = append(c.hosts, strings.Split(value, ",")...)
	case "hostaddr":
		return unsupported("`hostaddr`")
	case "port":
		for _, port := range strings.Split(value, ",") {
			n := uint64(5432)
			if port != "" {
				var err error
				if n, err = parseUnsigned(port, 16); err != nil {
					return invalidValue(key)
				}
			}
			c.ports = append(c.ports, uint16(n))
		}
	case "connect_timeout":
		n, err := number()
		if err != nil {
			return err
		}
		if n > 0 {
			c.connectTimeout = seconds(n)
		}
	case "tcp_user_timeout":
		n, err := number()
		if err != nil {
			return err
		}
		if n > 0 {
			return unsupported("`tcp_user_timeout`")
		}
	case "keepalives":
		n, err := parseUnsigned(value, 64)
		if err != nil {
			return invalidValue(key)
		}
		c.keepalives = n != 0
	case "keepalives_idle":
		n, err := number()
		if err != nil {
			return err
		}
		if n > 0 {
			c.keepalivesIdle = seconds(n)
		}
	case "keepalives_interval":
		n, err := number()
		if err != nil {
			return err
		}
		if n > 0 {
			c.keepalivesInterval = seconds(n)
		}
	case "keepalives_retries":
		n, err := parseUnsigned(value, 32)
		if err != nil {
			return invalidValue(key)
		}
		c.keepalivesRetries = int(n)
	case "target_session_attrs":
		if value != "any" && value != "read-write" && value != "read-only" {
			return invalidValue(key)
		}
		c.targetSessionAttrs = value
	case "channel_binding":
		if value != "disable" && value != "prefer" && value != "require" {
			return invalidValue(key)
		}
		c.channelBinding = value
	case "load_balance_hosts":
		switch value {
		case "disable":
		case "random":
			return unsupported("`load_balance_hosts=random`")
		default:
			return invalidValue(key)
		}
	default:
		return urlError("unknown option `" + key + "`")
	}
	return nil
}

// checkHosts are the checks of tokio_postgres::connect, made when the connection is opened.
func (c *urlConfig) checkHosts() error {
	for _, host := range c.hosts {
		// tokio-postgres connects to Tcp("") (and to a host with a comma, which is one host for it)
		// and fails to resolve it; the list of hosts reaches pgx comma-separated, so neither can be
		// passed on. The error is the connect-time one.
		if host == "" || strings.Contains(host, ",") {
			return &Error{Kind: ErrPostgres, Message: "error connecting to server: failed to lookup address information: Name or service not known"}
		}
	}
	if len(c.hosts) == 0 {
		return &Error{Kind: ErrPostgres, Message: "invalid configuration: both host and hostaddr are missing"}
	}
	if len(c.ports) > 1 && len(c.ports) != len(c.hosts) {
		return &Error{Kind: ErrPostgres, Message: "invalid configuration: invalid number of ports"}
	}
	return nil
}

// kv quotes a keyword/value pair for pgx.
func kv(key, value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return key + "='" + value + "'"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// pgxSettings are the settings that pgx merges from the environment (PG*), the password file and
// the service file when the connection string does not give them, each set to the value that
// tokio-postgres uses when the URL does not give it: empty, or its own default. The service
// settings are the one thing that cannot be set to a neutral value (see poolConfig).
func (c *urlConfig) pgxSettings(user string) string {
	ports := make([]string, len(c.ports))
	for i, p := range c.ports {
		ports[i] = strconv.Itoa(int(p))
	}
	if len(ports) == 0 {
		ports = []string{"5432"}
	}
	return strings.Join([]string{
		kv("host", strings.Join(c.hosts, ",")),
		kv("port", strings.Join(ports, ",")),
		kv("database", deref(c.dbname)),
		kv("user", user),
		kv("password", deref(c.password)),
		kv("passfile", ""),
		kv("application_name", ""),
		kv("options", ""),
		kv("timezone", ""),
		kv("connect_timeout", "0"),
		kv("sslmode", "disable"),
		kv("sslnegotiation", "postgres"),
		kv("sslcertmode", "disable"),
		kv("sslkey", ""),
		kv("sslcert", ""),
		kv("sslrootcert", ""),
		kv("sslpassword", ""),
		kv("sslsni", "0"),
		kv("target_session_attrs", c.targetSessionAttrs),
		kv("channel_binding", c.channelBinding),
		kv("min_protocol_version", ""),
		kv("max_protocol_version", ""),
		kv("require_auth", ""),
		kv("servicefile", ""),
	}, " ")
}

// keepAliveConfig is the TCP keepalive of tokio-postgres: on, idle 2 hours unless the URL says
// otherwise. A negative interval or count leaves the operating system's value, which is what
// tokio-postgres does when the URL gives none (Go's own defaults are 15 s and 9).
func (c *urlConfig) keepAliveConfig() net.KeepAliveConfig {
	interval, count := -time.Nanosecond, -1
	if c.keepalivesInterval > 0 {
		interval = c.keepalivesInterval
	}
	if c.keepalivesRetries > 0 {
		count = c.keepalivesRetries
	}
	return net.KeepAliveConfig{Enable: true, Idle: c.keepalivesIdle, Interval: interval, Count: count}
}

// poolConfig builds the pgx pool configuration from the URL's own settings.
func (c *urlConfig) poolConfig() (*pgxpool.Config, error) {
	// pgx reads the service file named by PGSERVICE whenever that variable is set, and no setting of
	// the connection string can switch that off (even an empty `service` makes it read the file).
	// tokio-postgres ignores PGSERVICE. Rather than change the process environment, a set PGSERVICE
	// is refused (a disclosed deviation, see the dependency evaluation).
	if os.Getenv("PGSERVICE") != "" {
		return nil, &Error{Kind: ErrCreatePool, Message: "Forgebase reads the connection settings only from the database URL; unset PGSERVICE"}
	}
	if err := c.checkHosts(); err != nil {
		return nil, err
	}
	// No user in the URL: the operating system's user name, as tokio-postgres does (whoami). It is
	// resolved here because pgx ignores an empty user setting and then takes PGUSER.
	user := deref(c.user)
	if c.user != nil && user == "" {
		// An empty user name is sent as it is by tokio-postgres, and the server refuses it. pgx
		// would take PGUSER instead, so the refusal is made here, with the server's own text.
		return nil, &Error{Kind: ErrPostgres, Message: "FATAL: no PostgreSQL user name specified in startup packet (SQLSTATE 28000)"}
	}
	if c.user == nil {
		current, err := osuser.Current()
		if err != nil || current.Username == "" {
			return nil, &Error{Kind: ErrPostgres, Message: "cannot determine the operating system user name for a database URL without a user"}
		}
		user = current.Username
	}
	config, err := pgxpool.ParseConfig(c.pgxSettings(user))
	if err != nil {
		// pgx's own message could quote parts of the settings; none is shown.
		return nil, urlError("it cannot be used")
	}

	cc := &config.ConnConfig.Config
	// Run-time parameters: only what the URL sets (tokio-postgres sends client_encoding, user,
	// database, options and application_name in the startup message). pgx adds every setting it
	// does not know as a parameter, from the environment too (PGTZ becomes timezone).
	cc.RuntimeParams = map[string]string{}
	if c.options != nil {
		cc.RuntimeParams["options"] = *c.options
	}
	if c.applicationName != nil {
		cc.RuntimeParams["application_name"] = *c.applicationName
	}
	cc.KerberosSrvName, cc.KerberosSpn = "", ""

	dialer := &net.Dialer{Timeout: c.connectTimeout}
	if c.keepalives {
		dialer.KeepAliveConfig = c.keepAliveConfig()
	} else {
		dialer.KeepAlive = -1
	}
	cc.DialFunc = dialer.DialContext
	cc.ConnectTimeout = 0
	forceNoTLS(config)
	return config, nil
}
