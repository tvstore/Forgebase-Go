package core

import (
	"fmt"
	"strings"
)

// Metadata overlays (M10.1, ADR 0023): fields that an app adds to a DocType of a dependency (app
// overlays) and fields that a site adds to a DocType of its own database (site custom fields). Both
// are parsed by the code that parses the [[fields]] of a DocType file, so a field means the same
// thing everywhere; this file has the error type, the reserved prefix and the public parsing
// functions. The rules that need the registry are Registry.AddOverlayFields and
// Registry.AddCustomField.

// CustomPrefix starts every field name of a site custom field, and no app field may (DocType field
// and overlay field alike): the prefix keeps the two name spaces apart, so a site can never collide
// with an app, now or after an app update.
const CustomPrefix = "custom_"

// OverlayErrorKind is the variant of an OverlayError.
type OverlayErrorKind int

const (
	// OverlayParse: the text is not a valid field definition (not valid TOML or JSON, an unknown
	// key, a wrong value type, a missing key). Message comes from the parser.
	OverlayParse OverlayErrorKind = iota + 1
	// OverlayNoFields: an overlay file without any [[fields]].
	OverlayNoFields
	// OverlayInvalid: the definition parsed but breaks the rules of a field or of the DocType it
	// joins; Problems lists them all.
	OverlayInvalid
	// OverlayUnknownDocType: the target DocType does not exist (Name).
	OverlayUnknownDocType
	// OverlayChildDocType: the target is a child DocType (Name); M10.1 does not extend child
	// DocTypes.
	OverlayChildDocType
	// OverlayTableField: a Table field (Name): the rows of a child DocType cannot be added
	// afterwards.
	OverlayTableField
	// OverlayCollision: the DocType (DocType) has a field of this name (Name) already.
	OverlayCollision
	// OverlayReservedPrefix: an app field (Name) named like a site custom field.
	OverlayReservedPrefix
	// OverlayMissingPrefix: a site custom field (Name) without the prefix.
	OverlayMissingPrefix
	// OverlayEmptyCustomName: a site custom field named just custom_: at least one character must
	// follow the prefix.
	OverlayEmptyCustomName
	// OverlayRequired: a site custom field (Name) with required.
	OverlayRequired
	// OverlayReadOnly: a site custom field (Name) with read_only.
	OverlayReadOnly
)

// OverlayError is why a field could not be added to a DocType by an overlay or as a custom field.
// Kind says which, and the members named at its constant are set. Error is the Rust Display text.
type OverlayError struct {
	Kind     OverlayErrorKind
	Message  string
	Problems []DefinitionError
	Name     string
	DocType  string
}

func (e OverlayError) Error() string {
	switch e.Kind {
	case OverlayParse:
		return "cannot parse the definition: " + e.Message
	case OverlayNoFields:
		return "the overlay has no [[fields]]"
	case OverlayInvalid:
		return "invalid definition: " + joinDefinitionErrors(e.Problems)
	case OverlayUnknownDocType:
		return fmt.Sprintf("unknown doctype `%s`", e.Name)
	case OverlayChildDocType:
		return fmt.Sprintf("doctype `%s` is a child DocType; fields can only be added to a DocType with documents of its own", e.Name)
	case OverlayTableField:
		return fmt.Sprintf("field `%s` has type table; overlays and custom fields cannot add Table fields", e.Name)
	case OverlayCollision:
		return fmt.Sprintf("field `%s` already exists on doctype `%s`", e.Name, e.DocType)
	case OverlayReservedPrefix:
		return fmt.Sprintf("field `%s` starts with `custom_`, a prefix reserved for site custom fields", e.Name)
	case OverlayMissingPrefix:
		return fmt.Sprintf("the name of a site custom field must start with `custom_` (got `%s`)", e.Name)
	case OverlayEmptyCustomName:
		return "the name of a site custom field needs at least one character after `custom_`"
	case OverlayRequired:
		return fmt.Sprintf("field `%s`: a site custom field cannot be required (existing documents have no value for it)", e.Name)
	case OverlayReadOnly:
		return fmt.Sprintf("field `%s`: a site custom field cannot be read_only (no hook can write it)", e.Name)
	}
	return "invalid overlay"
}

// ParseOverlay parses the text of an overlay file: [[fields]] entries written like those of a
// DocType file, and no other key. The fields are checked on their own here; the rules that involve
// the target DocType apply when they are added to the registry. The error, if any, is an
// OverlayError.
func ParseOverlay(text string) ([]DocField, error) { return overlayFromTOMLString(text) }

// ParseFieldTOML parses one field written as the body of a TOML [[fields]] table.
func ParseFieldTOML(text string) (DocField, error) { return fieldFromTOMLString(text) }

// ParseFieldJSON parses one field written as a JSON object with the keys of a TOML [[fields]]
// table. value is what encoding/json decodes into an any (a map[string]any); integers may be
// json.Number (decode with UseNumber), int, int64 or a float64 with an integral value.
func ParseFieldJSON(value any) (DocField, error) { return fieldFromJSON(value) }

