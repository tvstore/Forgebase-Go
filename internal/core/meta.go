package core

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// The DocType metadata model, its validation and the Registry.

// DefaultMaxLength is the default max_length of a Data field.
const DefaultMaxLength uint32 = 140

// MaxDataLength is the largest allowed max_length of a Data field.
const MaxDataLength uint32 = 65_535

// MaxDecimalPrecision is the largest allowed Decimal precision (the range of the decimal type,
// internal/decimal).
const MaxDecimalPrecision uint8 = 28

// MaxListFields is the largest number of fields in a DocType's list_fields.
const MaxListFields = 8

// TitleFieldKinds are the field kinds a title_field may have: short values that read as a title.
// Not Text (long), Check (a flag, no name) or Table (no value).
var TitleFieldKinds = [7]string{"data", "int", "decimal", "date", "datetime", "select", "link"}

// WorkflowColumn is the name of the framework column that holds the state of a document of a
// DocType with a workflow.
const WorkflowColumn = "workflow_state"

// docTypeIconList is the icon names a DocType's icon may have, one per line in doctype_icons.txt.
// The desk has a test that checks the same file against its icon set, so the two cannot drift
// apart.
//
//go:embed doctype_icons.txt
var docTypeIconList string

// DocTypeIcons are the icon names a DocType's icon may have (UX.4), in file order.
func DocTypeIcons() []string {
	var out []string
	for _, line := range strings.Split(docTypeIconList, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Humanize is the default label of a name: split on _, capitalise the first letter of each part,
// join with a space. order_date gives Order Date.
func Humanize(name string) string {
	var parts []string
	for _, part := range strings.Split(name, "_") {
		if part == "" {
			continue
		}
		r, size := utf8.DecodeRuneInString(part)
		parts = append(parts, strings.ToUpper(string(r))+part[size:])
	}
	return strings.Join(parts, " ")
}

// DocType is a document type: a named list of fields stored in one table.
//
// A child DocType (IsChild) has no documents of its own: its rows are stored in a table of their
// own but always belong to a parent document, through a Table field of exactly one other DocType.
//
// A DocType held by a Registry is immutable: never change one that was added; use Clone and add
// the copy where needed.
type DocType struct {
	// Name is the identifier, see ValidateDocTypeName.
	Name string
	// Label is the human-readable name. Required in the TOML file, never derived from Name.
	Label string
	// Naming says how document names are generated.
	Naming Naming
	// Fields are in declaration order.
	Fields []DocField
	// IsChild: rows of a parent's table field, no documents or names of its own. A child has
	// random naming, no Table fields (no nesting) and no unique fields.
	IsChild bool
	// Submittable: the DocType has a docstatus (draft, submitted, cancelled) and can be amended
	// (M2.4): its table gets the docstatus and amended_from columns. It cannot be a child DocType
	// and cannot use field naming.
	Submittable bool
	// Permissions say who may do what with the DocType's documents ([[permissions]], M3.2, ADR
	// 0011). Empty means only the Administrator role and the trusted system caller have access
	// (deny by default). A child DocType has none: its rows follow the parent's rules. Decisions
	// are made by Evaluate, nowhere else.
	Permissions []PermissionRule
	// TitleField is the field that best names a document for a person (title_field, UX.1), or nil.
	// A non-Table field of a kind in TitleFieldKinds; not allowed on a child DocType. Metadata for
	// the desk only: the framework attaches no behaviour to it.
	TitleField *string
	// ListFields are the columns the desk list shows after name (list_fields, UX.1), or nil for the
	// desk's own default (an empty non-nil slice is "an empty list", which is a definition error).
	// Existing fields, no Table or Text field, no duplicates, at most MaxListFields; not allowed on
	// a child DocType. Metadata for the desk only.
	ListFields []string
	// Icon is the icon the desk shows for the DocType (icon, UX.4): a name from the list in
	// doctype_icons.txt (DocTypeIcons), or nil for the desk's default. Not allowed on a child
	// DocType. Metadata for the desk only.
	Icon *string
	// Workflow holds the states a document moves through and the transitions between them
	// ([workflow], M8.1, ADR 0015), or nil. Defined by the app in the DocType file. On a DocType
	// with a workflow, plain submit and cancel are refused and the state changes only through
	// transitions. Not allowed on a child DocType, and a field of the DocType may not be named
	// workflow_state.
	Workflow *Workflow
}

// Clone is a deep copy of the DocType.
func (d *DocType) Clone() *DocType {
	c := *d
	c.Fields = make([]DocField, len(d.Fields))
	for i, f := range d.Fields {
		c.Fields[i] = f.clone()
	}
	if d.Permissions != nil {
		c.Permissions = slices.Clone(d.Permissions)
	}
	if d.TitleField != nil {
		s := *d.TitleField
		c.TitleField = &s
	}
	if d.ListFields != nil {
		c.ListFields = slices.Clone(d.ListFields)
		if c.ListFields == nil {
			c.ListFields = []string{}
		}
	}
	if d.Icon != nil {
		s := *d.Icon
		c.Icon = &s
	}
	if d.Workflow != nil {
		w := &Workflow{}
		if d.Workflow.States != nil {
			w.States = make([]WorkflowState, len(d.Workflow.States))
			for i, s := range d.Workflow.States {
				s.EditRoles = slices.Clone(s.EditRoles)
				w.States[i] = s
			}
		}
		if d.Workflow.Transitions != nil {
			w.Transitions = make([]Transition, len(d.Workflow.Transitions))
			for i, t := range d.Workflow.Transitions {
				t.Roles = slices.Clone(t.Roles)
				w.Transitions[i] = t
			}
		}
		c.Workflow = w
	}
	return &c
}

// DocField is one field of a DocType.
type DocField struct {
	// Name is the identifier, see ValidateFieldName.
	Name string
	// Label is the human-readable name; when loaded from TOML without a label, Name humanized.
	Label string
	// Type is the type and its parameters.
	Type FieldType
	// Required: a value must be present: not null and, for Data, Text and Select, not an empty or
	// whitespace-only string. Enforced by application validation, not by the database.
	Required bool
	// Unique: values must be unique across documents.
	Unique bool
	// AllowOnSubmit: the field may still be changed on a submitted document. Only allowed on a
	// column field (not a Table field) of a submittable DocType; the rows of Table fields are
	// always locked once the document is submitted.
	AllowOnSubmit bool
	// ReadOnly: the field is written only by hooks (and by migrations), never by a caller:
	// internal/db refuses caller input for it (UX.1). Not allowed on a Table field, and not
	// together with Required (a caller could never provide the value).
	ReadOnly bool
	// PermLevel is the permission level of the field (perm_level, M10.2, ADR 0024): 0 to
	// MaxPermLevel, 0 by default. A caller reads or writes a field at level n > 0 only with a
	// [[permissions]] rule of level = n (FieldAccessOf); a field they cannot read is unknown to
	// them. Not allowed on a field of a child DocType (protect the whole table with the Table
	// field's own level), on the naming field, the title_field or a list_fields column, nor
	// together with Required.
	PermLevel uint8
}

func (f DocField) clone() DocField {
	f.Type.Options = cloneKeepNil(f.Type.Options)
	return f
}

func cloneKeepNil(s []string) []string {
	if s == nil {
		return nil
	}
	return append([]string{}, s...)
}

// FieldKind is the kind of a FieldType.
type FieldKind int

const (
	// FieldData is short text with a maximum length in characters (1..=65535).
	FieldData FieldKind = iota + 1
	// FieldText is text without a length limit.
	FieldText
	// FieldInt is a 64-bit signed integer.
	FieldInt
	// FieldDecimal is a fixed-point number with Precision total digits, Scale of them after the
	// point.
	FieldDecimal
	// FieldCheck is a boolean.
	FieldCheck
	// FieldDate is a calendar date.
	FieldDate
	// FieldDatetime is a point in time with an offset.
	FieldDatetime
	// FieldSelect is one of a fixed list of strings (non-empty, unique, non-empty entries).
	FieldSelect
	// FieldLink is a reference to a document of another DocType (or of the same one), by document
	// name. The value is a text Value holding the target document's name. The database stores the
	// target's id as a foreign key. Target must exist in the Registry that holds this DocType, see
	// Registry.CheckReferences.
	FieldLink
	// FieldTable is an ordered list of rows of a child DocType. There is no Value for it and no
	// column in the parent's table: the rows live in Document.Tables. Required means at least one
	// row; Unique is not allowed. Target is the name of the child DocType. It must exist in the
	// Registry that holds this DocType and be a child DocType used by no other Table field, see
	// Registry.CheckReferences.
	FieldTable
)

// FieldType is the closed set of field types and their parameters. Only the members that belong to
// Kind are meaningful. Build one with a composite literal, e.g. FieldType{Kind: FieldInt}.
type FieldType struct {
	Kind FieldKind
	// MaxLength is the maximum number of characters of a Data field (1..=65535).
	MaxLength uint32
	// Precision is the total number of digits of a Decimal field (1..=28).
	Precision uint8
	// Scale is the number of digits after the decimal point (<= Precision).
	Scale uint8
	// Options are the allowed values of a Select field.
	Options []string
	// Target is the target DocType of a Link or Table field.
	Target string
}

// Name is the name of the type as written in metadata files.
func (t FieldType) Name() string {
	switch t.Kind {
	case FieldData:
		return "data"
	case FieldText:
		return "text"
	case FieldInt:
		return "int"
	case FieldDecimal:
		return "decimal"
	case FieldCheck:
		return "check"
	case FieldDate:
		return "date"
	case FieldDatetime:
		return "datetime"
	case FieldSelect:
		return "select"
	case FieldLink:
		return "link"
	case FieldTable:
		return "table"
	}
	return "unknown"
}

func (t FieldType) String() string { return t.Name() }

// NamingKind says how the name of a new document is produced.
type NamingKind int

const (
	// NamingRandom is a generated random name (the default).
	NamingRandom NamingKind = iota
	// NamingField is the value of the named field (Naming.Field). That field must be a required,
	// unique Data field.
	NamingField
	// NamingSeries is a generated name from a counter, such as INV-2026-00007, see SeriesPattern
	// (naming = { series = "INV-.YYYY.-.#####" }). Not allowed on a child DocType.
	NamingSeries
)

// Naming is how the name of a new document is produced. The zero value is random naming.
type Naming struct {
	Kind NamingKind
	// Field is the naming field, for NamingField.
	Field string
	// Series is the pattern, for NamingSeries.
	Series *SeriesPattern
}

// DefinitionErrorKind is the variant of a DefinitionError.
type DefinitionErrorKind int

// The kinds of DefinitionError. Each names the members of DefinitionError that it sets.
const (
	ErrInvalidDocTypeName            DefinitionErrorKind = iota + 1 // Name, Reason
	ErrEmptyLabel                                                   // -
	ErrInvalidFieldName                                             // Field, Reason
	ErrNulInLabel                                                   // -
	ErrEmptyFieldLabel                                              // Field
	ErrNulInFieldLabel                                              // Field
	ErrDuplicateField                                               // Field
	ErrUnknownFieldType                                             // Field, Found
	ErrMissingParam                                                 // Field, FieldType, Param
	ErrUnexpectedParam                                              // Field, FieldType, Param
	ErrMaxLengthOutOfRange                                          // Field, Value
	ErrPrecisionOutOfRange                                          // Field, Value
	ErrScaleExceedsPrecision                                        // Field, Scale, Precision
	ErrEmptyOptions                                                 // Field
	ErrEmptyOption                                                  // Field
	ErrNulInOption                                                  // Field
	ErrDuplicateOption                                              // Field, Option
	ErrUnknownNamingStrategy                                        // Found
	ErrMalformedNaming                                              // -
	ErrInvalidSeriesPattern                                         // Pattern, Reason
	ErrChildSubmittable                                             // -
	ErrSubmittableWithFieldNaming                                   // -
	ErrAllowOnSubmitNotSubmittable                                  // Field
	ErrAllowOnSubmitTable                                           // Field
	ErrNamingFieldMissing                                           // Field
	ErrNamingFieldNotData                                           // Field, Found
	ErrReadOnlyTable                                                // Field
	ErrReadOnlyRequired                                             // Field
	ErrChildWithDeskKey                                             // Key
	ErrUnknownIcon                                                  // Icon, Allowed
	ErrTitleFieldMissing                                            // Field
	ErrTitleFieldKind                                               // Field, Found
	ErrEmptyListFields                                              // -
	ErrTooManyListFields                                            // Count, Max
	ErrListFieldMissing                                             // Field
	ErrListFieldKind                                                // Field, Found
	ErrDuplicateListField                                           // Field
	ErrNamingFieldNotRequired                                       // Field
	ErrNamingFieldNotUnique                                         // Field
	ErrInvalidLinkTarget                                            // Field, Target, Reason
	ErrInvalidTableTarget                                           // Field, Target, Reason
	ErrUniqueTable                                                  // Field
	ErrChildWithNaming                                              // -
	ErrChildWithTable                                               // Field
	ErrChildWithUnique                                              // Field
	ErrInvalidPermissionRole                                        // Role, Why
	ErrEmptyPermissionRule                                          // Role
	ErrPermissionNeedsSubmittable                                   // Role, Right
	ErrDuplicatePermissionRule                                      // Role, IfOwner
	ErrChildWithPermissions                                         // -
	ErrInvalidPermLevel                                             // Field, Value
	ErrInvalidRuleLevel                                             // Role, Value
	ErrChildFieldPermLevel                                          // Field
	ErrLevelRuleRight                                               // Role, Level, Right
	ErrLevelRuleIfOwner                                             // Role, Level
	ErrLevelWriteWithoutRead                                        // Role, Level
	ErrDuplicateLevelRule                                           // Role, Level
	ErrKeyFieldPermLevel                                            // Field, UsedAs
	ErrRequiredPermLevel                                            // Field
	ErrDuplicateDocType                                             // Name
	ErrUnknownHooksDocType                                          // Name
	ErrHooksOnChildDocType                                          // DocType
	ErrUnknownLinkTarget                                            // DocType, Field, Target
	ErrUnknownTableTarget                                           // DocType, Field, Target
	ErrTableTargetNotChild                                          // DocType, Field, Target
	ErrLinkToChild                                                  // DocType, Field, Target
	ErrUnusedChild                                                  // DocType
	ErrChildSharedByTables                                          // Child, Uses
	ErrChildWithWorkflow                                            // -
	ErrWorkflowNoStates                                             // -
	ErrWorkflowInitialNotDraft                                      // State
	ErrWorkflowName                                                 // NameKind, Name, Why
	ErrWorkflowDuplicateState                                       // State
	ErrWorkflowInvalidDocStatus                                     // State, Value
	ErrWorkflowUnknownStyle                                         // State, Found
	ErrWorkflowStateNeedsSubmittable                                // State, DocStatus
	ErrWorkflowEditRolesNotDraft                                    // State
	ErrWorkflowInvalidRole                                          // Place, Role, Why
	ErrWorkflowUnknownState                                         // Action, From, Key, State
	ErrWorkflowDuplicateAction                                      // Action, From
	ErrWorkflowNoRoles                                              // Action, From
	ErrWorkflowDuplicateRole                                        // Action, From, Role
	ErrWorkflowSelfTransition                                       // Action, State
	ErrWorkflowBadMove                                              // Action, From, To, FromDocStatus, ToDocStatus
	ErrWorkflowUnreachableState                                     // State
	ErrWorkflowStateFieldName                                       // -
)

// DefinitionError is one problem found in a DocType definition. Kind says which, and the members
// named at its constant are set; Error is the Rust Display text, word for word. Compare errors
// with == or reflect.DeepEqual after building the expected value with the same members.
type DefinitionError struct {
	Kind DefinitionErrorKind

	Name, Field, Found, FieldType, Param, Option, Pattern, Key, Icon, Allowed string
	Target, Role, DocType, UsedAs, Child, Place, Action, From, To, State      string
	NameKind, Why                                                             string

	// Reason is why a name (an IdentError) or a series pattern (a SeriesError) was rejected.
	Reason error
	// Value is the rejected number: a max_length, a precision, a perm_level, a level or a
	// docstatus.
	Value            int64
	Scale, Precision uint8
	Level            uint8
	Count, Max       int
	IfOwner          bool
	Right            Right
	Uses             []string
	DocStatus        int16
	FromDocStatus    int16
	ToDocStatus      int16
}

// Error is the Rust Display text.
func (e DefinitionError) Error() string {
	f := fmt.Sprintf
	switch e.Kind {
	case ErrInvalidDocTypeName:
		return f("doctype name `%s` %s", e.Name, e.Reason)
	case ErrEmptyLabel:
		return "doctype label must not be empty"
	case ErrInvalidFieldName:
		return f("field name `%s` %s", e.Field, e.Reason)
	case ErrNulInLabel:
		return "doctype label must not contain a NUL character"
	case ErrEmptyFieldLabel:
		return f("field `%s`: label must not be empty", e.Field)
	case ErrNulInFieldLabel:
		return f("field `%s`: label must not contain a NUL character", e.Field)
	case ErrDuplicateField:
		return f("field `%s` is defined more than once", e.Field)
	case ErrUnknownFieldType:
		return f("field `%s`: unknown type `%s` (expected one of data, text, int, decimal, check, date, datetime, select, link, table)", e.Field, e.Found)
	case ErrMissingParam:
		return f("field `%s`: type `%s` requires `%s`", e.Field, e.FieldType, e.Param)
	case ErrUnexpectedParam:
		return f("field `%s`: `%s` is not allowed for type `%s`", e.Field, e.Param, e.FieldType)
	case ErrMaxLengthOutOfRange:
		return f("field `%s`: max_length %d is out of range (1..=%d)", e.Field, e.Value, MaxDataLength)
	case ErrPrecisionOutOfRange:
		return f("field `%s`: precision %d is out of range (1..=%d)", e.Field, e.Value, MaxDecimalPrecision)
	case ErrScaleExceedsPrecision:
		return f("field `%s`: scale %d must not exceed precision %d", e.Field, e.Scale, e.Precision)
	case ErrEmptyOptions:
		return f("field `%s`: options must not be empty", e.Field)
	case ErrEmptyOption:
		return f("field `%s`: options must not contain an empty string", e.Field)
	case ErrNulInOption:
		return f("field `%s`: options must not contain a NUL character", e.Field)
	case ErrDuplicateOption:
		return f("field `%s`: duplicate option `%s`", e.Field, e.Option)
	case ErrUnknownNamingStrategy:
		return f("unknown naming strategy `%s` (expected \"random\", { field = \"<name>\" } or { series = \"<pattern>\" })", e.Found)
	case ErrMalformedNaming:
		return "naming must be \"random\", { field = \"<fieldname>\" } or { series = \"<pattern>\" }"
	case ErrInvalidSeriesPattern:
		return f("naming series pattern `%s` %s", e.Pattern, e.Reason)
	case ErrChildSubmittable:
		return "a child doctype cannot be submittable (the document that owns its rows is)"
	case ErrSubmittableWithFieldNaming:
		return "a submittable doctype cannot use field naming (an amended document is named <original>-<n>); use random naming or a series"
	case ErrAllowOnSubmitNotSubmittable:
		return f("field `%s`: allow_on_submit needs a submittable doctype (`submittable = true`)", e.Field)
	case ErrAllowOnSubmitTable:
		return f("field `%s`: a table field cannot be allow_on_submit (its rows are locked once submitted)", e.Field)
	case ErrNamingFieldMissing:
		return f("naming refers to field `%s`, which does not exist", e.Field)
	case ErrNamingFieldNotData:
		return f("naming field `%s` must be of type data, found `%s`", e.Field, e.Found)
	case ErrReadOnlyTable:
		return f("field `%s`: a table field cannot be read_only", e.Field)
	case ErrReadOnlyRequired:
		return f("field `%s`: a read_only field cannot be required", e.Field)
	case ErrChildWithDeskKey:
		return f("a child doctype cannot have `%s`", e.Key)
	case ErrUnknownIcon:
		return f("icon `%s` is not an allowed icon (allowed: %s)", e.Icon, e.Allowed)
	case ErrTitleFieldMissing:
		return f("title_field `%s` is not a field of the doctype", e.Field)
	case ErrTitleFieldKind:
		return f("title_field `%s` is a %s field (allowed: data, int, decimal, date, datetime, select, link)", e.Field, e.Found)
	case ErrEmptyListFields:
		return "list_fields must not be empty (leave it out for the default)"
	case ErrTooManyListFields:
		return f("list_fields has %d fields, at most %d are allowed", e.Count, e.Max)
	case ErrListFieldMissing:
		return f("list_fields `%s` is not a field of the doctype", e.Field)
	case ErrListFieldKind:
		return f("list_fields `%s` is a %s field (table and text fields cannot be listed)", e.Field, e.Found)
	case ErrDuplicateListField:
		return f("list_fields names `%s` more than once", e.Field)
	case ErrNamingFieldNotRequired:
		return f("naming field `%s` must be required", e.Field)
	case ErrNamingFieldNotUnique:
		return f("naming field `%s` must be unique", e.Field)
	case ErrInvalidLinkTarget:
		return f("field `%s`: link target `%s` %s", e.Field, e.Target, e.Reason)
	case ErrInvalidTableTarget:
		return f("field `%s`: table target `%s` %s", e.Field, e.Target, e.Reason)
	case ErrUniqueTable:
		return f("field `%s`: a table field cannot be unique", e.Field)
	case ErrChildWithNaming:
		return "a child doctype has no naming rule (its rows have no names)"
	case ErrChildWithTable:
		return f("field `%s`: a child doctype cannot have table fields (no nesting)", e.Field)
	case ErrChildWithUnique:
		return f("field `%s`: a child doctype cannot have unique fields", e.Field)
	case ErrInvalidPermissionRole:
		return f("permission rule for role %s: invalid role: %s", debugQuote(e.Role), e.Why)
	case ErrEmptyPermissionRule:
		return f("permission rule for role %s grants no right", debugQuote(e.Role))
	case ErrPermissionNeedsSubmittable:
		return f("permission rule for role %s: `%s` needs a submittable doctype (`submittable = true`)", debugQuote(e.Role), e.Right)
	case ErrDuplicatePermissionRule:
		return f("more than one permission rule for role %s with if_owner = %t", debugQuote(e.Role), e.IfOwner)
	case ErrChildWithPermissions:
		return "a child doctype has no permission rules (its rows follow the parent's)"
	case ErrInvalidPermLevel:
		return f("field `%s`: perm_level must be an integer from 0 to 9, got %d", e.Field, e.Value)
	case ErrInvalidRuleLevel:
		return f("permission rule for role %s: level must be an integer from 0 to 9, got %d", debugQuote(e.Role), e.Value)
	case ErrChildFieldPermLevel:
		return f("field `%s`: a field of a child doctype stays at perm_level 0 (give the Table field of the parent a perm_level to protect the whole table)", e.Field)
	case ErrLevelRuleRight:
		return f("permission rule for role %s at level %d: `%s` is a document right decided at level 0; a level above 0 grants read and write only", debugQuote(e.Role), e.Level, e.Right)
	case ErrLevelRuleIfOwner:
		return f("permission rule for role %s at level %d: if_owner applies to level 0 rules only (the level 0 rule already limits the documents)", debugQuote(e.Role), e.Level)
	case ErrLevelWriteWithoutRead:
		return f("permission rule for role %s at level %d: `write` needs `read` (a field that cannot be seen cannot be written)", debugQuote(e.Role), e.Level)
	case ErrDuplicateLevelRule:
		return f("more than one permission rule for role %s at level %d", debugQuote(e.Role), e.Level)
	case ErrKeyFieldPermLevel:
		return f("field `%s` is %s and must stay at perm_level 0: it is shown to everybody who can read a document", e.Field, e.UsedAs)
	case ErrRequiredPermLevel:
		return f("field `%s`: a required field must stay at perm_level 0 (a caller who cannot write it could never save a document)", e.Field)
	case ErrDuplicateDocType:
		return f("doctype `%s` is already registered", e.Name)
	case ErrUnknownHooksDocType:
		return f("cannot register hooks: unknown doctype `%s`", e.Name)
	case ErrHooksOnChildDocType:
		return f("cannot register hooks for child doctype `%s`: register them on its parent, whose hooks see the rows", e.DocType)
	case ErrUnknownLinkTarget:
		return f("doctype `%s`: field `%s` links to unknown doctype `%s`", e.DocType, e.Field, e.Target)
	case ErrUnknownTableTarget:
		return f("doctype `%s`: field `%s` is a table of unknown doctype `%s`", e.DocType, e.Field, e.Target)
	case ErrTableTargetNotChild:
		return f("doctype `%s`: field `%s` is a table of `%s`, which is not a child doctype (`is_child = true`)", e.DocType, e.Field, e.Target)
	case ErrLinkToChild:
		return f("doctype `%s`: field `%s` links to child doctype `%s`, which cannot be linked to", e.DocType, e.Field, e.Target)
	case ErrUnusedChild:
		return f("child doctype `%s` is not used by any table field", e.DocType)
	case ErrChildSharedByTables:
		return f("child doctype `%s` is used by more than one table field: %s", e.Child, strings.Join(e.Uses, ", "))
	case ErrChildWithWorkflow:
		return "a child doctype cannot have a workflow"
	case ErrWorkflowNoStates:
		return "workflow: at least one state is required (the first is the initial state)"
	case ErrWorkflowInitialNotDraft:
		return f("workflow: the first state `%s` is the initial state and must have docstatus 0", e.State)
	case ErrWorkflowName:
		return f("workflow: %s name `%s` %s", e.NameKind, e.Name, e.Why)
	case ErrWorkflowDuplicateState:
		return f("workflow: state `%s` is defined more than once", e.State)
	case ErrWorkflowInvalidDocStatus:
		return f("workflow: state `%s`: docstatus %d is not 0, 1 or 2", e.State, e.Value)
	case ErrWorkflowUnknownStyle:
		return f("workflow: state `%s`: unknown style `%s` (expected one of neutral, success, warning, danger, info)", e.State, e.Found)
	case ErrWorkflowStateNeedsSubmittable:
		return f("workflow: state `%s` has docstatus %d, which needs `submittable = true`", e.State, e.DocStatus)
	case ErrWorkflowEditRolesNotDraft:
		return f("workflow: state `%s`: edit_roles are only allowed on a state with docstatus 0", e.State)
	case ErrWorkflowInvalidRole:
		return f("workflow: %s: invalid role `%s`: %s", e.Place, e.Role, e.Why)
	case ErrWorkflowUnknownState:
		return f("workflow: transition `%s` from `%s`: unknown state `%s` in `%s`", e.Action, e.From, e.State, e.Key)
	case ErrWorkflowDuplicateAction:
		return f("workflow: action `%s` is defined more than once from state `%s`", e.Action, e.From)
	case ErrWorkflowNoRoles:
		return f("workflow: transition `%s` from `%s` needs at least one role", e.Action, e.From)
	case ErrWorkflowDuplicateRole:
		return f("workflow: transition `%s` from `%s`: role `%s` is listed more than once", e.Action, e.From, e.Role)
	case ErrWorkflowSelfTransition:
		return f("workflow: transition `%s` leads from state `%s` to itself", e.Action, e.State)
	case ErrWorkflowBadMove:
		return f("workflow: transition `%s` from `%s` to `%s` moves docstatus %d to %d; only 0 to 0, 0 to 1, 1 to 1 and 1 to 2 are allowed", e.Action, e.From, e.To, e.FromDocStatus, e.ToDocStatus)
	case ErrWorkflowUnreachableState:
		return f("workflow: state `%s` cannot be reached from the initial state", e.State)
	case ErrWorkflowStateFieldName:
		return "field `workflow_state` clashes with the column of the workflow state"
	}
	return "invalid definition"
}

// MetaErrorKind is the variant of a MetaError.
type MetaErrorKind int

const (
	// MetaParse: the text is not valid TOML or does not match the metadata schema (unknown key,
	// wrong value type, missing key).
	MetaParse MetaErrorKind = iota + 1
	// MetaInvalid: the definition parsed but breaks one or more rules; all problems are listed.
	MetaInvalid
)

// MetaError is the error returned when loading a DocType from TOML.
//
// The text of a parse problem (Message) comes from the TOML parser and the schema check; it names
// the key, but it is not the text of the Rust toml crate (see ADR 0027): the parse errors of the
// two implementations agree in kind (every case that Rust refuses is refused), not in wording.
type MetaError struct {
	Kind MetaErrorKind
	// Message is the parser's message, for MetaParse.
	Message string
	// Problems lists every broken rule, in order, for MetaInvalid.
	Problems []DefinitionError
}

func (e *MetaError) Error() string {
	if e.Kind == MetaParse {
		return "cannot parse doctype definition: " + e.Message
	}
	return "invalid doctype definition: " + joinDefinitionErrors(e.Problems)
}

func joinDefinitionErrors(errs []DefinitionError) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}

// ParseDocType parses and validates a DocType from TOML text. The error, if any, is a *MetaError.
//
// The format is described in docs/blueprint/architecture.md section 3 ("DocType file format
// (M1)"); an example is testdata/customer.toml. Every rule violation is reported, not just the
// first. Decimal values with more decimal places than scale are rejected on validation, not
// rounded.
func ParseDocType(text string) (*DocType, error) { return fromTOMLString(text) }

// Field looks up a field by name; nil when there is none.
func (d *DocType) Field(name string) *DocField {
	for i := range d.Fields {
		if d.Fields[i].Name == name {
			return &d.Fields[i]
		}
	}
	return nil
}

// HasFieldLevels reports whether any field has a PermLevel above 0 (M10.2, ADR 0024). A DocType
// without one is never filtered by field levels, whoever the caller is.
func (d *DocType) HasFieldLevels() bool {
	for _, f := range d.Fields {
		if f.PermLevel > 0 {
			return true
		}
	}
	return false
}

// ColumnFields are the fields that have a column in the DocType's table: every field except Table
// fields.
func (d *DocType) ColumnFields() []*DocField {
	var out []*DocField
	for i := range d.Fields {
		if d.Fields[i].Type.Kind != FieldTable {
			out = append(out, &d.Fields[i])
		}
	}
	return out
}

// TableField is a Table field with the name of its child DocType.
type TableField struct {
	Field  *DocField
	Target string
}

// TableFields are the Table fields with the name of their child DocType, in declaration order.
func (d *DocType) TableFields() []TableField {
	var out []TableField
	for i := range d.Fields {
		if d.Fields[i].Type.Kind == FieldTable {
			out = append(out, TableField{Field: &d.Fields[i], Target: d.Fields[i].Type.Target})
		}
	}
	return out
}

// Validate checks every definition rule and returns all problems found (nil when the DocType is
// valid).
func (d *DocType) Validate() []DefinitionError { return d.check(nil) }

// check is Validate with failed: the names of fields that could not be converted (and so are not
// in d.Fields): their names are still checked, and naming errors about them are suppressed because
// the real problem is already reported.
func (d *DocType) check(failed []string) []DefinitionError {
	var errs []DefinitionError
	if err := ValidateDocTypeName(d.Name); err != nil {
		errs = append(errs, DefinitionError{Kind: ErrInvalidDocTypeName, Name: d.Name, Reason: err})
	}
	if strings.TrimSpace(d.Label) == "" {
		errs = append(errs, DefinitionError{Kind: ErrEmptyLabel})
	}
	if strings.ContainsRune(d.Label, 0) {
		errs = append(errs, DefinitionError{Kind: ErrNulInLabel})
	}
	seen := map[string]bool{}
	for i := range d.Fields {
		field := &d.Fields[i]
		if seen[field.Name] {
			errs = append(errs, DefinitionError{Kind: ErrDuplicateField, Field: field.Name})
			continue
		}
		seen[field.Name] = true
		errs = field.check(errs)
	}
	for _, name := range failed {
		if err := ValidateFieldName(name); err != nil {
			errs = append(errs, DefinitionError{Kind: ErrInvalidFieldName, Field: name, Reason: err})
		}
		if seen[name] {
			errs = append(errs, DefinitionError{Kind: ErrDuplicateField, Field: name})
		}
		seen[name] = true
	}
	for i := range d.Fields {
		field := &d.Fields[i]
		if !field.AllowOnSubmit {
			continue
		}
		if field.Type.Kind == FieldTable {
			errs = append(errs, DefinitionError{Kind: ErrAllowOnSubmitTable, Field: field.Name})
		} else if !d.Submittable {
			errs = append(errs, DefinitionError{Kind: ErrAllowOnSubmitNotSubmittable, Field: field.Name})
		}
	}
	for i := range d.Fields {
		field := &d.Fields[i]
		if !field.ReadOnly {
			continue
		}
		if field.Type.Kind == FieldTable {
			errs = append(errs, DefinitionError{Kind: ErrReadOnlyTable, Field: field.Name})
		}
		if field.Required {
			errs = append(errs, DefinitionError{Kind: ErrReadOnlyRequired, Field: field.Name})
		}
	}
	errs = d.checkDeskKeys(failed, errs)
	errs = d.checkPermissions(errs)
	errs = d.checkFieldLevels(errs)
	errs = d.checkWorkflow(failed, errs)
	if d.Submittable {
		if d.IsChild {
			errs = append(errs, DefinitionError{Kind: ErrChildSubmittable})
		}
		if d.Naming.Kind == NamingField {
			errs = append(errs, DefinitionError{Kind: ErrSubmittableWithFieldNaming})
		}
	}
	if d.IsChild {
		if d.Naming.Kind != NamingRandom {
			errs = append(errs, DefinitionError{Kind: ErrChildWithNaming})
		}
		for i := range d.Fields {
			field := &d.Fields[i]
			if field.Type.Kind == FieldTable {
				errs = append(errs, DefinitionError{Kind: ErrChildWithTable, Field: field.Name})
			}
			// A unique Table field is reported by the field check.
			if field.Unique && field.Type.Kind != FieldTable {
				errs = append(errs, DefinitionError{Kind: ErrChildWithUnique, Field: field.Name})
			}
		}
	} else if d.Naming.Kind == NamingField && !slices.Contains(failed, d.Naming.Field) {
		errs = d.checkNamingField(d.Naming.Field, errs)
	}
	return errs
}

// checkDeskKeys checks title_field, list_fields and icon. A name in failed is a field that could
// not be converted; its real problem is already reported, so it is not reported again as missing.
func (d *DocType) checkDeskKeys(failed []string, errs []DefinitionError) []DefinitionError {
	if d.IsChild {
		if d.TitleField != nil {
			errs = append(errs, DefinitionError{Kind: ErrChildWithDeskKey, Key: "title_field"})
		}
		if d.ListFields != nil {
			errs = append(errs, DefinitionError{Kind: ErrChildWithDeskKey, Key: "list_fields"})
		}
		if d.Icon != nil {
			errs = append(errs, DefinitionError{Kind: ErrChildWithDeskKey, Key: "icon"})
		}
		return errs
	}
	if d.Icon != nil && !slices.Contains(DocTypeIcons(), *d.Icon) {
		errs = append(errs, DefinitionError{Kind: ErrUnknownIcon, Icon: *d.Icon, Allowed: strings.Join(DocTypeIcons(), ", ")})
	}
	if d.TitleField != nil && !slices.Contains(failed, *d.TitleField) {
		name := *d.TitleField
		f := d.Field(name)
		switch {
		case f == nil:
			errs = append(errs, DefinitionError{Kind: ErrTitleFieldMissing, Field: name})
		case !slices.Contains(TitleFieldKinds[:], f.Type.Name()):
			errs = append(errs, DefinitionError{Kind: ErrTitleFieldKind, Field: name, Found: f.Type.Name()})
		}
	}
	if d.ListFields != nil {
		list := d.ListFields
		if len(list) == 0 {
			errs = append(errs, DefinitionError{Kind: ErrEmptyListFields})
		}
		if len(list) > MaxListFields {
			errs = append(errs, DefinitionError{Kind: ErrTooManyListFields, Count: len(list), Max: MaxListFields})
		}
		seen := map[string]bool{}
		for _, name := range list {
			if seen[name] {
				errs = append(errs, DefinitionError{Kind: ErrDuplicateListField, Field: name})
				continue
			}
			seen[name] = true
			if slices.Contains(failed, name) {
				continue
			}
			f := d.Field(name)
			switch {
			case f == nil:
				errs = append(errs, DefinitionError{Kind: ErrListFieldMissing, Field: name})
			case f.Type.Kind == FieldTable || f.Type.Kind == FieldText:
				errs = append(errs, DefinitionError{Kind: ErrListFieldKind, Field: name, Found: f.Type.Name()})
			}
		}
	}
	return errs
}

func (d *DocType) checkPermissions(errs []DefinitionError) []DefinitionError {
	if d.IsChild && len(d.Permissions) > 0 {
		errs = append(errs, DefinitionError{Kind: ErrChildWithPermissions})
	}
	type ownerKey struct {
		role    string
		ifOwner bool
	}
	type levelKey struct {
		role  string
		level uint8
	}
	seen := map[ownerKey]bool{}
	seenLevels := map[levelKey]bool{}
	for _, rule := range d.Permissions {
		if reason := CheckRoleName(rule.Role); reason != "" {
			errs = append(errs, DefinitionError{Kind: ErrInvalidPermissionRole, Role: rule.Role, Why: reason})
		}
		granted := false
		for _, right := range AllRights {
			if rule.Grants(right) {
				granted = true
			}
		}
		if !granted {
			errs = append(errs, DefinitionError{Kind: ErrEmptyPermissionRule, Role: rule.Role})
		}
		if !d.Submittable {
			for _, right := range []Right{RightSubmit, RightCancel, RightAmend} {
				if rule.Grants(right) {
					errs = append(errs, DefinitionError{Kind: ErrPermissionNeedsSubmittable, Role: rule.Role, Right: right})
				}
			}
		}
		switch {
		case rule.Level > MaxPermLevel:
			errs = append(errs, DefinitionError{Kind: ErrInvalidRuleLevel, Role: rule.Role, Value: int64(rule.Level)})
		case rule.Level > 0:
			errs = checkLevelRule(rule, seenLevels, errs, func(role string, level uint8) levelKey { return levelKey{role, level} })
		default:
			k := ownerKey{rule.Role, rule.IfOwner}
			if seen[k] {
				errs = append(errs, DefinitionError{Kind: ErrDuplicatePermissionRule, Role: rule.Role, IfOwner: rule.IfOwner})
			}
			seen[k] = true
		}
	}
	return errs
}

// checkLevelRule applies the rules of a rule with Level above 0 (M10.2, ADR 0024): read and write
// only, no if_owner, no write without read, and one rule per role and level.
func checkLevelRule[K comparable](rule PermissionRule, seen map[K]bool, errs []DefinitionError, key func(string, uint8) K) []DefinitionError {
	for _, right := range AllRights {
		if rule.Grants(right) && right != RightRead && right != RightWrite {
			errs = append(errs, DefinitionError{Kind: ErrLevelRuleRight, Role: rule.Role, Level: rule.Level, Right: right})
		}
	}
	if rule.IfOwner {
		errs = append(errs, DefinitionError{Kind: ErrLevelRuleIfOwner, Role: rule.Role, Level: rule.Level})
	}
	if rule.Write && !rule.Read {
		errs = append(errs, DefinitionError{Kind: ErrLevelWriteWithoutRead, Role: rule.Role, Level: rule.Level})
	}
	k := key(rule.Role, rule.Level)
	if seen[k] {
		errs = append(errs, DefinitionError{Kind: ErrDuplicateLevelRule, Role: rule.Role, Level: rule.Level})
	}
	seen[k] = true
	return errs
}

// checkFieldLevels checks the perm_level of the fields (M10.2, ADR 0024): in range, not on a child
// DocType, not on a field that a DocType setting names, not together with required.
//
// The DocType settings that name a field are the naming field, title_field and list_fields; the
// workflow names no field (its state is the standard column workflow_state, and a field of that
// name is refused), and [[permissions]], the icon and the series pattern name none either.
func (d *DocType) checkFieldLevels(errs []DefinitionError) []DefinitionError {
	for i := range d.Fields {
		field := &d.Fields[i]
		if field.PermLevel > MaxPermLevel {
			errs = append(errs, DefinitionError{Kind: ErrInvalidPermLevel, Field: field.Name, Value: int64(field.PermLevel)})
			continue
		}
		if field.PermLevel == 0 {
			continue
		}
		if d.IsChild {
			errs = append(errs, DefinitionError{Kind: ErrChildFieldPermLevel, Field: field.Name})
		}
		if field.Required {
			errs = append(errs, DefinitionError{Kind: ErrRequiredPermLevel, Field: field.Name})
		}
		usedAs := ""
		switch {
		case d.Naming.Kind == NamingField && d.Naming.Field == field.Name:
			usedAs = "the naming field"
		case d.TitleField != nil && *d.TitleField == field.Name:
			usedAs = "the title_field"
		case slices.Contains(d.ListFields, field.Name):
			usedAs = "a list_fields column"
		}
		if usedAs != "" {
			errs = append(errs, DefinitionError{Kind: ErrKeyFieldPermLevel, Field: field.Name, UsedAs: usedAs})
		}
	}
	return errs
}

// checkWorkflow checks the [workflow]: names, docstatus rules, roles, transitions and reachability.
func (d *DocType) checkWorkflow(failed []string, errs []DefinitionError) []DefinitionError {
	workflow := d.Workflow
	if workflow == nil {
		return errs
	}
	if d.IsChild {
		return append(errs, DefinitionError{Kind: ErrChildWithWorkflow})
	}
	if d.Field(WorkflowColumn) != nil || slices.Contains(failed, WorkflowColumn) {
		errs = append(errs, DefinitionError{Kind: ErrWorkflowStateFieldName})
	}
	initial := workflow.Initial()
	if initial == nil {
		return append(errs, DefinitionError{Kind: ErrWorkflowNoStates})
	}
	if initial.DocStatus != Draft {
		errs = append(errs, DefinitionError{Kind: ErrWorkflowInitialNotDraft, State: initial.Name})
	}
	names := map[string]bool{}
	for _, state := range workflow.States {
		errs = checkWorkflowName("state", state.Name, errs)
		if names[state.Name] {
			errs = append(errs, DefinitionError{Kind: ErrWorkflowDuplicateState, State: state.Name})
		}
		names[state.Name] = true
		if state.DocStatus != Draft && !d.Submittable {
			errs = append(errs, DefinitionError{Kind: ErrWorkflowStateNeedsSubmittable, State: state.Name, DocStatus: state.DocStatus.Int16()})
		}
		if len(state.EditRoles) > 0 && state.DocStatus != Draft {
			errs = append(errs, DefinitionError{Kind: ErrWorkflowEditRolesNotDraft, State: state.Name})
		}
		place := fmt.Sprintf("state `%s` edit_roles", state.Name)
		for _, role := range state.EditRoles {
			errs = checkWorkflowRole(place, role, errs)
		}
	}
	type actionKey struct{ from, action string }
	actions := map[actionKey]bool{}
	for i := range workflow.Transitions {
		t := &workflow.Transitions[i]
		errs = checkWorkflowName("action", t.Action, errs)
		k := actionKey{t.From, t.Action}
		if actions[k] {
			errs = append(errs, DefinitionError{Kind: ErrWorkflowDuplicateAction, Action: t.Action, From: t.From})
		}
		actions[k] = true
		for _, kv := range [][2]string{{"from", t.From}, {"to", t.To}} {
			if !names[kv[1]] {
				errs = append(errs, DefinitionError{Kind: ErrWorkflowUnknownState, Action: t.Action, From: t.From, Key: kv[0], State: kv[1]})
			}
		}
		if len(t.Roles) == 0 {
			errs = append(errs, DefinitionError{Kind: ErrWorkflowNoRoles, Action: t.Action, From: t.From})
		}
		place := fmt.Sprintf("transition `%s` from `%s`", t.Action, t.From)
		roles := map[string]bool{}
		for _, role := range t.Roles {
			errs = checkWorkflowRole(place, role, errs)
			if roles[role] {
				errs = append(errs, DefinitionError{Kind: ErrWorkflowDuplicateRole, Action: t.Action, From: t.From, Role: role})
			}
			roles[role] = true
		}
		if t.From == t.To && names[t.From] {
			errs = append(errs, DefinitionError{Kind: ErrWorkflowSelfTransition, Action: t.Action, State: t.From})
		} else if from, to := workflow.State(t.From), workflow.State(t.To); from != nil && to != nil {
			if _, ok := MoveOf(from.DocStatus, to.DocStatus); !ok {
				errs = append(errs, DefinitionError{
					Kind: ErrWorkflowBadMove, Action: t.Action, From: t.From, To: t.To,
					FromDocStatus: from.DocStatus.Int16(), ToDocStatus: to.DocStatus.Int16(),
				})
			}
		}
	}
	// Reachability from the initial state, over the transitions that name existing states.
	reached := map[string]bool{initial.Name: true}
	queue := []string{initial.Name}
	for len(queue) > 0 {
		from := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, t := range workflow.TransitionsFrom(from) {
			if names[t.To] && !reached[t.To] {
				reached[t.To] = true
				queue = append(queue, t.To)
			}
		}
	}
	for _, state := range workflow.States {
		if !reached[state.Name] {
			errs = append(errs, DefinitionError{Kind: ErrWorkflowUnreachableState, State: state.Name})
		}
	}
	return errs
}

func (d *DocType) checkNamingField(name string, errs []DefinitionError) []DefinitionError {
	f := d.Field(name)
	if f == nil {
		return append(errs, DefinitionError{Kind: ErrNamingFieldMissing, Field: name})
	}
	if f.Type.Kind != FieldData {
		errs = append(errs, DefinitionError{Kind: ErrNamingFieldNotData, Field: name, Found: f.Type.Name()})
	}
	if !f.Required {
		errs = append(errs, DefinitionError{Kind: ErrNamingFieldNotRequired, Field: name})
	}
	if !f.Unique {
		errs = append(errs, DefinitionError{Kind: ErrNamingFieldNotUnique, Field: name})
	}
	return errs
}

// checkWorkflowName checks a state or action name: not blank, no leading or trailing whitespace, at
// most MaxNameLen characters, no NUL.
func checkWorkflowName(kind, name string, errs []DefinitionError) []DefinitionError {
	var reason string
	switch {
	case strings.TrimSpace(name) == "":
		reason = "must not be empty"
	case strings.TrimSpace(name) != name:
		reason = "must not start or end with whitespace"
	case utf8.RuneCountInString(name) > MaxNameLen:
		reason = "is longer than 64 characters"
	case strings.ContainsRune(name, 0):
		reason = "must not contain a NUL character"
	default:
		return errs
	}
	return append(errs, DefinitionError{Kind: ErrWorkflowName, NameKind: kind, Name: name, Why: reason})
}

func checkWorkflowRole(place, role string, errs []DefinitionError) []DefinitionError {
	if reason := CheckRoleName(role); reason != "" {
		errs = append(errs, DefinitionError{Kind: ErrWorkflowInvalidRole, Place: place, Role: role, Why: reason})
	}
	return errs
}

func (f *DocField) check(errs []DefinitionError) []DefinitionError {
	if err := ValidateFieldName(f.Name); err != nil {
		errs = append(errs, DefinitionError{Kind: ErrInvalidFieldName, Field: f.Name, Reason: err})
	}
	if strings.TrimSpace(f.Label) == "" {
		errs = append(errs, DefinitionError{Kind: ErrEmptyFieldLabel, Field: f.Name})
	}
	if strings.ContainsRune(f.Label, 0) {
		errs = append(errs, DefinitionError{Kind: ErrNulInFieldLabel, Field: f.Name})
	}
	switch f.Type.Kind {
	case FieldData:
		if f.Type.MaxLength < 1 || f.Type.MaxLength > MaxDataLength {
			errs = append(errs, DefinitionError{Kind: ErrMaxLengthOutOfRange, Field: f.Name, Value: int64(f.Type.MaxLength)})
		}
	case FieldDecimal:
		if f.Type.Precision < 1 || f.Type.Precision > MaxDecimalPrecision {
			errs = append(errs, DefinitionError{Kind: ErrPrecisionOutOfRange, Field: f.Name, Value: int64(f.Type.Precision)})
		} else if f.Type.Scale > f.Type.Precision {
			errs = append(errs, DefinitionError{Kind: ErrScaleExceedsPrecision, Field: f.Name, Scale: f.Type.Scale, Precision: f.Type.Precision})
		}
	case FieldSelect:
		if len(f.Type.Options) == 0 {
			errs = append(errs, DefinitionError{Kind: ErrEmptyOptions, Field: f.Name})
		}
		seen := map[string]bool{}
		for _, option := range f.Type.Options {
			switch {
			case strings.ContainsRune(option, 0):
				errs = append(errs, DefinitionError{Kind: ErrNulInOption, Field: f.Name})
			case option == "":
				errs = append(errs, DefinitionError{Kind: ErrEmptyOption, Field: f.Name})
			case seen[option]:
				errs = append(errs, DefinitionError{Kind: ErrDuplicateOption, Field: f.Name, Option: option})
			default:
				seen[option] = true
			}
		}
	case FieldLink:
		if err := ValidateDocTypeName(f.Type.Target); err != nil {
			errs = append(errs, DefinitionError{Kind: ErrInvalidLinkTarget, Field: f.Name, Target: f.Type.Target, Reason: err})
		}
	case FieldTable:
		if err := ValidateDocTypeName(f.Type.Target); err != nil {
			errs = append(errs, DefinitionError{Kind: ErrInvalidTableTarget, Field: f.Name, Target: f.Type.Target, Reason: err})
		}
		if f.Unique {
			errs = append(errs, DefinitionError{Kind: ErrUniqueTable, Field: f.Name})
		}
	}
	return errs
}

// FieldSourceKind says who added a field to a DocType that it was not defined with (M10.1, ADR
// 0023).
type FieldSourceKind int

const (
	// SourceApp: an app overlay: the name of the app that owns the field (FieldSource.App).
	SourceApp FieldSourceKind = iota + 1
	// SourceSite: a site custom field.
	SourceSite
)

// FieldSource says who added a field to a DocType that it was not defined with.
type FieldSource struct {
	Kind FieldSourceKind
	App  string
}

// Registry is a set of DocTypes keyed by name, an optional owner tag per DocType, and the lifecycle
// hooks the app registered for them.
//
// The zero Registry is not usable: make one with NewRegistry. A Registry is mutated while the app
// starts up (single goroutine) and is read-only afterwards; the DocTypes it holds are immutable
// (Clone makes a Registry that shares them, the live reload of overlays swaps whole Registries). It
// is not safe to mutate while another goroutine reads it.
type Registry struct {
	doctypes map[string]*DocType
	// owners maps a DocType name to the tag of the app that owns it (only for DocTypes added with
	// AddOwned).
	owners map[string]string
	// addedFields maps (DocType, field) to who added the field after the DocType was defined: an
	// app overlay or the site (M10.1, ADR 0023). Fields of the DocType file itself are not listed.
	addedFields map[[2]string]FieldSource
	hooks       map[string][]DocTypeHooks
	// jobs are the full names (<app>.<job>) of the background jobs the binary knows. Names only:
	// the registry knows nothing about handlers.
	jobs map[string]bool
	// reports are the query reports (M8.4, ADR 0016) of the apps, by full name (<app>.<name>).
	reports map[string]*ReportDef
	// methods are the app methods (M9.1, ADR 0019) by full name (<app>.<name>). Names and access
	// only: the registry knows nothing about handlers.
	methods map[string]*MethodDef
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		doctypes:    map[string]*DocType{},
		owners:      map[string]string{},
		addedFields: map[[2]string]FieldSource{},
		hooks:       map[string][]DocTypeHooks{},
		jobs:        map[string]bool{},
		reports:     map[string]*ReportDef{},
		methods:     map[string]*MethodDef{},
	}
}

