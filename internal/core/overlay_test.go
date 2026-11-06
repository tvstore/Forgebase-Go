package core

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func overlayKind(err error) OverlayErrorKind {
	if e, ok := err.(OverlayError); ok {
		return e.Kind
	}
	return 0
}

// Rust: overlay.rs::an_overlay_is_only_fields
func TestAnOverlayIsOnlyFields(t *testing.T) {
	fields, err := ParseOverlay("[[fields]]\nname = \"tier\"\ntype = \"select\"\noptions = [\"a\", \"b\"]\n\n[[fields]]\nname = \"vip\"\ntype = \"check\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields[0].Label != "Tier" || fields[0].Type.Kind != FieldSelect || len(fields[0].Type.Options) != 2 {
		t.Errorf("fields: %+v", fields)
	}
	for _, bad := range []string{
		"name = \"x\"\n[[fields]]\nname = \"a\"\ntype = \"check\"\n",
		"[[permissions]]\nrole = \"x\"\n",
		"[[fields]]\nname = \"a\"\ntype = \"check\"\ncolour = 1\n",
	} {
		_, err := ParseOverlay(bad)
		if overlayKind(err) != OverlayParse {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := ParseOverlay(""); overlayKind(err) != OverlayNoFields {
		t.Errorf("empty: %v", err)
	}
}

// Rust: overlay.rs::a_field_gets_the_rules_of_a_doctype_field
func TestAFieldGetsTheRulesOfADoctypeField(t *testing.T) {
	_, err := ParseOverlay("[[fields]]\nname = \"a\"\ntype = \"select\"\n")
	if overlayKind(err) != OverlayInvalid {
		t.Errorf("options: %v", err)
	}
	_, err = ParseOverlay("[[fields]]\nname = \"a\"\ntype = \"nope\"\n")
	if overlayKind(err) != OverlayInvalid {
		t.Errorf("type: %v", err)
	}
}

const (
	overlayCustomer = "name = \"customer\"\nlabel = \"Customer\"\n[[fields]]\nname = \"email\"\ntype = \"data\"\n"
	overlayOrder    = "name = \"order\"\nlabel = \"Order\"\nsubmittable = true\n[[fields]]\nname = \"total\"\ntype = \"int\"\n[[fields]]\nname = \"lines\"\ntype = \"table\"\ntarget = \"line\"\n"
	overlayLine     = "name = \"line\"\nlabel = \"Line\"\nis_child = true\n"
)

func overlayRegistry(t *testing.T) *Registry {
	t.Helper()
	registry := NewRegistry()
	for _, text := range []string{overlayCustomer, overlayOrder, overlayLine} {
		if errs := registry.AddOwned("sales", mustDocType(t, text)); errs != nil {
			t.Fatal(errs)
		}
	}
	return registry
}

func overlayFields(t *testing.T, text string) []DocField {
	t.Helper()
	fields, err := ParseOverlay(text)
	if err != nil {
		t.Fatal(err)
	}
	return fields
}

// Rust: overlay.rs::a_bad_name_is_refused_when_the_field_is_added
func TestABadNameIsRefusedWhenTheFieldIsAdded(t *testing.T) {
	registry := overlayRegistry(t)
	for _, name := range []string{"Bad Name", "id", "1x", "custom_Bad"} {
		field, err := ParseFieldJSON(map[string]any{"name": name, "type": "check"})
		if err != nil {
			t.Fatalf("%s parses: %v", name, err)
		}
		err = registry.AddCustomField("customer", field)
		if k := overlayKind(err); k != OverlayInvalid && k != OverlayMissingPrefix {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Rust: overlay.rs::toml_and_json_give_the_same_field
func TestTomlAndJsonGiveTheSameField(t *testing.T) {
	fromTOML, err := ParseFieldTOML("name = \"custom_vip\"\ntype = \"check\"\nunique = true\n")
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := ParseFieldJSON(map[string]any{"name": "custom_vip", "type": "check", "unique": true})
	if err != nil {
		t.Fatal(err)
	}
	sameValue(t, fromTOML, fromJSON, "toml vs json")
	back, err := ParseFieldJSON(FieldToJSON(fromJSON))
	if err != nil {
		t.Fatal(err)
	}
	sameValue(t, back, fromJSON, "round trip")
	_, err = ParseFieldJSON(map[string]any{"name": "a", "type": "check", "x": 1})
	if overlayKind(err) != OverlayParse {
		t.Errorf("unknown key: %v", err)
	}
}

// Rust: overlay.rs::every_type_round_trips_through_json
func TestEveryTypeRoundTripsThroughJson(t *testing.T) {
	for _, text := range []string{
		"name = \"a\"\ntype = \"data\"\nmax_length = 20\nrequired = true\n",
		"name = \"a\"\ntype = \"decimal\"\nprecision = 10\nscale = 2\n",
		"name = \"a\"\ntype = \"select\"\noptions = [\"x\", \"y\"]\nallow_on_submit = true\n",
		"name = \"a\"\ntype = \"link\"\ntarget = \"customer\"\nlabel = \"Cust\"\nread_only = true\n",
		"name = \"a\"\ntype = \"text\"\n",
		"name = \"a\"\ntype = \"int\"\n",
		"name = \"a\"\ntype = \"date\"\n",
		"name = \"a\"\ntype = \"datetime\"\n",
	} {
		field, err := ParseFieldTOML(text)
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		back, err := ParseFieldJSON(FieldToJSON(field))
		if err != nil || !reflect.DeepEqual(back, field) {
			t.Errorf("%s: %+v %v", text, back, err)
		}
		// The same through real JSON text (numbers decoded as json.Number).
		raw, _ := json.Marshal(FieldToJSON(field))
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		var generic any
		if err := dec.Decode(&generic); err != nil {
			t.Fatal(err)
		}
		back, err = ParseFieldJSON(generic)
		if err != nil || !reflect.DeepEqual(back, field) {
			t.Errorf("json text %s: %+v %v", raw, back, err)
		}
	}
}

// Rust: overlay.rs::an_overlay_appends_in_file_order_and_records_the_owner
func TestAnOverlayAppendsInFileOrderAndRecordsTheOwner(t *testing.T) {
	registry := overlayRegistry(t)
	err := registry.AddOverlayFields("ext", "customer",
		overlayFields(t, "[[fields]]\nname = \"b\"\ntype = \"int\"\n[[fields]]\nname = \"a\"\ntype = \"int\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range registry.Get("customer").Fields {
		names = append(names, f.Name)
	}
	sameValue(t, names, []string{"email", "b", "a"}, "names")
	if o, ok := registry.FieldOwner("customer", "email"); !ok || o != "sales" {
		t.Errorf("owner(email) = %q %v", o, ok)
	}
	if o, ok := registry.FieldOwner("customer", "b"); !ok || o != "ext" {
		t.Errorf("owner(b) = %q %v", o, ok)
	}
	if s, ok := registry.FieldSourceOf("customer", "b"); !ok || s != (FieldSource{Kind: SourceApp, App: "ext"}) {
		t.Errorf("source(b) = %v %v", s, ok)
	}
	if _, ok := registry.FieldSourceOf("customer", "email"); ok {
		t.Error("source(email)")
	}
}

// Rust: overlay.rs::an_overlay_is_refused_all_or_nothing
func TestAnOverlayIsRefusedAllOrNothing(t *testing.T) {
	registry := overlayRegistry(t)
	before := registry.Get("customer").Clone()
	err := registry.AddOverlayFields("ext", "customer",
		overlayFields(t, "[[fields]]\nname = \"ok\"\ntype = \"int\"\n[[fields]]\nname = \"email\"\ntype = \"int\"\n"))
	sameValue(t, err, error(OverlayError{Kind: OverlayCollision, DocType: "customer", Name: "email"}), "collision")
	sameValue(t, registry.Get("customer"), before, "customer unchanged")
	if _, ok := registry.FieldSourceOf("customer", "ok"); ok {
		t.Error("source recorded")
	}
	twice := overlayFields(t, "[[fields]]\nname = \"x\"\ntype = \"int\"\n[[fields]]\nname = \"x\"\ntype = \"int\"\n")
	if k := overlayKind(registry.AddOverlayFields("ext", "customer", twice)); k != OverlayCollision {
		t.Errorf("twice: %v", k)
	}
}

// Rust: overlay.rs::overlay_refusals
func TestOverlayRefusals(t *testing.T) {
	registry := overlayRegistry(t)
	oneField := func(text string) []DocField { return overlayFields(t, text) }
	sameValue(t, registry.AddOverlayFields("ext", "nope", oneField("[[fields]]\nname = \"a\"\ntype = \"int\"\n")),
		error(OverlayError{Kind: OverlayUnknownDocType, Name: "nope"}), "unknown")
	sameValue(t, registry.AddOverlayFields("ext", "line", oneField("[[fields]]\nname = \"a\"\ntype = \"int\"\n")),
		error(OverlayError{Kind: OverlayChildDocType, Name: "line"}), "child")
	sameValue(t, registry.AddOverlayFields("ext", "customer", oneField("[[fields]]\nname = \"rows\"\ntype = \"table\"\ntarget = \"line\"\n")),
		error(OverlayError{Kind: OverlayTableField, Name: "rows"}), "table")
	sameValue(t, registry.AddOverlayFields("ext", "customer", oneField("[[fields]]\nname = \"custom_a\"\ntype = \"int\"\n")),
		error(OverlayError{Kind: OverlayReservedPrefix, Name: "custom_a"}), "prefix")
	// The DocType's own rules still apply: allow_on_submit needs a submittable DocType.
	err := registry.AddOverlayFields("ext", "customer", oneField("[[fields]]\nname = \"a\"\ntype = \"int\"\nallow_on_submit = true\n"))
	if overlayKind(err) != OverlayInvalid {
		t.Errorf("allow_on_submit: %v", err)
	}
	err = registry.AddOverlayFields("ext", "order",
		oneField("[[fields]]\nname = \"a\"\ntype = \"int\"\nallow_on_submit = true\nrequired = true\nunique = true\n"))
	if err != nil {
		t.Errorf("every property is allowed: %v", err)
	}
}

// Rust: overlay.rs::a_custom_field_has_its_own_rules
func TestACustomFieldHasItsOwnRules(t *testing.T) {
	registry := overlayRegistry(t)
	field := func(text string) DocField {
		f, err := ParseFieldTOML(text)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	sameValue(t, registry.AddCustomField("customer", field("name = \"vip\"\ntype = \"check\"\n")), error(OverlayError{Kind: OverlayMissingPrefix, Name: "vip"}), "prefix")
	sameValue(t, registry.AddCustomField("customer", field("name = \"custom_\"\ntype = \"check\"\n")), error(OverlayError{Kind: OverlayEmptyCustomName}), "empty name")
	sameValue(t, registry.AddCustomField("customer", field("name = \"custom_a\"\ntype = \"check\"\nrequired = true\n")), error(OverlayError{Kind: OverlayRequired, Name: "custom_a"}), "required")
	sameValue(t, registry.AddCustomField("customer", field("name = \"custom_a\"\ntype = \"check\"\nread_only = true\n")), error(OverlayError{Kind: OverlayReadOnly, Name: "custom_a"}), "read_only")
	sameValue(t, registry.AddCustomField("line", field("name = \"custom_a\"\ntype = \"check\"\n")), error(OverlayError{Kind: OverlayChildDocType, Name: "line"}), "child")
	sameValue(t, registry.AddCustomField("nope", field("name = \"custom_a\"\ntype = \"check\"\n")), error(OverlayError{Kind: OverlayUnknownDocType, Name: "nope"}), "unknown")
	if k := overlayKind(registry.AddCustomField("customer", field("name = \"custom_a\"\ntype = \"link\"\ntarget = \"ghost\"\n"))); k != OverlayInvalid {
		t.Errorf("ghost link: %v", k)
	}
	if k := overlayKind(registry.AddCustomField("customer", field("name = \"custom_a\"\ntype = \"link\"\ntarget = \"line\"\n"))); k != OverlayInvalid {
		t.Errorf("link to child: %v", k)
	}
	if registry.Get("customer").Field("custom_a") != nil {
		t.Error("a refused field was added")
	}
	err := registry.AddCustomField("customer", field("name = \"custom_ref\"\ntype = \"link\"\ntarget = \"order\"\nunique = true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := registry.FieldSourceOf("customer", "custom_ref"); !ok || s.Kind != SourceSite {
		t.Errorf("source = %v %v", s, ok)
	}
	if _, ok := registry.FieldOwner("customer", "custom_ref"); ok {
		t.Error("a site field has an owning app")
	}
	sameValue(t, registry.AddCustomField("customer", field("name = \"custom_ref\"\ntype = \"check\"\n")),
		error(OverlayError{Kind: OverlayCollision, DocType: "customer", Name: "custom_ref"}), "collision")
}

// ----- Field permission levels (M10.2, ADR 0024) ------------------------------------------

// Rust: overlay.rs::perm_level_round_trips_through_json_and_is_omitted_at_zero
func TestPermLevelRoundTripsThroughJsonAndIsOmittedAtZero(t *testing.T) {
	field, err := ParseFieldTOML("name = \"custom_limit\"\ntype = \"int\"\nperm_level = 2\n")
	if err != nil || field.PermLevel != 2 {
		t.Fatalf("field: %+v %v", field, err)
	}
	j := FieldToJSON(field)
	if j["perm_level"] != 2 {
		t.Errorf("perm_level = %v", j["perm_level"])
	}
	back, err := ParseFieldJSON(j)
	if err != nil {
		t.Fatal(err)
	}
	sameValue(t, back, field, "round trip")
	plain, _ := ParseFieldTOML("name = \"a\"\ntype = \"int\"\n")
	if _, ok := FieldToJSON(plain)["perm_level"]; ok {
		t.Error("perm_level written at 0")
	}
	// Out of range, wrong kind.
	for _, bad := range []string{"perm_level = 10", "perm_level = -1"} {
		_, err := ParseFieldTOML("name = \"a\"\ntype = \"int\"\n" + bad + "\n")
		if overlayKind(err) != OverlayInvalid {
			t.Errorf("%s: %v", bad, err)
		}
	}
	_, err = ParseFieldJSON(map[string]any{"name": "a", "type": "int", "perm_level": "1"})
	if overlayKind(err) != OverlayParse {
		t.Errorf("string level: %v", err)
	}
	_, err = ParseFieldJSON(map[string]any{"name": "a", "type": "int", "perm_level": 12})
	if overlayKind(err) != OverlayInvalid {
		t.Errorf("level 12: %v", err)
	}
}

// Rust: overlay.rs::an_overlay_field_may_carry_a_level_and_the_rules_come_from_the_target
func TestAnOverlayFieldMayCarryALevelAndTheRulesComeFromTheTarget(t *testing.T) {
	registry := overlayRegistry(t)
	err := registry.AddOverlayFields("ext", "customer", overlayFields(t, "[[fields]]\nname = \"limit\"\ntype = \"int\"\nperm_level = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	customer := registry.Get("customer")
	if customer.Field("limit").PermLevel != 1 || !customer.HasFieldLevels() {
		t.Error("level not carried")
	}
	// No rule of level 1 exists on the target: only the Administrator and the System see it. An
	// overlay cannot add rules (it holds [[fields]] only).
	if _, err := ParseOverlay("[[permissions]]\nrole = \"A\"\nlevel = 1\nread = true\n"); overlayKind(err) != OverlayParse {
		t.Errorf("permissions in an overlay: %v", err)
	}
	// A required field with a level is refused like in a DocType file.
	err = registry.AddOverlayFields("ext", "customer",
		overlayFields(t, "[[fields]]\nname = \"must\"\ntype = \"int\"\nrequired = true\nperm_level = 1\n"))
	oe, ok := err.(OverlayError)
	if !ok || oe.Kind != OverlayInvalid || !reflect.DeepEqual(oe.Problems, []DefinitionError{{Kind: ErrRequiredPermLevel, Field: "must"}}) {
		t.Errorf("required level: %v", err)
	}
	if registry.Get("customer").Field("must") != nil {
		t.Error("a refused field was added")
	}
}

// Rust: overlay.rs::a_custom_field_may_carry_a_level_but_still_not_required
func TestACustomFieldMayCarryALevelButStillNotRequired(t *testing.T) {
	registry := overlayRegistry(t)
	field, err := ParseFieldJSON(map[string]any{"name": "custom_limit", "type": "int", "perm_level": 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddCustomField("customer", field); err != nil {
		t.Fatal(err)
	}
	if registry.Get("customer").Field("custom_limit").PermLevel != 3 {
		t.Error("level not carried")
	}
	// The M10.1 rule stays: required is refused for a site field, with or without a level.
	required, err := ParseFieldJSON(map[string]any{"name": "custom_req", "type": "int", "perm_level": 1, "required": true})
	if err != nil {
		t.Fatal(err)
	}
	sameValue(t, registry.AddCustomField("customer", required), error(OverlayError{Kind: OverlayRequired, Name: "custom_req"}), "required")
	// A bad level cannot even be parsed into a field.
	if _, err := ParseFieldJSON(map[string]any{"name": "custom_x", "type": "int", "perm_level": 99}); err == nil {
		t.Error("level 99 parsed")
	}
	// The table of an order may carry a level, but a site cannot add one: custom Table fields stay
	// refused, and a child DocType cannot be extended at all.
	child, err := ParseFieldJSON(map[string]any{"name": "custom_y", "type": "int", "perm_level": 1})
	if err != nil {
		t.Fatal(err)
	}
	if k := overlayKind(registry.AddCustomField("line", child)); k != OverlayChildDocType {
		t.Errorf("child: %v", k)
	}
}

func TestParseFieldJSONNumbers(t *testing.T) {
	decode := func(text string) any {
		dec := json.NewDecoder(strings.NewReader(text))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	plain := func(text string) any {
		var v any
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// Integers: accepted as json.Number and as Go ints.
	if _, err := ParseFieldJSON(decode(`{"name":"a","type":"data","max_length":20,"perm_level":1}`)); err != nil {
		t.Errorf("integers: %v", err)
	}
	// Floats are refused for integer keys, through UseNumber and through plain Unmarshal (float64).
	for _, key := range []string{"max_length", "perm_level"} {
		for _, num := range []string{"5.0", "1e2", "1.5", "-0"} {
			text := `{"name":"a","type":"data","` + key + `":` + num + `}`
			for label, v := range map[string]any{"UseNumber": decode(text), "float64": plain(text)} {
				_, err := ParseFieldJSON(v)
				if overlayKind(err) != OverlayParse || !strings.Contains(err.Error(), "invalid type: floating point") {
					t.Errorf("%s %s=%s: %v", label, key, num, err)
				}
			}
		}
	}
	if _, err := ParseFieldJSON(decode(`{"name":"a","type":"data","max_length":-0}`)); err == nil || !strings.Contains(err.Error(), "floating point `-0.0`, expected u32") {
		t.Errorf("-0 message: %v", err)
	}
	if _, err := ParseFieldJSON(map[string]any{"name": "a", "type": "int", "perm_level": math.Copysign(0, -1)}); err == nil || !strings.Contains(err.Error(), "floating point `-0.0`, expected i64") {
		t.Errorf("float64 -0 message: %v", err)
	}
	_, err := ParseFieldJSON(decode(`{"name":"a","type":"data","max_length":5.0}`))
	if err == nil || !strings.Contains(err.Error(), "floating point `5.0`, expected u32") {
		t.Errorf("message: %v", err)
	}
	for _, v := range []any{"20", true} {
		if _, err := ParseFieldJSON(map[string]any{"name": "a", "type": "data", "max_length": v}); overlayKind(err) != OverlayParse {
			t.Errorf("max_length %v: %v", v, err)
		}
	}
	// A JSON null is absent only for an Option field of the Rust struct (label, max_length,
	// precision, scale, options, target); for the others serde refuses it.
	for _, key := range []string{"label", "max_length", "target"} {
		text := `{"name":"a","type":"data","` + key + `":null}`
		if _, err := ParseFieldJSON(decode(text)); err != nil {
			t.Errorf("null %s: %v", key, err)
		}
	}
	if _, err := ParseFieldJSON(decode(`{"name":"a","type":"select","options":null}`)); overlayKind(err) != OverlayInvalid {
		t.Errorf("null options is absent, so a select lacks them: %v", err)
	}
	for key, want := range map[string]string{
		"required": "expected a boolean", "unique": "expected a boolean", "allow_on_submit": "expected a boolean",
		"read_only": "expected a boolean", "perm_level": "expected i64", "name": "expected a string", "type": "expected a string",
	} {
		v := map[string]any{"name": "a", "type": "int"}
		v[key] = nil
		_, err := ParseFieldJSON(v)
		if overlayKind(err) != OverlayParse || !strings.Contains(err.Error(), "invalid type: null, "+want) {
			t.Errorf("null %s: %v", key, err)
		}
	}
	if _, err := ParseFieldJSON("not an object"); overlayKind(err) != OverlayParse {
		t.Errorf("not an object: %v", err)
	}
}

func TestOverlayErrorMessages(t *testing.T) {
	cases := []struct {
		e    OverlayError
		want string
	}{
		{OverlayError{Kind: OverlayNoFields}, "the overlay has no [[fields]]"},
		{OverlayError{Kind: OverlayUnknownDocType, Name: "x"}, "unknown doctype `x`"},
		{OverlayError{Kind: OverlayChildDocType, Name: "x"}, "doctype `x` is a child DocType; fields can only be added to a DocType with documents of its own"},
		{OverlayError{Kind: OverlayTableField, Name: "x"}, "field `x` has type table; overlays and custom fields cannot add Table fields"},
		{OverlayError{Kind: OverlayCollision, DocType: "d", Name: "f"}, "field `f` already exists on doctype `d`"},
		{OverlayError{Kind: OverlayReservedPrefix, Name: "custom_a"}, "field `custom_a` starts with `custom_`, a prefix reserved for site custom fields"},
		{OverlayError{Kind: OverlayMissingPrefix, Name: "vip"}, "the name of a site custom field must start with `custom_` (got `vip`)"},
		{OverlayError{Kind: OverlayEmptyCustomName}, "the name of a site custom field needs at least one character after `custom_`"},
		{OverlayError{Kind: OverlayRequired, Name: "f"}, "field `f`: a site custom field cannot be required (existing documents have no value for it)"},
		{OverlayError{Kind: OverlayReadOnly, Name: "f"}, "field `f`: a site custom field cannot be read_only (no hook can write it)"},
		{OverlayError{Kind: OverlayInvalid, Problems: []DefinitionError{{Kind: ErrEmptyLabel}, {Kind: ErrNulInLabel}}}, "invalid definition: doctype label must not be empty; doctype label must not contain a NUL character"},
	}
	for _, c := range cases {
		if got := c.e.Error(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
