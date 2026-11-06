package db_test

import (
	"errors"
	"os"
	osuser "os/user"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Not in Rust: the pool size. Rust uses the deadpool default, four connections per CPU.
func TestPoolSize(t *testing.T) {
	d := dbtest.New(t)
	if got, want := int(d.Pool.Config().MaxConns), runtime.NumCPU()*4; got != want {
		t.Errorf("default pool size %d, want %d (four per CPU)", got, want)
	}
	pool, err := db.ConnectSized(bg, d.URL(), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if got := pool.Config().MaxConns; got != 3 {
		t.Errorf("sized pool %d, want 3", got)
	}
}

// Not in Rust (its TLS arrives with P5, ADR 0018): a URL without sslmode, or with sslmode=disable,
// connects without TLS, although pgx would try TLS first (sslmode=prefer) and a development server
// usually offers it.
func TestNoSslmodeConnectsWithoutTLS(t *testing.T) {
	d := dbtest.New(t)
	for _, url := range []string{d.URL(), d.URL() + "?sslmode=disable"} {
		pool, err := db.Connect(bg, url)
		if err != nil {
			t.Fatal(err)
		}
		var ssl bool
		err = pool.QueryRow(bg, "SELECT coalesce((SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()), false)").Scan(&ssl)
		pool.Close()
		if err != nil {
			t.Fatal(err)
		}
		if ssl {
			t.Errorf("%s: the connection uses TLS", strings.SplitN(url, "?", 2)[len(strings.SplitN(url, "?", 2))-1])
		}
	}
}

// Not in Rust: any other sslmode is refused until P5, without trying to connect and without the URL
// or the password in the message.
func TestOtherSslmodesAreRefusedWithoutConnecting(t *testing.T) {
	for _, mode := range []string{"require", "verify-full", "prefer"} {
		_, err := db.Connect(bg, "postgres://nobody:s3cret@127.0.0.1:1/none?sslmode="+mode)
		e := asDbError(t, err)
		if e.Kind != db.ErrTLS {
			t.Errorf("%s: %v", mode, err)
		}
		if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("%s: the message has the URL: %v", mode, err)
		}
	}
}

// Not in Rust: an invalid URL never shows its text (the standard library's parse errors would).
func TestAnInvalidURLIsNotInTheError(t *testing.T) {
	_, err := db.Connect(bg, "postgres://u:s3cret@h:notaport/db")
	e := asDbError(t, err)
	if e.Kind != db.ErrCreatePool || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("%v", err)
	}
}

// Not in Rust: the conversion of driver errors (Rust From<tokio_postgres::Error>).
func TestWrap(t *testing.T) {
	if db.WrapForTest(nil) != nil {
		t.Error("nil")
	}
	lock := db.WrapForTest(&pgconn.PgError{Severity: "ERROR", Code: "55P03", Message: "canceling statement due to lock timeout"})
	if e := asDbError(t, lock); e.Kind != db.ErrLockTimeout {
		t.Errorf("%v", lock)
	}
	if !strings.Contains(lock.Error(), "retry later") {
		t.Errorf("%v", lock)
	}
	server := db.WrapForTest(&pgconn.PgError{Severity: "ERROR", Code: "23505", Message: "duplicate key", Detail: "Key (x)=(1) already exists."})
	if got, want := server.Error(), "ERROR: duplicate key (SQLSTATE 23505); detail: Key (x)=(1) already exists."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	var pg *pgconn.PgError
	if !errors.As(server, &pg) || pg.Code != "23505" {
		t.Error("errors.As does not reach the PgError")
	}
	missing := db.WrapForTest(&pgconn.PgError{Severity: "FATAL", Code: "3D000", Message: `database "x" does not exist`})
	if !db.IsMissingDatabase(missing) || db.IsMissingDatabase(server) || db.IsMissingDatabase(nil) {
		t.Error("IsMissingDatabase")
	}
	plain := db.WrapForTest(errors.New("boom"))
	if e := asDbError(t, plain); e.Kind != db.ErrPostgres || plain.Error() != "boom" {
		t.Errorf("%v", plain)
	}
	// An *Error passes through unchanged.
	if again := db.WrapForTest(plain); again != plain {
		t.Error("a wrapped error was wrapped again")
	}
}

