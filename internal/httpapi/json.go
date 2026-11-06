package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/decimal"
)

// JSON input -> core.Value mapping, shared by the HTTP API and the command-line tool (the Rust module
// forgebase-http json.rs; the output half is db.DocumentToJSON, and the filter and sort parsing of the list
// is in filters.go).
//
//	Field type              JSON
//	Data, Text, Select      string
//	Link                    string: the name of the target document
//	Table                   array of row objects in order, each mapped like a document (no nested
//	                        Table); null means no rows; in an update the array replaces the rows
//	Int                     integer (a float, or an integer outside the int64 range, is rejected)
//	Decimal                 string, for example "1234.50" (a JSON number is rejected so that no
//	                        precision is lost to floats); every form the decimal type parses is
//	                        accepted, for example "1e3", "+12.5", ".5"
//	Check                   true / false
//	Date                    string "YYYY-MM-DD"
//	Datetime                RFC 3339 string
//	any                     null for no value
//
// The input must be a JSON object. A duplicate key is accepted and the last one wins. Keys that are
// not fields of the DocType are passed on (with a null value) so that validation reports them
// together with the other problems.

// JSONErrorKind is the variant of a JSONError.
type JSONErrorKind int

const (
	// JSONSyntax: the input text is not valid JSON (Err has the reason).
	JSONSyntax JSONErrorKind = iota + 1
	// JSONNotAnObject: the input is valid JSON but not an object.
	JSONNotAnObject
	// JSONField: a value does not fit its field (Field, Problem).
	JSONField
	// JSONQuery: a filter or sort value cannot be used (Problem is the whole message).
	JSONQuery
)

// JSONError is why JSON input could not be mapped.
type JSONError struct {
	Kind    JSONErrorKind
	Field   string
	Problem string
	Err     error
}

func (e *JSONError) Error() string {
	switch e.Kind {
	case JSONSyntax:
		return "invalid JSON: " + e.Err.Error()
	case JSONNotAnObject:
		return "the data must be a JSON object"
	case JSONQuery:
		return e.Problem
	}
	return fmt.Sprintf("field `%s`: %s", core.Shorten(e.Field), e.Problem)
}

func (e *JSONError) Unwrap() error { return e.Err }

func bad(field, problem string) *JSONError {
	return &JSONError{Kind: JSONField, Field: field, Problem: problem}
}

// Input is the values and table rows of a JSON object.
type Input struct {
	// Values are the field values (every field except Table fields).
	Values map[string]core.Value
	// Tables are the rows of each Table field that the input mentions.
	Tables map[string][]core.Row
}

// decodeJSON parses one JSON value; numbers stay json.Number so that integers are exact.
func decodeJSON(text string) (any, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, &JSONError{Kind: JSONSyntax, Err: err}
	}
	// serde_json refuses text after the value ("trailing characters").
	if _, err := dec.Token(); err != io.EOF {
		return nil, &JSONError{Kind: JSONSyntax, Err: fmt.Errorf("trailing characters after the JSON value")}
	}
	return v, nil
}

// ParseInput parses JSON text into field values and table rows for doctype. registry provides the
// child DocTypes of its Table fields.
func ParseInput(registry *core.Registry, doctype *core.DocType, text string) (*Input, error) {
	v, err := decodeJSON(text)
	if err != nil {
		return nil, err
	}
	return InputFromJSON(registry, doctype, v)
}

