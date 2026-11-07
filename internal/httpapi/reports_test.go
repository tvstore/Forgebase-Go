package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// Query reports over HTTP (M8.4, ADR 0016): the list and the run route with each status code,
// authentication, CSRF, the envelopes, the role gate, the parameter errors, the timeout, the read-only
// refusal, the titles of Link columns and the fact that the SQL is never sent. Ported from
// crates/forgebase-http/tests/reports.rs.

const salesSQL = "SELECT customer, orders, total, day FROM sales " +
	"WHERE day >= $1 AND day <= $2 AND ($3::text IS NULL OR customer = $3) " +
	"AND ($4::text IS NULL OR kind = $4) ORDER BY total DESC"

func rpParam(name string, kind core.ParamKind, required bool) core.ReportParam {
	return core.ReportParam{Name: name, Label: name, Kind: kind, Required: required}
}

func rpColumn(name string, kind core.ColumnKind) core.ReportColumn {
	return core.ReportColumn{Name: name, Label: name, Kind: kind}
}

func rpReport(name, label string, roles []string, sql string) *core.ReportDef {
	return &core.ReportDef{App: "t", Name: name, Label: label, SQL: sql, Roles: roles,
		Columns: []core.ReportColumn{rpColumn("n", core.ColumnKind{Kind: core.ColumnInt})},
		Timeout: core.DefaultTimeout, MaxRows: core.DefaultMaxRows}
}

func reportList() []*core.ReportDef {
	byCustomer := rpReport("by_customer", "Sales by customer", []string{"Sales Manager"}, salesSQL)
	byCustomer.Params = []core.ReportParam{
		rpParam("from", core.ParamKind{Kind: core.ParamDate}, true),
		rpParam("to", core.ParamKind{Kind: core.ParamDate}, true),
		rpParam("customer", core.ParamKind{Kind: core.ParamLink, DocType: "ledger"}, false),
		rpParam("kind", core.ParamKind{Kind: core.ParamSelect, Options: []string{"retail", "online"}}, false),
	}
	byCustomer.Columns = []core.ReportColumn{
		rpColumn("customer", core.ColumnKind{Kind: core.ColumnLink, DocType: "ledger"}),
		rpColumn("orders", core.ColumnKind{Kind: core.ColumnInt}),
		rpColumn("total", core.ColumnKind{Kind: core.ColumnDecimal}),
		rpColumn("day", core.ColumnKind{Kind: core.ColumnDate}),
	}
	byCustomer.MaxRows = 3
	slow := rpReport("slow", "A slow one", []string{"Sales Manager"}, "SELECT pg_sleep(10)::text")
	slow.Columns = []core.ReportColumn{rpColumn("s", core.ColumnKind{Kind: core.ColumnData})}
	slow.Timeout = 200 * time.Millisecond
	writer := rpReport("writer", "Writer", []string{"Sales Manager"}, "UPDATE sales SET total = 0 RETURNING orders")
	writer.Columns = []core.ReportColumn{rpColumn("orders", core.ColumnKind{Kind: core.ColumnInt})}
	audit := rpReport("audit", "Audit", []string{"Auditor"}, "SELECT 1 FROM sales")
	return []*core.ReportDef{byCustomer, slow, writer, audit}
}

// reportEnv is the test DocTypes and reports, on a server with config; poolSize > 0 gives the server a
// pool of its own of that size (the test database keeps its own pool for env.db).
func reportEnv(t *testing.T, config Config, reports []*core.ReportDef, poolSize int) *env {
	t.Helper()
	registry := testRegistry(t)
	for _, r := range reports {
		if !registry.AddReport(r) {
			t.Fatalf("duplicate report %s", r.FullName())
		}
	}
	e := newEnvRegistry(t, config, registry)
	if poolSize > 0 {
		pool, err := db.ConnectSized(context.Background(), e.db.URL(), poolSize)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		e.state = NewState(pool, registry, config)
		e.h = ImplicitSites(e.state)
	}
	return e
}

type reportSetup struct {
	e                        *env
	mgr, reader, clerk, root session
	ledger                   string
}

