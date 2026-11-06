package db

import (
	"fmt"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The JSON form of stored values and documents: the one place that decides how a StoredDocument is
// written as JSON (the Rust module json.rs).
//
// The HTTP API, the command-line tool and the webhook payloads all use it, so a receiver of a
// webhook sees the same document shape as a client of GET /api/v1/resource/.... The documents are
// plain map[string]any, which encoding/json writes with sorted keys, like serde_json's map.

// OutputError is a stored value that cannot be written as JSON (for example a year beyond 9999).
type OutputError struct {
	// Field is the field name (for a value in a row: "<table> row <n>: <field>").
	Field string
	// Problem is what is wrong.
	Problem string
}

func (e *OutputError) Error() string {
	return fmt.Sprintf("cannot format field `%s` as JSON: %s", e.Field, e.Problem)
}

const yearComponentProblem = "The year component cannot be formatted into the requested format."

// FormatDate is the JSON form of a Date: YYYY-MM-DD. The clock part of t is ignored.
func FormatDate(t time.Time) (string, error) {
	y, m, d := t.Date()
	if y < minYear || y > maxYear {
		return "", fmt.Errorf("%s", yearComponentProblem)
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, int(m), d), nil
}

// UTCRFC3339 is t in UTC as RFC 3339 with the shortest fractional second ("2026-03-01T10:00:00Z",
// "2026-03-01T10:00:00.5Z"), as the Rust time crate writes it. A year outside 0 to 9999 has no
// RFC 3339 form and is an error.
func UTCRFC3339(t time.Time) (string, error) {
	u := t.UTC()
	if y := u.Year(); y < minYear || y > maxYear {
		return "", fmt.Errorf("out of range once converted to UTC")
	}
	if y := u.Year(); y < 0 {
		return "", fmt.Errorf("%s", yearComponentProblem)
	}
	return u.Format(time.RFC3339Nano), nil
}

// ValueToJSON is one value as JSON: null, a string (text, decimal, date, datetime), an int64 or a
// bool. name is only used in the error.
func ValueToJSON(name string, v core.Value) (any, error) {
	output := func(problem string) error { return &OutputError{Field: name, Problem: problem} }
	switch v.Kind {
	case core.KindNull:
		return nil, nil
	case core.KindText:
		return v.Text, nil
	case core.KindInt:
		return v.Int, nil
	case core.KindDecimal:
		return v.Dec.String(), nil
	case core.KindBool:
		return v.Bool, nil
	case core.KindDate:
		s, err := FormatDate(v.Time)
		if err != nil {
			return nil, output(err.Error())
		}
		return s, nil
	case core.KindDatetime:
		s, err := UTCRFC3339(v.Time)
		if err != nil {
			return nil, output(err.Error())
		}
		return s, nil
	}
	return nil, output(fmt.Sprintf("no JSON form for a %s value", v.KindName()))
}

// DocumentToJSON is a stored document as a JSON object: the standard columns (and, for a
// submittable DocType, docstatus as 0, 1 or 2 and amended_from as a name or null; for a DocType
// with a workflow, workflow_state as the name of the state, absent while the document has none),
// the field values and, for each Table field, the array of its rows.
func DocumentToJSON(doc *StoredDocument) (map[string]any, error) {
	rfc3339 := func(field string, t time.Time) (string, error) {
		s, err := UTCRFC3339(t)
		if err != nil {
			return "", &OutputError{Field: field, Problem: err.Error()}
		}
		return s, nil
	}
	object := map[string]any{
		"id":          doc.ID.String(),
		"name":        doc.Name,
		"owner":       doc.Owner,
		"modified_by": doc.ModifiedBy,
		"version":     doc.Version,
	}
	var err error
	if object["created_at"], err = rfc3339("created_at", doc.CreatedAt); err != nil {
		return nil, err
	}
	if object["modified_at"], err = rfc3339("modified_at", doc.ModifiedAt); err != nil {
		return nil, err
	}
	// Only a submittable DocType has a docstatus: 0 draft, 1 submitted, 2 cancelled.
	if doc.DocStatus != nil {
		object["docstatus"] = int64(doc.DocStatus.Int16())
		if doc.AmendedFrom != "" {
			object["amended_from"] = doc.AmendedFrom
		} else {
			object["amended_from"] = nil
		}
	}
	// Only a DocType with a workflow has a state (a document that has none yet has no key).
	if doc.WorkflowState != "" {
		object["workflow_state"] = doc.WorkflowState
	}
	// In key order, so that the first problem reported is always the same one.
	for _, name := range sortedKeys(doc.Values) {
		if object[name], err = ValueToJSON(name, doc.Values[name]); err != nil {
			return nil, err
		}
	}
	for _, name := range sortedKeys(doc.Tables) {
		rows := doc.Tables[name]
		items := make([]any, 0, len(rows))
		for i, row := range rows {
			item := make(map[string]any, len(row))
			for _, field := range sortedKeys(row) {
				place := fmt.Sprintf("%s row %d: %s", name, i+1, field)
				if item[field], err = ValueToJSON(place, row[field]); err != nil {
					return nil, err
				}
			}
			items = append(items, item)
		}
		object[name] = items
	}
	return object, nil
}
