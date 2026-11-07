package httpapi

import (
	"net/http"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// The metadata routes for the desk: what a user may see of the DocTypes (GET /api/v1/meta and GET
// /api/v1/meta/{doctype}; the Rust module meta.rs). Only metadata, never documents; the caller's rights
// come from the one evaluator of internal/core, and the DocType's [[permissions]] rules (which name
// roles) are never sent. A field the caller cannot read (perm_level, ADR 0024) is left out, and a field
// they can read but not write is reported with read_only: true.

// accessName is "all", "own" (only documents the caller owns) or "none".
func accessName(a core.Access) string {
	switch a {
	case core.AccessAll:
		return "all"
	case core.AccessOwnerOnly:
		return "own"
	}
	return "none"
}

// rights is the caller's rights on doctype, one entry per right. Submit, cancel and amend are "none"
// for a DocType that is not submittable (the API refuses them with not_submittable even for an
// administrator, so the desk must not offer them). Create and amend are "none" for a user restricted
// on the DocType itself (user permissions, ADR 0025).
func rights(doctype *core.DocType, caller core.Caller) map[string]any {
	out := map[string]any{}
	for _, right := range core.AllRights {
		lifecycle := right == core.RightSubmit || right == core.RightCancel || right == core.RightAmend
		makesNew := right == core.RightCreate || right == core.RightAmend
		access := core.AccessDenied
		if !(lifecycle && !doctype.Submittable) && !(makesNew && !core.MayMakeDocuments(doctype, caller)) {
			access = core.Evaluate(doctype, caller, right)
		}
		out[right.Name()] = accessName(access)
	}
	return out
}

func namingName(n core.Naming) string {
	switch n.Kind {
	case core.NamingField:
		return "field:" + n.Field
	case core.NamingSeries:
		return "series"
	}
	return "random"
}

// field is one field: name, label, type, flags (required, unique, allow_on_submit, read_only) and the
// type's parameters.
func field(f *core.DocField) map[string]any {
	m := map[string]any{
		"name":            f.Name,
		"label":           f.Label,
		"type":            f.Type.Name(),
		"required":        f.Required,
		"unique":          f.Unique,
		"allow_on_submit": f.AllowOnSubmit,
		"read_only":       f.ReadOnly,
	}
	switch f.Type.Kind {
	case core.FieldData:
		m["max_length"] = f.Type.MaxLength
	case core.FieldDecimal:
		m["precision"] = f.Type.Precision
		m["scale"] = f.Type.Scale
	case core.FieldSelect:
		m["options"] = append([]string{}, f.Type.Options...)
	case core.FieldLink, core.FieldTable:
		m["target"] = f.Type.Target
	}
	return m
}

// workflow is the workflow for the desk (M8.1): the states with their docstatus and badge style, and
// the transitions. The roles of the transitions and the edit_roles of the states are never sent, like
// the [[permissions]] rules: GET .../transitions says what the caller may do.
func workflow(doctype *core.DocType) any {
	if doctype.Workflow == nil {
		return nil
	}
	states := make([]any, len(doctype.Workflow.States))
	for i, s := range doctype.Workflow.States {
		states[i] = map[string]any{"name": s.Name, "docstatus": s.DocStatus.Int16(), "style": s.Style.Name()}
	}
	transitions := make([]any, len(doctype.Workflow.Transitions))
	for i, t := range doctype.Workflow.Transitions {
		transitions[i] = map[string]any{"action": t.Action, "from": t.From, "to": t.To}
	}
	return map[string]any{"states": states, "transitions": transitions}
}

// fieldWithChildren is a field of the DocType; a Table field also carries the field list of its child
// DocType (fields), so the desk needs one request per DocType.
func fieldWithChildren(registry *core.Registry, f *core.DocField) map[string]any {
	m := field(f)
	if f.Type.Kind == core.FieldTable {
		rows := []any{}
		// The registry was checked at startup, so the child exists.
		if child := registry.Get(f.Type.Target); child != nil {
			for i := range child.Fields {
				rows = append(rows, field(&child.Fields[i]))
			}
		}
		m["fields"] = rows
	}
	return m
}

func ownerOf(registry *core.Registry, doctype string) any {
	if owner, ok := registry.Owner(doctype); ok {
		return owner
	}
	return nil
}

func optionalString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// metaList is GET /api/v1/meta: the non-child DocTypes the caller can read, sorted by name, each with
// name, label, app (the owning app, null when the registry records none), icon (null or a name from the
// DocType icon list), submittable and the caller's rights.
func metaList(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	caller := auth.caller()
	registry := s.snapshot()
	items := []any{}
	for _, d := range registry.Iter() {
		if d.IsChild || core.Evaluate(d, caller, core.RightRead) == core.AccessDenied {
			continue
		}
		items = append(items, map[string]any{
			"name":        d.Name,
			"label":       d.Label,
			"app":         ownerOf(registry, d.Name),
			"icon":        optionalString(d.Icon),
			"submittable": d.Submittable,
			"rights":      rights(d, caller),
		})
	}
	writeData(w, http.StatusOK, items)
	return nil
}

// metaRead is GET /api/v1/meta/{doctype}: the UI metadata of one DocType. An unknown or child DocType
// is 404; one the caller cannot read is 403 permission_denied (DocType level, like the resource
// routes).
func metaRead(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(1)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	caller := auth.caller()
	if core.Evaluate(doctype, caller, core.RightRead) == core.AccessDenied {
		return &db.Error{Kind: db.ErrPermissionDenied, DocType: doctype.Name, Right: core.RightRead}
	}
	// Field levels (M10.2, ADR 0024): a field the caller cannot read is not in the metadata at all, and
	// one they can read but not write is read_only (the desk never sends such a field and shows it
	// read-only). Every response is no-store, so the per-caller answer is never cached.
	access := core.FieldAccessOf(doctype, caller)
	fields := []any{}
	for _, f := range core.ReadableFields(doctype, access) {
		m := fieldWithChildren(registry, f)
		if f.PermLevel > 0 && !access.CanWrite(f.PermLevel) {
			m["read_only"] = true
		}
		fields = append(fields, m)
	}
	var listFields any
	if doctype.ListFields != nil {
		listFields = append([]string{}, doctype.ListFields...)
	}
	writeData(w, http.StatusOK, map[string]any{
		"name":        doctype.Name,
		"label":       doctype.Label,
		"app":         ownerOf(registry, doctype.Name),
		"icon":        optionalString(doctype.Icon),
		"naming":      namingName(doctype.Naming),
		"submittable": doctype.Submittable,
		"title_field": optionalString(doctype.TitleField),
		"list_fields": listFields,
		"fields":      fields,
		"rights":      rights(doctype, caller),
		"workflow":    workflow(doctype),
	})
	return nil
}
