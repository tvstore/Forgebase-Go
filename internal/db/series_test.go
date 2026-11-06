package db_test

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Naming series (M2.4) against a real PostgreSQL: the counter lives in fb_series and moves with
// the insert's transaction, so numbering has no gaps per prefix. Ported from
// crates/forgebase-db/tests/series.rs.

const seriesInvoice = `
name = "invoice"
label = "Invoice"
naming = { series = "INV-.YYYY.-.#####" }
[[fields]]
name = "ref"
type = "data"
unique = true
`

// seriesBill has the same prefix as seriesInvoice with another width: they share a counter.
const seriesBill = `
name = "bill"
label = "Bill"
naming = { series = "INV-.YYYY.-.#######.-.MM" }
`

const seriesTicket = `
name = "ticket"
label = "Ticket"
naming = { series = "T-.YY.MM.DD.-.###" }
`

const seriesOrder = `
name = "order"
label = "Order"
submittable = true
naming = { series = "SO-.####" }
`

// refDoc is a document with an optional ref value ("" for none).
func refDoc(reference string) core.Document {
	if reference == "" {
		return doc()
	}
	return doc("ref", text(reference))
}

// utc is the UTC date of the database as YYYY, YY, MM, DD.
func utc(d *dbtest.DB) [4]string {
	r := d.Rows("SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYY'), to_char(now() AT TIME ZONE 'UTC', 'YY'), " +
		"to_char(now() AT TIME ZONE 'UTC', 'MM'), to_char(now() AT TIME ZONE 'UTC', 'DD')")[0]
	return [4]string{r[0], r[1], r[2], r[3]}
}

func counters(d *dbtest.DB) [][2]string {
	out := [][2]string{}
	for _, r := range d.Rows("SELECT prefix, current::text FROM fb_series ORDER BY prefix") {
		out = append(out, [2]string{r[0], r[1]})
	}
	return out
}

// Rust: series.rs::names_come_from_the_series_with_the_utc_date_of_the_transaction
func TestNamesComeFromTheSeriesWithTheUTCDateOfTheTransaction(t *testing.T) {
	d, reg := setup(t, seriesInvoice, seriesTicket)
	date := utc(d)
	yyyy, yy, mm, dd := date[0], date[1], date[2], date[3]
	for n := 1; n <= 3; n++ {
		stored := mustInsert(t, d, reg, "invoice", doc(), sys("ann"))
		if stored.Name != fmt.Sprintf("INV-%s-%05d", yyyy, n) || stored.Version != 1 {
			t.Error(stored.Name)
		}
	}
	ticket := mustInsert(t, d, reg, "ticket", core.Document{}, sys("ann"))
	if ticket.Name != fmt.Sprintf("T-%s%s%s-001", yy, mm, dd) {
		t.Error(ticket.Name)
	}
	// One counter row per prefix, holding the last number.
	want(t, counters(d), [][2]string{{"INV-" + yyyy + "-", "3"}, {fmt.Sprintf("T-%s%s%s-", yy, mm, dd), "1"}}, "counters")
}

// Rust: series.rs::the_date_is_the_utc_date_whatever_the_session_time_zone
func TestTheDateIsTheUTCDateWhateverTheSessionTimeZone(t *testing.T) {
	d, reg := setup(t, seriesTicket)
	date := utc(d)
	conn, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// A zone 14 hours ahead of UTC: its local date differs from the UTC date for a third of the day;
	// the name must not follow it.
	if _, err := conn.Exec(bg, "SET TIME ZONE 'Pacific/Kiritimati'"); err != nil {
		t.Fatal(err)
	}
	stored, err := db.Insert(bg, conn, reg, "ticket", core.Document{}, sys("ann"))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != fmt.Sprintf("T-%s%s%s-001", date[1], date[2], date[3]) {
		t.Error(stored.Name)
	}
}

// Rust: series.rs::patterns_with_the_same_prefix_share_one_counter
func TestPatternsWithTheSamePrefixShareOneCounter(t *testing.T) {
	d, reg := setup(t, seriesInvoice, seriesBill)
	date := utc(d)
	yyyy, mm := date[0], date[2]
	a := mustInsert(t, d, reg, "invoice", doc(), sys("ann"))
	b := mustInsert(t, d, reg, "bill", core.Document{}, sys("ann"))
	a2 := mustInsert(t, d, reg, "invoice", doc(), sys("ann"))
	if a.Name != fmt.Sprintf("INV-%s-00001", yyyy) {
		t.Error(a.Name)
	}
	// The other width and the date part after the number; the number goes on counting.
	if b.Name != fmt.Sprintf("INV-%s-0000002-%s", yyyy, mm) || a2.Name != fmt.Sprintf("INV-%s-00003", yyyy) {
		t.Error(b.Name, a2.Name)
	}
	want(t, counters(d), [][2]string{{"INV-" + yyyy + "-", "3"}}, "counters")
}

