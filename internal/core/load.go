package core

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// TOML loading: parse into a raw, string-typed form, then convert into the validated model.
//
// The TOML text is parsed by github.com/BurntSushi/toml into a generic tree (map[string]any), and
// the schema of the Rust serde structs (deny_unknown_fields, the value types, the defaults) is
// applied here by a small strict decoder. This is deliberate: decoding into Go structs would match
// keys case-insensitively and would not give "unknown key" and "missing key" errors of the same
// kind as serde (see ADR 0027). Every case that the Rust loader refuses as a parse error is refused
// here; only the wording of the message differs.

// decoder reads the keys of one TOML table or JSON object. The first problem is kept; the getters
// return zero values once there is one.
type decoder struct {
	path string
	m    map[string]any
	err  string
}

// describe is serde's "invalid type" description of a value.
func describe(v any) string {
	switch x := v.(type) {
	case string:
		return fmt.Sprintf("string %q", x)
	case int64:
		return fmt.Sprintf("integer `%d`", x)
	case float64:
		return "floating point `" + formatFloat(x) + "`"
	case bool:
		return fmt.Sprintf("boolean `%v`", x)
	case nil:
		return "null"
	case map[string]any:
		return "map"
	case []any, []map[string]any:
		return "sequence"
	}
	return fmt.Sprintf("%T", v)
}

// formatFloat prints a float like serde does in "invalid type" messages (5.0, 1.5, 100.0).
func formatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64) // -0 formats as "-0"
	if !strings.ContainsAny(s, ".eEIN") {
		s += ".0"
	}
	return s
}

