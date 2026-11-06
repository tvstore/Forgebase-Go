package db

import (
	"fmt"
	"strings"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The read scope of a statement: the SQL form of what a caller may see of a DocType (M10.3, ADR
// 0025, next to if_owner of ADR 0011). The Rust module scope.rs.
//
// Nothing is decided here. core.Evaluate says whether the caller reads every document or only
// their own, and core.RowScopeOf says which documents their user permissions allow; a readScope
// holds both answers and turns them into WHERE conditions. Every statement that finds a document
// on behalf of a caller (get, list, summary, export, the locking reads, link resolution, link
// titles) builds its conditions here, so no path can forget one.
//
// The conditions are ANDed:
//
//   - if_owner: "owner" = $n;
//   - the own name of a restricted DocType: "name" = ANY($n::text::text[]);
//   - a Link field to a restricted target:
//     ("col" IS NULL OR "col" IN (SELECT t."id" FROM "doc_target" t WHERE t."name" = ANY($n::text::text[]))).
//
// A Link column holds the target's id, so the subquery goes through the primary key and the unique
// name index of the target. Every value is a bound parameter, the allowed names as one array
// literal in a text parameter (see arrayLiteral); only validated, quoted identifiers reach the
// SQL. With no restriction there is no condition, and the statement is exactly what it is without
// user permissions.

// readScope is what a caller may see of one DocType: the if_owner owner and the user-permission
// scope. The zero value sees everything.
type readScope struct {
	// ownerOnly is true for an if_owner read: the caller sees only the documents of owner (an
	// empty owner matches nothing, as in Rust).
	ownerOnly bool
	owner     string
	// rows are the documents the caller's user permissions allow.
	rows core.RowScope
}

// scopeSQL is a statement's scope conditions with their bound values, see readScope.andSQL.
type scopeSQL struct {
	// sql is empty, or " AND <condition> AND <condition> ...".
	sql string
	// params are the values of the placeholders the conditions use, in order; all of them text.
	params []string
}

// conditions are the conditions, owner first, then the own name, then the Links in declaration
// order. table qualifies the columns of the statement's own table ("": unqualified, for a
// statement with a single table). bind takes a value and the cast it needs and returns the
// placeholder to put in the SQL.
func (s readScope) conditions(table string, bind func(value, cast string) string) []string {
	column := func(name string) string {
		if table != "" {
			return table + "." + QuoteIdent(name)
		}
		return QuoteIdent(name)
	}
	var conditions []string
	if s.ownerOnly {
		conditions = append(conditions, column("owner")+" = "+bind(s.owner, "text"))
	}
	if s.rows.Names != nil {
		conditions = append(conditions, column("name")+" = ANY("+bind(arrayLiteral(s.rows.Names), "text::text[]")+")")
	}
	// One parameter per target, even when several fields point at it.
	bound := map[string]string{}
	for _, link := range s.rows.Links {
		placeholder, ok := bound[link.Target]
		if !ok {
			placeholder = bind(arrayLiteral(link.Allowed), "text::text[]")
			bound[link.Target] = placeholder
		}
		col := column(link.Field)
		conditions = append(conditions, fmt.Sprintf(
			"(%s IS NULL OR %s IN (SELECT t.\"id\" FROM %s t WHERE t.\"name\" = ANY(%s)))",
			col, col, QuoteIdent(core.TableName(link.Target)), placeholder))
	}
	return conditions
}

// andSQL is the conditions as " AND ..." for a statement that already has a WHERE, and the values
// to bind after the first first-1 parameters (the placeholders are numbered from first).
// Unqualified columns: only for a statement on a single table.
func (s readScope) andSQL(first int) scopeSQL {
	var params []string
	conditions := s.conditions("", func(value, cast string) string {
		params = append(params, value)
		return fmt.Sprintf("$%d::%s", first+len(params)-1, cast)
	})
	var sb strings.Builder
	for _, c := range conditions {
		sb.WriteString(" AND ")
		sb.WriteString(c)
	}
	return scopeSQL{sql: sb.String(), params: params}
}

// args are the scope's values as query arguments, to put after the leading ones.
func (s scopeSQL) args() []any {
	out := make([]any, len(s.params))
	for i, p := range s.params {
		out[i] = p
	}
	return out
}

// arrayLiteral is names as a PostgreSQL array literal for a text array: {"a","b"}. Every element
// is quoted, with \ and " escaped, so a name with a comma, a brace, a quote, spaces or the text
// NULL is that name and nothing else. The literal is bound as a parameter and cast on the server
// ($n::text::text[]); it is never put into the SQL text. (A text parameter keeps the list in one
// value that every statement builder of the package can bind alike.)
func arrayLiteral(names []string) string {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteByte('"')
		for _, c := range name {
			if c == '\\' || c == '"' {
				sb.WriteByte('\\')
			}
			sb.WriteRune(c)
		}
		sb.WriteByte('"')
	}
	sb.WriteByte('}')
	return sb.String()
}
