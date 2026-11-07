package app

import (
	"fmt"
	"io/fs"
	"strings"
	"unicode/utf8"

	"github.com/tvstore/forgebase-go/internal/core"
)

// App overlays (M10.1, ADR 0023; the Rust parts of app.rs and compose.rs): fields an app adds to a DocType of
// a dependency.

// ExtendDocType adds fields to a DocType of an app this app depends on (directly or through other apps), from
// the overlay files in fsys: every file whose name ends in .toml, at any depth, in the order fs.WalkDir
// visits them (lexical within each directory), with the path in fsys as its name in messages. Names that start
// with a dot are skipped. It is made for an embed.FS, like DocTypes and Fixtures:
//
//	//go:embed overlays/customer/*.toml
//	var customerOverlays embed.FS
//
//	forgebase.NewApp("sales_extension").
//		DependsOn("sales").
//		ExtendDocType("customer", customerOverlays) // files are "overlays/customer/tier.toml", ...
//
// Calling it again adds more files, for the same DocType or another. See ADR 0023.
//
// An overlay file holds [[fields]] entries, written exactly like those of a DocType file and with every
// property a field has (required, unique, read_only, allow_on_submit, ...) except that the type may not be
// table. The fields are appended after the fields of the DocType, in app order and then file order, so they get
// a column at migrate like any other field, and the hooks of this app (and of every other app) see them.
//
// Everything wrong with an overlay is a startup error that names this app and the file: an unknown target
// DocType; a target that no dependency of this app owns (the app's own DocTypes are refused too: put the field
// in the DocType's file); a child DocType; a Table field; a field name the DocType, an earlier overlay or
// another app's overlay has already (the message names both apps); a name that starts with custom_, a prefix
// reserved for site custom fields (an app's own DocType fields may not start with it either).
//
// Two cautions for the author of the app. A required field added to a DocType that already has documents makes
// the next update of each of them fail until the field is filled in. A unique field creates a unique index over
// the existing rows (the rows hold NULL for it, and NULLs never collide).
func (a *App) ExtendDocType(doctype string, fsys fs.FS) *App {
	files, problems := readFiles(fsys, ".toml")
	for _, f := range files {
		if !utf8.ValidString(f.text) {
			a.fileProblems = append(a.fileProblems, fmt.Sprintf("app `%s`: %s: cannot read: stream did not contain valid UTF-8", a.name, f.path))
			continue
		}
		a.overlays = append(a.overlays, overlayDef{target: doctype, path: f.path, toml: f.text})
	}
	for _, p := range problems {
		a.appProblems = append(a.appProblems, fmt.Sprintf("app `%s`: ExtendDocType: %s", a.name, p))
	}
	return a
}

// overlay is one overlay file of an app, parsed.
type overlay struct {
	app    string
	path   string
	target string
	fields []core.DocField
}

// parseOverlays parses the overlay files of the apps, in app order and then registration order. A file that
// does not parse is a file problem naming the app and the file.
func parseOverlays(apps []*App, errs *StartupError) []overlay {
	var parsed []overlay
	for _, a := range apps {
		if a == nil {
			continue
		}
		for _, o := range a.overlays {
			fields, err := core.ParseOverlay(o.toml)
			if err != nil {
				errs.files = append(errs.files, fmt.Sprintf("app `%s`: %s: %s", a.name, o.path, err))
				continue
			}
			parsed = append(parsed, overlay{app: a.name, path: o.path, target: o.target, fields: fields})
		}
	}
	return parsed
}

// applyOverlays adds the fields of the overlays to their target DocTypes, in the order of overlays. An overlay
// with any problem adds nothing; all of its problems are reported, each naming the app and the file. A field
// name that is taken names both apps. fieldFiles records, for every field added, the app and the file that
// added it.
func applyOverlays(overlays []overlay, reach map[string]map[string]bool, definedIn map[string][2]string, registry *core.Registry, fieldFiles map[[2]string][2]string, errs *StartupError) {
	for _, o := range overlays {
		problem := func(text string) string { return fmt.Sprintf("app `%s`: %s: %s", o.app, o.path, text) }
		owner, ok := registry.Owner(o.target)
		if !ok {
			errs.apps = append(errs.apps, problem(fmt.Sprintf(
				"overlay for unknown DocType `%s` (it must be a DocType of an app that `%s` depends on)", o.target, o.app)))
			continue
		}
		if owner == o.app {
			errs.apps = append(errs.apps, problem(fmt.Sprintf(
				"overlay for `%s`, a DocType of app `%s` itself; add the field to the DocType's own file", o.target, o.app)))
			continue
		}
		if !reach[o.app][owner] {
			errs.apps = append(errs.apps, problem(fmt.Sprintf(
				"overlay for `%s`, which belongs to app `%s`; app `%s` does not depend on `%s` (add DependsOn(%q), registered before `%s`)",
				o.target, owner, o.app, owner, owner, o.app)))
			continue
		}
		if d := registry.Get(o.target); d != nil && d.IsChild {
			errs.apps = append(errs.apps, problem(fmt.Sprintf(
				"overlay for `%s`, a child DocType; overlays extend DocTypes with documents of their own", o.target)))
			continue
		}
		var problems []string
		for _, field := range o.fields {
			switch {
			case field.Type.Kind == core.FieldTable:
				problems = append(problems, fmt.Sprintf("field `%s` has type table; an overlay cannot add a Table field", field.Name))
			case strings.HasPrefix(field.Name, core.CustomPrefix):
				problems = append(problems, fmt.Sprintf(
					"field `%s` starts with `%s`, a prefix reserved for site custom fields", field.Name, core.CustomPrefix))
			default:
				if d := registry.Get(o.target); d != nil && d.Field(field.Name) != nil {
					first, found := fieldFiles[[2]string{o.target, field.Name}]
					if !found {
						first, found = definedIn[o.target]
					}
					if !found {
						first = [2]string{"?", "?"}
					}
					problems = append(problems, fmt.Sprintf(
						"field `%s` of doctype `%s` is already defined by app `%s` (%s); app `%s` cannot add a field of the same name",
						field.Name, o.target, first[0], first[1], o.app))
				}
			}
		}
		if len(problems) > 0 {
			for _, p := range problems {
				errs.apps = append(errs.apps, problem(p))
			}
			continue
		}
		err := registry.AddOverlayFields(o.app, o.target, o.fields)
		if err == nil {
			for _, field := range o.fields {
				fieldFiles[[2]string{o.target, field.Name}] = [2]string{o.app, o.path}
			}
			continue
		}
		if oe, ok := err.(core.OverlayError); ok && oe.Kind == core.OverlayInvalid {
			for _, p := range oe.Problems {
				errs.apps = append(errs.apps, problem(fmt.Sprintf("invalid overlay for `%s`: %s", o.target, p.Error())))
			}
			continue
		}
		errs.apps = append(errs.apps, problem(err.Error()))
	}
}
