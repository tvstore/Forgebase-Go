package httpapi

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A JSON parser with the errors of serde_json 1.0.154 (the parser of the Rust binary): the same
// wording and the same "at line L column C" position for the mistakes that matter. It mirrors
// serde_json's de.rs and read.rs (SliceRead) function by function, because the position of an error
// depends on whether the parser had consumed or only peeked at the byte it complains about: error()
// reports the position after the last consumed byte (read.rs:595-598), peek_error() the position of the
// next byte (read.rs:600-604). The values are serde_json::Value: objects keep the last of a duplicate key,
// numbers keep their kind (see jsonNumber).
//
// Known differences: the text of a float is parsed by strconv, where serde_json's default (no
// float_roundtrip) may differ in the last digit of a number with more than 15 significant digits; that
// only shows in the message about a number that is not an integer.

// jsonSyntax is a syntax error with its position, as serde_json prints it.
type jsonSyntax struct {
	msg       string
	line, col int
}

func (e *jsonSyntax) Error() string {
	return fmt.Sprintf("%s at line %d column %d", e.msg, e.line, e.col)
}

// The messages of serde_json's ErrorCode (error.rs:349-387).
const (
	msgEOFList       = "EOF while parsing a list"
	msgEOFObject     = "EOF while parsing an object"
	msgEOFString     = "EOF while parsing a string"
	msgEOFValue      = "EOF while parsing a value"
	msgExpectedColon = "expected `:`"
	msgListEnd       = "expected `,` or `]`"
	msgObjectEnd     = "expected `,` or `}`"
	msgIdent         = "expected ident"
	msgValue         = "expected value"
	msgEscape        = "invalid escape"
	msgNumber        = "invalid number"
	msgRange         = "number out of range"
	msgControl       = "control character (\\u0000-\\u001F) found while parsing a string"
	msgKey           = "key must be a string"
	msgLoneSurrogate = "lone leading surrogate in hex escape"
	msgTrailingComma = "trailing comma"
	msgTrailing      = "trailing characters"
	msgEndHex        = "unexpected end of hex escape"
	msgRecursion     = "recursion limit exceeded"
)

// jsonNumber is a JSON number as serde_json's Number holds it: an integer that fits u64 (positive) or
// i64 (negative, not -0), else a float.
type jsonNumber struct {
	u     uint64
	i     int64
	f     float64
	which byte // 'u', 'i' or 'f'
}

// int64Value is Number::as_i64.
func (n jsonNumber) int64Value() (int64, bool) {
	switch n.which {
	case 'u':
		if n.u <= math.MaxInt64 {
			return int64(n.u), true
		}
	case 'i':
		return n.i, true
	}
	return 0, false
}

// String is Number's Display: the integer, or the shortest float text with ".0" for a whole number.
func (n jsonNumber) String() string {
	switch n.which {
	case 'u':
		return strconv.FormatUint(n.u, 10)
	case 'i':
		return strconv.FormatInt(n.i, 10)
	}
	return formatFloat(n.f)
}

// formatFloat writes a finite float like the ryu family does (serde_json's Number Display): the
// shortest digits that read back, "1000.0" for a whole number up to 16 digits, positional down to
// 0.00001, else d.ddde±x without a "+".
func formatFloat(f float64) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde+XX
	mantissa, exp, _ := strings.Cut(sci, "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	e10, _ := strconv.Atoi(exp)
	length := len(digits)
	k := e10 - (length - 1) // the value is digits * 10^k
	kk := length + k
	switch {
	case k >= 0 && kk <= 16:
		return sign + digits + strings.Repeat("0", k) + ".0"
	case kk > 0 && kk <= 16:
		return sign + digits[:kk] + "." + digits[kk:]
	case kk > -5 && kk <= 0:
		return sign + "0." + strings.Repeat("0", -kk) + digits
	case length == 1:
		return sign + digits + "e" + strconv.Itoa(kk-1)
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + strconv.Itoa(kk-1)
}

type jparser struct {
	s string
	i int
	// depth is serde_json's remaining_depth: 128 at the start.
	depth int
}

// parseJSON is serde_json::from_str::<Value>: one value, then only whitespace.
func parseJSON(text string) (any, error) {
	p := &jparser{s: text, depth: 128}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	if _, ok := p.ws(); ok {
		return nil, p.peekErr(msgTrailing)
	}
	return v, nil
}

// position is read.rs position_of_index: the line, and the column as the number of bytes of the line
// before index.
func (p *jparser) position(index int) (int, int) {
	start := strings.LastIndexByte(p.s[:index], '\n') + 1
	return 1 + strings.Count(p.s[:start], "\n"), index - start
}

// err is an error caused by a byte that was consumed (or by the end): the position is the index.
func (p *jparser) err(msg string) error {
	line, col := p.position(p.i)
	return &jsonSyntax{msg, line, col}
}

// peekErr is an error caused by the byte that is next: the position is that of the next byte.
func (p *jparser) peekErr(msg string) error {
	line, col := p.position(min(len(p.s), p.i+1))
	return &jsonSyntax{msg, line, col}
}

func (p *jparser) peek() (byte, bool) {
	if p.i < len(p.s) {
		return p.s[p.i], true
	}
	return 0, false
}

func (p *jparser) peekOrNull() byte {
	b, _ := p.peek()
	return b
}

func (p *jparser) next() (byte, bool) {
	b, ok := p.peek()
	if ok {
		p.i++
	}
	return b, ok
}

// ws skips whitespace and returns the next byte without consuming it.
func (p *jparser) ws() (byte, bool) {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\n', '\t', '\r':
			p.i++
		default:
			return p.s[p.i], true
		}
	}
	return 0, false
}

