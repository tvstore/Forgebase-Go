package app

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The app model: App, the builder an application writes, and Binary, the binary made of apps (the Rust
// modules app.rs and builder.rs). The root package forgebase re-exports both as type aliases, so this
// file is where the godoc of the public methods lives.
//
// Version is the version of the framework. It lives here (and not in the root package, which imports
// this one) so that Binary.Run can print it; the root package re-exports it as forgebase.Version.

// Version is the version of the framework. It is the value printed by `forgebase --version`.
const Version = "0.0.0"

// DocTypeFile is a DocType definition file of an app: its path (as shown in messages: the path in
// the file system the app was given, for an embed.FS "doctypes/customer.toml") and its TOML text.
type DocTypeFile struct {
	Path string
	TOML string
}

// MigrationFile is a data migration of an app, read from a file named NNNN_name.sql.
type MigrationFile struct {
	Version int32
	Name    string
	Path    string
	SQL     string
}

type scheduleDef struct {
	job      string
	schedule Schedule
}

type namedReport struct {
	name   string
	report *Report
}

type hookSet struct {
	doctype string
	hooks   core.DocTypeHooks
}

// App is a named unit of an application: DocTypes, lifecycle hooks and SQL data migrations, compiled into
// the binary. Several apps make one binary; they are added to it with [Binary.App] in dependency order,
// and nothing is sorted for you (ADR 0012, ADR 0028).
//
//	func App() *forgebase.App {
//		return forgebase.NewApp("sales").
//			Version("1.0.0").
//			DependsOn("crm").
//			DocTypes(doctypesFS).
//			Hooks("sales_order", OrderRules{}).
//			Migrations(migrationsFS)
//	}
//
// An App is only data: building one cannot fail, and the methods return the App itself so that calls
// chain. Everything wrong with it (an invalid name, a missing dependency, a hook on an unknown DocType, a
// file that cannot be read, ...) is reported together with the other problems when the binary starts
// (StartupError). Build an App with NewApp; the zero value is not usable.
//
// What an app may do:
//
//   - own DocTypes (a DocType name belongs to exactly one app);
//   - link to DocTypes of itself and of the apps it depends on (directly or through other apps), and
//     nothing else;
//   - register hooks (DocTypeHooks) for its own DocTypes and for DocTypes of the apps it depends on.
//     Hooks run in app order, then in registration order within the app: the hooks of an app that
//     extends another run after the hooks of the app it extends;
//   - declare SQL data migrations;
//   - register background jobs (Job), which hooks, other jobs and the command line can enqueue, and run them on
//     a Schedule;
//   - define query reports (Report): SQL compiled into the binary, run read-only for the roles it names.
//
// What it may not do: change the definition of another app's DocType, run code outside the binary, or
// reach the database from a hook.
type App struct {
	name       string
	version    string
	dependsOn  []string
	doctypes   []DocTypeFile
	hooks      []hookSet
	migrations []MigrationFile
	jobs       []jobDef
	schedules  []scheduleDef
	reports    []namedReport
	// appProblems are what is wrong with the app itself (shown under "invalid app setup") and
	// fileProblems what is wrong with a DocType file (shown under "cannot load DocType files"),
	// found while the files were read. Nothing fails until the binary starts.
	appProblems  []string
	fileProblems []string
}

// NewApp starts an app. The name follows the rules of a DocType name (lower-case ASCII letters, digits and
// underscores, starting with a letter) and must be unique in the binary; `local` and `forgebase` are
// reserved. It is recorded in fb_app and names the app in messages and in the metadata API.
func NewApp(name string) *App { return &App{name: name} }

// Name is the name given to NewApp.
func (a *App) Name() string { return a.name }

// Version sets the version of the app, recorded in fb_app at every `migrate`. It has no other effect.
// An empty version is recorded as NULL.
func (a *App) Version(version string) *App {
	a.version = version
	return a
}

// DependsOn names apps this app depends on. They must be added to the binary before this app. It adds to
// the dependencies already named.
func (a *App) DependsOn(apps ...string) *App {
	a.dependsOn = append(a.dependsOn, apps...)
	return a
}

// DocTypes adds the DocType definition files in fsys: every file whose name ends in .toml, at any depth,
// in the order fs.WalkDir visits them (lexical within each directory), with the path in fsys as its
// name in messages. Names that start with a dot are skipped. It is made for an embed.FS:
//
//	//go:embed doctypes/*.toml
//	var doctypesFS embed.FS
//
//	forgebase.NewApp("sales").DocTypes(doctypesFS) // files are "doctypes/customer.toml", ...
//
// Calling it again adds more files. Embedding a directory is the whole list: there is nothing to
// register by hand, and a file that is not embedded is caught by the guard of forgebase/testing
// (AssertDocTypesListed). The Rust list order of doctypes![..] maps to the lexical order: the order only
// affects the order of the problems in a StartupError, not the result.
func (a *App) DocTypes(fsys fs.FS) *App {
	files, problems := readFiles(fsys, ".toml")
	for _, f := range files {
		if !utf8.ValidString(f.text) {
			a.fileProblems = append(a.fileProblems, fmt.Sprintf("app `%s`: %s: cannot read: stream did not contain valid UTF-8", a.name, f.path))
			continue
		}
		a.doctypes = append(a.doctypes, DocTypeFile{Path: f.path, TOML: f.text})
	}
	for _, p := range problems {
		a.appProblems = append(a.appProblems, fmt.Sprintf("app `%s`: DocTypes: %s", a.name, p))
	}
	return a
}

// Hooks registers a hook set for a DocType of this app or of an app it depends on (directly or through
// other apps). Several hook sets may be registered for one DocType; they run in registration order, after
// the hooks of the apps before this one. A child DocType has no hooks (the hooks of its parent see its
// rows). See DocTypeHooks for the pipeline.
//
// The hook set is shared by every request, so it must be safe for concurrent use. There is one hook
// set per call: to use the same logic for several DocTypes, pass the same value again.
func (a *App) Hooks(doctype string, hooks core.DocTypeHooks) *App {
	if hooks == nil {
		a.appProblems = append(a.appProblems, fmt.Sprintf("app `%s`: hooks for `%s` are nil", a.name, doctype))
		return a
	}
	a.hooks = append(a.hooks, hookSet{doctype: doctype, hooks: hooks})
	return a
}

// Job registers a background job with the default options (5 attempts, a 30 second delay that doubles up to 1
// hour, a 5 minute timeout). The name follows the rules of a DocType name and is unique within the app; the
// job is known as <app>.<name>. A hook enqueues it with HookContext.Enqueue (the job is written in the same
// transaction as the document change), another job with JobContext.Enqueue, an operator with `forgebase job
// enqueue`; `forgebase worker` runs it. See [JobHandler] for what a handler does and how delivery works.
func (a *App) Job(name string, handler JobHandler) *App {
	return a.JobWith(name, handler, NewJobOptions())
}

// JobWith is like Job, with [JobOptions]. Invalid options are a startup error.
func (a *App) JobWith(name string, handler JobHandler, options JobOptions) *App {
	if handler == nil {
		a.appProblems = append(a.appProblems, fmt.Sprintf("app `%s`: job `%s` has no handler", a.name, name))
		return a
	}
	a.jobs = append(a.jobs, jobDef{name: name, handler: handler, options: options})
	return a
}

// Schedule runs one of this app's jobs on a [Schedule]. job is the short name given to Job; the job runs as
// <app>.<job> and gets the payload {"scheduled_for": "<RFC 3339 UTC time of the tick>"}. `forgebase worker`
// enqueues it at every tick (once, however many workers run) and runs it like any other job, with the actor
// `scheduler`.
//
// Startup errors: a job this app did not register (a schedule for another app's job is refused too), a second
// schedule for the same job, and an invalid schedule.
//
// The schedules are stored by `forgebase migrate`, so run it after adding, changing or removing one; see
// [Schedule] and ADR 0013.
func (a *App) Schedule(job string, schedule Schedule) *App {
	a.schedules = append(a.schedules, scheduleDef{job: job, schedule: schedule})
	return a
}

// Report defines a query report: SQL that the app compiled in, run read-only for the roles it names. The
// name follows the rules of a DocType name and is unique within the app; the report is known as
// <app>.<name>. See [Report] for the rules of the SQL, the parameters and the columns. A nil report is a
// startup error, like everything wrong with a report, once the binary starts.
func (a *App) Report(name string, report *Report) *App {
	if report == nil {
		a.appProblems = append(a.appProblems, fmt.Sprintf("app `%s`: report `%s` is nil", a.name, name))
		return a
	}
	a.reports = append(a.reports, namedReport{name: name, report: report})
	return a
}

var migrationFileName = regexp.MustCompile(`^([0-9]+)_(.+)\.sql$`)

// Migrations adds the SQL data migrations in fsys: every file whose name ends in .sql, at any depth. A file
// is named NNNN_name.sql: the digits are the version (positive, at most 2147483647) and the rest is the
// name recorded in fb_app_migration; any other name is a startup error. Within one call the migrations
// are ordered by version; the versions must be strictly increasing over everything added, in call
// order. Other files (a README.md) are ignored.
//
//	//go:embed migrations/*.sql
//	var migrationsFS embed.FS
//
//	forgebase.NewApp("sales").Migrations(migrationsFS) // migrations/0001_backfill.sql, ...
//
// `forgebase migrate` applies them after the schema sync, once per database, each in its own
// transaction. The SQL runs with the database role of the binary: app code is trusted. It must not contain
// BEGIN or COMMIT, and an applied migration is never edited: its checksum is verified and a changed one
// is refused.
func (a *App) Migrations(fsys fs.FS) *App {
	files, problems := readFiles(fsys, ".sql")
	for _, p := range problems {
		a.appProblems = append(a.appProblems, fmt.Sprintf("app `%s`: Migrations: %s", a.name, p))
	}
	var found []MigrationFile
	for _, f := range files {
		match := migrationFileName.FindStringSubmatch(path.Base(f.path))
		if match == nil {
			a.appProblems = append(a.appProblems, fmt.Sprintf(
				"app `%s`: migration file %s must be named NNNN_name.sql (the digits are the version, for example 0001_backfill.sql)", a.name, f.path))
			continue
		}
		version, err := strconv.ParseInt(match[1], 10, 32)
		if err != nil {
			a.appProblems = append(a.appProblems, fmt.Sprintf(
				"app `%s`: migration file %s: the version %s is too large (the most is 2147483647)", a.name, f.path, match[1]))
			continue
		}
		if !utf8.ValidString(f.text) {
			a.appProblems = append(a.appProblems, fmt.Sprintf("app `%s`: %s: cannot read: stream did not contain valid UTF-8", a.name, f.path))
			continue
		}
		found = append(found, MigrationFile{Version: int32(version), Name: match[2], Path: f.path, SQL: f.text})
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].Version < found[j].Version })
	a.migrations = append(a.migrations, found...)
	return a
}