// Clone copies the registry. The DocTypes, reports, methods and hook sets are shared with the
// original (they are immutable); the lists and maps are copied, so adding to the clone does not
// change the original.
func (r *Registry) Clone() *Registry {
	c := NewRegistry()
	for k, v := range r.doctypes {
		c.doctypes[k] = v
	}
	for k, v := range r.owners {
		c.owners[k] = v
	}
	for k, v := range r.addedFields {
		c.addedFields[k] = v
	}
	for k, v := range r.hooks {
		c.hooks[k] = slices.Clone(v)
	}
	for k, v := range r.jobs {
		c.jobs[k] = v
	}
	for k, v := range r.reports {
		c.reports[k] = v
	}
	for k, v := range r.methods {
		c.methods[k] = v
	}
	return c
}

// AddJob records a background job name (<app>.<job>) as known, so that enqueueing it is accepted. It
// returns false when the name was already known. The name is not checked here.
func (r *Registry) AddJob(name string) bool {
	if r.jobs[name] {
		return false
	}
	r.jobs[name] = true
	return true
}

// HasJob reports whether a job with this full name is known.
func (r *Registry) HasJob(name string) bool { return r.jobs[name] }

// JobNames are the known job names, in name order.
func (r *Registry) JobNames() []string { return sortedKeys(r.jobs) }

