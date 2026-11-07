package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/httpapi"
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
// Reports (P6) are checked and registered after the hooks. Jobs (P5) and methods (P7.1) are checked after that,
// then the schedules (they do not depend on the DocType files, so their problems are reported with the
// others). Overlays (P7) and fixtures (P7) are added to this order by the phases that bring them.

// localApp is the name of the app that the --doctypes directory forms.
const localApp = "local"

// frameworkApp is the name the framework keeps for its own jobs.
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
	// jobs are the jobs of all apps and the built-in ones by full name (<app>.<job>); the registry has the
	// names.
	jobs map[string]*jobDef
	// schedules are the schedules of all apps and the built-in ones, by full job name (valid ones only).
	schedules map[string]db.Spec
	// methods runs the methods of all apps by full name (<app>.<method>); the registry has their names and
	// access.
	methods *methods
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

// Migrate runs the framework migrations, the schema sync, the app migrations in app order and the sync of the
// declared schedules and the workflow backfill (db.MigrateAll). The check of the reports (CheckReports) is
// the caller's, after the report; the fixtures are added here in P7.
func (c *Composed) Migrate(ctx context.Context, pool *pgxpool.Pool, options db.SyncOptions) (*db.MigrateReport, error) {
	specs := make([]db.AppSpec, len(c.apps))
	for i, a := range c.apps {
		specs[i] = db.AppSpec{Name: a.name, Version: a.version}
		for _, m := range a.migrations {
			specs[i].Migrations = append(specs[i].Migrations, db.AppMigration{Version: m.Version, Name: m.Name, SQL: m.SQL})
		}
	}
	// The schedules by name, as Rust's BTreeMap gives them (not nil: a binary with no schedule deletes the
	// rows of a binary that had some).
	declared := make([]db.DeclaredSchedule, 0, len(c.schedules))
	for name, spec := range c.schedules {
		declared = append(declared, db.DeclaredSchedule{Name: name, Spec: spec})
	}
	sort.Slice(declared, func(i, j int) bool { return declared[i].Name < declared[j].Name })
	return db.MigrateAll(ctx, pool, c.registry, specs, declared, options)
}

// MethodDispatch is what runs the app methods of the composed apps, for internal/httpapi (State.WithMethods).
func (c *Composed) MethodDispatch() httpapi.MethodDispatch { return c.methods }

// reportCount is how many reports the composed apps define.
func (c *Composed) reportCount() int { return len(c.registry.Reports()) }

