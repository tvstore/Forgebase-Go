package app

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// The site commands: list and create (M7.2, ADR 0014), against real PostgreSQL databases, run in process.
// Ported from the scenarios config_and_site_list, site_create and follow_ups of
// crates/forgebase/tests/site_apps.rs. The backup and the restore, which run the real tools and kill a
// real process, are in cmd/forgebase/backup_test.go.

const (
	listAURL = "FB_TEST_LIST_A_URL"
	listBURL = "FB_TEST_LIST_B_URL"
)

// Rust: site_apps.rs::config_and_site_list (the apps key is validated with the other config problems, and
// `site list` prints the sites without touching a database)
func TestSiteListPrintsTheSitesWithoutADatabase(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "forgebase.toml")
	site := func(apps string) string {
		return fmt.Sprintf("[[sites]]\nname = \"a\"\nhosts = [\"a.localhost\"]\ndatabase_url_env = \"%s\"\n%s\n", listAURL, apps)
	}
	const secret = "postgres://user:hunter2@127.0.0.1:1/never_connected"
	list := func(content string, args ...string) result {
		if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return runApps(siteApps(), map[string]string{listAURL: secret}, "",
			append([]string{"--config", config, "site", "list"}, args...)...)
	}

	// Problems, all at once, each naming the site.
	r := list(site(`apps = ["extra", "nope", "base", "base"]`))
	expectCode(t, r, 1, "problems")
	expectContains(t, r.stderr, "site `a`: unknown app `nope` in `apps`", "site `a`: app `base` is listed twice in `apps`")
	r = list(site(`apps = ["extra"]`))
	expectCode(t, r, 1, "a dependency that is not listed")
	expectContains(t, r.stderr, "site `a`: app `extra` needs `base`, which is not in `apps`")
	// A host that could never match a request is refused when the file is loaded.
	r = list(strings.Replace(site(""), "a.localhost", "a/b.localhost", 1))
	expectCode(t, r, 1, "a bad host")
	expectContains(t, r.stderr, `host "a/b.localhost"`)

	// Valid: absent means all, an empty list means none, a closed subset is fine.
	r = list(site("") + fmt.Sprintf("\n[[sites]]\nname = \"b\"\nhosts = [\"b.localhost\", \"b2.localhost\"]\ndatabase_url_env = \"%s\"\napps = []\n\n"+
		"[[sites]]\nname = \"c\"\nhosts = [\"c.localhost\"]\ndatabase_url_env = \"C_URL\"\napps = [\"extra\", \"base\"]\n", listBURL))
	expectCode(t, r, 0, "list")
	want := strings.Join([]string{
		"SITE  HOSTS                      APPS         DATABASE_URL_ENV",
		"a     a.localhost                all          " + listAURL,
		"b     b.localhost, b2.localhost  (none)       " + listBURL,
		"c     c.localhost                extra, base  C_URL",
	}, "\n") + "\n"
	if r.stdout != want {
		t.Errorf("got:\n%s\nwant:\n%s", r.stdout, want)
	}
	if strings.Contains(r.stdout, "hunter2") || strings.Contains(r.stdout, "never_connected") || r.stderr != "" {
		t.Errorf("%+v", r)
	}

	// Without a config file: the implicit site, from the environment, no database access.
	r = runApps(siteApps(), map[string]string{"FORGEBASE_DATABASE_URL": secret}, "", "site", "list")
	expectCode(t, r, 0, "implicit")
	if !strings.HasPrefix(r.stdout, "SITE") || !strings.Contains(r.stdout, "default") || !strings.Contains(r.stdout, "localhost, 127.0.0.1") ||
		!strings.Contains(r.stdout, "all") || !strings.Contains(r.stdout, "FORGEBASE_DATABASE_URL") || strings.Contains(r.stdout, "hunter2") {
		t.Errorf("%q", r.stdout)
	}
	// The implicit site answers to FORGEBASE_ALLOWED_HOSTS (blanks and empty entries dropped).
	r = runApps(siteApps(), map[string]string{"FORGEBASE_ALLOWED_HOSTS": " a.test ,, b.test "}, "", "site", "list")
	expectCode(t, r, 0, "implicit with hosts")
	if want := "SITE     HOSTS           APPS  DATABASE_URL_ENV\ndefault  a.test, b.test  all   FORGEBASE_DATABASE_URL\n"; r.stdout != want {
		t.Errorf("%q", r.stdout)
	}
	// `--site` is not used by `site` commands: a usage error (exit code 2).
	r = runApps(siteApps(), nil, "", "site", "list", "--site", "default")
	expectCode(t, r, 2, "--site")
	expectContains(t, r.stderr, "do not use --site")
	// An unknown site is a usage error too, whatever the command.
	r = runApps(siteApps(), nil, "", "site", "create", "nope", "--empty")
	expectCode(t, r, 2, "unknown site")
	expectContains(t, r.stderr, "unknown site `nope`; without a config file (--config) the only site is `default`")
}

