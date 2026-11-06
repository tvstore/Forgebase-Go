package core

import (
	"slices"
	"strings"
	"testing"
)

// Rust: meta.rs::methods_are_registered_by_full_name_and_a_duplicate_is_refused
func TestMethodsAreRegisteredByFullNameAndADuplicateIsRefused(t *testing.T) {
	def := func(access MethodAccess) MethodDef { return MethodDef{App: "sales", Name: "ping", Access: access} }
	registry := NewRegistry()
	if registry.Method("sales.ping") != nil {
		t.Fatal("method present in an empty registry")
	}
	if !registry.AddMethod(def(MethodAnyUser())) {
		t.Fatal("first add refused")
	}
	if registry.AddMethod(def(MethodRoles("A"))) {
		t.Fatal("duplicate accepted")
	}
	kept := registry.Method("sales.ping")
	if kept == nil || !kept.Access.AnyUser {
		t.Errorf("the first one is not kept: %+v", kept)
	}
	if registry.Method("ping") != nil {
		t.Error("lookup by bare name")
	}
	if len(registry.Methods()) != 1 {
		t.Errorf("methods = %d", len(registry.Methods()))
	}
}

// Rust: meta.rs::humanize_splits_on_underscores_and_capitalises
func TestHumanizeSplitsOnUnderscoresAndCapitalises(t *testing.T) {
	for name, label := range map[string]string{
		"order_date":     "Order Date",
		"qty":            "Qty",
		"customer_group": "Customer Group",
		"a_b_c":          "A B C",
		"item2_total":    "Item2 Total",
		"":               "",
	} {
		if got := Humanize(name); got != label {
			t.Errorf("Humanize(%q) = %q, want %q", name, got, label)
		}
	}
}

func errorsOf(t *testing.T, d *DocType) []DefinitionError {
	t.Helper()
	errs := d.Validate()
	if errs == nil {
		t.Fatal("expected validation errors")
	}
	return errs
}

// Rust: meta.rs::valid_doctype_passes
func TestValidDoctypePasses(t *testing.T) {
	d := mkDocType(
		dataField("a", 1),
		dataField("b", 65_535),
		decimalField("c", 28, 28),
		selectField("d", "x", "y"),
	)
	if errs := d.Validate(); errs != nil {
		t.Errorf("errors: %v", errs)
	}
}

// Rust: meta.rs::name_and_label_rules
func TestNameAndLabelRules(t *testing.T) {
	d := mkDocType()
	d.Name = "Thing"
	d.Label = "  "
	sameValue(t, errorsOf(t, d), []DefinitionError{
		{Kind: ErrInvalidDocTypeName, Name: "Thing", Reason: IdentError{Kind: IdentInvalidFormat}},
		{Kind: ErrEmptyLabel},
	}, "errors")
}

// Rust: meta.rs::field_rules
func TestFieldRules(t *testing.T) {
	emptyLabel := intField("x")
	emptyLabel.Label = ""
	cases := []struct {
		f    DocField
		want DefinitionError
	}{
		{intField("Bad"), DefinitionError{Kind: ErrInvalidFieldName, Field: "Bad", Reason: IdentError{Kind: IdentInvalidFormat}}},
		{intField("owner"), DefinitionError{Kind: ErrInvalidFieldName, Field: "owner", Reason: IdentError{Kind: IdentReserved}}},
		{emptyLabel, DefinitionError{Kind: ErrEmptyFieldLabel, Field: "x"}},
		{dataField("x", 0), DefinitionError{Kind: ErrMaxLengthOutOfRange, Field: "x", Value: 0}},
		{dataField("x", 65_536), DefinitionError{Kind: ErrMaxLengthOutOfRange, Field: "x", Value: 65_536}},
		{decimalField("x", 0, 0), DefinitionError{Kind: ErrPrecisionOutOfRange, Field: "x", Value: 0}},
		{decimalField("x", 29, 2), DefinitionError{Kind: ErrPrecisionOutOfRange, Field: "x", Value: 29}},
		{decimalField("x", 5, 6), DefinitionError{Kind: ErrScaleExceedsPrecision, Field: "x", Scale: 6, Precision: 5}},
		{selectField("x"), DefinitionError{Kind: ErrEmptyOptions, Field: "x"}},
		{selectField("x", "a", ""), DefinitionError{Kind: ErrEmptyOption, Field: "x"}},
		{selectField("x", "a", "a"), DefinitionError{Kind: ErrDuplicateOption, Field: "x", Option: "a"}},
	}
	for _, c := range cases {
		sameValue(t, errorsOf(t, mkDocType(c.f)), []DefinitionError{c.want}, "field "+c.f.Name)
	}
}