// sourceFile is a file read from an fs.FS.
type sourceFile struct {
	path string
	text string
}

// readFiles reads the files of fsys whose name ends in suffix, at any depth, in walk order, skipping
// names that start with a dot. The problems are returned as text; the readable files are returned
// whatever the problems.
func readFiles(fsys fs.FS, suffix string) ([]sourceFile, []string) {
	if fsys == nil {
		return nil, []string{"the file system is nil"}
	}
	var files []sourceFile
	var problems []string
	walkErr := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: cannot read: %s", p, ioErrorText(err)))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if p != "." && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(name, suffix) {
			return nil
		}
		data, readErr := fs.ReadFile(fsys, p)
		if readErr != nil {
			problems = append(problems, fmt.Sprintf("%s: cannot read: %s", p, ioErrorText(readErr)))
			return nil
		}
		files = append(files, sourceFile{path: p, text: string(data)})
		return nil
	})
	if walkErr != nil {
		problems = append(problems, fmt.Sprintf("cannot read the file system: %s", ioErrorText(walkErr)))
	}
	return files, problems
}

// Binary is a Forgebase binary: the apps compiled into it, and the command line that runs them.
//
//	func main() {
//		os.Exit(forgebase.New().
//			App(crm.App()). // first: sales depends on it
//			App(sales.App()).
//			Run())
//	}
//
// The apps are listed explicitly and in order: an app must come after every app it depends on (nothing
// is sorted), and the DocType hooks of an app that extends another run after the hooks of the app it
// extends. The stock `forgebase` binary has no apps and uses --doctypes DIR instead; a binary with apps
// accepts --doctypes DIR too, as one more app named `local` that depends on all the others.
type Binary struct {
	apps []*App
}

