package core

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Documents and their validation against a DocType.

// Row is one row of a child table: field values keyed by field name, like Document.Values.
type Row map[string]Value

// DocStatus is the state of a document of a submittable DocType (submittable = true), stored as
// the docstatus column: 0, 1 or 2.
//
// A draft is editable and deletable; a submitted document is locked (only fields marked
// allow_on_submit change) and cannot be deleted; a cancelled document can be deleted or amended
// into a new draft. Documents of other DocTypes have no docstatus.
type DocStatus int16

const (
	// Draft (0): being edited.
	Draft DocStatus = 0
	// Submitted (1): submitted, locked.
	Submitted DocStatus = 1
	// Cancelled (2): cancelled after submission.
	Cancelled DocStatus = 2
)

// Int16 is the value of the docstatus column.
func (s DocStatus) Int16() int16 { return int16(s) }

// DocStatusFromInt16 is the status for a docstatus column value; false for anything but 0, 1 and 2.
func DocStatusFromInt16(v int16) (DocStatus, bool) {
	switch DocStatus(v) {
	case Draft, Submitted, Cancelled:
		return DocStatus(v), true
	}
	return Draft, false
}

// String is draft, submitted or cancelled.
func (s DocStatus) String() string {
	switch s {
	case Draft:
		return "draft"
	case Submitted:
		return "submitted"
	case Cancelled:
		return "cancelled"
	}
	return fmt.Sprintf("docstatus(%d)", int16(s))
}

// Document is a document: an optional name plus field values keyed by field name.
//
// A missing key and Null both mean "no value". Plain data with exported fields, on purpose (ADR
// 0012). Build one with a composite literal that names its fields.
type Document struct {
	// Name is the document name, once assigned; "" before that (Rust: Option<String>).
	Name string
	// Values are the field values. A Table field has no value here; its rows are in Tables.
	Values map[string]Value
	// Tables are the rows of each Table field, keyed by field name. A row's position in the list
	// is its order (idx, counting from 1); an absent key and an empty list both mean "no rows".
	Tables map[string][]Row
}

// Changes is a partial update: the fields to change.
//
// A field in Values is set (a Null clears it). A Table field in Tables replaces its rows with the
// given ones (an empty list removes every row; internal/db's update leaves a table alone when the
// rows equal the stored ones); a Table field that is not mentioned keeps its rows.
type Changes struct {
	// Values are the new field values.
	Values map[string]Value
	// Tables are the new rows for Table fields, replacing the existing ones.
	Tables map[string][]Row
}

// IsEmpty is true when nothing is to be changed.
func (c Changes) IsEmpty() bool { return len(c.Values) == 0 && len(c.Tables) == 0 }

// ValidationErrorKind is the variant of a ValidationError.
type ValidationErrorKind int

const (
	// ErrUnknownField: the document has a value for a field the DocType does not define.
	ErrUnknownField ValidationErrorKind = iota + 1
	// ErrRequired: a required field is missing, null, or (Text, Data, Select) an empty or
	// whitespace-only string.
	ErrRequired
	// ErrTypeMismatch: the value has the wrong kind for the field type (Expected, Found).
	ErrTypeMismatch
	// ErrTooLong: a Data value is longer than max_length characters (Len, Max).
	ErrTooLong
	// ErrInvalidOption: a Select value is not one of the options (Value).
	ErrInvalidOption
	// ErrContainsNul: a text value contains a NUL character, which PostgreSQL text cannot store.
	ErrContainsNul
	// ErrDecimalScale: a Decimal value has more fractional digits than the field's scale (Scale,
	// Max).
	ErrDecimalScale
	// ErrDecimalPrecision: a Decimal value has more integer digits than precision - scale allows
	// (Digits, Max).
	ErrDecimalPrecision
	// ErrReadOnly: a caller supplied a value for a read_only field. Not produced by Validate (a
	// hook may set such a field): internal/db reports it for the caller's input, before the hooks.
	ErrReadOnly
	// ErrNotPermitted: a caller supplied a value for a field they can read but not write, because
	// the field has a perm_level above 0 and none of their roles has a write rule at that level
	// (M10.2, ADR 0024). Not produced by Validate. Sending the stored value back is allowed. A
	// field the caller cannot even read is not reported like this but as ErrUnknownField.
	ErrNotPermitted
	// ErrHidden: a problem with a field that the caller cannot read. The field is deliberately not
	// named: its name and value must not reach a caller who is not allowed to know the field (ADR
	// 0024). Not produced by Validate; internal/db puts it in place of such a problem.
	ErrHidden
	// ErrLinkNotFound: a Link value names a document that does not exist (Target, Name). Not
	// produced by Validate (which does no I/O): internal/db reports it when it resolves the link.
	ErrLinkNotFound
	// ErrInRow: a row of a Table field has a problem. Row counts from 1, like the row's position;
	// Inner is the problem, about a field of the child DocType.
	ErrInRow
)

