package db_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// The list query (M3.2, ADR 0011) against a real PostgreSQL: every operator, sorting and paging,
// limits, hostile input, and that unreadable rows never come back whatever the filter, sort or page.
// Ported from crates/forgebase-db/tests/list.rs.

const lsGroup = `
name = "grp"
label = "Group"
naming = { field = "code" }
[[fields]]
name = "code"
type = "data"
required = true
unique = true

[[permissions]]
role = "Clerk"
read = true
create = true
[[permissions]]
role = "Own"
read = true
`

const lsLine = `
name = "item_line"
label = "Item Line"
is_child = true
[[fields]]
name = "qty"
type = "int"
`

// lsItem has every field type; Clerk reads and writes everything, Own only its own documents.
const lsItem = `
name = "item"
label = "Item"
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "qty"
type = "int"
[[fields]]
name = "price"
type = "decimal"
precision = 10
scale = 2
[[fields]]
name = "done"
type = "check"
[[fields]]
name = "due"
type = "date"
[[fields]]
name = "seen"
type = "datetime"
[[fields]]
name = "kind"
type = "select"
options = ["a", "b"]
[[fields]]
name = "note"
type = "text"
[[fields]]
name = "grp"
type = "link"
target = "grp"
[[fields]]
name = "lines"
type = "table"
target = "item_line"

[[permissions]]
role = "Clerk"
read = true
write = true
create = true
delete = true
[[permissions]]
role = "Own"
read = true
create = true
if_owner = true
`

const lsQuote = `
name = "quote"
label = "Quote"
submittable = true
[[fields]]
name = "title"
type = "data"

[[permissions]]
role = "Clerk"
read = true
create = true
submit = true
cancel = true
delete = true
`

func lsSetup(t *testing.T) (*dbtest.DB, *core.Registry) {
	t.Helper()
	return setup(t, lsGroup, lsLine, lsItem, lsQuote)
}

func lsDate(day int) core.Value { return date(2026, time.October, day) }

func lsAt(day int64) core.Value {
	return core.Datetime(time.Unix(0, 0).UTC().Add(time.Duration(day) * 24 * time.Hour))
}

func f(field string, op db.FilterOp) db.Filter { return db.Filter{Field: field, Op: op} }

func lq(filters ...db.Filter) db.ListQuery { return db.ListQuery{Filters: filters} }

func sorted(filters []db.Filter, sort ...db.Sort) db.ListQuery {
	return db.ListQuery{Filters: filters, Sort: sort}
}

func u32(n uint32) *uint32 { return &n }
func u64(n uint64) *uint64 { return &n }

func listDocs(t *testing.T, d *dbtest.DB, reg *core.Registry, doctype string, caller core.Caller, query db.ListQuery) []*db.StoredDocument {
	t.Helper()
	docs, err := db.List(bg, d.Pool, reg, doctype, caller, query)
	if err != nil {
		t.Fatalf("list %s: %v", doctype, err)
	}
	return docs
}

// lsTitles are the titles of the items a caller lists, in order.
func lsTitles(t *testing.T, d *dbtest.DB, reg *core.Registry, caller core.Caller, query db.ListQuery) []string {
	t.Helper()
	out := []string{}
	for _, doc := range listDocs(t, d, reg, "item", caller, query) {
		out = append(out, doc.Values["title"].Text)
	}
	return out
}

func lsAdd(t *testing.T, d *dbtest.DB, reg *core.Registry, owner core.Caller, kv ...any) string {
	t.Helper()
	return mustInsert(t, d, reg, "item", doc(kv...), owner).Name
}

// lsSeed is two groups and five items with varied values.
func lsSeed(t *testing.T, d *dbtest.DB, reg *core.Registry) {
	t.Helper()
	for _, code := range []string{"g1", "g2"} {
		mustInsert(t, d, reg, "grp", doc("code", text(code)), sys("sys"))
	}
	for _, r := range []struct {
		title      string
		qty        int64
		price      string
		done       bool
		due        int
		seen       int64
		kind, grp_ string
	}{
		{"a", 1, "10.00", true, 1, 1, "a", "g1"},
		{"b", 2, "20.50", false, 2, 2, "b", "g1"},
		{"c", 3, "30.00", true, 3, 3, "a", "g2"},
		{"d", 4, "5.25", false, 4, 4, "b", ""},
		{"e", 5, "40.00", true, 5, 5, "a", "g2"},
	} {
		kv := []any{"title", text(r.title), "qty", core.Int(r.qty), "price", dec(r.price), "done", core.Bool(r.done),
			"due", lsDate(r.due), "seen", lsAt(r.seen), "kind", text(r.kind)}
		if r.grp_ != "" {
			kv = append(kv, "grp", text(r.grp_))
		}
		lsAdd(t, d, reg, sys("sys"), kv...)
	}
}