func newReportSetup(t *testing.T) *reportSetup {
	t.Helper()
	e := reportEnv(t, DefaultConfig(), reportList(), 0)
	e.db.Exec("CREATE TABLE sales (customer text, orders int, total numeric, day date, kind text)")
	e.user("mgr", "Sales Manager", "Keeper")
	// Runs reports but may not read the ledger: no titles for them.
	e.user("reader", "Sales Manager")
	e.user("clerk", "Clerk")
	e.user("root", "Administrator")
	e.user("audit", "Auditor")
	s := &reportSetup{e: e, mgr: e.login("mgr"), reader: e.login("reader"), clerk: e.login("clerk"), root: e.login("root")}
	s.ledger = e.adminInsert(s.root, "ledger", J{"title": "Acme Ltd"})
	e.db.Exec(fmt.Sprintf(`INSERT INTO sales VALUES ('%[1]s', 3, 10.50, '2026-10-02', 'retail'),
		('%[1]s', 1, 99.99, '2026-10-20', 'online'),
		('other', 7, 20, '2026-10-10', 'retail'),
		('other', 2, 5, '2026-10-12', 'retail'),
		('late', 9, 1, '2026-12-01', 'retail')`, s.ledger))
	return s
}

func (s *reportSetup) run(who session, name string, body any) reply {
	return s.e.send(post("/api/v1/report/" + name).session(who).json(body))
}

func october() J { return J{"params": J{"from": "2026-10-01", "to": "2026-10-31"}} }

func (s *reportSetup) salesUnchanged(t *testing.T) {
	t.Helper()
	rows := s.e.db.Rows("SELECT sum(total)::text FROM sales")
	if rows[0][0] != "136.49" {
		t.Fatal(rows)
	}
}

// Rust: reports.rs::the_list_has_the_reports_the_caller_may_run_by_label_and_never_the_sql
func TestHTTPReportListHasTheReportsTheCallerMayRunByLabelAndNeverTheSql(t *testing.T) {
	s := newReportSetup(t)
	rep := s.e.send(get("/api/v1/report").session(s.mgr))
	wantStatus(t, rep, http.StatusOK, rep.text)
	data := rep.dataList()
	var names []string
	for _, r := range data {
		names = append(names, str(r, "name"))
	}
	// By label in byte order: "A slow one" < "Sales by customer" < "Writer"; the Auditor's report is not
	// listed.
	strsEq(t, names, "t.slow", "t.by_customer", "t.writer")
	first := data[1]
	eqJSON(t, at(first, "app"), `"t"`)
	eqJSON(t, at(first, "label"), `"Sales by customer"`)
	eqJSON(t, at(first, "params"), `[
		{"name":"from","label":"from","kind":"date","required":true,"default":null},
		{"name":"to","label":"to","kind":"date","required":true,"default":null},
		{"name":"customer","label":"customer","kind":"link","required":false,"default":null,"doctype":"ledger"},
		{"name":"kind","label":"kind","kind":"select","required":false,"default":null,"options":["retail","online"]}]`)
	eqJSON(t, at(first, "columns"), `[
		{"name":"customer","label":"customer","kind":"link","doctype":"ledger"},
		{"name":"orders","label":"orders","kind":"int"},
		{"name":"total","label":"total","kind":"decimal"},
		{"name":"day","label":"day","kind":"date"}]`)
	m := first.(map[string]any)
	if _, ok := m["sql"]; ok {
		t.Fatal("sql")
	}
	if _, ok := m["roles"]; ok {
		t.Fatal("roles")
	}
	// Administrator sees every report; a user without a report role sees none.
	if all := s.e.send(get("/api/v1/report").session(s.root)).dataList(); len(all) != 4 {
		t.Fatal(len(all))
	}
	none := s.e.send(get("/api/v1/report").session(s.clerk))
	wantStatus(t, none, http.StatusOK)
	eqJSON(t, none.data(), `[]`)
}

