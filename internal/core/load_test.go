package core

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

const customerTOML = `
name = "customer"
label = "Customer"
naming = { field = "email" }

[[fields]]
name = "email"
type = "data"
max_length = 140
required = true
unique = true

[[fields]]
name = "status"
label = "Account status"
type = "select"
options = ["active", "closed"]

[[fields]]
name = "credit_limit"
type = "decimal"
precision = 12
scale = 2
`

func docWithField(body string) string {
	return "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\n" + body + "\n"
}

func one(e DefinitionError) []DefinitionError { return []DefinitionError{e} }

// Rust: load.rs::parses_valid_definition
func TestParsesValidDefinition(t *testing.T) {
	d := mustDocType(t, customerTOML)
	if d.Name != "customer" || d.Label != "Customer" {
		t.Errorf("name/label: %q %q", d.Name, d.Label)
	}
	sameValue(t, d.Naming, Naming{Kind: NamingField, Field: "email"}, "naming")
	if len(d.Fields) != 3 {
		t.Fatalf("fields = %d", len(d.Fields))
	}
	if d.Fields[0].Label != "Email" || d.Fields[1].Label != "Account status" || d.Fields[2].Label != "Credit Limit" {
		t.Errorf("labels: %q %q %q", d.Fields[0].Label, d.Fields[1].Label, d.Fields[2].Label)
	}
	sameValue(t, d.Fields[2].Type, FieldType{Kind: FieldDecimal, Precision: 12, Scale: 2}, "decimal type")
	if !d.Fields[0].Required || !d.Fields[0].Unique {
		t.Error("email flags")
	}
}

// Rust: load.rs::naming_defaults_and_random
func TestNamingDefaultsAndRandom(t *testing.T) {
	base := "name = \"t\"\nlabel = \"T\"\n"
	for _, text := range []string{base, "naming = \"random\"\n" + base} {
		if got := mustDocType(t, text).Naming; got.Kind != NamingRandom {
			t.Errorf("naming = %+v", got)
		}
	}
	sameValue(t, invalidOf(t, "naming = \"series\"\n"+base), one(DefinitionError{Kind: ErrUnknownNamingStrategy, Found: "series"}), "series string")
}

// Rust: load.rs::data_default_max_length
func TestDataDefaultMaxLength(t *testing.T) {
	d := mustDocType(t, docWithField("type = \"data\""))
	sameValue(t, d.Fields[0].Type, FieldType{Kind: FieldData, MaxLength: 140}, "default max_length")
}

// Rust: load.rs::simple_types
func TestSimpleTypes(t *testing.T) {
	for _, typ := range []string{"text", "int", "check", "date", "datetime"} {
		d := mustDocType(t, docWithField(fmt.Sprintf("type = \"%s\"", typ)))
		if d.Fields[0].Type.Name() != typ {
			t.Errorf("type %s: %s", typ, d.Fields[0].Type.Name())
		}
	}
}

// Rust: load.rs::parses_link_fields
func TestParsesLinkFields(t *testing.T) {
	d := mustDocType(t, docWithField("type = \"link\"\ntarget = \"customer\""))
	sameValue(t, d.Fields[0].Type, FieldType{Kind: FieldLink, Target: "customer"}, "link type")
	if d.Fields[0].Type.Name() != "link" {
		t.Error("name")
	}
}

// Rust: load.rs::link_parameter_errors
func TestLinkParameterErrors(t *testing.T) {
	sameValue(t, invalidOf(t, docWithField("type = \"link\"")),
		one(DefinitionError{Kind: ErrMissingParam, Field: "f", FieldType: "link", Param: "target"}), "missing target")
	sameValue(t, invalidOf(t, docWithField("type = \"int\"\ntarget = \"customer\"")),
		one(DefinitionError{Kind: ErrUnexpectedParam, Field: "f", FieldType: "int", Param: "target"}), "target on int")
	sameValue(t, invalidOf(t, docWithField("type = \"link\"\ntarget = \"customer\"\nmax_length = 5")),
		one(DefinitionError{Kind: ErrUnexpectedParam, Field: "f", FieldType: "link", Param: "max_length"}), "max_length on link")
	errs := invalidOf(t, docWithField("type = \"link\"\ntarget = \"Bad Target\""))
	if len(errs) != 1 || errs[0].Kind != ErrInvalidLinkTarget || errs[0].Target != "Bad Target" {
		t.Errorf("bad target: %v", errs)
	}
}

// Rust: load.rs::parses_table_fields_and_child_doctypes
func TestParsesTableFieldsAndChildDoctypes(t *testing.T) {
	d := mustDocType(t, docWithField("type = \"table\"\ntarget = \"row\""))
	sameValue(t, d.Fields[0].Type, FieldType{Kind: FieldTable, Target: "row"}, "table type")
	if d.Fields[0].Type.Name() != "table" || d.IsChild {
		t.Error("table field / is_child")
	}
	child := "name = \"row\"\nlabel = \"R\"\nis_child = true\n[[fields]]\nname = \"qty\"\ntype = \"int\"\n"
	d = mustDocType(t, child)
	if !d.IsChild || d.Naming.Kind != NamingRandom {
		t.Errorf("child: %+v", d)
	}
}

// Rust: load.rs::table_parameter_errors
func TestTableParameterErrors(t *testing.T) {
	sameValue(t, invalidOf(t, docWithField("type = \"table\"")),
		one(DefinitionError{Kind: ErrMissingParam, Field: "f", FieldType: "table", Param: "target"}), "missing")
	sameValue(t, invalidOf(t, docWithField("type = \"table\"\ntarget = \"row\"\nmax_length = 5")),
		one(DefinitionError{Kind: ErrUnexpectedParam, Field: "f", FieldType: "table", Param: "max_length"}), "unexpected")
	sameValue(t, invalidOf(t, docWithField("type = \"table\"\ntarget = \"row\"\nunique = true")),
		one(DefinitionError{Kind: ErrUniqueTable, Field: "f"}), "unique")
	// required (at least one row) is fine.
	mustDocType(t, docWithField("type = \"table\"\ntarget = \"row\"\nrequired = true"))
}

// Rust: load.rs::child_doctype_rules_in_toml
func TestChildDoctypeRulesInToml(t *testing.T) {
	base := "name = \"row\"\nlabel = \"R\"\nis_child = true\n"
	// Any naming key is refused, also the default one.
	for _, naming := range []string{"\"random\"", "{ field = \"code\" }", "\"bogus\""} {
		sameValue(t, invalidOf(t, "naming = "+naming+"\n"+base), one(DefinitionError{Kind: ErrChildWithNaming}), naming)
	}
	nested := base + "[[fields]]\nname = \"more\"\ntype = \"table\"\ntarget = \"x\"\n"
	sameValue(t, invalidOf(t, nested), one(DefinitionError{Kind: ErrChildWithTable, Field: "more"}), "nested")
	unique := base + "[[fields]]\nname = \"code\"\ntype = \"data\"\nunique = true\n"
	sameValue(t, invalidOf(t, unique), one(DefinitionError{Kind: ErrChildWithUnique, Field: "code"}), "unique")
	// A wrong value kind is a parse error.
	_, err := ParseDocType("name = \"t\"\nlabel = \"T\"\nis_child = \"yes\"\n")
	if !isParseError(err) {
		t.Errorf("is_child = \"yes\": %v", err)
	}
}