// Rust: error.rs (the Display texts of the variants that this phase has)
func TestErrorTexts(t *testing.T) {
	for _, c := range []struct {
		e    *db.Error
		want string
	}{
		{&db.Error{Kind: db.ErrChecksumMismatch, Version: 3, Name: "auth", Expected: "aa", Actual: "bb"},
			"framework migration 3 (auth) was changed after it was applied: expected checksum aa, database has bb"},
		{&db.Error{Kind: db.ErrUnknownMigration, Version: 99, Name: "x"},
			"database has framework migration 99 (x) that this version of Forgebase does not know; upgrade Forgebase"},
		{&db.Error{Kind: db.ErrFrameworkNotMigrated},
			"framework tables are missing; run the framework migrations before schema sync"},
		{&db.Error{Kind: db.ErrCreatePool, Message: "m"}, "cannot create connection pool: m"},
		{&db.Error{Kind: db.ErrPool, Message: "m"}, "cannot get a database connection: m"},
		{&db.Error{Kind: db.ErrTLS, Message: "m"}, "m"},
		{&db.Error{Kind: db.ErrSyncConflicts, Conflicts: []db.Conflict{
			{Kind: db.ConflictUnmanagedTable, Table: "doc_a"}, {Kind: db.ConflictRelationNameTaken, Name: "doc_b"}}},
			"schema sync refused, 2 conflict(s): table doc_a already exists but is not managed by Forgebase (not adopted); " +
				"cannot create table doc_b: a relation or type with that name already exists"},
		{&db.Error{Kind: db.ErrInvalidMetadata, Problems: []core.DefinitionError{
			{Kind: core.ErrUnknownLinkTarget, DocType: "a", Field: "f", Target: "t"}}},
			"invalid DocType metadata: doctype `a`: field `f` links to unknown doctype `t`"},
	} {
		if got := c.e.Error(); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
	if got := (&db.Error{Kind: db.ErrLockTimeout}).Error(); got != "timed out waiting for a database lock (another transaction holds a conflicting lock); "+
		"the statement that timed out was rolled back (earlier committed steps, such as framework migrations in `migrate`, stay applied), retry later" {
		t.Errorf("%q", got)
	}
}

// session reads the settings of the session that a URL gives.
func session(t *testing.T, url string) map[string]string {
	t.Helper()
	pool, err := db.Connect(bg, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	out := map[string]string{}
	row := pool.QueryRow(bg, "SELECT current_setting('search_path'), current_setting('TimeZone'), "+
		"current_setting('application_name'), current_user::text, current_database()::text")
	var searchPath, timeZone, appName, user, database string
	if err := row.Scan(&searchPath, &timeZone, &appName, &user, &database); err != nil {
		t.Fatal(err)
	}
	out["search_path"], out["TimeZone"], out["application_name"], out["user"], out["database"] = searchPath, timeZone, appName, user, database
	return out
}

// Not in Rust: the URL is the only source of connection settings, as in tokio-postgres, which reads
// no PG* variable, no password file and no service file. pgx reads all of them.
func TestTheURLIsTheOnlySourceOfSettings(t *testing.T) {
	d := dbtest.New(t)
	baseline := session(t, d.URL())
	if baseline["application_name"] != "" || baseline["user"] != "forgebase" || baseline["database"] == "" {
		t.Fatalf("baseline: %v", baseline)
	}

	dir := t.TempDir()
	passfile := dir + "/pgpass"
	if err := os.WriteFile(passfile, []byte("*:*:*:*:forgebase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	servicefile := dir + "/pg_service.conf"
	if err := os.WriteFile(servicefile, []byte("[svc]\nhost=203.0.113.1\nport=1\ndbname=nodb\nuser=nobody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"PGOPTIONS":            "-c search_path=nowhere",
		"PGTZ":                 "Asia/Tokyo",
		"PGAPPNAME":            "from-the-environment",
		"PGPASSWORD":           "wrong-password",
		"PGUSER":               "nobody",
		"PGHOST":               "203.0.113.1",
		"PGPORT":               "1",
		"PGDATABASE":           "nodb",
		"PGSERVICEFILE":        servicefile,
		"PGPASSFILE":           passfile,
		"PGSSLMODE":            "verify-full",
		"PGSSLROOTCERT":        dir + "/nonexistent.pem",
		"PGCONNECT_TIMEOUT":    "1",
		"PGTARGETSESSIONATTRS": "standby",
		"PGCHANNELBINDING":     "require",
		"PGREQUIREAUTH":        "password",
		"PGMINPROTOCOLVERSION": "3.2",
		"PGMAXPROTOCOLVERSION": "3.2",
		"PGSSLNEGOTIATION":     "direct",
		"PGSSLCERT":            dir + "/nonexistent.crt",
	} {
		t.Setenv(k, v)
	}
	want(t, session(t, d.URL()), baseline, "the session under a hostile environment")

	// What the URL sets, it sets.
	url := d.URL() + "?application_name=from-the-url&options=-c%20search_path%3Dmine"
	s := session(t, url)
	if s["application_name"] != "from-the-url" || s["search_path"] != "mine" || s["TimeZone"] != baseline["TimeZone"] {
		t.Errorf("%v", s)
	}

	// A URL without a password does not take one from PGPASSWORD or the password file: the server
	// refuses it (the file holds the right one).
	noPassword := strings.Replace(d.URL(), "forgebase:forgebase@", "forgebase@", 1)
	_, err := db.Connect(bg, noPassword)
	if err == nil || !strings.Contains(err.Error(), "28P01") && !strings.Contains(err.Error(), "password") {
		t.Errorf("connected without a password, or with another error: %v", err)
	}
	// A URL without a host does not take PGHOST.
	_, err = db.Connect(bg, "postgres://forgebase:forgebase@/"+baseline["database"])
	if e := asDbError(t, err); e.Kind != db.ErrPostgres || !strings.Contains(err.Error(), "both host and hostaddr are missing") {
		t.Errorf("%v", err)
	}
	// A URL without a user does not take PGUSER: it is the operating system's user, as in
	// tokio-postgres (whoami). The test server has no such role, or asks for its password: the
	// error names that user, never "nobody".
	current, uerr := osuser.Current()
	if uerr != nil {
		t.Fatal(uerr)
	}
	_, err = db.Connect(bg, strings.Replace(d.URL(), "forgebase:forgebase@", "", 1))
	if err == nil {
		t.Fatal("connected without a user")
	}
	if e := asDbError(t, err); e.Kind != db.ErrPostgres ||
		!strings.Contains(err.Error(), `for user "`+current.Username+`"`) && !strings.Contains(err.Error(), `role "`+current.Username+`"`) {
		t.Errorf("expected a server refusal naming the operating system user %q: %v", current.Username, err)
	}
	if strings.Contains(err.Error(), "nobody") {
		t.Errorf("PGUSER was used: %v", err)
	}
	// An unknown key is refused, not sent to the server as a run-time parameter.
	_, err = db.Connect(bg, d.URL()+"?search_path=x")
	if e := asDbError(t, err); e.Kind != db.ErrCreatePool || !strings.Contains(err.Error(), "unknown option `search_path`") {
		t.Errorf("%v", err)
	}
}

// Not in Rust: PGSERVICE is the one environment variable that pgx cannot be told to ignore, so a set
// one is refused (and the process environment is not touched).
func TestAPGSERVICEInTheEnvironmentIsRefused(t *testing.T) {
	t.Setenv("PGSERVICE", "svc")
	_, err := db.Connect(bg, "postgres://forgebase:forgebase@127.0.0.1:1/x")
	e := asDbError(t, err)
	if e.Kind != db.ErrCreatePool || err.Error() != "cannot create connection pool: Forgebase reads the connection settings only from the database URL; unset PGSERVICE" {
		t.Errorf("%v", err)
	}
	if os.Getenv("PGSERVICE") != "svc" {
		t.Error("the environment was changed")
	}
}