// Rust: reports.rs::a_report_runs_with_typed_params_and_returns_columns_rows_and_the_cut_flag
func TestHTTPAReportRunsWithTypedParamsAndReturnsColumnsRowsAndTheCutFlag(t *testing.T) {
	s := newReportSetup(t)
	rep := s.run(s.mgr, "t.by_customer", october())
	wantStatus(t, rep, http.StatusOK, rep.text)
	body := rep.obj().(map[string]any)
	if _, ok := body["link_titles"]; ok {
		t.Fatal(rep.text)
	}
	data := body["data"].(map[string]any)
	if len(data["columns"].([]any)) != 4 {
		t.Fatal(rep.text)
	}
	eqJSON(t, data["rows"], fmt.Sprintf(`[["%s",1,"99.99","2026-10-20"],["other",7,"20","2026-10-10"],["%[1]s",3,"10.50","2026-10-02"]]`, s.ledger))
	// 4 rows match, the limit is 3.
	eqJSON(t, data["truncated"], `true`)
	// Optional params narrow the result; null is the same as absent.
	one := s.run(s.mgr, "t.by_customer", J{"params": J{"from": "2026-10-01", "to": "2026-10-31", "customer": "other", "kind": nil}})
	if len(at(one.data(), "rows").([]any)) != 2 {
		t.Fatal(one.text)
	}
	eqJSON(t, at(one.data(), "truncated"), `false`)
	kind := s.run(s.mgr, "t.by_customer", J{"params": J{"from": "2026-10-01", "to": "2026-10-31", "kind": "online"}})
	if len(at(kind.data(), "rows").([]any)) != 1 {
		t.Fatal(kind.text)
	}
	// An Administrator runs it too.
	wantStatus(t, s.run(s.root, "t.by_customer", october()), http.StatusOK)
}

// Rust: reports.rs::link_titles_follow_the_read_rules_of_the_caller
func TestHTTPReportLinkTitlesFollowTheReadRulesOfTheCaller(t *testing.T) {
	s := newReportSetup(t)
	// ledger has a title field; Keeper reads ledgers, so the manager gets the title.
	path := "t.by_customer?link_titles=1"
	titled := s.run(s.mgr, path, october())
	wantStatus(t, titled, http.StatusOK, titled.text)
	eqJSON(t, at(titled.obj(), "link_titles"), fmt.Sprintf(`{"ledger":{"%s":"Acme Ltd"}}`, s.ledger))
	// The reader runs the report but may not read ledgers: the rows, and no titles.
	plain := s.run(s.reader, path, october())
	wantStatus(t, plain, http.StatusOK)
	if len(at(plain.data(), "rows").([]any)) != 3 {
		t.Fatal(plain.text)
	}
	eqJSON(t, at(plain.obj(), "link_titles"), `{}`)
	wantCode(t, s.run(s.mgr, "t.by_customer?link_titles=2", october()), http.StatusBadRequest, "invalid_query")
}

// Rust: reports.rs::a_report_the_caller_may_not_run_is_404_like_one_that_does_not_exist
func TestHTTPAReportTheCallerMayNotRunIs404LikeOneThatDoesNotExist(t *testing.T) {
	s := newReportSetup(t)
	denied := s.run(s.clerk, "t.by_customer", october())
	unknown := s.run(s.clerk, "t.nothing", october())
	unknownAdmin := s.run(s.root, "t.nothing", october())
	for _, rep := range []reply{denied, unknown, unknownAdmin} {
		wantCode(t, rep, http.StatusNotFound, "not_found", rep.text)
	}
	// The same body: nothing tells that the report exists.
	if denied.text != unknown.text {
		t.Fatal(denied.text, unknown.text)
	}
	// Not even the parameter errors of a report the caller may not run.
	if bad := s.run(s.clerk, "t.by_customer", J{"params": J{"nope": 1}}); bad.text != unknown.text {
		t.Fatal(bad.text)
	}
	for _, name := range []string{"by_customer", "t.", ".by_customer", "t.by_customer.x"} {
		wantStatus(t, s.run(s.mgr, name, october()), http.StatusNotFound, name)
	}
}