// NewBinary is a binary without apps; the root package exposes it as forgebase.New.
func NewBinary() *Binary { return &Binary{} }

// App adds an app after the ones added before.
func (b *Binary) App(a *App) *Binary {
	b.apps = append(b.apps, a)
	return b
}

// Check verifies that the apps fit together, without a database: valid and unique names, dependencies
// present and registered before their dependents, no DocType defined twice, valid DocType files, Links
// only within an app and its dependencies, hooks only for DocTypes of the app and its dependencies,
// increasing migration versions. Every problem is in the error, a *StartupError. Run does this at
// startup; an app can also call it in a test.
func (b *Binary) Check() error {
	if _, startup := compose(b.apps, ""); startup != nil {
		return startup
	}
	return nil
}

// Run runs the command line (os.Args without the program name) and returns the process exit code: 0 on
// success, 1 on a startup, runtime, validation or database error (the message goes to standard error,
// starting with `error:`), 2 on a usage error. Pass the result to os.Exit.
func (b *Binary) Run() int { return b.RunArgs(os.Args[1:]...) }

// RunArgs is like Run with the arguments given (without the program name). It never exits the process:
// help and usage errors are printed and returned as an exit code (0 and 2), so a test can call it in
// process. The environment and the standard streams are the process's.
func (b *Binary) RunArgs(args ...string) int {
	return Run(Options{
		Version: Version,
		Apps:    b.apps,
		Args:    args,
		Getenv:  os.Getenv,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	})
}

// DocTypePaths are the paths of the DocType files embedded in a, in order, for the guard of
// forgebase/testing.
func DocTypePaths(a *App) []string {
	paths := make([]string, len(a.doctypes))
	for i, f := range a.doctypes {
		paths[i] = f.Path
	}
	return paths
}

// FilePaths are the paths of the files of fsys whose name ends in suffix, by the rules of DocTypes and
// Migrations (any depth, walk order, names with a leading dot skipped), and the problems found on the way.
// It is the guard of forgebase/testing: the directory on disk is read with the rules the embedded set was.
func FilePaths(fsys fs.FS, suffix string) (paths []string, problems []string) {
	files, problems := readFiles(fsys, suffix)
	for _, f := range files {
		paths = append(paths, f.path)
	}
	return paths, problems
}