// Rust: list.rs::every_operator_on_every_field_type
func TestEveryOperatorOnEveryFieldType(t *testing.T) {
	d, reg := lsSetup(t)
	lsSeed(t, d, reg)
	check := func(filters []db.Filter, expected ...string) {
		t.Helper()
		query := sorted(filters, db.Asc("title"))
		got := lsTitles(t, d, reg, sys("sys"), query)
		if !slices.Equal(got, append([]string{}, expected...)) {
			t.Errorf("%+v: got %v, want %v", query, got, expected)
		}
	}
	fs := func(fl ...db.Filter) []db.Filter { return fl }
	// Int
	check(fs(f("qty", db.Eq(core.Int(3)))), "c")
	check(fs(f("qty", db.Ne(core.Int(3)))), "a", "b", "d", "e")
	check(fs(f("qty", db.Lt(core.Int(3)))), "a", "b")
	check(fs(f("qty", db.Le(core.Int(3)))), "a", "b", "c")
	check(fs(f("qty", db.Gt(core.Int(3)))), "d", "e")
	check(fs(f("qty", db.Ge(core.Int(3)))), "c", "d", "e")
	check(fs(f("qty", db.In(core.Int(1), core.Int(5), core.Int(9)))), "a", "e")
	// Decimal, with exact comparison
	check(fs(f("price", db.Eq(dec("20.5")))), "b")
	check(fs(f("price", db.Lt(dec("10.00")))), "d")
	check(fs(f("price", db.Ge(dec("30")))), "c", "e")
	// Check
	check(fs(f("done", db.Eq(core.Bool(true)))), "a", "c", "e")
	check(fs(f("done", db.Ne(core.Bool(true)))), "b", "d")
	// Date and datetime
	check(fs(f("due", db.Gt(lsDate(3)))), "d", "e")
	check(fs(f("due", db.In(lsDate(1), lsDate(2)))), "a", "b")
	check(fs(f("seen", db.Le(lsAt(2)))), "a", "b")
	// Text and Select
	check(fs(f("title", db.Eq(text("b")))), "b")
	check(fs(f("title", db.Gt(text("c")))), "d", "e")
	check(fs(f("kind", db.Eq(text("b")))), "b", "d")
	check(fs(f("kind", db.In(text("a"), text("zzz")))), "a", "c", "e")
	// Several filters are ANDed.
	check(fs(f("kind", db.Eq(text("a"))), f("qty", db.Gt(core.Int(1)))), "c", "e")
	// Null checks (<> leaves out nulls, as SQL does)
	check(fs(f("grp", db.IsNull(true))), "d")
	check(fs(f("grp", db.IsNull(false))), "a", "b", "c", "e")
	check(fs(f("note", db.IsNull(true))), "a", "b", "c", "d", "e")
	check(fs(f("note", db.IsNull(false))))
	check(fs(f("note", db.Ne(text("x")))))
	// Standard columns
	check(fs(f("version", db.Eq(core.Int(1)))), "a", "b", "c", "d", "e")
	check(fs(f("owner", db.Eq(text("sys")))), "a", "b", "c", "d", "e")
	check(fs(f("owner", db.Eq(text("nobody")))))
	check(fs(f("modified_by", db.Like("s%"))), "a", "b", "c", "d", "e")
	check(fs(f("created_at", db.Gt(lsAt(-1)))), "a", "b", "c", "d", "e")
	check(fs(f("modified_at", db.Lt(lsAt(-1)))))
	// Filtering by name
	all := listDocs(t, d, reg, "item", sys("sys"), db.ListQuery{})
	one := all[2].Name
	only := listDocs(t, d, reg, "item", sys("sys"), lq(f("name", db.Eq(text(one)))))
	if len(only) != 1 || only[0].Name != one {
		t.Error(only)
	}
}