// Rust: reports.rs::parameter_problems_are_422_with_a_detail_per_parameter
func TestHTTPParameterProblemsAre422WithADetailPerParameter(t *testing.T) {
	s := newReportSetup(t)
	// Wrong kinds are found while the body is mapped.
	kinds := s.run(s.mgr, "t.by_customer", J{"params": J{"from": "yesterday", "to": 5, "customer": 7}})
	wantCode(t, kinds, http.StatusUnprocessableEntity, "validation_failed", kinds.text)
	details := kinds.details()
	if len(details) != 3 {
		t.Fatal(details)
	}
	for _, prefix := range []string{"field `from`:", "field `to`:", "field `customer`:"} {
		found := false
		for _, d := range details {
			found = found || strings.HasPrefix(d, prefix)
		}
		if !found {
			t.Fatalf("%s in %v", prefix, details)
		}
	}
	// Missing, unknown and not an option are found by the checks of the report.
	rules := s.run(s.mgr, "t.by_customer", J{"params": J{"kind": "wholesale", "extra": true}})
	wantStatus(t, rules, http.StatusUnprocessableEntity)
	strsEq(t, rules.details(), "unknown field `extra`", "field `from` is required", "field `to` is required",
		"field `kind`: `wholesale` is not one of the allowed options")
}

// Rust: reports.rs::a_hostile_param_is_data_and_the_table_survives
func TestHTTPAHostileParamIsDataAndTheTableSurvives(t *testing.T) {
	s := newReportSetup(t)
	rep := s.run(s.mgr, "t.by_customer", J{"params": J{"from": "2026-10-01", "to": "2026-10-31", "customer": "x'); DROP TABLE sales; --"}})
	wantStatus(t, rep, http.StatusOK, rep.text)
	eqJSON(t, at(rep.data(), "rows"), `[]`)
	s.salesUnchanged(t)
}

// Rust: reports.rs::bad_bodies_and_queries_are_refused_with_the_envelope
func TestHTTPBadReportBodiesAndQueriesAreRefusedWithTheEnvelope(t *testing.T) {
	s := newReportSetup(t)
	for _, body := range []any{[]any{}, J{"parms": J{}}, J{"params": []int{1}}} {
		wantCode(t, s.run(s.mgr, "t.by_customer", body), http.StatusBadRequest, "bad_request", fmt.Sprint(body))
	}
	if code := s.e.send(post("/api/v1/report/t.by_customer?limit=5").session(s.mgr).json(october())).code(); code != "invalid_query" {
		t.Fatal(code)
	}
	wantStatus(t, s.e.send(post("/api/v1/report/t.by_customer").session(s.mgr).raw("text/plain", []byte("x"))), http.StatusUnsupportedMediaType)
	if code := s.e.send(get("/api/v1/report?x=1").session(s.mgr)).code(); code != "invalid_query" {
		t.Fatal(code)
	}
	// Methods: the list is GET only and a run is POST only.
	wantStatus(t, s.e.send(post("/api/v1/report").session(s.mgr).json(J{})), http.StatusMethodNotAllowed)
	wantStatus(t, s.e.send(get("/api/v1/report/t.by_customer").session(s.mgr)), http.StatusMethodNotAllowed)
}

// Rust: reports.rs::the_body_limit_applies
func TestHTTPTheReportBodyLimitApplies(t *testing.T) {
	config := DefaultConfig()
	config.BodyLimitBytes = 256
	e := reportEnv(t, config, reportList(), 0)
	e.user("mgr", "Sales Manager")
	mgr := e.login("mgr")
	rep := e.send(post("/api/v1/report/t.by_customer").session(mgr).json(J{"params": J{"customer": strings.Repeat("x", 1000)}}))
	wantCode(t, rep, http.StatusRequestEntityTooLarge, "payload_too_large")
}