// Rust: load.rs::parse_errors
func TestParseErrors(t *testing.T) {
	cases := []struct{ what, text string }{
		{"unknown top-level key", "name = \"t\"\nlabel = \"T\"\nextra = 1\n"},
		{"unknown field key", "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\ntype = \"int\"\nextra = 1\n"},
		{"missing name", "label = \"T\"\n"},
		{"missing label", "name = \"t\"\n"},
		{"missing field type", "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\n"},
		{"wrong value kind", "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\ntype = 5\n"},
		{"negative max_length", "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\ntype = \"data\"\nmax_length = -1\n"},
		{"not toml", "this is not toml"},
		// Go-specific: serde matches keys exactly; so does this loader (no case folding).
		{"key in the wrong case", "Name = \"t\"\nlabel = \"T\"\n"},
		{"duplicate key", "name = \"t\"\nname = \"u\"\nlabel = \"T\"\n"},
		{"precision above u8", "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\ntype = \"decimal\"\nprecision = 300\nscale = 2\n"},
		{"max_length above u32", "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\ntype = \"data\"\nmax_length = 4294967296\n"},
		{"float for an integer", "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\ntype = \"data\"\nmax_length = 5.0\n"},
		{"fields not a list", "name = \"t\"\nlabel = \"T\"\nfields = 5\n"},
		{"options with a number", "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\ntype = \"select\"\noptions = [\"a\", 1]\n"},
	}
	for _, c := range cases {
		_, err := ParseDocType(c.text)
		if !isParseError(err) {
			t.Errorf("%s: got %v", c.what, err)
		}
	}
	// The message names the key.
	_, err := ParseDocType("name = \"t\"\nlabel = \"T\"\nextra = 1\n")
	if !strings.Contains(err.Error(), "`extra`") || !strings.HasPrefix(err.Error(), "cannot parse doctype definition: ") {
		t.Errorf("message: %v", err)
	}
	_, err = ParseDocType("name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\ntype = \"int\"\nextra = 1\n")
	if !strings.Contains(err.Error(), "fields[0]") || !strings.Contains(err.Error(), "`extra`") {
		t.Errorf("nested message: %v", err)
	}
	_, err = ParseDocType("label = \"T\"\n")
	if !strings.Contains(err.Error(), "missing field `name`") {
		t.Errorf("missing message: %v", err)
	}
}

// Rust: load.rs::field_labels_default_to_the_humanized_name
func TestFieldLabelsDefaultToTheHumanizedName(t *testing.T) {
	d := mustDocType(t, "name = \"sales_order\"\nlabel = \"Sales Order\"\n[[fields]]\nname = \"order_date\"\ntype = \"date\"\n"+
		"[[fields]]\nname = \"qty\"\ntype = \"int\"\nlabel = \"Quantity\"\n")
	if d.Label != "Sales Order" || d.Fields[0].Label != "Order Date" || d.Fields[1].Label != "Quantity" {
		t.Errorf("labels: %q %q %q", d.Label, d.Fields[0].Label, d.Fields[1].Label)
	}
}

// Rust: load.rs::read_only_rules
func TestReadOnlyRules(t *testing.T) {
	if d := mustDocType(t, docWithField("type = \"int\"\nread_only = true")); !d.Fields[0].ReadOnly {
		t.Error("read_only not set")
	}
	if d := mustDocType(t, docWithField("type = \"int\"")); d.Fields[0].ReadOnly {
		t.Error("read_only set by default")
	}
	sameValue(t, invalidOf(t, docWithField("type = \"int\"\nread_only = true\nrequired = true")),
		one(DefinitionError{Kind: ErrReadOnlyRequired, Field: "f"}), "read_only required")
	sameValue(t, invalidOf(t, docWithField("type = \"table\"\ntarget = \"row\"\nread_only = true")),
		one(DefinitionError{Kind: ErrReadOnlyTable, Field: "f"}), "read_only table")
}

// Rust: load.rs::title_field_and_list_fields
func TestTitleFieldAndListFields(t *testing.T) {
	base := "name = \"t\"\nlabel = \"T\"\n"
	fields := "[[fields]]\nname = \"a\"\ntype = \"data\"\n[[fields]]\nname = \"b\"\ntype = \"check\"\n" +
		"[[fields]]\nname = \"c\"\ntype = \"text\"\n[[fields]]\nname = \"rows\"\ntype = \"table\"\ntarget = \"row\"\n"
	plain := mustDocType(t, base+fields)
	if plain.TitleField != nil || plain.ListFields != nil {
		t.Errorf("plain: %v %v", plain.TitleField, plain.ListFields)
	}
	d := mustDocType(t, base+"title_field = \"a\"\nlist_fields = [\"a\", \"b\"]\n"+fields)
	if d.TitleField == nil || *d.TitleField != "a" || !slices.Equal(d.ListFields, []string{"a", "b"}) {
		t.Errorf("title/list: %v %v", d.TitleField, d.ListFields)
	}

	title := func(value string) []DefinitionError {
		return invalidOf(t, base+"title_field = \""+value+"\"\n"+fields)
	}
	sameValue(t, title("nope"), one(DefinitionError{Kind: ErrTitleFieldMissing, Field: "nope"}), "title missing")
	for _, c := range [][2]string{{"b", "check"}, {"c", "text"}, {"rows", "table"}} {
		sameValue(t, title(c[0]), one(DefinitionError{Kind: ErrTitleFieldKind, Field: c[0], Found: c[1]}), "title "+c[0])
	}
	list := func(value string) []DefinitionError {
		return invalidOf(t, base+"list_fields = "+value+"\n"+fields)
	}
	sameValue(t, list("[\"nope\"]"), one(DefinitionError{Kind: ErrListFieldMissing, Field: "nope"}), "list missing")
	sameValue(t, list("[\"c\", \"rows\"]"), []DefinitionError{
		{Kind: ErrListFieldKind, Field: "c", Found: "text"},
		{Kind: ErrListFieldKind, Field: "rows", Found: "table"},
	}, "list kinds")
	sameValue(t, list("[\"a\", \"a\"]"), one(DefinitionError{Kind: ErrDuplicateListField, Field: "a"}), "list duplicate")
	sameValue(t, list("[]"), one(DefinitionError{Kind: ErrEmptyListFields}), "list empty")
	many := list("[\"a\", \"a\", \"a\", \"a\", \"a\", \"a\", \"a\", \"a\", \"a\"]")
	sameValue(t, many[0], DefinitionError{Kind: ErrTooManyListFields, Count: 9, Max: 8}, "too many")
	// Exactly the limit is fine.
	var names []string
	text := base
	for i := 1; i <= 8; i++ {
		names = append(names, fmt.Sprintf("%q", fmt.Sprintf("f%d", i)))
	}
	text += "list_fields = [" + strings.Join(names, ", ") + "]\n"
	for i := 1; i <= 8; i++ {
		text += fmt.Sprintf("[[fields]]\nname = \"f%d\"\ntype = \"int\"\n", i)
	}
	if n := len(mustDocType(t, text).ListFields); n != 8 {
		t.Errorf("list_fields = %d", n)
	}
}

