package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// The canonical JSON of a fixture record and its checksum (M9.4, ADR 0022; the Rust module fixtures.rs of the
// forgebase crate). The checksum is stored in fb_fixture, so it must be byte for byte the one the Rust binary
// computes: a database fixtured by the Rust binary must not be fixtured again by the Go binary (a changed
// checksum would make the next migrate update every record and overwrite the site's edits).
//
// It lives here because the values are the ones of this package's JSON parser (jsonNumber keeps the kind of a
// number as serde_json does) and because the fixtures are mapped to documents by InputFromJSON.

// ParseJSON parses one JSON text with the refusals and the errors of serde_json (jsonparse.go). The values are
// the ones CanonicalJSON, FixtureChecksum, NumberProblems and InputFromJSON take.
func ParseJSON(text string) (any, error) { return parseJSON(text) }

// FixtureChecksum is the checksum of a record: SHA-256, as lower-case hex, of its canonical JSON
// (CanonicalJSON). Whitespace and the order of keys in the file do not change it; a changed value does.
func FixtureChecksum(record any) string {
	sum := sha256.Sum256([]byte(CanonicalJSON(record)))
	return hex.EncodeToString(sum[:])
}

// CanonicalJSON is the canonical JSON of a parsed value: the whole text is produced here, so it depends on
// nothing but this code. Stored checksums must never change for another reason.
//
// The form:
//   - compact, no white space; arrays in order; object keys sorted by their bytes;
//   - strings in double quotes with " \ backspace, form feed, newline, carriage return and tab escaped as \" \\
//     \b \f \n \r \t, any other control character below U+0020 as \u00xx (lower-case hex), everything else as
//     is;
//   - an integer that fits int64 or uint64 as its decimal digits (no plus sign, no leading zeros);
//   - any other number as float64, by writeCanonicalFloat: the shortest digits that read back as the same
//     float64 (Rust's {:e}, which strconv's -1 precision matches), as a plain decimal with an exponent from -5
//     to 15 (0.00001, 12.5, 100.0: always a . and at least one fractional digit) and otherwise as d.ddde+N or
//     d.ddde-N (1.5e+17, 6.0e-22).
//
// NumberProblems refuses at startup the numbers for which the parser of another build could differ, so that if
// two builds both accept a number its canonical text is the same.
func CanonicalJSON(v any) string {
	var out strings.Builder
	writeCanonical(v, &out)
	return out.String()
}

func writeCanonical(v any, out *strings.Builder) {
	switch x := v.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(x))
	case jsonNumber:
		switch x.which {
		case 'u':
			out.WriteString(strconv.FormatUint(x.u, 10))
		case 'i':
			out.WriteString(strconv.FormatInt(x.i, 10))
		default:
			// Not finite cannot happen for a value that passed NumberProblems; it is written as null here only
			// so that this function never fails.
			if math.IsInf(x.f, 0) || math.IsNaN(x.f) {
				out.WriteString("null")
			} else {
				writeCanonicalFloat(x.f, out)
			}
		}
	case string:
		writeCanonicalString(x, out)
	case []any:
		out.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				out.WriteByte(',')
			}
			writeCanonical(item, out)
		}
		out.WriteByte(']')
	case map[string]any:
		out.WriteByte('{')
		for i, key := range sortedKeys(x) {
			if i > 0 {
				out.WriteByte(',')
			}
			writeCanonicalString(key, out)
			out.WriteByte(':')
			writeCanonical(x[key], out)
		}
		out.WriteByte('}')
	}
}

// writeCanonicalString writes a string as the canonical form does (see CanonicalJSON).
func writeCanonicalString(text string, out *strings.Builder) {
	out.WriteByte('"')
	for _, c := range text {
		switch {
		case c == '"':
			out.WriteString(`\"`)
		case c == '\\':
			out.WriteString(`\\`)
		case c == '\b':
			out.WriteString(`\b`)
		case c == '\f':
			out.WriteString(`\f`)
		case c == '\n':
			out.WriteString(`\n`)
		case c == '\r':
			out.WriteString(`\r`)
		case c == '\t':
			out.WriteString(`\t`)
		case c < 0x20:
			fmt.Fprintf(out, `\u%04x`, c)
		default:
			out.WriteRune(c)
		}
	}
	out.WriteByte('"')
}

