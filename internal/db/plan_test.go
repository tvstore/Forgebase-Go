package db

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Unit tests of the planner, from plan.rs. They use no database.

func registryOf(t *testing.T, texts ...string) *core.Registry {
	t.Helper()
	registry := core.NewRegistry()
	for _, text := range texts {
		doctype, err := core.ParseDocType(text)
		if err != nil {
			t.Fatalf("valid DocType: %v\n%s", err, text)
		}
		if errs := registry.Add(doctype); len(errs) > 0 {
			t.Fatalf("valid registry: %v", errs)
		}
	}
	return registry
}

func stepTexts(steps []Step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.String()
	}
	return out
}

func conflictTexts(conflicts []Conflict) []string {
	out := make([]string, len(conflicts))
	for i, c := range conflicts {
		out[i] = c.String()
	}
	return out
}

func wantEqual(t *testing.T, got, want any, label string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %#v\nwant %#v", label, got, want)
	}
}

func wantNoPlanOutput(t *testing.T, plan schemaPlan) {
	t.Helper()
	if len(plan.steps) != 0 || len(plan.conflicts) != 0 || len(plan.warnings) != 0 {
		t.Errorf("expected an empty plan, got steps %v conflicts %v warnings %v",
			stepTexts(plan.steps), conflictTexts(plan.conflicts), plan.warnings)
	}
}

// Rust: plan.rs::a_not_validated_foreign_key_is_a_conflict
func TestANotValidatedForeignKeyIsAConflictUnit(t *testing.T) {
	expected := expectedForeignKey("item")
	found := expected
	found.validated = false
	var conflicts []Conflict
	checkForeignKeys("doc_x", "link", expected, []foreignKey{found}, &conflicts)
	if len(conflicts) != 1 || conflicts[0].Kind != ConflictForeignKeyMismatch || !strings.Contains(conflicts[0].Actual, "not validated") {
		t.Errorf("%v", conflicts)
	}
}

// Rust: plan.rs::a_table_field_is_not_a_column_in_the_schema_statements
func TestATableFieldIsNotAColumnInTheSchemaStatements(t *testing.T) {
	registry := registryOf(t,
		"name = \"ord\"\nlabel = \"O\"\n"+
			"[[fields]]\nname = \"title\"\ntype = \"data\"\n"+
			"[[fields]]\nname = \"lines\"\ntype = \"table\"\ntarget = \"line\"\n"+
			"[[fields]]\nname = \"note\"\ntype = \"text\"\n",
		"name = \"line\"\nlabel = \"L\"\nis_child = true\n")
	doctype := registry.Get("ord")
	create := createTableSQL(doctype, "doc_ord")
	if !strings.Contains(create, `"title"`) || !strings.Contains(create, `"note"`) {
		t.Errorf("%s", create)
	}
	if strings.Contains(create, "lines") {
		t.Errorf("%s", create)
	}
	if strings.Contains(addKeysSQL(doctype, "doc_ord"), "lines") {
		t.Error("the keys name the Table field")
	}
	if len(linkFields(doctype)) != 0 {
		t.Error("a Table field is a Link field")
	}
}

const itemTOML = `
name = "item"
label = "Item"
[[fields]]
name = "code"
type = "data"
max_length = 20
unique = true
[[fields]]
name = "price"
type = "decimal"
precision = 10
scale = 2
`

// Rust: plan.rs::new_doctype_creates_table_and_registers
func TestNewDocTypeCreatesTableAndRegisters(t *testing.T) {
	plan := planSchema(registryOf(t, itemTOML), newCatalog())
	if len(plan.steps) != 3 {
		t.Fatalf("%d steps", len(plan.steps))
	}
	wantEqual(t, plan.steps[0].Kind, StepCreateTable, "step 0")
	wantEqual(t, plan.steps[0].SQL, "CREATE TABLE \"doc_item\" (\n    "+
		"\"id\" uuid NOT NULL,\n    "+
		"\"name\" text NOT NULL,\n    "+
		"\"owner\" text NOT NULL,\n    "+
		"\"created_at\" timestamp with time zone NOT NULL,\n    "+
		"\"modified_at\" timestamp with time zone NOT NULL,\n    "+
		"\"modified_by\" text NOT NULL,\n    "+
		"\"version\" bigint NOT NULL DEFAULT 1,\n    "+
		"\"code\" character varying(20),\n    "+
		"\"price\" numeric(10,2)\n)", "create table")
	wantEqual(t, plan.steps[1].Kind, StepAddKeys, "step 1")
	wantEqual(t, plan.steps[1].SQL,
		"ALTER TABLE \"doc_item\" ADD PRIMARY KEY (\"id\"), ADD UNIQUE (\"name\"), ADD UNIQUE (\"code\")", "add keys")
	wantEqual(t, plan.steps[2], Step{Kind: StepRegisterDocType, DocType: "item"}, "step 2")
}

func standardColumnMap() map[string]column {
	m := map[string]column{}
	for _, c := range documentStandardColumns {
		m[c.name] = column{dataType: c.dataType, notNull: true}
	}
	return m
}