func newDecoder(path string, v any, allowed ...string) *decoder {
	d := &decoder{path: path}
	m, ok := v.(map[string]any)
	if !ok {
		d.fail("invalid type: %s, expected a map", describe(v))
		return d
	}
	d.m = m
	var unknown []string
	for k := range m {
		if !contains(allowed, k) {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		expected := "there are no fields"
		if len(allowed) > 0 {
			quoted := make([]string, len(allowed))
			for i, a := range allowed {
				quoted[i] = "`" + a + "`"
			}
			expected = "expected one of " + strings.Join(quoted, ", ")
			if len(allowed) == 1 {
				expected = "expected `" + allowed[0] + "`"
			}
		}
		d.fail("unknown field `%s`, %s", unknown[0], expected)
	}
	return d
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (d *decoder) fail(format string, args ...any) {
	if d.err != "" {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if d.path != "" {
		msg = d.path + ": " + msg
	}
	d.err = msg
}

// get returns the value of key, and whether it is present. A JSON null is present (as nil): for
// a field that is not an Option in the Rust struct, serde refuses it ("invalid type: null").
func (d *decoder) get(key string) (any, bool) {
	if d.err != "" {
		return nil, false
	}
	v, ok := d.m[key]
	return v, ok
}

// getOpt is get for an Option<T> field of the Rust struct: a JSON null counts as absent.
func (d *decoder) getOpt(key string) (any, bool) {
	v, ok := d.get(key)
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

func (d *decoder) missing(key string) { d.fail("missing field `%s`", key) }

func (d *decoder) reqString(key string) string {
	v, ok := d.get(key)
	if !ok {
		d.missing(key)
		return ""
	}
	s, isString := v.(string)
	if !isString {
		d.fail("invalid type: %s, expected a string", describe(v))
	}
	return s
}

func (d *decoder) optString(key string) *string {
	v, ok := d.getOpt(key)
	if !ok {
		return nil
	}
	s, isString := v.(string)
	if !isString {
		d.fail("invalid type: %s, expected a string", describe(v))
		return nil
	}
	return &s
}

func (d *decoder) boolean(key string) bool {
	v, ok := d.get(key)
	if !ok {
		return false
	}
	b, isBool := v.(bool)
	if !isBool {
		d.fail("invalid type: %s, expected a boolean", describe(v))
	}
	return b
}

// integer is an i64 key with a default of 0.
func (d *decoder) integer(key string) int64 {
	v, ok := d.get(key)
	if !ok {
		return 0
	}
	n, isInt := v.(int64)
	if !isInt {
		d.fail("invalid type: %s, expected i64", describe(v))
	}
	return n
}

// reqInteger is a required i64 key.
func (d *decoder) reqInteger(key string) int64 {
	if _, ok := d.get(key); !ok {
		if d.err == "" {
			d.missing(key)
		}
		return 0
	}
	return d.integer(key)
}

// optUint is an optional unsigned integer key that must fit in bits bits.
func (d *decoder) optUint(key string, bits uint, typeName string) *uint64 {
	v, ok := d.getOpt(key)
	if !ok {
		return nil
	}
	n, isInt := v.(int64)
	if !isInt {
		d.fail("invalid type: %s, expected %s", describe(v), typeName)
		return nil
	}
	if n < 0 || uint64(n) >= uint64(1)<<bits {
		d.fail("invalid value: integer `%d`, expected %s", n, typeName)
		return nil
	}
	u := uint64(n)
	return &u
}

func asSlice(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return x, true
	case []map[string]any:
		out := make([]any, len(x))
		for i, m := range x {
			out[i] = m
		}
		return out, true
	}
	return nil, false
}

// stringList is a list of strings (option: the Rust field is an Option, so a JSON null is
// absent); present is false when the key is absent. A present list is never nil (an empty one is
// empty).
func (d *decoder) stringList(key string, option bool) (list []string, present bool) {
	get := d.get
	if option {
		get = d.getOpt
	}
	v, ok := get(key)
	if !ok {
		return nil, false
	}
	items, isList := asSlice(v)
	if !isList {
		d.fail("invalid type: %s, expected a sequence", describe(v))
		return nil, false
	}
	list = make([]string, 0, len(items))
	for _, item := range items {
		s, isString := item.(string)
		if !isString {
			d.fail("invalid type: %s, expected a string", describe(item))
			return nil, false
		}
		list = append(list, s)
	}
	return list, true
}

// tables is an optional list of tables (a missing key is an empty list).
func (d *decoder) tables(key string) []any {
	v, ok := d.get(key)
	if !ok {
		return nil
	}
	items, isList := asSlice(v)
	if !isList {
		d.fail("invalid type: %s, expected a sequence", describe(v))
		return nil
	}
	return items
}

// ----- the raw forms of the Rust serde structs --------------------------------------------------

type rawDocType struct {
	name, label string
	naming      any
	hasNaming   bool
	isChild     bool
	submittable bool
	fields      []rawField
	permissions []rawPermission
	titleField  *string
	listFields  []string
	hasList     bool
	icon        *string
	workflow    *rawWorkflow
}

type rawWorkflow struct {
	states      []rawState
	transitions []rawTransition
}

type rawState struct {
	name      string
	docstatus int64
	style     *string
	editRoles []string
}

type rawTransition struct {
	action, from, to string
	roles            []string
}

type rawPermission struct {
	role                                                                                  string
	read, write, create, deleteRight, submit, cancel, amend, export, importRight, ifOwner bool
	level                                                                                 int64
}

type rawField struct {
	name          string
	label         *string
	fieldType     string
	required      bool
	unique        bool
	allowOnSubmit bool
	readOnly      bool
	permLevel     int64
	maxLength     *uint32
	precision     *uint8
	scale         *uint8
	options       []string
	hasOptions    bool
	target        *string
}

var (
	docTypeKeys    = []string{"name", "label", "naming", "is_child", "submittable", "fields", "permissions", "title_field", "list_fields", "icon", "workflow"}
	workflowKeys   = []string{"states", "transitions"}
	stateKeys      = []string{"name", "docstatus", "style", "edit_roles"}
	transitionKeys = []string{"action", "from", "to", "roles"}
	permissionKeys = []string{"role", "read", "write", "create", "delete", "submit", "cancel", "amend", "export", "import", "if_owner", "level"}
	fieldKeys      = []string{"name", "label", "type", "required", "unique", "allow_on_submit", "read_only", "perm_level", "max_length", "precision", "scale", "options", "target"}
)

func decodeDocType(root any) (*rawDocType, string) {
	d := newDecoder("", root, docTypeKeys...)
	raw := &rawDocType{}
	raw.name = d.reqString("name")
	raw.label = d.reqString("label")
	if v, ok := d.getOpt("naming"); ok {
		raw.naming, raw.hasNaming = v, true
	}
	raw.isChild = d.boolean("is_child")
	raw.submittable = d.boolean("submittable")
	for i, item := range d.tables("fields") {
		f, msg := decodeRawField(fmt.Sprintf("fields[%d]", i), item)
		if msg != "" {
			d.fail("%s", msg)
			break
		}
		raw.fields = append(raw.fields, *f)
	}
	for i, item := range d.tables("permissions") {
		p, msg := decodePermission(fmt.Sprintf("permissions[%d]", i), item)
		if msg != "" {
			d.fail("%s", msg)
			break
		}
		raw.permissions = append(raw.permissions, *p)
	}
	raw.titleField = d.optString("title_field")
	raw.listFields, raw.hasList = d.stringList("list_fields", true)
	raw.icon = d.optString("icon")
	if v, ok := d.getOpt("workflow"); ok {
		w, msg := decodeWorkflow("workflow", v)
		if msg != "" {
			d.fail("%s", msg)
		}
		raw.workflow = w
	}
	return raw, d.err
}

func decodeWorkflow(path string, v any) (*rawWorkflow, string) {
	d := newDecoder(path, v, workflowKeys...)
	w := &rawWorkflow{}
	for i, item := range d.tables("states") {
		sd := newDecoder(fmt.Sprintf("%s.states[%d]", path, i), item, stateKeys...)
		s := rawState{}
		s.name = sd.reqString("name")
		s.docstatus = sd.reqInteger("docstatus")
		s.style = sd.optString("style")
		s.editRoles, _ = sd.stringList("edit_roles", false)
		if sd.err != "" {
			d.fail("%s", sd.err)
			break
		}
		w.states = append(w.states, s)
	}
	for i, item := range d.tables("transitions") {
		td := newDecoder(fmt.Sprintf("%s.transitions[%d]", path, i), item, transitionKeys...)
		t := rawTransition{}
		t.action = td.reqString("action")
		t.from = td.reqString("from")
		t.to = td.reqString("to")
		t.roles, _ = td.stringList("roles", false)
		if td.err != "" {
			d.fail("%s", td.err)
			break
		}
		w.transitions = append(w.transitions, t)
	}
	return w, d.err
}

func decodePermission(path string, v any) (*rawPermission, string) {
	d := newDecoder(path, v, permissionKeys...)
	p := &rawPermission{}
	p.role = d.reqString("role")
	p.read = d.boolean("read")
	p.write = d.boolean("write")
	p.create = d.boolean("create")
	p.deleteRight = d.boolean("delete")
	p.submit = d.boolean("submit")
	p.cancel = d.boolean("cancel")
	p.amend = d.boolean("amend")
	p.export = d.boolean("export")
	p.importRight = d.boolean("import")
	p.ifOwner = d.boolean("if_owner")
	p.level = d.integer("level")
	return p, d.err
}

func decodeRawField(path string, v any) (*rawField, string) {
	d := newDecoder(path, v, fieldKeys...)
	f := &rawField{}
	f.name = d.reqString("name")
	f.label = d.optString("label")
	f.fieldType = d.reqString("type")
	f.required = d.boolean("required")
	f.unique = d.boolean("unique")
	f.allowOnSubmit = d.boolean("allow_on_submit")
	f.readOnly = d.boolean("read_only")
	f.permLevel = d.integer("perm_level")
	if n := d.optUint("max_length", 32, "u32"); n != nil {
		u := uint32(*n)
		f.maxLength = &u
	}
	if n := d.optUint("precision", 8, "u8"); n != nil {
		u := uint8(*n)
		f.precision = &u
	}
	if n := d.optUint("scale", 8, "u8"); n != nil {
		u := uint8(*n)
		f.scale = &u
	}
	f.options, f.hasOptions = d.stringList("options", true)
	f.target = d.optString("target")
	return f, d.err
}

// ----- conversion into the validated model ------------------------------------------------------

func convertWorkflow(raw *rawWorkflow, errs *[]DefinitionError) *Workflow {
	w := &Workflow{}
	for _, state := range raw.states {
		docstatus := Draft
		if n := state.docstatus; n >= math.MinInt16 && n <= math.MaxInt16 {
			if ds, ok := DocStatusFromInt16(int16(n)); ok {
				docstatus = ds
			} else {
				*errs = append(*errs, DefinitionError{Kind: ErrWorkflowInvalidDocStatus, State: state.name, Value: n})
			}
		} else {
			*errs = append(*errs, DefinitionError{Kind: ErrWorkflowInvalidDocStatus, State: state.name, Value: n})
		}
		style := StyleNeutral
		if state.style != nil {
			if s, ok := StateStyleFromName(*state.style); ok {
				style = s
			} else {
				*errs = append(*errs, DefinitionError{Kind: ErrWorkflowUnknownStyle, State: state.name, Found: *state.style})
			}
		}
		editRoles := state.editRoles
		if editRoles == nil {
			editRoles = []string{}
		}
		w.States = append(w.States, WorkflowState{Name: state.name, DocStatus: docstatus, Style: style, EditRoles: editRoles})
	}
	for _, t := range raw.transitions {
		roles := t.roles
		if roles == nil {
			roles = []string{}
		}
		w.Transitions = append(w.Transitions, Transition{Action: t.action, From: t.from, To: t.to, Roles: roles})
	}
	if w.States == nil {
		w.States = []WorkflowState{}
	}
	if w.Transitions == nil {
		w.Transitions = []Transition{}
	}
	return w
}

// convertPermission converts a rule. A level outside 0 to 9 is reported and replaced by 0, so the
// other checks still run without a second, derived error.
func convertPermission(raw rawPermission, errs *[]DefinitionError) PermissionRule {
	level := uint8(0)
	if raw.level >= 0 && raw.level <= int64(MaxPermLevel) {
		level = uint8(raw.level)
	} else {
		*errs = append(*errs, DefinitionError{Kind: ErrInvalidRuleLevel, Role: raw.role, Value: raw.level})
	}
	return PermissionRule{
		Role: raw.role, Read: raw.read, Write: raw.write, Create: raw.create, Delete: raw.deleteRight,
		Submit: raw.submit, Cancel: raw.cancel, Amend: raw.amend, Export: raw.export,
		Import: raw.importRight, IfOwner: raw.ifOwner, Level: level,
	}
}

func convertNaming(raw any, present bool, errs *[]DefinitionError) Naming {
	if !present {
		return Naming{}
	}
	switch v := raw.(type) {
	case string:
		if v == "random" {
			return Naming{}
		}
		*errs = append(*errs, DefinitionError{Kind: ErrUnknownNamingStrategy, Found: v})
		return Naming{}
	case map[string]any:
		if len(v) == 1 {
			if field, ok := v["field"].(string); ok {
				return Naming{Kind: NamingField, Field: field}
			}
			if pattern, ok := v["series"].(string); ok {
				parsed, err := ParseSeriesPattern(pattern)
				if err != nil {
					*errs = append(*errs, DefinitionError{Kind: ErrInvalidSeriesPattern, Pattern: pattern, Reason: err})
					return Naming{}
				}
				return Naming{Kind: NamingSeries, Series: parsed}
			}
		}
	}
	*errs = append(*errs, DefinitionError{Kind: ErrMalformedNaming})
	return Naming{}
}

func parseTOMLRoot(text string) (map[string]any, string) {
	var root map[string]any
	if _, err := toml.Decode(text, &root); err != nil {
		return nil, err.Error()
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, ""
}

func fromTOMLString(text string) (*DocType, error) {
	root, msg := parseTOMLRoot(text)
	if msg != "" {
		return nil, &MetaError{Kind: MetaParse, Message: msg}
	}
	raw, msg := decodeDocType(root)
	if msg != "" {
		return nil, &MetaError{Kind: MetaParse, Message: msg}
	}
	var errs []DefinitionError
	var failed []string

	// A child has no naming rule at all; convertNaming would accept naming = "random".
	var naming Naming
	if raw.isChild && raw.hasNaming {
		errs = append(errs, DefinitionError{Kind: ErrChildWithNaming})
	} else {
		naming = convertNaming(raw.naming, raw.hasNaming, &errs)
	}

	fields := make([]DocField, 0, len(raw.fields))
	for _, f := range raw.fields {
		field, ferrs := convertField(f)
		if ferrs != nil {
			errs = append(errs, ferrs...)
			failed = append(failed, f.name)
			continue
		}
		fields = append(fields, field)
	}

	doctype := &DocType{
		Name: raw.name, Label: raw.label, Naming: naming, Fields: fields,
		IsChild: raw.isChild, Submittable: raw.submittable,
		TitleField: raw.titleField, Icon: raw.icon,
	}
	if raw.hasList {
		doctype.ListFields = raw.listFields
	}
	for _, p := range raw.permissions {
		doctype.Permissions = append(doctype.Permissions, convertPermission(p, &errs))
	}
	if raw.workflow != nil {
		doctype.Workflow = convertWorkflow(raw.workflow, &errs)
	}
	errs = append(errs, doctype.check(failed)...)
	if len(errs) > 0 {
		return nil, &MetaError{Kind: MetaInvalid, Problems: errs}
	}
	return doctype, nil
}

// convertField converts one raw field into the validated model: the type and its parameters. The
// rules that need the whole DocType (duplicates, names, allow_on_submit) are checked by
// DocType.check. It returns the problems (non-nil) when the field cannot be converted.
func convertField(f rawField) (DocField, []DefinitionError) {
	var levelErr *DefinitionError
	level := uint8(0)
	if f.permLevel >= 0 && f.permLevel <= int64(MaxPermLevel) {
		level = uint8(f.permLevel)
	} else {
		levelErr = &DefinitionError{Kind: ErrInvalidPermLevel, Field: f.name, Value: f.permLevel}
	}
	fieldType, terrs := convertType(f)
	if terrs != nil || levelErr != nil {
		if levelErr != nil {
			terrs = append(terrs, *levelErr)
		}
		return DocField{}, terrs
	}
	label := Humanize(f.name)
	if f.label != nil {
		label = *f.label
	}
	return DocField{
		Name: f.name, Label: label, Type: fieldType, Required: f.required, Unique: f.unique,
		AllowOnSubmit: f.allowOnSubmit, ReadOnly: f.readOnly, PermLevel: level,
	}, nil
}

func convertType(f rawField) (FieldType, []DefinitionError) {
	name := f.fieldType
	var fieldType *FieldType
	var allowed []string
	switch name {
	case "data":
		max := DefaultMaxLength
		if f.maxLength != nil {
			max = *f.maxLength
		}
		fieldType = &FieldType{Kind: FieldData, MaxLength: max}
		allowed = []string{"max_length"}
	case "text":
		fieldType = &FieldType{Kind: FieldText}
	case "int":
		fieldType = &FieldType{Kind: FieldInt}
	case "check":
		fieldType = &FieldType{Kind: FieldCheck}
	case "date":
		fieldType = &FieldType{Kind: FieldDate}
	case "datetime":
		fieldType = &FieldType{Kind: FieldDatetime}
	case "decimal":
		allowed = []string{"precision", "scale"}
	case "select":
		allowed = []string{"options"}
	case "link", "table":
		allowed = []string{"target"}
	default:
		return FieldType{}, []DefinitionError{{Kind: ErrUnknownFieldType, Field: f.name, Found: f.fieldType}}
	}

	var errs []DefinitionError
	for _, p := range []struct {
		param   string
		present bool
	}{
		{"max_length", f.maxLength != nil},
		{"precision", f.precision != nil},
		{"scale", f.scale != nil},
		{"options", f.hasOptions},
		{"target", f.target != nil},
	} {
		if p.present && !contains(allowed, p.param) {
			errs = append(errs, DefinitionError{Kind: ErrUnexpectedParam, Field: f.name, FieldType: f.fieldType, Param: p.param})
		}
	}

	missing := func(param string) {
		errs = append(errs, DefinitionError{Kind: ErrMissingParam, Field: f.name, FieldType: name, Param: param})
	}
	if fieldType == nil {
		switch name {
		case "decimal":
			if f.precision != nil && f.scale != nil {
				fieldType = &FieldType{Kind: FieldDecimal, Precision: *f.precision, Scale: *f.scale}
			} else {
				if f.precision == nil {
					missing("precision")
				}
				if f.scale == nil {
					missing("scale")
				}
			}
		case "select":
			if f.hasOptions {
				fieldType = &FieldType{Kind: FieldSelect, Options: append([]string{}, f.options...)}
			} else {
				missing("options")
			}
		case "link":
			if f.target != nil {
				fieldType = &FieldType{Kind: FieldLink, Target: *f.target}
			} else {
				missing("target")
			}
		case "table":
			if f.target != nil {
				fieldType = &FieldType{Kind: FieldTable, Target: *f.target}
			} else {
				missing("target")
			}
		}
	}
	if fieldType != nil && len(errs) == 0 {
		return *fieldType, nil
	}
	if errs == nil {
		errs = []DefinitionError{}
	}
	return FieldType{}, errs
}

// ----- overlays and single fields ---------------------------------------------------------------

// converted converts a raw field of an overlay or a custom field. The name is checked here too: the
// DocType check that would do it never sees a field that did not convert.
func converted(raw rawField) (DocField, error) {
	field, errs := convertField(raw)
	if errs != nil {
		if err := ValidateFieldName(raw.name); err != nil {
			errs = append(errs, DefinitionError{Kind: ErrInvalidFieldName, Field: raw.name, Reason: err})
		}
		return DocField{}, OverlayError{Kind: OverlayInvalid, Problems: errs}
	}
	return field, nil
}

func overlayFromTOMLString(text string) ([]DocField, error) {
	root, msg := parseTOMLRoot(text)
	if msg != "" {
		return nil, OverlayError{Kind: OverlayParse, Message: msg}
	}
	d := newDecoder("", root, "fields")
	var raws []rawField
	for i, item := range d.tables("fields") {
		f, m := decodeRawField(fmt.Sprintf("fields[%d]", i), item)
		if m != "" {
			d.fail("%s", m)
			break
		}
		raws = append(raws, *f)
	}
	if d.err != "" {
		return nil, OverlayError{Kind: OverlayParse, Message: d.err}
	}
	if len(raws) == 0 {
		return nil, OverlayError{Kind: OverlayNoFields}
	}
	fields := make([]DocField, 0, len(raws))
	for _, raw := range raws {
		field, err := converted(raw)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func fieldFromTOMLString(text string) (DocField, error) {
	root, msg := parseTOMLRoot(text)
	if msg != "" {
		return DocField{}, OverlayError{Kind: OverlayParse, Message: msg}
	}
	raw, m := decodeRawField("", root)
	if m != "" {
		return DocField{}, OverlayError{Kind: OverlayParse, Message: m}
	}
	return converted(*raw)
}

// normalizeJSON turns the numbers of a decoded JSON tree into int64 when they are integers (a
// json.Number without a fraction or exponent, any Go integer) and float64 otherwise (a float64, or
// a json.Number such as 5.0 or 1e2), so that the decoder sees what a TOML tree has and refuses a
// float where serde_json refuses it.
func normalizeJSON(v any) any {
	switch x := v.(type) {
	case json.Number:
		// serde_json reads a number with a fraction or an exponent (5.0, 1e2) as a float.
		// JSON -0 is a float too (serde_json: "convert into a float on -0").
		if !strings.ContainsAny(string(x), ".eE") && string(x) != "-0" {
			if n, err := x.Int64(); err == nil {
				return n
			}
		}
		f, _ := x.Float64()
		return f
	case float64:
		return x // never converted to an integer; -0 stays a float
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case uint8:
		return int64(x)
	case uint32:
		return int64(x)
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, item := range x {
			m[k] = normalizeJSON(item)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = normalizeJSON(item)
		}
		return out
	}
	return v
}

func fieldFromJSON(value any) (DocField, error) {
	raw, m := decodeRawField("", normalizeJSON(value))
	if m != "" {
		return DocField{}, OverlayError{Kind: OverlayParse, Message: m}
	}
	return converted(*raw)
}
