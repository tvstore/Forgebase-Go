package db

import (
	"context"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// What `site backup` and `site restore` need from the database (M7.3, ADR 0014; the Rust module
// backup.rs): the connection of a site as PG* environment variables for pg_dump and pg_restore, and a
// look at the server (its version, and whether the database is empty).
//
// The tools are started by the command line (internal/app). Nothing here puts a URL, a password or
// any other secret into a message: an error names an option, never its value.

// EnvVar is one environment variable of a child process.
type EnvVar struct{ Name, Value string }

// PgEnvironment is the PG* variables that give pg_dump or pg_restore the connection of databaseURL:
// PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE, PGSSLMODE and, with TLS, PGSSLROOTCERT. A part the URL
// does not have is left out. This keeps the URL and the password off the command line, where ps shows
// them.
//
// TLS follows ADR 0018: no sslmode or disable gives PGSSLMODE=disable; require, verify-ca and
// verify-full give PGSSLMODE=verify-full (the verification this package's own connections do) and
// PGSSLROOTCERT is the sslrootcert path, or system (PostgreSQL 16 or later); prefer and other values are
// errors.
//
// A URL option that these variables cannot express (more than one host, options, application_name,
// connect_timeout, the keepalive and target-session settings, ...) is an error that names the option;
// the database name must be usable as a site database name (the rule of CreateDatabase), which also
// means it can never be read as a connection string. No error contains a value of the URL.
func PgEnvironment(databaseURL string) ([]EnvVar, error) {
	cfg, err := prepareURLRecording(databaseURL, true)
	if err != nil {
		return nil, urlProblem(err, ErrBackup)
	}
	if option := cfg.unsupportedOption(); option != "" {
		return nil, &Error{Kind: ErrBackup, Message: "the database URL uses the option `" + option + "`, which cannot be passed to pg_dump or " +
			"pg_restore through the PG* environment variables; remove it from the URL"}
	}
	// The same refusal as for a connection: TLS needs a TCP host name, so a Unix socket (or no host)
	// with sslmode=require is not turned into PGSSLMODE=verify-full.
	if cfg.ssl.verify {
		if err := cfg.checkTLSHost(); err != nil {
			return nil, err
		}
	}
	var env []EnvVar
	if len(cfg.hosts) == 1 {
		env = append(env, EnvVar{"PGHOST", cfg.hosts[0]})
	}
	if len(cfg.ports) == 1 {
		env = append(env, EnvVar{"PGPORT", strconv.Itoa(int(cfg.ports[0]))})
	}
	if cfg.user != nil {
		env = append(env, EnvVar{"PGUSER", *cfg.user})
	}
	if cfg.password != nil {
		if !utf8.ValidString(*cfg.password) {
			return nil, &Error{Kind: ErrBackup, Message: "the password of the database URL is not valid UTF-8"}
		}
		env = append(env, EnvVar{"PGPASSWORD", *cfg.password})
	}
	if cfg.dbname == nil {
		return nil, &Error{Kind: ErrBackup, Message: "the database URL does not name a database"}
	}
	name := *cfg.dbname
	if problem := nameProblem(name); problem != "" {
		return nil, &Error{Kind: ErrBackup, Message: "the database name " + quote(name) + " of the URL " + problem}
	}
	env = append(env, EnvVar{"PGDATABASE", name})
	return append(env, pgSSLEnvironment(cfg.ssl)...), nil
}

// unsupportedOption is the name of the first URL option that is not one of host, port, user, password,
// database and the TLS parameters (sslmode, sslrootcert; they are handled apart), if the URL sets one
// (it differs from the default of an empty configuration); "" when there is none.
func (c *urlConfig) unsupportedOption() string {
	checks := []struct {
		name string
		set  bool
	}{
		{"host (more than one)", len(c.hosts) > 1},
		{"port (more than one)", len(c.ports) > 1},
		{"hostaddr", c.unsupportedSet["hostaddr"]},
		{"options", c.options != nil},
		{"application_name", c.applicationName != nil},
		{"sslnegotiation", c.sslNegotiation != "postgres"},
		{"connect_timeout", c.connectTimeout != 0},
		{"tcp_user_timeout", c.unsupportedSet["tcp_user_timeout"]},
		{"keepalives", !c.keepalives},
		{"keepalives_idle", c.keepalivesIdle != 2*time.Hour},
		{"keepalives_interval", c.keepalivesInterval != 0},
		{"keepalives_retries", c.keepalivesRetries != 0},
		{"target_session_attrs", c.targetSessionAttrs != "any"},
		{"channel_binding", c.channelBinding != "prefer"},
		{"load_balance_hosts", c.unsupportedSet["load_balance_hosts"]},
	}
	for _, check := range checks {
		if check.set {
			return check.name
		}
	}
	return ""
}

// DatabaseInfo is what `site backup` and `site restore` need to know about the server and the
// database.
type DatabaseInfo struct {
	// ServerVersionNum is server_version_num, for example 160004.
	ServerVersionNum int32
	// IsEmpty: no relation, extra schema, extension, function or type in the database (see
	// EmptyCheckSQL).
	IsEmpty bool
}

// ServerMajor is the major version of the server: 16 for 160004.
func (i DatabaseInfo) ServerMajor() uint32 {
	if i.ServerVersionNum < 0 {
		return 0
	}
	return uint32(i.ServerVersionNum / 10_000)
}

// EmptyCheckSQL is the SQL of the emptiness check, one row with one boolean. Conservative: the
// database is empty only when it has
//
//   - no relation (table, index, sequence, view, composite type, ...) in any schema except pg_catalog,
//     information_schema and the pg_toast and pg_temp schemas;
//   - no schema other than public (and those);
//   - no extension other than plpgsql;
//   - no function, and no domain, enum, range or multirange type, outside those schemas.
//
// Objects of other kinds (for example a publication or an event trigger) are not checked. Anything the
// check finds makes the database "not empty", so a restore never merges into existing data of the kinds
// checked.
const EmptyCheckSQL = `
WITH user_schemas AS (
    SELECT oid, nspname FROM pg_namespace
    WHERE nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
      AND nspname NOT LIKE 'pg\_temp\_%' AND nspname NOT LIKE 'pg\_toast\_temp\_%'
)
SELECT NOT EXISTS (SELECT 1 FROM pg_class c JOIN user_schemas s ON s.oid = c.relnamespace)
   AND NOT EXISTS (SELECT 1 FROM user_schemas WHERE nspname <> 'public')
   AND NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname <> 'plpgsql')
   AND NOT EXISTS (SELECT 1 FROM pg_proc p JOIN user_schemas s ON s.oid = p.pronamespace)
   AND NOT EXISTS (SELECT 1 FROM pg_type t JOIN user_schemas s ON s.oid = t.typnamespace
                   WHERE t.typtype IN ('d', 'e', 'r', 'm'))`

// InspectDatabase connects to the database of databaseURL once (no pool; TLS as the URL says) and reads
// its DatabaseInfo. The error is the server's or the connection's, never the URL.
func InspectDatabase(ctx context.Context, databaseURL string) (DatabaseInfo, error) {
	cfg, err := prepareURL(databaseURL)
	if err != nil {
		return DatabaseInfo{}, urlProblem(err, ErrBackup)
	}
	conn, err := connectOnce(ctx, cfg)
	if err != nil {
		return DatabaseInfo{}, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var version string
	if err := conn.QueryRow(ctx, "SHOW server_version_num", pgx.QueryExecModeSimpleProtocol).Scan(&version); err != nil {
		return DatabaseInfo{}, wrap(err)
	}
	var empty bool
	if err := conn.QueryRow(ctx, EmptyCheckSQL, pgx.QueryExecModeSimpleProtocol).Scan(&empty); err != nil {
		return DatabaseInfo{}, wrap(err)
	}
	number, err := strconv.ParseInt(version, 10, 32)
	if err != nil {
		return DatabaseInfo{}, &Error{Kind: ErrBackup, Message: "the server reports no usable version number"}
	}
	return DatabaseInfo{ServerVersionNum: int32(number), IsEmpty: empty}, nil
}