// Rust: meta.rs::duplicate_fields_are_rejected
func TestDuplicateFieldsAreRejected(t *testing.T) {
	d := mkDocType(intField("a"), textField("a"))
	sameValue(t, errorsOf(t, d), []DefinitionError{{Kind: ErrDuplicateField, Field: "a"}}, "errors")
}

// Rust: meta.rs::naming_field_rules
func TestNamingFieldRules(t *testing.T) {
	good := dataField("email", 10)
	good.Required, good.Unique = true, true
	naming := func(name string) Naming { return Naming{Kind: NamingField, Field: name} }

	d := mkDocType(good)
	d.Naming = naming("email")
	if errs := d.Validate(); errs != nil {
		t.Errorf("good: %v", errs)
	}

	d.Naming = naming("nope")
	sameValue(t, errorsOf(t, d), []DefinitionError{{Kind: ErrNamingFieldMissing, Field: "nope"}}, "missing")

	notData := good
	notData.Type = FieldType{Kind: FieldText}
	d = mkDocType(notData)
	d.Naming = naming("email")
	sameValue(t, errorsOf(t, d), []DefinitionError{{Kind: ErrNamingFieldNotData, Field: "email", Found: "text"}}, "not data")

	weak := good
	weak.Required, weak.Unique = false, false
	d = mkDocType(weak)
	d.Naming = naming("email")
	sameValue(t, errorsOf(t, d), []DefinitionError{
		{Kind: ErrNamingFieldNotRequired, Field: "email"},
		{Kind: ErrNamingFieldNotUnique, Field: "email"},
	}, "weak")
}

// Rust: meta.rs::multiple_errors_are_collected
func TestMultipleErrorsAreCollected(t *testing.T) {
	d := mkDocType(intField("id"), selectField("x"))
	d.Label = ""
	if n := len(errorsOf(t, d)); n != 3 {
		t.Errorf("got %d errors", n)
	}
}

// Rust: meta.rs::nul_is_rejected_in_labels_and_options
func TestNulIsRejectedInLabelsAndOptions(t *testing.T) {
	d := mkDocType(selectField("s", "a\x00b", "ok"), textField("t"))
	d.Fields[1].Label = "x\x00"
	d.Label = "T\x00"
	sameValue(t, errorsOf(t, d), []DefinitionError{
		{Kind: ErrNulInLabel},
		{Kind: ErrNulInOption, Field: "s"},
		{Kind: ErrNulInFieldLabel, Field: "t"},
	}, "errors")
}

// Rust: meta.rs::registry_rejects_invalid_doctypes
func TestRegistryRejectsInvalidDoctypes(t *testing.T) {
	registry := NewRegistry()
	badName := mkDocType()
	badName.Name = "x\"; drop table y; --"
	errs := registry.Add(badName)
	if len(errs) != 1 || errs[0].Kind != ErrInvalidDocTypeName {
		t.Errorf("errors: %v", errs)
	}
	badScale := mkDocType(decimalField("amount", 2, 3))
	sameValue(t, registry.Add(badScale), []DefinitionError{{Kind: ErrScaleExceedsPrecision, Field: "amount", Scale: 3, Precision: 2}}, "bad scale")
	if !registry.IsEmpty() {
		t.Error("registry not empty")
	}
}

// Rust: meta.rs::link_target_must_be_a_valid_doctype_name
func TestLinkTargetMustBeAValidDoctypeName(t *testing.T) {
	if errs := mkDocType(linkField("c", "customer")).Validate(); errs != nil {
		t.Errorf("good link: %v", errs)
	}
	for _, bad := range []string{"Customer", "", "x\"; drop table y; --", "9a"} {
		errs := errorsOf(t, mkDocType(linkField("c", bad)))
		if len(errs) != 1 || errs[0].Kind != ErrInvalidLinkTarget || errs[0].Target != bad {
			t.Errorf("%q: %v", bad, errs)
		}
	}
	tooLong := strings.Repeat("a", MaxDocTypeNameLen+1)
	if n := len(errorsOf(t, mkDocType(linkField("c", tooLong)))); n != 1 {
		t.Errorf("too long: %d errors", n)
	}
}

