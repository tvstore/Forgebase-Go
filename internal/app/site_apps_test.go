package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
	"github.com/tvstore/forgebase-go/internal/httpapi"
)

// Apps per site (M7.2, ADR 0014): a binary with two apps, base and extra (which depends on base), run with a
// config file of sites that install different apps, against real PostgreSQL databases. Ported from the
// scenarios migrate_per_site and cli_per_site of crates/forgebase/tests/site_apps.rs, and from the
// config validation of config_and_site_list that needs only the apps of the binary; the jobs and schedules
// of the apps are part of them since P5 (the worker of a site is in site_workers_test.go of the root
// package, which runs the apps as a real process). The site list, site create and follow_ups scenarios are in
// site_cmd_test.go (P5.2). The fixtures of extra (M9.4) are part of migrate_per_site and of
// the removed-app test since P7.2. http_per_site is ported below, the method assertions (P7.1)
// included; it builds the server's per-site states in the test (what serve does with siteComposeds and
// routeSites) and sends requests into the routing handler, with no socket.

const (
	siteItem = "name = \"item\"\nlabel = \"Item\"\nnaming = { field = \"code\" }\n" +
		"[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n"
	siteExtraItem = "name = \"extra_item\"\nlabel = \"Extra item\"\nnaming = { field = \"code\" }\n" +
		"[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n"
)

func noop(context.Context, *JobContext) error { return nil }

// stamp is extra.stamp: it inserts an extra_item with the code of its payload.
func stamp(ctx context.Context, job *JobContext) error {
	var p struct {
		Code string `json:"code"`
	}
	code := "none"
	if err := job.DecodePayload(&p); err == nil && p.Code != "" {
		code = p.Code
	}
	_, err := job.Documents().Insert(ctx, "extra_item", core.Document{Values: map[string]core.Value{"code": core.Text(code)}})
	return err
}

// hello is the method <app>.hello of the test apps: any user may call it.
func hello(context.Context, *MethodContext) (any, error) { return map[string]bool{"hello": true}, nil }

func baseApp() *App {
	return NewApp("base").Version("1.0.0").
		DocTypes(files("doctypes/item.toml", siteItem)).
		Method("hello", NewMethod(hello).AnyUser()).
		Job("tick", noop).
		Schedule("tick", DailyAt(4, 0))
}

func extraApp() *App {
	return NewApp("extra").DependsOn("base").
		DocTypes(files("doctypes/extra_item.toml", siteExtraItem)).
		Method("hello", NewMethod(hello).AnyUser()).
		Migrations(fstest.MapFS{"migrations/0001_seed.sql": {Data: []byte("CREATE TABLE extra_seed (entry text)")}}).
		Fixtures(fstest.MapFS{"fixtures/extra_item.json": {Data: []byte(`{"doctype": "extra_item", "records": [{"code": "fixture-1"}]}`)}}).
		Job("stamp", stamp).
		Job("mark", noop).
		Schedule("mark", EveryMinutes(60))
}

const (
	siteSchedules = `SELECT name FROM fb_schedule ORDER BY name COLLATE "C"`
)

// withBuiltin is the names of the schedules of the apps and the built-in ones, in the order of the query.
func withBuiltin(own ...string) []string {
	all := append(append([]string{}, own...), "forgebase.purge_jobs", "forgebase.purge_login_failures", "forgebase.purge_sessions")
	sort.Strings(all)
	return all
}

func names(d *dbtest.DB, sql string) []string {
	var out []string
	for _, r := range d.Rows(sql) {
		out = append(out, r[0])
	}
	return out
}

func siteApps() []*App { return []*App{baseApp(), extraApp()} }

