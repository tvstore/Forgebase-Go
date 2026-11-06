// Package dbtest gives the tests of internal/db (and of the packages above it) one throwaway
// PostgreSQL database per test. It corresponds to tests/common/mod.rs of the Rust crate
// forgebase-db.
//
// The server comes from the environment variable FORGEBASE_TEST_DATABASE_URL, a superuser or
// CREATEDB URL such as postgres://forgebase:forgebase@localhost:5432/postgres (see
// CONTRIBUTING.md). When it is not set a test fails, it never skips: a skipped database test
// is a test that silently does not run. This package is for tests only.
package dbtest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

const envVar = "FORGEBASE_TEST_DATABASE_URL"

// adminURL is the URL of the server with its maintenance database. The test fails when the
// variable is not set.
func adminURL(t testing.TB) string {
	t.Helper()
	url := os.Getenv(envVar)
	if url == "" {
		t.Fatalf("%[1]s is not set. The forgebase-db integration tests need a PostgreSQL 16 server; "+
			"set it to a superuser/CREATEDB URL, for example "+
			"%[1]s=postgres://forgebase:forgebase@localhost:5432/postgres (see CONTRIBUTING.md). "+
			"The tests never skip.", envVar)
	}
	return url
}

// withDatabase replaces the database name in postgres://user:pass@host:port/db[?query].
func withDatabase(url, database string) string {
	afterScheme := 0
	if i := strings.Index(url, "://"); i >= 0 {
		afterScheme = i + 3
	}
	pathStart := len(url)
	if i := strings.IndexByte(url[afterScheme:], '/'); i >= 0 {
		pathStart = afterScheme + i
	}
	pathEnd := len(url)
	if i := strings.IndexByte(url[pathStart:], '?'); i >= 0 {
		pathEnd = pathStart + i
	}
	return url[:pathStart] + "/" + database + url[pathEnd:]
}

// URLForDatabase is the admin URL with another database, for tests that connect on their own.
func URLForDatabase(t testing.TB, database string) string {
	t.Helper()
	return withDatabase(adminURL(t), database)
}

// URLWithPassword is the admin URL with another password.
func URLWithPassword(t testing.TB, password string) string {
	t.Helper()
	url := adminURL(t)
	start := 0
	if i := strings.Index(url, "://"); i >= 0 {
		start = i + 3
	}
	at := strings.LastIndexByte(url, '@')
	if at < 0 {
		t.Fatalf("the %s URL has no user info", envVar)
	}
	colon := at
	if i := strings.IndexByte(url[start:at], ':'); i >= 0 {
		colon = start + i
	}
	return url[:colon] + ":" + password + url[at:]
}

var counter atomic.Uint32

// DB is a database created for one test and dropped (with FORCE) when the test ends, also when it
// fails.
type DB struct {
	t testing.TB
	// Pool is the connection pool of the database.
	Pool     *pgxpool.Pool
	adminURL string
	name     string
}

// New is a fresh, empty database.
func New(t testing.TB) *DB {
	t.Helper()
	admin := adminURL(t)
	name := fmt.Sprintf("fb_test_%x_%x_%d", os.Getpid(), time.Now().Nanosecond(), counter.Add(1)-1)
	ctx := context.Background()
	adminConn, err := connectAdmin(ctx, admin)
	if err != nil {
		t.Fatalf("cannot connect to %s: %v", envVar, err)
	}
	_, err = adminConn.Exec(ctx, "CREATE DATABASE "+db.QuoteIdent(name))
	_ = adminConn.Close(ctx)
	if err != nil {
		t.Fatalf("create test database: %v", err)
	}
	d := &DB{t: t, adminURL: admin, name: name}
	// Registered before the pool exists, so the database is dropped whatever happens next.
	t.Cleanup(d.drop)
	d.Pool, err = db.Connect(ctx, withDatabase(admin, name))
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	return d
}

// connectAdmin opens a plain pgx connection to the admin URL with pgx's own defaults (sslmode
// prefer, PG* variables), not through db.Connect: the admin connection only creates and drops
// databases and is not part of what the tests check.
func connectAdmin(ctx context.Context, url string) (*pgx.Conn, error) {
	return pgx.Connect(ctx, url)
}

