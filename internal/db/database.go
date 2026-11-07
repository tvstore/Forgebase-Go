package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Creating a database and reading which apps it records (M7.2, ADR 0014; the Rust module database.rs).

// DefaultMaintenanceDB is the database CreateDatabase connects to when none is named.
const DefaultMaintenanceDB = "postgres"

// maxNameBytes is the longest database name PostgreSQL accepts, in bytes.
const maxNameBytes = 63

// sqlStateDuplicateDatabase is the SQLSTATE of CREATE DATABASE for a name that exists.
const sqlStateDuplicateDatabase = "42P04"

// CreateDatabase creates the database that databaseURL names, connecting to the maintenance database
// maintenanceDB (nil means DefaultMaintenanceDB; an explicit "" is sent as it is, and the server then uses the
// user name as the database name, as with tokio-postgres) of the same server with the same user and password. It
// returns true when it created the database and false when it already existed (nothing is changed
// then).
//
// The connection uses the TLS settings of the URL (Connect). CREATE DATABASE cannot run in a
// transaction, so this uses one plain connection of its own, closed before it returns. The name must
// be 1 to 63 ASCII letters, digits, `_` or `-`, and is quoted with QuoteIdent. The role needs the
// CREATEDB privilege.
//
// No error contains the URL: an unusable URL is reported without its text.
func CreateDatabase(ctx context.Context, databaseURL string, maintenanceDB *string) (bool, error) {
	cfg, err := prepareURL(databaseURL)
	if err != nil {
		return false, urlProblem(err, ErrCreateDatabase)
	}
	if cfg.dbname == nil {
		return false, &Error{Kind: ErrCreateDatabase, Message: "the database URL does not name a database"}
	}
	name := *cfg.dbname
	if problem := nameProblem(name); problem != "" {
		return false, &Error{Kind: ErrCreateDatabase, Message: fmt.Sprintf("the database name %s of the URL %s", quote(name), problem)}
	}
	maintenance := DefaultMaintenanceDB
	if maintenanceDB != nil {
		maintenance = *maintenanceDB
	}
	cfg.dbname = &maintenance
	conn, err := connectOnce(ctx, cfg)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	// No arguments: the simple protocol, one statement, which PostgreSQL runs outside a transaction block.
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+QuoteIdent(name)); err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == sqlStateDuplicateDatabase {
			return false, nil
		}
		return false, wrap(err)
	}
	return true, nil
}

// connectOnce opens one connection (no pool) with the settings of cfg, TLS included. The error is the
// server's or the connection's, never the URL.
func connectOnce(ctx context.Context, cfg *urlConfig) (*pgx.Conn, error) {
	config, err := cfg.poolConfig()
	if err != nil {
		return nil, err
	}
	return connectConfig(ctx, config)
}

func connectConfig(ctx context.Context, config *pgxpool.Config) (*pgx.Conn, error) {
	conn, err := pgx.ConnectConfig(ctx, config.ConnConfig)
	if err != nil {
		return nil, acquireError(err)
	}
	return conn, nil
}

// urlProblem is the error of a URL that cannot be used, for the callers that never show the URL: a
// TLS refusal keeps its own message, anything else is an invalid URL, in the kind of the caller.
func urlProblem(err error, kind ErrorKind) error {
	var e *Error
	if errors.As(err, &e) && e.Kind == ErrTLS {
		return err
	}
	return &Error{Kind: kind, Message: "the database URL is not a valid PostgreSQL URL"}
}

// nameProblem is why name cannot be created as a site database, or "" when it can.
func nameProblem(name string) string {
	valid := name != "" && len(name) <= maxNameBytes
	for i := 0; valid && i < len(name); i++ {
		b := name[i]
		valid = b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-'
	}
	if valid {
		return ""
	}
	return "must be 1 to 63 characters: ASCII letters, digits, `_` and `-`"
}
