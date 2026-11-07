// Package {{name}} is the {{name}} app: DocTypes, hooks and migrations. cmd/{{name}} puts it into a binary.
package {{name}}

import (
	"embed"
	"strings"

	"github.com/tvstore/forgebase-go"
)

// Every file under doctypes/ that ends in .toml is embedded (a test checks that the directory and the
// embedded set are the same).
//
//go:embed doctypes/*.toml
var doctypes embed.FS

// Data migrations (SQL, append-only): see migrations/README.md. Uncomment this and the Migrations call
// in App() when the first migration exists; the pattern needs at least one file.
//
//	//go:embed migrations/*.sql
//	var migrations embed.FS

// App is the app. A plain function that returns data: nothing is registered anywhere else.
func App() *forgebase.App {
	return forgebase.NewApp("{{name}}").
		Version("0.1.0").
		// To build on another app: DependsOn("crm"), and add crm before this app in cmd/{{name}}/main.go.
		DocTypes(doctypes).
		Hooks("{{name}}_note", NoteRules{})
	// .Migrations(migrations)
}

// NoteRules are the business rules of a note. Hooks are plain functions of the document: no database
// access. Embed forgebase.BaseHooks and override only the points you need.
type NoteRules struct{ forgebase.BaseHooks }

// Validate trims the title and refuses a blank one.
func (NoteRules) Validate(_ forgebase.HookContext, doc *forgebase.Document) error {
	// Hooks run before the framework's own validation, so a value may be missing or of another kind:
	// look before you change.
	if title, ok := doc.Values["title"]; ok && title.Kind == forgebase.KindText {
		trimmed := strings.TrimSpace(title.Text)
		if trimmed == "" {
			return forgebase.NewHookError("the title must not be blank")
		}
		doc.Values["title"] = forgebase.Text(trimmed)
	}
	return nil
}
