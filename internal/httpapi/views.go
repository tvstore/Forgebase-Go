package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// Saved list views and the group-by summary (M8.3; the Rust module views.rs of forgebase-http). The
// handlers only map JSON and parameters; who may see or change a view, and what a summary may read, is
// decided by internal/db (views.go and summary.go), which take the read rule from the same evaluator and
// list code as the resource routes.

func viewJSON(view *db.View) (map[string]any, error) {
	at, err := db.UTCRFC3339(view.ModifiedAt)
	if err != nil {
		return nil, err
	}
	definition, err := decodeJSON(string(view.Definition))
	if err != nil {
		return nil, err
	}
	var owner any
	if !view.Shared() {
		owner = view.Owner
	}
	return map[string]any{
		"id":          view.ID.String(),
		"label":       view.Label,
		"shared":      view.Shared(),
		"owner":       owner,
		"definition":  definition,
		"version":     view.Version,
		"modified_at": at,
	}, nil
}

// viewID is a view id from the path; one that is not a UUID names no view.
func viewID(text string) (uuid.UUID, error) {
	id, err := uuid.Parse(text)
	if err != nil {
		return uuid.UUID{}, errNotFound("saved view not found")
	}
	return id, nil
}

func problem(err error) error { return errValidation([]string{err.Error()}) }

// viewDefinition is the definition of a body: {"filters", "sort", "columns"}, each optional, in the forms
// of the list API. The filters and the sort go through the parsers of the list route; the checks against
// the DocType are internal/db's.
func viewDefinition(doctype *core.DocType, v any) (*db.ViewDefinition, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errValidation([]string{"`definition` must be an object"})
	}
	for _, key := range sortedKeys(m) {
		if key != "filters" && key != "sort" && key != "columns" {
			return nil, errValidation([]string{"unknown key " + core.DebugQuote(core.Shorten(key)) + " in `definition` (use filters, sort, columns)"})
		}
	}
	list := func(key string) ([]any, error) {
		switch items := m[key].(type) {
		case nil:
			if _, present := m[key]; present {
				return nil, errValidation([]string{"`definition." + key + "` must be a list"})
			}
			return []any{}, nil
		case []any:
			return items, nil
		}
		return nil, errValidation([]string{"`definition." + key + "` must be a list"})
	}
	filters, err := list("filters")
	if err != nil {
		return nil, err
	}
	strs := func(key string) ([]string, error) {
		items, err := list(key)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(items))
		for i, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, errValidation([]string{"`definition." + key + "` must be a list of strings"})
			}
			out[i] = s
		}
		return out, nil
	}
	sorts, err := strs("sort")
	if err != nil {
		return nil, err
	}
	columns, err := strs("columns")
	if err != nil {
		return nil, err
	}
	filterText, err := marshal(filters)
	if err != nil {
		return nil, errInternal(err)
	}
	query := db.ListQuery{}
	if query.Filters, err = ParseFilters(doctype, string(filterText)); err != nil {
		return nil, problem(err)
	}
	if query.Sort, err = ParseSort(sorts); err != nil {
		return nil, problem(err)
	}
	text, err := marshal(map[string]any{"filters": filters, "sort": sorts, "columns": columns})
	if err != nil {
		return nil, errInternal(err)
	}
	return &db.ViewDefinition{JSON: json.RawMessage(text), Query: query}, nil
}

// viewLabel is the label of a body: nil when absent.
func viewLabel(m map[string]any) (*string, error) {
	v, present := m["label"]
	if !present {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, errBadRequest("`label` must be a string")
	}
	return &s, nil
}

// viewRoute is what the view routes do first: authenticate (and read the body when there is one), then
// the DocType and the read right. The first value is the parsed body, nil for a route without one.
func (s *State) viewRoute(w http.ResponseWriter, r *request, hasBody bool, pathLen int) (auth *authenticated, body any, path []string, doctype *core.DocType, err error) {
	if auth, err = s.authenticate(r); err != nil {
		return
	}
	if hasBody {
		if body, err = s.readJSON(w, r); err != nil {
			return
		}
	}
	if path, err = r.pathParams(pathLen); err != nil {
		return
	}
	if doctype, err = topLevel(s.snapshot(), path[0]); err != nil {
		return
	}
	err = require(doctype, auth.caller(), []core.Right{core.RightRead}, core.RightRead)
	return
}

// handleViewList is GET /api/v1/views/{doctype}: the views the caller sees, shared first.
func handleViewList(s *State, w http.ResponseWriter, r *request) error {
	auth, _, _, doctype, err := s.viewRoute(w, r, false, 1)
	if err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	views, err := db.ListViews(r.Context(), s.pool, s.snapshot(), doctype.Name, auth.caller())
	if err != nil {
		return err
	}
	out := make([]any, len(views))
	for i, v := range views {
		if out[i], err = viewJSON(v); err != nil {
			return err
		}
	}
	writeData(w, http.StatusOK, out)
	return nil
}