// Rust: meta.rs::check_references_reports_unknown_targets
func TestCheckReferencesReportsUnknownTargets(t *testing.T) {
	registry := NewRegistry()
	order := mkDocType(linkField("customer", "customer"), linkField("owner_group", "grp"))
	order.Name = "order"
	if errs := registry.Add(order); errs != nil {
		t.Fatal(errs)
	}
	sameValue(t, registry.CheckReferences(), []DefinitionError{
		{Kind: ErrUnknownLinkTarget, DocType: "order", Field: "customer", Target: "customer"},
		{Kind: ErrUnknownLinkTarget, DocType: "order", Field: "owner_group", Target: "grp"},
	}, "unknown targets")
	customer := mkDocType()
	customer.Name = "customer"
	registry.Add(customer)
	grp := mkDocType()
	grp.Name = "grp"
	registry.Add(grp)
	// A target added after the DocType that links to it is fine.
	if errs := registry.CheckReferences(); errs != nil {
		t.Errorf("after adding targets: %v", errs)
	}
	if errs := NewRegistry().CheckReferences(); errs != nil {
		t.Errorf("an empty registry has no references: %v", errs)
	}
}

// Rust: meta.rs::self_link_is_allowed
func TestSelfLinkIsAllowed(t *testing.T) {
	registry := NewRegistry()
	tree := mkDocType(linkField("parent", "tree"))
	tree.Name = "tree"
	if errs := registry.Add(tree); errs != nil {
		t.Fatal(errs)
	}
	if errs := registry.CheckReferences(); errs != nil {
		t.Errorf("errors: %v", errs)
	}
}

// Rust: meta.rs::table_target_must_be_a_valid_doctype_name_and_not_unique
func TestTableTargetMustBeAValidDoctypeNameAndNotUnique(t *testing.T) {
	if errs := mkDocType(tableField("rows", "row")).Validate(); errs != nil {
		t.Errorf("good table: %v", errs)
	}
	errs := errorsOf(t, mkDocType(tableField("rows", "Bad Name")))
	if len(errs) != 1 || errs[0].Kind != ErrInvalidTableTarget || errs[0].Target != "Bad Name" {
		t.Errorf("bad name: %v", errs)
	}
	unique := tableField("rows", "row")
	unique.Unique = true
	sameValue(t, errorsOf(t, mkDocType(unique)), []DefinitionError{{Kind: ErrUniqueTable, Field: "rows"}}, "unique")
	// required is allowed: at least one row.
	required := tableField("rows", "row")
	required.Required = true
	if errs := mkDocType(required).Validate(); errs != nil {
		t.Errorf("required: %v", errs)
	}
	if (FieldType{Kind: FieldTable, Target: "r"}).Name() != "table" {
		t.Error("table type name")
	}
}

// Rust: meta.rs::child_doctype_rules
func TestChildDoctypeRules(t *testing.T) {
	child := func(fields ...DocField) *DocType { return named("row", true, fields...) }
	if errs := child(intField("qty"), linkField("c", "x")).Validate(); errs != nil {
		t.Errorf("good child: %v", errs)
	}
	sameValue(t, errorsOf(t, child(tableField("more", "other"))), []DefinitionError{{Kind: ErrChildWithTable, Field: "more"}}, "nested table")
	unique := intField("code")
	unique.Unique = true
	sameValue(t, errorsOf(t, child(unique)), []DefinitionError{{Kind: ErrChildWithUnique, Field: "code"}}, "unique")
	withNaming := child()
	withNaming.Naming = Naming{Kind: NamingField, Field: "code"}
	sameValue(t, errorsOf(t, withNaming), []DefinitionError{{Kind: ErrChildWithNaming}}, "naming")
	// The same fields are fine in a non-child DocType (apart from the table target rules).
	unique2 := intField("code")
	unique2.Unique = true
	if errs := mkDocType(unique2).Validate(); errs != nil {
		t.Errorf("non-child unique: %v", errs)
	}
}

func registryOf(t *testing.T, doctypes ...*DocType) *Registry {
	t.Helper()
	registry := NewRegistry()
	for _, d := range doctypes {
		if errs := registry.Add(d); errs != nil {
			t.Fatalf("Add(%s): %v", d.Name, errs)
		}
	}
	return registry
}