// Rust: reports.rs::authentication_and_csrf_are_asked_like_for_every_other_route
func TestHTTPReportAuthenticationAndCsrfAreAskedLikeForEveryOtherRoute(t *testing.T) {
	s := newReportSetup(t)
	body := october()
	// No credential.
	wantCode(t, s.e.send(get("/api/v1/report")), http.StatusUnauthorized, "unauthenticated")
	wantStatus(t, s.e.send(post("/api/v1/report/t.by_customer").json(body)), http.StatusUnauthorized)
	// The cookie without the CSRF token: refused, and the report did not run.
	wantCode(t, s.e.send(post("/api/v1/report/t.by_customer").cookie(s.mgr).json(body)), http.StatusForbidden, "csrf_failed")
	if code := s.e.send(post("/api/v1/report/t.by_customer").cookie(s.mgr).csrf("wrong").json(body)).code(); code != "csrf_failed" {
		t.Fatal(code)
	}
	// An API token needs no CSRF token.
	ok := s.e.send(post("/api/v1/report/t.by_customer").token(s.e.apiToken("mgr")).json(body))
	wantStatus(t, ok, http.StatusOK, ok.text)
	// A GET needs only the cookie.
	wantStatus(t, s.e.send(get("/api/v1/report").cookie(s.mgr)), http.StatusOK)
}

// Rust: reports.rs::a_slow_report_is_504_and_a_report_that_writes_is_an_internal_error
func TestHTTPASlowReportIs504AndAReportThatWritesIsAnInternalError(t *testing.T) {
	s := newReportSetup(t)
	slow := s.run(s.mgr, "t.slow", J{})
	wantCode(t, slow, http.StatusGatewayTimeout, "report_timeout", slow.text)
	if !strings.Contains(slow.errorObj()["message"].(string), "took too long") {
		t.Fatal(slow.text)
	}
	writer := s.run(s.mgr, "t.writer", J{})
	wantCode(t, writer, http.StatusInternalServerError, "internal", writer.text)
	if writer.errorObj()["message"] != "internal server error" {
		t.Fatal(writer.text)
	}
	s.salesUnchanged(t)
}

// Rust: reports.rs::the_sql_is_in_no_response
func TestHTTPTheSqlIsInNoReportResponse(t *testing.T) {
	s := newReportSetup(t)
	s.e.send(get("/api/v1/report").session(s.mgr))
	s.run(s.mgr, "t.by_customer", october())
	s.run(s.mgr, "t.by_customer", J{"params": J{"x": 1}})
	s.run(s.mgr, "t.slow", J{})
	s.run(s.mgr, "t.writer", J{})
	s.run(s.clerk, "t.by_customer", october())
	s.e.mu.Lock()
	log := append([]reply(nil), s.e.log...)
	s.e.mu.Unlock()
	if len(log) < 6 {
		t.Fatal(len(log))
	}
	for _, rep := range log {
		for _, needle := range []string{"SELECT", "pg_sleep", "UPDATE sales", "FROM sales", "$1"} {
			if strings.Contains(rep.text, needle) {
				t.Fatalf("%s in %s", needle, rep.text)
			}
		}
	}
}

// Rust: reports.rs::a_report_leaves_no_session_state_on_the_next_checkout
func TestHTTPAReportLeavesNoSessionStateOnTheNextCheckout(t *testing.T) {
	leak := rpReport("leak", "Leak", []string{"Sales Manager"}, "SELECT 1 AS n FROM (SELECT pg_advisory_lock(4242)) l")
	held := rpReport("held", "Held", []string{"Sales Manager"},
		"SELECT count(*) AS n FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 1 AND objid = 4242 AND pid = pg_backend_pid()")
	// One connection in the pool: the request after the report gets whatever the pool hands out next. The
	// connection a report ran on is closed, so it is a new one with no lock.
	e := reportEnv(t, DefaultConfig(), []*core.ReportDef{leak, held}, 1)
	e.user("mgr", "Sales Manager")
	mgr := e.login("mgr")
	run := func(name string) reply { return e.send(post("/api/v1/report/t." + name).session(mgr).json(J{})) }
	leaked := run("leak")
	wantStatus(t, leaked, http.StatusOK, leaked.text)
	heldReply := run("held")
	wantStatus(t, heldReply, http.StatusOK, heldReply.text)
	eqJSON(t, at(heldReply.data(), "rows"), `[[0]]`)
	// And the lock is gone from the server (the closed connection ends its session).
	started := time.Now()
	for {
		rows := e.db.Rows("SELECT count(*)::text FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 1 AND objid = 4242")
		if rows[0][0] == "0" {
			break
		}
		if time.Since(started) > 10*time.Second {
			t.Fatal("the advisory lock of the report is still held")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