// Rust: series.rs::a_number_wider_than_the_hashes_is_printed_in_full
func TestANumberWiderThanTheHashesIsPrintedInFull(t *testing.T) {
	d, reg := setup(t, seriesOrder)
	d.Exec("INSERT INTO fb_series (prefix, current) VALUES ('SO-', 9999)")
	stored := mustInsert(t, d, reg, "order", core.Document{}, sys("ann"))
	if stored.Name != "SO-10000" {
		t.Error(stored.Name)
	}
}

// Rust: series.rs::a_name_cannot_be_given_with_a_series
func TestANameCannotBeGivenWithASeries(t *testing.T) {
	d, reg := setup(t, seriesInvoice)
	given := doc()
	given.Name = "INV-1"
	_, err := db.Insert(bg, d.Pool, reg, "invoice", given, sys("ann"))
	if e := wantKind(t, err, db.ErrNaming); !strings.Contains(e.Message, "naming series") {
		t.Error(e.Message)
	}
	if len(counters(d)) != 0 {
		t.Error("no number was taken")
	}
}

// Rust: series.rs::a_failed_insert_gives_its_number_back
func TestAFailedInsertGivesItsNumberBack(t *testing.T) {
	d, reg := setup(t, seriesInvoice)
	yyyy := utc(d)[0]
	first := mustInsert(t, d, reg, "invoice", refDoc("dup"), sys("ann"))
	if first.Name != fmt.Sprintf("INV-%s-00001", yyyy) {
		t.Error(first.Name)
	}
	// The unique violation happens after the number was taken, in the same transaction.
	_, err := db.Insert(bg, d.Pool, reg, "invoice", refDoc("dup"), sys("ann"))
	wantKind(t, err, db.ErrUniqueViolation)
	want(t, counters(d), [][2]string{{"INV-" + yyyy + "-", "1"}}, "counters")
	next := mustInsert(t, d, reg, "invoice", refDoc("other"), sys("ann"))
	if next.Name != fmt.Sprintf("INV-%s-00002", yyyy) {
		t.Errorf("the number is reused, no gap: %s", next.Name)
	}
}

// refuse rejects every validate.
type refuse struct{ core.BaseHooks }

func (refuse) Validate(core.HookContext, *core.Document) error { return core.NewHookError("no") }

// Rust: series.rs::documents_refused_before_naming_take_no_number
func TestDocumentsRefusedBeforeNamingTakeNoNumber(t *testing.T) {
	d, _ := setup(t, seriesInvoice)
	hooked := registry(t, seriesInvoice)
	if err := hooked.AddHooks("invoice", refuse{}); err != nil {
		t.Fatal(err)
	}
	plain := registry(t, seriesInvoice)
	// A hook error, and a validation error (a value for an unknown field).
	_, err := db.Insert(bg, d.Pool, hooked, "invoice", doc(), sys("ann"))
	wantKind(t, err, db.ErrHook)
	invalid := doc()
	invalid.Values["nope"] = core.Int(1)
	_, err = db.Insert(bg, d.Pool, plain, "invoice", invalid, sys("ann"))
	want(t, validationOf(t, err), []core.ValidationError{{Kind: core.ErrUnknownField, Field: "nope"}}, "errors")
	if len(counters(d)) != 0 {
		t.Error("a number was taken")
	}
	first := mustInsert(t, d, plain, "invoice", doc(), sys("ann"))
	if !strings.HasSuffix(first.Name, "-00001") {
		t.Error(first.Name)
	}
}

// Rust: series.rs::the_counter_moves_with_a_callers_transaction
func TestTheCounterMovesWithACallersTransaction(t *testing.T) {
	d, reg := setup(t, seriesInvoice)
	conn, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	a, err := db.Insert(bg, tx, reg, "invoice", doc(), sys("ann"))
	if err != nil || !strings.HasSuffix(a.Name, "-00001") {
		t.Fatalf("%v %v", a, err)
	}
	// A failed call inside the transaction (a savepoint) gives only its own number back.
	bad := doc()
	bad.Name = "x"
	if _, err := db.Insert(bg, tx, reg, "invoice", bad, sys("ann")); err == nil {
		t.Fatal("the insert worked")
	}
	b, err := db.Insert(bg, tx, reg, "invoice", doc(), sys("ann"))
	if err != nil || !strings.HasSuffix(b.Name, "-00002") {
		t.Fatalf("%v %v", b, err)
	}
	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	// Everything of the transaction is gone, the counter row too.
	if len(counters(d)) != 0 {
		t.Error("the counter survived")
	}
	c, err := db.Insert(bg, conn, reg, "invoice", doc(), sys("ann"))
	if err != nil || !strings.HasSuffix(c.Name, "-00001") {
		t.Fatalf("%v %v", c, err)
	}
}

