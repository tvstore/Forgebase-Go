package db

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/decimal"
)

// Binding core.Value as query parameters and decoding result columns back into Values (the Rust
// module value.rs).
//
// Generated SQL gives every placeholder an explicit cast (see castType), so the parameter type
// comes from the metadata and never from inference. A value of the wrong kind for that type is an
// error, not wrong bytes on the wire.
//
// Datetimes have microsecond precision (PostgreSQL timestamptz): finer digits of a bound value are
// truncated toward the PostgreSQL epoch (2000-01-01), like the Rust time crate does, not rounded
// or rejected.

// The years the Rust time crate (without its large-dates feature) can represent. A datetime or a
// date outside them can be neither written nor read, as in the Rust implementation.
const (
	minYear = -9999
	maxYear = 9999
)

// castType is the SQL type a placeholder for a field of this type is cast to.
func castType(ft core.FieldType) string {
	switch ft.Kind {
	case core.FieldData:
		return "varchar"
	case core.FieldText, core.FieldSelect:
		return "text"
	case core.FieldInt:
		return "bigint"
	case core.FieldDecimal:
		return "numeric"
	case core.FieldCheck:
		return "boolean"
	case core.FieldDate:
		return "date"
	case core.FieldDatetime:
		return "timestamptz"
	}
	// A Table field has no column and no parameter; callers never ask.
	return "uuid"
}

// bindValue is the query argument for value v bound to a placeholder cast to cast. Null is SQL
// NULL for any type. A value of another kind than cast takes is an error (position is the
// 1-based placeholder number, for the message).
func bindValue(cast string, v core.Value, position int) (any, error) {
	wrong := func() (any, error) {
		return nil, fmt.Errorf("error serializing parameter %d: cannot bind a %s value as %s", position, v.KindName(), cast)
	}
	switch v.Kind {
	case core.KindNull:
		return nil, nil
	case core.KindText:
		if cast != "varchar" && cast != "text" {
			return wrong()
		}
		return v.Text, nil
	case core.KindInt:
		if cast != "bigint" {
			return wrong()
		}
		return v.Int, nil
	case core.KindDecimal:
		if cast != "numeric" {
			return wrong()
		}
		// numeric text keeps the scale: pgx sends a string in text format whatever the type.
		return v.Dec.String(), nil
	case core.KindBool:
		if cast != "boolean" {
			return wrong()
		}
		return v.Bool, nil
	case core.KindDate:
		if cast != "date" {
			return wrong()
		}
		y, m, d := v.Time.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC), nil
	case core.KindDatetime:
		if cast != "timestamptz" {
			return wrong()
		}
		// time.Time can hold instants the Rust time crate cannot: report them like Rust does
		// instead of sending them.
		u := v.Time.UTC()
		if y := u.Year(); y < minYear || y > maxYear {
			return nil, fmt.Errorf("error serializing parameter %d: datetime is out of range once converted to UTC", position)
		}
		return truncateMicros(u), nil
	}
	return nil, fmt.Errorf("error serializing parameter %d: cannot bind a %s value", position, v.KindName())
}

// pgEpoch is the PostgreSQL epoch, 2000-01-01T00:00:00Z.
var pgEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// truncateMicros makes t what PostgreSQL stores as the Rust driver sends it: whole microseconds
// since the PostgreSQL epoch, truncated toward zero. Go's own encoding floors, which differs for
// an instant before the epoch with digits finer than a microsecond.
func truncateMicros(t time.Time) time.Time {
	rest := t.Nanosecond() % 1000
	if rest == 0 {
		return t
	}
	if t.Before(pgEpoch) {
		// Before the epoch the duration is negative: toward zero is upward.
		return t.Add(time.Duration(1000-rest) * time.Nanosecond)
	}
	return t.Add(-time.Duration(rest) * time.Nanosecond)
}

// columnDest is the scan destination for a column of a field of this type, and fromColumn turns
// what was scanned into a Value.
func columnDest(ft core.FieldType) any {
	switch ft.Kind {
	case core.FieldData, core.FieldText, core.FieldSelect, core.FieldLink:
		return new(pgtype.Text)
	case core.FieldInt:
		return new(pgtype.Int8)
	case core.FieldDecimal:
		return new(numericText)
	case core.FieldCheck:
		return new(pgtype.Bool)
	case core.FieldDate:
		return new(pgtype.Date)
	case core.FieldDatetime:
		return new(pgtype.Timestamptz)
	}
	// A Table field has no column; callers read ColumnFields only.
	return new(pgtype.Text)
}

// fromColumn reads what columnDest scanned. SQL NULL is Null. A value that cannot be represented
// (a year beyond +-9999, infinity, a numeric NaN or one that does not fit a Decimal) is an error;
// the caller adds the field name.
func fromColumn(dest any) (core.Value, error) {
	switch d := dest.(type) {
	case *pgtype.Text:
		if !d.Valid {
			return core.Null(), nil
		}
		return core.Text(d.String), nil
	case *pgtype.Int8:
		if !d.Valid {
			return core.Null(), nil
		}
		return core.Int(d.Int64), nil
	case *numericText:
		if !d.Valid {
			return core.Null(), nil
		}
		// NaN and Infinity are not numbers a Decimal can hold: Parse refuses them.
		dec, err := decimal.Parse(d.String)
		if err != nil {
			return core.Null(), fmt.Errorf("numeric %q cannot be read: %w", d.String, err)
		}
		return core.Decimal(dec), nil
	case *pgtype.Bool:
		if !d.Valid {
			return core.Null(), nil
		}
		return core.Bool(d.Bool), nil
	case *pgtype.Date:
		if !d.Valid {
			return core.Null(), nil
		}
		if d.InfinityModifier != pgtype.Finite {
			return core.Null(), fmt.Errorf("date %s cannot be read", d.InfinityModifier)
		}
		if y := d.Time.Year(); y < minYear || y > maxYear {
			return core.Null(), fmt.Errorf("date out of the supported range (years %d to %d)", minYear, maxYear)
		}
		return core.NewDate(d.Time.Year(), d.Time.Month(), d.Time.Day()), nil
	case *pgtype.Timestamptz:
		if !d.Valid {
			return core.Null(), nil
		}
		if d.InfinityModifier != pgtype.Finite {
			return core.Null(), fmt.Errorf("timestamp %s cannot be read", d.InfinityModifier)
		}
		t := d.Time.UTC()
		if y := t.Year(); y < minYear || y > maxYear {
			return core.Null(), fmt.Errorf("timestamp out of the supported range (years %d to %d)", minYear, maxYear)
		}
		return core.Datetime(t), nil
	}
	return core.Null(), fmt.Errorf("unsupported column type %T", dest)
}

// numericText is a numeric column read as the text PostgreSQL writes it. The binary form that pgx
// decodes by default loses the scale of a zero (0.00 comes back as 0), the text form keeps every
// scale (12.50 stays 12.50, like rust_decimal's reading of the binary form), so a query that returns
// numeric columns asks for them as text (numericAsText).
type numericText struct{ pgtype.Text }

// numericAsText is the first argument of every query that returns the columns of a document: pgx
// takes it as the result format of the numeric columns, not as a parameter.
var numericAsText = pgx.QueryResultFormatsByOID{pgtype.NumericOID: pgx.TextFormatCode}

// withFormats is args with numericAsText in front of them.
func withFormats(args ...any) []any { return append([]any{numericAsText}, args...) }