func (d *DB) drop() {
	if d.Pool != nil {
		d.Pool.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := connectAdmin(ctx, d.adminURL)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close(ctx) }()
	_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+db.QuoteIdent(d.name)+" WITH (FORCE)")
}

// URL is the URL of this database, for a test that needs a pool of its own.
func (d *DB) URL() string { return withDatabase(d.adminURL, d.name) }

// Migrated is a fresh database with the framework migrations applied.
func Migrated(t testing.TB) *DB {
	t.Helper()
	d := New(t)
	if _, err := db.MigrateFramework(context.Background(), d.Pool, false); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

// Exec runs sql (which may hold several statements) on a pooled connection.
func (d *DB) Exec(sql string) {
	d.t.Helper()
	if _, err := d.Pool.Exec(context.Background(), sql); err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
}

// TryExec runs sql and returns its error.
func (d *DB) TryExec(sql string) error {
	_, err := d.Pool.Exec(context.Background(), sql)
	return err
}

// Rows runs a query returning rows of text columns (cast in the query).
func (d *DB) Rows(sql string) [][]string {
	d.t.Helper()
	rows, err := d.Pool.Query(context.Background(), sql)
	if err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			d.t.Fatalf("%s: %v", sql, err)
		}
		row := make([]string, len(values))
		for i, v := range values {
			s, ok := v.(string)
			if !ok {
				d.t.Fatalf("%s: column %d is %T, not text (cast it in the query)", sql, i, v)
			}
			row[i] = s
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

// Column is a column of a table: its name and "<data_type>[(len|precision,scale)]/<nullable>".
type Column = [2]string

// Columns are the columns of a table, via information_schema, in column order.
func (d *DB) Columns(table string) []Column {
	d.t.Helper()
	sql := fmt.Sprintf("SELECT column_name::text, data_type::text || coalesce('(' || character_maximum_length || ')', '') "+
		"|| CASE WHEN data_type = 'numeric' THEN '(' || numeric_precision || ',' || numeric_scale || ')' ELSE '' END || '/' || is_nullable::text "+
		"FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = '%s' "+
		"ORDER BY ordinal_position", table)
	out := []Column{}
	for _, r := range d.Rows(sql) {
		out = append(out, Column{r[0], r[1]})
	}
	return out
}

// Sync syncs the registry with the default options.
func (d *DB) Sync(registry *core.Registry) (*db.SyncReport, error) {
	return db.Sync(context.Background(), d.Pool, registry, db.DefaultSyncOptions())
}

// Registry builds a registry from DocType TOML texts. The test fails when one is not valid.
func Registry(t testing.TB, texts ...string) *core.Registry {
	t.Helper()
	registry := core.NewRegistry()
	for _, text := range texts {
		doctype, err := core.ParseDocType(text)
		if err != nil {
			t.Fatalf("valid DocType: %v\n%s", err, text)
		}
		if errs := registry.Add(doctype); len(errs) > 0 {
			t.Fatalf("valid registry: %v", errs)
		}
	}
	return registry
}

// Customer is a DocType with one field of each simple type.
const Customer = `
name = "customer"
label = "Customer"
[[fields]]
name = "email"
type = "data"
max_length = 100
required = true
unique = true
[[fields]]
name = "notes"
type = "text"
[[fields]]
name = "points"
type = "int"
[[fields]]
name = "credit_limit"
type = "decimal"
precision = 12
scale = 2
[[fields]]
name = "active"
type = "check"
[[fields]]
name = "joined_on"
type = "date"
[[fields]]
name = "last_seen"
type = "datetime"
[[fields]]
name = "tier"
type = "select"
options = ["gold", "silver"]
`

// Person is a DocType named by its email, a typical Link target.
const Person = `
name = "person"
label = "Person"
naming = { field = "email" }
[[fields]]
name = "email"
type = "data"
required = true
unique = true
`

// SalesOrder links to person (optional) and to itself (optional); approver is a required link.
const SalesOrder = `
name = "sales_order"
label = "Sales Order"
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "customer"
type = "link"
target = "person"
[[fields]]
name = "follows"
type = "link"
target = "sales_order"
[[fields]]
name = "approver"
type = "link"
target = "person"
required = true
`