// CheckReports prepares the SQL of every report against the database and returns one message per report
// that does not fit what it declares (db.CheckReports). Run after the migrations, which create the tables
// the reports read.
func (c *Composed) CheckReports(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	reports := c.registry.Reports()
	if len(reports) == 0 {
		return nil, nil
	}
	conn, err := db.GetReportConnection(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	return db.CheckReports(ctx, conn.Conn(), reports)
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
		checkReports(apps, reach, registry, errs)
	}
	jobs := checkJobs(apps, registry, errs)
	handlers := checkMethods(apps, registry, errs)
	schedules := checkSchedules(apps, errs)
	if !errs.isEmpty() {
		return nil, errs
	}
	c := &Composed{registry: registry, jobs: jobs, schedules: schedules, methods: &methods{handlers: handlers}}
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

// checkJobs checks the job names and options and registers the valid jobs: the names in the registry, the
// definitions in the result, by full name. The framework's own jobs come first: they exist in every binary.
func checkJobs(apps []*App, registry *core.Registry, errs *StartupError) map[string]*jobDef {
	jobs := map[string]*jobDef{}
	for _, b := range builtinJobs() {
		def := b.def
		registry.AddJob(def.name)
		jobs[def.name] = &def
	}
	for _, a := range apps {
		if a == nil {
			continue
		}
		seen := map[string]bool{}
		for _, job := range a.jobs {
			name := job.name
			if err := core.ValidateDocTypeName(name); err != nil {
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: job name %s %s", a.name, rustQuote(name), err))
				continue
			}
			if seen[name] {
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: job `%s` is registered more than once", a.name, name))
				continue
			}
			seen[name] = true
			problems := job.options.problems()
			for _, problem := range problems {
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: job `%s`: %s", a.name, name, problem))
			}
			full := a.name + "." + name
			// The same full name from two apps with the same name is reported as a duplicate app.
			if _, taken := jobs[full]; len(problems) == 0 && !taken {
				registry.AddJob(full)
				def := job
				def.name = full
				jobs[full] = &def
			}
		}
	}
	return jobs
}

// checkReports checks the reports and registers the valid ones, in the registry by full name. A Link param
// or column may name a DocType of the app itself or of an app it depends on (the rule of the Link fields),
// so the report works on every site that installs the app.
func checkReports(apps []*App, reach map[string]map[string]bool, registry *core.Registry, errs *StartupError) {
	for _, a := range apps {
		if a == nil {
			continue
		}
		seen := map[string]bool{}
		for _, r := range a.reports {
			name := r.name
			if seen[name] {
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: report `%s` is registered more than once", a.name, name))
				continue
			}
			seen[name] = true
			def := r.report.definition(a.name, name)
			problems := def.Problems()
			uses := reach[a.name]
			for _, target := range def.LinkTargets() {
				dt := registry.Get(target.DocType)
				switch {
				case dt == nil:
					problems = append(problems, fmt.Sprintf("%s links to unknown DocType `%s`", target.Place, target.DocType))
				case dt.IsChild:
					problems = append(problems, fmt.Sprintf("%s links to `%s`, a child DocType (its rows belong to a parent document)",
						target.Place, target.DocType))
				default:
					owner, _ := registry.Owner(target.DocType)
					if owner != a.name && !uses[owner] {
						problems = append(problems, fmt.Sprintf("%s links to `%s`, which belongs to app `%s`; app `%s` does not depend on `%s`",
							target.Place, target.DocType, owner, a.name, owner))
					}
				}
			}
			for _, problem := range problems {
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: report `%s`: %s", a.name, name, problem))
			}
			// The same full name from two apps with the same name is reported as a duplicate app.
			if len(problems) == 0 {
				registry.AddReport(&def)
			}
		}
	}
}

// checkMethods checks the methods and registers the valid ones: names and access in the registry, the handlers
// in the result, by full name. Same rules as the reports and jobs: a valid name, unique within the app, and
// exactly one way of giving access.
func checkMethods(apps []*App, registry *core.Registry, errs *StartupError) map[string]MethodHandler {
	handlers := map[string]MethodHandler{}
	for _, a := range apps {
		if a == nil {
			continue
		}
		seen := map[string]bool{}
		for _, m := range a.methods {
			if seen[m.name] {
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: method `%s` is registered more than once", a.name, m.name))
				continue
			}
			seen[m.name] = true
			def, problems := m.method.definition(a.name, m.name)
			for _, problem := range problems {
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: method `%s`: %s", a.name, m.name, problem))
			}
			// The same full name from two apps with the same name is reported as a duplicate app.
			if len(problems) == 0 && registry.AddMethod(def) {
				handlers[def.FullName()] = m.method.handler
			}
		}
	}
	return handlers
}

// checkSchedules checks the schedules of the apps and adds the built-in ones. A schedule must name a job the
// same app registered, at most once, and have valid values. It returns the valid schedules by full job name.
func checkSchedules(apps []*App, errs *StartupError) map[string]db.Spec {
	schedules := map[string]db.Spec{}
	for _, b := range builtinJobs() {
		if b.spec != nil {
			schedules[b.def.name] = *b.spec
		}
	}
	for _, a := range apps {
		if a == nil {
			continue
		}
		seen := map[string]bool{}
		for _, s := range a.schedules {
			registeredJob := false
			for _, job := range a.jobs {
				if job.name == s.job {
					registeredJob = true
					break
				}
			}
			if !registeredJob {
				errs.apps = append(errs.apps, fmt.Sprintf(
					"app `%s`: schedule for job `%s`, which `%s` did not register (register it with App.Job first; an app schedules only its own jobs)",
					a.name, s.job, a.name))
				continue
			}
			if seen[s.job] {
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: job `%s` has more than one schedule", a.name, s.job))
				continue
			}
			seen[s.job] = true
			problems := s.schedule.problems()
			for _, problem := range problems {
				errs.apps = append(errs.apps, fmt.Sprintf("app `%s`: schedule for job `%s`: %s", a.name, s.job, problem))
			}
			if len(problems) == 0 {
				if _, taken := schedules[a.name+"."+s.job]; !taken {
					schedules[a.name+"."+s.job] = s.schedule.spec
				}
			}
		}
	}
	return schedules
}