// shortestFloat is the digits and the decimal exponent of the shortest text that reads back as f (f is
// finite): Rust's format!("{:e}", f.abs()), "d.ddde<exp>", taken apart.
func shortestFloat(f float64) (digits string, exponent int) {
	text := strconv.FormatFloat(math.Abs(f), 'e', -1, 64)
	mantissa, exp, _ := strings.Cut(text, "e")
	n, err := strconv.Atoi(exp)
	if err != nil {
		n = math.MaxInt32
	}
	var b strings.Builder
	for _, c := range mantissa {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String(), n
}

// writeCanonicalFloat writes a finite float64 as the canonical form does (see CanonicalJSON): the digits and
// exponent of the shortest text, rendered by a fixed rule.
func writeCanonicalFloat(f float64, out *strings.Builder) {
	digits, exponent := shortestFloat(f)
	if math.Signbit(f) {
		out.WriteByte('-')
	}
	if exponent >= -5 && exponent <= 15 {
		if exponent >= 0 {
			integerLen := exponent + 1
			if len(digits) <= integerLen {
				out.WriteString(digits)
				out.WriteString(strings.Repeat("0", integerLen-len(digits)))
				out.WriteString(".0")
			} else {
				out.WriteString(digits[:integerLen])
				out.WriteByte('.')
				out.WriteString(digits[integerLen:])
			}
		} else {
			out.WriteString("0.")
			out.WriteString(strings.Repeat("0", -exponent-1))
			out.WriteString(digits)
		}
		return
	}
	out.WriteString(digits[:1])
	out.WriteByte('.')
	if len(digits) > 1 {
		out.WriteString(digits[1:])
	} else {
		out.WriteByte('0')
	}
	sign := '+'
	if exponent < 0 {
		sign = '-'
		exponent = -exponent
	}
	fmt.Fprintf(out, "e%c%d", sign, exponent)
}

// NumberProblems are the problems of every number in value, rows of Table fields included.
func NumberProblems(value any) []string {
	var problems []string
	collectNumberProblems(value, &problems)
	return problems
}

func collectNumberProblems(value any, problems *[]string) {
	switch x := value.(type) {
	case jsonNumber:
		if problem := numberProblem(x); problem != "" {
			*problems = append(*problems, problem)
		}
	case []any:
		for _, item := range x {
			collectNumberProblems(item, problems)
		}
	case map[string]any:
		for _, key := range sortedKeys(x) {
			collectNumberProblems(x[key], problems)
		}
	}
}

// numberProblem is why a number cannot be part of a fixture record, or "". The guarantee: if two builds of the
// app (with a parser that differs in the way a number is read: serde_json with arbitrary_precision or
// float_roundtrip, in the Rust implementation) both accept a number, its canonical text is the same. Accepted:
// an integer in the int64 or uint64 range, and a number written as a float (a fraction, an exponent, or an
// integer above that range) whose shortest float64 text has at most 15 significant digits and a decimal
// exponent within +-15, and which is below 2^63 in magnitude. Refused: negative zero, a number that is not
// finite, a non-zero number that underflows to zero, an integer beyond the 64-bit range, and any number with
// more digits or a larger exponent. The test is applied to the parsed value, as in Rust.
func numberProblem(n jsonNumber) string {
	text := n.String()
	advice := "write it as a string, or with fewer significant digits"
	negativeZero := fmt.Sprintf("number `%s` is a negative zero, which cannot be represented the same way in every build; write 0", text)
	// Negative zero: a sign and no non-zero digit before any exponent.
	mantissa, _, _ := strings.Cut(strings.ReplaceAll(text, "E", "e"), "e")
	nonzeroDigits := strings.ContainsAny(mantissa, "123456789")
	if strings.HasPrefix(mantissa, "-") && !nonzeroDigits {
		return negativeZero
	}
	if n.which == 'u' || n.which == 'i' {
		return ""
	}
	f := n.f
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return fmt.Sprintf("number `%s` is out of range", text)
	}
	if f == 0 && math.Signbit(f) {
		return negativeZero
	}
	if f == 0 && nonzeroDigits {
		return fmt.Sprintf("number `%s` is too small to be represented (it becomes zero); write it as a string", text)
	}
	// An integer beyond the 64-bit range goes through float64, whatever it is written like.
	if f == math.Trunc(f) && math.Abs(f) >= 9_223_372_036_854_775_808.0 {
		return fmt.Sprintf("number `%s` is an integer outside the 64-bit range and cannot be represented the same way in every build; write it as a string", text)
	}
	digits, exponent := shortestFloat(f)
	if len(digits) > 15 || exponent < -15 || exponent > 15 {
		return fmt.Sprintf("number `%s` cannot be represented the same way in every build; %s", text, advice)
	}
	return ""
}