// Rust: load.rs::icon_is_optional_and_checked_against_the_list
func TestIconIsOptionalAndCheckedAgainstTheList(t *testing.T) {
	base := "name = \"t\"\nlabel = \"T\"\n"
	if mustDocType(t, base).Icon != nil {
		t.Error("icon set by default")
	}
	d := mustDocType(t, base+"icon = \"file-text\"\n")
	if d.Icon == nil || *d.Icon != "file-text" {
		t.Errorf("icon = %v", d.Icon)
	}
	errs := invalidOf(t, base+"icon = \"rocket\"\n")
	if len(errs) != 1 {
		t.Fatalf("errors: %v", errs)
	}
	message := errs[0].Error()
	if !strings.Contains(message, "`rocket`") {
		t.Errorf("message %q", message)
	}
	// The message lists the allowed names.
	if !strings.Contains(message, "file-text, users, user") {
		t.Errorf("message %q", message)
	}
	// Every name in the list is accepted, and the list has no duplicates.
	names := DocTypeIcons()
	if len(names) < 16 {
		t.Errorf("only %d icons", len(names))
	}
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	if len(slices.Compact(sorted)) != len(names) {
		t.Error("duplicate icons")
	}
	for _, name := range names {
		mustDocType(t, base+"icon = \""+name+"\"\n")
	}
}

// Rust: load.rs::icon_is_refused_on_a_child
func TestIconIsRefusedOnAChild(t *testing.T) {
	child := "name = \"row\"\nlabel = \"R\"\nis_child = true\nicon = \"box\"\n"
	sameValue(t, invalidOf(t, child), one(DefinitionError{Kind: ErrChildWithDeskKey, Key: "icon"}), "icon on child")
}

// Rust: load.rs::title_field_and_list_fields_are_refused_on_a_child
func TestTitleFieldAndListFieldsAreRefusedOnAChild(t *testing.T) {
	child := "name = \"row\"\nlabel = \"R\"\nis_child = true\ntitle_field = \"a\"\nlist_fields = [\"a\"]\n" +
		"[[fields]]\nname = \"a\"\ntype = \"data\"\n"
	sameValue(t, invalidOf(t, child), []DefinitionError{
		{Kind: ErrChildWithDeskKey, Key: "title_field"},
		{Kind: ErrChildWithDeskKey, Key: "list_fields"},
	}, "desk keys on child")
}

// Rust: load.rs::type_errors
func TestTypeErrors(t *testing.T) {
	cases := []struct {
		body string
		want DefinitionError
	}{
		{"type = \"float\"", DefinitionError{Kind: ErrUnknownFieldType, Field: "f", Found: "float"}},
		{"type = \"decimal\"\nscale = 2", DefinitionError{Kind: ErrMissingParam, Field: "f", FieldType: "decimal", Param: "precision"}},
		{"type = \"decimal\"\nprecision = 5", DefinitionError{Kind: ErrMissingParam, Field: "f", FieldType: "decimal", Param: "scale"}},
		{"type = \"select\"", DefinitionError{Kind: ErrMissingParam, Field: "f", FieldType: "select", Param: "options"}},
		{"type = \"int\"\nmax_length = 5", DefinitionError{Kind: ErrUnexpectedParam, Field: "f", FieldType: "int", Param: "max_length"}},
		{"type = \"data\"\noptions = [\"a\"]", DefinitionError{Kind: ErrUnexpectedParam, Field: "f", FieldType: "data", Param: "options"}},
		{"type = \"data\"\nmax_length = 0", DefinitionError{Kind: ErrMaxLengthOutOfRange, Field: "f", Value: 0}},
		{"type = \"select\"\noptions = []", DefinitionError{Kind: ErrEmptyOptions, Field: "f"}},
		{"type = \"decimal\"\nprecision = 2\nscale = 3", DefinitionError{Kind: ErrScaleExceedsPrecision, Field: "f", Scale: 3, Precision: 2}},
	}
	for _, c := range cases {
		sameValue(t, invalidOf(t, docWithField(c.body)), one(c.want), c.body)
	}
}

// Rust: load.rs::collects_all_errors_across_fields
func TestCollectsAllErrorsAcrossFields(t *testing.T) {
	text := `
name = "Bad Name"
label = "T"
naming = { field = "gone" }

[[fields]]
name = "owner"
type = "int"

[[fields]]
name = "a"
type = "nope"

[[fields]]
name = "gone"
type = "date"
`
	errs := invalidOf(t, text)
	// Bad doctype name, reserved field name, unknown type, naming field not data/required/unique.
	if len(errs) != 6 {
		t.Errorf("%d errors: %v", len(errs), errs)
	}
	if !hasKind(errs, ErrUnknownFieldType) || !hasKind(errs, ErrInvalidFieldName) {
		t.Errorf("errors: %v", errs)
	}
}

// Rust: load.rs::failed_naming_field_is_not_reported_twice
func TestFailedNamingFieldIsNotReportedTwice(t *testing.T) {
	text := "name = \"t\"\nlabel = \"T\"\nnaming = { field = \"f\" }\n[[fields]]\nname = \"f\"\ntype = \"nope\"\n"
	sameValue(t, invalidOf(t, text), one(DefinitionError{Kind: ErrUnknownFieldType, Field: "f", Found: "nope"}), "failed naming field")
}

// Rust: load.rs::naming_shape_errors
func TestNamingShapeErrors(t *testing.T) {
	base := "name = \"t\"\nlabel = \"T\"\n"
	for _, naming := range []string{
		"5",
		"{ fieldd = \"a\" }",
		"{ field = 1 }",
		"{ series = 1 }",
		"{ series = \"A.#\", field = \"a\" }",
		"{ field = \"a\", x = 1 }",
		"[\"a\"]",
	} {
		errs := invalidOf(t, "naming = "+naming+"\n"+base)
		sameValue(t, errs, one(DefinitionError{Kind: ErrMalformedNaming}), naming)
		if got, want := errs[0].Error(), "naming must be \"random\", { field = \"<fieldname>\" } or { series = \"<pattern>\" }"; got != want {
			t.Errorf("message %q", got)
		}
	}
}

// Rust: load.rs::failed_fields_still_get_name_and_duplicate_checks
func TestFailedFieldsStillGetNameAndDuplicateChecks(t *testing.T) {
	text := "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"Bad Name\"\ntype = \"nope\"\n"
	errs := invalidOf(t, text)
	if len(errs) != 2 || errs[0].Kind != ErrUnknownFieldType || errs[1].Kind != ErrInvalidFieldName {
		t.Errorf("errors: %v", errs)
	}
	text = "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\ntype = \"int\"\n[[fields]]\nname = \"f\"\ntype = \"nope\"\n"
	errs = invalidOf(t, text)
	if !hasErr(errs, DefinitionError{Kind: ErrDuplicateField, Field: "f"}) || !hasKind(errs, ErrUnknownFieldType) {
		t.Errorf("errors: %v", errs)
	}
}

// Rust: load.rs::duplicate_field_names
func TestDuplicateFieldNames(t *testing.T) {
	text := "name = \"t\"\nlabel = \"T\"\n[[fields]]\nname = \"f\"\ntype = \"int\"\n[[fields]]\nname = \"f\"\ntype = \"int\"\n"
	sameValue(t, invalidOf(t, text), one(DefinitionError{Kind: ErrDuplicateField, Field: "f"}), "duplicate")
}

// Rust: load.rs::error_message_lists_problems
func TestErrorMessageListsProblems(t *testing.T) {
	_, err := ParseDocType(docWithField("type = \"float\""))
	if err == nil || !strings.Contains(err.Error(), "unknown type `float`") || !strings.HasPrefix(err.Error(), "invalid doctype definition: ") {
		t.Errorf("message: %v", err)
	}
}

