package app

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// Putting the apps together: validating them, loading their DocTypes into one registry, registering their
// hooks, and migrating them (the Rust module compose.rs).
//
// Order of work (documented in ADR 0012 and ADR 0028): the apps' own consistency (names, dependencies,
// order, migration versions); the DocTypes of every app in app order, then the DocTypes of the --doctypes
// directory (the app `local`); the references between DocTypes (the rules of the registry, plus: a Link or
// Table may only target a DocType of its own app or of a dependency); the hooks in app order. Every problem
// found is collected and reported together. The reference and hook checks only run when every DocType file
// loaded, because a file that did not load would make them report follow-up errors.
//
// Overlays (P7), jobs and schedules (P5), reports (P6), methods (P7) and fixtures (P7) are added to this
// order by the phases that bring them.

// localApp is the name of the app that the --doctypes directory forms.
const localApp = "local"

// frameworkApp is the name the framework keeps for its own jobs (P5).
const frameworkApp = "forgebase"

func validUTF8(b []byte) bool { return utf8.Valid(b) }

// StartupError is everything wrong with the apps of a binary and the DocType files, found at startup. All
// problems are collected, so one run shows them all.
//
// It prints as up to two lists: "invalid app setup" (names, dependencies, order, ownership, hooks,
// migrations, links between apps) and "cannot load DocType files" (a file that cannot be read or parsed,
// an invalid DocType, a Link to a DocType nobody defines, each naming its file).
type StartupError struct {
	apps  []string
	files []string
}

// Problems is every problem, one message each: first the app setup problems, then the DocType file
// problems.
func (e *StartupError) Problems() []string {
	return append(append([]string(nil), e.apps...), e.files...)
}

func (e *StartupError) isEmpty() bool { return len(e.apps) == 0 && len(e.files) == 0 }

// forSite is the error with every problem prefixed with the site it was found for (a problem that only
// the apps of one site have, for example a --doctypes file that links to an app the site lacks).
func (e *StartupError) forSite(site string) *StartupError {
	out := &StartupError{}
	for _, p := range e.apps {
		out.apps = append(out.apps, fmt.Sprintf("site `%s`: %s", site, p))
	}
	for _, p := range e.files {
		out.files = append(out.files, fmt.Sprintf("site `%s`: %s", site, p))
	}
	return out
}

func (e *StartupError) Error() string {
	var b strings.Builder
	if len(e.apps) > 0 {
		b.WriteString("invalid app setup:\n" + bullets(e.apps))
	}
	if len(e.files) > 0 {
		if len(e.apps) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("cannot load DocType files:\n" + bullets(e.files))
	}
	return b.String()
}

// Composed is the apps of a binary, composed: one registry and what migrate needs.
type Composed struct {
	registry *core.Registry
	// apps are the registered apps (not local) in app order.
	apps []appInfo
}

type appInfo struct {
	name       string
	version    string
	migrations []MigrationFile
}

// Registry is the registry of all DocTypes, with their owners and hooks.
func (c *Composed) Registry() *core.Registry { return c.registry }

// appCount is how many apps were composed (the --doctypes directory is not counted).
func (c *Composed) appCount() int { return len(c.apps) }

// hasApp is true when name is one of the composed apps (not local).
func (c *Composed) hasApp(name string) bool {
	for _, a := range c.apps {
		if a.name == name {
			return true
		}
	}
	return false
}

// Migrate runs the framework migrations, the schema sync and the app migrations in app order
// (db.MigrateApps). The schedules, the workflow backfill, the fixtures and the report check are added here
// in P5 to P7.
func (c *Composed) Migrate(ctx context.Context, pool *pgxpool.Pool, options db.SyncOptions) (*db.MigrateReport, error) {
	specs := make([]db.AppSpec, len(c.apps))
	for i, a := range c.apps {
		specs[i] = db.AppSpec{Name: a.name, Version: a.version}
		for _, m := range a.migrations {
			specs[i].Migrations = append(specs[i].Migrations, db.AppMigration{Version: m.Version, Name: m.Name, SQL: m.SQL})
		}
	}
	return db.MigrateApps(ctx, pool, c.registry, specs, options)
}

// ComposeBinary composes the apps of b, for forgebase/testing.
func ComposeBinary(b *Binary) (*Composed, error) {
	c, startup := compose(b.apps, "")
	if startup != nil {
		return nil, startup
	}
	return c, nil
}

