package db_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
	"github.com/tvstore/forgebase-go/internal/decimal"
)

// Query reports (M8.4, ADR 0016) against a real PostgreSQL: the role gate, parameter checks, SQL
// injection, the read-only transaction, the timeout, the row limit, the conversion of every column kind
// and the static check of the statements. Ported from crates/forgebase-db/tests/reports.rs.

func mkReport(sql string, params []core.ReportParam, columns ...core.ReportColumn) *core.ReportDef {
	return &core.ReportDef{
		App: "t", Name: "r", Label: "R", SQL: sql, Roles: []string{"Manager"},
		Params: params, Columns: columns, Timeout: core.DefaultTimeout, MaxRows: core.DefaultMaxRows,
	}
}

func rparam(name string, kind core.ParamKindKind, required bool) core.ReportParam {
	return core.ReportParam{Name: name, Label: name, Kind: core.ParamKind{Kind: kind}, Required: required}
}

func rcol(name string, kind core.ColumnKindKind) core.ReportColumn {
	return core.ReportColumn{Name: name, Label: name, Kind: core.ColumnKind{Kind: kind}}
}

func given(kv ...any) map[string]core.Value { return pairs(kv...) }

func runReport(t *testing.T, d *dbtest.DB, r *core.ReportDef, caller core.Caller, params map[string]core.Value) (*db.ReportResult, error) {
	t.Helper()
	conn, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	return db.RunReport(bg, conn.Conn(), r, caller, params)
}

func checkProblems(t *testing.T, d *dbtest.DB, reports ...*core.ReportDef) []string {
	t.Helper()
	conn, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	problems, err := db.CheckReports(bg, conn.Conn(), reports)
	if err != nil {
		t.Fatal(err)
	}
	return problems
}

// reportItems is a table of three rows that the reports read and the write tests try to change.
func reportItems(t *testing.T) *dbtest.DB {
	d := dbtest.New(t)
	d.Exec(`CREATE TABLE items (id int PRIMARY KEY, label text NOT NULL, amount numeric(12,2), day date);
		INSERT INTO items VALUES (1, 'bolt', 1.50, '2026-10-01'), (2, 'nut', 2.25, '2026-10-15'), (3, 'washer', NULL, NULL)`)
	return d
}

func itemsUnchanged(t *testing.T, d *dbtest.DB) {
	t.Helper()
	rowsEqual(t, d.Rows("SELECT id::text, label, coalesce(amount::text, '-') FROM items ORDER BY id"),
		[][]string{{"1", "bolt", "1.50"}, {"2", "nut", "2.25"}, {"3", "washer", "-"}})
}

const listSQL = "SELECT id, label FROM items WHERE ($1::text IS NULL OR label = $1) ORDER BY id"

func listReport() *core.ReportDef {
	return mkReport(listSQL, []core.ReportParam{rparam("label", core.ParamData, false)},
		rcol("id", core.ColumnInt), rcol("label", core.ColumnData))
}

func manager() core.Caller { return user("ann", "Manager") }

// Rust: reports.rs::only_the_roles_the_administrator_and_the_system_run_a_report
func TestOnlyTheRolesTheAdministratorAndTheSystemRunAReport(t *testing.T) {
	d := reportItems(t)
	report := listReport()
	for _, c := range []core.Caller{manager(), user("root", core.Administrator), sys("cli")} {
		res, err := runReport(t, d, report, c, nil)
		if err != nil || len(res.Rows) != 3 {
			t.Fatal(res, err)
		}
	}
	for _, roles := range [][]string{{"Clerk"}, {}} {
		clerk := user("bob", roles...)
		_, err := runReport(t, d, report, clerk, nil)
		if e := wantKind(t, err, db.ErrReportNotFound); e.Report != "t.r" {
			t.Fatal(e)
		}
		// The refusal comes before the parameters are looked at: it says nothing about them.
		_, err = runReport(t, d, report, clerk, given("nope", core.Null()))
		wantKind(t, err, db.ErrReportNotFound)
	}
}