var scratchCounter atomic.Uint32

// scratchDB is a database name that does not exist, made on the server of an admin database (a test
// database, which gives the server and drops what the test made).
type scratchDB struct {
	t     *testing.T
	admin *dbtest.DB
	name  string
	url   string
}

func newScratchDB(t *testing.T) *scratchDB {
	t.Helper()
	admin := dbtest.New(t)
	name := fmt.Sprintf("fb_test_create_%x_%d", os.Getpid(), scratchCounter.Add(1))
	s := &scratchDB{t: t, admin: admin, name: name, url: dbtest.URLForDatabase(t, name)}
	t.Cleanup(func() { _ = admin.TryExec("DROP DATABASE IF EXISTS " + db.QuoteIdent(name) + " WITH (FORCE)") })
	return s
}

func (s *scratchDB) exists() bool {
	return s.admin.Rows("SELECT count(*)::text FROM pg_database WHERE datname = '" + s.name + "'")[0][0] == "1"
}

// migrations is the count of fb_migration rows of the created database.
func (s *scratchDB) migrations() string {
	s.t.Helper()
	pool, err := db.Connect(context.Background(), s.url)
	if err != nil {
		s.t.Fatal(err)
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM fb_migration").Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return fmt.Sprint(n)
}

// Rust: site_apps.rs::site_create (`site create` makes the database and migrates it; it refuses an
// existing database and an unusable name, and never prints the URL)
func TestSiteCreateMakesTheDatabaseAndMigratesIt(t *testing.T) {
	s := newScratchDB(t)
	dir := t.TempDir()
	config := filepath.Join(dir, "forgebase.toml")
	text := "[[sites]]\nname = \"fresh\"\nhosts = [\"fresh.localhost\"]\ndatabase_url_env = \"FB_TEST_NEW_URL\"\napps = [\"base\"]\n"
	if err := os.WriteFile(config, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	create := func(url string, args ...string) result {
		return runApps(siteApps(), map[string]string{"FB_TEST_NEW_URL": url}, "",
			append([]string{"--config", config, "site", "create", "fresh"}, args...)...)
	}

	// Not a site of the config file: a usage error, nothing is created.
	r := runApps(siteApps(), map[string]string{"FB_TEST_NEW_URL": s.url}, "", "--config", config, "site", "create", "nope")
	expectCode(t, r, 2, "unknown site")
	expectContains(t, r.stderr, "unknown site `nope`")
	if s.exists() {
		t.Fatal("a database was created for an unknown site")
	}

	// Created and migrated; the maintenance database can be named (here: the admin database).
	r = create(s.url, "--maintenance-db", s.admin.Name())
	expectCode(t, r, 0, "create")
	if !strings.HasPrefix(r.stdout, "created the database of site `fresh`\n") ||
		!strings.Contains(r.stdout, fmt.Sprintf("framework migrations: %d applied", len(db.Migrations))) ||
		!strings.Contains(r.stdout, "doc_item") || strings.Contains(r.stdout, "doc_extra_item") {
		t.Errorf("%s", r.stdout)
	}
	if !s.exists() || s.migrations() != fmt.Sprint(len(db.Migrations)) {
		t.Errorf("exists %v, %s migrations", s.exists(), s.migrations())
	}

	// A second create is refused, and the database is left alone.
	r = create(s.url)
	expectCode(t, r, 1, "second create")
	if r.stderr != "error: database for site `fresh` already exists; run `forgebase migrate --site fresh`\n" || r.stdout != "" {
		t.Errorf("%+v", r)
	}
	if s.migrations() != fmt.Sprint(len(db.Migrations)) {
		t.Error("the database was changed")
	}

	// An unusable database name in the URL is refused before anything is created; the message has neither
	// the URL nor the password.
	for _, bad := range []string{"bad%3Bname", "bad%20name", "x%22y"} {
		url := dbtest.URLForDatabase(t, bad)
		r = create(url)
		expectCode(t, r, 1, bad)
		expectContains(t, r.stderr, "site `fresh`", "must be 1 to 63 characters")
		if strings.Contains(r.stderr, "forgebase:forgebase") || strings.Contains(r.stderr, url) {
			t.Errorf("%s", r.stderr)
		}
	}
	// A URL that is not a URL, and a maintenance database that does not exist.
	r = create("not a url ::")
	expectCode(t, r, 1, "not a url")
	expectContains(t, r.stderr, "site `fresh`")
	if strings.Contains(r.stderr, "not a url") {
		t.Errorf("%s", r.stderr)
	}
	fresh := newScratchDB(t)
	r = create(fresh.url, "--maintenance-db", "fb_no_such_maintenance_db")
	expectCode(t, r, 1, "missing maintenance database")
	expectContains(t, r.stderr, "site `fresh`")
	if fresh.exists() || strings.Contains(r.stderr, fresh.url) {
		t.Errorf("exists %v: %s", fresh.exists(), r.stderr)
	}
}

// Rust: sites.rs / site_apps.rs::site_create (--empty): the database is created and not migrated, and needs
// neither apps nor --doctypes.
func TestSiteCreateEmptyCreatesAnUnmigratedDatabase(t *testing.T) {
	s := newScratchDB(t)
	r := runCLI(map[string]string{"FORGEBASE_DATABASE_URL": s.url}, "", "site", "create", "default", "--empty")
	expectCode(t, r, 0, "create --empty")
	if r.stdout != "created the database of site `default`\n"+
		"the database is empty; restore into it with `forgebase site restore default --from FILE`\n" {
		t.Errorf("%q", r.stdout)
	}
	if !s.exists() {
		t.Fatal("no database")
	}
	if got := s.admin.Rows("SELECT count(*)::text FROM pg_database WHERE datname = '" + s.name + "'"); got[0][0] != "1" {
		t.Error(got)
	}
	info, err := db.InspectDatabase(context.Background(), s.url)
	if err != nil || !info.IsEmpty {
		t.Errorf("--empty does not migrate: %+v %v", info, err)
	}
	// Without --empty the stock binary needs --doctypes.
	r = runCLI(map[string]string{"FORGEBASE_DATABASE_URL": s.url}, "", "site", "create", "default")
	expectCode(t, r, 2, "no --doctypes")
	expectContains(t, r.stderr, "--doctypes <DIR>")
	// A site create with DocTypes migrates them, and a migrate that fails keeps the database.
	other := newScratchDB(t)
	r = runCLI(map[string]string{"FORGEBASE_DATABASE_URL": other.url}, "", "site", "create", "default", "--doctypes", doctypes)
	expectCode(t, r, 0, "create with doctypes")
	expectContains(t, r.stdout, "created the database of site `default`\n", "doc_customer")
}

// Rust: site_apps.rs::follow_ups (the follow-ups of the M7.1 review)
func TestFollowUpsOfTheSitesReview(t *testing.T) {
	a := dbtest.New(t)
	dir := t.TempDir()
	// The database of site b points nowhere (port 1 refuses at once).
	const dead = "postgres://nobody:s3cret@127.0.0.1:1/none"
	config := filepath.Join(dir, "forgebase.toml")
	text := fmt.Sprintf("[[sites]]\nname = \"a\"\nhosts = [\"a.localhost\"]\ndatabase_url_env = \"%s\"\n\n"+
		"[[sites]]\nname = \"b\"\nhosts = [\"b.localhost\"]\ndatabase_url_env = \"%s\"\n", aURL, bURL)
	if err := os.WriteFile(config, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(vars map[string]string, stdin string, args ...string) result {
		return runApps(siteApps(), vars, stdin, append([]string{"--config", config}, args...)...)
	}
	vars := map[string]string{aURL: a.URL(), bURL: dead}

	// A payload that is not JSON (given as an argument or on standard input) is reported before the
	// database is opened, so the unreachable URL does not matter.
	for _, c := range []struct {
		args  []string
		stdin string
	}{
		{[]string{"job", "enqueue", "base.tick", "--site", "b", "--payload", "{"}, ""},
		{[]string{"job", "enqueue", "base.tick", "--site", "b", "--payload", "-"}, "{not json"},
	} {
		r := run(vars, c.stdin, c.args...)
		expectCode(t, r, 1, strings.Join(c.args, " "))
		expectContains(t, r.stderr, "not valid JSON")
		if strings.Contains(r.stderr, "s3cret") || strings.Contains(r.stderr, "connect") {
			t.Errorf("%s", r.stderr)
		}
	}
	// A valid payload then does reach the database: the error is the connection, with the site, and never
	// the URL.
	r := run(vars, "", "job", "enqueue", "base.tick", "--site", "b")
	expectCode(t, r, 1, "dead site")
	if !strings.HasPrefix(r.stderr, "error: site `b`: ") || strings.Contains(r.stderr, "s3cret") || strings.Contains(r.stderr, dead) {
		t.Errorf("%s", r.stderr)
	}

	// serve composes every site's registry before it opens a pool. Site a (all apps) cannot connect
	// either, but site b (only base) cannot compose the local DocType that links to extra_item: that is
	// the error, and no connection is tried.
	doctypesDir := filepath.Join(dir, "doctypes")
	if err := os.MkdirAll(doctypesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	note := "name = \"local_note\"\nlabel = \"Local note\"\n[[fields]]\nname = \"item\"\ntype = \"link\"\ntarget = \"extra_item\"\n"
	if err := os.WriteFile(filepath.Join(doctypesDir, "local_note.toml"), []byte(note), 0o600); err != nil {
		t.Fatal(err)
	}
	narrow := fmt.Sprintf("[[sites]]\nname = \"a\"\nhosts = [\"a.localhost\"]\ndatabase_url_env = \"%s\"\n\n"+
		"[[sites]]\nname = \"b\"\nhosts = [\"b.localhost\"]\ndatabase_url_env = \"%s\"\napps = [\"base\"]\n", aURL, bURL)
	if err := os.WriteFile(config, []byte(narrow), 0o600); err != nil {
		t.Fatal(err)
	}
	r = run(map[string]string{aURL: dead, bURL: dead}, "", "serve", "--doctypes", doctypesDir)
	expectCode(t, r, 1, "serve compose")
	if !strings.HasPrefix(r.stderr, "error: ") || !strings.Contains(r.stderr, "site `b`") || !strings.Contains(r.stderr, "extra_item") ||
		strings.Contains(r.stderr, "connect") || strings.Contains(r.stderr, "s3cret") {
		t.Errorf("%s", r.stderr)
	}

	// serve with a site that cannot connect says which one, and listens on nothing.
	if err := os.WriteFile(config, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	r = run(vars, "", "serve")
	expectCode(t, r, 1, "serve dead site")
	if !strings.HasPrefix(r.stderr, "error: site `b`: ") || strings.Contains(r.stderr, "s3cret") || r.stdout != "" {
		t.Errorf("%+v", r)
	}
}

// Rust: database.rs (create_database: `Some("")` is sent as it is). An explicit empty --maintenance-db is not the
// default `postgres`: the server takes the user name as the database name. The outcome is therefore that of
// naming the user's own database (it exists on some servers, as with the postgres image, and not on others).
func TestAnExplicitEmptyMaintenanceDatabaseIsTheUsersOwn(t *testing.T) {
	admin, err := neturl.Parse(os.Getenv("FORGEBASE_TEST_DATABASE_URL"))
	if err != nil || admin.User == nil {
		t.Fatalf("the test database URL has no user: %v", err)
	}
	user := admin.User.Username()
	outcome := func(maintenance string) (int, bool) {
		s := newScratchDB(t)
		r := runCLI(map[string]string{"FORGEBASE_DATABASE_URL": s.url}, "", "site", "create", "default", "--empty", "--maintenance-db", maintenance)
		if r.code != 0 && !strings.HasPrefix(r.stderr, "error: site `default`: ") {
			t.Errorf("%q: %s", maintenance, r.stderr)
		}
		return r.code, s.exists()
	}
	code1, exists1 := outcome("")
	code2, exists2 := outcome(user)
	if code1 != code2 || exists1 != exists2 {
		t.Errorf("empty: %d %v, the user's database: %d %v", code1, exists1, code2, exists2)
	}
}
