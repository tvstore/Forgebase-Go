package app

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/httpapi"
)

// Fixtures at startup (M9.4, ADR 0022; the Rust module fixtures.rs of the forgebase crate): reading the files of
// the apps, checking them and turning each record into what internal/db applies at migrate.
//
// A problem is collected with the other app problems (StartupError) and names the app, the file and the record.
// Only what can be decided without the database is checked here: the file, the DocType and the rules for it,
// the identity of every record and that every value has the JSON type of its field. Whether a value is valid
// (required fields, lengths, options, links) is decided when the record is applied, by the document pipeline,
// because a before_insert or validate hook may fill or change fields. The canonical JSON and the checksum are
// in internal/httpapi (canonical.go), next to the parser whose numbers they write.

// FixtureFile is a fixture file of an app: its path (as shown in messages: the path in the file system the app
// was given, for an embed.FS "fixtures/customer_group.json") and its JSON text.
type FixtureFile struct {
	Path string
	JSON string
}

// Fixtures adds the fixture files in fsys: every file whose name ends in .json, at any depth, in the order
// fs.WalkDir visits them (lexical within each directory), with the path in fsys as its name in messages. Names
// that start with a dot are skipped. It is made for an embed.FS, like DocTypes:
//
//	//go:embed fixtures/*.json
//	var fixturesFS embed.FS
//
//	forgebase.NewApp("sales").Fixtures(fixturesFS) // files are "fixtures/customer_group.json", ...
//
// A fixture file holds the records of one DocType ({"doctype": "...", "records": [...]}, each record shaped like
// the body of POST /api/v1/resource/<doctype>). `forgebase migrate` applies them after the app migrations, in app
// order, then file order (the lexical order above: name the files 1_x.json, 2_y.json to choose it), then record
// order, through the document pipeline as the system caller fixture:<app>, in one transaction per app. A record
// is inserted if it is missing; it is updated only when the app ships a new version of that record, so edits
// made on the site survive until then; nothing is ever deleted. See ADR 0022 for the exact rules.
//
// Only a DocType with field naming may have fixtures (the name is the identity of a record); a DocType with a
// workflow, a submittable one and a child DocType are refused, and so are DocTypes of apps this app does not
// depend on. Every problem is a startup error, like the other mistakes of an app. Calling it again adds more
// files.
func (a *App) Fixtures(fsys fs.FS) *App {
	files, problems := readFiles(fsys, ".json")
	for _, f := range files {
		if !utf8.ValidString(f.text) {
			a.fileProblems = append(a.fileProblems, fmt.Sprintf("app `%s`: %s: cannot read: stream did not contain valid UTF-8", a.name, f.path))
			continue
		}
		a.fixtures = append(a.fixtures, FixtureFile{Path: f.path, JSON: f.text})
	}
	for _, p := range problems {
		a.appProblems = append(a.appProblems, fmt.Sprintf("app `%s`: Fixtures: %s", a.name, p))
	}
	return a
}

// checkFixtures checks the fixtures of apps and returns the records of each app, in app order (the result has one
// entry per app, empty for an app without fixtures). reach has, per app, the apps it depends on, directly or
// not.
func checkFixtures(apps []*App, reach map[string]map[string]bool, registry *core.Registry, errs *StartupError) [][]db.FixtureRecord {
	// (doctype, name) -> the app that has the record.
	identities := map[[2]string]string{}
	result := make([][]db.FixtureRecord, len(apps))
	for i, a := range apps {
		if a == nil {
			continue
		}
		var records []db.FixtureRecord
		// DocType -> the file of this app that has its records.
		filesOf := map[string]string{}
		for _, file := range a.fixtures {
			problem := func(text string) string { return fmt.Sprintf("app `%s`: %s: %s", a.name, file.Path, text) }
			parsed, perr := parseFixtureFile(file.JSON)
			if perr != "" {
				errs.apps = append(errs.apps, problem(perr))
				continue
			}
			doctype, derr := usableFixtureDocType(a.name, parsed.doctype, reach, registry)
			if derr != "" {
				errs.apps = append(errs.apps, problem(derr))
				continue
			}
			if first, taken := filesOf[doctype.Name]; taken {
				errs.apps = append(errs.apps, problem(fmt.Sprintf(
					"doctype `%s` already has fixtures in %s; use one file per DocType", doctype.Name, first)))
				continue
			}
			filesOf[doctype.Name] = file.Path
			namingField := doctype.Naming.Field
			seen := map[string]bool{}
			for position, record := range parsed.records {
				index := position + 1
				recordProblem := func(name, text string) string {
					if name != "" {
						return problem(fmt.Sprintf("record %d (`%s`): %s", index, name, text))
					}
					return problem(fmt.Sprintf("record %d: %s", index, text))
				}
				object, ok := record.(map[string]any)
				if !ok {
					errs.apps = append(errs.apps, recordProblem("", "must be a JSON object"))
					continue
				}
				name, ok := object[namingField].(string)
				if !ok || strings.TrimSpace(name) == "" {
					errs.apps = append(errs.apps, recordProblem("", fmt.Sprintf(
						"must have a non-empty text value for `%s`, the field that names a `%s` document", namingField, doctype.Name)))
					continue
				}
				if seen[name] {
					errs.apps = append(errs.apps, recordProblem(name, "appears twice in this app"))
					continue
				}
				seen[name] = true
				if other, taken := identities[[2]string{doctype.Name, name}]; taken {
					errs.apps = append(errs.apps, recordProblem(name, fmt.Sprintf(
						"`%s %s` is also a fixture of app `%s`; a record belongs to one app", doctype.Name, name, other)))
					continue
				}
				if numbers := httpapi.NumberProblems(record); len(numbers) > 0 {
					errs.apps = append(errs.apps, recordProblem(name, strings.Join(numbers, "; ")))
					continue
				}
				if text := fixtureFieldProblems(registry, doctype, object); text != "" {
					errs.apps = append(errs.apps, recordProblem(name, text))
					continue
				}
				input, err := httpapi.InputFromJSON(registry, doctype, record)
				if err != nil {
					errs.apps = append(errs.apps, recordProblem(name, err.Error()))
					continue
				}
				identities[[2]string{doctype.Name, name}] = a.name
				records = append(records, db.FixtureRecord{
					DocType:  doctype.Name,
					File:     file.Path,
					Index:    index,
					Name:     name,
					Checksum: httpapi.FixtureChecksum(record),
					Document: core.Document{Values: input.Values, Tables: input.Tables},
				})
			}
		}
		result[i] = records
	}
	return result
}