// AddReport records a query report under its full name (<app>.<name>). It returns false, and keeps
// the report it has, when the name is already taken. The report is not checked here
// (ReportDef.Problems). The registry keeps the pointer: do not change the report afterwards.
func (r *Registry) AddReport(report *ReportDef) bool {
	name := report.FullName()
	if _, ok := r.reports[name]; ok {
		return false
	}
	r.reports[name] = report
	return true
}

// Report is the report with this full name (<app>.<name>), or nil.
func (r *Registry) Report(fullName string) *ReportDef { return r.reports[fullName] }

// Reports are every report, in full-name order.
func (r *Registry) Reports() []*ReportDef {
	keys := sortedKeys(r.reports)
	out := make([]*ReportDef, len(keys))
	for i, k := range keys {
		out[i] = r.reports[k]
	}
	return out
}

// AddMethod records an app method under its full name (<app>.<name>). It returns false, and keeps
// the method it has, when the name is already taken. The method is not checked here
// (MethodDef.Problems).
func (r *Registry) AddMethod(method MethodDef) bool {
	name := method.FullName()
	if _, ok := r.methods[name]; ok {
		return false
	}
	r.methods[name] = &method
	return true
}

// Method is the method with this full name (<app>.<name>), or nil.
func (r *Registry) Method(fullName string) *MethodDef { return r.methods[fullName] }

