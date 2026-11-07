// Package decimal is a fixed-point decimal number that behaves like the Rust crate rust_decimal
// 1.x (version 1.43.0), which the Rust implementation of Forgebase uses: a 96-bit unsigned
// mantissa, a scale of 0 to 28 and a sign. The scale is part of the value's text form: "12.50"
// stays "12.50" (see ADR 0027).
//
// Only what Forgebase uses is implemented: parsing (every form rust_decimal's FromStr accepts),
// formatting, comparison, normalisation, a few accessors and (arith.go, P4) the arithmetic an app's hooks need. A Decimal is an immutable value; the
// zero value is 0 with scale 0. Two Decimals compare equal with == only when mantissa, scale and
// sign are the same; use Equal or Cmp to compare the numbers.
package decimal

import (
	"math/big"
	"math/bits"
	"strings"
)

// MaxScale is the largest scale (number of fractional digits) a Decimal can have.
const MaxScale = 28

// maxHi is 2^32: the mantissa must stay below 2^96, that is hi < maxHi.
const maxHi = 1 << 32

// Decimal is a signed fixed-point number: (-1)^neg * mantissa / 10^scale, mantissa < 2^96.
// A zero mantissa is not negative, with two exceptions that rust_decimal has too: a negative
// exponent form that rounds to zero ("-0.4e-28") keeps its sign, and Neg of a zero sets it; both
// print with a "-".
type Decimal struct {
	lo, hi uint64
	scale  uint8
	neg    bool
}

// Error is a parse or conversion error. Its text is the Display text of rust_decimal's Error.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func errorf(msg string) error { return &Error{msg: msg} }

var (
	// ErrExceedsMaximum: "Number exceeds maximum value that can be represented."
	ErrExceedsMaximum = errorf("Number exceeds maximum value that can be represented.")
)

func errScale(scale uint64) error {
	return &Error{msg: "Scale exceeds the maximum precision allowed: " + uitoa(scale) + " > 28"}
}

func uitoa(n uint64) string { return new(big.Int).SetUint64(n).String() }

// New is Decimal::new: mantissa / 10^scale. It panics when scale is above MaxScale.
func New(mantissa int64, scale uint32) Decimal {
	if scale > MaxScale {
		panic("Scale exceeds maximum supported scale")
	}
	neg := mantissa < 0
	var u uint64
	if neg {
		u = uint64(-(mantissa + 1)) + 1
	} else {
		u = uint64(mantissa)
	}
	return build(u128{lo: u}, uint(scale), neg)
}

// FromInt is Decimal::from(i64).
func FromInt(n int64) Decimal { return New(n, 0) }

type u128 struct{ hi, lo uint64 }

func (x u128) isZero() bool { return x.hi == 0 && x.lo == 0 }

// mulAdd10 returns x*10+d. The caller checks overflow96 on the result.
func (x u128) mulAdd10(d uint64) u128 {
	hi, lo := bits.Mul64(x.lo, 10)
	hi += x.hi * 10
	lo, c := bits.Add64(lo, d, 0)
	return u128{hi: hi + c, lo: lo}
}

func (x u128) overflow96() bool { return x.hi >= maxHi }

func (x u128) big() *big.Int {
	b := new(big.Int).SetUint64(x.hi)
	b.Lsh(b, 64)
	return b.Or(b, new(big.Int).SetUint64(x.lo))
}

func fromBig(b *big.Int) u128 {
	lo := new(big.Int).And(b, new(big.Int).SetUint64(^uint64(0))).Uint64()
	hi := new(big.Int).Rsh(b, 64).Uint64()
	return u128{hi: hi, lo: lo}
}

func build(m u128, scale uint, neg bool) Decimal {
	if m.isZero() {
		neg = false
	}
	return Decimal{lo: m.lo, hi: m.hi, scale: uint8(scale), neg: neg}
}

func (d Decimal) mantissa() u128 { return u128{hi: d.hi, lo: d.lo} }

// Scale is the number of fractional digits (0 to 28), trailing zeros included.
func (d Decimal) Scale() uint32 { return uint32(d.scale) }