func (p *jparser) ident(rest string) error {
	for i := 0; i < len(rest); i++ {
		b, ok := p.next()
		if !ok {
			return p.err(msgEOFValue)
		}
		if b != rest[i] {
			return p.err(msgIdent)
		}
	}
	return nil
}

// value is deserialize_any (de.rs:1393-1460).
func (p *jparser) value() (any, error) {
	b, ok := p.ws()
	if !ok {
		return nil, p.peekErr(msgEOFValue)
	}
	switch {
	case b == 'n':
		p.i++
		return nil, p.ident("ull")
	case b == 't':
		p.i++
		return true, p.ident("rue")
	case b == 'f':
		p.i++
		return false, p.ident("alse")
	case b == '-':
		p.i++
		return p.number(p.i - 1)
	case b >= '0' && b <= '9':
		return p.number(p.i)
	case b == '"':
		p.i++
		return p.str()
	case b == '[' || b == '{':
		p.depth--
		if p.depth == 0 {
			return nil, p.peekErr(msgRecursion)
		}
		p.i++
		var v any
		var err error
		if b == '[' {
			v, err = p.list()
		} else {
			v, err = p.object()
		}
		p.depth++
		return v, err
	}
	return nil, p.peekErr(msgValue)
}

// list is SeqAccess::next_element_seed in a loop, then end_seq.
func (p *jparser) list() (any, error) {
	items := []any{}
	first := true
	for {
		b, ok := p.ws()
		if !ok {
			return nil, p.peekErr(msgEOFList)
		}
		if b == ']' {
			p.i++
			return items, nil
		}
		switch {
		case first:
			first = false
		case b == ',':
			p.i++
			b2, ok := p.ws()
			if !ok {
				return nil, p.peekErr(msgEOFValue)
			}
			if b2 == ']' {
				return nil, p.peekErr(msgTrailingComma)
			}
		default:
			return nil, p.peekErr(msgListEnd)
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
}

// object is MapAccess::next_key_seed and next_value_seed in a loop, then end_map.
func (p *jparser) object() (any, error) {
	object := map[string]any{}
	first := true
	for {
		b, ok := p.ws()
		if !ok {
			return nil, p.peekErr(msgEOFObject)
		}
		if b == '}' {
			p.i++
			return object, nil
		}
		switch {
		case first:
			first = false
			if b != '"' {
				return nil, p.peekErr(msgKey)
			}
		case b == ',':
			p.i++
			b2, ok := p.ws()
			switch {
			case !ok:
				return nil, p.peekErr(msgEOFValue)
			case b2 == '}':
				return nil, p.peekErr(msgTrailingComma)
			case b2 != '"':
				return nil, p.peekErr(msgKey)
			}
		default:
			return nil, p.peekErr(msgObjectEnd)
		}
		p.i++ // the quote of the key
		key, err := p.str()
		if err != nil {
			return nil, err
		}
		colon, ok := p.ws()
		if !ok {
			return nil, p.peekErr(msgEOFObject)
		}
		if colon != ':' {
			return nil, p.peekErr(msgExpectedColon)
		}
		p.i++
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		object[key.(string)] = v
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// number is parse_integer, parse_number, parse_decimal and parse_exponent (de.rs:462-620); start is the
// index of the sign, or of the first digit.
func (p *jparser) number(start int) (any, error) {
	c, ok := p.next()
	switch {
	case !ok:
		return nil, p.err(msgEOFValue)
	case c == '0':
		if isDigit(p.peekOrNull()) {
			return nil, p.peekErr(msgNumber)
		}
	case c >= '1' && c <= '9':
		for isDigit(p.peekOrNull()) {
			p.i++
		}
	default:
		return nil, p.err(msgNumber)
	}
	isFloat := false
	if p.peekOrNull() == '.' {
		isFloat = true
		p.i++
		digits := 0
		for isDigit(p.peekOrNull()) {
			p.i++
			digits++
		}
		if digits == 0 {
			if _, ok := p.peek(); ok {
				return nil, p.peekErr(msgNumber)
			}
			return nil, p.peekErr(msgEOFValue)
		}
	}
	if e := p.peekOrNull(); e == 'e' || e == 'E' {
		isFloat = true
		p.i++
		if sign := p.peekOrNull(); sign == '+' || sign == '-' {
			p.i++
		}
		d, ok := p.next()
		switch {
		case !ok:
			return nil, p.err(msgEOFValue)
		case !isDigit(d):
			return nil, p.err(msgNumber)
		}
		for isDigit(p.peekOrNull()) {
			p.i++
		}
	}
	text := p.s[start:p.i]
	if !isFloat {
		if text[0] != '-' {
			if u, err := strconv.ParseUint(text, 10, 64); err == nil {
				return jsonNumber{which: 'u', u: u}, nil
			}
		} else if n, err := strconv.ParseInt(text, 10, 64); err == nil && n < 0 {
			return jsonNumber{which: 'i', i: n}, nil
		}
		// Too large for the integer types, or -0: a float.
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil && math.IsInf(f, 0) {
		return nil, p.err(msgRange)
	}
	return jsonNumber{which: 'f', f: f}, nil
}

func isEscapeByte(b byte) bool { return b == '"' || b == '\\' || b < 0x20 }

// str is parse_str_bytes (read.rs:521-566) after the opening quote.
func (p *jparser) str() (any, error) {
	var buf []byte
	for {
		start := p.i
		for p.i < len(p.s) && !isEscapeByte(p.s[p.i]) {
			p.i++
		}
		buf = append(buf, p.s[start:p.i]...)
		if p.i == len(p.s) {
			return nil, p.err(msgEOFString)
		}
		switch p.s[p.i] {
		case '"':
			p.i++
			return string(buf), nil
		case '\\':
			p.i++
			var err error
			if buf, err = p.escape(buf); err != nil {
				return nil, err
			}
		default:
			return nil, p.err(msgControl)
		}
	}
}

// escape is parse_escape (read.rs:925-946), after the backslash.
func (p *jparser) escape(buf []byte) ([]byte, error) {
	c, ok := p.next()
	if !ok {
		return nil, p.err(msgEOFString)
	}
	switch c {
	case '"', '\\', '/':
		return append(buf, c), nil
	case 'b':
		return append(buf, '\b'), nil
	case 'f':
		return append(buf, '\f'), nil
	case 'n':
		return append(buf, '\n'), nil
	case 'r':
		return append(buf, '\r'), nil
	case 't':
		return append(buf, '\t'), nil
	case 'u':
		return p.unicode(buf)
	}
	return nil, p.err(msgEscape)
}

// hex is decode_hex_escape (read.rs:648-662): four hex digits.
func (p *jparser) hex() (uint16, error) {
	if len(p.s)-p.i < 4 {
		p.i = len(p.s)
		return 0, p.err(msgEOFString)
	}
	digits := p.s[p.i : p.i+4]
	p.i += 4
	n, err := strconv.ParseUint(digits, 16, 16)
	if err != nil || strings.ContainsAny(digits, "+-_xX ") {
		return 0, p.err(msgEscape)
	}
	return uint16(n), nil
}

// unicode is parse_unicode_escape (read.rs:951-1024) for text: surrogates must come in pairs.
func (p *jparser) unicode(buf []byte) ([]byte, error) {
	n, err := p.hex()
	if err != nil {
		return nil, err
	}
	if n >= 0xDC00 && n <= 0xDFFF {
		return nil, p.err(msgLoneSurrogate)
	}
	if n < 0xD800 || n > 0xDBFF {
		return utf8.AppendRune(buf, rune(n)), nil
	}
	for _, want := range []byte{'\\', 'u'} {
		if p.i >= len(p.s) {
			return nil, p.err(msgEOFString)
		}
		if p.s[p.i] != want {
			p.i++
			return nil, p.err(msgEndHex)
		}
		p.i++
	}
	n2, err := p.hex()
	if err != nil {
		return nil, err
	}
	if n2 < 0xDC00 || n2 > 0xDFFF {
		return nil, p.err(msgLoneSurrogate)
	}
	cp := ((rune(n)-0xD800)<<10 | (rune(n2) - 0xDC00)) + 0x10000
	return utf8.AppendRune(buf, cp), nil
}
