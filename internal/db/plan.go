package db

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Pure schema planning: compares DocType metadata with a snapshot of the live schema.
//
// No I/O here; Sync loads the snapshot and executes the plan. Only additive steps are ever
// generated. Generated SQL contains validated, quoted identifiers and numeric type parameters
// only, never values. The statements are byte-identical to the Rust implementation's, because the
// two must agree on an existing schema (the constraints are unnamed on purpose: PostgreSQL picks
// the names).

const timestamptz = "timestamp with time zone"

type standardColumn struct{ name, dataType string }

// standardColumns of every (non-child) DocType table. All are NOT NULL.
var documentStandardColumns = []standardColumn{
	{"id", "uuid"},
	{"name", "text"},
	{"owner", "text"},
	{"created_at", timestamptz},
	{"modified_at", timestamptz},
	{"modified_by", "text"},
	{"version", "bigint"},
}

// childStandardColumns of a child DocType table; all NOT NULL.
var childStandardColumns = []standardColumn{{"id", "uuid"}, {"parent_id", "uuid"}, {"idx", "integer"}}

type extraColumn struct {
	name, dataType string
	notNull        bool
}

// submittableColumns are the extra standard columns of a submittable DocType. docstatus is 0
// draft, 1 submitted, 2 cancelled; amended_from is the name of the cancelled document an
// amendment replaces.
var submittableColumns = []extraColumn{
	{"docstatus", "smallint", true},
	{"amended_from", "text", false},
}

// workflowColumns is the extra standard column of a DocType with a workflow (M8.1): the name of the
// state the document is in, nullable (a document that existed before the workflow gets its state
// when migrate backfills it).
var workflowColumns = []extraColumn{{core.WorkflowColumn, "text", false}}

// extraColumns are the framework columns of doctype after the standard ones: the submittable
// columns of a submittable DocType, then the workflow column of a DocType with a workflow.
func extraColumns(doctype *core.DocType) []extraColumn {
	var out []extraColumn
	if doctype.Submittable {
		out = append(out, submittableColumns...)
	}
	if doctype.Workflow != nil {
		out = append(out, workflowColumns...)
	}
	return out
}

// standardColumnsOf are the standard columns of the table of doctype: a child's rows have no name,
// owner, version or timestamps (they are saved with their parent) but point at the parent and have
// a position.
func standardColumnsOf(doctype *core.DocType) []standardColumn {
	if doctype.IsChild {
		return childStandardColumns
	}
	return documentStandardColumns
}

// StepKind is the variant of a Step.
type StepKind int

const (
	// StepCreateTable creates the table of a new DocType: columns only, no keys (see StepAddKeys).
	StepCreateTable StepKind = iota + 1
	// StepAddKeys adds the primary key and unique constraints of a new table. It runs after every
	// new table of the sync exists, so the names PostgreSQL picks for them cannot clash with a table
	// name.
	StepAddKeys
	// StepAddForeignKeys adds the foreign keys of a new table (the parent_id key of a child table
	// and the keys of its Link fields) or of Link columns added to an existing table. It runs after
	// every new table has its primary key, so links between new tables (also cycles and self-links)
	// and a child created together with its parent work in one sync.
	StepAddForeignKeys
	// StepAddColumn adds a nullable column for a field added to an existing DocType, or the
	// docstatus (NOT NULL DEFAULT 0, existing documents become drafts) and amended_from columns of a
	// DocType that became submittable, or the nullable workflow_state column of a DocType that got a
	// workflow (migrate then sets the state of its existing documents).
	StepAddColumn
	// StepCreateUniqueIndex adds a unique index for a unique field on an existing table.
	StepCreateUniqueIndex
	// StepCreateIndex adds a non-unique index on a Link column or on parent_id of a child table
	// (PostgreSQL does not index foreign keys itself).
	StepCreateIndex
	// StepRegisterDocType records the DocType in fb_doctype (executed with bound parameters).
	StepRegisterDocType
)

// Step is one change the sync would make. Steps are executed in order. DocType is always set;
// Column for StepAddColumn, StepCreateUniqueIndex and StepCreateIndex; SQL for every kind but
// StepRegisterDocType.
type Step struct {
	Kind    StepKind
	DocType string
	Column  string
	SQL     string
}