// Rust: meta.rs::check_references_for_child_tables
func TestCheckReferencesForChildTables(t *testing.T) {
	good := registryOf(t,
		named("order", false, tableField("items", "item")),
		named("item", true, intField("qty")),
	)
	if errs := good.CheckReferences(); errs != nil {
		t.Errorf("good: %v", errs)
	}
	parent, field := good.ParentOf("item")
	if parent == nil || parent.Name != "order" || field.Name != "items" {
		t.Errorf("parent_of(item) = %v, %v", parent, field)
	}
	if p, _ := good.ParentOf("order"); p != nil {
		t.Error("parent_of(order) should be none")
	}

	bad := registryOf(t,
		named("order", false, tableField("a", "nope"), tableField("b", "plain")),
		named("plain", false),
	)
	sameValue(t, bad.CheckReferences(), []DefinitionError{
		{Kind: ErrUnknownTableTarget, DocType: "order", Field: "a", Target: "nope"},
		{Kind: ErrTableTargetNotChild, DocType: "order", Field: "b", Target: "plain"},
	}, "bad")
}

// Rust: meta.rs::a_child_belongs_to_exactly_one_table_field
func TestAChildBelongsToExactlyOneTableField(t *testing.T) {
	shared := registryOf(t,
		named("a", false, tableField("rows", "row"), tableField("more", "row")),
		named("b", false, tableField("rows", "row")),
		named("row", true),
	)
	errs := shared.CheckReferences()
	sameValue(t, errs, []DefinitionError{{Kind: ErrChildSharedByTables, Child: "row", Uses: []string{"a.rows", "a.more", "b.rows"}}}, "shared")
	if got, want := errs[0].Error(), "child doctype `row` is used by more than one table field: a.rows, a.more, b.rows"; got != want {
		t.Errorf("message %q", got)
	}
	unused := registryOf(t, named("row", true))
	sameValue(t, unused.CheckReferences(), []DefinitionError{{Kind: ErrUnusedChild, DocType: "row"}}, "unused")
}

// Rust: meta.rs::a_link_to_a_child_is_refused
func TestALinkToAChildIsRefused(t *testing.T) {
	registry := registryOf(t,
		named("order", false, tableField("items", "item"), linkField("fav", "item")),
		named("item", true),
	)
	sameValue(t, registry.CheckReferences(), []DefinitionError{{Kind: ErrLinkToChild, DocType: "order", Field: "fav", Target: "item"}}, "link to child")
}

// Rust: meta.rs::column_and_table_fields
func TestColumnAndTableFields(t *testing.T) {
	d := mkDocType(intField("a"), tableField("rows", "row"), textField("b"))
	var columns []string
	for _, f := range d.ColumnFields() {
		columns = append(columns, f.Name)
	}
	if !slices.Equal(columns, []string{"a", "b"}) {
		t.Errorf("columns = %v", columns)
	}
	tables := d.TableFields()
	if len(tables) != 1 || tables[0].Field.Name != "rows" || tables[0].Target != "row" {
		t.Errorf("tables = %v", tables)
	}
}

// Rust: meta.rs::registry_rejects_duplicates
func TestRegistryRejectsDuplicates(t *testing.T) {
	registry := NewRegistry()
	if !registry.IsEmpty() {
		t.Error("new registry not empty")
	}
	if errs := registry.Add(mkDocType()); errs != nil {
		t.Fatal(errs)
	}
	sameValue(t, registry.Add(mkDocType()), []DefinitionError{{Kind: ErrDuplicateDocType, Name: "thing"}}, "duplicate")
	if registry.Len() != 1 {
		t.Errorf("len = %d", registry.Len())
	}
	if registry.Get("thing") == nil || registry.Get("other") != nil {
		t.Error("get")
	}
	if len(registry.Iter()) != 1 {
		t.Error("iter")
	}
}

// namedHooks is a hook set that does nothing; tests tell registered sets apart by pointer.
type namedHooks struct {
	BaseHooks
	id int
}

// Rust: meta.rs::hooks_are_kept_in_registration_order
func TestHooksAreKeptInRegistrationOrder(t *testing.T) {
	first, second := &namedHooks{id: 1}, &namedHooks{id: 2}
	registry := NewRegistry()
	registry.Add(mkDocType())
	if len(registry.Hooks("thing")) != 0 {
		t.Error("hooks before registration")
	}
	if err := registry.AddHooks("thing", first); err != nil {
		t.Fatal(err)
	}
	if err := registry.AddHooks("thing", second); err != nil {
		t.Fatal(err)
	}
	hooks := registry.Hooks("thing")
	if len(hooks) != 2 || hooks[0] != DocTypeHooks(first) || hooks[1] != DocTypeHooks(second) {
		t.Errorf("hooks = %v", hooks)
	}
	// A clone shares the hooks.
	if len(registry.Clone().Hooks("thing")) != 2 {
		t.Error("clone lost the hooks")
	}
	if len(registry.Hooks("other")) != 0 {
		t.Error("hooks of an unknown doctype")
	}
}

