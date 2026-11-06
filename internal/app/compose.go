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

// Putting the DocTypes together: loading them into one registry and migrating them (the Rust module
// compose.rs).
//
// The stock binary has no apps, so the only source is the --doctypes directory, which is the app
// `local`. The app model (the App builder, depends_on, app migrations, overlays, hooks, jobs, reports
// and fixtures) arrives in P4 and P5: load already takes the app name, and compose is the place where
// the apps of a binary are looped over, so adding them does not change the callers.
//
// Every problem found is collected and reported together. The reference checks only run when every
// DocType file loaded, because a file that did not load would make them report follow-up errors.

// localApp is the name of the app that the --doctypes directory forms.
const localApp = "local"

func validUTF8(b []byte) bool { return utf8.Valid(b) }

// StartupError is everything wrong with the DocType files (and, from P4, with the apps of a
// binary), found at startup. All problems are collected, so one run shows them all.
//
// It prints as up to two lists: "invalid app setup" (names, dependencies, order, ownership, hooks,
// migrations, links between apps) and "cannot load DocType files" (a file that cannot be read or
// parsed, an invalid DocType, a Link to a DocType nobody defines, each naming its file).
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

// composed is the DocTypes of a binary, composed: one registry and what migrate needs.
type composed struct {
	registry *core.Registry
	// apps are the registered apps (not local) in app order; none until P4.
	apps []string
}

// appCount is how many apps were composed (the --doctypes directory is not counted).
func (c *composed) appCount() int { return len(c.apps) }

// migrate runs the framework migrations and the schema sync (db.Migrate). The app migrations, the
// schedules and the fixtures of apps are added here in P4 and P5.
func (c *composed) migrate(ctx context.Context, pool *pgxpool.Pool, options db.SyncOptions) (*db.MigrateReport, error) {
	return db.Migrate(ctx, pool, c.registry, options)
}

// compose loads the DocType files of the directory local (when it is not empty) as the app
// `local`. The Rust compose also takes the apps of the binary first; with none, the registry is
// the directory's.
func compose(local string) (*composed, *StartupError) {
	errs := &StartupError{}
	registry := core.NewRegistry()
	// DocType name -> (app, file) that defined it.
	definedIn := map[string][2]string{}
	if local != "" {
		files, problems := readDir(local)
		errs.files = append(errs.files, problems...)
		load(localApp, files, registry, definedIn, errs)
	}
	// Only when everything loaded: a Link to a DocType from a broken file would be reported twice.
	if len(errs.files) == 0 {
		checkReferences(registry, definedIn, errs)
	}
	if !errs.isEmpty() {
		return nil, errs
	}
	return &composed{registry: registry}, nil
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

// checkReferences runs the reference rules of the registry, each problem naming the file of the
// DocType it is about. (The rule that a Link or Table may only target a DocType of its own app or of
// a dependency arrives with the apps in P4.)
func checkReferences(registry *core.Registry, definedIn map[string][2]string, errs *StartupError) {
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
}
