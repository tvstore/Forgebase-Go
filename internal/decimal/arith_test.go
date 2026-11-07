package decimal

import "testing"

// Not in Rust: the arithmetic is the Go port's own (see arith.go).
func TestAddKeepsTheLargerScale(t *testing.T) {
	for _, c := range []struct{ a, b, want string }{
		{"1.5", "1.25", "2.75"},
		{"1.50", "1.25", "2.75"},
		{"1.5", "1.5", "3.0"},
		{"-1.5", "1.5", "0.0"},
		{"0.10", "-0.30", "-0.20"},
		{"79228162514264337593543950334", "1", "79228162514264337593543950335"},
	} {
		got, ok := mustParse(t, c.a).Add(mustParse(t, c.b))
		if !ok || got.String() != c.want {
			t.Errorf("%s + %s = %s (ok %v), want %s", c.a, c.b, got, ok, c.want)
		}
	}
	if _, ok := mustParse(t, "79228162514264337593543950335").Add(FromInt(1)); ok {
		t.Error("an overflowing sum must not be ok")
	}
}

func TestMulAddsTheScales(t *testing.T) {
	for _, c := range []struct{ a, b, want string }{
		{"3", "2.50", "7.50"},
		{"-3", "2.50", "-7.50"},
		{"0", "2.50", "0.00"},
		{"1.1", "1.1", "1.21"},
	} {
		got, ok := mustParse(t, c.a).Mul(mustParse(t, c.b))
		if !ok || got.String() != c.want {
			t.Errorf("%s * %s = %s (ok %v), want %s", c.a, c.b, got, ok, c.want)
		}
	}
	if _, ok := mustParse(t, "79228162514264337593543950335").Mul(FromInt(2)); ok {
		t.Error("an overflowing product must not be ok")
	}
	if _, ok := mustParse(t, "0.00000000000000000000000001").Mul(mustParse(t, "0.001")); ok {
		t.Error("a product above scale 28 must not be ok")
	}
}

func TestRescaleRoundsHalfAwayFromZero(t *testing.T) {
	for _, c := range []struct {
		in    string
		scale uint32
		want  string
	}{
		{"2.345", 2, "2.35"},
		{"-2.345", 2, "-2.35"},
		{"2.344", 2, "2.34"},
		{"2.5", 0, "3"},
		{"-2.5", 0, "-3"},
		{"2", 2, "2.00"},
		{"2.10", 2, "2.10"},
		{"-0.001", 2, "0.00"},
		{"0", 2, "0.00"},
	} {
		got, ok := mustParse(t, c.in).Rescale(c.scale)
		if !ok || got.String() != c.want {
			t.Errorf("Rescale(%s, %d) = %s (ok %v), want %s", c.in, c.scale, got, ok, c.want)
		}
	}
	if _, ok := mustParse(t, "79228162514264337593543950335").Rescale(2); ok {
		t.Error("a rescale that needs more than 96 bits must not be ok")
	}
	if _, ok := FromInt(1).Rescale(29); ok {
		t.Error("a scale above 28 must not be ok")
	}
}

func TestSign(t *testing.T) {
	if mustParse(t, "-0.5").Sign() != -1 || mustParse(t, "0.00").Sign() != 0 || mustParse(t, "7").Sign() != 1 {
		t.Error("Sign")
	}
	if FromInt(0).Neg().Sign() != 0 {
		t.Error("the negative zero has sign 0")
	}
}