// Rust: load.rs::series_naming_is_parsed_and_checked
func TestSeriesNamingIsParsedAndChecked(t *testing.T) {
	base := "name = \"t\"\nlabel = \"T\"\n"
	d := mustDocType(t, "naming = { series = \"INV-.YYYY.-.#####\" }\n"+base)
	if d.Naming.Kind != NamingSeries || d.Naming.Series.String() != "INV-.YYYY.-.#####" {
		t.Fatalf("naming: %+v", d.Naming)
	}
	for _, bad := range []string{"", "INV-", "A.#.#", "A..#", "A#.##"} {
		errs := invalidOf(t, "naming = { series = \""+bad+"\" }\n"+base)
		if len(errs) != 1 || errs[0].Kind != ErrInvalidSeriesPattern || errs[0].Pattern != bad {
			t.Errorf("%q: %v", bad, errs)
		}
	}
	message := invalidOf(t, "naming = { series = \"INV-\" }\n"+base)[0].Error()
	if want := "naming series pattern `INV-` needs exactly one number part such as `#####`, found none"; message != want {
		t.Errorf("message %q", message)
	}
}

// Rust: load.rs::a_child_cannot_use_a_series_or_be_submittable
func TestAChildCannotUseASeriesOrBeSubmittable(t *testing.T) {
	base := "name = \"t\"\nlabel = \"T\"\nis_child = true\n"
	sameValue(t, invalidOf(t, "naming = { series = \"A.#\" }\n"+base), one(DefinitionError{Kind: ErrChildWithNaming}), "series on child")
	sameValue(t, invalidOf(t, "submittable = true\n"+base), one(DefinitionError{Kind: ErrChildSubmittable}), "submittable child")
}

// Rust: load.rs::submittable_rules
func TestSubmittableRules(t *testing.T) {
	base := "name = \"t\"\nlabel = \"T\"\nsubmittable = true\n"
	if !mustDocType(t, base).Submittable {
		t.Error("submittable not set")
	}
	if mustDocType(t, "name = \"t\"\nlabel = \"T\"\n").Submittable {
		t.Error("submittable by default")
	}
	// Random naming and a series are fine, field naming is not.
	mustDocType(t, "naming = \"random\"\n"+base)
	mustDocType(t, "naming = { series = \"A.#\" }\n"+base)
	field := "[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n"
	sameValue(t, invalidOf(t, "naming = { field = \"code\" }\n"+base+field), one(DefinitionError{Kind: ErrSubmittableWithFieldNaming}), "field naming")
}

// Rust: load.rs::allow_on_submit_rules
func TestAllowOnSubmitRules(t *testing.T) {
	submittable := "name = \"t\"\nlabel = \"T\"\nsubmittable = true\n"
	plain := "name = \"t\"\nlabel = \"T\"\n"
	field := func(extra string) string { return "[[fields]]\nname = \"f\"\n" + extra + "\n" }
	ok := mustDocType(t, submittable+field("type = \"text\"\nallow_on_submit = true"))
	if !ok.Fields[0].AllowOnSubmit {
		t.Error("allow_on_submit not set")
	}
	// Default is false.
	if mustDocType(t, submittable+field("type = \"text\"")).Fields[0].AllowOnSubmit {
		t.Error("allow_on_submit by default")
	}
	// Not meaningful without submittable: a definition error, not ignored.
	sameValue(t, invalidOf(t, plain+field("type = \"text\"\nallow_on_submit = true")),
		one(DefinitionError{Kind: ErrAllowOnSubmitNotSubmittable, Field: "f"}), "not submittable")
	// allow_on_submit = false is fine anywhere.
	mustDocType(t, plain+field("type = \"text\"\nallow_on_submit = false"))
	// A table is always locked.
	sameValue(t, invalidOf(t, submittable+field("type = \"table\"\ntarget = \"row\"\nallow_on_submit = true")),
		one(DefinitionError{Kind: ErrAllowOnSubmitTable, Field: "f"}), "table")
}

// Rust: load.rs::docstatus_and_amended_from_are_reserved_field_names
func TestDocstatusAndAmendedFromAreReservedFieldNames(t *testing.T) {
	for _, name := range []string{"docstatus", "amended_from"} {
		errs := invalidOf(t, fmt.Sprintf("name = \"t\"\nlabel = \"T\"\nsubmittable = true\n[[fields]]\nname = \"%s\"\ntype = \"int\"\n", name))
		if len(errs) != 1 || errs[0].Kind != ErrInvalidFieldName || errs[0].Field != name {
			t.Errorf("%s: %v", name, errs)
		}
	}
}

const (
	plainDoc       = "name = \"t\"\nlabel = \"T\"\n"
	submittableDoc = "name = \"t\"\nlabel = \"T\"\nsubmittable = true\n"
)

func rule(body string) string { return "[[permissions]]\n" + body + "\n" }

// Rust: load.rs::export_and_import_are_rights_of_a_rule_and_false_by_default
func TestExportAndImportAreRightsOfARuleAndFalseByDefault(t *testing.T) {
	text := plainDoc +
		rule("role = \"Exporter\"\nread = true\nexport = true") +
		rule("role = \"Importer\"\ncreate = true\nimport = true\nif_owner = true")
	d := mustDocType(t, text)
	exporter := d.Permissions[0]
	if !(exporter.Read && exporter.Export && !exporter.Import && !exporter.Create) {
		t.Errorf("exporter: %+v", exporter)
	}
	importer := d.Permissions[1]
	if !(importer.Create && importer.Import && !importer.Export && importer.IfOwner) {
		t.Errorf("importer: %+v", importer)
	}
	// A rule that grants only export or only import is not an empty rule.
	mustDocType(t, plainDoc+rule("role = \"A\"\nexport = true"))
	mustDocType(t, plainDoc+rule("role = \"A\"\nimport = true"))
}

// Rust: load.rs::parses_permission_rules_with_false_defaults
func TestParsesPermissionRulesWithFalseDefaults(t *testing.T) {
	text := submittableDoc +
		rule("role = \"Sales User\"\nread = true\nwrite = true\ncreate = true\nsubmit = true") +
		rule("role = \"Auditor\"\nread = true\nif_owner = true")
	d := mustDocType(t, text)
	sameValue(t, d.Permissions, []PermissionRule{
		{Role: "Sales User", Read: true, Write: true, Create: true, Submit: true},
		{Role: "Auditor", Read: true, IfOwner: true},
	}, "rules")
	// No rules at all is valid: deny by default.
	if len(mustDocType(t, plainDoc).Permissions) != 0 {
		t.Error("rules by default")
	}
}

// Rust: load.rs::permission_rules_reject_unknown_keys_and_missing_roles
func TestPermissionRulesRejectUnknownKeysAndMissingRoles(t *testing.T) {
	for _, body := range []string{
		"role = \"A\"\nread = true\nexecute = true",
		"read = true",
		"role = \"A\"\nread = \"yes\"",
	} {
		_, err := ParseDocType(plainDoc + rule(body))
		if !isParseError(err) {
			t.Errorf("%s: %v", body, err)
		}
	}
}