// String is the SQL of the step, for dry-run output. StepRegisterDocType is shown as a comment.
func (s Step) String() string {
	if s.Kind == StepRegisterDocType {
		return fmt.Sprintf("-- register DocType %q in fb_doctype", s.DocType)
	}
	return s.SQL + ";"
}

// ConflictKind is the variant of a Conflict.
type ConflictKind int

const (
	// ConflictUnmanagedTable: the table doc_<name> exists but was not created by Forgebase (Table).
	ConflictUnmanagedTable ConflictKind = iota + 1
	// ConflictRelationNameTaken: something other than a usable table (index, view, sequence, type, a
	// table with no visible columns, ...) already has the name doc_<name>, so the table cannot be
	// created (Name is the wanted table name).
	ConflictRelationNameTaken
	// ConflictForeignKeyMismatch: a foreign key does not match the metadata: a Link column without
	// the foreign key to the target's id, a foreign key to another table or column, a delete rule
	// other than RESTRICT, a foreign key on a column that is not a Link; for a child table the same
	// about parent_id, whose key must reference the parent's id with ON DELETE CASCADE (Table,
	// Column, Expected, Actual; Actual is "missing" if absent).
	ConflictForeignKeyMismatch
	// ConflictColumnMismatch: a column exists with a different type (or, for standard columns, a
	// different nullability, uniqueness or primary key; the same for the docstatus and amended_from
	// columns of a submittable DocType and the workflow_state column of a DocType with a workflow),
	// or a standard column is missing (Table, Column, Expected, Actual; Actual is "missing" if
	// absent).
	ConflictColumnMismatch
)

// Conflict is a difference that cannot be applied additively. Any conflict refuses the whole sync.
// Only the members that belong to Kind are set.
type Conflict struct {
	Kind             ConflictKind
	Table, Column    string
	Name             string
	Expected, Actual string
}

// String is the Rust Display text.
func (c Conflict) String() string {
	switch c.Kind {
	case ConflictUnmanagedTable:
		return fmt.Sprintf("table %s already exists but is not managed by Forgebase (not adopted)", c.Table)
	case ConflictRelationNameTaken:
		return fmt.Sprintf("cannot create table %s: a relation or type with that name already exists", c.Name)
	case ConflictColumnMismatch:
		return fmt.Sprintf("column %s.%s is %s in the database but the metadata requires %s",
			c.Table, c.Column, c.Actual, c.Expected)
	case ConflictForeignKeyMismatch:
		return fmt.Sprintf("foreign key on %s.%s is %s in the database but the metadata requires %s",
			c.Table, c.Column, c.Actual, c.Expected)
	}
	return "unknown conflict"
}

// WarningKind is the variant of a Warning.
type WarningKind int

const (
	// WarningColumnNotInMetadata: a column exists that no field describes (field removed); the
	// column is kept (Table, Column).
	WarningColumnNotInMetadata WarningKind = iota + 1
	// WarningUniqueRemoved: a field is no longer unique but its unique index remains (Table,
	// Column).
	WarningUniqueRemoved
	// WarningColumnNotNullable: a field column is NOT NULL in the database, but fields are nullable
	// (required is enforced by the application), so inserts may fail. Reported, not changed (Table,
	// Column).
	WarningColumnNotNullable
	// WarningDocTypeNotInRegistry: a DocType is recorded in fb_doctype but not in the registry; its
	// table is kept (DocType).
	WarningDocTypeNotInRegistry
)

// Warning is something noticed but intentionally not changed. Only the members that belong to Kind
// are set.
type Warning struct {
	Kind          WarningKind
	Table, Column string
	DocType       string
}

// String is the Rust Display text.
func (w Warning) String() string {
	switch w.Kind {
	case WarningColumnNotInMetadata:
		return fmt.Sprintf("column %s.%s is not in the metadata; kept", w.Table, w.Column)
	case WarningUniqueRemoved:
		return fmt.Sprintf("field %s.%s is no longer unique in the metadata; unique index kept", w.Table, w.Column)
	case WarningColumnNotNullable:
		return fmt.Sprintf("column %s.%s is NOT NULL in the database but fields are nullable; left as is", w.Table, w.Column)
	case WarningDocTypeNotInRegistry:
		return fmt.Sprintf("DocType %q is registered in the database but not in the metadata; table kept", w.DocType)
	}
	return "unknown warning"
}