// AppNameProblems is what is wrong with an app name: the rules of a DocType name, and the names that are
// reserved. Also used by `forgebase new-app`, so the generated app is accepted at startup.
func AppNameProblems(name string) []string {
	var problems []string
	if err := core.ValidateDocTypeName(name); err != nil {
		problems = append(problems, fmt.Sprintf("app name %s %s", rustQuote(name), err))
	}
	if name == frameworkApp {
		problems = append(problems, fmt.Sprintf("app name `%s` is reserved for the framework's own jobs", frameworkApp))
	}
	if name == localApp {
		problems = append(problems, fmt.Sprintf("app name `%s` is reserved for the DocTypes of the --doctypes directory", localApp))
	}
	return problems
}

// compose checks and loads apps (in this order) and, when local is not empty, the DocType files of that
// directory as the last app, which depends on all the others.
func compose(apps []*App, local string) (*Composed, *StartupError) {
	errs := &StartupError{}
	reach := checkApps(apps, errs)
	registry := core.NewRegistry()
	// DocType name -> (app, file) that defined it.
	definedIn := map[string][2]string{}
	for _, a := range apps {
		if a == nil {
			continue
		}
		files := make([]file, len(a.doctypes))
		for i, f := range a.doctypes {
			files[i] = file{path: f.Path, text: f.TOML}
		}
		errs.files = append(errs.files, a.fileProblems...)
		load(a.name, files, registry, definedIn, errs)
	}
	// The directory is read after the apps. Its problems are reported like any other file problem,
	// without an app prefix (it is not an app of the binary).
	localReach := map[string]bool{}
	if local != "" {
		files, problems := readDir(local)
		errs.files = append(errs.files, problems...)
		load(localApp, files, registry, definedIn, errs)
		for _, a := range apps {
			if a != nil {
				localReach[a.name] = true
			}
		}
	}
	// Only when everything loaded: a Link to a DocType from a broken file would be reported twice,
	// and a hook on one as an unknown DocType.
	if len(errs.files) == 0 {
		checkReferences(registry, definedIn, reach, localReach, errs)
		checkHooks(apps, reach, registry, errs)
	}
	if !errs.isEmpty() {
		return nil, errs
	}
	c := &Composed{registry: registry}
	for _, a := range apps {
		c.apps = append(c.apps, appInfo{name: a.name, version: a.version, migrations: a.migrations})
	}
	return c, nil
}

// checkApps checks names, dependencies, order and migration versions. It returns, per app name, the
// names of the apps it may use: its dependencies and theirs.
func checkApps(apps []*App, errs *StartupError) map[string]map[string]bool {
	reach := map[string]map[string]bool{}
	for index, a := range apps {
		if a == nil {
			errs.apps = append(errs.apps, fmt.Sprintf("app #%d is nil", index+1))
			continue
		}
		name := a.name
		errs.apps = append(errs.apps, AppNameProblems(name)...)
		errs.apps = append(errs.apps, a.appProblems...)
		if _, dup := reach[name]; dup {
			errs.apps = append(errs.apps, fmt.Sprintf("app `%s` is registered more than once", name))
		}
		uses := map[string]bool{}
		for _, dependency := range a.dependsOn {
			if theirs, ok := reach[dependency]; ok && dependency != name {
				uses[dependency] = true
				for k := range theirs {
					uses[k] = true
				}
				continue
			}
			switch {
			case dependency == name:
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s` depends on itself", name))
			case registered(apps, dependency):
				errs.apps = append(errs.apps, fmt.Sprintf(
					"app `%s` depends on `%s`, which is registered after it; register `%s` first (apps are never sorted)",
					name, dependency, dependency))
			default:
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s` depends on `%s`, which is not registered", name, dependency))
			}
		}
		if _, ok := reach[name]; !ok {
			reach[name] = uses
		}
		var previous *MigrationFile
		for i := range a.migrations {
			m := &a.migrations[i]
			switch {
			case m.Version < 1:
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: migration version %d (%s, %s) must be positive", name, m.Version, m.Name, m.Path))
			case previous != nil && previous.Version >= m.Version:
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: migration versions must increase: %d (%s, %s) comes after %d (%s)",
					name, m.Version, m.Name, m.Path, previous.Version, previous.Name))
			}
			previous = m
		}
	}
	return reach
}

func registered(apps []*App, name string) bool {
	for _, a := range apps {
		if a != nil && a.name == name {
			return true
		}
	}
	return false
}

