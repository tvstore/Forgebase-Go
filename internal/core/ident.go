package core

import (
	"fmt"
	"slices"
)

// Identifier rules for DocType and field names. Names are plain snake_case ASCII identifiers
// (^[a-z][a-z0-9_]*$) so they can be used safely to derive SQL identifiers. This file only
// validates and builds strings; quoting them for SQL belongs to internal/db.

// TablePrefix is the prefix of every DocType table name.
const TablePrefix = "doc_"

// MaxDocTypeNameLen is the maximum DocType name length in bytes: PostgreSQL identifiers hold 63
// bytes and the table name is doc_<name>.
const MaxDocTypeNameLen = 63 - len(TablePrefix)

// MaxFieldNameLen is the maximum field (column) name length in bytes.
const MaxFieldNameLen = 63

// ReservedFieldNames are the names a user field may not use: the standard columns of a DocType
// table (docstatus and amended_from exist only on submittable DocTypes but are reserved
// everywhere), and the PostgreSQL system columns (tableoid ... ctid), which CREATE TABLE rejects.
var ReservedFieldNames = [17]string{
	"id", "name", "owner", "created_at", "modified_at", "modified_by", "version",
	"docstatus", "amended_from", "idx", "parent_id",
	"tableoid", "xmin", "xmax", "cmin", "cmax", "ctid",
}

// IdentErrorKind says why an identifier was rejected.
type IdentErrorKind int

const (
	// IdentEmpty: the identifier is the empty string.
	IdentEmpty IdentErrorKind = iota + 1
	// IdentInvalidFormat: the identifier does not match ^[a-z][a-z0-9_]*$.
	IdentInvalidFormat
	// IdentTooLong: the identifier is longer than the allowed number of bytes.
	IdentTooLong
	// IdentReserved: the identifier is the name of a standard or PostgreSQL system column.
	IdentReserved
)

// IdentError is why an identifier was rejected. Len and Max are set for IdentTooLong.
type IdentError struct {
	Kind     IdentErrorKind
	Len, Max int
}

// Error is the Rust Display text; it reads as the end of a sentence ("name `x` must not be empty").
func (e IdentError) Error() string {
	switch e.Kind {
	case IdentEmpty:
		return "must not be empty"
	case IdentInvalidFormat:
		return "must match ^[a-z][a-z0-9_]*$ (lowercase ASCII letters, digits and underscores, starting with a letter)"
	case IdentTooLong:
		return fmt.Sprintf("is %d bytes long, the maximum is %d", e.Len, e.Max)
	case IdentReserved:
		return "is a reserved column name"
	}
	return "invalid identifier"
}

// ValidateDocTypeName validates a DocType name (^[a-z][a-z0-9_]*$, at most 59 bytes). It returns
// nil or an IdentError.
func ValidateDocTypeName(name string) error {
	if e, bad := validatePattern(name, MaxDocTypeNameLen); bad {
		return e
	}
	return nil
}

// ValidateFieldName validates a field name (^[a-z][a-z0-9_]*$, at most 63 bytes, not a reserved
// column name). It returns nil or an IdentError.
func ValidateFieldName(name string) error {
	if e, bad := validatePattern(name, MaxFieldNameLen); bad {
		return e
	}
	if slices.Contains(ReservedFieldNames[:], name) {
		return IdentError{Kind: IdentReserved}
	}
	return nil
}

// TableName returns the table name for a DocType (doc_<name>), unquoted. The name must already
// have passed ValidateDocTypeName.
func TableName(doctype string) string { return TablePrefix + doctype }

func validatePattern(name string, maxLen int) (IdentError, bool) {
	if name == "" {
		return IdentError{Kind: IdentEmpty}, true
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		lower := b >= 'a' && b <= 'z'
		digitOrUnderscore := (b >= '0' && b <= '9') || b == '_'
		if !(lower || (i > 0 && digitOrUnderscore)) {
			return IdentError{Kind: IdentInvalidFormat}, true
		}
	}
	if len(name) > maxLen {
		return IdentError{Kind: IdentTooLong, Len: len(name), Max: maxLen}, true
	}
	return IdentError{}, false
}