// IsZero reports whether the value is 0 (of any scale).
func (d Decimal) IsZero() bool { return d.mantissa().isZero() }

// IsNegative reports whether the sign flag is set: the value is below zero, or it is the zero of
// an exponent form that rounded to zero ("-0.4e-28"), or the result of Neg on a positive zero.
// Every other zero is not negative.
func (d Decimal) IsNegative() bool { return d.neg }

// Mantissa is the signed mantissa (Decimal::mantissa).
func (d Decimal) Mantissa() *big.Int {
	b := d.mantissa().big()
	if d.neg {
		b.Neg(b)
	}
	return b
}

// MantissaDigits is the number of decimal digits of the unsigned mantissa; 0 for the value zero.
func (d Decimal) MantissaDigits() uint32 {
	if d.IsZero() {
		return 0
	}
	return uint32(len(d.mantissa().big().String()))
}

// Neg flips the sign flag, also for zero, like rust_decimal's Neg (whose Display then prints
// "-0" for a negative zero).
func (d Decimal) Neg() Decimal {
	d.neg = !d.neg
	return d
}

// Cmp compares the numbers, ignoring the scale: -1, 0 or 1.
func (d Decimal) Cmp(o Decimal) int {
	if d.IsZero() && o.IsZero() {
		return 0
	}
	if d.neg != o.neg {
		if d.neg {
			return -1
		}
		return 1
	}
	a, b := d.mantissa().big(), o.mantissa().big()
	switch {
	case d.scale < o.scale:
		a.Mul(a, pow10(uint(o.scale-d.scale)))
	case d.scale > o.scale:
		b.Mul(b, pow10(uint(d.scale-o.scale)))
	}
	c := a.Cmp(b)
	if d.neg {
		return -c
	}
	return c
}

// Equal reports whether both are the same number: 1.0 equals 1.00.
func (d Decimal) Equal(o Decimal) bool { return d.Cmp(o) == 0 }

func pow10(n uint) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }

// Normalize strips trailing zeros of the fraction (3.100 becomes 3.1); zero becomes 0.
func (d Decimal) Normalize() Decimal {
	if d.IsZero() {
		return Decimal{}
	}
	m := d.mantissa().big()
	ten := big.NewInt(10)
	q, r := new(big.Int), new(big.Int)
	scale := d.scale
	for scale > 0 {
		q.QuoRem(m, ten, r)
		if r.Sign() != 0 {
			break
		}
		m.Set(q)
		scale--
	}
	return Decimal{lo: fromBig(m).lo, hi: fromBig(m).hi, scale: scale, neg: d.neg}
}

// String is the Display text: the scale is kept ("12.50"), a leading "-" for a negative value.
func (d Decimal) String() string {
	digits := ""
	if !d.IsZero() {
		digits = d.mantissa().big().String()
	}
	scale := int(d.scale)
	if len(digits) < scale {
		digits = strings.Repeat("0", scale-len(digits)) + digits
	}
	var sb strings.Builder
	if d.neg {
		sb.WriteByte('-')
	}
	whole := len(digits) - scale
	switch {
	case scale == 0:
		if digits == "" {
			digits = "0"
		}
		sb.WriteString(digits)
	case whole == 0:
		sb.WriteString("0.")
		sb.WriteString(digits)
	default:
		sb.WriteString(digits[:whole])
		sb.WriteByte('.')
		sb.WriteString(digits[whole:])
	}
	return sb.String()
}

// rescaleDown is rescale_internal for a smaller scale: it drops digits, rounding half away from
// zero on the most significant dropped digit.
func rescaleDown(m *big.Int, from, to uint) *big.Int {
	if from <= to {
		return m
	}
	diff := from - to
	q := new(big.Int).Quo(m, pow10(diff-1))
	q2, digit := new(big.Int).QuoRem(q, big.NewInt(10), new(big.Int))
	if digit.Int64() >= 5 {
		q2.Add(q2, big.NewInt(1))
	}
	return q2
}

var errNoDigits = errorf("Invalid decimal: no digits found")

func invalidChar(b byte) error {
	switch b {
	case '.':
		return errorf("Invalid decimal: two decimal points")
	case '_':
		return errorf("Invalid decimal: must start lead with a number")
	}
	return errorf("Invalid decimal: unknown character")
}