// load parses files and adds their DocTypes to the registry as owned by app. A problem names its
// file; the files of an app other than local also name the app.
func load(app string, files []file, registry *core.Registry, definedIn map[string][2]string, errs *StartupError) {
	prefix := ""
	if app != localApp {
		prefix = fmt.Sprintf("app `%s`: ", app)
	}
	for _, f := range files {
		doctype, err := core.ParseDocType(f.text)
		if err != nil {
			errs.files = append(errs.files, fmt.Sprintf("%s%s: %s", prefix, f.path, err))
			continue
		}
		name := doctype.Name
		reserved := false
		for _, field := range doctype.Fields {
			if strings.HasPrefix(field.Name, core.CustomPrefix) {
				reserved = true
				errs.files = append(errs.files, fmt.Sprintf(
					"%s%s: field `%s` of doctype `%s` starts with `%s`, a prefix reserved for site custom fields; rename it",
					prefix, f.path, field.Name, name, core.CustomPrefix))
			}
		}
		if reserved {
			continue
		}
		if first, ok := definedIn[name]; ok {
			if first[0] == app {
				errs.files = append(errs.files, fmt.Sprintf("%s%s: doctype `%s` is already defined in %s", prefix, f.path, name, first[1]))
			} else {
				errs.files = append(errs.files, fmt.Sprintf(
					"%s%s: doctype `%s` is already defined by app `%s` (%s); a DocType belongs to one app",
					prefix, f.path, name, first[0], first[1]))
			}
			continue
		}
		if problems := registry.AddOwned(app, doctype); problems != nil {
			for _, problem := range problems {
				errs.files = append(errs.files, fmt.Sprintf("%s%s: %s", prefix, f.path, problem))
			}
			continue
		}
		definedIn[name] = [2]string{app, f.path}
	}
}

// checkReferences runs the reference rules of the registry, each problem naming the file of the DocType it is
// about, and the app rule: a Link or Table field may only target a DocType of its own app or of an app it
// depends on.
func checkReferences(registry *core.Registry, definedIn map[string][2]string, reach map[string]map[string]bool, localReach map[string]bool, errs *StartupError) {
	for _, problem := range registry.CheckReferences() {
		doctype := problem.DocType
		switch problem.Kind {
		case core.ErrChildSharedByTables:
			doctype = problem.Child
		}
		if def, ok := definedIn[doctype]; ok && doctype != "" {
			if def[0] == localApp {
				errs.files = append(errs.files, fmt.Sprintf("%s: %s", def[1], problem))
			} else {
				errs.files = append(errs.files, fmt.Sprintf("app `%s`: %s: %s", def[0], def[1], problem))
			}
			continue
		}
		errs.files = append(errs.files, problem.Error())
	}
	// The app rule runs even when the reference rules found problems, so that one run shows both kinds. (A
	// target nobody defines has no owner and is skipped here: it is already reported.)
	for _, doctype := range registry.Iter() {
		def, ok := definedIn[doctype.Name]
		if !ok {
			continue
		}
		app, path := def[0], def[1]
		uses := localReach
		if app != localApp {
			var known bool
			if uses, known = reach[app]; !known {
				continue
			}
		}
		for _, field := range doctype.Fields {
			if field.Type.Kind != core.FieldLink && field.Type.Kind != core.FieldTable {
				continue
			}
			target := field.Type.Target
			owner, ok := registry.Owner(target)
			if !ok {
				continue
			}
			if owner != app && !uses[owner] {
				errs.apps = append(errs.apps, fmt.Sprintf(
					"app `%s`: %s: field `%s` of doctype `%s` refers to `%s`, which belongs to app `%s`; app `%s` does not depend on `%s` (add DependsOn(%q), registered before `%s`)",
					app, path, field.Name, doctype.Name, target, owner, app, owner, owner, app))
			}
		}
	}
}

// checkHooks registers the hooks in app order, then registration order. An app may hook DocTypes of its own
// and of its dependencies.
func checkHooks(apps []*App, reach map[string]map[string]bool, registry *core.Registry, errs *StartupError) {
	for _, a := range apps {
		if a == nil {
			continue
		}
		for _, h := range a.hooks {
			owner, ok := registry.Owner(h.doctype)
			if !ok {
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: hooks registered for unknown DocType `%s`", a.name, h.doctype))
				continue
			}
			if owner != a.name && !reach[a.name][owner] {
				errs.apps = append(errs.apps, fmt.Sprintf(
					"app `%s`: hooks registered for DocType `%s` of app `%s`, which `%s` does not depend on", a.name, h.doctype, owner, a.name))
				continue
			}
			if err := registry.AddHooks(h.doctype, h.hooks); err != nil {
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: hooks for `%s`: %s", a.name, h.doctype, err))
			}
		}
	}
}
