package httpapi

import (
	"net/http"
	"sort"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// Query reports (M8.4, ADR 0016; the Rust module reports.rs of forgebase-http): the list of the reports a
// caller may run, and running one. The handlers map JSON; who may run a report, the parameter checks and
// the read-only run are decided by internal/core and internal/db. The SQL is never sent, not in a list and
// not in an error.

func paramJSON(param core.ReportParam) (map[string]any, error) {
	m := map[string]any{
		"name":     param.Name,
		"label":    param.Label,
		"kind":     param.Kind.Name(),
		"required": param.Required,
		"default":  nil,
	}
	if param.Default != nil {
		v, err := db.ValueToJSON(param.Name, *param.Default)
		if err != nil {
			return nil, err
		}
		m["default"] = v
	}
	switch param.Kind.Kind {
	case core.ParamLink:
		m["doctype"] = param.Kind.DocType
	case core.ParamSelect:
		m["options"] = append([]string{}, param.Kind.Options...)
	}
	return m, nil
}

func columnsJSON(report *core.ReportDef) []any {
	out := make([]any, len(report.Columns))
	for i, column := range report.Columns {
		m := map[string]any{"name": column.Name, "label": column.Label, "kind": column.Kind.Name()}
		if column.Kind.Kind == core.ColumnLink {
			m["doctype"] = column.Kind.DocType
		}
		out[i] = m
	}
	return out
}

// handleReportList is GET /api/v1/report: the reports the caller may run, by label (byte order, then
// name), with their parameters and columns.
func handleReportList(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	caller := auth.caller()
	var reports []*core.ReportDef
	for _, report := range s.snapshot().Reports() {
		if core.CanRunReport(report.Roles, caller) {
			reports = append(reports, report)
		}
	}
	sort.SliceStable(reports, func(i, j int) bool {
		if reports[i].Label != reports[j].Label {
			return reports[i].Label < reports[j].Label
		}
		return reports[i].FullName() < reports[j].FullName()
	})
	items := make([]any, len(reports))
	for i, report := range reports {
		ps := make([]any, len(report.Params))
		for j, param := range report.Params {
			var err error
			if ps[j], err = paramJSON(param); err != nil {
				return err
			}
		}
		items[i] = map[string]any{
			"name":    report.FullName(),
			"app":     report.App,
			"label":   report.Label,
			"params":  ps,
			"columns": columnsJSON(report),
		}
	}
	writeData(w, http.StatusOK, items)
	return nil
}

// findReport is the report name if the caller may run it, else the one not_found of every report that does
// not exist or is not for them. Before the body is mapped: the parameter names of a report the caller may
// not run are not for them. This asks the one evaluator of internal/core; internal/db checks again.
func findReport(registry *core.Registry, name string, caller core.Caller) (*core.ReportDef, error) {
	report := registry.Report(name)
	if report == nil || !core.CanRunReport(report.Roles, caller) {
		return nil, errNotFound("report not found")
	}
	return report, nil
}

// reportParams are the parameter values of a report run from its request body {"params": {..}} (optional).
func reportParams(report *core.ReportDef, body any) (map[string]core.Value, error) {
	m, err := object(body, "params")
	if err != nil {
		return nil, err
	}
	var given map[string]any
	switch p := m["params"].(type) {
	case nil:
	case map[string]any:
		given = p
	default:
		return nil, errBadRequest("`params` must be an object")
	}
	values := map[string]core.Value{}
	var problems []string
	for _, key := range sortedKeys(given) {
		var param *core.ReportParam
		for i := range report.Params {
			if report.Params[i].Name == key {
				param = &report.Params[i]
			}
		}
		if param == nil {
			// Reported by the checks of the report as an unknown parameter.
			values[key] = core.Null()
			continue
		}
		v, err := paramValue(key, param.Kind, given[key])
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		values[key] = v
	}
	if len(problems) > 0 {
		return nil, errValidation(problems)
	}
	return values, nil
}

// paramValue is the value of a report parameter (M8.4): mapped like a field of the same kind (Data, Link
// and Select are strings, Decimal a string, Date YYYY-MM-DD); null is no value.
func paramValue(name string, kind core.ParamKind, v any) (core.Value, error) {
	var ft core.FieldType
	switch kind.Kind {
	case core.ParamData:
		ft = core.FieldType{Kind: core.FieldData}
	case core.ParamInt:
		ft = core.FieldType{Kind: core.FieldInt}
	case core.ParamDecimal:
		ft = core.FieldType{Kind: core.FieldDecimal}
	case core.ParamDate:
		ft = core.FieldType{Kind: core.FieldDate}
	case core.ParamCheck:
		ft = core.FieldType{Kind: core.FieldCheck}
	case core.ParamLink:
		ft = core.FieldType{Kind: core.FieldLink, Target: kind.DocType}
	case core.ParamSelect:
		ft = core.FieldType{Kind: core.FieldSelect}
	}
	return toValue(name, ft, v)
}

// handleReportRun is POST /api/v1/report/{app}.{name} with {"params": {..}} (optional): runs the report
// and returns {"data": {"columns", "rows", "truncated"}}. ?link_titles=1 adds the titles of the Link
// columns as a sibling link_titles key, under the caller's read rules.
func handleReportRun(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	body, err := s.readJSON(w, r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(1)
	if err != nil {
		return err
	}
	caller := auth.caller()
	registry := s.snapshot()
	report, err := findReport(registry, path[0], caller)
	if err != nil {
		return err
	}
	query, err := params(r, "link_titles")
	if err != nil {
		return err
	}
	titles, err := linkTitlesFlag(query)
	if err != nil {
		return err
	}
	values, err := reportParams(report, body)
	if err != nil {
		return err
	}
	// A connection that is closed afterwards, not pooled: the report's SQL may leave session state
	// behind (db.ReportConnection).
	conn, err := db.GetReportConnection(r.Context(), s.pool)
	if err != nil {
		return err
	}
	defer conn.Close(r.Context())
	result, err := db.RunReport(r.Context(), conn.Conn(), report, caller, values)
	if err != nil {
		return err
	}
	rows := make([]any, len(result.Rows))
	for i, row := range result.Rows {
		cells := make([]any, len(row))
		for j, value := range row {
			if cells[j], err = db.ValueToJSON(report.Columns[j].Name, value); err != nil {
				return err
			}
		}
		rows[i] = cells
	}
	out := map[string]any{"data": map[string]any{
		"columns":   columnsJSON(report),
		"rows":      rows,
		"truncated": result.Truncated,
	}}
	if titles {
		// The connection of this request, as in withLinkTitles: a second one could never be had when
		// every pool slot is held by a request like this.
		linkTitles, err := db.ReportLinkTitles(r.Context(), conn.Conn(), registry, caller, report, result.Rows)
		if err != nil {
			return err
		}
		out["link_titles"] = linkTitles
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
