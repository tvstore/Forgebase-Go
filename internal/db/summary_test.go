package db_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// The group-by summary (M8.3) against a real PostgreSQL: counts and aggregates, the read scope it shares
// with the list (if_owner), filters, null keys, the fields that are refused, ordering and the 500-group
// cap. Ported from crates/forgebase-db/tests/summary.rs.

const sumSale = `
name = "sale"
label = "Sale"
submittable = true
[[fields]]
name = "title"
type = "data"
[[fields]]
name = "status"
type = "select"
options = ["draft", "open", "closed"]
[[fields]]
name = "customer"
type = "link"
target = "person"
[[fields]]
name = "amount"
type = "decimal"
precision = 10
scale = 2
[[fields]]
name = "qty"
type = "int"
[[fields]]
name = "due"
type = "date"
[[fields]]
name = "paid"
type = "check"
[[fields]]
name = "note"
type = "text"
[[fields]]
name = "seen"
type = "datetime"
[[fields]]
name = "lines"
type = "table"
target = "sale_line"

[[permissions]]
role = "Clerk"
read = true
create = true
[[permissions]]
role = "Own"
read = true
create = true
if_owner = true
`

const sumLine = `
name = "sale_line"
label = "Sale Line"
is_child = true
[[fields]]
name = "qty"
type = "int"
`

const sumPerson = `
name = "person"
label = "Person"
naming = { field = "email" }
[[fields]]
name = "email"
type = "data"
required = true
unique = true
[[permissions]]
role = "Clerk"
read = true
create = true
`

const sumTag = `
name = "tag"
label = "Tag"
[[fields]]
name = "label"
type = "data"
[[permissions]]
role = "Clerk"
read = true
create = true
`

func sumSetup(t *testing.T) (*dbtest.DB, *core.Registry) {
	return setup(t, sumSale, sumLine, sumPerson, sumTag)
}

func clerk() core.Caller { return user("clerk", "Clerk") }

func own(name string) core.Caller { return user(name, "Own") }

func octDate(day int) core.Value { return date(2026, 10, day) }

func addDoc(t *testing.T, d *dbtest.DB, reg *core.Registry, owner core.Caller, doctype string, kv ...any) {
	t.Helper()
	mustInsert(t, d, reg, doctype, doc(kv...), owner)
}

func addSale(t *testing.T, d *dbtest.DB, reg *core.Registry, owner core.Caller, kv ...any) {
	t.Helper()
	addDoc(t, d, reg, owner, "sale", kv...)
}

func sumQuery(groupBy string, aggregates ...db.Aggregate) db.SummaryQuery {
	return db.SummaryQuery{GroupBy: groupBy, Aggregates: aggregates}
}

func agg(fn db.AggregateFn, field string) db.Aggregate { return db.Aggregate{Fn: fn, Field: field} }

func runSummary(d *dbtest.DB, reg *core.Registry, caller core.Caller, doctype string, q db.SummaryQuery) (*db.SummaryResult, error) {
	return db.Summary(bg, d.Pool, reg, doctype, caller, q)
}

func mustSummary(t *testing.T, d *dbtest.DB, reg *core.Registry, caller core.Caller, doctype string, q db.SummaryQuery) *db.SummaryResult {
	t.Helper()
	got, err := runSummary(d, reg, caller, doctype, q)
	if err != nil {
		t.Fatalf("%+v: %v", q, err)
	}
	return got
}

// groupKeys is "key=count" per group; a null key is "null", a bool "true" or "false".
func groupKeys(s *db.SummaryResult) []string {
	out := []string{}
	for _, g := range s.Groups {
		out = append(out, fmt.Sprintf("%s=%d", valueText(g.Key), g.Count))
	}
	return out
}

func valueText(v core.Value) string {
	switch v.Kind {
	case core.KindNull:
		return "null"
	case core.KindText:
		return v.Text
	case core.KindInt:
		return fmt.Sprint(v.Int)
	case core.KindBool:
		return fmt.Sprint(v.Bool)
	case core.KindDate:
		return v.Time.Format("2006-01-02")
	}
	return fmt.Sprintf("%v", v)
}