// ValidationError is one problem found while validating a document. Which members are set depends
// on Kind (see the constants); Error is the Rust Display text.
type ValidationError struct {
	Kind     ValidationErrorKind
	Field    string
	Expected string
	Found    string
	Len      int
	Max      uint32
	Value    string
	Scale    uint32
	Digits   uint32
	Target   string
	Name     string
	Row      int
	Inner    *ValidationError
}

func (e ValidationError) Error() string {
	switch e.Kind {
	case ErrUnknownField:
		return fmt.Sprintf("unknown field `%s`", Shorten(e.Field))
	case ErrRequired:
		return fmt.Sprintf("field `%s` is required", e.Field)
	case ErrTypeMismatch:
		return fmt.Sprintf("field `%s`: expected %s, found %s", e.Field, e.Expected, e.Found)
	case ErrTooLong:
		return fmt.Sprintf("field `%s`: %d characters exceeds max_length %d", e.Field, e.Len, e.Max)
	case ErrInvalidOption:
		return fmt.Sprintf("field `%s`: `%s` is not one of the allowed options", e.Field, Shorten(e.Value))
	case ErrContainsNul:
		return fmt.Sprintf("field `%s`: value must not contain a NUL character", e.Field)
	case ErrDecimalScale:
		return fmt.Sprintf("field `%s`: %d decimal places exceeds scale %d", e.Field, e.Scale, e.Max)
	case ErrDecimalPrecision:
		return fmt.Sprintf("field `%s`: %d integer digits exceeds the maximum of %d", e.Field, e.Digits, e.Max)
	case ErrReadOnly:
		return fmt.Sprintf("field `%s` is read-only", e.Field)
	case ErrNotPermitted:
		return fmt.Sprintf("field `%s` cannot be changed with your permissions", e.Field)
	case ErrHidden:
		return "a field that you cannot see has a value that is not valid"
	case ErrLinkNotFound:
		return fmt.Sprintf("field `%s`: %s `%s` does not exist", e.Field, e.Target, Shorten(e.Name))
	case ErrInRow:
		return fmt.Sprintf("field `%s` row %d: %s", e.Field, e.Row, e.Inner)
	}
	return "invalid document"
}

// FieldName is the field this problem is about. For a problem in a row, the Table field. Empty for
// ErrHidden, which names none. (Rust: ValidationError::field.)
func (e ValidationError) FieldName() string {
	if e.Kind == ErrHidden {
		return ""
	}
	return e.Field
}

