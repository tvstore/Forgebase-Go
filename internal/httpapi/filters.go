package httpapi

import (
	"fmt"
	"strings"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// The filter and sort values of a list (the Rust functions parse_filters, parse_sort and
// check_filters_syntax of forgebase-http json.rs): `doc list --filter` and `--sort`, and from P3.2 the
// `filters` and `sort` query parameters of the list endpoint.

func queryError(problem string) *JSONError {
	return &JSONError{Kind: JSONQuery, Problem: problem}
}

func invalidFilter(problem string) *JSONError { return queryError("invalid filter: " + problem) }

// filterItems is the filter text as a list of filter objects: a JSON array.
func filterItems(text string) ([]any, error) {
	v, err := decodeJSON(text)
	if err != nil {
		return nil, err
	}
	items, ok := v.([]any)
	if !ok {
		return nil, invalidFilter("expected a JSON array of filters")
	}
	return items, nil
}

// filterObject is item i of the filters (0-based) as an object with only the keys field, op and value.
func filterObject(i int, item any) (map[string]any, *JSONError) {
	place := func(problem string) *JSONError { return invalidFilter(fmt.Sprintf("filter %d: %s", i+1, problem)) }
	object, ok := item.(map[string]any)
	if !ok {
		return nil, place(fmt.Sprintf("expected an object, found %s", kind(item)))
	}
	for _, key := range sortedKeys(object) {
		if key != "field" && key != "op" && key != "value" {
			return nil, place(fmt.Sprintf("unknown key `%s` (use field, op, value)", core.Shorten(key)))
		}
	}
	return object, nil
}

// CheckFiltersSyntax is the syntax-only part of ParseFilters: the text is a JSON array of objects with
// only the keys field, op and value, and a string field and op. Whether the field exists and the value
// fits is not looked at (see CheckInputSyntax).
func CheckFiltersSyntax(text string) error {
	items, err := filterItems(text)
	if err != nil {
		return err
	}
	for i, item := range items {
		object, jerr := filterObject(i, item)
		if jerr != nil {
			return jerr
		}
		for _, key := range [2]string{"field", "op"} {
			if _, ok := object[key].(string); !ok {
				return invalidFilter(fmt.Sprintf("filter %d: `%s` must be a string", i+1, key))
			}
		}
	}
	return nil
}

// ParseFilters parses the filter JSON (`doc list --filter`, the filters query parameter): an array of
// {"field", "op", "value"} objects. value is mapped by the field's type like in --data (a Link field
// takes the target's name; docstatus and version are integers, created_at and modified_at RFC 3339
// strings); in takes an array of them, like a string, is null and is not null no value. Whether the
// field exists and the operator fits it is checked by internal/db before anything is run.
func ParseFilters(doctype *core.DocType, text string) ([]db.Filter, error) {
	items, err := filterItems(text)
	if err != nil {
		return nil, err
	}
	filters := make([]db.Filter, 0, len(items))
	for i, item := range items {
		place := func(problem string) *JSONError { return invalidFilter(fmt.Sprintf("filter %d: %s", i+1, problem)) }
		object, jerr := filterObject(i, item)
		if jerr != nil {
			return nil, jerr
		}
		field, ok := object["field"].(string)
		if !ok {
			return nil, place("`field` must be a string")
		}
		op, ok := object["op"].(string)
		if !ok {
			return nil, place("`op` must be a string")
		}
		value, hasValue := object["value"]
		fieldType, ok := db.ColumnType(doctype, field)
		if !ok {
			return nil, place(fmt.Sprintf("unknown field %s (table fields cannot be filtered)", core.DebugQuote(core.Shorten(field))))
		}
		one := func() (core.Value, error) {
			if !hasValue {
				return core.Value{}, place(fmt.Sprintf("operator `%s` needs a `value`", core.Shorten(op)))
			}
			return toValue(field, fieldType, value)
		}
		var fop db.FilterOp
		switch op {
		case "=", "!=", "<", "<=", ">", ">=":
			v, err := one()
			if err != nil {
				return nil, err
			}
			fop = map[string]func(core.Value) db.FilterOp{
				"=": db.Eq, "!=": db.Ne, "<": db.Lt, "<=": db.Le, ">": db.Gt, ">=": db.Ge,
			}[op](v)
		case "in":
			values, ok := value.([]any)
			if !ok {
				return nil, place("operator `in` needs a `value` array")
			}
			converted := make([]core.Value, 0, len(values))
			for _, item := range values {
				v, err := toValue(field, fieldType, item)
				if err != nil {
					return nil, err
				}
				converted = append(converted, v)
			}
			fop = db.FilterOp{Kind: db.OpIn, Values: converted}
		case "like":
			pattern, ok := value.(string)
			if !ok {
				return nil, place("operator `like` needs a string `value`")
			}
			fop = db.Like(pattern)
		case "is null", "is not null":
			if hasValue {
				return nil, place(fmt.Sprintf("operator `%s` takes no `value`", core.Shorten(op)))
			}
			fop = db.IsNull(op == "is null")
		default:
			return nil, place(fmt.Sprintf("unknown operator `%s` (use =, !=, <, <=, >, >=, in, like, is null, is not null)", core.Shorten(op)))
		}
		filters = append(filters, db.Filter{Field: field, Op: fop})
	}
	return filters, nil
}

// ParseSort parses the sort values (`doc list --sort`, the comma separated sort query parameter):
// FIELD, FIELD:asc or FIELD:desc.
func ParseSort(specs []string) ([]db.Sort, error) {
	sorts := make([]db.Sort, 0, len(specs))
	for _, spec := range specs {
		field, dir, hasDir := strings.Cut(spec, ":")
		switch {
		case !hasDir:
			sorts = append(sorts, db.Asc(spec))
		case dir == "asc":
			sorts = append(sorts, db.Asc(field))
		case dir == "desc":
			sorts = append(sorts, db.Desc(field))
		default:
			return nil, queryError(fmt.Sprintf("invalid sort %s: use FIELD, FIELD:asc or FIELD:desc", core.DebugQuote(core.Shorten(spec))))
		}
	}
	return sorts, nil
}