// Methods are every method, in full-name order.
func (r *Registry) Methods() []*MethodDef {
	keys := sortedKeys(r.methods)
	out := make([]*MethodDef, len(keys))
	for i, k := range keys {
		out[i] = r.methods[k]
	}
	return out
}

// Add validates and adds a DocType. It fails with every definition problem found
// (DocType.Validate), or with ErrDuplicateDocType if one with the same name is already present. The
// registry therefore only holds valid DocTypes. The registry keeps the pointer: do not change the
// DocType afterwards.
//
// Only the DocType itself is checked: a Link or Table field may name a DocType that is added later
// (definition files are loaded in any order). Call Registry.CheckReferences after the last DocType
// has been added.
func (r *Registry) Add(doctype *DocType) []DefinitionError {
	if errs := doctype.Validate(); errs != nil {
		return errs
	}
	if _, ok := r.doctypes[doctype.Name]; ok {
		return []DefinitionError{{Kind: ErrDuplicateDocType, Name: doctype.Name}}
	}
	r.doctypes[doctype.Name] = doctype
	return nil
}

// AddOwned is like Add, and records owner as the owner of the DocType (Registry.Owner). The owner is
// only a tag (an app name): this package attaches no rules to it.
func (r *Registry) AddOwned(owner string, doctype *DocType) []DefinitionError {
	name := doctype.Name
	if errs := r.Add(doctype); errs != nil {
		return errs
	}
	r.owners[name] = owner
	return nil
}