// handleViewCreate is POST /api/v1/views/{doctype}: body {"label", "definition", "shared"?}; 201 with the
// view.
func handleViewCreate(s *State, w http.ResponseWriter, r *request) error {
	auth, body, _, doctype, err := s.viewRoute(w, r, true, 1)
	if err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	m, err := object(body, "label", "definition", "shared")
	if err != nil {
		return err
	}
	shared := false
	if v, present := m["shared"]; present {
		if shared, present = v.(bool); !present {
			return errBadRequest("`shared` must be true or false")
		}
	}
	label, err := viewLabel(m)
	if err != nil {
		return err
	}
	if label == nil {
		return errBadRequest("`label` must be a string")
	}
	raw, present := m["definition"]
	if !present {
		return errBadRequest("`definition` must be an object")
	}
	caller := auth.caller()
	definition, err := viewDefinition(core.ReadableDocTypeFor(doctype, caller), raw)
	if err != nil {
		return err
	}
	view, err := db.CreateView(r.Context(), s.pool, s.snapshot(), doctype.Name, caller, *label, definition, shared)
	if err != nil {
		return err
	}
	out, err := viewJSON(view)
	if err != nil {
		return err
	}
	writeData(w, http.StatusCreated, out)
	return nil
}

// handleViewUpdate is PATCH /api/v1/views/{doctype}/{id}: body {"version", "label"?, "definition"?}.
func handleViewUpdate(s *State, w http.ResponseWriter, r *request) error {
	auth, body, path, doctype, err := s.viewRoute(w, r, true, 2)
	if err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	id, err := viewID(path[1])
	if err != nil {
		return err
	}
	m, err := object(body, "version", "label", "definition")
	if err != nil {
		return err
	}
	expected, err := version(m)
	if err != nil {
		return err
	}
	caller := auth.caller()
	var changes db.ViewChanges
	if raw, present := m["definition"]; present {
		if changes.Definition, err = viewDefinition(core.ReadableDocTypeFor(doctype, caller), raw); err != nil {
			return err
		}
	}
	if changes.Label, err = viewLabel(m); err != nil {
		return err
	}
	view, err := db.UpdateView(r.Context(), s.pool, s.snapshot(), doctype.Name, id, expected, caller, changes)
	if err != nil {
		return err
	}
	out, err := viewJSON(view)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, out)
	return nil
}

// handleViewDelete is DELETE /api/v1/views/{doctype}/{id}?version=N: 204 without a body.
func handleViewDelete(s *State, w http.ResponseWriter, r *request) error {
	auth, _, path, doctype, err := s.viewRoute(w, r, false, 2)
	if err != nil {
		return err
	}
	query, err := params(r, "version")
	if err != nil {
		return err
	}
	expected, err := versionParam(query)
	if err != nil {
		return err
	}
	id, err := viewID(path[1])
	if err != nil {
		return err
	}
	if err := db.DeleteView(r.Context(), s.pool, s.snapshot(), doctype.Name, id, expected, auth.caller()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// summaryAggregates is aggregates=sum:amount,avg:amount: FUNCTION:FIELD pairs (the count is always there).
func summaryAggregates(text string) ([]db.Aggregate, error) {
	if text == "" {
		return nil, nil
	}
	var out []db.Aggregate
	for _, item := range strings.Split(text, ",") {
		function, field, ok := strings.Cut(item, ":")
		if !ok {
			return nil, errInvalidQuery("invalid aggregate " + core.DebugQuote(core.Shorten(item)) + ": use FUNCTION:FIELD, for example sum:amount")
		}
		fn, ok := db.AggregateFromName(function)
		if !ok {
			return nil, errInvalidQuery("unknown aggregate function " + core.DebugQuote(core.Shorten(function)) +
				" (use sum, avg, min or max; the count is always included)")
		}
		out = append(out, db.Aggregate{Fn: fn, Field: field})
	}
	return out, nil
}

// handleSummary is GET /api/v1/summary/{doctype}?group_by=FIELD&aggregates=...&filters=...: the documents
// the caller may read, grouped. A separate path, because /resource/{doctype}/summary would collide with a
// document named summary.
func handleSummary(s *State, w http.ResponseWriter, r *request) error {
	auth, _, _, doctype, err := s.viewRoute(w, r, false, 1)
	if err != nil {
		return err
	}
	query, err := params(r, "group_by", "aggregates", "filters")
	if err != nil {
		return err
	}
	groupBy, ok := query["group_by"]
	if !ok {
		return errInvalidQuery("`group_by` is required")
	}
	caller := auth.caller()
	summaryQuery := db.SummaryQuery{GroupBy: groupBy}
	if text, ok := query["filters"]; ok {
		if summaryQuery.Filters, err = ParseFilters(core.ReadableDocTypeFor(doctype, caller), text); err != nil {
			return syntaxToQuery(err)
		}
	}
	if summaryQuery.Aggregates, err = summaryAggregates(query["aggregates"]); err != nil {
		return err
	}
	summary, err := db.Summary(r.Context(), s.pool, s.snapshot(), doctype.Name, caller, summaryQuery)
	if err != nil {
		return err
	}
	groups := make([]any, len(summary.Groups))
	for i, g := range summary.Groups {
		key, err := db.ValueToJSON("key", g.Key)
		if err != nil {
			return err
		}
		values := map[string]any{}
		for name, v := range g.Values {
			if values[name], err = db.ValueToJSON(name, v); err != nil {
				return err
			}
		}
		groups[i] = map[string]any{"key": key, "count": g.Count, "values": values}
	}
	writeData(w, http.StatusOK, map[string]any{"groups": groups, "truncated": summary.Truncated})
	return nil
}
