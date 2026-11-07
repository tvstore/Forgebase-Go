package testing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	stdtesting "testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go"
	"github.com/tvstore/forgebase-go/internal/app"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/httpapi"
)

const envVar = "FORGEBASE_TEST_DATABASE_URL"

// Password is the password of every user made with [DB.CreateUser].
const Password = "correct horse battery"

func adminURL(t stdtesting.TB) string {
	t.Helper()
	url := os.Getenv(envVar)
	if url == "" {
		t.Fatalf("%[1]s is not set. forgebase/testing needs a PostgreSQL 16 server; set it to a "+
			"superuser/CREATEDB URL, for example "+
			"%[1]s=postgres://forgebase:forgebase@localhost:5432/postgres. Tests never skip.", envVar)
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

var counter atomic.Uint32

// Migrated is what [DB.Migrate] did. Read the fields.
type Migrated struct {
	// AppMigrations are the app migrations applied, in the order they ran; with a dry run, the
	// pending ones.
	AppMigrations []AppliedMigration
}

// AppliedMigration is a migration that [DB.Migrate] applied (in a dry run: would apply).
type AppliedMigration struct {
	// App is the app.
	App string
	// Version is the migration version.
	Version int32
	// Name is the migration name, from the file name 0001_name.sql.
	Name string
}

// DB is a database created for one test, and dropped when the test ends.
type DB struct {
	t        stdtesting.TB
	pool     *pgxpool.Pool
	adminURL string
	name     string
	url      string
}

// NewDB creates an empty database for the test t. Nothing is migrated yet ([DB.Migrate]). The database
// is dropped (with FORCE) when the test ends. It fails the test when FORGEBASE_TEST_DATABASE_URL is not
// set or the server cannot be used.
func NewDB(t stdtesting.TB) *DB {
	t.Helper()
	admin := adminURL(t)
	name := fmt.Sprintf("fb_test_app_%x_%x_%d", os.Getpid(), time.Now().Nanosecond(), counter.Add(1))
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("cannot connect to %s: %v", envVar, err)
	}
	_, err = conn.Exec(ctx, "CREATE DATABASE "+db.QuoteIdent(name))
	_ = conn.Close(ctx)
	if err != nil {
		t.Fatalf("create the test database: %v", err)
	}
	d := &DB{t: t, adminURL: admin, name: name, url: withDatabase(admin, name)}
	// Registered before the pool exists, so the database is dropped whatever happens next.
	t.Cleanup(d.drop)
	if d.pool, err = db.Connect(ctx, d.url); err != nil {
		t.Fatalf("connect to the test database: %v", err)
	}
	return d
}

func (d *DB) drop() {
	if d.pool != nil {
		d.pool.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, d.adminURL)
	if err != nil {
		d.t.Logf("cannot drop the test database %s: %v", d.name, err)
		return
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+db.QuoteIdent(d.name)+" WITH (FORCE)"); err != nil {
		d.t.Logf("cannot drop the test database %s: %v", d.name, err)
	}
}

// URL is the URL of this database, for code that connects on its own.
func (d *DB) URL() string { return d.url }

// Migrate runs what `forgebase migrate` runs for b: the framework migrations, the DocType schema sync,
// the app migrations in app order and then the check of the reports' SQL. With dryRun nothing is kept
// (and the reports are not checked). The error is the message
// `forgebase migrate` would print: a *forgebase.StartupError when the apps do not fit together (nothing
// was run), else a refused schema sync, a changed or unknown applied migration, a failing migration, a
// lock timeout or a database error.
func (d *DB) Migrate(b *forgebase.Binary, dryRun bool) (*Migrated, error) {
	d.t.Helper()
	composed, err := app.ComposeBinary(b)
	if err != nil {
		return nil, err
	}
	report, err := composed.Migrate(context.Background(), d.pool, db.SyncOptions{DryRun: dryRun, LockTimeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	// Like `forgebase migrate`: after the migrations, and not in a dry run.
	if !dryRun {
		problems, err := composed.CheckReports(context.Background(), d.pool)
		if err != nil {
			return nil, err
		}
		if len(problems) > 0 {
			lines := make([]string, len(problems))
			for i, p := range problems {
				lines[i] = "  - " + p
			}
			return nil, fmt.Errorf("the migrations were applied, but %d report(s) do not fit their SQL:\n%s", len(problems), strings.Join(lines, "\n"))
		}
	}
	done := &Migrated{}
	for _, m := range report.AppMigrations {
		done.AppMigrations = append(done.AppMigrations, AppliedMigration{App: m.App, Version: m.Version, Name: m.Name})
	}
	return done, nil
}

// CheckReports prepares the SQL of every report of b against d and returns one message per problem: the
// check `forgebase migrate` runs after the migrations (and [DB.Migrate] too, which fails with the same
// messages). It reads the tables the migrations create, so migrate first. An empty list means every report
// fits what it declares: its parameter and column counts, and the SQL types of its parameters and columns
// (ADR 0016).
//
//	db.Migrate(binary, false)
//	if problems := fbtest.CheckReports(binary, db); len(problems) > 0 { t.Fatal(problems) }
//
// It fails the test when the apps do not fit together or on a database error.
func CheckReports(b *forgebase.Binary, d *DB) []string {
	d.t.Helper()
	composed, err := app.ComposeBinary(b)
	if err != nil {
		d.t.Fatalf("the apps do not fit together: %v", err)
	}
	problems, err := composed.CheckReports(context.Background(), d.pool)
	if err != nil {
		d.t.Fatalf("CheckReports: %v", err)
	}
	return problems
}

// JobRun is what [RunJobs] did. Only read it.
type JobRun struct {
	// Done is the number of jobs that ended done.
	Done int
	// Retried is the number of attempts that failed and were queued again (after a delay; see [RunJobs]).
	Retried int
	// Dead is the number of jobs that ended dead.
	Dead int
}

// RunJobs runs every job of b that is due, one at a time, until none is due, with the same code the worker
// uses (claim, handler in one transaction with the completion, retry or dead). Jobs that hooks and other
// jobs enqueue while it runs are run too.
//
// A job that fails and is retried is queued again after its backoff (30 seconds by default), so it is not
// run again by this call: Retried counts it, and the test can check the row, or make it due with SQL
// (UPDATE fb_job SET run_at = now()) and call RunJobs again. Jobs of names the binary does not know are left
// alone. The worker's log lines go to standard error.
//
// It fails the test when the apps do not fit together, on a database error, and after 10 000 jobs (a job that
// enqueues itself without end).
func RunJobs(b *forgebase.Binary, d *DB) JobRun {
	d.t.Helper()
	composed, err := app.ComposeBinary(b)
	if err != nil {
		d.t.Fatalf("the apps do not fit together: %v", err)
	}
	run, err := app.RunJobs(context.Background(), composed, d.pool)
	if err != nil {
		d.t.Fatalf("RunJobs: %v", err)
	}
	return JobRun{Done: run.Done, Retried: run.Retried, Dead: run.Dead}
}

// RunSchedulesAt enqueues the job of every schedule of b that is due at at (its next_run_at is not after at)
// and returns how many jobs it enqueued. It runs the worker's own tick with at in place of the database's
// now(); [RunJobs] then runs the jobs. After a tick the schedule's next_run_at is the first time after at, so
// the same at enqueues nothing more, and a test moves time forward by passing a later at.
//
// [DB.Migrate] writes the schedules, so migrate first (a database that was not migrated has no schedule
// table). The payload of each job is {"scheduled_for": ...} with the time that was due. The built-in
// clean-up jobs are scheduled like the app's, so a due built-in job is enqueued too and counted.
//
//	db.Migrate(binary, false)
//	fbtest.RunSchedulesAt(binary, db, time.Now().Add(24*time.Hour))
//	fbtest.RunJobs(binary, db)
//
// It fails the test when the apps do not fit together or on a database error (for example, before Migrate).
func RunSchedulesAt(b *forgebase.Binary, d *DB, at time.Time) int {
	d.t.Helper()
	composed, err := app.ComposeBinary(b)
	if err != nil {
		d.t.Fatalf("the apps do not fit together: %v", err)
	}
	n, err := app.RunSchedulesAt(context.Background(), composed, d.pool, at)
	if err != nil {
		d.t.Fatalf("RunSchedulesAt: %v", err)
	}
	return n
}

// Exec runs SQL (several statements are fine). It fails the test when the SQL fails.
func (d *DB) Exec(sql string) {
	d.t.Helper()
	if _, err := d.pool.Exec(context.Background(), sql); err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
}

// Rows runs a query and returns its rows as text (every column must be text: cast with ::text in the
// SQL, and NULL is the string "NULL"). It fails the test when the SQL fails.
func (d *DB) Rows(sql string) [][]string {
	d.t.Helper()
	rows, err := d.pool.Query(context.Background(), sql, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		values := rows.RawValues()
		row := make([]string, len(values))
		for i, v := range values {
			if v == nil {
				row[i] = "NULL"
			} else {
				row[i] = string(v)
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		d.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

// CreateUser creates an enabled user with roles and the password [Password]. Needs the framework tables
// ([DB.Migrate] first).
func (d *DB) CreateUser(username string, roles ...string) {
	d.t.Helper()
	if _, err := db.CreateUser(context.Background(), d.pool, username, Password, roles); err != nil {
		d.t.Fatalf("create the user: %v", err)
	}
}

// CreateToken creates an API token for an existing user and returns it as key:secret for
// [Client.WithToken]. Needs the framework tables ([DB.Migrate] first).
func (d *DB) CreateToken(username string) string {
	d.t.Helper()
	token, err := db.CreateAPIToken(context.Background(), d.pool, username)
	if err != nil {
		d.t.Fatalf("create the token: %v", err)
	}
	return token.Key + ":" + token.Secret
}

// Client is an HTTP client for the API of b on this database, not logged in. The database must have
// been migrated. It fails the test when the apps of b do not fit together ([forgebase.Binary.Check]).
func (d *DB) Client(b *forgebase.Binary) *Client {
	d.t.Helper()
	composed, err := app.ComposeBinary(b)
	if err != nil {
		d.t.Fatalf("the apps do not fit together: %v", err)
	}
	state := httpapi.NewState(d.pool, composed.Registry(), httpapi.DefaultConfig())
	return &Client{t: d.t, handler: httpapi.ImplicitSites(state)}
}

// Reply is what a request got back.
type Reply struct {
	// Status is the HTTP status code.
	Status int
	// Body is the decoded JSON body (a map[string]any for the envelope {"data": ...} or {"error":
	// ...}); nil when the reply has no body (204) or it is not JSON.
	Body any
	// Text is the body as it was sent.
	Text   string
	header http.Header
}

// Header is the value of a response header (name in any case), for example Retry-After; "" when absent.
func (r *Reply) Header(name string) string { return r.header.Get(name) }

func (r *Reply) member(key string) any {
	if body, ok := r.Body.(map[string]any); ok {
		return body[key]
	}
	return nil
}

// Data is "data" of a success reply: a map[string]any, a []any or a scalar; nil when there is none.
func (r *Reply) Data() any { return r.member("data") }

// DataObject is Data as a JSON object, or nil when it is not one.
func (r *Reply) DataObject() map[string]any {
	v, _ := r.Data().(map[string]any)
	return v
}

// DataList is Data as a JSON array, or nil when it is not one.
func (r *Reply) DataList() []any {
	v, _ := r.Data().([]any)
	return v
}

// ErrorCode is "error.code" of an error reply, "" when there is none.
func (r *Reply) ErrorCode() string { return r.errorString("code") }

// ErrorMessage is "error.message" of an error reply, "" when there is none.
func (r *Reply) ErrorMessage() string { return r.errorString("message") }

func (r *Reply) errorString(key string) string {
	if e, ok := r.member("error").(map[string]any); ok {
		s, _ := e[key].(string)
		return s
	}
	return ""
}

type session struct {
	cookie string
	// csrf is "" for a client that leaves the CSRF header out ([Client.WithoutCSRF]).
	csrf string
}

// Client sends requests straight into the HTTP API (no socket). Paths are the full ones
// (/api/v1/resource/customer). After [Client.Login] the session cookie and the CSRF token are sent with
// every request. A Client is not changed by the methods that return another one.
type Client struct {
	t       stdtesting.TB
	handler http.Handler
	session *session
	// token is key:secret of an API token: sent as "Authorization: token ..." instead of the cookie.
	token string
}

// Login logs in as username (a user made with [DB.CreateUser]) and returns a client that carries the
// session. It fails the test when the login fails.
func (c *Client) Login(username string) *Client {
	c.t.Helper()
	reply, cookie := c.send(http.MethodPost, "/api/v1/login", "application/json",
		mustJSON(c.t, map[string]any{"username": username, "password": Password}), nil)
	if reply.Status != http.StatusOK {
		c.t.Fatalf("login of %s failed: %d %s", username, reply.Status, reply.Text)
	}
	if cookie == "" {
		c.t.Fatalf("the login of %s did not set the session cookie", username)
	}
	csrf, _ := reply.DataObject()["csrf_token"].(string)
	if csrf == "" {
		c.t.Fatalf("the login of %s did not return a CSRF token", username)
	}
	return &Client{t: c.t, handler: c.handler, session: &session{cookie: cookie, csrf: csrf}}
}

// WithToken is a client that authenticates with the API token keySecret (from [DB.CreateToken]) instead
// of a session: no cookie and no CSRF header.
func (c *Client) WithToken(keySecret string) *Client {
	return &Client{t: c.t, handler: c.handler, token: keySecret}
}

// WithoutCSRF is this client's session, but the requests leave out the CSRF header (to test that a cookie
// call without it is refused). The same client when it has no session.
func (c *Client) WithoutCSRF() *Client {
	out := *c
	if c.session != nil {
		out.session = &session{cookie: c.session.cookie}
	}
	return &out
}

// Get is GET path.
func (c *Client) Get(path string) *Reply {
	c.t.Helper()
	return c.json(http.MethodGet, path, nil)
}

// Post is POST path with body encoded as JSON.
func (c *Client) Post(path string, body any) *Reply {
	c.t.Helper()
	return c.json(http.MethodPost, path, body)
}

// Patch is PATCH path with body encoded as JSON.
func (c *Client) Patch(path string, body any) *Reply {
	c.t.Helper()
	return c.json(http.MethodPatch, path, body)
}

// Delete is DELETE path.
func (c *Client) Delete(path string) *Reply {
	c.t.Helper()
	return c.json(http.MethodDelete, path, nil)
}

// PostRaw is POST path with body as it is (not necessarily JSON) and this Content-Type, or none when
// contentType is empty.
func (c *Client) PostRaw(path, contentType string, body []byte) *Reply {
	c.t.Helper()
	reply, _ := c.send(http.MethodPost, path, contentType, body, c.session)
	return reply
}

func (c *Client) json(method, path string, body any) *Reply {
	c.t.Helper()
	var data []byte
	contentType := ""
	if body != nil {
		data, contentType = mustJSON(c.t, body), "application/json"
	}
	reply, _ := c.send(method, path, contentType, data, c.session)
	return reply
}

func mustJSON(t stdtesting.TB, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("cannot encode the request body as JSON: %v", err)
	}
	return data
}

// send sends a request; it returns the reply and the value of the session cookie it set, if any.
func (c *Client) send(method, target, contentType string, body []byte, s *session) (*Reply, string) {
	c.t.Helper()
	var reader io.Reader = http.NoBody
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Host = "localhost"
	switch {
	case c.token != "":
		req.Header.Set("Authorization", "token "+c.token)
	case s != nil:
		req.Header.Set("Cookie", "fb_session="+s.cookie)
		if s.csrf != "" {
			req.Header.Set("X-Forgebase-CSRF", s.csrf)
		}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatalf("cannot read the reply: %v", err)
	}
	cookie := ""
	for _, set := range res.Header.Values("Set-Cookie") {
		if rest, ok := strings.CutPrefix(set, "fb_session="); ok {
			cookie, _, _ = strings.Cut(rest, ";")
			break
		}
	}
	reply := &Reply{Status: res.StatusCode, Text: string(data), header: res.Header}
	if len(data) > 0 {
		var decoded any
		if json.Unmarshal(data, &decoded) == nil {
			reply.Body = decoded
		}
	}
	return reply, cookie
}

// AssertDocTypesListed fails the test when the DocType files in the directory dir and the DocType files
// embedded in a are not the same set: a file in the directory that the app does not embed (it would
// silently be missing from the binary), or an embedded file that is not in the directory.
//
// dir is the directory with the DocType files on disk, relative to the test's working directory (the
// directory of the package under test), written as the path that the embed directive uses: with
// //go:embed doctypes/*.toml, dir is "doctypes". Paths are compared in full ("doctypes/customer.toml"),
// so a file embedded from another directory does not count. A leading ./ and a trailing / are ignored.
//
//	func TestEveryDocTypeFileIsEmbedded(t *testing.T) {
//		fbtest.AssertDocTypesListed(t, sales.App(), "doctypes")
//	}
//
// The same file rules as the app's DocTypes method apply: the directory is read **recursively** (every
// *.toml at any depth, names that start with a dot ignored). This differs from the Rust guard, which compared
// only the files directly in the directory.
//
// dir must be the embed path of the files: the comparison is of dir + "/" + the file's path under dir with the
// paths the app's DocTypes saw. So an app that passes the embed.FS itself works with dir "doctypes", and an app
// that passes a sub-tree, DocTypes(fs.Sub(files, "doctypes")), has embedded paths without the prefix
// ("customer.toml") and can only be checked with dir "." from a test whose working directory is the
// doctypes directory. Prefer passing the embed.FS itself:
//
//	//go:embed doctypes/*.toml
//	var files embed.FS
//
//	forgebase.NewApp("sales").DocTypes(files)   // fbtest.AssertDocTypesListed(t, app, "doctypes")
//
//	sub, _ := fs.Sub(files, "doctypes")
//	forgebase.NewApp("sales").DocTypes(sub)     // only: AssertDocTypesListed(t, app, ".") from inside doctypes/
func AssertDocTypesListed(t stdtesting.TB, a *forgebase.App, dir string) {
	t.Helper()
	clean := path.Clean(filepath.ToSlash(dir))
	info, err := os.Stat(filepath.FromSlash(clean))
	if err != nil || !info.IsDir() {
		reason := "it is not a directory"
		if err != nil {
			reason = err.Error()
		}
		t.Fatalf("cannot read DocType directory %s: %s", dir, reason)
	}
	found, problems := app.FilePaths(os.DirFS(filepath.FromSlash(clean)), ".toml")
	if len(problems) > 0 {
		t.Fatalf("DocType directory %s: %v", dir, problems)
	}
	onDisk := map[string]bool{}
	for _, p := range found {
		if clean != "." {
			p = clean + "/" + p
		}
		onDisk[p] = true
	}
	listed := map[string]bool{}
	for _, p := range app.DocTypePaths(a) {
		listed[path.Clean(p)] = true
	}
	var notListed, notFound []string
	for p := range onDisk {
		if !listed[p] {
			notListed = append(notListed, p)
		}
	}
	for p := range listed {
		if !onDisk[p] {
			notFound = append(notFound, p)
		}
	}
	if len(notListed) == 0 && len(notFound) == 0 {
		return
	}
	sort.Strings(notListed)
	sort.Strings(notFound)
	t.Fatalf("app `%s`: the embedded DocType set and %s differ; in the directory but not embedded: %q; embedded but not in the directory: %q",
		a.Name(), dir, notListed, notFound)
}
