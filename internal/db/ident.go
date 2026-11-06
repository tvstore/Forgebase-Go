package db

import "strings"

// QuoteIdent quotes an identifier for SQL: it wraps it in double quotes and doubles any quote
// inside.
//
// Identifiers are already validated by internal/core; quoting every one anyway is defense in
// depth.
func QuoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}
