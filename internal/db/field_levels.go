package db

import (
	"github.com/tvstore/forgebase-go/internal/core"
)

// Field permission levels in the document operations (M10.2, ADR 0024). The Rust module
// field_levels.rs.
//
// The decision is made by core.FieldAccessOf, one pure function; this file only applies it. A field
// the caller cannot read is unknown to the caller:
//
//   - the read paths (get, list, summary, export, saved views, the metadata the HTTP layer uses to
//     read input and filters) work on core.ReadableDocType, the DocType without the hidden fields,
//     so a hidden field is neither selected nor accepted in a filter, a sort, a group-by or an
//     aggregate, and the error is the very one of a name that does not exist;
//   - the write paths work on the whole DocType (hooks see every field and the stored document is
//     merged, so a lower-level user cannot wipe hidden data): checkInput first refuses the
//     caller's input for a hidden field as unknown and for a readable but unwritable field as
//     core.ErrNotPermitted, then redact removes the hidden fields from the document that is
//     returned, and scrub removes their names from errors.
//
// Nothing here runs for a DocType without perm_level or for the system context and an
// administrator, so those behave exactly as before.

// restricted is the caller's field access if it matters for doctype: nil for a DocType without
// perm_level and for a caller that can use every level.
func restricted(doctype *core.DocType, access core.FieldAccess) *core.FieldAccess {
	if doctype.HasFieldLevels() && !access.IsAll() {
		return &access
	}
	return nil
}

// levelsOf is the caller's field levels when they matter.
func levelsOf(doctype *core.DocType, caller core.Caller) *core.FieldAccess {
	return restricted(doctype, core.FieldAccessOf(doctype, caller))
}

// checkInput refuses the caller's input (values and tables, a new document's or an update's
// changes) for fields the caller may not use, before any hook runs. stored is the document being
// updated (nil for an insert).
//
//   - A name that is not a field or a field the caller cannot read is core.ErrUnknownField,
//     whatever the value (null too, and even when it equals the stored value): the two cannot be
//     told apart. For a DocType with levels the check of unknown names therefore runs here, early
//     and alone, for every caller it applies to, so the answer for a hidden field is the answer for
//     a missing one.
//   - A field the caller can read but not write is core.ErrNotPermitted when the input would
//     change it: a non-null value on an insert, a value that differs from the stored one on an
//     update (sending the stored value back is allowed, as for read_only); for a Table field, rows
//     on an insert and rows that differ from the stored ones on an update.
//
// A field at level 0 is never refused here (the document rights decide), and nothing is refused for
// a caller without restrictions (restricted).
func checkInput(doctype *core.DocType, access *core.FieldAccess, values map[string]core.Value, tables map[string][]core.Row, stored *StoredDocument) error {
	if access == nil {
		return nil
	}
	var errs []core.ValidationError
	classify := func(name string, changed func() bool) {
		f := doctype.Field(name)
		switch {
		case f == nil, !access.Shows(f.PermLevel):
			errs = append(errs, core.ValidationError{Kind: core.ErrUnknownField, Field: name})
		case f.PermLevel > 0 && !access.CanWrite(f.PermLevel) && changed():
			errs = append(errs, core.ValidationError{Kind: core.ErrNotPermitted, Field: name})
		}
	}
	for _, name := range sortedKeys(values) {
		value := values[name]
		classify(name, func() bool {
			if stored != nil {
				return !valueOrNull(stored.Values, name).Equal(value)
			}
			return value.Kind != core.KindNull
		})
	}
	for _, name := range sortedKeys(tables) {
		rows := tables[name]
		classify(name, func() bool {
			if stored != nil {
				if r, ok := stored.Tables[name]; ok {
					return !rowsEqual(r, rows)
				}
			}
			return len(rows) != 0
		})
	}
	if len(errs) == 0 {
		return nil
	}
	return &Error{Kind: ErrValidation, Validation: errs}
}

// redact is the document as the caller may see it: the values and tables of the fields the caller
// cannot read are removed. The standard columns are level 0 and stay.
func redact(stored *StoredDocument, doctype *core.DocType, access *core.FieldAccess) *StoredDocument {
	if access == nil {
		return stored
	}
	for i := range doctype.Fields {
		f := &doctype.Fields[i]
		if !access.Shows(f.PermLevel) {
			delete(stored.Values, f.Name)
			delete(stored.Tables, f.Name)
		}
	}
	return stored
}

// scrub is the error with every mention of a field the caller cannot read taken out (the leak
// review of ADR 0024): a framework validation problem about a hidden field (a stored value that is
// not valid any more, a value a hook set, a Link a hook changed) becomes core.ErrHidden, which
// names nothing, and so does a locked-field error about one (a hook changed it). Other errors do
// not name fields, or name only what the caller sent.
func scrub(err error, doctype *core.DocType, access *core.FieldAccess) error {
	if access == nil || err == nil {
		return err
	}
	e, ok := err.(*Error)
	if !ok {
		return err
	}
	hidden := func(name string) bool {
		f := doctype.Field(name)
		return f != nil && !access.Shows(f.PermLevel)
	}
	switch e.Kind {
	case ErrValidation:
		scrubbed := make([]core.ValidationError, 0, len(e.Validation))
		hiddenSeen := false
		for _, v := range e.Validation {
			// What the caller sent (an unknown name, a field they may not write) is theirs to read
			// back; everything else about a hidden field is not.
			aboutInput := v.Kind == core.ErrUnknownField || v.Kind == core.ErrNotPermitted
			if !aboutInput && hidden(v.FieldName()) {
				v = core.ValidationError{Kind: core.ErrHidden}
			}
			// One generic entry is enough.
			if v.Kind == core.ErrHidden {
				if hiddenSeen {
					continue
				}
				hiddenSeen = true
			}
			scrubbed = append(scrubbed, v)
		}
		return &Error{Kind: ErrValidation, Validation: scrubbed}
	case ErrFieldLocked:
		if hidden(e.Field) {
			return &Error{Kind: ErrValidation, Validation: []core.ValidationError{{Kind: core.ErrHidden}}}
		}
	}
	return err
}

func valueOrNull(m map[string]core.Value, name string) core.Value {
	return m[name] // the zero Value is Null
}

// rowsEqual is Rust's equality of two lists of rows: the same number of rows, each with the same
// fields and equal values. A missing field is not the same as a null one here (both are maps of
// the fields they hold), so a row that omits a column differs from the stored row that has it.
func rowsEqual(a, b []core.Row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for k, v := range a[i] {
			w, ok := b[i][k]
			if !ok || !v.Equal(w) {
				return false
			}
		}
	}
	return true
}