// column is a column as found in the live schema.
type column struct {
	// dataType is rendered like the DDL type: bigint, character varying(140), numeric(12,2).
	dataType string
	notNull  bool
}

// foreignKey is a single-column foreign key as found in the live schema.
type foreignKey struct {
	// table is the referenced table (schema.table when it is not in the current schema).
	table  string
	column string
	// onDelete is pg_constraint.confdeltype: 'a' no action, 'r' restrict, 'c' cascade, 'n' set null,
	// 'd' set default.
	onDelete byte
	// validated is pg_constraint.convalidated: false for a NOT VALID constraint, whose existing rows
	// were never checked.
	validated bool
}

func (fk foreignKey) describe() string {
	var rule string
	switch fk.onDelete {
	case 'a':
		rule = "NO ACTION"
	case 'r':
		rule = "RESTRICT"
	case 'c':
		rule = "CASCADE"
	case 'n':
		rule = "SET NULL"
	case 'd':
		rule = "SET DEFAULT"
	default:
		rule = "an unknown rule"
	}
	table := QuoteIdent(fk.table)
	if schema, name, ok := strings.Cut(fk.table, "."); ok {
		table = QuoteIdent(schema) + "." + QuoteIdent(name)
	}
	validated := ""
	if !fk.validated {
		validated = " (not validated)"
	}
	return fmt.Sprintf("REFERENCES %s (%s) ON DELETE %s%s", table, QuoteIdent(fk.column), rule, validated)
}

// expectedForeignKey is the foreign key a Link field to target must have.
func expectedForeignKey(target string) foreignKey {
	return foreignKey{table: core.TableName(target), column: "id", onDelete: 'r', validated: true}
}

// expectedParentKey is the foreign key parent_id of a child table must have: deleting a parent
// deletes its rows.
func expectedParentKey(parent string) foreignKey {
	fk := expectedForeignKey(parent)
	fk.onDelete = 'c'
	return fk
}

// colKey is a (table, column) pair.
type colKey struct{ table, column string }

// catalog is a snapshot of the live schema, limited to the tables of the registry.
type catalog struct {
	// columns: table -> column -> definition. A table without columns is absent.
	columns map[string]map[string]column
	// unique: (table, column) pairs covered by a valid single-column unique index (not a primary key).
	unique map[colKey]bool
	// primary: (table, column) pairs that are a single-column primary key.
	primary map[colKey]bool
	// indexed: (table, column) pairs covered by a valid, single-column, non-partial index of any kind
	// (including unique indexes and primary keys).
	indexed map[colKey]bool
	// foreignKeys: (table, column) -> the single-column foreign keys on that column.
	foreignKeys map[colKey][]foreignKey
	// relations are the wanted table names already taken in the schema by a relation of any kind
	// (table, index, view, sequence, ...) or by a standalone type (domain, enum, ...).
	relations map[string]bool
	// tables is the subset of relations that are ordinary (or partitioned) tables.
	tables map[string]bool
	// managed are the DocType names recorded in fb_doctype.
	managed map[string]bool
}

func newCatalog() *catalog {
	return &catalog{
		columns:     map[string]map[string]column{},
		unique:      map[colKey]bool{},
		primary:     map[colKey]bool{},
		indexed:     map[colKey]bool{},
		foreignKeys: map[colKey][]foreignKey{},
		relations:   map[string]bool{},
		tables:      map[string]bool{},
		managed:     map[string]bool{},
	}
}

// schemaPlan is the outcome of comparing the registry with the live schema.
type schemaPlan struct {
	steps     []Step
	conflicts []Conflict
	warnings  []Warning
}