// Rust: series.rs::twenty_parallel_inserts_get_the_numbers_one_to_twenty
func TestTwentyParallelInsertsGetTheNumbersOneToTwenty(t *testing.T) {
	d, reg := setup(t, seriesInvoice)
	yyyy := utc(d)[0]
	names := make([]string, 20)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stored, err := db.Insert(bg, d.Pool, reg, "invoice", refDoc(fmt.Sprintf("r%d", i)), sys("ann"))
			if err != nil {
				t.Errorf("insert %d: %v", i, err)
				return
			}
			names[i] = stored.Name
		}()
	}
	wg.Wait()
	slices.Sort(names)
	var expected []string
	for n := 1; n <= 20; n++ {
		expected = append(expected, fmt.Sprintf("INV-%s-%05d", yyyy, n))
	}
	want(t, names, expected, "no gap and no duplicate")
	want(t, counters(d), [][2]string{{"INV-" + yyyy + "-", "20"}}, "counters")
}

// Rust: series.rs::parallel_inserts_with_failures_leave_no_gap
func TestParallelInsertsWithFailuresLeaveNoGap(t *testing.T) {
	d, reg := setup(t, seriesInvoice)
	// Ten inserts want the same unique value: one wins, nine fail after taking a number.
	results := make([]error, 10)
	names := make([]string, 10)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var stored *db.StoredDocument
			stored, results[i] = db.Insert(bg, d.Pool, reg, "invoice", refDoc("same"), sys("ann"))
			if results[i] == nil {
				names[i] = stored.Name
			}
		}()
	}
	wg.Wait()
	winners := 0
	for i, err := range results {
		if err == nil {
			winners++
			if !strings.HasSuffix(names[i], "-00001") {
				t.Error(names[i])
			}
			continue
		}
		wantKind(t, err, db.ErrUniqueViolation)
	}
	if winners != 1 {
		t.Errorf("%d winners", winners)
	}
	next := mustInsert(t, d, reg, "invoice", refDoc("next"), sys("ann"))
	if !strings.HasSuffix(next.Name, "-00002") {
		t.Error(next.Name)
	}
}

// Rust: series.rs::a_submittable_series_doctype_amends_by_its_own_rule_and_takes_no_number
func TestASubmittableSeriesDoctypeAmendsByItsOwnRuleAndTakesNoNumber(t *testing.T) {
	d, reg := setup(t, seriesOrder)
	one := mustInsert(t, d, reg, "order", core.Document{}, sys("ann"))
	if one.Name != "SO-0001" {
		t.Error(one.Name)
	}
	one = mustSubmit(t, d, reg, "order", one.Name, 1, sys("ann"))
	one = mustCancel(t, d, reg, "order", one.Name, 2, sys("ann"))
	amended := mustAmend(t, d, reg, "order", one.Name, 3, sys("ann"))
	if amended.Name != "SO-0001-1" || amended.AmendedFrom != "SO-0001" {
		t.Error(describeDoc(amended))
	}
	want(t, counters(d), [][2]string{{"SO-", "1"}}, "counters")
	// The next new order is numbered 2, and its amendments follow the same rule.
	two := mustInsert(t, d, reg, "order", core.Document{}, sys("ann"))
	if two.Name != "SO-0002" {
		t.Error(two.Name)
	}
	// Deleting a document does not give its number back (only a rollback does).
	if err := db.Delete(bg, d.Pool, reg, "order", two.Name, 1, sys("ann")); err != nil {
		t.Fatal(err)
	}
	three := mustInsert(t, d, reg, "order", core.Document{}, sys("ann"))
	if three.Name != "SO-0003" {
		t.Error(three.Name)
	}
}

// Rust: series.rs::the_series_prefix_is_bound_never_interpolated
func TestTheSeriesPrefixIsBoundNeverInterpolated(t *testing.T) {
	// A literal that would break out of a statement is stored as plain text.
	evil := `
name = "evil"
label = "Evil"
naming = { series = "x'');DROP TABLE fb_series;--.#" }
`
	d, reg := setup(t, evil)
	stored := mustInsert(t, d, reg, "evil", core.Document{}, sys("ann"))
	if stored.Name != "x'');DROP TABLE fb_series;--1" {
		t.Error(stored.Name)
	}
	want(t, counters(d), [][2]string{{"x'');DROP TABLE fb_series;--", "1"}}, "counters")
}