// Owner is the owner tag given to AddOwned; false for a DocType added with Add (and for an unknown
// name).
func (r *Registry) Owner(doctype string) (string, bool) {
	o, ok := r.owners[doctype]
	return o, ok
}

// FieldSourceOf says who added field to doctype after its definition: false for a field of the
// DocType's own definition (and for an unknown name).
func (r *Registry) FieldSourceOf(doctype, field string) (FieldSource, bool) {
	s, ok := r.addedFields[[2]string{doctype, field}]
	return s, ok
}

// FieldOwner is the app that owns field of doctype: the app that added it by an overlay, else the
// owner of the DocType; false for a site custom field or an unknown name.
func (r *Registry) FieldOwner(doctype, field string) (string, bool) {
	if s, ok := r.FieldSourceOf(doctype, field); ok {
		if s.Kind == SourceApp {
			return s.App, true
		}
		return "", false
	}
	return r.Owner(doctype)
}

// Get looks up a DocType by name; nil when there is none.
func (r *Registry) Get(name string) *DocType { return r.doctypes[name] }

// Iter returns the DocTypes in name order.
func (r *Registry) Iter() []*DocType {
	keys := sortedKeys(r.doctypes)
	out := make([]*DocType, len(keys))
	for i, k := range keys {
		out[i] = r.doctypes[k]
	}
	return out
}