// pgType is the PostgreSQL type of a field column. Also what information_schema reports once
// rendered.
func pgType(t core.FieldType) string {
	switch t.Kind {
	case core.FieldData:
		return fmt.Sprintf("character varying(%d)", t.MaxLength)
	case core.FieldText, core.FieldSelect:
		return "text"
	case core.FieldInt:
		return "bigint"
	case core.FieldDecimal:
		return fmt.Sprintf("numeric(%d,%d)", t.Precision, t.Scale)
	case core.FieldCheck:
		return "boolean"
	case core.FieldDate:
		return "date"
	case core.FieldDatetime:
		return timestamptz
	case core.FieldLink:
		return "uuid"
	default:
		// FieldTable: no column, the rows live in the child's table. Callers skip these fields.
		return "uuid"
	}
}

func standardColumnSQL(name, dataType string) string {
	constraint := "NOT NULL"
	if name == "version" {
		constraint = "NOT NULL DEFAULT 1"
	}
	return fmt.Sprintf("%s %s %s", QuoteIdent(name), dataType, constraint)
}

// extraColumnSQL is `"docstatus" smallint NOT NULL DEFAULT 0`, `"amended_from" text` or
// `"workflow_state" text`.
func extraColumnSQL(c extraColumn) string {
	constraint := ""
	switch {
	case c.name == "docstatus":
		constraint = " NOT NULL DEFAULT 0"
	case c.notNull:
		constraint = " NOT NULL"
	}
	return fmt.Sprintf("%s %s%s", QuoteIdent(c.name), c.dataType, constraint)
}

func createTableSQL(doctype *core.DocType, table string) string {
	var lines []string
	for _, c := range standardColumnsOf(doctype) {
		lines = append(lines, standardColumnSQL(c.name, c.dataType))
	}
	for _, c := range extraColumns(doctype) {
		lines = append(lines, extraColumnSQL(c))
	}
	for _, f := range doctype.ColumnFields() {
		lines = append(lines, fmt.Sprintf("%s %s", QuoteIdent(f.Name), pgType(f.Type)))
	}
	return fmt.Sprintf("CREATE TABLE %s (\n    %s\n)", QuoteIdent(table), strings.Join(lines, ",\n    "))
}

// addKeysSQL: keys of a new table, added once all new tables exist. Unnamed on purpose: PostgreSQL
// picks free names (<table>_<column>_key, adjusted if taken or too long). Because every table of
// the sync already exists, none of those names can clash with a table name.
func addKeysSQL(doctype *core.DocType, table string) string {
	clauses := []string{fmt.Sprintf("ADD PRIMARY KEY (%s)", QuoteIdent("id"))}
	// A child's rows have no name, and its fields cannot be unique.
	if !doctype.IsChild {
		clauses = append(clauses, fmt.Sprintf("ADD UNIQUE (%s)", QuoteIdent("name")))
	}
	for _, f := range doctype.ColumnFields() {
		if f.Unique {
			clauses = append(clauses, fmt.Sprintf("ADD UNIQUE (%s)", QuoteIdent(f.Name)))
		}
	}
	return fmt.Sprintf("ALTER TABLE %s %s", QuoteIdent(table), strings.Join(clauses, ", "))
}

// foreignKeyClause is a foreign key to add: column, target DocType (its table's id), delete rule.
type foreignKeyClause struct{ column, target, rule string }

// addForeignKeysSQL puts foreign keys in one statement. Unnamed on purpose, like the keys. The rule
// is RESTRICT for a Link (a document that is still linked cannot be deleted) and CASCADE for
// parent_id.
func addForeignKeysSQL(table string, keys []foreignKeyClause) string {
	clauses := make([]string, len(keys))
	for i, k := range keys {
		clauses[i] = fmt.Sprintf("ADD FOREIGN KEY (%s) REFERENCES %s (%s) ON DELETE %s",
			QuoteIdent(k.column), QuoteIdent(core.TableName(k.target)), QuoteIdent("id"), k.rule)
	}
	return fmt.Sprintf("ALTER TABLE %s %s", QuoteIdent(table), strings.Join(clauses, ", "))
}

func linkIndexSQL(table, column string) string {
	return fmt.Sprintf("CREATE INDEX ON %s (%s)", QuoteIdent(table), QuoteIdent(column))
}