// parsedFixtureFile is the content of a fixture file.
type parsedFixtureFile struct {
	doctype string
	records []any
}

func parseFixtureFile(text string) (*parsedFixtureFile, string) {
	v, err := httpapi.ParseJSON(text)
	if err != nil {
		return nil, "invalid JSON: " + err.Error()
	}
	object, ok := v.(map[string]any)
	if !ok {
		return nil, `must be a JSON object: {"doctype": "...", "records": [...]}`
	}
	doctype, ok := object["doctype"].(string)
	if !ok {
		return nil, "needs a `doctype` (the name of the DocType, as text)"
	}
	records, ok := object["records"].([]any)
	if !ok {
		return nil, "needs `records` (a list of objects)"
	}
	delete(object, "doctype")
	delete(object, "records")
	if len(object) > 0 {
		keys := make([]string, 0, len(object))
		for k := range object {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, fmt.Sprintf("unknown key `%s`; a fixture file has only `doctype` and `records`", keys[0])
	}
	return &parsedFixtureFile{doctype: doctype, records: records}, ""
}

// usableFixtureDocType is the DocType a fixture file is for, if the rules allow fixtures for it; else the
// reason.
func usableFixtureDocType(app, name string, reach map[string]map[string]bool, registry *core.Registry) (*core.DocType, string) {
	doctype := registry.Get(name)
	if doctype == nil {
		return nil, fmt.Sprintf("doctype `%s` is not defined by any app", name)
	}
	owner, _ := registry.Owner(name)
	if owner != app && !reach[app][owner] {
		return nil, fmt.Sprintf("doctype `%s` belongs to app `%s`, which app `%s` does not depend on", name, owner, app)
	}
	switch {
	case doctype.IsChild:
		return nil, fmt.Sprintf("doctype `%s` is a child DocType: its rows belong to a parent document; ship the parent's fixture with the rows in its table field", name)
	case doctype.Submittable:
		return nil, fmt.Sprintf("doctype `%s` is submittable: a fixture update must not fight the document lifecycle (draft, submitted, cancelled)", name)
	case doctype.Workflow != nil:
		return nil, fmt.Sprintf("doctype `%s` has a workflow: a fixture update must not fight the workflow, which owns the state of its documents", name)
	}
	switch doctype.Naming.Kind {
	case core.NamingField:
		return doctype, ""
	case core.NamingSeries:
		return nil, fmt.Sprintf("doctype `%s` uses a naming series: a fixture record needs a name that is the same on every site; use field naming (naming = { field = \"...\" })", name)
	}
	return nil, fmt.Sprintf("doctype `%s` uses random naming: a fixture record needs a name that is the same on every site; use field naming (naming = { field = \"...\" })", name)
}

// fixtureFieldProblems is what is wrong with the keys of a record and of the rows of its Table fields: a key
// that is not a field of the DocType (of the child DocType, for a row), and a key the pipeline would refuse from
// a caller (a read_only field). "" when there is nothing.
func fixtureFieldProblems(registry *core.Registry, doctype *core.DocType, record map[string]any) string {
	var problems []string
	fixtureKeyProblems(doctype, record, "", &problems)
	for _, key := range sortedMapKeys(record) {
		field := doctype.Field(key)
		if field == nil || field.Type.Kind != core.FieldTable {
			continue
		}
		child := registry.Get(field.Type.Target)
		rows, ok := record[key].([]any)
		if child == nil || !ok {
			continue
		}
		for i, row := range rows {
			if object, ok := row.(map[string]any); ok {
				fixtureKeyProblems(child, object, fmt.Sprintf("`%s` row %d: ", key, i+1), &problems)
			}
		}
	}
	return strings.Join(problems, "; ")
}

func fixtureKeyProblems(doctype *core.DocType, object map[string]any, prefix string, problems *[]string) {
	for _, key := range sortedMapKeys(object) {
		switch field := doctype.Field(key); {
		case field == nil:
			*problems = append(*problems, fmt.Sprintf("%s`%s` is not a field of `%s`", prefix, key, doctype.Name))
		case field.ReadOnly:
			*problems = append(*problems, fmt.Sprintf("%s`%s` is a read_only field: only hooks may write it", prefix, key))
		}
	}
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fixtureRecords are the fixture records of the app name, in the order they are applied (none for an app
// without fixtures or an unknown one).
func (c *Composed) fixtureRecords(name string) []db.FixtureRecord {
	for _, a := range c.apps {
		if a.name == name {
			return a.fixtures
		}
	}
	return nil
}