func refusedQuery(t *testing.T, err error) string {
	t.Helper()
	return wantKind(t, err, db.ErrInvalidQuery).Message
}

func wantValue(t *testing.T, got, want core.Value) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// sumSeed: draft: 3 sales (10.00, 20.50, 5.00; qty 1, 2, 4), open: 2 (100.00, 50.00; qty 10, 20), closed: 1
// (no amount, no qty), no status: 1 (7.00).
func sumSeed(t *testing.T, d *dbtest.DB, reg *core.Registry) {
	t.Helper()
	c := clerk()
	for _, s := range []struct {
		status, amount string
		qty            int64
		due            int
	}{
		{"draft", "10.00", 1, 3}, {"draft", "20.50", 2, 1}, {"draft", "5.00", 4, 2},
		{"open", "100.00", 10, 9}, {"open", "50.00", 20, 8}, {"closed", "", -1, 7},
	} {
		kv := []any{"status", text(s.status), "due", octDate(s.due)}
		if s.amount != "" {
			kv = append(kv, "amount", dec(s.amount))
		}
		if s.qty >= 0 {
			kv = append(kv, "qty", core.Int(s.qty))
		}
		addSale(t, d, reg, c, kv...)
	}
	addSale(t, d, reg, c, "amount", dec("7.00"))
}

// Rust: summary.rs::counts_and_aggregates_on_known_data
func TestCountsAndAggregatesOnKnownData(t *testing.T) {
	d, reg := sumSetup(t)
	sumSeed(t, d, reg)
	q := sumQuery("status", agg(db.AggSum, "amount"), agg(db.AggAvg, "amount"), agg(db.AggMin, "amount"),
		agg(db.AggMax, "amount"), agg(db.AggSum, "qty"), agg(db.AggAvg, "qty"))
	result := mustSummary(t, d, reg, clerk(), "sale", q)
	if result.Truncated {
		t.Fatal("truncated")
	}
	// Largest first; the ties (one each) in byte order with the null key last.
	strsEqual(t, groupKeys(result), []string{"draft=3", "open=2", "closed=1", "null=1"})
	draft := result.Groups[0].Values
	wantValue(t, draft["sum_amount"], dec("35.50"))
	// The average of a decimal has the field's scale plus 2: 11.8333... at 4 places.
	wantValue(t, draft["avg_amount"], dec("11.8333"))
	wantValue(t, draft["min_amount"], dec("5.00"))
	wantValue(t, draft["max_amount"], dec("20.50"))
	// An Int sums to an Int, averages to a decimal of 6 places.
	wantValue(t, draft["sum_qty"], core.Int(7))
	wantValue(t, draft["avg_qty"], dec("2.333333"))
	dates := mustSummary(t, d, reg, clerk(), "sale", sumQuery("status", agg(db.AggMin, "due"), agg(db.AggMax, "due")))
	wantValue(t, dates.Groups[0].Values["min_due"], octDate(1))
	wantValue(t, dates.Groups[0].Values["max_due"], octDate(3))
	// Every value null: null aggregates; the count is still there.
	closed := result.Groups[2].Values
	for _, k := range []string{"sum_amount", "avg_qty", "min_amount", "sum_qty"} {
		wantValue(t, closed[k], core.Null())
	}
	// The null group has its own amount.
	wantValue(t, result.Groups[3].Values["sum_amount"], dec("7.00"))
	// Only what was asked for.
	plain := mustSummary(t, d, reg, clerk(), "sale", sumQuery("status"))
	for _, g := range plain.Groups {
		if len(g.Values) != 0 {
			t.Fatal(g)
		}
	}
	if plain.Groups[0].Count != 3 {
		t.Fatal(plain.Groups[0])
	}
}