type linkField struct {
	field  *core.DocField
	target string
}

// linkFields are the Link fields of a DocType with their targets.
func linkFields(doctype *core.DocType) []linkField {
	var out []linkField
	for i := range doctype.Fields {
		f := &doctype.Fields[i]
		if f.Type.Kind == core.FieldLink {
			out = append(out, linkField{f, f.Type.Target})
		}
	}
	return out
}

func addColumnSQL(table string, field *core.DocField) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", QuoteIdent(table), QuoteIdent(field.Name), pgType(field.Type))
}

func uniqueIndexSQL(table, column string) string {
	return fmt.Sprintf("CREATE UNIQUE INDEX ON %s (%s)", QuoteIdent(table), QuoteIdent(column))
}

// fieldSteps are the steps that give one field added to an existing table its column (M10.1): the
// column, the foreign key and index of a Link, and the unique index of a unique field, in the order
// the sync applies them. Built from the same statements the sync uses, so a field added by hand and
// a field added by migrate end up with the same schema. (Used by custom fields, P7.)
func fieldSteps(doctype string, field *core.DocField) []Step {
	table := core.TableName(doctype)
	steps := []Step{{Kind: StepAddColumn, DocType: doctype, Column: field.Name, SQL: addColumnSQL(table, field)}}
	isLink := field.Type.Kind == core.FieldLink
	if isLink {
		steps = append(steps, Step{Kind: StepAddForeignKeys, DocType: doctype,
			SQL: addForeignKeysSQL(table, []foreignKeyClause{{field.Name, field.Type.Target, "RESTRICT"}})})
	}
	if field.Unique {
		steps = append(steps, Step{Kind: StepCreateUniqueIndex, DocType: doctype, Column: field.Name,
			SQL: uniqueIndexSQL(table, field.Name)})
	} else if isLink {
		steps = append(steps, Step{Kind: StepCreateIndex, DocType: doctype, Column: field.Name,
			SQL: linkIndexSQL(table, field.Name)})
	}
	return steps
}

// phases groups the steps. Order of execution: all new tables, then their primary and unique keys,
// then their foreign keys (every primary key exists by then), then added columns, then the foreign
// keys of added Link columns, then late unique and Link indexes, then registration.
type phases struct {
	createTables, addKeys, addForeignKeys, addColumns, addColumnForeignKeys, indexes, register []Step
}