// Rust: list.rs::like_matches_patterns_on_text_fields_only
func TestLikeMatchesPatternsOnTextFieldsOnly(t *testing.T) {
	d, reg := lsSetup(t)
	for _, title := range []string{"apple", "apricot", "banana", "100%", "a_b", "AX"} {
		lsAdd(t, d, reg, sys("sys"), "title", text(title))
	}
	by := func(p string) db.ListQuery {
		return sorted([]db.Filter{f("title", db.Like(p))}, db.Asc("title"))
	}
	for _, c := range []struct {
		pattern string
		want    []string
	}{
		{"ap%", []string{"apple", "apricot"}},
		{"%an%", []string{"banana"}},
		{"a_b", []string{"a_b"}},
		// Case-sensitive, and \ escapes a wildcard.
		{"ax", []string{}},
		{"100\\%", []string{"100%"}},
		// A SQL fragment in the pattern is just a pattern.
		{"x' OR '1'='1", []string{}},
	} {
		if got := lsTitles(t, d, reg, sys("sys"), by(c.pattern)); !slices.Equal(got, c.want) {
			t.Errorf("%q: %v", c.pattern, got)
		}
	}
	// like on kind (select) and note (text) is fine; on anything else it is rejected.
	for _, field := range []string{"qty", "price", "done", "due", "seen", "grp", "version", "created_at"} {
		_, err := db.List(bg, d.Pool, reg, "item", sys("sys"), lq(f(field, db.Like("%"))))
		if e := wantKind(t, err, db.ErrInvalidQuery); !strings.Contains(e.Message, "text fields only") {
			t.Errorf("%s: %v", field, err)
		}
	}
	for _, field := range []string{"kind", "note", "name", "owner", "modified_by"} {
		listDocs(t, d, reg, "item", sys("sys"), lq(f(field, db.Like("%"))))
	}
}