// Rust: load.rs::permission_rule_definition_errors
func TestPermissionRuleDefinitionErrors(t *testing.T) {
	// Blank role, a rule that grants nothing.
	sameValue(t, invalidOf(t, plainDoc+rule("role = \"  \"")), []DefinitionError{
		{Kind: ErrInvalidPermissionRole, Role: "  ", Why: "must not be blank"},
		{Kind: ErrEmptyPermissionRule, Role: "  "},
	}, "blank role")
	errs := invalidOf(t, plainDoc+rule("role = \"a\\tb\"\nread = true"))
	if len(errs) != 1 || errs[0].Kind != ErrInvalidPermissionRole {
		t.Errorf("control char: %v", errs)
	}
	sameValue(t, invalidOf(t, plainDoc+rule("role = \"A\"")), one(DefinitionError{Kind: ErrEmptyPermissionRule, Role: "A"}), "empty rule")
	// submit, cancel and amend need a submittable DocType.
	sameValue(t, invalidOf(t, plainDoc+rule("role = \"A\"\nsubmit = true\ncancel = true\namend = true")), []DefinitionError{
		{Kind: ErrPermissionNeedsSubmittable, Role: "A", Right: RightSubmit},
		{Kind: ErrPermissionNeedsSubmittable, Role: "A", Right: RightCancel},
		{Kind: ErrPermissionNeedsSubmittable, Role: "A", Right: RightAmend},
	}, "needs submittable")
	mustDocType(t, submittableDoc+rule("role = \"A\"\nsubmit = true\ncancel = true\namend = true"))
	// Same role and if_owner twice; a different if_owner is fine.
	twice := plainDoc + rule("role = \"A\"\nread = true") + rule("role = \"A\"\nwrite = true")
	sameValue(t, invalidOf(t, twice), one(DefinitionError{Kind: ErrDuplicatePermissionRule, Role: "A"}), "twice")
	both := plainDoc + rule("role = \"A\"\nread = true") + rule("role = \"A\"\nwrite = true\nif_owner = true")
	mustDocType(t, both)
	// The Administrator role is allowed (pointless, but not wrong).
	mustDocType(t, plainDoc+rule("role = \"Administrator\"\nread = true"))
}

// Rust: load.rs::a_child_doctype_cannot_have_permission_rules
func TestAChildDoctypeCannotHavePermissionRules(t *testing.T) {
	child := "name = \"row\"\nlabel = \"R\"\nis_child = true\n"
	sameValue(t, invalidOf(t, child+rule("role = \"A\"\nread = true")), one(DefinitionError{Kind: ErrChildWithPermissions}), "child rules")
	mustDocType(t, child)
}

// ---- [workflow] (M8.1) ----

const orderDoc = "name = \"order\"\nlabel = \"O\"\nsubmittable = true\n"

// workflowTOML is a valid workflow over a submittable DocType: Draft -> Pending -> Approved ->
// Cancelled, with a way back from Pending to Draft.
const workflowTOML = `
[workflow]
[[workflow.states]]
name = "Draft"
docstatus = 0
[[workflow.states]]
name = "Pending"
docstatus = 0
style = "warning"
edit_roles = ["Manager"]
[[workflow.states]]
name = "Approved"
docstatus = 1
style = "success"
[[workflow.states]]
name = "Cancelled"
docstatus = 2
style = "danger"
[[workflow.transitions]]
action = "Send"
from = "Draft"
to = "Pending"
roles = ["Clerk"]
[[workflow.transitions]]
action = "Reject"
from = "Pending"
to = "Draft"
roles = ["Manager", "Clerk"]
[[workflow.transitions]]
action = "Approve"
from = "Pending"
to = "Approved"
roles = ["Manager"]
[[workflow.transitions]]
action = "Cancel"
from = "Approved"
to = "Cancelled"
roles = ["Manager"]
`

func withWorkflow(doctype, workflow string) string { return doctype + workflow }

// edited is the valid workflow with from replaced by to (it must occur).
func edited(t *testing.T, from, to string) string {
	t.Helper()
	if !strings.Contains(workflowTOML, from) {
		t.Fatalf("%q does not occur", from)
	}
	return withWorkflow(orderDoc, strings.Replace(workflowTOML, from, to, 1))
}

// Rust: load.rs::parses_a_valid_workflow
func TestParsesAValidWorkflow(t *testing.T) {
	doctype := mustDocType(t, withWorkflow(orderDoc, workflowTOML))
	workflow := doctype.Workflow
	if workflow == nil {
		t.Fatal("no workflow")
	}
	type st struct {
		name      string
		docstatus int16
		style     string
	}
	var states []st
	for _, s := range workflow.States {
		states = append(states, st{s.Name, s.DocStatus.Int16(), s.Style.Name()})
	}
	sameValue(t, states, []st{{"Draft", 0, "neutral"}, {"Pending", 0, "warning"}, {"Approved", 1, "success"}, {"Cancelled", 2, "danger"}}, "states")
	if !slices.Equal(workflow.States[1].EditRoles, []string{"Manager"}) || len(workflow.States[0].EditRoles) != 0 {
		t.Error("edit_roles")
	}
	if len(workflow.Transitions) != 4 {
		t.Errorf("transitions = %d", len(workflow.Transitions))
	}
	reject := workflow.Transitions[1]
	sameValue(t, reject, Transition{Action: "Reject", From: "Pending", To: "Draft", Roles: []string{"Manager", "Clerk"}}, "reject")
	// No [workflow] means none, and a non-submittable DocType may have a draft-only one.
	if mustDocType(t, orderDoc).Workflow != nil {
		t.Error("workflow by default")
	}
	drafts := `name = "t"
label = "T"
[workflow]
[[workflow.states]]
name = "Open"
docstatus = 0
[[workflow.states]]
name = "Closed"
docstatus = 0
[[workflow.transitions]]
action = "Close"
from = "Open"
to = "Closed"
roles = ["A"]
`
	if mustDocType(t, drafts).Workflow == nil {
		t.Error("drafts workflow")
	}
}

// Rust: load.rs::workflow_keys_are_strict
func TestWorkflowKeysAreStrict(t *testing.T) {
	cases := []struct{ what, text string }{
		{"state key", edited(t, "style = \"warning\"", "colour = \"red\"")},
		{"transition key", edited(t, "roles = [\"Clerk\"]", "roles = [\"Clerk\"]\nextra = 1")},
		{"workflow key", withWorkflow(orderDoc, "[workflow]\nverb = 1\n")},
		{"state without a name", withWorkflow(orderDoc, "[workflow]\n[[workflow.states]]\ndocstatus = 0\n")},
		{"state without a docstatus", withWorkflow(orderDoc, "[workflow]\n[[workflow.states]]\nname = \"A\"\n")},
		{"action of the wrong type", edited(t, "action = \"Send\"", "action = 5")},
	}
	for _, c := range cases {
		_, err := ParseDocType(c.text)
		if !isParseError(err) {
			t.Errorf("%s: %v", c.what, err)
		}
	}
}

// Rust: load.rs::a_workflow_on_a_child_doctype_is_refused
func TestAWorkflowOnAChildDoctypeIsRefused(t *testing.T) {
	child := "name = \"row\"\nlabel = \"R\"\nis_child = true\n"
	sameValue(t, invalidOf(t, withWorkflow(child, workflowTOML)), one(DefinitionError{Kind: ErrChildWithWorkflow}), "child workflow")
}

// Rust: load.rs::a_workflow_needs_states_and_a_draft_initial_state
func TestAWorkflowNeedsStatesAndADraftInitialState(t *testing.T) {
	sameValue(t, invalidOf(t, withWorkflow(orderDoc, "[workflow]\n")), one(DefinitionError{Kind: ErrWorkflowNoStates}), "no states")
	notDraft := "\n[workflow]\n[[workflow.states]]\nname = \"Done\"\ndocstatus = 1\n"
	sameValue(t, invalidOf(t, withWorkflow(orderDoc, notDraft)), one(DefinitionError{Kind: ErrWorkflowInitialNotDraft, State: "Done"}), "not draft")
}