// Len is the number of registered DocTypes.
func (r *Registry) Len() int { return len(r.doctypes) }

// IsEmpty is true when no DocType is registered.
func (r *Registry) IsEmpty() bool { return len(r.doctypes) == 0 }

// AddHooks registers a hook set for a DocType. The DocType must already be in the registry
// (ErrUnknownHooksDocType) and must not be a child DocType (ErrHooksOnChildDocType: a child table has
// no lifecycle of its own, the hooks of its parent see its rows). It returns nil or a
// DefinitionError.
//
// Several hook sets may be registered for one DocType; they run in registration order (see the
// comment in hooks.go for the pipeline). Registration is explicit and happens at startup (ADR 0006,
// ADR 0009).
func (r *Registry) AddHooks(doctype string, hooks DocTypeHooks) error {
	found := r.doctypes[doctype]
	switch {
	case found == nil:
		return DefinitionError{Kind: ErrUnknownHooksDocType, Name: doctype}
	case found.IsChild:
		return DefinitionError{Kind: ErrHooksOnChildDocType, DocType: doctype}
	}
	r.hooks[doctype] = append(r.hooks[doctype], hooks)
	return nil
}

// Hooks are the hook sets registered for a DocType, in registration order (empty when there are none
// or the DocType is unknown). The slice is clipped: appending to it never changes the registry.
func (r *Registry) Hooks(doctype string) []DocTypeHooks {
	return slices.Clip(r.hooks[doctype])
}