// Rust: summary.rs::the_read_scope_is_the_lists_if_owner_included
func TestTheReadScopeIsTheListsIfOwnerIncluded(t *testing.T) {
	d, reg := sumSetup(t)
	ann, bob := own("ann"), own("bob")
	for _, status := range []string{"draft", "draft", "open"} {
		addSale(t, d, reg, ann, "status", text(status), "qty", core.Int(1))
	}
	for _, status := range []string{"draft", "closed", "closed", "closed"} {
		addSale(t, d, reg, bob, "status", text(status), "qty", core.Int(10))
	}
	q := sumQuery("status", agg(db.AggSum, "qty"))
	mine := mustSummary(t, d, reg, ann, "sale", q)
	strsEqual(t, groupKeys(mine), []string{"draft=2", "open=1"}) // only ann's own documents
	wantValue(t, mine.Groups[0].Values["sum_qty"], core.Int(2))
	theirs := mustSummary(t, d, reg, bob, "sale", q)
	strsEqual(t, groupKeys(theirs), []string{"closed=3", "draft=1"})
	wantValue(t, theirs.Groups[0].Values["sum_qty"], core.Int(30))
	all := mustSummary(t, d, reg, clerk(), "sale", q)
	strsEqual(t, groupKeys(all), []string{"closed=3", "draft=3", "open=1"})
	// The counts agree with a list of the same scope and filters.
	listed, err := db.List(bg, d.Pool, reg, "sale", ann, db.ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, g := range mine.Groups {
		total += g.Count
	}
	if int64(len(listed)) != total {
		t.Fatal(len(listed), total)
	}
	// Grouping by owner shows the caller's own name only, with the caller's own count.
	strsEqual(t, groupKeys(mustSummary(t, d, reg, ann, "sale", sumQuery("owner"))), []string{"ann=3"})
	// A filter on someone else's value does not reveal their rows.
	filtered := sumQuery("status")
	filtered.Filters = []db.Filter{{Field: "owner", Op: db.Eq(text("bob"))}}
	if got := mustSummary(t, d, reg, ann, "sale", filtered); len(got.Groups) != 0 {
		t.Fatal(got)
	}
}

// Rust: summary.rs::without_the_read_right_it_is_denied_and_a_child_doctype_is_refused
func TestSummaryWithoutTheReadRightIsDeniedAndAChildDoctypeIsRefused(t *testing.T) {
	d, reg := sumSetup(t)
	nobody := user("zed", "Nobody")
	_, err := runSummary(d, reg, nobody, "sale", sumQuery("status"))
	wantKind(t, err, db.ErrPermissionDenied)
	// Denied before the query is looked at, so even an invalid one says nothing.
	_, err = runSummary(d, reg, nobody, "sale", sumQuery("nope"))
	wantKind(t, err, db.ErrPermissionDenied)
	_, err = runSummary(d, reg, clerk(), "sale_line", sumQuery("qty"))
	wantKind(t, err, db.ErrChildDocType)
	_, err = runSummary(d, reg, clerk(), "nope", sumQuery("qty"))
	wantKind(t, err, db.ErrUnknownDocType)
}

// Rust: summary.rs::filters_combine_with_the_group_by
func TestFiltersCombineWithTheGroupBy(t *testing.T) {
	d, reg := sumSetup(t)
	sumSeed(t, d, reg)
	q := sumQuery("status", agg(db.AggSum, "amount"))
	q.Filters = []db.Filter{{Field: "amount", Op: db.Ge(dec("10.00"))}, {Field: "status", Op: db.Ne(text("open"))}}
	result := mustSummary(t, d, reg, clerk(), "sale", q)
	// Ne leaves out the null status, and the amount filter leaves out the 5.00 and the nulls.
	strsEqual(t, groupKeys(result), []string{"draft=2"})
	wantValue(t, result.Groups[0].Values["sum_amount"], dec("30.50"))
	// A filter is validated as in a list.
	bad := sumQuery("status")
	bad.Filters = []db.Filter{{Field: "nope", Op: db.IsNull(true)}}
	_, err := runSummary(d, reg, clerk(), "sale", bad)
	if !strings.Contains(refusedQuery(t, err), "unknown field") {
		t.Fatal(err)
	}
	bad.Filters = []db.Filter{{Field: "qty", Op: db.Like("1%")}}
	_, err = runSummary(d, reg, clerk(), "sale", bad)
	if !strings.Contains(refusedQuery(t, err), "text fields only") {
		t.Fatal(err)
	}
}

// Rust: summary.rs::every_kind_of_group_key_works
func TestEveryKindOfGroupKeyWorks(t *testing.T) {
	d, reg := sumSetup(t)
	c := clerk()
	addDoc(t, d, reg, c, "person", "email", text("zoe@example.com"))
	addDoc(t, d, reg, c, "person", "email", text("amy@example.com"))
	addSale(t, d, reg, c, "customer", text("zoe@example.com"), "paid", core.Bool(true), "qty", core.Int(2), "title", text("x"), "due", octDate(5))
	addSale(t, d, reg, c, "customer", text("zoe@example.com"), "paid", core.Bool(false), "qty", core.Int(2), "title", text("x"), "due", octDate(5))
	addSale(t, d, reg, c, "customer", text("amy@example.com"), "qty", core.Int(3), "title", text("y"), "due", octDate(6))
	by := func(field string) []string { return groupKeys(mustSummary(t, d, reg, c, "sale", sumQuery(field))) }
	// A link groups by the target's name (a tie in byte order).
	strsEqual(t, by("customer"), []string{"zoe@example.com=2", "amy@example.com=1"})
	strsEqual(t, by("paid"), []string{"false=1", "true=1", "null=1"})
	strsEqual(t, by("qty"), []string{"2=2", "3=1"})
	strsEqual(t, by("due"), []string{"2026-10-05=2", "2026-10-06=1"})
	strsEqual(t, by("title"), []string{"x=2", "y=1"})
	// docstatus is an Int on a submittable DocType; modified_by is text.
	strsEqual(t, by("docstatus"), []string{"0=3"})
	strsEqual(t, by("modified_by"), []string{"clerk=3"})
}

// Rust: summary.rs::the_group_by_and_the_aggregates_are_validated
func TestTheGroupByAndTheAggregatesAreValidated(t *testing.T) {
	d, reg := sumSetup(t)
	sumSeed(t, d, reg)
	check := func(q db.SummaryQuery, needle string) {
		t.Helper()
		_, err := runSummary(d, reg, clerk(), "sale", q)
		if message := refusedQuery(t, err); !strings.Contains(message, needle) {
			t.Errorf("%+v: %s", q, message)
		}
	}
	for _, c := range [][2]string{
		{"note", "text field"}, {"lines", "table field"}, {"amount", "decimal field"}, {"seen", "datetime field"},
		{"name", "cannot group by"}, {"created_at", "cannot group by"}, {"modified_at", "cannot group by"},
		{"version", "cannot group by"}, {"workflow_state", "unknown field"}, {"nope", "unknown field"}, {"", "unknown field"},
		{"id", "unknown field"}, {"status\" ; drop table doc_sale; --", "unknown field"},
	} {
		check(sumQuery(c[0]), c[1])
	}
	for _, c := range []struct {
		a      db.Aggregate
		needle string
	}{
		{agg(db.AggSum, "title"), "data field"}, {agg(db.AggAvg, "due"), "date field"}, {agg(db.AggSum, "due"), "date field"},
		{agg(db.AggMin, "title"), "data field"}, {agg(db.AggMax, "paid"), "check field"}, {agg(db.AggSum, "note"), "text field"},
		{agg(db.AggSum, "customer"), "link field"}, {agg(db.AggSum, "lines"), "table field"},
		// Standard columns are not aggregated.
		{agg(db.AggSum, "version"), "unknown field"}, {agg(db.AggMax, "docstatus"), "unknown field"}, {agg(db.AggSum, "nope"), "unknown field"},
	} {
		check(sumQuery("status", c.a), c.needle)
	}
	check(sumQuery("status", agg(db.AggSum, "qty"), agg(db.AggSum, "qty")), "twice")
	seven := []db.Aggregate{agg(db.AggSum, "qty"), agg(db.AggAvg, "qty"), agg(db.AggMin, "qty"), agg(db.AggMax, "qty"),
		agg(db.AggSum, "amount"), agg(db.AggAvg, "amount"), agg(db.AggMin, "amount")}
	check(sumQuery("status", seven...), "at most 6")
	// Six are fine.
	mustSummary(t, d, reg, clerk(), "sale", sumQuery("status", seven[:6]...))
	// Min and max take dates, sum and avg take neither dates nor text.
	mustSummary(t, d, reg, clerk(), "sale", sumQuery("status", agg(db.AggMax, "due")))
}

// Rust: summary.rs::workflow_state_is_a_group_key_only_with_a_workflow
func TestWorkflowStateIsAGroupKeyOnlyWithAWorkflow(t *testing.T) {
	// The check above covers a DocType without a workflow; a DocType without submittable has no docstatus
	// either.
	d, reg := sumSetup(t)
	_, err := runSummary(d, reg, clerk(), "tag", sumQuery("docstatus"))
	if !strings.Contains(refusedQuery(t, err), "unknown field") {
		t.Fatal(err)
	}
}

// Rust: summary.rs::text_keys_are_ordered_by_bytes_after_the_count
func TestTextKeysAreOrderedByBytesAfterTheCount(t *testing.T) {
	d, reg := sumSetup(t)
	c := clerk()
	for _, label := range []string{"b", "B", "a", "_", "Z", "b"} {
		addDoc(t, d, reg, c, "tag", "label", text(label))
	}
	// b twice first; then the ties by byte order, not by locale.
	strsEqual(t, groupKeys(mustSummary(t, d, reg, c, "tag", sumQuery("label"))), []string{"b=2", "B=1", "Z=1", "_=1", "a=1"})
}

// Rust: summary.rs::more_than_500_groups_are_cut_and_marked
func TestMoreThan500GroupsAreCutAndMarked(t *testing.T) {
	d, reg := sumSetup(t)
	insert := func(count int) string {
		return fmt.Sprintf("INSERT INTO doc_tag (id, name, owner, created_at, modified_at, modified_by, label) "+
			"SELECT gen_random_uuid(), 'tag-' || n, 'sys', now(), now(), 'sys', 'label-' || lpad(n::text, 4, '0') "+
			"FROM generate_series(1, %d) n", count)
	}
	d.Exec(insert(db.MaxGroups))
	result := mustSummary(t, d, reg, clerk(), "tag", sumQuery("label"))
	if len(result.Groups) != db.MaxGroups || result.Truncated {
		t.Fatal("exactly 500 groups are not truncated", len(result.Groups), result.Truncated)
	}
	d.Exec("INSERT INTO doc_tag (id, name, owner, created_at, modified_at, modified_by, label) " +
		"VALUES (gen_random_uuid(), 'tag-x', 'sys', now(), now(), 'sys', 'label-0001')")
	d.Exec("INSERT INTO doc_tag (id, name, owner, created_at, modified_at, modified_by, label) " +
		"VALUES (gen_random_uuid(), 'tag-y', 'sys', now(), now(), 'sys', 'label-9999')")
	result = mustSummary(t, d, reg, clerk(), "tag", sumQuery("label"))
	if !result.Truncated || len(result.Groups) != db.MaxGroups {
		t.Fatal(result.Truncated, len(result.Groups))
	}
	// The group with two documents is first; the cut takes the last keys.
	if first := result.Groups[0]; first.Key.Text != "label-0001" || first.Count != 2 {
		t.Fatal(first)
	}
	if last := result.Groups[len(result.Groups)-1]; last.Key.Text != "label-0500" {
		t.Fatal(last)
	}
}