// planSchema compares every DocType in registry with cat.
func planSchema(registry *core.Registry, cat *catalog) schemaPlan {
	var plan schemaPlan
	var ph phases
	for _, doctype := range registry.Iter() {
		planDocType(registry, doctype, cat, &plan, &ph)
	}
	plan.steps = slices.Concat(ph.createTables, ph.addKeys, ph.addForeignKeys, ph.addColumns,
		ph.addColumnForeignKeys, ph.indexes, ph.register)
	for _, managed := range sortedKeys(cat.managed) {
		if registry.Get(managed) == nil {
			plan.warnings = append(plan.warnings, Warning{Kind: WarningDocTypeNotInRegistry, DocType: managed})
		}
	}
	return plan
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func planDocType(registry *core.Registry, doctype *core.DocType, cat *catalog, plan *schemaPlan, ph *phases) {
	table := core.TableName(doctype.Name)
	// The parent of a child table; always there once CheckReferences passed.
	parent := ""
	if doctype.IsChild {
		if p, _ := registry.ParentOf(doctype.Name); p != nil {
			parent = p.Name
		}
	}
	managed := cat.managed[doctype.Name]
	taken := cat.relations[table]
	// Only an ordinary table is ever "unmanaged"; anything else with the name is a name clash.
	if taken && !cat.tables[table] {
		plan.conflicts = append(plan.conflicts, Conflict{Kind: ConflictRelationNameTaken, Name: table})
		return
	}
	columns, hasColumns := cat.columns[table]
	if !hasColumns {
		if taken {
			// An ordinary table with no visible columns: cannot be created over, not adopted.
			plan.conflicts = append(plan.conflicts, Conflict{Kind: ConflictRelationNameTaken, Name: table})
			return
		}
		ph.createTables = append(ph.createTables, Step{Kind: StepCreateTable, DocType: doctype.Name,
			SQL: createTableSQL(doctype, table)})
		ph.addKeys = append(ph.addKeys, Step{Kind: StepAddKeys, DocType: doctype.Name,
			SQL: addKeysSQL(doctype, table)})
		links := linkFields(doctype)
		var keys []foreignKeyClause
		if parent != "" {
			keys = append(keys, foreignKeyClause{"parent_id", parent, "CASCADE"})
			ph.indexes = append(ph.indexes, Step{Kind: StepCreateIndex, DocType: doctype.Name,
				Column: "parent_id", SQL: linkIndexSQL(table, "parent_id")})
		}
		for _, l := range links {
			keys = append(keys, foreignKeyClause{l.field.Name, l.target, "RESTRICT"})
		}
		if len(keys) > 0 {
			ph.addForeignKeys = append(ph.addForeignKeys, Step{Kind: StepAddForeignKeys, DocType: doctype.Name,
				SQL: addForeignKeysSQL(table, keys)})
		}
		// A unique Link field is already indexed by its unique constraint.
		for _, l := range links {
			if !l.field.Unique {
				ph.indexes = append(ph.indexes, Step{Kind: StepCreateIndex, DocType: doctype.Name,
					Column: l.field.Name, SQL: linkIndexSQL(table, l.field.Name)})
			}
		}
		if !managed {
			ph.register = append(ph.register, Step{Kind: StepRegisterDocType, DocType: doctype.Name})
		}
		return
	}
	if !managed {
		plan.conflicts = append(plan.conflicts, Conflict{Kind: ConflictUnmanagedTable, Table: table})
		return
	}
	for _, sc := range standardColumnsOf(doctype) {
		mismatch := func(actual string) Conflict {
			return Conflict{Kind: ConflictColumnMismatch, Table: table, Column: sc.name,
				Expected: sc.dataType + " NOT NULL", Actual: actual}
		}
		c, ok := columns[sc.name]
		switch {
		case !ok:
			plan.conflicts = append(plan.conflicts, mismatch("missing"))
		case c.dataType != sc.dataType || !c.notNull:
			null := ""
			if c.notNull {
				null = " NOT NULL"
			}
			plan.conflicts = append(plan.conflicts, mismatch(c.dataType+null))
		}
	}
	for _, ec := range extraColumns(doctype) {
		expected := ec.dataType
		if ec.notNull {
			expected += " NOT NULL"
		}
		c, ok := columns[ec.name]
		switch {
		case !ok:
			ph.addColumns = append(ph.addColumns, Step{Kind: StepAddColumn, DocType: doctype.Name, Column: ec.name,
				SQL: fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", QuoteIdent(table), extraColumnSQL(ec))})
		case c.dataType != ec.dataType || c.notNull != ec.notNull:
			null := ""
			if c.notNull {
				null = " NOT NULL"
			}
			plan.conflicts = append(plan.conflicts, Conflict{Kind: ConflictColumnMismatch, Table: table,
				Column: ec.name, Expected: expected, Actual: c.dataType + null})
		}
	}
	key := func(column string) colKey { return colKey{table, column} }
	type standardKey struct {
		column          string
		present         bool
		expected, found string
	}
	standardKeys := []standardKey{{"id", cat.primary[key("id")], "primary key", "not a primary key"}}
	if !doctype.IsChild {
		standardKeys = append(standardKeys, standardKey{"name", cat.unique[key("name")], "unique", "not unique"})
	}
	for _, sk := range standardKeys {
		if _, ok := columns[sk.column]; ok && !sk.present {
			plan.conflicts = append(plan.conflicts, Conflict{Kind: ConflictColumnMismatch, Table: table,
				Column: sk.column, Expected: sk.expected, Actual: sk.found})
		}
	}
	// The key to the parent: a missing parent_id column is already a conflict above.
	if parent != "" {
		if _, ok := columns["parent_id"]; ok {
			parentKey := key("parent_id")
			checkForeignKeys(table, "parent_id", expectedParentKey(parent), cat.foreignKeys[parentKey], &plan.conflicts)
			if !cat.indexed[parentKey] {
				ph.indexes = append(ph.indexes, Step{Kind: StepCreateIndex, DocType: doctype.Name,
					Column: "parent_id", SQL: linkIndexSQL(table, "parent_id")})
			}
		}
	}
	var addedLinks []foreignKeyClause
	for _, field := range doctype.ColumnFields() {
		k := key(field.Name)
		hasUnique := cat.unique[k]
		target := ""
		if field.Type.Kind == core.FieldLink {
			target = field.Type.Target
		}
		c, exists := columns[field.Name]
		added := !exists
		switch {
		case !exists:
			ph.addColumns = append(ph.addColumns, Step{Kind: StepAddColumn, DocType: doctype.Name,
				Column: field.Name, SQL: addColumnSQL(table, field)})
			if target != "" {
				addedLinks = append(addedLinks, foreignKeyClause{field.Name, target, "RESTRICT"})
			}
		case c.dataType != pgType(field.Type):
			plan.conflicts = append(plan.conflicts, Conflict{Kind: ConflictColumnMismatch, Table: table,
				Column: field.Name, Expected: pgType(field.Type), Actual: c.dataType})
			continue
		case c.notNull:
			plan.warnings = append(plan.warnings, Warning{Kind: WarningColumnNotNullable, Table: table, Column: field.Name})
		}
		if field.Unique && !hasUnique {
			ph.indexes = append(ph.indexes, Step{Kind: StepCreateUniqueIndex, DocType: doctype.Name,
				Column: field.Name, SQL: uniqueIndexSQL(table, field.Name)})
		} else if !field.Unique && hasUnique {
			plan.warnings = append(plan.warnings, Warning{Kind: WarningUniqueRemoved, Table: table, Column: field.Name})
		}
		fks := cat.foreignKeys[k]
		switch {
		case target != "" && added:
			// A column added in this sync gets its foreign key (and index) in the same sync.
		case target != "":
			checkForeignKeys(table, field.Name, expectedForeignKey(target), fks, &plan.conflicts)
		default:
			for _, fk := range fks {
				plan.conflicts = append(plan.conflicts, Conflict{Kind: ConflictForeignKeyMismatch, Table: table,
					Column: field.Name, Expected: "no foreign key", Actual: fk.describe()})
			}
		}
		// The unique index of a unique field covers lookups; otherwise a Link column needs a plain
		// index (also when it is only missing, for example dropped by hand).
		if target != "" && !field.Unique && !cat.indexed[k] {
			ph.indexes = append(ph.indexes, Step{Kind: StepCreateIndex, DocType: doctype.Name,
				Column: field.Name, SQL: linkIndexSQL(table, field.Name)})
		}
	}
	if len(addedLinks) > 0 {
		ph.addColumnForeignKeys = append(ph.addColumnForeignKeys, Step{Kind: StepAddForeignKeys, DocType: doctype.Name,
			SQL: addForeignKeysSQL(table, addedLinks)})
	}
	for _, name := range sortedKeys(columns) {
		known := slices.ContainsFunc(standardColumnsOf(doctype), func(c standardColumn) bool { return c.name == name }) ||
			slices.ContainsFunc(extraColumns(doctype), func(c extraColumn) bool { return c.name == name }) ||
			slices.ContainsFunc(doctype.ColumnFields(), func(f *core.DocField) bool { return f.Name == name })
		if !known {
			plan.warnings = append(plan.warnings, Warning{Kind: WarningColumnNotInMetadata, Table: table, Column: name})
		}
	}
}

// checkForeignKeys: a Link column (or parent_id) that exists in the database must have exactly the
// expected foreign key.
func checkForeignKeys(table, column string, expected foreignKey, found []foreignKey, conflicts *[]Conflict) {
	mismatch := func(actual string) Conflict {
		return Conflict{Kind: ConflictForeignKeyMismatch, Table: table, Column: column,
			Expected: expected.describe(), Actual: actual}
	}
	if len(found) == 0 {
		*conflicts = append(*conflicts, mismatch("missing"))
	}
	for _, fk := range found {
		if fk != expected {
			*conflicts = append(*conflicts, mismatch(fk.describe()))
		}
	}
}
