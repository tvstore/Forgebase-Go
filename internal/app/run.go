package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/httpapi"
)

// Executing the commands (the Rust modules run.rs and lib.rs).
//
// Exit codes: 0 success; 1 runtime, validation, startup or database error (message on standard
// error, starting with `error:`); 2 usage error (the message, the usage and a hint on standard
// error).

// Options is what the command line needs from the process: its arguments (without the program name)
// and its environment and standard streams, so that a test can run a command in process.
type Options struct {
	// Version is printed by --version.
	Version string
	// Apps are the apps compiled into the binary, in order. The stock binary has none.
	Apps []*App
	Args []string
	// Getenv reads an environment variable; "" means not set.
	Getenv func(string) string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// notYet maps the commands that are not ported yet to the phase that brings them. The commands are
// parsed completely (so their usage errors and help are those of the Rust binary) and then refused.
var notYet = map[string]string{
	"customize":       "P7",
	"user permission": "P7",
}

// phaseOf is the phase that brings the command with this path, or "" when it exists.
func phaseOf(path []string) string {
	// The longest match wins: user permission is P7, the rest of user exists.
	for n := len(path); n > 0; n-- {
		if phase, ok := notYet[strings.Join(path[:n], " ")]; ok {
			return phase
		}
	}
	return ""
}

// Run executes the command line and returns the process exit code. It never exits the process.
func Run(o Options) int {
	m, exit := parse(rootCommand(), o.Version, o.Args)
	if exit != nil {
		out := o.Stderr
		if exit.stdout {
			out = o.Stdout
		}
		_, _ = io.WriteString(out, exit.text)
		return exit.code
	}
	// A binary without apps has nothing but --doctypes to learn DocTypes from.
	if len(o.Apps) == 0 && m.cmd.arg("doctypes") != nil && m.str("doctypes") == "" && !m.flag("empty") {
		return printExit(o, usageErr("the following required arguments were not provided:\n  --doctypes <DIR>",
			"Usage: "+helpUsage(withRequired(m.cmd, "doctypes"))))
	}
	// The one command that needs neither the apps nor a database nor the config file.
	if m.path[0] == "new-app" {
		created, err := newApp(m.str("name"), m.str("forgebase_path"), m.str("dir"))
		if err != nil {
			return fail(o, err)
		}
		r := &runner{o: o, m: m}
		if err := r.write(newAppReport(created)); err != nil {
			return fail(o, err)
		}
		return 0
	}
	if phase := phaseOf(m.path); phase != "" {
		return fail(o, fmt.Errorf("%s is not available yet in the Go port (arrives in phase %s)", strings.Join(m.path, " "), phase))
	}
	r := &runner{o: o, m: m}
	if err := r.run(context.Background()); err != nil {
		if isUsage(err) {
			return printExit(o, usageErr(err.Error(), "Usage: "+helpUsage(rootCommandLinked())))
		}
		return fail(o, err)
	}
	return 0
}

func rootCommandLinked() *command {
	root := rootCommand()
	root.link()
	return root
}

func printExit(o Options, exit *exitRequest) int {
	_, _ = io.WriteString(o.Stderr, exit.text)
	return exit.code
}

// fail prints the error and gives exit code 1.
func fail(o Options, err error) int {
	_, _ = fmt.Fprintf(o.Stderr, "error: %s\n", describeError(err))
	return 1
}

type runner struct {
	o Options
	m *matches
}

func (r *runner) write(text string) error {
	if _, err := io.WriteString(r.o.Stdout, text); err != nil {
		return errf("cannot write output: %s", ioErrorText(err))
	}
	return nil
}

// run executes the parsed command. A database command works on exactly one site, chosen from the
// config file and --site before anything else is done. The site gives the command its pool; there
// is no other way to reach a database.
func (r *runner) run(ctx context.Context) error {
	cfg, err := loadConfig(r.m.str("config"), r.o.Getenv, binaryApps(r.o.Apps))
	if err != nil {
		return err
	}
	// serve serves every site (or the one --site names); the other commands work on exactly one.
	if r.m.path[0] == "serve" {
		return r.serve(ctx, cfg)
	}
	// The site commands take the site as an argument, and use no --site.
	if r.m.path[0] == "site" {
		return r.site(ctx, cfg)
	}
	siteName, named := r.m.opt("site")
	site, err := cfg.selectSite(siteName, named)
	if err != nil {
		return err
	}
	switch r.m.path[0] {
	case "migrate":
		composed, err := r.siteComposed(site, r.m.str("doctypes"))
		if err != nil {
			return err
		}
		options := db.SyncOptions{
			DryRun:      r.m.flag("dry_run"),
			LockTimeout: millis(r.m.u64("lock_timeout_ms")),
		}
		text, err := r.migrateSite(ctx, site, composed, options)
		if text != "" {
			if werr := r.write(text); werr != nil {
				return werr
			}
		}
		return err
	case "doc":
		return r.doc(ctx, site)
	case "data":
		return r.data(ctx, site)
	case "user":
		return r.user(ctx, site)
	case "token":
		return r.token(ctx, site)
	case "job":
		return r.job(ctx, site)
	case "webhook":
		return r.webhook(ctx, site)
	case "worker":
		return r.worker(ctx, site)
	}
	return fmt.Errorf("unknown command %q", r.m.path[0])
}

// siteComposed is the apps installed on site plus the --doctypes directory (the app `local`, which
// depends on those apps), composed into the registry of that site. It composes first all the apps of the
// binary, so a problem in the binary or the directory is reported once, as for every command; then, when
// the site installs only some of the apps, those (a problem found only there names the site).
func (r *runner) siteComposed(site *site, local string) (*Composed, error) {
	all, startup := compose(r.o.Apps, local)
	if startup != nil {
		return nil, startup
	}
	if !site.appsListed {
		return all, nil
	}
	installed, startup := compose(site.installed(r.o.Apps), local)
	if startup != nil {
		return nil, startup.forSite(site.name)
	}
	return installed, nil
}

// binaryApps are the apps as the config file validation needs them: the names and the dependencies.
func binaryApps(apps []*App) []binaryApp {
	out := make([]binaryApp, 0, len(apps))
	for _, a := range apps {
		if a != nil {
			out = append(out, binaryApp{name: a.name, dependsOn: a.dependsOn})
		}
	}
	return out
}

// millis is a number of milliseconds as a duration, at most the longest duration (the sync clamps
// it further to what PostgreSQL accepts).
func millis(ms uint64) time.Duration {
	if ms > uint64(math.MaxInt64/int64(time.Millisecond)) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(ms) * time.Millisecond
}

// migrateSite migrates site with its composed DocTypes and returns the report. What was applied is
// returned with an error too (a refused fixture), so that the caller prints it first. The warnings
// are written to standard error: the apps recorded in the database that are not installed, and
// the documents of a workflow that are in a state it no longer defines.
func (r *runner) migrateSite(ctx context.Context, site *site, c *Composed, options db.SyncOptions) (string, error) {
	sc, err := site.open(ctx, r.o.Getenv, c.registry.Clone(), 0)
	if err != nil {
		return "", err
	}
	defer sc.close()
	report, err := c.Migrate(ctx, sc.pool, options)
	if err != nil {
		return "", err
	}
	recorded, err := db.RecordedApps(ctx, sc.pool)
	if err != nil {
		return "", err
	}
	for _, name := range recorded {
		if !c.hasApp(name) {
			_, _ = fmt.Fprintf(r.o.Stderr, "warning: site `%s`: app `%s` is recorded in the database but not installed; its tables and data are kept\n",
				sc.site.name, name)
		}
	}
	for _, warning := range report.WorkflowWarnings {
		_, _ = fmt.Fprintf(r.o.Stderr, "warning: site `%s`: %s\n", sc.site.name, warning)
	}
	text := migrateReport(report, options.DryRun, c.appCount())
	// What was applied is reported first (the migrations are committed), then the fixtures of an
	// app that were refused, which were rolled back.
	if report.FixturesFailure != nil {
		return text, report.FixturesFailure
	}
	// The reports read the tables the migrations create, so they are checked after them, and never in a
	// dry run (nothing was applied, so the tables may not be there).
	if reports := c.reportCount(); reports > 0 && !options.DryRun {
		problems, err := c.CheckReports(ctx, sc.pool)
		if err != nil {
			return text, err
		}
		if len(problems) > 0 {
			// What was applied is reported first: the migrations are committed.
			return text, &db.Error{Kind: db.ErrReportCheck, Details: problems}
		}
		text += fmt.Sprintf("reports: %d checked\n", reports)
	}
	return text, nil
}

// ---- doc ----------------------------------------------------------------------------------------

// docOperation is a document operation with its input already read and checked: the DocType, the
// files and the syntax of the JSON are checked before the database is opened, and the field names and
// values against the registry of the site context right after.
type docOperation struct {
	verb     string
	doctype  string
	name     string
	action   string
	version  int64
	actor    string
	text     string
	hasInput bool
	// The options of list: --filter, --sort (in order), --limit and --offset.
	filter    string
	hasFilter bool
	sort      []string
	limit     *uint32
	offset    *uint64
}

func (r *runner) doc(ctx context.Context, site *site) error {
	verb := r.m.path[1]
	op := &docOperation{
		verb:    verb,
		doctype: r.m.str("doctype"),
		name:    r.m.str("name"),
		actor:   r.m.str("actor"),
	}
	if verb == "transition" {
		op.action = r.m.str("action")
	}
	if verb == "list" {
		op.filter, op.hasFilter = r.m.opt("filter")
		op.sort = r.m.list("sort")
		if _, ok := r.m.opt("limit"); ok {
			limit := uint32(r.m.u64("limit"))
			op.limit = &limit
		}
		if _, ok := r.m.opt("offset"); ok {
			offset := r.m.u64("offset")
			op.offset = &offset
		}
	}
	// insert and get take no version.
	if _, ok := r.m.opt("expected_version"); ok {
		op.version = r.m.i64("expected_version")
	}
	// The DocType is looked up in the files (before the database is opened, so an unknown one never
	// touches it) and the input is read; the input is then checked against the registry of the site
	// context.
	composed, err := r.siteComposed(site, r.m.str("doctypes"))
	if err != nil {
		return err
	}
	if composed.registry.Get(op.doctype) == nil {
		return unknownDocType(composed.registry, op.doctype)
	}
	if verb == "insert" || verb == "update" {
		text, err := r.readData(r.m.str("data"))
		if err != nil {
			return err
		}
		// Syntax errors fail before the database is opened; field names and values are checked
		// later against the registry of the site context.
		if err := httpapi.CheckInputSyntax(text); err != nil {
			return err
		}
		op.text, op.hasInput = text, true
	}
	if op.hasFilter {
		// The same: the filters are checked against the registry later.
		if err := httpapi.CheckFiltersSyntax(op.filter); err != nil {
			return err
		}
	}
	sc, err := site.open(ctx, r.o.Getenv, composed.registry, 0)
	if err != nil {
		return err
	}
	defer sc.close()
	return r.execute(ctx, sc, op)
}

// readData is the JSON text of --data: the argument itself, or standard input for `-`.
func (r *runner) readData(arg string) (string, error) {
	if arg != "-" {
		return arg, nil
	}
	data, err := io.ReadAll(r.o.Stdin)
	if err != nil {
		return "", errf("cannot read standard input: %s", ioErrorText(err))
	}
	if !utf8.Valid(data) {
		return "", errf("cannot read standard input: stream did not contain valid UTF-8")
	}
	return string(data), nil
}

func unknownDocType(registry *core.Registry, name string) error {
	known := "none"
	if !registry.IsEmpty() {
		names := make([]string, 0, registry.Len())
		for _, d := range registry.Iter() {
			names = append(names, d.Name)
		}
		known = strings.Join(names, ", ")
	}
	return errf("unknown DocType `%s`; known DocTypes: %s", name, known)
}

// execute runs the operation against the registry of the site context. A command that only reads
// (get) runs as the actor `cli`.
func (r *runner) execute(ctx context.Context, sc *siteContext, op *docOperation) error {
	registry := sc.registry
	doctype := registry.Get(op.doctype)
	if doctype == nil {
		return unknownDocType(registry, op.doctype)
	}
	caller := core.SystemCaller(op.actor)
	var stored *db.StoredDocument
	var err error
	switch op.verb {
	case "insert":
		var input *httpapi.Input
		if input, err = httpapi.ParseInput(registry, doctype, op.text); err != nil {
			return err
		}
		stored, err = db.Insert(ctx, sc.pool, registry, op.doctype, core.Document{Values: input.Values, Tables: input.Tables}, caller)
	case "list":
		query := db.ListQuery{Limit: op.limit, Offset: op.offset}
		if op.hasFilter {
			if query.Filters, err = httpapi.ParseFilters(doctype, op.filter); err != nil {
				return err
			}
		}
		if query.Sort, err = httpapi.ParseSort(op.sort); err != nil {
			return err
		}
		documents, err := db.List(ctx, sc.pool, registry, op.doctype, core.SystemCaller("cli"), query)
		if err != nil {
			return err
		}
		return r.printDocuments(documents)
	case "get":
		stored, err = db.Get(ctx, sc.pool, registry, op.doctype, op.name, core.SystemCaller("cli"))
		if err == nil && stored == nil {
			err = &db.Error{Kind: db.ErrNotFound, DocType: op.doctype, Name: op.name}
		}
	case "update":
		var input *httpapi.Input
		if input, err = httpapi.ParseInput(registry, doctype, op.text); err != nil {
			return err
		}
		stored, err = db.Update(ctx, sc.pool, registry, op.doctype, op.name, op.version,
			core.Changes{Values: input.Values, Tables: input.Tables}, caller)
	case "submit":
		stored, err = db.Submit(ctx, sc.pool, registry, op.doctype, op.name, op.version, caller)
	case "cancel":
		stored, err = db.Cancel(ctx, sc.pool, registry, op.doctype, op.name, op.version, caller)
	case "transition":
		stored, err = db.Transition(ctx, sc.pool, registry, op.doctype, op.name, op.action, op.version, caller)
	case "amend":
		stored, err = db.Amend(ctx, sc.pool, registry, op.doctype, op.name, op.version, caller)
	case "delete":
		return db.Delete(ctx, sc.pool, registry, op.doctype, op.name, op.version, caller)
	default:
		return fmt.Errorf("unknown doc command %q", op.verb)
	}
	if err != nil {
		return err
	}
	return r.printDocument(stored)
}

// printDocuments writes a list of stored documents as a pretty JSON array.
func (r *runner) printDocuments(documents []*db.StoredDocument) error {
	objects := make([]any, len(documents))
	for i, doc := range documents {
		object, err := db.DocumentToJSON(doc)
		if err != nil {
			return outputError(err)
		}
		objects[i] = object
	}
	text, err := prettyJSON(objects)
	if err != nil {
		return errf("cannot serialize the document as JSON: %s", err)
	}
	return r.write(text + "\n")
}

// outputError is the error of a stored value that cannot be written as JSON.
func outputError(err error) error {
	var oe *db.OutputError
	if errors.As(err, &oe) {
		return errf("%s", oe)
	}
	return err
}

// printDocument writes a stored document as pretty JSON.
func (r *runner) printDocument(stored *db.StoredDocument) error {
	object, err := db.DocumentToJSON(stored)
	if err != nil {
		return outputError(err)
	}
	text, err := prettyJSON(object)
	if err != nil {
		return errf("cannot serialize the document as JSON: %s", err)
	}
	return r.write(text + "\n")
}

// prettyJSON is v as indented JSON as serde_json's to_string_pretty writes it: two spaces, keys in
// map order (sorted), no HTML escaping and U+2028 / U+2029 written as themselves.
func prettyJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	text := strings.TrimSuffix(buf.String(), "\n")
	return unescapeLineSeparators(text), nil
}