// Rust: load.rs::state_names_are_checked
func TestStateNamesAreChecked(t *testing.T) {
	for _, c := range []struct{ name, reason string }{
		{"", "must not be empty"},
		{"   ", "must not be empty"},
		{" Cancelled", "must not start or end with whitespace"},
		{"Cancelled ", "must not start or end with whitespace"},
		{strings.Repeat("x", 65), "is longer than 64 characters"},
	} {
		text := edited(t, "name = \"Cancelled\"", fmt.Sprintf("name = \"%s\"", c.name))
		errs := invalidOf(t, text)
		if !hasErr(errs, DefinitionError{Kind: ErrWorkflowName, NameKind: "state", Name: c.name, Why: c.reason}) {
			t.Errorf("%q: %v", c.name, errs)
		}
	}
	// The limit is 64 characters, not bytes, and exactly 64 is fine.
	long := strings.Repeat("é", 64)
	ok := edited(t, "name = \"Cancelled\"", fmt.Sprintf("name = \"%s\"", long))
	ok = strings.Replace(ok, "to = \"Cancelled\"", fmt.Sprintf("to = \"%s\"", long), -1)
	mustDocType(t, ok)
	// Names are case-sensitive: draft and Draft are two states.
	ok = strings.Replace(edited(t, "name = \"Cancelled\"", "name = \"draft\""), "to = \"Cancelled\"", "to = \"draft\"", -1)
	mustDocType(t, ok)
}

// Rust: load.rs::a_state_name_is_unique
func TestAStateNameIsUnique(t *testing.T) {
	text := strings.Replace(edited(t, "name = \"Cancelled\"", "name = \"Pending\""), "to = \"Cancelled\"", "to = \"Pending\"", -1)
	errs := invalidOf(t, text)
	if !hasErr(errs, DefinitionError{Kind: ErrWorkflowDuplicateState, State: "Pending"}) {
		t.Errorf("errors: %v", errs)
	}
}

// Rust: load.rs::docstatus_rules_of_states
func TestDocstatusRulesOfStates(t *testing.T) {
	// Not 0, 1 or 2. The state is then a draft in the other checks, so a move into it may be
	// reported as well.
	errs := invalidOf(t, edited(t, "name = \"Cancelled\"\ndocstatus = 2", "name = \"Cancelled\"\ndocstatus = 3"))
	sameValue(t, errs[0], DefinitionError{Kind: ErrWorkflowInvalidDocStatus, State: "Cancelled", Value: 3}, "docstatus 3")
	// 1 and 2 need a submittable DocType.
	plain := withWorkflow("name = \"order\"\nlabel = \"O\"\n", workflowTOML)
	errs = invalidOf(t, plain)
	if !hasErr(errs, DefinitionError{Kind: ErrWorkflowStateNeedsSubmittable, State: "Approved", DocStatus: 1}) ||
		!hasErr(errs, DefinitionError{Kind: ErrWorkflowStateNeedsSubmittable, State: "Cancelled", DocStatus: 2}) {
		t.Errorf("errors: %v", errs)
	}
	// An unknown style.
	sameValue(t, invalidOf(t, edited(t, "style = \"warning\"", "style = \"red\"")),
		one(DefinitionError{Kind: ErrWorkflowUnknownStyle, State: "Pending", Found: "red"}), "style")
}

// Rust: load.rs::edit_roles_are_for_drafts_and_valid_role_names
func TestEditRolesAreForDraftsAndValidRoleNames(t *testing.T) {
	sameValue(t, invalidOf(t, edited(t, "style = \"success\"", "style = \"success\"\nedit_roles = [\"Manager\"]")),
		one(DefinitionError{Kind: ErrWorkflowEditRolesNotDraft, State: "Approved"}), "edit_roles on a submitted state")
	errs := invalidOf(t, edited(t, "edit_roles = [\"Manager\"]", "edit_roles = [\"Manager\", \" \"]"))
	sameValue(t, errs, one(DefinitionError{Kind: ErrWorkflowInvalidRole, Place: "state `Pending` edit_roles", Role: " ", Why: "must not be blank"}), "blank edit role")
}

// Rust: load.rs::transition_endpoints_must_be_states
func TestTransitionEndpointsMustBeStates(t *testing.T) {
	errs := invalidOf(t, edited(t, "from = \"Draft\"", "from = \"Nowhere\""))
	if !hasErr(errs, DefinitionError{Kind: ErrWorkflowUnknownState, Action: "Send", From: "Nowhere", Key: "from", State: "Nowhere"}) {
		t.Errorf("from: %v", errs)
	}
	errs = invalidOf(t, edited(t, "to = \"Pending\"", "to = \"Nowhere\""))
	if !hasErr(errs, DefinitionError{Kind: ErrWorkflowUnknownState, Action: "Send", From: "Draft", Key: "to", State: "Nowhere"}) {
		t.Errorf("to: %v", errs)
	}
}

// Rust: load.rs::actions_are_checked_and_unique_per_from_state
func TestActionsAreCheckedAndUniquePerFromState(t *testing.T) {
	for _, c := range []struct{ action, reason string }{
		{"", "must not be empty"},
		{" Send", "must not start or end with whitespace"},
		{"Send\\t", "must not start or end with whitespace"},
		{strings.Repeat("a", 65), "is longer than 64 characters"},
	} {
		errs := invalidOf(t, edited(t, "action = \"Send\"", fmt.Sprintf("action = \"%s\"", c.action)))
		want := c.action
		if want == "Send\\t" {
			want = "Send\t" // the TOML escape \t is a tab
		}
		sameValue(t, errs, one(DefinitionError{Kind: ErrWorkflowName, NameKind: "action", Name: want, Why: c.reason}), c.action)
	}
	// The same action from the same state twice is refused; from another state it is fine.
	twice := strings.Replace(workflowTOML, "action = \"Approve\"\nfrom = \"Pending\"", "action = \"Reject\"\nfrom = \"Pending\"", 1)
	sameValue(t, invalidOf(t, withWorkflow(orderDoc, twice)), one(DefinitionError{Kind: ErrWorkflowDuplicateAction, Action: "Reject", From: "Pending"}), "duplicate action")
	mustDocType(t, edited(t, "action = \"Cancel\"", "action = \"Send\""))
}

// Rust: load.rs::transition_roles_are_required_valid_and_unique
func TestTransitionRolesAreRequiredValidAndUnique(t *testing.T) {
	noRoles := one(DefinitionError{Kind: ErrWorkflowNoRoles, Action: "Send", From: "Draft"})
	sameValue(t, invalidOf(t, edited(t, "roles = [\"Clerk\"]", "roles = []")), noRoles, "empty roles")
	// Missing roles is the same as empty.
	sameValue(t, invalidOf(t, edited(t, "roles = [\"Clerk\"]\n", "")), noRoles, "missing roles")
	sameValue(t, invalidOf(t, edited(t, "roles = [\"Clerk\"]", "roles = [\"\"]")),
		one(DefinitionError{Kind: ErrWorkflowInvalidRole, Place: "transition `Send` from `Draft`", Role: "", Why: "must be 1 to 140 characters"}), "empty role")
	sameValue(t, invalidOf(t, edited(t, "roles = [\"Clerk\"]", "roles = [\"Clerk\", \"Clerk\"]")),
		one(DefinitionError{Kind: ErrWorkflowDuplicateRole, Action: "Send", From: "Draft", Role: "Clerk"}), "duplicate role")
}

