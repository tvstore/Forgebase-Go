package core

import (
	"time"

	"github.com/tvstore/forgebase-go/internal/decimal"
)

// ValueKind is the variant of a Value.
type ValueKind int

const (
	// KindNull is no value. It is the zero value of Value.
	KindNull ValueKind = iota
	// KindText is text, used by Data, Text, Select and Link fields.
	KindText
	// KindInt is a 64-bit integer (Int fields).
	KindInt
	// KindDecimal is a fixed-point decimal number (Decimal fields).
	KindDecimal
	// KindBool is a boolean (Check fields).
	KindBool
	// KindDate is a calendar date (Date fields).
	KindDate
	// KindDatetime is a point in time with an offset (Datetime fields).
	KindDatetime
)

// Value is a single field value in a Document. The zero Value is Null.
//
// Select fields hold their value as KindText; so do Link fields, which hold the name of the
// target document. Only the member that belongs to Kind is meaningful. A Date is a time.Time
// whose clock part is ignored (use NewDate); a Datetime is a time.Time with its offset.
type Value struct {
	Kind ValueKind
	Text string
	Int  int64
	Dec  decimal.Decimal
	Bool bool
	Time time.Time
}

// Null is the value "no value".
func Null() Value { return Value{} }

// Text is a text value.
func Text(s string) Value { return Value{Kind: KindText, Text: s} }

// Int is an integer value.
func Int(n int64) Value { return Value{Kind: KindInt, Int: n} }

// Decimal is a decimal value.
func Decimal(d decimal.Decimal) Value { return Value{Kind: KindDecimal, Dec: d} }

// Bool is a boolean value.
func Bool(b bool) Value { return Value{Kind: KindBool, Bool: b} }

// NewDate is a Date value for the calendar day year-month-day.
func NewDate(year int, month time.Month, day int) Value {
	return Value{Kind: KindDate, Time: time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

// Datetime is a Datetime value.
func Datetime(t time.Time) Value { return Value{Kind: KindDatetime, Time: t} }

// KindName is the short name of the variant, used in error messages.
func (v Value) KindName() string {
	switch v.Kind {
	case KindText:
		return "text"
	case KindInt:
		return "int"
	case KindDecimal:
		return "decimal"
	case KindBool:
		return "bool"
	case KindDate:
		return "date"
	case KindDatetime:
		return "datetime"
	}
	return "null"
}

// Equal is Rust's PartialEq for Value: the same variant and the same content. Decimals are
// compared as numbers (1.0 equals 1.00), datetimes as instants.
func (v Value) Equal(o Value) bool {
	if v.Kind != o.Kind {
		return false
	}
	switch v.Kind {
	case KindText:
		return v.Text == o.Text
	case KindInt:
		return v.Int == o.Int
	case KindDecimal:
		return v.Dec.Equal(o.Dec)
	case KindBool:
		return v.Bool == o.Bool
	case KindDate:
		y1, m1, d1 := v.Time.Date()
		y2, m2, d2 := o.Time.Date()
		return y1 == y2 && m1 == m2 && d1 == d2
	case KindDatetime:
		return v.Time.Equal(o.Time)
	}
	return true
}
