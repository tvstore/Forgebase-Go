package db

import (
	"context"
	"fmt"
	"sort"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Link titles (UX.4): the title of the documents that Link values point to, so that the desk can show
// "Ada Lovelace" instead of "CUST-0001" (the Rust module link_titles.rs).
//
// The titles follow the caller's read rules, exactly like List: a target DocType the caller cannot
// read at all gets no entry, and for an if_owner read the statement is limited to the caller's own
// documents ("owner" = $2), so a title never reveals a document the caller could not fetch. Nothing is
// decided here: the access comes from the one evaluator in internal/core, and the owner condition and
// the user permissions (M10.3, ADR 0025: a document outside the caller's scope has no title) are the
// ones List and Get use.
//
// One statement per target DocType (never one per row):
// SELECT name, <title> FROM <target> WHERE name = ANY($1).

// LinkTitleMap is the titles by target DocType and then by document name:
// {"customer": {"CUST-0001": "Ada"}}. A target the caller cannot read, a target without a title_field,
// and a document without a title have no entry.
type LinkTitleMap map[string]map[string]string

// collectLinks are the Link values of documents (of doctype), grouped by target DocType; the Link
// fields of the rows of Table fields are included. The names are deduplicated and at most MaxLimit per
// target are kept (a page is at most that many documents; a title that is left out only means the desk
// shows the name).
func collectLinks(registry *core.Registry, doctype *core.DocType, documents []*StoredDocument) map[string]map[string]struct{} {
	found := map[string]map[string]struct{}{}
	add := func(target string, value core.Value, ok bool) {
		if !ok || value.Kind != core.KindText {
			return
		}
		names := found[target]
		if names == nil {
			names = map[string]struct{}{}
			found[target] = names
		}
		if len(names) < int(MaxLimit) {
			names[value.Text] = struct{}{}
		}
	}
	for i := range doctype.Fields {
		field := &doctype.Fields[i]
		switch field.Type.Kind {
		case core.FieldLink:
			for _, d := range documents {
				v, ok := d.Values[field.Name]
				add(field.Type.Target, v, ok)
			}
		case core.FieldTable:
			child := registry.Get(field.Type.Target)
			if child == nil {
				continue
			}
			for j := range child.Fields {
				childField := &child.Fields[j]
				if childField.Type.Kind != core.FieldLink {
					continue
				}
				for _, d := range documents {
					for _, row := range d.Tables[field.Name] {
						v, ok := row[childField.Name]
						add(childField.Type.Target, v, ok)
					}
				}
			}
		}
	}
	return found
}

// LinkTitles are the titles of the documents that documents (of doctype) link to, for the targets that
// have a title_field and that the caller may read. See the comment above.
//
// documents are as List or Get return them (Link values are the targets' names). A caller who may not
// read a target gets nothing for it, not an error.
func LinkTitles(ctx context.Context, q Queryer, registry *core.Registry, caller core.Caller, doctype string, documents []*StoredDocument) (LinkTitleMap, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	return resolveLinkTitles(ctx, q, registry, caller, collectLinks(registry, dt, documents))
}

// resolveLinkTitles are the titles for names grouped by target DocType, under the caller's read rules.
// Shared with the Link columns of a report (P6).
func resolveLinkTitles(ctx context.Context, q Queryer, registry *core.Registry, caller core.Caller, found map[string]map[string]struct{}) (LinkTitleMap, error) {
	titles := LinkTitleMap{}
	targets := make([]string, 0, len(found))
	for target := range found {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		targetType := registry.Get(target)
		if targetType == nil || targetType.TitleField == nil {
			continue
		}
		access := core.Evaluate(targetType, caller, core.RightRead)
		if access == core.AccessDenied {
			continue
		}
		owner, owned := ownerScope(access, caller)
		scope := readScope{ownerOnly: owned, owner: owner, rows: core.RowScopeOf(targetType, caller)}
		names := make([]string, 0, len(found[target]))
		for name := range found[target] {
			names = append(names, name)
		}
		sort.Strings(names)
		got, err := queryTitles(ctx, q, registry, targetType, names, scope)
		if err != nil {
			return nil, err
		}
		if len(got) > 0 {
			titles[target] = got
		}
	}
	return titles, nil
}

func queryTitles(ctx context.Context, q Queryer, registry *core.Registry, target *core.DocType, names []string, scope readScope) (map[string]string, error) {
	table := QuoteIdent(core.TableName(target.Name))
	titleField := *target.TitleField
	column := QuoteIdent(titleField)
	// title_field was checked against the DocType's fields when it was loaded. A Link title is shown as
	// the name of what it points to, like every Link value; anything else as text.
	title := fmt.Sprintf("%s.%s::text", table, column)
	if f := target.Field(titleField); f != nil && f.Type.Kind == core.FieldLink {
		if registry.Get(f.Type.Target) == nil {
			return map[string]string{}, nil
		}
		title = fmt.Sprintf("(SELECT t.\"name\" FROM %s t WHERE t.\"id\" = %s.%s)",
			QuoteIdent(core.TableName(f.Type.Target)), table, column)
	}
	scoped := scope.andSQLFor(table, 2)
	sql := fmt.Sprintf("SELECT %s.\"name\", %s FROM %s WHERE %s.\"name\" = ANY($1::text[])%s", table, title, table, table, scoped.sql)
	args := append([]any{names}, scoped.args()...)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	found := map[string]string{}
	for rows.Next() {
		var name string
		var title *string
		if err := rows.Scan(&name, &title); err != nil {
			return nil, wrap(err)
		}
		if title != nil && *title != "" {
			found[name] = *title
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}
	return found, nil
}
