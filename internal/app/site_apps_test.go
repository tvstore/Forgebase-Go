package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Apps per site (M7.2, ADR 0014): a binary with two apps, base and extra (which depends on base), run with a
// config file of sites that install different apps, against real PostgreSQL databases. Ported from the
// scenarios migrate_per_site and cli_per_site of crates/forgebase/tests/site_apps.rs, and from the
// config validation of config_and_site_list that needs only the apps of the binary. Deferred, with the
// phase that brings what they use: site list and site create (P5), the worker of a site (P5), the HTTP
// view of a site's apps (cmd/forgebase tests, with the sales example in P4 and the methods in P7), and
// the removed-app scenario with fixtures (P7).

const (
	siteItem = "name = \"item\"\nlabel = \"Item\"\nnaming = { field = \"code\" }\n" +
		"[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n"
	siteExtraItem = "name = \"extra_item\"\nlabel = \"Extra item\"\nnaming = { field = \"code\" }\n" +
		"[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n"
)

func baseApp() *App {
	return NewApp("base").Version("1.0.0").DocTypes(files("doctypes/item.toml", siteItem))
}

func extraApp() *App {
	return NewApp("extra").DependsOn("base").
		DocTypes(files("doctypes/extra_item.toml", siteExtraItem)).
		Migrations(fstest.MapFS{"migrations/0001_seed.sql": {Data: []byte("CREATE TABLE extra_seed (entry text)")}})
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
}

// Rust: site_apps.rs::config_and_site_list (the validation of the apps list, which needs the apps of the
// binary; the other lines of that scenario are the site list, P5)
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