// Rust: reports.rs::parameters_are_bound_in_order_and_an_absent_optional_one_is_null
func TestParametersAreBoundInOrderAndAnAbsentOptionalOneIsNull(t *testing.T) {
	d := reportItems(t)
	report := listReport()
	all, err := runReport(t, d, report, manager(), nil)
	if err != nil || len(all.Rows) != 3 || all.Truncated {
		t.Fatal(all, err)
	}
	one, err := runReport(t, d, report, manager(), given("label", text("nut")))
	if err != nil || len(one.Rows) != 1 || !one.Rows[0][0].Equal(core.Int(2)) || !one.Rows[0][1].Equal(text("nut")) {
		t.Fatal(one, err)
	}
	// An explicit null is the same as absent.
	null, err := runReport(t, d, report, manager(), given("label", core.Null()))
	if err != nil || len(null.Rows) != 3 {
		t.Fatal(null, err)
	}
}

// Rust: reports.rs::parameter_problems_are_reported_per_parameter
func TestParameterProblemsAreReportedPerParameter(t *testing.T) {
	d := reportItems(t)
	report := listReport()
	report.Params = []core.ReportParam{
		rparam("from", core.ParamDate, true),
		rparam("label", core.ParamData, false),
		{Name: "kind", Label: "kind", Kind: core.ParamKind{Kind: core.ParamSelect, Options: []string{"a", "b"}}},
	}
	_, err := runReport(t, d, report, manager(), given("label", core.Int(3), "kind", text("c"), "extra", text("x")))
	var lines []string
	for _, v := range validationOf(t, err) {
		lines = append(lines, v.Error())
	}
	strsEqual(t, lines, []string{
		"unknown field `extra`",
		"field `from` is required",
		"field `label`: expected text, found int",
		"field `kind`: `c` is not one of the allowed options",
	})
}

func echoReport() *core.ReportDef {
	return mkReport("SELECT $1::text", []core.ReportParam{rparam("v", core.ParamData, true)}, rcol("v", core.ColumnData))
}

// Rust: reports.rs::a_parameter_cannot_inject_sql
func TestAParameterCannotInjectSql(t *testing.T) {
	d := reportItems(t)
	report := listReport()
	for _, hostile := range []string{"'); DROP TABLE items; --", "x' OR '1'='1", "\\'; DELETE FROM items; --"} {
		res, err := runReport(t, d, report, manager(), given("label", text(hostile)))
		if err != nil || len(res.Rows) != 0 {
			t.Fatalf("%s: %v %v", hostile, res, err)
		}
	}
	itemsUnchanged(t, d)
	// A value that is data is returned as data.
	res, err := runReport(t, d, echoReport(), manager(), given("v", text("'); DROP TABLE items; --")))
	if err != nil || len(res.Rows) != 1 || !res.Rows[0][0].Equal(text("'); DROP TABLE items; --")) {
		t.Fatal(res, err)
	}
	itemsUnchanged(t, d)
}

// Rust: reports.rs::a_report_that_writes_fails_and_changes_nothing
func TestAReportThatWritesFailsAndChangesNothing(t *testing.T) {
	d := reportItems(t)
	for _, sql := range []string{
		"WITH d AS (DELETE FROM items RETURNING 1) SELECT count(*) FROM d",
		"UPDATE items SET label = 'x' RETURNING id",
		"INSERT INTO items VALUES (9, 'new', 1, NULL) RETURNING id",
	} {
		_, err := runReport(t, d, mkReport(sql, nil, rcol("n", core.ColumnInt)), manager(), nil)
		if e := wantKind(t, err, db.ErrReportReadOnly); e.Report != "t.r" {
			t.Fatalf("%s: %v", sql, e)
		}
		// The message never shows the SQL.
		if strings.Contains(err.Error(), "items") {
			t.Fatal(err)
		}
	}
	itemsUnchanged(t, d)
}

