package core

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// EchoChars is how many characters of user input an error message repeats.
const EchoChars = 64

// Shorten returns text shortened for an error message: at most EchoChars characters (cut on a
// character boundary) and "…" if something was cut. Input that an error repeats (a column name, a
// cell, a rejected option) can be megabytes long, and a message must not grow with it.
func Shorten(text string) string {
	n := 0
	for i := range text {
		if n == EchoChars {
			return text[:i] + "…"
		}
		n++
	}
	return text
}

// debugQuote formats a string like Rust's `{:?}` for a str: in double quotes, with \" \\ \n \r
// \t \0 escaped and any other control or non-printable character as \u{hex}. Messages that Rust
// builds with `{role:?}` use it so that the text is the same. (Rust also escapes combining marks;
// this does the same for the Unicode categories Mn and Me, which cover the cases that matter.)
func debugQuote(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case 0:
			sb.WriteString(`\0`)
		default:
			if unicode.IsPrint(r) && !unicode.In(r, unicode.Mn, unicode.Me) {
				sb.WriteRune(r)
			} else {
				fmt.Fprintf(&sb, `\u{%x}`, r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// durationDebug formats a duration like Rust's `{:?}` for std::time::Duration: 30s, 1.5s, 250ms,
// 1.000001s, 15µs, 0ns. The unit is s when there are whole seconds, else ms, µs or ns; the
// fraction has no trailing zeros.
func durationDebug(d time.Duration) string {
	if d < 0 {
		d = 0 // Rust durations are never negative
	}
	secs := int64(d / time.Second)
	nanos := int64(d % time.Second)
	frac := func(integer, fraction int64, digits int, unit string) string {
		if fraction == 0 {
			return fmt.Sprintf("%d%s", integer, unit)
		}
		f := strings.TrimRight(fmt.Sprintf("%0*d", digits, fraction), "0")
		return fmt.Sprintf("%d.%s%s", integer, f, unit)
	}
	switch {
	case secs > 0:
		return frac(secs, nanos, 9, "s")
	case nanos >= 1_000_000:
		return frac(nanos/1_000_000, nanos%1_000_000, 6, "ms")
	case nanos >= 1_000:
		return frac(nanos/1_000, nanos%1_000, 3, "µs")
	}
	return fmt.Sprintf("%dns", nanos)
}