// Rust: meta.rs::hooks_need_a_known_parent_doctype
func TestHooksNeedAKnownParentDoctype(t *testing.T) {
	registry := NewRegistry()
	registry.Add(mkDocType())
	child := mkDocType()
	child.Name = "row"
	child.IsChild = true
	registry.Add(child)
	hooks := &namedHooks{}
	sameValue(t, registry.AddHooks("nope", hooks), error(DefinitionError{Kind: ErrUnknownHooksDocType, Name: "nope"}), "unknown")
	sameValue(t, registry.AddHooks("row", hooks), error(DefinitionError{Kind: ErrHooksOnChildDocType, DocType: "row"}), "child")
	if len(registry.Hooks("nope")) != 0 || len(registry.Hooks("row")) != 0 {
		t.Error("hooks were registered")
	}
}

// Rust: meta.rs::registry_debug_shows_hook_counts
// Not ported: it tests the Rust `Debug` formatting of Registry, which has no Go counterpart (Go
// formats with %v/%#v and the Registry has unexported fields). The hook counts themselves are
// covered by TestHooksAreKeptInRegistrationOrder.

// Rust: meta.rs::owner_is_a_tag_set_by_add_owned
func TestOwnerIsATagSetByAddOwned(t *testing.T) {
	registry := NewRegistry()
	if errs := registry.AddOwned("sales", mkDocType()); errs != nil {
		t.Fatal(errs)
	}
	other := mkDocType()
	other.Name = "other"
	registry.Add(other)
	if o, ok := registry.Owner("thing"); !ok || o != "sales" {
		t.Errorf("owner(thing) = %q, %v", o, ok)
	}
	if _, ok := registry.Owner("other"); ok {
		t.Error("owner(other)")
	}
	if _, ok := registry.Owner("unknown"); ok {
		t.Error("owner(unknown)")
	}
	// A refused DocType records no owner.
	dup := mkDocType()
	dup.Name = "other"
	if errs := registry.AddOwned("crm", dup); errs == nil {
		t.Error("duplicate accepted")
	}
	if _, ok := registry.Owner("other"); ok {
		t.Error("a refused DocType recorded an owner")
	}
}

func TestDefinitionErrorMessages(t *testing.T) {
	// A sample of the Display texts of the Rust DefinitionError, checked word for word.
	cases := []struct {
		e    DefinitionError
		want string
	}{
		{DefinitionError{Kind: ErrInvalidDocTypeName, Name: "Thing", Reason: IdentError{Kind: IdentEmpty}}, "doctype name `Thing` must not be empty"},
		{DefinitionError{Kind: ErrMaxLengthOutOfRange, Field: "x", Value: 0}, "field `x`: max_length 0 is out of range (1..=65535)"},
		{DefinitionError{Kind: ErrPrecisionOutOfRange, Field: "x", Value: 29}, "field `x`: precision 29 is out of range (1..=28)"},
		{DefinitionError{Kind: ErrUnknownNamingStrategy, Found: "series"}, "unknown naming strategy `series` (expected \"random\", { field = \"<name>\" } or { series = \"<pattern>\" })"},
		{DefinitionError{Kind: ErrInvalidPermissionRole, Role: "a\tb", Why: "must not contain control characters"}, "permission rule for role \"a\\tb\": invalid role: must not contain control characters"},
		{DefinitionError{Kind: ErrDuplicatePermissionRule, Role: "A", IfOwner: true}, "more than one permission rule for role \"A\" with if_owner = true"},
		{DefinitionError{Kind: ErrLevelRuleRight, Role: "A", Level: 1, Right: RightCreate}, "permission rule for role \"A\" at level 1: `create` is a document right decided at level 0; a level above 0 grants read and write only"},
		{DefinitionError{Kind: ErrWorkflowName, NameKind: "state", Name: "", Why: "must not be empty"}, "workflow: state name `` must not be empty"},
		{DefinitionError{Kind: ErrWorkflowBadMove, Action: "X", From: "a", To: "b", FromDocStatus: 0, ToDocStatus: 2}, "workflow: transition `X` from `a` to `b` moves docstatus 0 to 2; only 0 to 0, 0 to 1, 1 to 1 and 1 to 2 are allowed"},
		{DefinitionError{Kind: ErrWorkflowUnknownState, Action: "Send", From: "Nowhere", Key: "from", State: "Nowhere"}, "workflow: transition `Send` from `Nowhere`: unknown state `Nowhere` in `from`"},
	}
	for _, c := range cases {
		if got := c.e.Error(); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}