// CheckReferences checks that the DocTypes of this registry fit together. It returns every problem
// found (nil when all is well):
//
//   - a Link field refers to a DocType that is in the registry (a DocType may link to itself) and is
//     not a child DocType;
//   - a Table field refers to a DocType that is in the registry and is a child DocType;
//   - every child DocType is the target of exactly one Table field (it belongs to exactly one
//     parent).
//
// Registry.Add cannot do this because DocTypes arrive one at a time. Call it once after all DocTypes
// are loaded; internal/db's sync and the CLI loader do.
func (r *Registry) CheckReferences() []DefinitionError {
	var errs []DefinitionError
	// child DocType -> the doctype.field of every Table field that targets it
	usedBy := map[string][]string{}
	for _, doctype := range r.Iter() {
		for i := range doctype.Fields {
			field := &doctype.Fields[i]
			switch field.Type.Kind {
			case FieldLink:
				t := r.doctypes[field.Type.Target]
				switch {
				case t == nil:
					errs = append(errs, DefinitionError{Kind: ErrUnknownLinkTarget, DocType: doctype.Name, Field: field.Name, Target: field.Type.Target})
				case t.IsChild:
					errs = append(errs, DefinitionError{Kind: ErrLinkToChild, DocType: doctype.Name, Field: field.Name, Target: field.Type.Target})
				}
			case FieldTable:
				t := r.doctypes[field.Type.Target]
				switch {
				case t == nil:
					errs = append(errs, DefinitionError{Kind: ErrUnknownTableTarget, DocType: doctype.Name, Field: field.Name, Target: field.Type.Target})
				case !t.IsChild:
					errs = append(errs, DefinitionError{Kind: ErrTableTargetNotChild, DocType: doctype.Name, Field: field.Name, Target: field.Type.Target})
				default:
					usedBy[field.Type.Target] = append(usedBy[field.Type.Target], doctype.Name+"."+field.Name)
				}
			}
		}
	}
	for _, child := range r.Iter() {
		if !child.IsChild {
			continue
		}
		uses, ok := usedBy[child.Name]
		switch {
		case !ok:
			errs = append(errs, DefinitionError{Kind: ErrUnusedChild, DocType: child.Name})
		case len(uses) > 1:
			errs = append(errs, DefinitionError{Kind: ErrChildSharedByTables, Child: child.Name, Uses: uses})
		}
	}
	return errs
}

// ParentOf is the DocType and Table field that own the rows of child DocType child, if a Table field
// targets it. After CheckReferences succeeded that is exactly one for every child DocType. Both are
// nil when none does.
func (r *Registry) ParentOf(child string) (*DocType, *DocField) {
	for _, doctype := range r.Iter() {
		for _, tf := range doctype.TableFields() {
			if tf.Target == child {
				return doctype, tf.Field
			}
		}
	}
	return nil, nil
}