// FieldToJSON is the field as the JSON object that ParseFieldJSON reads back (what a site stores):
// a map[string]any that encoding/json marshals; the numbers are ints.
func FieldToJSON(field DocField) map[string]any {
	m := map[string]any{
		"name":  field.Name,
		"label": field.Label,
		"type":  field.Type.Name(),
	}
	switch field.Type.Kind {
	case FieldData:
		m["max_length"] = int(field.Type.MaxLength)
	case FieldDecimal:
		m["precision"] = int(field.Type.Precision)
		m["scale"] = int(field.Type.Scale)
	case FieldSelect:
		options := make([]any, len(field.Type.Options))
		for i, o := range field.Type.Options {
			options[i] = o
		}
		m["options"] = options
	case FieldLink, FieldTable:
		m["target"] = field.Type.Target
	}
	for _, kv := range []struct {
		key string
		on  bool
	}{
		{"required", field.Required},
		{"unique", field.Unique},
		{"allow_on_submit", field.AllowOnSubmit},
		{"read_only", field.ReadOnly},
	} {
		if kv.on {
			m[kv.key] = true
		}
	}
	if field.PermLevel > 0 {
		m["perm_level"] = int(field.PermLevel)
	}
	return m
}

// AddOverlayFields adds the fields of an app overlay to DocType target, after its own fields and in
// the given order, and records owner as the owner of each (M10.1, ADR 0023). All or nothing: on an
// error the registry is unchanged.
//
// Refused: an unknown target, a child DocType, a Table field, a name the DocType has already
// (OverlayCollision, also between two fields of fields), a name that starts with CustomPrefix, and
// whatever the DocType's own validation refuses with the fields added. Whether the owner may extend
// this DocType (it must depend on the app that owns it) is the caller's rule, not this package's.
// Call CheckReferences after the last overlay: a Link field may name a DocType that is added later.
// The error, if any, is an OverlayError.
func (r *Registry) AddOverlayFields(owner, target string, fields []DocField) error {
	for _, field := range fields {
		if strings.HasPrefix(field.Name, CustomPrefix) {
			return OverlayError{Kind: OverlayReservedPrefix, Name: field.Name}
		}
	}
	return r.appendFields(FieldSource{Kind: SourceApp, App: owner}, target, fields)
}

// AddCustomField adds a site custom field to DocType target (M10.1, ADR 0023), after the fields it
// has.
//
// The rules of a site field on top of those of AddOverlayFields: the name starts with CustomPrefix,
// and the field is neither required nor read_only. Unlike an overlay, a Link target is checked here
// (it must exist and not be a child DocType), because a site field is added to a registry that is
// complete. The error, if any, is an OverlayError.
func (r *Registry) AddCustomField(target string, field DocField) error {
	if !strings.HasPrefix(field.Name, CustomPrefix) {
		return OverlayError{Kind: OverlayMissingPrefix, Name: field.Name}
	}
	if field.Name == CustomPrefix {
		return OverlayError{Kind: OverlayEmptyCustomName}
	}
	if field.Required {
		return OverlayError{Kind: OverlayRequired, Name: field.Name}
	}
	if field.ReadOnly {
		return OverlayError{Kind: OverlayReadOnly, Name: field.Name}
	}
	if field.Type.Kind == FieldLink {
		link := field.Type.Target
		var problem *DefinitionError
		switch t := r.doctypes[link]; {
		case t == nil:
			problem = &DefinitionError{Kind: ErrUnknownLinkTarget, DocType: target, Field: field.Name, Target: link}
		case t.IsChild:
			problem = &DefinitionError{Kind: ErrLinkToChild, DocType: target, Field: field.Name, Target: link}
		}
		if problem != nil {
			return OverlayError{Kind: OverlayInvalid, Problems: []DefinitionError{*problem}}
		}
	}
	return r.appendFields(FieldSource{Kind: SourceSite}, target, []DocField{field})
}

func (r *Registry) appendFields(source FieldSource, target string, fields []DocField) error {
	found := r.doctypes[target]
	if found == nil {
		return OverlayError{Kind: OverlayUnknownDocType, Name: target}
	}
	if found.IsChild {
		return OverlayError{Kind: OverlayChildDocType, Name: target}
	}
	extended := found.Clone()
	for _, field := range fields {
		if field.Type.Kind == FieldTable {
			return OverlayError{Kind: OverlayTableField, Name: field.Name}
		}
		if extended.Field(field.Name) != nil {
			return OverlayError{Kind: OverlayCollision, DocType: target, Name: field.Name}
		}
		extended.Fields = append(extended.Fields, field.clone())
	}
	if errs := extended.Validate(); errs != nil {
		return OverlayError{Kind: OverlayInvalid, Problems: errs}
	}
	for _, field := range fields {
		r.addedFields[[2]string{target, field.Name}] = source
	}
	r.doctypes[target] = extended
	return nil
}
