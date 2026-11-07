package decimal

import "math/big"

// The arithmetic an app's hooks need (P4): sum, product, rescale and sign. It is not part of the
// Rust crate's surface (a Rust app uses rust_decimal's own operators); the results follow
// rust_decimal where an app can observe them, and are stricter where rust_decimal rounds to make a
// result fit: a result that does not fit (a mantissa of 96 bits or more, or a scale above 28) is
// reported as not ok instead of being rounded.

var limit96 = new(big.Int).Lsh(big.NewInt(1), 96)

// Sign is -1, 0 or +1 by the value of d (a zero with the sign flag set is 0).
func (d Decimal) Sign() int {
	switch {
	case d.IsZero():
		return 0
	case d.neg:
		return -1
	}
	return 1
}

// signedAt is the mantissa with its sign, brought to scale (which must not be below d's scale).
func (d Decimal) signedAt(scale uint) *big.Int {
	m := d.Mantissa()
	if scale > uint(d.scale) {
		m.Mul(m, pow10(scale-uint(d.scale)))
	}
	return m
}

// fromSigned builds the decimal m / 10^scale; false when the mantissa needs 96 bits or more.
func fromSigned(m *big.Int, scale uint) (Decimal, bool) {
	neg := m.Sign() < 0
	abs := new(big.Int).Abs(m)
	if abs.Cmp(limit96) >= 0 {
		return Decimal{}, false
	}
	return build(fromBig(abs), scale, neg), true
}

// Add is d + o. The scale of the result is the larger of the two scales (1.5 + 1.25 is 2.75, 1.50 +
// 1.25 is 2.75, 1.5 + 1.5 is 3.0). ok is false when the sum does not fit in 96 bits.
func (d Decimal) Add(o Decimal) (sum Decimal, ok bool) {
	scale := uint(max(d.scale, o.scale))
	m := d.signedAt(scale)
	m.Add(m, o.signedAt(scale))
	return fromSigned(m, scale)
}

// Mul is d * o. The scale of the result is the sum of the two scales. ok is false when the product
// does not fit in 96 bits or its scale would be above MaxScale (rust_decimal rounds the product to
// fit instead; an app that needs that rounds an operand first).
func (d Decimal) Mul(o Decimal) (product Decimal, ok bool) {
	scale := uint(d.scale) + uint(o.scale)
	if scale > MaxScale {
		return Decimal{}, false
	}
	m := new(big.Int).Mul(d.Mantissa(), o.Mantissa())
	return fromSigned(m, scale)
}

// Rescale gives d the scale scale. A larger scale appends zeros (ok is false when the mantissa then
// needs 96 bits or more); a smaller one drops digits, rounding half away from zero (2.345 at scale
// 2 is 2.35, -2.345 is -2.35), like rust_decimal's rescale. ok is also false for a scale above
// MaxScale.
func (d Decimal) Rescale(scale uint32) (Decimal, bool) {
	if scale > MaxScale {
		return Decimal{}, false
	}
	to := uint(scale)
	from := uint(d.scale)
	switch {
	case to == from:
		return d, true
	case to > from:
		return fromSigned(d.signedAt(to), to)
	}
	return fromSigned(applySign(rescaleDown(d.mantissa().big(), from, to), d.neg), to)
}

func applySign(m *big.Int, neg bool) *big.Int {
	if neg {
		m.Neg(m)
	}
	return m
}