// bytesToOverflowU64 mirrors rust_decimal: only inputs of at least this many bytes use the
// 96-bit path, which is also where the rounding of excess fractional digits starts.
const bytesToOverflowU64 = 18

// willOverflowU64 is u64::MAX / 10 - 255.
const willOverflowU64 = ^uint64(0)/10 - 255

// Parse is Decimal::from_str: an optional sign, digits with at most one point, `_` separators
// after a first digit; a fraction longer than 28 digits or a mantissa beyond 96 bits is rounded
// (half up on the first dropped digit) when it has a fraction and an error otherwise; if that
// fails and the text contains `e` or `E`, the scientific form is tried (from_scientific_lossy).
func Parse(s string) (Decimal, error) {
	d, err := parseRadix10(s)
	if err == nil {
		return d, nil
	}
	if strings.ContainsAny(s, "eE") {
		return parseScientificLossy(s)
	}
	return Decimal{}, err
}

func parseRadix10(s string) (Decimal, error) {
	b := []byte(s)
	if len(b) == 0 {
		return Decimal{}, errorf("Invalid decimal: empty")
	}
	big := len(b) >= bytesToOverflowU64
	var (
		neg, point, has, mode128 bool
		scale                    uint
		data                     u128
	)
	finish := func() (Decimal, error) {
		if !has {
			return Decimal{}, errNoDigits
		}
		return build(data, scale, neg), nil
	}
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c >= '0' && c <= '9':
			digit := uint64(c - '0')
			if !mode128 {
				data = data.mulAdd10(digit)
				if point {
					scale++
				} else {
					scale = 0
				}
				has = true
				i++
				if i < len(b) {
					if point && big && scale >= 28 {
						return maybeRound(data, b[i], b[i+1:], scale, point, neg)
					}
					if big && data.lo >= willOverflowU64 {
						mode128 = true
					}
					continue
				}
				return finish()
			}
			next := data.mulAdd10(digit)
			if next.overflow96() {
				if !point {
					return Decimal{}, errorf("Invalid decimal: overflow from too many digits")
				}
				return maybeRound(data, c, b[i+1:], scale, point, neg)
			}
			data = next
			if point {
				scale++
			}
			i++
			if i < len(b) {
				if point && scale >= 28 {
					nb := b[i]
					if nb == '_' {
						rest := b[i+1:]
						for len(rest) > 0 && rest[0] == '_' {
							rest = rest[1:]
						}
						if len(rest) == 0 {
							return finish()
						}
						return maybeRound(data, rest[0], rest[1:], scale, point, neg)
					}
					return maybeRound(data, nb, b[i+1:], scale, point, neg)
				}
				continue
			}
			return finish()
		case c == '.' && !point:
			point = true
			i++
		case (c == '-' || c == '+') && i == 0:
			neg = c == '-'
			i++
		case c == '_' && has:
			i++
		default:
			return Decimal{}, invalidChar(c)
		}
	}
	return finish()
}

func maybeRound(data u128, nextByte byte, remaining []byte, scale uint, point, neg bool) (Decimal, error) {
	var digit uint64
	switch {
	case nextByte >= '0' && nextByte <= '9':
		digit = uint64(nextByte - '0')
	case nextByte == '_':
		digit = 0 // perhaps an error case, kept for compatibility with rust_decimal
	case nextByte == '.' && !point:
		digit = 0
	default:
		return Decimal{}, invalidChar(nextByte)
	}
	if digit >= 5 {
		data = data.add(1)
		if data.overflow96() {
			if scale == 0 {
				return Decimal{}, errorf("Invalid decimal: overflow from mantissa after rounding")
			}
			data = fromBig(new(big.Int).Quo(new(big.Int).Add(data.big(), big.NewInt(4)), big.NewInt(10)))
			scale--
		}
	}
	// Every byte after the rounding position must still be valid.
	seenDot := point || nextByte == '.'
	for _, c := range remaining {
		switch {
		case c >= '0' && c <= '9', c == '_':
		case c == '.' && !seenDot:
			seenDot = true
		case c == '.':
			return Decimal{}, errorf("Invalid decimal: two decimal points")
		default:
			return Decimal{}, invalidChar(c)
		}
	}
	return build(data, scale, neg), nil
}