// Rust: reports.rs::a_statement_that_is_not_a_query_is_refused_before_it_runs
func TestAStatementThatIsNotAQueryIsRefusedBeforeItRuns(t *testing.T) {
	d := reportItems(t)
	for _, sql := range []string{"COMMIT", "DELETE FROM items"} {
		_, err := runReport(t, d, mkReport(sql, nil, rcol("n", core.ColumnInt)), manager(), nil)
		if e := wantKind(t, err, db.ErrReport); !strings.Contains(e.Message, "t.r") {
			t.Fatal(e)
		}
		if strings.Contains(err.Error(), "DELETE") {
			t.Fatal(err)
		}
	}
	itemsUnchanged(t, d)
}

// Rust: reports.rs::a_multi_statement_report_is_refused_and_changes_nothing
func TestAMultiStatementReportIsRefusedAndChangesNothing(t *testing.T) {
	// The extended protocol prepares one statement: a second one riding along never runs.
	d := reportItems(t)
	report := mkReport("SELECT 1; DELETE FROM items", nil, rcol("n", core.ColumnInt))
	_, err := runReport(t, d, report, manager(), nil)
	if err == nil || !strings.Contains(err.Error(), "multiple commands") {
		t.Fatal(err)
	}
	// The message never shows the SQL.
	if strings.Contains(err.Error(), "DELETE") || strings.Contains(err.Error(), "items") {
		t.Fatal(err)
	}
	itemsUnchanged(t, d)
	// The check at migrate says so, naming the report and not the SQL.
	found := checkProblems(t, d, report)
	if len(found) != 1 || !strings.Contains(found[0], "report `t.r`: the SQL does not prepare:") || strings.Contains(found[0], "DELETE") {
		t.Fatal(found)
	}
	itemsUnchanged(t, d)
}

