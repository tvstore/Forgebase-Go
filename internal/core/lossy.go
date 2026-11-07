package core

import (
	"strings"
	"unicode/utf8"
)

// LossyUTF8 is String::from_utf8_lossy (OsStr::to_string_lossy): every maximal invalid subpart of the
// input (a stray byte, or the longest prefix of a UTF-8 sequence that is cut short or broken, so "\xe2\x82"
// is one) shows as one U+FFFD.
func LossyUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r != utf8.RuneError || n != 1 {
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		b.WriteString("\uFFFD")
		i += invalidPrefixLen(s[i:])
	}
	return b.String()
}

// invalidPrefixLen is the length (1 to 3) of the maximal invalid subpart at the start of s, whose first
// byte does not start a valid sequence: the bytes of a prefix of a well-formed sequence (Unicode table
// 3-7) that the next byte does not continue.
func invalidPrefixLen(s string) int {
	first := s[0]
	var lo, hi byte = 0x80, 0xBF
	need := 0
	switch {
	case first >= 0xC2 && first <= 0xDF:
		need = 1
	case first == 0xE0:
		lo, need = 0xA0, 2
	case first >= 0xE1 && first <= 0xEC, first == 0xEE, first == 0xEF:
		need = 2
	case first == 0xED:
		hi, need = 0x9F, 2
	case first == 0xF0:
		lo, need = 0x90, 3
	case first >= 0xF1 && first <= 0xF3:
		need = 3
	case first == 0xF4:
		hi, need = 0x8F, 3
	default:
		return 1
	}
	n := 1
	for n <= need && n < len(s) {
		c := s[n]
		if c < lo || c > hi {
			break
		}
		lo, hi = 0x80, 0xBF
		n++
	}
	return n
}