// CheckInputSyntax is the syntax-only part of ParseInput: the text is JSON and an object. Field
// names and values are not looked at, so it needs no DocType (the command line runs it before the
// database is opened, and the full check later against the site's registry).
func CheckInputSyntax(text string) error {
	v, err := decodeJSON(text)
	if err != nil {
		return err
	}
	if _, ok := v.(map[string]any); !ok {
		return &JSONError{Kind: JSONNotAnObject}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// InputFromJSON is like ParseInput for JSON that is already parsed (numbers as json.Number). The
// keys are read in sorted order, so the first problem reported is always the same one.
func InputFromJSON(registry *core.Registry, doctype *core.DocType, v any) (*Input, error) {
	object, ok := v.(map[string]any)
	if !ok {
		return nil, &JSONError{Kind: JSONNotAnObject}
	}
	input := &Input{Values: map[string]core.Value{}, Tables: map[string][]core.Row{}}
	for _, name := range sortedKeys(object) {
		field := doctype.Field(name)
		switch {
		case field == nil:
			// Reported by validation as an unknown field.
			input.Values[name] = core.Null()
		case field.Type.Kind == core.FieldTable:
			rows, err := toRows(registry, name, field.Type.Target, object[name])
			if err != nil {
				return nil, err
			}
			input.Tables[name] = rows
		default:
			value, err := toValue(name, field.Type, object[name])
			if err != nil {
				return nil, err
			}
			input.Values[name] = value
		}
	}
	return input, nil
}

// toRows is the rows of a Table field: an array of objects (null is no rows).
func toRows(registry *core.Registry, name, target string, v any) ([]core.Row, error) {
	if v == nil {
		return []core.Row{}, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, bad(name, fmt.Sprintf("expected an array of rows, found %s", kind(v)))
	}
	child := registry.Get(target)
	if child == nil {
		return nil, bad(name, fmt.Sprintf("unknown child doctype `%s`", target))
	}
	rows := make([]core.Row, 0, len(items))
	for i, item := range items {
		inRow := func(problem string) *JSONError { return bad(name, fmt.Sprintf("row %d: %s", i+1, problem)) }
		object, ok := item.(map[string]any)
		if !ok {
			return nil, inRow(fmt.Sprintf("expected an object, found %s", kind(item)))
		}
		row := core.Row{}
		for _, field := range sortedKeys(object) {
			f := child.Field(field)
			if f == nil {
				// Reported by validation as an unknown field of the row.
				row[field] = core.Null()
				continue
			}
			value, err := toValue(field, f.Type, object[field])
			if err != nil {
				return nil, inRow(err.Error())
			}
			row[field] = value
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// toValue maps one JSON value to a value of field type ft. null is no value for every type.
func toValue(name string, ft core.FieldType, v any) (core.Value, error) {
	if v == nil {
		return core.Null(), nil
	}
	expected := func(what string) *JSONError {
		return bad(name, fmt.Sprintf("expected %s, found %s", what, kind(v)))
	}
	switch ft.Kind {
	case core.FieldData, core.FieldText, core.FieldSelect, core.FieldLink:
		s, ok := v.(string)
		if !ok {
			return core.Value{}, expected("a string")
		}
		return core.Text(s), nil
	case core.FieldInt:
		n, ok := v.(json.Number)
		if !ok {
			return core.Value{}, expected("an integer")
		}
		i, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return core.Value{}, bad(name, fmt.Sprintf("%s is not an integer in the range of a 64-bit signed integer", n))
		}
		return core.Int(i), nil
	case core.FieldDecimal:
		switch x := v.(type) {
		case string:
			d, err := decimal.Parse(x)
			if err != nil {
				return core.Value{}, bad(name, fmt.Sprintf("`%s` is not a decimal number: %s", core.Shorten(x), err))
			}
			return core.Decimal(d), nil
		case json.Number:
			return core.Value{}, bad(name, "a decimal must be a JSON string such as \"12.50\", not a number")
		}
		return core.Value{}, expected("a decimal string")
	case core.FieldCheck:
		b, ok := v.(bool)
		if !ok {
			return core.Value{}, expected("true or false")
		}
		return core.Bool(b), nil
	case core.FieldDate:
		s, ok := v.(string)
		if !ok {
			return core.Value{}, expected("a date string")
		}
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return core.Value{}, bad(name, fmt.Sprintf("`%s` is not a date in the form YYYY-MM-DD", core.Shorten(s)))
		}
		return core.Value{Kind: core.KindDate, Time: t}, nil
	case core.FieldDatetime:
		s, ok := v.(string)
		if !ok {
			return core.Value{}, expected("an RFC 3339 string")
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return core.Value{}, bad(name, fmt.Sprintf("`%s` is not an RFC 3339 date and time, e.g. 2024-03-15T10:20:30Z", core.Shorten(s)))
		}
		return core.Datetime(t), nil
	case core.FieldTable:
		return core.Value{}, bad(name, "a table field takes a list of rows, not a single value")
	}
	return core.Value{}, bad(name, "unknown field type")
}

// kind describes a JSON value for an error message.
func kind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case json.Number:
		return "a number"
	case string:
		return "a string"
	case []any:
		return "an array"
	}
	return "an object"
}