// runApps runs a command line of a binary with apps.
func runApps(apps []*App, vars map[string]string, stdin string, args ...string) result {
	var out, errOut bytes.Buffer
	code := Run(Options{
		Version: "9.9.9",
		Apps:    apps,
		Args:    args,
		Getenv:  func(name string) string { return vars[name] },
		Stdin:   strings.NewReader(stdin),
		Stdout:  &out,
		Stderr:  &errOut,
	})
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// appPair is two sites, a and b, each with its own database; a installs only base, b installs both.
type appPair struct {
	t    *testing.T
	a, b *dbtest.DB
	dir  string
}

func newAppPair(t *testing.T, aApps string) *appPair {
	t.Helper()
	p := &appPair{t: t, a: dbtest.New(t), b: dbtest.New(t), dir: t.TempDir()}
	p.config(aApps)
	return p
}

// config writes the config file; aApps is the apps line of site a.
func (p *appPair) config(aApps string) {
	p.t.Helper()
	text := fmt.Sprintf("[[sites]]\nname = \"a\"\nhosts = [\"a.localhost\"]\ndatabase_url_env = \"%s\"\n%s\n\n"+
		"[[sites]]\nname = \"b\"\nhosts = [\"b.localhost\"]\ndatabase_url_env = \"%s\"\napps = [\"base\", \"extra\"]\n", aURL, aApps, bURL)
	if err := os.WriteFile(filepath.Join(p.dir, "forgebase.toml"), []byte(text), 0o600); err != nil {
		p.t.Fatal(err)
	}
}

func (p *appPair) run(args ...string) result {
	vars := map[string]string{aURL: p.a.URL(), bURL: p.b.URL()}
	return runApps(siteApps(), vars, "", append([]string{"--config", filepath.Join(p.dir, "forgebase.toml")}, args...)...)
}

// Rust: site_apps.rs::migrate_per_site
func TestMigrateInstallsOnlyTheAppsOfTheSite(t *testing.T) {
	p := newAppPair(t, `apps = ["base"]`)
	r := p.run("migrate", "--site", "a")
	expectCode(t, r, 0, "migrate a")
	// Site a has no app migrations to run: it installs base only, which has none.
	expectContains(t, r.stdout, "app migrations: up to date\n")
	if strings.Contains(r.stdout, "fixtures:") {
		t.Errorf("site a: %s", r.stdout)
	}
	if scalar(t, p.a, "SELECT string_agg(name, ',' ORDER BY name) FROM fb_doctype") != "item" {
		t.Errorf("site a has %v", p.a.Rows("SELECT name FROM fb_doctype"))
	}
	if scalar(t, p.a, "SELECT coalesce(to_regclass('extra_seed')::text, 'none')") != "none" {
		t.Error("the migration of an app the site does not install ran on site a")
	}
	if got := p.a.Rows("SELECT name, coalesce(version, 'NULL') FROM fb_app ORDER BY name"); len(got) != 1 || got[0][0] != "base" || got[0][1] != "1.0.0" {
		t.Errorf("site a records %v", got)
	}

	r = p.run("migrate", "--site", "b")
	expectCode(t, r, 0, "migrate b")
	expectContains(t, r.stdout, "app migrations: 1 applied\n  extra 0001 seed\n")
	if scalar(t, p.b, "SELECT string_agg(name, ',' ORDER BY name) FROM fb_doctype") != "extra_item,item" {
		t.Errorf("site b has %v", p.b.Rows("SELECT name FROM fb_doctype"))
	}
	if scalar(t, p.b, "SELECT to_regclass('extra_seed')::text") != "extra_seed" {
		t.Error("the migration of extra did not run on site b")
	}
	if got := p.b.Rows("SELECT name FROM fb_app ORDER BY name"); len(got) != 2 {
		t.Errorf("site b records %v", got)
	}
	// Fixtures (M9.4): an app that is not installed on a site gets none there.
	expectContains(t, r.stdout, "fixtures: extra: 1 inserted, 0 updated, 0 kept, 0 unchanged\n")
	if scalar(t, p.b, "SELECT name FROM doc_extra_item") != "fixture-1" {
		t.Error("the fixture of extra was not applied on site b")
	}
	if scalar(t, p.a, "SELECT to_regclass('fb_fixture')::text") != "fb_fixture" ||
		scalar(t, p.a, "SELECT count(*)::text FROM fb_fixture") != "0" || scalar(t, p.b, "SELECT count(*)::text FROM fb_fixture") != "1" {
		t.Error("fb_fixture")
	}
	// The schedules are those of the apps the site installs, and the built-in ones.
	if got := names(p.a, siteSchedules); !reflect.DeepEqual(got, withBuiltin("base.tick")) {
		t.Errorf("site a schedules %v", got)
	}
	if got := names(p.b, siteSchedules); !reflect.DeepEqual(got, withBuiltin("base.tick", "extra.mark")) {
		t.Errorf("site b schedules %v", got)
	}
	// Migrating again changes nothing and warns about nothing.
	again := p.run("migrate", "--site", "b")
	expectCode(t, again, 0, "migrate b again")
	if again.stderr != "" || strings.Contains(again.stdout, "schedules:") {
		t.Errorf("%+v", again)
	}
}

// Rust: site_apps.rs::removed_app (the warning; the data kept by the migrate is checked above)
func TestMigrateWarnsAboutAnAppRecordedButNotInstalled(t *testing.T) {
	// Site a had every app, then the config narrows it to base.
	p := newAppPair(t, ``)
	// Without an apps line site a has every app of the binary.
	expectCode(t, p.run("migrate", "--site", "a"), 0, "migrate a with every app")
	if scalar(t, p.a, "SELECT to_regclass('extra_seed')::text") != "extra_seed" {
		t.Fatal("extra was not installed")
	}
	p.config(`apps = ["base"]`)
	r := p.run("migrate", "--site", "a")
	expectCode(t, r, 0, "migrate a with base only")
	want := "warning: site `a`: app `extra` is recorded in the database but not installed; its tables and data are kept\n"
	if r.stderr != want {
		t.Errorf("stderr %q, want %q", r.stderr, want)
	}
	// Nothing was removed.
	if scalar(t, p.a, "SELECT to_regclass('extra_seed')::text") != "extra_seed" {
		t.Error("the table of the app that is not installed was dropped")
	}
	if got := p.a.Rows("SELECT name FROM fb_app ORDER BY name"); len(got) != 2 {
		t.Errorf("fb_app %v", got)
	}
	// The schedule of the app that is not installed is deleted (and reported); nothing else is dropped.
	expectContains(t, r.stdout, "remove extra.mark (every 60m)")
	if got := names(p.a, siteSchedules); !reflect.DeepEqual(got, withBuiltin("base.tick")) {
		t.Errorf("site a schedules %v", got)
	}
}

// Rust: site_apps.rs::removed_app (the scenario with the fixture: the document and the record of the fixture of
// an app that is no longer installed stay, and installing the app again finds the data where it was left)
func TestARemovedAppKeepsItsFixtureAndFindsItAgain(t *testing.T) {
	p := newAppPair(t, `apps = ["base"]`)
	expectCode(t, p.run("migrate", "--site", "b"), 0, "migrate b")
	expectCode(t, p.run("doc", "insert", "extra_item", "--site", "b", "--data", `{"code":"kept"}`), 0, "insert kept")

	// Site b narrows to base: rewrite its apps line.
	text := fmt.Sprintf("[[sites]]\nname = \"a\"\nhosts = [\"a.localhost\"]\ndatabase_url_env = \"%s\"\napps = [\"base\"]\n\n"+
		"[[sites]]\nname = \"b\"\nhosts = [\"b.localhost\"]\ndatabase_url_env = \"%s\"\napps = [\"base\"]\n", aURL, bURL)
	if err := os.WriteFile(filepath.Join(p.dir, "forgebase.toml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	r := p.run("migrate", "--site", "b")
	expectCode(t, r, 0, "migrate b with base only")
	if strings.Contains(r.stdout, "fixtures:") {
		t.Errorf("an app that is not installed got its fixtures: %s", r.stdout)
	}
	// `kept` and the document of the fixture stay, and so does the record of the fixture.
	if scalar(t, p.b, "SELECT count(*)::text FROM doc_extra_item") != "2" || scalar(t, p.b, "SELECT count(*)::text FROM fb_fixture") != "1" {
		t.Error("the data of the app that is not installed was removed")
	}
	// The DocType is gone from the site.
	gone := p.run("doc", "get", "extra_item", "kept", "--site", "b")
	expectCode(t, gone, 1, "doc get on an app that is not installed")
	expectContains(t, gone.stderr, "unknown DocType `extra_item`")

	// Installing it again finds the data where it was left, and the fixture unchanged.
	text = fmt.Sprintf("[[sites]]\nname = \"a\"\nhosts = [\"a.localhost\"]\ndatabase_url_env = \"%s\"\napps = [\"base\"]\n\n"+
		"[[sites]]\nname = \"b\"\nhosts = [\"b.localhost\"]\ndatabase_url_env = \"%s\"\napps = [\"base\", \"extra\"]\n", aURL, bURL)
	if err := os.WriteFile(filepath.Join(p.dir, "forgebase.toml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	r = p.run("migrate", "--site", "b")
	expectCode(t, r, 0, "migrate b with extra again")
	expectContains(t, r.stdout, "fixtures: extra: 0 inserted, 0 updated, 0 kept, 1 unchanged\n")
	if r.stderr != "" {
		t.Errorf("stderr %q", r.stderr)
	}
	out := p.run("doc", "get", "extra_item", "kept", "--site", "b")
	expectCode(t, out, 0, "doc get kept")
	expectContains(t, out.stdout, `"name": "kept"`)
}

// Rust: site_apps.rs::cli_per_site
func TestCommandsKnowOnlyTheAppsOfTheSite(t *testing.T) {
	p := newAppPair(t, `apps = ["base"]`)
	expectCode(t, p.run("migrate", "--site", "a"), 0, "migrate a")
	expectCode(t, p.run("migrate", "--site", "b"), 0, "migrate b")
	// extra_item exists on b ...
	r := p.run("doc", "insert", "extra_item", "--site", "b", "--data", `{"code":"x-1"}`)
	expectCode(t, r, 0, "insert on b")
	expectContains(t, r.stdout, `"name": "x-1"`)
	// ... and is unknown on a, which lists the DocTypes it has.
	r = p.run("doc", "insert", "extra_item", "--site", "a", "--data", `{"code":"x-1"}`)
	expectCode(t, r, 1, "insert on a")
	if r.stderr != "error: unknown DocType `extra_item`; known DocTypes: item\n" {
		t.Errorf("%q", r.stderr)
	}

	// The jobs of an app that is not installed are unknown too, and nothing is enqueued.
	r = p.run("job", "enqueue", "extra.stamp", "--site", "a")
	expectCode(t, r, 1, "enqueue on a")
	expectContains(t, r.stderr, "unknown job `extra.stamp`")
	if got := scalar(t, p.a, "SELECT count(*)::text FROM fb_job"); got != "0" {
		t.Errorf("%s jobs on a", got)
	}
	expectCode(t, p.run("job", "enqueue", "extra.stamp", "--site", "b"), 0, "enqueue on b")
	// Built-in jobs are in every site.
	expectCode(t, p.run("job", "enqueue", "forgebase.purge_jobs", "--site", "a"), 0, "built-in on a")
}

// Rust: site_apps.rs::config_and_site_list (the validation of the apps list, which needs the apps of the
// binary; the other lines of that scenario, the site list, are in site_cmd_test.go)
func TestTheAppsOfASiteAreCheckedAgainstTheAppsOfTheBinary(t *testing.T) {
	p := newAppPair(t, `apps = ["extra"]`)
	r := p.run("migrate", "--site", "a")
	expectCode(t, r, 1, "extra without base")
	expectContains(t, r.stderr, "invalid config file", "site `a`: app `extra` needs `base`, which is not in `apps`")
	p.config(`apps = ["base", "nope", "base"]`)
	r = p.run("migrate", "--site", "a")
	expectCode(t, r, 1, "unknown and repeated")
	expectContains(t, r.stderr, "site `a`: unknown app `nope` in `apps`; the apps of this binary are: base, extra", "site `a`: app `base` is listed twice in `apps`")
}

// Rust: run.rs (site_composed): a problem found only with the apps of one site names the site. Here a
// --doctypes file links to extra_item, which exists on b only.
func TestAProblemOfOnlyOneSitesAppsNamesTheSite(t *testing.T) {
	p := newAppPair(t, `apps = ["base"]`)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.toml"), []byte(linkingTOML("note", "extra_item")), 0o600); err != nil {
		t.Fatal(err)
	}
	// Every app of the binary: the file is fine.
	if r := p.run("migrate", "--site", "b", "--doctypes", dir); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	r := p.run("migrate", "--site", "a", "--doctypes", dir)
	expectCode(t, r, 1, "the apps of site a")
	expectContains(t, r.stderr, "error: cannot load DocType files:\n  - site `a`: ", "note.toml", "extra_item")
}

// siteRequest sends a request to the routing handler for host, with an optional session cookie.
func siteRequest(t *testing.T, h http.Handler, method, path, host string, session siteSession, body string) (int, string, http.Header) {
	t.Helper()
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Host = host
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if session.cookie != "" {
		req.Header.Set("Cookie", "fb_session="+session.cookie)
		req.Header.Set("X-Forgebase-CSRF", session.csrf)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Result().Header
}

// siteSession is a login: the session cookie and its CSRF token.
type siteSession struct{ cookie, csrf string }

func siteLogin(t *testing.T, h http.Handler, host string) siteSession {
	t.Helper()
	code, text, header := siteRequest(t, h, "POST", "/api/v1/login", host, siteSession{}, `{"username":"root","password":"a long password"}`)
	if code != 200 {
		t.Fatalf("login on %s: %d %s", host, code, text)
	}
	var reply struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		}
	}
	if err := json.Unmarshal([]byte(text), &reply); err != nil {
		t.Fatal(err)
	}
	for _, set := range header.Values("Set-Cookie") {
		if rest, ok := strings.CutPrefix(set, "fb_session="); ok {
			value, _, _ := strings.Cut(rest, ";")
			return siteSession{cookie: value, csrf: reply.Data.CSRF}
		}
	}
	t.Fatal("no session cookie")
	return siteSession{}
}

// Rust: site_apps.rs::http_per_site
func TestHTTPShowsOnlyTheDocTypesOfTheAppsOfTheSite(t *testing.T) {
	p := newAppPair(t, `apps = ["base"]`)
	expectCode(t, p.run("migrate", "--site", "a"), 0, "migrate a")
	expectCode(t, p.run("migrate", "--site", "b"), 0, "migrate b")
	for _, d := range []*dbtest.DB{p.a, p.b} {
		if _, err := db.CreateUser(context.Background(), d.Pool, "root", "a long password", []string{"Administrator"}); err != nil {
			t.Fatal(err)
		}
	}
	// serve's own per-site wiring: its composition of each site (siteComposeds, with the dispatcher of its
	// methods) and its routing (routeSites), on the pools of the two test databases, without a socket.
	cfg, err := loadConfig(filepath.Join(p.dir, "forgebase.toml"), func(string) string { return "" }, binaryApps(siteApps()))
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{o: Options{Apps: siteApps()}}
	sites := cfg.sites
	composeds, err := r.siteComposeds(sites, "")
	if err != nil {
		t.Fatal(err)
	}
	contexts := []*siteContext{
		{site: sites[0], pool: p.a.Pool, registry: composeds[0].registry, methods: composeds[0].MethodDispatch()},
		{site: sites[1], pool: p.b.Pool, registry: composeds[1].registry, methods: composeds[1].MethodDispatch()},
	}
	routing, lines, err := routeSites(true, contexts, nil, httpapi.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "site a: a.localhost\n" {
		t.Errorf("announcement %q", lines)
	}
	sessionA, sessionB := siteLogin(t, routing, "a.localhost"), siteLogin(t, routing, "b.localhost")
	doctypes := func(host string, session siteSession) string {
		code, text, _ := siteRequest(t, routing, "GET", "/api/v1/meta", host, session, "")
		if code != 200 {
			t.Fatalf("%s: %d %s", host, code, text)
		}
		var reply struct{ Data []struct{ Name string } }
		if err := json.Unmarshal([]byte(text), &reply); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, d := range reply.Data {
			names = append(names, d.Name)
		}
		return strings.Join(names, ",")
	}
	if got := doctypes("a.localhost", sessionA); got != "item" {
		t.Errorf("site a: %s", got)
	}
	if got := doctypes("b.localhost", sessionB); got != "extra_item,item" {
		t.Errorf("site b: %s", got)
	}
	status := func(host string, session siteSession, method, path, body string) int {
		code, _, _ := siteRequest(t, routing, method, path, host, session, body)
		return code
	}
	// A DocType of an app that is not installed does not exist on A: meta, list and create.
	for _, c := range [][3]string{{"GET", "/api/v1/meta/extra_item", ""}, {"GET", "/api/v1/resource/extra_item", ""}, {"POST", "/api/v1/resource/extra_item", `{"code":"h-1"}`}} {
		if got := status("a.localhost", sessionA, c[0], c[1], c[2]); got != 404 {
			t.Errorf("a %v: %d", c[:2], got)
		}
	}
	// On B it is served; on A the installed DocType still is.
	if got := status("b.localhost", sessionB, "POST", "/api/v1/resource/extra_item", `{"code":"h-1"}`); got != 201 {
		t.Errorf("create on b: %d", got)
	}
	if got := status("b.localhost", sessionB, "GET", "/api/v1/resource/extra_item", ""); got != 200 {
		t.Errorf("list on b: %d", got)
	}
	if got := status("a.localhost", sessionA, "GET", "/api/v1/resource/item", ""); got != 200 {
		t.Errorf("item on a: %d", got)
	}
	// Nothing was written to A.
	if got := p.a.Rows("SELECT coalesce(to_regclass('doc_extra_item')::text, 'NULL')"); got[0][0] != "NULL" {
		t.Errorf("%v", got)
	}
	// The methods (M9.1) of an app that is not installed do not exist either: base.hello is served on both
	// sites, extra.hello only on B.
	for _, c := range []struct {
		host    string
		session siteSession
		method  string
		want    int
	}{
		{"a.localhost", sessionA, "base.hello", 200},
		{"a.localhost", sessionA, "extra.hello", 404},
		{"b.localhost", sessionB, "base.hello", 200},
		{"b.localhost", sessionB, "extra.hello", 200},
	} {
		if got := status(c.host, c.session, "POST", "/api/v1/method/"+c.method, "{}"); got != c.want {
			t.Errorf("%s on %s: %d, want %d", c.method, c.host, got, c.want)
		}
	}
	// A site that has no dispatcher (the State of a test, not of serve) answers 404 for every method.
	bare := []*siteContext{{site: sites[1], pool: p.b.Pool, registry: composeds[1].registry}}
	bareRouting, _, err := routeSites(true, bare, nil, httpapi.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := siteRequest(t, bareRouting, "POST", "/api/v1/method/base.hello", "b.localhost", siteLogin(t, bareRouting, "b.localhost"), "{}"); code != 404 {
		t.Errorf("without a dispatcher: %d", code)
	}
}