// Rust: load.rs::a_transition_must_change_the_state_and_make_an_allowed_move
func TestATransitionMustChangeTheStateAndMakeAnAllowedMove(t *testing.T) {
	sameValue(t, invalidOf(t, edited(t, "to = \"Pending\"", "to = \"Draft\"")), []DefinitionError{
		{Kind: ErrWorkflowSelfTransition, Action: "Send", State: "Draft"},
		// Pending is now only reachable through... nothing.
		{Kind: ErrWorkflowUnreachableState, State: "Pending"},
		{Kind: ErrWorkflowUnreachableState, State: "Approved"},
		{Kind: ErrWorkflowUnreachableState, State: "Cancelled"},
	}, "self transition")
	// Draft to cancelled (0 to 2), submitted back to draft (1 to 0) and anything out of a
	// cancelled state are refused.
	for _, c := range []struct {
		from, to string
		a, b     int16
	}{
		{"Draft", "Cancelled", 0, 2},
		{"Approved", "Draft", 1, 0},
		{"Cancelled", "Approved", 2, 1},
		{"Cancelled", "Draft", 2, 0},
	} {
		extra := fmt.Sprintf("[[workflow.transitions]]\naction = \"X\"\nfrom = \"%s\"\nto = \"%s\"\nroles = [\"A\"]\n", c.from, c.to)
		sameValue(t, invalidOf(t, withWorkflow(orderDoc, workflowTOML+extra)),
			one(DefinitionError{Kind: ErrWorkflowBadMove, Action: "X", From: c.from, To: c.to, FromDocStatus: c.a, ToDocStatus: c.b}),
			c.from+" to "+c.to)
	}
	// 0 to 0, 0 to 1, 1 to 1 and 1 to 2 are the allowed moves.
	extra := "[[workflow.transitions]]\naction = \"Note\"\nfrom = \"Approved\"\nto = \"Approved2\"\nroles = [\"A\"]\n" +
		"[[workflow.states]]\nname = \"Approved2\"\ndocstatus = 1\n"
	// (arrays of tables may be written in any order)
	mustDocType(t, withWorkflow(orderDoc, workflowTOML+extra))
}

// Rust: load.rs::every_state_must_be_reachable_from_the_initial_state
func TestEveryStateMustBeReachableFromTheInitialState(t *testing.T) {
	orphan := workflowTOML + "[[workflow.states]]\nname = \"Orphan\"\ndocstatus = 0\n"
	sameValue(t, invalidOf(t, withWorkflow(orderDoc, orphan)), one(DefinitionError{Kind: ErrWorkflowUnreachableState, State: "Orphan"}), "orphan")
	// A transition out of the orphan does not make it reachable.
	island := orphan + "[[workflow.transitions]]\naction = \"Go\"\nfrom = \"Orphan\"\nto = \"Draft\"\nroles = [\"A\"]\n"
	sameValue(t, invalidOf(t, withWorkflow(orderDoc, island)), one(DefinitionError{Kind: ErrWorkflowUnreachableState, State: "Orphan"}), "island")
	// The order of states does not matter for reachability, only for the initial state.
	late := workflowTOML + "[[workflow.states]]\nname = \"Late\"\ndocstatus = 0\n" +
		"[[workflow.transitions]]\naction = \"Later\"\nfrom = \"Draft\"\nto = \"Late\"\nroles = [\"A\"]\n"
	mustDocType(t, withWorkflow(orderDoc, late))
}

// Rust: load.rs::a_field_cannot_be_named_like_the_state_column
func TestAFieldCannotBeNamedLikeTheStateColumn(t *testing.T) {
	field := "[[fields]]\nname = \"workflow_state\"\ntype = \"data\"\n"
	sameValue(t, invalidOf(t, withWorkflow(orderDoc+field, workflowTOML)), one(DefinitionError{Kind: ErrWorkflowStateFieldName}), "workflow_state field")
	// Without a workflow the name is an ordinary field name.
	mustDocType(t, orderDoc+field)
}

// Rust: load.rs::all_workflow_errors_are_reported_together_with_the_others
func TestAllWorkflowErrorsAreReportedTogetherWithTheOthers(t *testing.T) {
	text := "name = \"Order\"\nlabel = \"\"\n" + `
[workflow]
[[workflow.states]]
name = "A"
docstatus = 1
[[workflow.transitions]]
action = "Go"
from = "A"
to = "Nowhere"
`
	errs := invalidOf(t, text)
	if !hasKind(errs, ErrEmptyLabel) || !hasErr(errs, DefinitionError{Kind: ErrWorkflowInitialNotDraft, State: "A"}) ||
		!hasKind(errs, ErrWorkflowNoRoles) || !hasKind(errs, ErrWorkflowUnknownState) {
		t.Errorf("errors: %v", errs)
	}
}

// ----- Field permission levels (M10.2, ADR 0024) ------------------------------------------

const levelsDoc = "name = \"t\"\nlabel = \"T\"\n"

func levelField(body string) string {
	return "[[fields]]\nname = \"x\"\ntype = \"int\"\n" + body + "\n"
}

// Rust: load.rs::perm_level_and_level_default_to_zero_and_parse
func TestPermLevelAndLevelDefaultToZeroAndParse(t *testing.T) {
	d := mustDocType(t, levelsDoc+levelField("")+"[[fields]]\nname = \"y\"\ntype = \"int\"\nperm_level = 3\n"+
		rule("role = \"A\"\nread = true\nlevel = 3"))
	if d.Field("x").PermLevel != 0 || d.Field("y").PermLevel != 3 || d.Permissions[0].Level != 3 || !d.HasFieldLevels() {
		t.Errorf("levels: %+v", d)
	}
	if mustDocType(t, levelsDoc+levelField("")).HasFieldLevels() {
		t.Error("has_field_levels without levels")
	}
	// The highest level is 9.
	mustDocType(t, levelsDoc+levelField("perm_level = 9"))
}

// Rust: load.rs::a_perm_level_or_level_outside_zero_to_nine_is_a_definition_error
func TestAPermLevelOrLevelOutsideZeroToNineIsADefinitionError(t *testing.T) {
	for _, bad := range []int64{10, -1, 300, 256} {
		sameValue(t, invalidOf(t, levelsDoc+levelField(fmt.Sprintf("perm_level = %d", bad))),
			one(DefinitionError{Kind: ErrInvalidPermLevel, Field: "x", Value: bad}), fmt.Sprint(bad))
		sameValue(t, invalidOf(t, levelsDoc+rule(fmt.Sprintf("role = \"A\"\nread = true\nlevel = %d", bad))),
			one(DefinitionError{Kind: ErrInvalidRuleLevel, Role: "A", Value: bad}), fmt.Sprint(bad))
	}
	// Reported next to the other problems of the field.
	errs := invalidOf(t, levelsDoc+"[[fields]]\nname = \"x\"\ntype = \"nope\"\nperm_level = 11\n")
	if len(errs) != 2 {
		t.Errorf("errors: %v", errs)
	}
	// A level is an integer.
	_, err := ParseDocType(levelsDoc + levelField("perm_level = \"1\""))
	if !isParseError(err) {
		t.Errorf("string level: %v", err)
	}
}

