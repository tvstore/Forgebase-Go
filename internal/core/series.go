package core

import (
	"fmt"
	"strings"
	"time"
)

// Naming series: patterns such as INV-.YYYY.-.##### that produce names like INV-2026-00007.
//
// This file is pure (no I/O): it parses and validates a pattern and renders names. The counter
// that supplies the number lives in the database (internal/db, table fb_series).
//
// # Pattern syntax
//
// A pattern is a list of parts separated by ".". A part is one of:
//
//	YYYY                       the 4-digit year
//	YY                         the last two digits of the year
//	MM                         the 2-digit month
//	DD                         the 2-digit day of the month
//	a run of # (e.g. #####)    the number, zero-padded to the number of #
//	anything else              itself (literal text)
//
// There must be exactly one number part. Parts may not be empty (.., a leading or a trailing "."
// is an error), a literal may not contain # (a part with a # must be all #) or NUL, and the number
// part is at most MaxNumberWidth wide. A number wider than the # run is printed in full, never
// truncated: INV-.## renders 123 as INV-123. A literal "." cannot be written (it is the separator).
//
// # The counter key
//
// Numbers are counted per prefix: the rendered text before the number part (SeriesPattern.Prefix),
// for example INV-2026-. Date parts after the number part are rendered but do not start a new
// count. Two patterns that render the same prefix share one counter, whatever their width or
// suffix, so the width of # can be changed later without numbers being reused; the cost is that two
// DocTypes with the same prefix share a counter (each then sees gaps), so give each DocType its own
// literal. Because a counter only grows, names of one DocType do not repeat. The exception is a
// pattern changed to a different prefix: prefix "A-" with number 11 and prefix "A-1" with number 1
// both render A-11, and the unique name constraint then refuses the insert (a last resort, not a
// check).

// MaxNumberWidth is the widest number part (# run) allowed: a uint64 has at most 20 digits.
const MaxNumberWidth = 20

// SeriesErrorKind says why a series pattern was rejected.
type SeriesErrorKind int

const (
	// SeriesEmpty: the pattern is the empty string.
	SeriesEmpty SeriesErrorKind = iota + 1
	// SeriesEmptyPart: a part is empty (two dots in a row, or a leading or trailing dot).
	SeriesEmptyPart
	// SeriesMixedNumber: a literal part mixes # with other characters. Part is set.
	SeriesMixedNumber
	// SeriesNul: a part contains a NUL character, which PostgreSQL text cannot store.
	SeriesNul
	// SeriesNoNumber: there is no number part.
	SeriesNoNumber
	// SeriesMultipleNumbers: there is more than one number part. Count is set.
	SeriesMultipleNumbers
	// SeriesNumberTooWide: the number part is wider than MaxNumberWidth. Width is set.
	SeriesNumberTooWide
)

// SeriesError is why a series pattern was rejected. Error reads as the end of a sentence
// ("naming series pattern `x` needs exactly one number part ...").
type SeriesError struct {
	Kind  SeriesErrorKind
	Part  string
	Count int
	Width int
}

func (e SeriesError) Error() string {
	switch e.Kind {
	case SeriesEmpty:
		return "must not be empty"
	case SeriesEmptyPart:
		return "has an empty part (a leading, trailing or doubled `.`)"
	case SeriesMixedNumber:
		return fmt.Sprintf("part `%s` mixes `#` with other characters (the number part must be only `#`)", e.Part)
	case SeriesNul:
		return "must not contain a NUL character"
	case SeriesNoNumber:
		return "needs exactly one number part such as `#####`, found none"
	case SeriesMultipleNumbers:
		return fmt.Sprintf("needs exactly one number part such as `#####`, found %d", e.Count)
	case SeriesNumberTooWide:
		return fmt.Sprintf("number part is %d wide, the maximum is %d", e.Width, MaxNumberWidth)
	}
	return "invalid series pattern"
}

type partKind int

const (
	partLiteral partKind = iota
	partYear4
	partYear2
	partMonth
	partDay
	partNumber
)

type seriesPart struct {
	kind  partKind
	text  string // literal
	width int    // number: zero-padded to this width
}

// SeriesPattern is a validated naming series pattern, see the comment at the top of this file.
// It is immutable once parsed.
type SeriesPattern struct {
	source string
	parts  []seriesPart
	number int // index of the number part in parts
}

// ParseSeriesPattern parses and validates a pattern. The error, if any, is a SeriesError.
func ParseSeriesPattern(pattern string) (*SeriesPattern, error) {
	if pattern == "" {
		return nil, SeriesError{Kind: SeriesEmpty}
	}
	if strings.ContainsRune(pattern, 0) {
		return nil, SeriesError{Kind: SeriesNul}
	}
	var parts []seriesPart
	var numbers []int
	for i, text := range strings.Split(pattern, ".") {
		var part seriesPart
		switch {
		case text == "":
			return nil, SeriesError{Kind: SeriesEmptyPart}
		case text == "YYYY":
			part = seriesPart{kind: partYear4}
		case text == "YY":
			part = seriesPart{kind: partYear2}
		case text == "MM":
			part = seriesPart{kind: partMonth}
		case text == "DD":
			part = seriesPart{kind: partDay}
		case strings.Trim(text, "#") == "":
			numbers = append(numbers, i)
			part = seriesPart{kind: partNumber, width: len(text)}
		case strings.Contains(text, "#"):
			return nil, SeriesError{Kind: SeriesMixedNumber, Part: text}
		default:
			part = seriesPart{kind: partLiteral, text: text}
		}
		parts = append(parts, part)
	}
	switch len(numbers) {
	case 0:
		return nil, SeriesError{Kind: SeriesNoNumber}
	case 1:
	default:
		return nil, SeriesError{Kind: SeriesMultipleNumbers, Count: len(numbers)}
	}
	number := numbers[0]
	if w := parts[number].width; w > MaxNumberWidth {
		return nil, SeriesError{Kind: SeriesNumberTooWide, Width: w}
	}
	return &SeriesPattern{source: pattern, parts: parts, number: number}, nil
}

// String is the pattern as written.
func (p *SeriesPattern) String() string { return p.source }

// Prefix is the rendered text before the number part, with the date parts replaced by date. It is
// the key of the counter.
func (p *SeriesPattern) Prefix(date time.Time) string {
	var sb strings.Builder
	for _, part := range p.parts[:p.number] {
		pushPart(&sb, part, date, 0)
	}
	return sb.String()
}

// Render is the name for number: the prefix, the number zero-padded to the width of the number
// part (a wider number is printed in full) and the rendered parts after it.
func (p *SeriesPattern) Render(date time.Time, number uint64) string {
	var sb strings.Builder
	for _, part := range p.parts {
		pushPart(&sb, part, date, number)
	}
	return sb.String()
}

func pushPart(sb *strings.Builder, part seriesPart, date time.Time, number uint64) {
	switch part.kind {
	case partLiteral:
		sb.WriteString(part.text)
	case partYear4:
		fmt.Fprintf(sb, "%04d", date.Year())
	case partYear2:
		y := date.Year() % 100
		if y < 0 {
			y += 100
		}
		fmt.Fprintf(sb, "%02d", y)
	case partMonth:
		fmt.Fprintf(sb, "%02d", int(date.Month()))
	case partDay:
		fmt.Fprintf(sb, "%02d", date.Day())
	case partNumber:
		fmt.Fprintf(sb, "%0*d", part.width, number)
	}
}