// unescapeLineSeparators turns the escapes   and   (which encoding/json always writes) into
// the characters, as serde_json does. An escaped backslash before the "u" is not an escape.
func unescapeLineSeparators(text string) string {
	if !strings.Contains(text, `\u202`) {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) {
			if strings.HasPrefix(text[i:], ` `) {
				b.WriteString(" ")
				i += 5
				continue
			}
			if strings.HasPrefix(text[i:], ` `) {
				b.WriteString(" ")
				i += 5
				continue
			}
			b.WriteByte(text[i])
			b.WriteByte(text[i+1])
			i++
			continue
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

// worker runs the jobs of the apps installed on site until Ctrl-C or SIGTERM (see worker.go). One worker
// process serves one site, and the scheduler runs in it, so only the schedules of the site's apps tick.
//
// The pool has concurrency + 2 connections: each running job holds one for its transaction, and a claim or
// the record of a failure needs one briefly while the handlers hold theirs. It is sized by the concurrency, not
// by the pool_size of the site.
func (r *runner) worker(ctx context.Context, site *site) error {
	composed, err := r.siteComposed(site, r.m.str("doctypes"))
	if err != nil {
		return err
	}
	concurrency := int(r.m.u64("concurrency"))
	sc, err := site.open(ctx, r.o.Getenv, composed.registry, concurrency+2)
	if err != nil {
		return err
	}
	defer sc.close()
	signals, stopSignals := stopSignals()
	defer stopSignals()
	w := newWorker(sc.pool, sc.registry, composed, defaultWorkerConfig(), r.o.Stderr)
	if w.runWorker(ctx, concurrency, workerID(), signals) {
		return nil
	}
	return errf("a worker slot panicked (see the log above)")
}