// Rust: load.rs::a_field_of_a_child_doctype_stays_at_level_zero
func TestAFieldOfAChildDoctypeStaysAtLevelZero(t *testing.T) {
	child := "name = \"row\"\nlabel = \"R\"\nis_child = true\n"
	sameValue(t, invalidOf(t, child+levelField("perm_level = 1")), one(DefinitionError{Kind: ErrChildFieldPermLevel, Field: "x"}), "child level")
	mustDocType(t, child+levelField("perm_level = 0"))
	// A Table field of the parent can carry the level.
	mustDocType(t, levelsDoc+"[[fields]]\nname = \"rows\"\ntype = \"table\"\ntarget = \"row\"\nperm_level = 2\n")
}

// Rust: load.rs::a_rule_of_a_level_grants_read_and_write_only_without_if_owner
func TestARuleOfALevelGrantsReadAndWriteOnlyWithoutIfOwner(t *testing.T) {
	at := func(body string) string { return submittableDoc + rule("role = \"A\"\nlevel = 1\n"+body) }
	mustDocType(t, at("read = true"))
	mustDocType(t, at("read = true\nwrite = true"))
	sameValue(t, invalidOf(t, at("write = true")), one(DefinitionError{Kind: ErrLevelWriteWithoutRead, Role: "A", Level: 1}), "write without read")
	sameValue(t, invalidOf(t, at("read = true\nif_owner = true")), one(DefinitionError{Kind: ErrLevelRuleIfOwner, Role: "A", Level: 1}), "if_owner")
	for _, right := range []Right{RightCreate, RightDelete, RightSubmit, RightCancel, RightAmend, RightExport, RightImport} {
		sameValue(t, invalidOf(t, at(fmt.Sprintf("read = true\n%s = true", right))),
			one(DefinitionError{Kind: ErrLevelRuleRight, Role: "A", Level: 1, Right: right}), right.Name())
	}
	// A level rule that grants nothing is the same mistake as at level 0.
	sameValue(t, invalidOf(t, at("")), one(DefinitionError{Kind: ErrEmptyPermissionRule, Role: "A"}), "empty level rule")
}

// Rust: load.rs::rules_are_unique_per_role_if_owner_and_level
func TestRulesAreUniquePerRoleIfOwnerAndLevel(t *testing.T) {
	a := func(level int, ifOwner bool) string {
		return rule(fmt.Sprintf("role = \"A\"\nread = true\nlevel = %d\nif_owner = %t", level, ifOwner))
	}
	// The same role may have a rule at level 0 and one at each level.
	mustDocType(t, plainDoc+a(0, false)+a(1, false)+a(2, false))
	// Two rules at the same level 1 are one too many (if_owner is refused there, so no variant).
	sameValue(t, invalidOf(t, plainDoc+rule("role = \"A\"\nread = true\nlevel = 1")+rule("role = \"A\"\nread = true\nwrite = true\nlevel = 1")),
		one(DefinitionError{Kind: ErrDuplicateLevelRule, Role: "A", Level: 1}), "duplicate level rule")
	// Level 0 keeps its own check (and its own error).
	sameValue(t, invalidOf(t, plainDoc+a(0, false)+a(0, false)),
		one(DefinitionError{Kind: ErrDuplicatePermissionRule, Role: "A"}), "duplicate level 0 rule")
}

// Rust: load.rs::fields_that_a_doctype_setting_names_stay_at_level_zero
func TestFieldsThatADoctypeSettingNamesStayAtLevelZero(t *testing.T) {
	key := func(setting, extra string) string {
		return "name = \"t\"\nlabel = \"T\"\n" + setting + "\n[[fields]]\nname = \"code\"\ntype = \"data\"\n" + extra + "\n"
	}
	sameValue(t, invalidOf(t, key("naming = { field = \"code\" }", "required = true\nunique = true\nperm_level = 1")), []DefinitionError{
		{Kind: ErrRequiredPermLevel, Field: "code"},
		{Kind: ErrKeyFieldPermLevel, Field: "code", UsedAs: "the naming field"},
	}, "naming field")
	sameValue(t, invalidOf(t, key("title_field = \"code\"", "perm_level = 1")),
		one(DefinitionError{Kind: ErrKeyFieldPermLevel, Field: "code", UsedAs: "the title_field"}), "title_field")
	sameValue(t, invalidOf(t, key("list_fields = [\"code\"]", "perm_level = 2")),
		one(DefinitionError{Kind: ErrKeyFieldPermLevel, Field: "code", UsedAs: "a list_fields column"}), "list_fields")
	// Another field may carry a level while those settings name level 0 fields.
	ok := key("title_field = \"code\"\nlist_fields = [\"code\"]\nnaming = { field = \"code\" }", "required = true\nunique = true") +
		"[[fields]]\nname = \"secret\"\ntype = \"int\"\nperm_level = 1\n"
	mustDocType(t, ok)
}

// Rust: load.rs::a_required_field_cannot_have_a_level
func TestARequiredFieldCannotHaveALevel(t *testing.T) {
	sameValue(t, invalidOf(t, levelsDoc+levelField("required = true\nperm_level = 1")),
		one(DefinitionError{Kind: ErrRequiredPermLevel, Field: "x"}), "required level")
	mustDocType(t, levelsDoc+levelField("required = true"))
}

// Rust: load.rs::a_level_without_a_rule_is_allowed_and_closed_to_everybody_but_the_administrator
func TestALevelWithoutARuleIsAllowedAndClosedToEverybodyButTheAdministrator(t *testing.T) {
	// No rule of level 1 anywhere: legitimate (only Administrator and System see it).
	d := mustDocType(t, levelsDoc+levelField("perm_level = 1")+rule("role = \"A\"\nread = true"))
	reader := UserCaller("u", []string{"A"})
	if FieldAccessOf(d, reader).CanRead(1) {
		t.Error("a reader sees level 1")
	}
	if !FieldAccessOf(d, SystemCaller("cli")).CanRead(1) {
		t.Error("the system does not see level 1")
	}
}

// Rust: load.rs::the_workflow_names_no_field_so_it_has_nothing_to_protect
func TestTheWorkflowNamesNoFieldSoItHasNothingToProtect(t *testing.T) {
	// The state is the standard column workflow_state: a field of that name is refused.
	errs := invalidOf(t, submittableDoc+"[workflow]\n[[workflow.states]]\nname = \"Draft\"\ndocstatus = 0\n[[fields]]\nname = \"workflow_state\"\ntype = \"data\"\nperm_level = 1\n")
	if !hasKind(errs, ErrWorkflowStateFieldName) {
		t.Errorf("errors: %v", errs)
	}
}

// Rust: load.rs::the_example_of_the_guide_loads_and_means_what_it_says
// The example of docs/guide/getting-started.md ("Field permission levels"), in a file.
func TestTheExampleOfTheGuideLoadsAndMeansWhatItSays(t *testing.T) {
	d := mustDocType(t, `
name = "customer"
label = "Customer"

[[fields]]
name = "full_name"
type = "data"

[[fields]]
name = "credit_limit"
type = "decimal"
precision = 12
scale = 2
perm_level = 1

[[permissions]]
role = "Sales User"
read = true
write = true
create = true

[[permissions]]
role = "Sales Manager"
read = true
write = true

# Level 1: the manager sees and changes credit_limit, the sales user does not even know it exists.
[[permissions]]
role = "Sales Manager"
level = 1
read = true
write = true
`)
	user := FieldAccessOf(d, UserCaller("u", []string{"Sales User"}))
	if !(user.Shows(0) && !user.Shows(1)) {
		t.Error("sales user")
	}
	manager := FieldAccessOf(d, UserCaller("m", []string{"Sales Manager"}))
	if !(manager.CanRead(1) && manager.CanWrite(1)) {
		t.Error("sales manager")
	}
}