// Rust: reports.rs::a_report_connection_is_closed_so_session_state_cannot_leak_to_the_next_checkout
func TestAReportConnectionIsClosedSoSessionStateCannotLeakToTheNextCheckout(t *testing.T) {
	// A pool of one connection makes the next checkout deterministic: it is the same pool, and a leaked
	// setting or lock would be on whatever connection it hands out.
	d := reportItems(t)
	pool, err := db.ConnectSized(bg, d.URL(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// Session-level state, which only the connection that ran the report would keep.
	leaky := mkReport("SELECT 1 AS n FROM (SELECT set_config('search_path', 'pg_catalog', false)) c, (SELECT pg_advisory_lock(4242)) l",
		nil, rcol("n", core.ColumnInt))
	func() {
		connection, err := db.GetReportConnection(bg, pool)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close(bg)
		res, err := db.RunReport(bg, connection.Conn(), leaky, manager(), nil)
		if err != nil || len(res.Rows) != 1 || !res.Rows[0][0].Equal(core.Int(1)) {
			t.Fatal(res, err)
		}
		// The advisory lock really is held by this connection (a session lock is not released by the
		// rollback that ends the report's transaction), so the check below proves something.
		var held int64
		if err := connection.Conn().QueryRow(bg, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 1 "+
			"AND objid = 4242 AND pid = pg_backend_pid()").Scan(&held); err != nil || held != 1 {
			t.Fatal(held, err)
		}
	}()
	// The next checkout is a new connection: the default setting, and no advisory lock held.
	var seen string
	if err := pool.QueryRow(bg, "SELECT current_setting('search_path')").Scan(&seen); err != nil || !strings.Contains(seen, "public") {
		t.Fatal(seen, err)
	}
	// The server notices the closed connection a moment later: poll for the lock to go.
	started := time.Now()
	for {
		var locks int64
		if err := pool.QueryRow(bg, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 1 AND objid = 4242").Scan(&locks); err != nil {
			t.Fatal(err)
		}
		if locks == 0 {
			break
		}
		if time.Since(started) > 10*time.Second {
			t.Fatal("the advisory lock of the report is still held")
		}
		time.Sleep(50 * time.Millisecond)
	}
	itemsUnchanged(t, d)
}

// Rust: reports.rs::the_timeout_stops_a_slow_report_and_the_connection_stays_usable
func TestTheTimeoutStopsASlowReportAndTheConnectionStaysUsable(t *testing.T) {
	d := reportItems(t)
	report := mkReport("SELECT pg_sleep(10)::text", nil, rcol("s", core.ColumnData))
	report.Timeout = 200 * time.Millisecond
	conn, err := d.Pool.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	started := time.Now()
	_, err = db.RunReport(bg, conn.Conn(), report, manager(), nil)
	if e := wantKind(t, err, db.ErrReportTimeout); e.Report != "t.r" {
		t.Fatal(e)
	}
	// The ten seconds of the statement were not waited for.
	if time.Since(started) > 5*time.Second {
		t.Fatal(time.Since(started))
	}
	// The same connection runs the next report, and the timeout did not stay on it.
	res, err := db.RunReport(bg, conn.Conn(), listReport(), manager(), nil)
	if err != nil || len(res.Rows) != 3 {
		t.Fatal(res, err)
	}
	var setting string
	if err := conn.QueryRow(bg, "SHOW statement_timeout").Scan(&setting); err != nil || setting != "0" {
		t.Fatal(setting, err)
	}
}

// Rust: reports.rs::the_row_limit_cuts_the_result_and_says_so
func TestTheRowLimitCutsTheResultAndSaysSo(t *testing.T) {
	d := dbtest.New(t)
	report := mkReport("SELECT g FROM generate_series(1, 25) AS g", nil, rcol("g", core.ColumnInt))
	report.MaxRows = 10
	cut, err := runReport(t, d, report, manager(), nil)
	if err != nil || len(cut.Rows) != 10 || !cut.Truncated || !cut.Rows[9][0].Equal(core.Int(10)) {
		t.Fatal(cut, err)
	}
	// Exactly the limit is not truncated; one more is.
	for _, c := range []struct {
		max       uint32
		rows      int
		truncated bool
	}{{25, 25, false}, {24, 24, true}} {
		report.MaxRows = c.max
		res, err := runReport(t, d, report, manager(), nil)
		if err != nil || len(res.Rows) != c.rows || res.Truncated != c.truncated {
			t.Fatal(c, res, err)
		}
	}
}

// Rust: reports.rs::every_column_kind_is_converted_to_a_value
func TestEveryColumnKindIsConvertedToAValue(t *testing.T) {
	d := dbtest.New(t)
	report := mkReport("SELECT 'x'::text, 'y'::varchar, 7::int2, 8::int4, 9::int8, 1.50::numeric, "+
		"date '2026-10-30', timestamptz '2026-10-30 10:20:30+02', true, 'C-1'::text", nil,
		rcol("a", core.ColumnData), rcol("b", core.ColumnData), rcol("c", core.ColumnInt), rcol("d", core.ColumnInt),
		rcol("e", core.ColumnInt), rcol("f", core.ColumnDecimal), rcol("g", core.ColumnDate), rcol("h", core.ColumnDatetime),
		rcol("i", core.ColumnCheck),
		core.ReportColumn{Name: "j", Label: "j", Kind: core.ColumnKind{Kind: core.ColumnLink, DocType: "customer"}})
	res, err := runReport(t, d, report, manager(), nil)
	if err != nil || len(res.Rows) != 1 {
		t.Fatal(res, err)
	}
	want := []core.Value{text("x"), text("y"), core.Int(7), core.Int(8), core.Int(9), dec("1.50"), date(2026, 10, 30),
		core.Datetime(time.Date(2026, 10, 30, 8, 20, 30, 0, time.UTC)), core.Bool(true), text("C-1")}
	for i, w := range want {
		if !res.Rows[0][i].Equal(w) {
			t.Fatalf("column %d: %v, want %v", i, res.Rows[0][i], w)
		}
	}
	if got := res.Rows[0][5].Dec.String(); got != "1.50" {
		t.Fatal(got)
	}
	// Nulls of every kind.
	nulls := mkReport("SELECT NULL::text, NULL::int4, NULL::numeric, NULL::date, NULL::timestamptz, NULL::bool", nil,
		rcol("a", core.ColumnData), rcol("b", core.ColumnInt), rcol("c", core.ColumnDecimal), rcol("d", core.ColumnDate),
		rcol("e", core.ColumnDatetime), rcol("f", core.ColumnCheck))
	res, err = runReport(t, d, nulls, manager(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range res.Rows[0] {
		if v.Kind != core.KindNull {
			t.Fatal(res.Rows)
		}
	}
}

// Rust: reports.rs::parameters_of_every_kind_reach_the_statement_with_their_value
func TestParametersOfEveryKindReachTheStatementWithTheirValue(t *testing.T) {
	d := dbtest.New(t)
	report := mkReport("SELECT $1::int4 + 1, $2::numeric * 2, $3::date + 1, NOT $4::bool, $5::text, $6::text",
		[]core.ReportParam{
			rparam("i", core.ParamInt, true), rparam("d", core.ParamDecimal, true), rparam("day", core.ParamDate, true),
			rparam("b", core.ParamCheck, true),
			{Name: "l", Label: "l", Kind: core.ParamKind{Kind: core.ParamLink, DocType: "customer"}, Required: true},
			{Name: "s", Label: "s", Kind: core.ParamKind{Kind: core.ParamSelect, Options: []string{"a"}}, Required: true},
		},
		rcol("i", core.ColumnInt), rcol("d", core.ColumnDecimal), rcol("day", core.ColumnDate), rcol("b", core.ColumnCheck),
		rcol("l", core.ColumnData), rcol("s", core.ColumnData))
	res, err := runReport(t, d, report, manager(), given("i", core.Int(41), "d", dec("1.25"), "day", date(2026, 10, 30),
		"b", core.Bool(false), "l", text("C-1"), "s", text("a")))
	if err != nil || len(res.Rows) != 1 {
		t.Fatal(res, err)
	}
	want := []core.Value{core.Int(42), dec("2.50"), date(2026, 10, 31), core.Bool(true), text("C-1"), text("a")}
	for i, w := range want {
		if !res.Rows[0][i].Equal(w) {
			t.Fatalf("column %d: %v, want %v", i, res.Rows[0][i], w)
		}
	}
	// An integer that does not fit the type PostgreSQL inferred is a database error, not a wrap.
	_, err = runReport(t, d, report, manager(), given("i", core.Int(9223372036854775807), "d", core.Decimal(decimal.FromInt(1)),
		"day", date(2026, 10, 30), "b", core.Bool(true), "l", text("x"), "s", text("a")))
	wantKind(t, err, db.ErrPostgres)
}

// Rust: reports.rs::the_check_accepts_a_report_that_fits
func TestTheCheckAcceptsAReportThatFits(t *testing.T) {
	d := reportItems(t)
	good := mkReport("SELECT id, label, amount, day FROM items WHERE id >= $1 AND ($2::text IS NULL OR label = $2) AND day >= $3 AND amount > $4",
		[]core.ReportParam{rparam("min", core.ParamInt, true), rparam("label", core.ParamData, false),
			rparam("from", core.ParamDate, false), rparam("above", core.ParamDecimal, false)},
		rcol("id", core.ColumnInt), rcol("label", core.ColumnData), rcol("amount", core.ColumnDecimal), rcol("day", core.ColumnDate))
	if p := checkProblems(t, d, good, listReport()); len(p) != 0 {
		t.Fatal(p)
	}
}

// Rust: reports.rs::the_check_names_the_report_and_the_problem
func TestTheCheckNamesTheReportAndTheProblem(t *testing.T) {
	d := reportItems(t)
	sql := "SELECT id, label FROM items WHERE id >= $1"
	fewer := mkReport(sql, nil, rcol("id", core.ColumnInt), rcol("label", core.ColumnData))
	moreColumns := mkReport(sql, []core.ReportParam{rparam("min", core.ParamInt, false)}, rcol("id", core.ColumnInt))
	wrongType := mkReport(sql, []core.ReportParam{rparam("min", core.ParamInt, false)}, rcol("id", core.ColumnDecimal), rcol("label", core.ColumnCheck))
	wrongParam := mkReport(sql, []core.ReportParam{rparam("min", core.ParamDate, false)}, rcol("id", core.ColumnInt), rcol("label", core.ColumnData))
	broken := mkReport("SELECT nope FROM items", nil, rcol("id", core.ColumnInt))
	missingTable := mkReport("SELECT 1 FROM ghosts", nil, rcol("id", core.ColumnInt))
	found := checkProblems(t, d, fewer, moreColumns, wrongType, wrongParam, broken, missingTable)
	text := strings.Join(found, "\n")
	for _, needle := range []string{
		"report `t.r`: the SQL has 1 parameter(s) but 0 are declared",
		"report `t.r`: the SQL returns 2 column(s) but 1 are declared",
		"column 1 (`id`) is declared decimal, but the SQL returns int4",
		"column 2 (`label`) is declared check, but the SQL returns text",
		"parameter $1 (`min`) is declared date, but the SQL expects int4",
		"the SQL does not prepare: column \"nope\" does not exist",
		"the SQL does not prepare: relation \"ghosts\" does not exist",
	} {
		if !strings.Contains(text, needle) {
			t.Errorf("%s in\n%s", needle, text)
		}
	}
	if len(found) != 7 {
		t.Fatalf("%d: %s", len(found), text)
	}
	// Preparing wrote nothing and left no savepoint or transaction behind.
	itemsUnchanged(t, d)
}

// Rust: reports.rs::a_parameter_typed_as_text_fits_every_kind
func TestAParameterTypedAsTextFitsEveryKind(t *testing.T) {
	d := dbtest.New(t)
	report := mkReport("SELECT $1::text, $2::text, $3::text, $4::text",
		[]core.ReportParam{rparam("a", core.ParamInt, false), rparam("b", core.ParamDecimal, false),
			rparam("c", core.ParamDate, false), rparam("d", core.ParamCheck, false)},
		rcol("a", core.ColumnData), rcol("b", core.ColumnData), rcol("c", core.ColumnData), rcol("d", core.ColumnData))
	if p := checkProblems(t, d, report); len(p) != 0 {
		t.Fatal(p)
	}
	res, err := runReport(t, d, report, manager(), given("a", core.Int(5), "b", dec("1.25"), "c", date(2026, 10, 30), "d", core.Bool(true)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range res.Rows[0] {
		got = append(got, v.Text)
	}
	strsEqual(t, got, []string{"5", "1.25", "2026-10-30", "true"})
}

// Not in Rust (Rust reads dates in binary): date and timestamp columns do not depend on the DateStyle of
// the database or the role.
func TestDateColumnsDoNotDependOnTheDateStyle(t *testing.T) {
	d := dbtest.New(t)
	d.Exec(`ALTER DATABASE "` + d.Name() + `" SET DateStyle = 'SQL, DMY'`)
	// A new pool: its connections take the setting of the database.
	pool, err := db.ConnectSized(bg, d.URL(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var style string
	if err := pool.QueryRow(bg, "SHOW DateStyle").Scan(&style); err != nil || !strings.Contains(style, "SQL") {
		t.Fatal(style, err)
	}
	report := mkReport("SELECT date '2026-10-30', timestamptz '2026-10-30 10:20:30+02'", nil,
		rcol("d", core.ColumnDate), rcol("t", core.ColumnDatetime))
	connection, err := db.GetReportConnection(bg, pool)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(bg)
	res, err := db.RunReport(bg, connection.Conn(), report, manager(), nil)
	if err != nil || len(res.Rows) != 1 {
		t.Fatal(res, err)
	}
	if !res.Rows[0][0].Equal(date(2026, 10, 30)) ||
		!res.Rows[0][1].Equal(core.Datetime(time.Date(2026, 10, 30, 8, 20, 30, 0, time.UTC))) {
		t.Fatal(res.Rows[0])
	}
}