func set(keys ...colKey) map[colKey]bool {
	m := map[colKey]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func names(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// syncedCatalog is a catalog that matches itemTOML exactly.
func syncedCatalog() *catalog {
	columns := standardColumnMap()
	columns["code"] = column{dataType: "character varying(20)"}
	columns["price"] = column{dataType: "numeric(10,2)"}
	cat := newCatalog()
	cat.columns["doc_item"] = columns
	cat.unique = set(colKey{"doc_item", "code"}, colKey{"doc_item", "name"})
	cat.primary = set(colKey{"doc_item", "id"})
	cat.relations = names("doc_item")
	cat.tables = names("doc_item")
	cat.managed = names("item")
	return cat
}

// Rust: plan.rs::matching_schema_is_a_no_op
func TestMatchingSchemaIsANoOp(t *testing.T) {
	wantNoPlanOutput(t, planSchema(registryOf(t, itemTOML), syncedCatalog()))
}

// Rust: plan.rs::differences_are_classified
func TestDifferencesAreClassified(t *testing.T) {
	cat := syncedCatalog()
	columns := cat.columns["doc_item"]
	columns["code"] = column{dataType: "character varying(10)"}
	delete(columns, "price")
	columns["legacy"] = columns["code"]
	owner := columns["owner"]
	owner.notNull = false
	columns["owner"] = owner
	cat.managed["gone"] = true
	plan := planSchema(registryOf(t, itemTOML), cat)
	if len(plan.conflicts) != 2 {
		t.Errorf("conflicts: %v", conflictTexts(plan.conflicts))
	}
	if len(plan.steps) != 1 || plan.steps[0].Kind != StepAddColumn || plan.steps[0].Column != "price" {
		t.Errorf("steps: %v", stepTexts(plan.steps))
	}
	if !slices.Contains(plan.warnings, Warning{Kind: WarningColumnNotInMetadata, Table: "doc_item", Column: "legacy"}) {
		t.Errorf("warnings: %v", plan.warnings)
	}
	if !slices.Contains(plan.warnings, Warning{Kind: WarningDocTypeNotInRegistry, DocType: "gone"}) {
		t.Errorf("warnings: %v", plan.warnings)
	}
}

// Rust: plan.rs::unmanaged_table_conflicts
func TestUnmanagedTableConflicts(t *testing.T) {
	cat := syncedCatalog()
	cat.managed = names()
	plan := planSchema(registryOf(t, itemTOML), cat)
	wantEqual(t, plan.conflicts, []Conflict{{Kind: ConflictUnmanagedTable, Table: "doc_item"}}, "conflicts")
	if len(plan.steps) != 0 {
		t.Errorf("steps: %v", stepTexts(plan.steps))
	}
}

// Rust: plan.rs::unique_added_and_removed
func TestUniqueAddedAndRemoved(t *testing.T) {
	cat := syncedCatalog()
	cat.unique = set()
	added := planSchema(registryOf(t, itemTOML), cat)
	if len(added.steps) != 1 || added.steps[0].Kind != StepCreateUniqueIndex ||
		added.steps[0].SQL != `CREATE UNIQUE INDEX ON "doc_item" ("code")` {
		t.Errorf("%v", stepTexts(added.steps))
	}
	withoutUnique := strings.ReplaceAll(itemTOML, "unique = true", "")
	removed := planSchema(registryOf(t, withoutUnique), syncedCatalog())
	wantEqual(t, removed.warnings, []Warning{{Kind: WarningUniqueRemoved, Table: "doc_item", Column: "code"}}, "warnings")
}

// Rust: plan.rs::standard_key_drift_is_a_conflict
func TestStandardKeyDriftIsAConflict(t *testing.T) {
	cat := syncedCatalog()
	delete(cat.unique, colKey{"doc_item", "name"})
	cat.primary = set()
	plan := planSchema(registryOf(t, itemTOML), cat)
	var columns []string
	for _, c := range plan.conflicts {
		if c.Kind != ConflictColumnMismatch {
			t.Fatalf("%s", c)
		}
		columns = append(columns, c.Column)
	}
	wantEqual(t, columns, []string{"id", "name"}, "columns")
}

// Rust: plan.rs::not_null_field_column_is_a_warning
func TestNotNullFieldColumnIsAWarning(t *testing.T) {
	cat := syncedCatalog()
	price := cat.columns["doc_item"]["price"]
	price.notNull = true
	cat.columns["doc_item"]["price"] = price
	plan := planSchema(registryOf(t, itemTOML), cat)
	if len(plan.conflicts) != 0 || len(plan.steps) != 0 {
		t.Errorf("conflicts %v steps %v", plan.conflicts, plan.steps)
	}
	wantEqual(t, plan.warnings, []Warning{{Kind: WarningColumnNotNullable, Table: "doc_item", Column: "price"}}, "warnings")
}

// Rust: plan.rs::non_table_relation_with_the_table_name_is_a_conflict
func TestNonTableRelationWithTheTableNameIsAConflict(t *testing.T) {
	cat := newCatalog()
	cat.relations["doc_item"] = true
	plan := planSchema(registryOf(t, itemTOML), cat)
	wantEqual(t, plan.conflicts, []Conflict{{Kind: ConflictRelationNameTaken, Name: "doc_item"}}, "conflicts")
	if len(plan.steps) != 0 {
		t.Errorf("steps: %v", stepTexts(plan.steps))
	}
}

func kindOfStep(s Step) string {
	switch s.Kind {
	case StepCreateTable:
		return "table:" + s.DocType
	case StepAddKeys:
		return "keys:" + s.DocType
	case StepAddForeignKeys:
		return "fks:" + s.DocType
	case StepAddColumn:
		return "column:" + s.DocType
	case StepCreateUniqueIndex, StepCreateIndex:
		return "index:" + s.DocType
	case StepRegisterDocType:
		return "register:" + s.DocType
	}
	return "?"
}

// Rust: plan.rs::steps_run_in_phases_across_doctypes
func TestStepsRunInPhasesAcrossDoctypes(t *testing.T) {
	var texts []string
	for _, name := range []string{"item", "item_code_key"} {
		texts = append(texts, fmt.Sprintf(
			"name = \"%s\"\nlabel = \"L\"\n[[fields]]\nname = \"c\"\ntype = \"data\"\nunique = true\n", name))
	}
	// An existing table `doc_old` gains a column and a unique index in the same sync.
	texts = append(texts, "name = \"old\"\nlabel = \"L\"\n[[fields]]\nname = \"c\"\ntype = \"data\"\nunique = true\n")
	registry := registryOf(t, texts...)
	cat := newCatalog()
	columns := standardColumnMap()
	columns["c"] = column{dataType: "character varying(140)"}
	cat.columns["doc_old"] = columns
	cat.unique[colKey{"doc_old", "name"}] = true
	cat.primary[colKey{"doc_old", "id"}] = true
	cat.relations["doc_old"] = true
	cat.tables["doc_old"] = true
	cat.managed["old"] = true
	plan := planSchema(registry, cat)
	var kinds []string
	for _, s := range plan.steps {
		kinds = append(kinds, kindOfStep(s))
	}
	wantEqual(t, kinds, []string{
		"table:item",
		"table:item_code_key",
		"keys:item",
		"keys:item_code_key",
		"index:old",
		"register:item",
		"register:item_code_key",
	}, "kinds")
}

const groupTOML = "name = \"grp\"\nlabel = \"G\"\n"

func orderTOML(linkExtra string) string {
	return "name = \"ord\"\nlabel = \"O\"\n[[fields]]\nname = \"grp\"\ntype = \"link\"\ntarget = \"grp\"\n" + linkExtra + "\n" +
		"[[fields]]\nname = \"prev\"\ntype = \"link\"\ntarget = \"ord\"\nunique = true\n"
}

// linkedCatalog is a catalog where grp and ord exist and match (ord has Link columns grp and prev).
func linkedCatalog() *catalog {
	uuid := column{dataType: "uuid"}
	ord := standardColumnMap()
	ord["grp"] = uuid
	ord["prev"] = uuid
	key := func(table, col string) colKey { return colKey{table, col} }
	fk := func(table string) foreignKey {
		return foreignKey{table: table, column: "id", onDelete: 'r', validated: true}
	}
	cat := newCatalog()
	cat.columns["doc_grp"] = standardColumnMap()
	cat.columns["doc_ord"] = ord
	cat.unique = set(key("doc_grp", "name"), key("doc_ord", "name"), key("doc_ord", "prev"))
	cat.primary = set(key("doc_grp", "id"), key("doc_ord", "id"))
	cat.indexed = set(key("doc_grp", "id"), key("doc_grp", "name"), key("doc_ord", "id"),
		key("doc_ord", "name"), key("doc_ord", "prev"), key("doc_ord", "grp"))
	cat.foreignKeys[key("doc_ord", "grp")] = []foreignKey{fk("doc_grp")}
	cat.foreignKeys[key("doc_ord", "prev")] = []foreignKey{fk("doc_ord")}
	cat.relations = names("doc_grp", "doc_ord")
	cat.tables = names("doc_grp", "doc_ord")
	cat.managed = names("grp", "ord")
	return cat
}

func indexOfContaining(t *testing.T, sql []string, needle string) int {
	t.Helper()
	for i, s := range sql {
		if strings.Contains(s, needle) {
			return i
		}
	}
	t.Fatalf("%s not in %#v", needle, sql)
	return -1
}

// Rust: plan.rs::new_linked_tables_get_foreign_keys_after_all_keys_and_indexes_last
func TestNewLinkedTablesGetForeignKeysAfterAllKeysAndIndexesLast(t *testing.T) {
	// ord links to grp and to itself; grp sorts first, but order does not matter.
	plan := planSchema(registryOf(t, orderTOML(""), groupTOML), newCatalog())
	if len(plan.conflicts) != 0 {
		t.Fatalf("%v", plan.conflicts)
	}
	sql := stepTexts(plan.steps)
	position := func(needle string) int { return indexOfContaining(t, sql, needle) }
	if !(position("ADD PRIMARY KEY") < position("ADD FOREIGN KEY")) {
		t.Error("primary keys after foreign keys")
	}
	if !(position(`CREATE TABLE "doc_ord"`) < position("ADD PRIMARY KEY")) {
		t.Error("tables after primary keys")
	}
	wantEqual(t, sql[position("ADD FOREIGN KEY")],
		`ALTER TABLE "doc_ord" ADD FOREIGN KEY ("grp") REFERENCES "doc_grp" ("id") ON DELETE RESTRICT, `+
			`ADD FOREIGN KEY ("prev") REFERENCES "doc_ord" ("id") ON DELETE RESTRICT;`, "foreign keys")
	// Only the non-unique Link field gets a plain index; prev is covered by its unique key.
	var indexes []string
	for _, s := range sql {
		if strings.HasPrefix(s, "CREATE INDEX") {
			indexes = append(indexes, s)
		}
	}
	wantEqual(t, indexes, []string{`CREATE INDEX ON "doc_ord" ("grp");`}, "indexes")
	if !(position("ADD FOREIGN KEY") < position("CREATE INDEX")) {
		t.Error("indexes before foreign keys")
	}
	if !(position("CREATE INDEX") < position("-- register")) {
		t.Error("registration before indexes")
	}
	// Both new tables have both key steps before any foreign key step.
	lastKeys := -1
	for i, s := range sql {
		if strings.Contains(s, "ADD PRIMARY KEY") {
			lastKeys = i
		}
	}
	if !(lastKeys < position("ADD FOREIGN KEY")) {
		t.Error("a key step after the foreign keys")
	}
}

// Rust: plan.rs::matching_linked_schema_is_a_no_op
func TestMatchingLinkedSchemaIsANoOp(t *testing.T) {
	wantNoPlanOutput(t, planSchema(registryOf(t, orderTOML(""), groupTOML), linkedCatalog()))
}

// Rust: plan.rs::link_column_added_to_an_existing_table_gets_foreign_key_then_index
func TestLinkColumnAddedToAnExistingTableGetsForeignKeyThenIndex(t *testing.T) {
	cat := linkedCatalog()
	key := colKey{"doc_ord", "grp"}
	delete(cat.columns["doc_ord"], "grp")
	delete(cat.foreignKeys, key)
	delete(cat.indexed, key)
	plan := planSchema(registryOf(t, orderTOML(""), groupTOML), cat)
	if len(plan.conflicts) != 0 {
		t.Fatalf("%v", plan.conflicts)
	}
	wantEqual(t, stepTexts(plan.steps), []string{
		`ALTER TABLE "doc_ord" ADD COLUMN "grp" uuid;`,
		`ALTER TABLE "doc_ord" ADD FOREIGN KEY ("grp") REFERENCES "doc_grp" ("id") ON DELETE RESTRICT;`,
		`CREATE INDEX ON "doc_ord" ("grp");`,
	}, "steps")
}

// Rust: plan.rs::missing_index_on_a_link_column_is_a_step_not_a_conflict
func TestMissingIndexOnALinkColumnIsAStepNotAConflict(t *testing.T) {
	cat := linkedCatalog()
	delete(cat.indexed, colKey{"doc_ord", "grp"})
	plan := planSchema(registryOf(t, orderTOML(""), groupTOML), cat)
	if len(plan.conflicts) != 0 {
		t.Errorf("%v", plan.conflicts)
	}
	if len(plan.steps) != 1 || plan.steps[0].Kind != StepCreateIndex || plan.steps[0].Column != "grp" {
		t.Errorf("%v", stepTexts(plan.steps))
	}
}

// Rust: plan.rs::foreign_key_drift_is_a_conflict
func TestForeignKeyDriftIsAConflict(t *testing.T) {
	key := colKey{"doc_ord", "grp"}
	registry := registryOf(t, orderTOML(""), groupTOML)
	describe := func(cat *catalog) []string { return conflictTexts(planSchema(registry, cat).conflicts) }

	dropped := linkedCatalog()
	delete(dropped.foreignKeys, key)
	wantEqual(t, describe(dropped), []string{
		`foreign key on doc_ord.grp is missing in the database but the metadata requires ` +
			`REFERENCES "doc_grp" ("id") ON DELETE RESTRICT`}, "dropped")

	cascade := linkedCatalog()
	cascade.foreignKeys[key][0].onDelete = 'c'
	wantEqual(t, describe(cascade), []string{
		`foreign key on doc_ord.grp is REFERENCES "doc_grp" ("id") ON DELETE CASCADE in the ` +
			`database but the metadata requires REFERENCES "doc_grp" ("id") ON DELETE RESTRICT`}, "cascade")

	noAction := linkedCatalog()
	noAction.foreignKeys[key][0].onDelete = 'a'
	if len(describe(noAction)) != 1 {
		t.Error("no action")
	}
	wrongTable := linkedCatalog()
	wrongTable.foreignKeys[key][0].table = "doc_ord"
	if len(describe(wrongTable)) != 1 {
		t.Error("wrong table")
	}
	wrongColumn := linkedCatalog()
	wrongColumn.foreignKeys[key][0].column = "name"
	if len(describe(wrongColumn)) != 1 {
		t.Error("wrong column")
	}
}

// Rust: plan.rs::foreign_key_on_a_non_link_column_is_a_conflict
func TestForeignKeyOnANonLinkColumnIsAConflict(t *testing.T) {
	cat := syncedCatalog()
	cat.foreignKeys[colKey{"doc_item", "price"}] = []foreignKey{{table: "doc_other", column: "id", onDelete: 'a', validated: true}}
	plan := planSchema(registryOf(t, itemTOML), cat)
	if len(plan.conflicts) != 1 || plan.conflicts[0].Kind != ConflictForeignKeyMismatch ||
		plan.conflicts[0].Column != "price" || plan.conflicts[0].Expected != "no foreign key" {
		t.Errorf("%v", plan.conflicts)
	}
}

const (
	parentTOML = "name = \"ord\"\nlabel = \"O\"\n[[fields]]\nname = \"title\"\ntype = \"data\"\n" +
		"[[fields]]\nname = \"lines\"\ntype = \"table\"\ntarget = \"line\"\n"
	lineTOML = "name = \"line\"\nlabel = \"L\"\nis_child = true\n" +
		"[[fields]]\nname = \"qty\"\ntype = \"int\"\n" +
		"[[fields]]\nname = \"grp\"\ntype = \"link\"\ntarget = \"grp\"\n"
)

func childRegistry(t *testing.T) *core.Registry {
	return registryOf(t, parentTOML, lineTOML, groupTOML)
}

// Rust: plan.rs::new_parent_and_child_tables
func TestNewParentAndChildTables(t *testing.T) {
	plan := planSchema(childRegistry(t), newCatalog())
	if len(plan.conflicts) != 0 || len(plan.warnings) != 0 {
		t.Fatalf("%v %v", plan.conflicts, plan.warnings)
	}
	sql := stepTexts(plan.steps)
	position := func(needle string) int { return indexOfContaining(t, sql, needle) }
	wantEqual(t, sql[position(`CREATE TABLE "doc_line"`)], "CREATE TABLE \"doc_line\" (\n    "+
		"\"id\" uuid NOT NULL,\n    "+
		"\"parent_id\" uuid NOT NULL,\n    "+
		"\"idx\" integer NOT NULL,\n    "+
		"\"qty\" bigint,\n    "+
		"\"grp\" uuid\n);", "child table")
	// The Table field is not a column of the parent.
	parentTable := sql[position(`CREATE TABLE "doc_ord"`)]
	if !strings.HasSuffix(parentTable, "\"title\" character varying(140)\n);") {
		t.Errorf("%s", parentTable)
	}
	// A child has a primary key only: no name, no unique constraints.
	if !slices.Contains(sql, `ALTER TABLE "doc_line" ADD PRIMARY KEY ("id");`) {
		t.Errorf("%#v", sql)
	}
	wantEqual(t, sql[position(`ALTER TABLE "doc_line" ADD FOREIGN KEY`)],
		`ALTER TABLE "doc_line" ADD FOREIGN KEY ("parent_id") REFERENCES "doc_ord" ("id") ON DELETE CASCADE, `+
			`ADD FOREIGN KEY ("grp") REFERENCES "doc_grp" ("id") ON DELETE RESTRICT;`, "child keys")
	if !(position(`ADD PRIMARY KEY ("id"), ADD UNIQUE ("name")`) < position("ADD FOREIGN KEY")) {
		t.Error("foreign keys before the keys")
	}
	primaries := 0
	for _, s := range sql {
		if strings.Contains(s, "ADD PRIMARY KEY") {
			primaries++
		}
	}
	if primaries != 3 {
		t.Errorf("%d primary key steps: %#v", primaries, sql)
	}
	var indexes []string
	for _, s := range sql {
		if strings.HasPrefix(s, "CREATE INDEX") {
			indexes = append(indexes, s)
		}
	}
	wantEqual(t, indexes, []string{
		`CREATE INDEX ON "doc_line" ("parent_id");`,
		`CREATE INDEX ON "doc_line" ("grp");`,
	}, "indexes")
	if !(position("ADD FOREIGN KEY") < position("CREATE INDEX")) {
		t.Error("indexes before foreign keys")
	}
	registered := 0
	for _, s := range sql {
		if strings.HasPrefix(s, "-- register") {
			registered++
		}
	}
	if registered != 3 {
		t.Errorf("%d registrations", registered)
	}
}

// childCatalog is ord, grp and the child line, which exist and match.
func childCatalog() *catalog {
	cat := linkedCatalog()
	key := func(table, col string) colKey { return colKey{table, col} }
	// linkedCatalog has doc_ord with Link columns; replace it with the plain parent.
	ord := standardColumnMap()
	ord["title"] = column{dataType: "character varying(140)"}
	cat.columns["doc_ord"] = ord
	for _, col := range []string{"grp", "prev"} {
		delete(cat.unique, key("doc_ord", col))
		delete(cat.indexed, key("doc_ord", col))
		delete(cat.foreignKeys, key("doc_ord", col))
	}
	col := func(t string, notNull bool) column { return column{dataType: t, notNull: notNull} }
	cat.columns["doc_line"] = map[string]column{
		"id":        col("uuid", true),
		"parent_id": col("uuid", true),
		"idx":       col("integer", true),
		"qty":       col("bigint", false),
		"grp":       col("uuid", false),
	}
	cat.primary[key("doc_line", "id")] = true
	for _, k := range []colKey{key("doc_line", "id"), key("doc_line", "parent_id"), key("doc_line", "grp")} {
		cat.indexed[k] = true
	}
	fk := func(table string, onDelete byte) foreignKey {
		return foreignKey{table: table, column: "id", onDelete: onDelete, validated: true}
	}
	cat.foreignKeys[key("doc_line", "parent_id")] = []foreignKey{fk("doc_ord", 'c')}
	cat.foreignKeys[key("doc_line", "grp")] = []foreignKey{fk("doc_grp", 'r')}
	cat.relations["doc_line"] = true
	cat.tables["doc_line"] = true
	cat.managed["line"] = true
	return cat
}

// Rust: plan.rs::matching_child_schema_is_a_no_op
func TestMatchingChildSchemaIsANoOp(t *testing.T) {
	wantNoPlanOutput(t, planSchema(childRegistry(t), childCatalog()))
}

// Rust: plan.rs::parent_key_drift_is_a_conflict
func TestParentKeyDriftIsAConflict(t *testing.T) {
	key := colKey{"doc_line", "parent_id"}
	registry := childRegistry(t)
	describe := func(cat *catalog) []string { return conflictTexts(planSchema(registry, cat).conflicts) }
	restrict := childCatalog()
	restrict.foreignKeys[key][0].onDelete = 'r'
	wantEqual(t, describe(restrict), []string{
		`foreign key on doc_line.parent_id is REFERENCES "doc_ord" ("id") ON DELETE RESTRICT in the ` +
			`database but the metadata requires REFERENCES "doc_ord" ("id") ON DELETE CASCADE`}, "restrict")
	dropped := childCatalog()
	delete(dropped.foreignKeys, key)
	wantEqual(t, describe(dropped), []string{
		`foreign key on doc_line.parent_id is missing in the database but the metadata requires ` +
			`REFERENCES "doc_ord" ("id") ON DELETE CASCADE`}, "dropped")
	notValidated := childCatalog()
	notValidated.foreignKeys[key][0].validated = false
	if len(describe(notValidated)) != 1 {
		t.Error("not validated")
	}
	wrongTable := childCatalog()
	wrongTable.foreignKeys[key][0].table = "doc_grp"
	if len(describe(wrongTable)) != 1 {
		t.Error("wrong table")
	}
}

// Rust: plan.rs::missing_parent_index_is_a_step_not_a_conflict
func TestMissingParentIndexIsAStepNotAConflict(t *testing.T) {
	cat := childCatalog()
	delete(cat.indexed, colKey{"doc_line", "parent_id"})
	plan := planSchema(childRegistry(t), cat)
	if len(plan.conflicts) != 0 {
		t.Errorf("%v", plan.conflicts)
	}
	if len(plan.steps) != 1 || plan.steps[0].Kind != StepCreateIndex || plan.steps[0].SQL != `CREATE INDEX ON "doc_line" ("parent_id")` {
		t.Errorf("%v", stepTexts(plan.steps))
	}
}

// Rust: plan.rs::child_standard_column_drift_and_extra_columns
func TestChildStandardColumnDriftAndExtraColumns(t *testing.T) {
	cat := childCatalog()
	columns := cat.columns["doc_line"]
	columns["idx"] = column{dataType: "bigint", notNull: true}
	delete(columns, "parent_id")
	// A leftover name column of a former non-child table is only a warning.
	columns["name"] = column{dataType: "text", notNull: true}
	plan := planSchema(childRegistry(t), cat)
	wantEqual(t, conflictTexts(plan.conflicts), []string{
		"column doc_line.parent_id is missing in the database but the metadata requires uuid NOT NULL",
		"column doc_line.idx is bigint NOT NULL in the database but the metadata requires integer NOT NULL",
	}, "conflicts")
	wantEqual(t, plan.warnings, []Warning{{Kind: WarningColumnNotInMetadata, Table: "doc_line", Column: "name"}}, "warnings")
}

// Rust: plan.rs::switching_between_child_and_not_child_is_a_conflict
func TestSwitchingBetweenChildAndNotChildIsAConflict(t *testing.T) {
	// item was a plain DocType and is now a child.
	registry := registryOf(t,
		"name = \"item\"\nlabel = \"I\"\nis_child = true\n",
		"name = \"holder\"\nlabel = \"H\"\n[[fields]]\nname = \"rows\"\ntype = \"table\"\ntarget = \"item\"\n")
	planNow := planSchema(registry, syncedCatalog())
	wantEqual(t, conflictTexts(planNow.conflicts), []string{
		"column doc_item.parent_id is missing in the database but the metadata requires uuid NOT NULL",
		"column doc_item.idx is missing in the database but the metadata requires integer NOT NULL",
	}, "to child")
	// And the other way round: a child table that becomes a normal DocType.
	registry = registryOf(t, "name = \"line\"\nlabel = \"L\"\n")
	conflicts := conflictTexts(planSchema(registry, childCatalog()).conflicts)
	if len(conflicts) != 6 {
		t.Errorf("%d conflicts: %v", len(conflicts), conflicts)
	}
	if !strings.Contains(conflicts[0], "doc_line.name is missing") {
		t.Errorf("%v", conflicts)
	}
}

// Rust: plan.rs::a_table_field_never_gets_a_column
func TestATableFieldNeverGetsAColumn(t *testing.T) {
	// A leftover column with the name of a Table field is reported like any unknown column.
	cat := childCatalog()
	cat.columns["doc_ord"]["lines"] = column{dataType: "text"}
	plan := planSchema(childRegistry(t), cat)
	if len(plan.steps) != 0 || len(plan.conflicts) != 0 {
		t.Errorf("%v %v", stepTexts(plan.steps), plan.conflicts)
	}
	wantEqual(t, plan.warnings, []Warning{{Kind: WarningColumnNotInMetadata, Table: "doc_ord", Column: "lines"}}, "warnings")
}

// Rust: plan.rs::non_table_relation_over_existing_columns_is_a_name_clash
func TestNonTableRelationOverExistingColumnsIsANameClash(t *testing.T) {
	// A view has columns in the catalog but is not an ordinary table.
	cat := syncedCatalog()
	cat.tables = names()
	plan := planSchema(registryOf(t, itemTOML), cat)
	wantEqual(t, plan.conflicts, []Conflict{{Kind: ConflictRelationNameTaken, Name: "doc_item"}}, "conflicts")
}

const memoTOML = "name = \"memo\"\nlabel = \"M\"\nsubmittable = true\n"

// memoCatalog is a catalog of a memo table whose columns are the standard ones plus extra.
func memoCatalog(extra ...extraColumn) *catalog {
	columns := standardColumnMap()
	for _, e := range extra {
		columns[e.name] = column{dataType: e.dataType, notNull: e.notNull}
	}
	cat := newCatalog()
	cat.columns["doc_memo"] = columns
	cat.unique = set(colKey{"doc_memo", "name"})
	cat.primary = set(colKey{"doc_memo", "id"})
	cat.relations = names("doc_memo")
	cat.tables = names("doc_memo")
	cat.managed = names("memo")
	return cat
}

// Rust: plan.rs::a_new_submittable_table_has_the_docstatus_columns_after_the_standard_ones
func TestANewSubmittableTableHasTheDocstatusColumnsAfterTheStandardOnes(t *testing.T) {
	plan := planSchema(registryOf(t, memoTOML), newCatalog())
	sql := plan.steps[0].SQL
	if plan.steps[0].Kind != StepCreateTable ||
		!strings.Contains(sql, "\"version\" bigint NOT NULL DEFAULT 1,\n    \"docstatus\" smallint NOT NULL DEFAULT 0,\n    \"amended_from\" text\n)") {
		t.Errorf("%s", sql)
	}
}

// Rust: plan.rs::missing_docstatus_columns_are_added_and_present_ones_are_a_no_op
func TestMissingDocstatusColumnsAreAddedAndPresentOnesAreANoOp(t *testing.T) {
	missing := planSchema(registryOf(t, memoTOML), memoCatalog())
	if len(missing.conflicts) != 0 {
		t.Errorf("%v", missing.conflicts)
	}
	var columns []string
	for _, s := range missing.steps {
		if s.Kind == StepAddColumn {
			columns = append(columns, s.Column)
		}
	}
	wantEqual(t, columns, []string{"docstatus", "amended_from"}, "columns")

	present := memoCatalog(extraColumn{"docstatus", "smallint", true}, extraColumn{"amended_from", "text", false})
	wantNoPlanOutput(t, planSchema(registryOf(t, memoTOML), present))
}

// Rust: plan.rs::docstatus_column_drift_is_a_conflict_and_turning_submittable_off_a_warning
func TestDocstatusColumnDriftIsAConflictAndTurningSubmittableOffAWarning(t *testing.T) {
	drifted := memoCatalog(extraColumn{"docstatus", "bigint", true}, extraColumn{"amended_from", "text", true})
	drift := planSchema(registryOf(t, memoTOML), drifted)
	if len(drift.conflicts) != 2 {
		t.Errorf("%v", drift.conflicts)
	}
	kept := memoCatalog(extraColumn{"docstatus", "smallint", true}, extraColumn{"amended_from", "text", false})
	off := planSchema(registryOf(t, "name = \"memo\"\nlabel = \"M\"\n"), kept)
	if len(off.steps) != 0 || len(off.conflicts) != 0 {
		t.Errorf("%v %v", off.steps, off.conflicts)
	}
	if len(off.warnings) != 2 {
		t.Errorf("%v", off.warnings)
	}
}

const flowTOML = "name = \"memo\"\nlabel = \"M\"\nsubmittable = true\n" +
	"[workflow]\n[[workflow.states]]\nname = \"Open\"\ndocstatus = 0\n"

// Rust: plan.rs::a_workflow_adds_a_nullable_state_column_after_the_docstatus_columns
func TestAWorkflowAddsANullableStateColumnAfterTheDocstatusColumns(t *testing.T) {
	planNew := planSchema(registryOf(t, flowTOML), newCatalog())
	if planNew.steps[0].Kind != StepCreateTable ||
		!strings.Contains(planNew.steps[0].SQL, "\"amended_from\" text,\n    \"workflow_state\" text\n)") {
		t.Errorf("%s", planNew.steps[0].SQL)
	}
	// A DocType that gets a workflow later: one nullable column, no default.
	status := []extraColumn{{"docstatus", "smallint", true}, {"amended_from", "text", false}}
	added := planSchema(registryOf(t, flowTOML), memoCatalog(status...))
	if len(added.conflicts) != 0 || len(added.warnings) != 0 {
		t.Errorf("%v %v", added.conflicts, added.warnings)
	}
	if len(added.steps) != 1 || added.steps[0].Kind != StepAddColumn {
		t.Fatalf("expected one AddColumn, got %v", added.steps)
	}
	wantEqual(t, added.steps[0].Column, "workflow_state", "column")
	wantEqual(t, added.steps[0].SQL, `ALTER TABLE "doc_memo" ADD COLUMN "workflow_state" text`, "sql")
	// Present: nothing to do. Drift (another type, NOT NULL) is a conflict.
	withState := append(slices.Clone(status), extraColumn{"workflow_state", "text", false})
	wantNoPlanOutput(t, planSchema(registryOf(t, flowTOML), memoCatalog(withState...)))
	for _, drift := range []extraColumn{{"workflow_state", "bigint", false}, {"workflow_state", "text", true}} {
		drifted := planSchema(registryOf(t, flowTOML), memoCatalog(append(slices.Clone(status), drift)...))
		if len(drifted.conflicts) != 1 {
			t.Errorf("%v: %v", drift, drifted.conflicts)
		}
	}
	// Removing the workflow keeps the column: a warning, like turning submittable off.
	off := planSchema(registryOf(t, memoTOML), memoCatalog(withState...))
	if len(off.steps) != 0 || len(off.conflicts) != 0 {
		t.Errorf("%v %v", off.steps, off.conflicts)
	}
	wantEqual(t, off.warnings, []Warning{{Kind: WarningColumnNotInMetadata, Table: "doc_memo", Column: "workflow_state"}}, "warnings")
}

// Not in Rust: the Display texts of Step, Conflict and Warning are what the CLI prints, and the
// foreign-key description is part of a conflict text.
func TestDisplayTexts(t *testing.T) {
	wantEqual(t, Step{Kind: StepRegisterDocType, DocType: "item"}.String(), `-- register DocType "item" in fb_doctype`, "register")
	wantEqual(t, Step{Kind: StepCreateIndex, SQL: "CREATE INDEX ON t (c)"}.String(), "CREATE INDEX ON t (c);", "index")
	wantEqual(t, Conflict{Kind: ConflictUnmanagedTable, Table: "doc_x"}.String(),
		"table doc_x already exists but is not managed by Forgebase (not adopted)", "unmanaged")
	wantEqual(t, Conflict{Kind: ConflictRelationNameTaken, Name: "doc_x"}.String(),
		"cannot create table doc_x: a relation or type with that name already exists", "taken")
	wantEqual(t, Warning{Kind: WarningColumnNotInMetadata, Table: "t", Column: "c"}.String(), "column t.c is not in the metadata; kept", "w1")
	wantEqual(t, Warning{Kind: WarningUniqueRemoved, Table: "t", Column: "c"}.String(),
		"field t.c is no longer unique in the metadata; unique index kept", "w2")
	wantEqual(t, Warning{Kind: WarningColumnNotNullable, Table: "t", Column: "c"}.String(),
		"column t.c is NOT NULL in the database but fields are nullable; left as is", "w3")
	wantEqual(t, Warning{Kind: WarningDocTypeNotInRegistry, DocType: "d"}.String(),
		`DocType "d" is registered in the database but not in the metadata; table kept`, "w4")
	other := foreignKey{table: "other.doc_x", column: "id", onDelete: 'n', validated: true}
	wantEqual(t, other.describe(), `REFERENCES "other"."doc_x" ("id") ON DELETE SET NULL`, "qualified table")
}

// Not in Rust: the rendering of a field added by hand (custom fields, M10.1) is the sync's own.
func TestFieldSteps(t *testing.T) {
	registry := registryOf(t, "name = \"o\"\nlabel = \"O\"\n[[fields]]\nname = \"p\"\ntype = \"link\"\ntarget = \"o\"\n"+
		"[[fields]]\nname = \"q\"\ntype = \"data\"\nunique = true\n")
	o := registry.Get("o")
	wantEqual(t, stepTexts(fieldSteps("o", &o.Fields[0])), []string{
		`ALTER TABLE "doc_o" ADD COLUMN "p" uuid;`,
		`ALTER TABLE "doc_o" ADD FOREIGN KEY ("p") REFERENCES "doc_o" ("id") ON DELETE RESTRICT;`,
		`CREATE INDEX ON "doc_o" ("p");`,
	}, "link")
	wantEqual(t, stepTexts(fieldSteps("o", &o.Fields[1])), []string{
		`ALTER TABLE "doc_o" ADD COLUMN "q" character varying(140);`,
		`CREATE UNIQUE INDEX ON "doc_o" ("q");`,
	}, "unique")
}