// Validate validates a complete document for insert. It returns nil when the document is valid,
// else every problem found, in a stable order.
//
// It reports every problem: unknown fields, missing or null required fields, wrong value kinds and
// constraint violations. doc.Name is not checked.
//
// A required Text, Data, Select or Link field counts as missing when it is absent, null, or an
// empty or whitespace-only string. Text, Data, Select and Link values must not contain NUL.
// Whether a Link target exists is not checked here (no I/O); internal/db does that.
//
// Tables. A Table field takes its rows from Document.Tables, never from Document.Values (a value
// there is an ErrTypeMismatch, and so is a row list for a field that is not a Table field). A
// required Table field needs at least one row. Each row is validated against the child DocType
// (found in registry) like a document: unknown fields, missing required fields, value kinds and
// constraints; its problems are wrapped in ErrInRow with the row's position. A table whose child
// DocType is not in registry is not checked (a registry that passed CheckReferences has it).
//
// doctype must be valid (DocType.Validate); a Registry only holds valid DocTypes. The result for an
// invalid DocType is unspecified (but never a panic).
func Validate(registry *Registry, doctype *DocType, doc *Document) []ValidationError {
	var errs []ValidationError
	for i := range doctype.Fields {
		field := &doctype.Fields[i]
		var present bool
		if field.Type.Kind == FieldTable {
			_, present = doc.Tables[field.Name]
		} else {
			// An explicit null is reported by checkValues.
			_, present = doc.Values[field.Name]
		}
		if field.Required && !present {
			errs = append(errs, ValidationError{Kind: ErrRequired, Field: field.Name})
		}
	}
	errs = checkValues(doctype, doc.Values, errs)
	errs = checkTables(registry, doctype, doc.Tables, errs)
	return errs
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func checkValues(doctype *DocType, values map[string]Value, errs []ValidationError) []ValidationError {
	for _, name := range sortedKeys(values) {
		value := values[name]
		field := doctype.Field(name)
		switch {
		case field == nil:
			errs = append(errs, ValidationError{Kind: ErrUnknownField, Field: name})
		case field.Type.Kind == FieldTable:
			// A Table field has no value; its rows go in Tables.
			errs = append(errs, ValidationError{Kind: ErrTypeMismatch, Field: name, Expected: "rows", Found: value.KindName()})
		default:
			errs = checkValue(field, value, errs)
		}
	}
	return errs
}

func checkTables(registry *Registry, doctype *DocType, tables map[string][]Row, errs []ValidationError) []ValidationError {
	for _, name := range sortedKeys(tables) {
		rows := tables[name]
		field := doctype.Field(name)
		if field == nil {
			errs = append(errs, ValidationError{Kind: ErrUnknownField, Field: name})
			continue
		}
		if field.Type.Kind != FieldTable {
			errs = append(errs, ValidationError{Kind: ErrTypeMismatch, Field: name, Expected: expectedKind(field.Type), Found: "rows"})
			continue
		}
		if field.Required && len(rows) == 0 {
			errs = append(errs, ValidationError{Kind: ErrRequired, Field: name})
		}
		child := registry.Get(field.Type.Target)
		if child == nil {
			continue
		}
		for i, row := range rows {
			var rowErrs []ValidationError
			for j := range child.Fields {
				cf := &child.Fields[j]
				if _, ok := row[cf.Name]; cf.Required && !ok {
					rowErrs = append(rowErrs, ValidationError{Kind: ErrRequired, Field: cf.Name})
				}
			}
			rowErrs = checkValues(child, row, rowErrs)
			for _, e := range rowErrs {
				inner := e
				errs = append(errs, ValidationError{Kind: ErrInRow, Field: name, Row: i + 1, Inner: &inner})
			}
		}
	}
	return errs
}

func checkValue(field *DocField, value Value, errs []ValidationError) []ValidationError {
	name := field.Name
	if value.Kind == KindNull {
		if field.Required {
			errs = append(errs, ValidationError{Kind: ErrRequired, Field: name})
		}
		return errs
	}
	ft := field.Type
	textual := ft.Kind == FieldData || ft.Kind == FieldText || ft.Kind == FieldSelect || ft.Kind == FieldLink
	if textual && value.Kind == KindText {
		if strings.ContainsRune(value.Text, 0) {
			return append(errs, ValidationError{Kind: ErrContainsNul, Field: name})
		}
		if field.Required && strings.TrimSpace(value.Text) == "" {
			return append(errs, ValidationError{Kind: ErrRequired, Field: name})
		}
	}
	switch {
	case ft.Kind == FieldData && value.Kind == KindText:
		if n := utf8.RuneCountInString(value.Text); n > int(ft.MaxLength) {
			errs = append(errs, ValidationError{Kind: ErrTooLong, Field: name, Len: n, Max: ft.MaxLength})
		}
	case ft.Kind == FieldSelect && value.Kind == KindText:
		found := false
		for _, o := range ft.Options {
			if o == value.Text {
				found = true
				break
			}
		}
		if !found {
			errs = append(errs, ValidationError{Kind: ErrInvalidOption, Field: name, Value: value.Text})
		}
	case ft.Kind == FieldDecimal && value.Kind == KindDecimal:
		// Trailing zeros do not count: 1.500 fits scale 1.
		d := value.Dec.Normalize()
		if d.Scale() > uint32(ft.Scale) {
			errs = append(errs, ValidationError{Kind: ErrDecimalScale, Field: name, Scale: d.Scale(), Max: uint32(ft.Scale)})
		} else {
			total := d.MantissaDigits()
			var digits uint32
			if total > d.Scale() {
				digits = total - d.Scale()
			}
			var max uint32
			if ft.Precision > ft.Scale {
				max = uint32(ft.Precision - ft.Scale)
			}
			if digits > max {
				errs = append(errs, ValidationError{Kind: ErrDecimalPrecision, Field: name, Digits: digits, Max: max})
			}
		}
	case (ft.Kind == FieldText || ft.Kind == FieldLink) && value.Kind == KindText,
		ft.Kind == FieldInt && value.Kind == KindInt,
		ft.Kind == FieldCheck && value.Kind == KindBool,
		ft.Kind == FieldDate && value.Kind == KindDate,
		ft.Kind == FieldDatetime && value.Kind == KindDatetime:
	default:
		errs = append(errs, ValidationError{Kind: ErrTypeMismatch, Field: name, Expected: expectedKind(ft), Found: value.KindName()})
	}
	return errs
}

func expectedKind(ft FieldType) string {
	switch ft.Kind {
	case FieldData, FieldText, FieldSelect, FieldLink:
		return "text"
	case FieldInt:
		return "int"
	case FieldDecimal:
		return "decimal"
	case FieldCheck:
		return "bool"
	case FieldDate:
		return "date"
	case FieldDatetime:
		return "datetime"
	}
	return "rows"
}