func (x u128) add(n uint64) u128 {
	lo, c := bits.Add64(x.lo, n, 0)
	return u128{hi: x.hi + c, lo: lo}
}

// parseU32 is Rust's str::parse::<u32>: an optional `+`, then ASCII digits only.
func parseU32(s string) (uint32, bool) {
	s = strings.TrimPrefix(s, "+")
	if s == "" {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + uint64(s[i]-'0')
		if n > 0xFFFFFFFF {
			return 0, false
		}
	}
	return uint32(n), true
}

var errFailedToParse = errorf("Failed to parse")

// parseScientificLossy is Decimal::from_scientific_lossy: "1e3", "1.2E-5". A negative exponent
// that would exceed scale 28 rounds the mantissa to fit; a positive one multiplies the mantissa
// (an overflow is an error) and strips trailing zeros.
func parseScientificLossy(s string) (Decimal, error) {
	idx := strings.IndexAny(s, "eE")
	base, exp := s[:idx], s[idx+1:]
	ret, err := Parse(base)
	if err != nil {
		return Decimal{}, err
	}
	cur := uint32(ret.scale)
	if stripped, ok := strings.CutPrefix(exp, "-"); ok {
		e, ok := parseU32(stripped)
		if !ok {
			return Decimal{}, errFailedToParse
		}
		if e > MaxScale {
			return Decimal{}, errScale(uint64(e))
		}
		if cur+e > MaxScale {
			m := rescaleDown(ret.mantissa().big(), uint(cur), uint(MaxScale-e))
			// rescale and set_scale keep the sign flag, so a mantissa that rounds to zero stays
			// negative ("-0.4e-28" prints as -0.0000000000000000000000000000), like rust_decimal.
			m128 := fromBig(m)
			ret = Decimal{lo: m128.lo, hi: m128.hi, scale: MaxScale, neg: ret.neg}
		} else {
			ret.scale = uint8(cur + e)
		}
		return ret, nil
	}
	e, ok := parseU32(exp)
	if !ok {
		return Decimal{}, errFailedToParse
	}
	if e <= cur {
		ret.scale = uint8(cur - e)
		return ret, nil
	}
	if e > MaxScale {
		return Decimal{}, errScale(uint64(e))
	}
	// Multiply by 10^e in at most two steps (10^19 at most each), like rust_decimal.
	left := e
	for left > 0 {
		step := left
		if step >= 19 {
			step = 19
		}
		left -= step
		var ok bool
		ret, ok = mulPow10(ret, step)
		if !ok {
			return Decimal{}, ErrExceedsMaximum
		}
	}
	return ret.Normalize(), nil
}

// mulPow10 is checked_mul by 10^k (scale 0): when the product needs more than 96 bits, fractional
// digits are dropped (round half to even) to make it fit; false if that is not enough.
func mulPow10(d Decimal, k uint32) (Decimal, bool) {
	if d.IsZero() {
		return Decimal{}, true
	}
	p := new(big.Int).Mul(d.mantissa().big(), pow10(uint(k)))
	limit := new(big.Int).Lsh(big.NewInt(1), 96)
	scale := uint(d.scale)
	if p.Cmp(limit) < 0 {
		return build(fromBig(p), scale, d.neg), true
	}
	n := uint(1)
	for ; n <= scale; n++ {
		if new(big.Int).Quo(p, pow10(n)).Cmp(limit) < 0 {
			break
		}
	}
	if n > scale {
		return Decimal{}, false
	}
	for {
		q, r := new(big.Int).QuoRem(p, pow10(n), new(big.Int))
		half := new(big.Int).Quo(pow10(n), big.NewInt(2))
		if c := r.Cmp(half); c > 0 || (c == 0 && q.Bit(0) == 1) {
			q.Add(q, big.NewInt(1))
		}
		if q.Cmp(limit) < 0 {
			return build(fromBig(q), scale-n, d.neg), true
		}
		n++
		if n > scale {
			return Decimal{}, false
		}
	}
}