// Rust: list.rs::link_fields_filter_and_sort_by_the_target_name
func TestLinkFieldsFilterAndSortByTheTargetName(t *testing.T) {
	d, reg := lsSetup(t)
	lsSeed(t, d, reg)
	by := func(fl ...db.Filter) db.ListQuery { return sorted(fl, db.Asc("title")) }
	for _, c := range []struct {
		name  string
		query db.ListQuery
		want  []string
	}{
		{"eq", by(f("grp", db.Eq(text("g1")))), []string{"a", "b"}},
		{"ne", by(f("grp", db.Ne(text("g1")))), []string{"c", "e"}},
		{"in", by(f("grp", db.In(text("g1"), text("g2")))), []string{"a", "b", "c", "e"}},
		// A target that does not exist matches nothing (=, in) or every linked row (<>).
		{"ghost", by(f("grp", db.Eq(text("ghost")))), []string{}},
		{"not ghost", by(f("grp", db.Ne(text("ghost")))), []string{"a", "b", "c", "e"}},
	} {
		if got := lsTitles(t, d, reg, sys("sys"), c.query); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	// Sorting by the link orders by the target's name, then the tie-breaker. Descending puts nulls
	// first, then g2, g1; ties by name (random, so compare as groups).
	order := lsTitles(t, d, reg, sys("sys"), sorted(nil, db.Desc("grp")))
	if order[0] != "d" {
		t.Error(order)
	}
	g2 := slices.Clone(order[1:3])
	slices.Sort(g2)
	if !slices.Equal(g2, []string{"c", "e"}) {
		t.Error(order)
	}
	// The target value is returned as a name.
	listed := listDocs(t, d, reg, "item", sys("sys"), lq(f("title", db.Eq(text("a")))))
	if listed[0].Values["grp"].Text != "g1" {
		t.Error(listed[0].Values["grp"])
	}
	// Only equality-like operators and null checks apply to links.
	for _, op := range []db.FilterOp{db.Gt(text("a")), db.Lt(text("a")), db.Like("g%")} {
		_, err := db.List(bg, d.Pool, reg, "item", sys("sys"), lq(f("grp", op)))
		wantKind(t, err, db.ErrInvalidQuery)
	}
}

// Rust: list.rs::type_mismatches_and_bad_operands_are_rejected
func TestTypeMismatchesAndBadOperandsAreRejected(t *testing.T) {
	d, reg := lsSetup(t)
	for _, filter := range []db.Filter{
		f("qty", db.Eq(text("1"))),
		f("qty", db.Eq(dec("1"))),
		f("price", db.Eq(core.Int(1))),
		f("title", db.Eq(core.Int(1))),
		f("done", db.Eq(text("true"))),
		f("due", db.Eq(text("2026-10-01"))),
		f("seen", db.Eq(lsDate(1))),
		f("grp", db.Eq(core.Int(1))),
		f("version", db.Eq(text("1"))),
		f("created_at", db.Eq(core.Int(1))),
		f("qty", db.Eq(core.Null())),
		f("qty", db.In(core.Int(1), text("2"))),
		f("qty", db.In()),
		f("done", db.Lt(core.Bool(true))),
		f("title", db.Eq(text("a\x00b"))),
		f("lines", db.IsNull(true)),
		f("nope", db.IsNull(true)),
		f("docstatus", db.IsNull(true)), // not submittable
	} {
		_, err := db.List(bg, d.Pool, reg, "item", sys("sys"), lq(filter))
		if e := asDbError(t, err); e.Kind != db.ErrInvalidQuery {
			t.Errorf("%+v: %v", filter, err)
		}
	}
}

// Rust: list.rs::sorting_is_multi_field_and_paging_is_stable
func TestSortingIsMultiFieldAndPagingIsStable(t *testing.T) {
	d, reg := lsSetup(t)
	lsSeed(t, d, reg)
	for _, c := range []struct {
		sort []db.Sort
		want []string
	}{
		{[]db.Sort{db.Desc("kind"), db.Desc("qty")}, []string{"d", "b", "e", "c", "a"}},
		{[]db.Sort{db.Asc("price")}, []string{"d", "a", "b", "c", "e"}},
		{[]db.Sort{db.Asc("due"), db.Desc("title")}, []string{"a", "b", "c", "d", "e"}},
		{[]db.Sort{db.Desc("seen")}, []string{"e", "d", "c", "b", "a"}},
	} {
		if got := lsTitles(t, d, reg, sys("sys"), sorted(nil, c.sort...)); !slices.Equal(got, c.want) {
			t.Errorf("%+v: %v", c.sort, got)
		}
	}
	// All five tie on done's group of two values; paging by 2 sees every row exactly once, in the same
	// order as one big page, because name is the final tie-breaker.
	whole := listDocs(t, d, reg, "item", sys("sys"), db.ListQuery{Sort: []db.Sort{db.Asc("done")}})
	var paged []*db.StoredDocument
	for _, offset := range []uint64{0, 2, 4, 6} {
		paged = append(paged, listDocs(t, d, reg, "item", sys("sys"),
			db.ListQuery{Sort: []db.Sort{db.Asc("done")}, Limit: u32(2), Offset: u64(offset)})...)
	}
	names := func(docs []*db.StoredDocument) []string {
		var out []string
		for _, doc := range docs {
			out = append(out, doc.Name)
		}
		return out
	}
	if len(paged) != 5 || !slices.Equal(names(paged), names(whole)) {
		t.Errorf("paged %v, whole %v", names(paged), names(whole))
	}
	// Within equal sort values the order is by name.
	var falses []string
	for _, doc := range whole {
		if !doc.Values["done"].Bool {
			falses = append(falses, doc.Name)
		}
	}
	if !slices.IsSorted(falses) {
		t.Error(falses)
	}
}

// Rust: list.rs::paging_limits
func TestPagingLimits(t *testing.T) {
	d, reg := lsSetup(t)
	for i := range 25 {
		mustInsert(t, d, reg, "item", doc("title", text(fmt.Sprintf("t%02d", i)), "qty", core.Int(int64(i))), sys("sys"))
	}
	run := func(limit *uint32, offset *uint64) ([]*db.StoredDocument, error) {
		return db.List(bg, d.Pool, reg, "item", sys("sys"), db.ListQuery{Sort: []db.Sort{db.Asc("qty")}, Limit: limit, Offset: offset})
	}
	if db.DefaultLimit != 20 || db.MaxLimit != 500 {
		t.Error("limits")
	}
	for _, c := range []struct {
		limit  *uint32
		offset *uint64
		n      int
	}{
		{nil, nil, 20}, {u32(500), nil, 25}, {u32(3), u64(22), 3}, {u32(10), u64(20), 5},
		{u32(10), u64(25), 0}, {u32(10), u64(100_000), 0},
	} {
		got, err := run(c.limit, c.offset)
		if err != nil || len(got) != c.n {
			t.Errorf("%v %v: %d %v", c.limit, c.offset, len(got), err)
		}
	}
	if first, _ := run(u32(1), u64(0)); first[0].Values["qty"].Int != 0 {
		t.Error(first[0].Values["qty"])
	}
	for _, c := range []struct {
		limit  *uint32
		offset *uint64
	}{{u32(501), nil}, {u32(0), nil}, {u32(1<<32 - 1), nil}, {nil, u64(1<<64 - 1)}} {
		_, err := run(c.limit, c.offset)
		wantKind(t, err, db.ErrInvalidQuery)
	}
}

// Rust: list.rs::submittable_doctypes_have_docstatus_and_amended_from_is_not_a_filter
func TestSubmittableDoctypesHaveDocstatusAndAmendedFromIsNotAFilter(t *testing.T) {
	d, reg := lsSetup(t)
	var names []*db.StoredDocument
	for _, title := range []string{"one", "two", "three"} {
		names = append(names, mustInsert(t, d, reg, "quote", doc("title", text(title)), sys("sys")))
	}
	v := mustSubmit(t, d, reg, "quote", names[1].Name, 1, sys("sys")).Version
	mustSubmit(t, d, reg, "quote", names[2].Name, 1, sys("sys"))
	mustCancel(t, d, reg, "quote", names[1].Name, v, sys("sys"))
	count := func(status int64) int {
		return len(listDocs(t, d, reg, "quote", sys("sys"), lq(f("docstatus", db.Eq(core.Int(status))))))
	}
	if count(0) != 1 || count(1) != 1 || count(2) != 1 {
		t.Errorf("%d %d %d", count(0), count(1), count(2))
	}
	if n := len(listDocs(t, d, reg, "quote", sys("sys"), lq(f("docstatus", db.Ge(core.Int(1)))))); n != 2 {
		t.Error(n)
	}
	_, err := db.List(bg, d.Pool, reg, "quote", sys("sys"), lq(f("amended_from", db.IsNull(true))))
	wantKind(t, err, db.ErrInvalidQuery)
}

// Rust: list.rs::list_rows_match_get_and_have_no_tables
func TestListRowsMatchGetAndHaveNoTables(t *testing.T) {
	d, reg := lsSetup(t)
	lsSeed(t, d, reg)
	// An item with child rows.
	withRows := doc("title", text("rows"), "grp", text("g1"))
	withRows.Tables["lines"] = []core.Row{row("qty", core.Int(1)), row("qty", core.Int(1))}
	made := mustInsert(t, d, reg, "item", withRows, sys("sys"))
	if len(made.Tables["lines"]) != 2 {
		t.Fatal(made.Tables)
	}
	listed := listDocs(t, d, reg, "item", sys("sys"), lq(f("title", db.Eq(text("rows")))))
	if len(listed) != 1 {
		t.Fatal(len(listed))
	}
	got := mustGet(t, d, reg, "item", made.Name, sys("sys"))
	if len(listed[0].Tables) != 0 {
		t.Error("a list has no tables")
	}
	got.Tables = map[string][]core.Row{}
	sameDocument(t, listed[0], got, "list vs get")
	if listed[0].Values["grp"].Text != "g1" {
		t.Error(listed[0].Values["grp"])
	}
}

// ---- Permissions ---------------------------------------------------------------------------------

// Rust: list.rs::a_list_never_returns_rows_the_caller_cannot_read
func TestAListNeverReturnsRowsTheCallerCannotRead(t *testing.T) {
	d, reg := lsSetup(t)
	mustInsert(t, d, reg, "grp", doc("code", text("g")), sys("sys"))
	ann, bob := user("ann", "Own"), user("bob", "Own")
	type titled struct{ title, name string }
	var bobs []titled
	for i := range 30 {
		owner, prefix := bob, "bob"
		if i%3 == 0 {
			owner, prefix = ann, "ann"
		}
		title := fmt.Sprintf("%s%02d", prefix, i)
		name := lsAdd(t, d, reg, owner, "title", text(title), "qty", core.Int(int64(i)), "grp", text("g"))
		if i%3 != 0 {
			bobs = append(bobs, titled{title, name})
		}
	}
	anns := lsTitles(t, d, reg, ann, sorted(nil, db.Asc("qty")))
	if len(anns) != 10 {
		t.Fatal(anns)
	}
	for _, title := range anns {
		if title[:3] != "ann" {
			t.Fatalf("%v", anns)
		}
	}
	// Filters cannot reach them: by owner, by name, by title, by in, by negation, by link, by like, by
	// null checks ...
	bobName := bobs[0].name
	var bobNames []core.Value
	for _, b := range bobs {
		bobNames = append(bobNames, text(b.name))
	}
	for _, filters := range [][]db.Filter{
		{f("owner", db.Eq(text("bob")))},
		{f("owner", db.Ne(text("ann")))},
		{f("owner", db.Like("b%"))},
		{f("name", db.Eq(text(bobName)))},
		{f("name", db.In(bobNames...))},
		{f("title", db.Like("bob%"))},
		{f("title", db.Eq(text(bobs[1].title)))},
		{f("modified_by", db.Eq(text("bob")))},
		{f("qty", db.Eq(core.Int(1)))},
	} {
		if got := lsTitles(t, d, reg, ann, lq(filters...)); len(got) != 0 {
			t.Errorf("%+v -> %v", filters, got)
		}
	}
	// ... and get agrees.
	if got, err := db.Get(bg, d.Pool, reg, "item", bobName, ann); err != nil || got != nil {
		t.Error("get found a hidden document", err)
	}
	// A filter that matches both sides only returns her own.
	if n := len(lsTitles(t, d, reg, ann, lq(f("grp", db.Eq(text("g")))))); n != 10 {
		t.Error(n)
	}
	if n := len(lsTitles(t, d, reg, ann, lq(f("note", db.IsNull(true))))); n != 10 {
		t.Error(n)
	}
	// Sorting and paging cannot reach them either: every page of her list is hers, and a page past her
	// own rows is empty although others' rows exist there.
	var seen []string
	for page := range uint64(6) {
		seen = append(seen, lsTitles(t, d, reg, ann, db.ListQuery{Sort: []db.Sort{db.Desc("owner"), db.Asc("qty")}, Limit: u32(2), Offset: u64(page * 2)})...)
	}
	if len(seen) != 10 {
		t.Error(seen)
	}
	for _, title := range seen {
		if title[:3] != "ann" {
			t.Error(seen)
		}
	}
	if got := lsTitles(t, d, reg, ann, db.ListQuery{Limit: u32(500), Offset: u64(10)}); len(got) != 0 {
		t.Error(got)
	}
	// Bob sees his twenty, an administrator and the system see all thirty.
	all := db.ListQuery{Limit: u32(500)}
	if n := len(lsTitles(t, d, reg, bob, all)); n != 20 {
		t.Error(n)
	}
	if n := len(lsTitles(t, d, reg, user("root", core.Administrator), all)); n != 30 {
		t.Error(n)
	}
	if n := len(lsTitles(t, d, reg, sys("sys"), all)); n != 30 {
		t.Error(n)
	}
	// Clerk reads everything (no if_owner); a union of roles takes the wider access.
	if n := len(lsTitles(t, d, reg, user("c", "Clerk"), all)); n != 30 {
		t.Error(n)
	}
	if n := len(lsTitles(t, d, reg, user("c", "Own", "Clerk"), all)); n != 30 {
		t.Error(n)
	}
}

// Rust: list.rs::a_caller_without_read_access_cannot_list
func TestACallerWithoutReadAccessCannotList(t *testing.T) {
	d, reg := lsSetup(t)
	for _, roles := range [][]string{nil, {"Nobody"}, {"own"}} {
		_, err := db.List(bg, d.Pool, reg, "item", user("u", roles...), db.ListQuery{})
		if e := wantKind(t, err, db.ErrPermissionDenied); e.DocType != "item" {
			t.Error(err)
		}
	}
	// quote lets Clerk read but Own has no rule at all.
	_, err := db.List(bg, d.Pool, reg, "quote", user("u", "Own"), db.ListQuery{})
	wantKind(t, err, db.ErrPermissionDenied)
}

// ---- Hostile input -------------------------------------------------------------------------------

// Rust: list.rs::hostile_names_never_reach_the_sql
func TestHostileNamesNeverReachTheSQL(t *testing.T) {
	d, reg := lsSetup(t)
	lsSeed(t, d, reg)
	for _, bad := range []string{
		"name; drop table doc_item", "title\"; DROP TABLE doc_item; --", "title --", "'; select 1; --",
		"qty, (select 1)", "doc_item.qty", "\"qty\"", "id", "amended_from", "TITLE", " title", "title ",
		"", "\x00", "ti\ntle",
	} {
		for _, query := range []db.ListQuery{
			lq(f(bad, db.IsNull(true))),
			lq(f(bad, db.Eq(core.Int(1)))),
			sorted(nil, db.Asc(bad)),
			sorted(nil, db.Desc(bad)),
		} {
			_, err := db.List(bg, d.Pool, reg, "item", sys("sys"), query)
			if e := wantKind(t, err, db.ErrInvalidQuery); !strings.Contains(e.Message, "unknown field") {
				t.Errorf("%q: %v", bad, err)
			}
		}
	}
	// Hostile DocType names: nothing is looked up or run.
	for _, bad := range []string{"item; drop table doc_item", "item\"", "doc_item", "ITEM", "", "item --"} {
		_, err := db.List(bg, d.Pool, reg, bad, sys("sys"), db.ListQuery{})
		wantKind(t, err, db.ErrUnknownDocType)
	}
	_, err := db.List(bg, d.Pool, reg, "item_line", sys("sys"), db.ListQuery{})
	wantKind(t, err, db.ErrChildDocType)
	// The table is still there.
	if n := len(lsTitles(t, d, reg, sys("sys"), db.ListQuery{})); n != 5 {
		t.Error(n)
	}
}

// Rust: list.rs::hostile_values_are_stored_and_compared_literally
func TestHostileValuesAreStoredAndComparedLiterally(t *testing.T) {
	d, reg := lsSetup(t)
	nasty := []string{
		"x'); DROP TABLE doc_item; --", "\"; DROP TABLE doc_item; --", "' OR '1'='1", "$1 $2 ${x} \\' %s", "50% off_ _",
	}
	for _, title := range nasty {
		lsAdd(t, d, reg, sys("sys"), "title", text(title))
	}
	lsAdd(t, d, reg, sys("sys"), "title", text("plain"))
	for _, title := range nasty {
		if got := lsTitles(t, d, reg, sys("sys"), lq(f("title", db.Eq(text(title))))); !slices.Equal(got, []string{title}) {
			t.Errorf("%s: %v", title, got)
		}
		if got := lsTitles(t, d, reg, sys("sys"), lq(f("title", db.In(text(title), text("zzz"))))); !slices.Equal(got, []string{title}) {
			t.Errorf("%s: %v", title, got)
		}
	}
	// The injection attempt as a value matches nothing else, and as an owner scope it is inert.
	if got := lsTitles(t, d, reg, sys("sys"), lq(f("title", db.Eq(text("' OR 1=1 --"))))); len(got) != 0 {
		t.Error(got)
	}
	hostile := user("x' OR '1'='1", "Own")
	if got := lsTitles(t, d, reg, hostile, db.ListQuery{}); len(got) != 0 {
		t.Error(got)
	}
	made := lsAdd(t, d, reg, hostile, "title", text("mine"))
	if got := lsTitles(t, d, reg, hostile, db.ListQuery{}); !slices.Equal(got, []string{"mine"}) {
		t.Error(got)
	}
	if stored := mustGet(t, d, reg, "item", made, hostile); stored.Owner != "x' OR '1'='1" {
		t.Error(stored.Owner)
	}
	// And the table survived all of it.
	if n := len(lsTitles(t, d, reg, sys("sys"), db.ListQuery{Limit: u32(500)})); n != 7 {
		t.Error(n)
	}
	// Cleanup path of a delete still works for a Clerk (smoke test of the combined flow).
	if err := db.Delete(bg, d.Pool, reg, "item", made, 1, user("c", "Clerk")); err != nil {
		t.Error(err)
	}
}
