package decimal

import (
	"math/big"
	"testing"
)

// Test vectors come from rust_decimal 1.43.0 (the version in the Rust Forgebase Cargo.lock):
// tests/decimal_tests.rs and the tests of src/str.rs. Each test names its source.

func mustParse(t *testing.T, s string) Decimal {
	t.Helper()
	d, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return d
}

func withScale(t *testing.T, mantissa string, scale uint) Decimal {
	t.Helper()
	m, ok := new(big.Int).SetString(mantissa, 10)
	if !ok {
		t.Fatalf("bad mantissa %q", mantissa)
	}
	return build(fromBig(m), scale, false)
}

// rust_decimal tests/decimal_tests.rs: it_parses_empty_string
func TestParsesEmptyString(t *testing.T) {
	for _, s := range []string{"", " "} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded", s)
		}
	}
	if _, err := Parse(""); err == nil || err.Error() != "Invalid decimal: empty" {
		t.Errorf("empty: %v", err)
	}
}

// rust_decimal tests/decimal_tests.rs: it_parses_{positive,negative}_{int,float,tiny_float}_string,
// it_parses_big_integer_string, it_parses_big_float_string
func TestParsesPlainForms(t *testing.T) {
	cases := []struct {
		in    string
		neg   bool
		scale uint32
		out   string
	}{
		{"233", false, 0, "233"},
		{"-233", true, 0, "-233"},
		{"233.323223", false, 6, "233.323223"},
		{"-233.43343", true, 5, "-233.43343"},
		{".000001", false, 6, "0.000001"},
		{"-0.000001", true, 6, "-0.000001"},
		{"79228162514264337593543950330", false, 0, "79228162514264337593543950330"},
		{"79.228162514264337593543950330", false, 27, "79.228162514264337593543950330"},
		// The forms of the HTTP JSON table: "+12.5", ".5", "5.".
		{"+12.5", false, 1, "12.5"},
		{".5", false, 1, "0.5"},
		{"5.", false, 0, "5"},
		{"-.5", true, 1, "-0.5"},
		// The scale is kept; negative zero loses its sign.
		{"12.50", false, 2, "12.50"},
		{"0.00", false, 2, "0.00"},
		{"-0", false, 0, "0"},
		{"-0.00", false, 2, "0.00"},
		{"007", false, 0, "7"},
	}
	for _, c := range cases {
		d := mustParse(t, c.in)
		if d.IsNegative() != c.neg || d.Scale() != c.scale || d.String() != c.out {
			t.Errorf("%q: neg=%v scale=%d string=%q, want %v %d %q", c.in, d.IsNegative(), d.Scale(), d.String(), c.neg, c.scale, c.out)
		}
	}
}

// rust_decimal tests/decimal_tests.rs: it_parses_scientific_notation_from_str
// and it_can_parse_scientific_notation_rounded (from_scientific_lossy, which FromStr uses)
func TestParsesScientificNotation(t *testing.T) {
	cases := []struct{ in, out string }{
		{"1.23e4", "12300"},
		{"6.7e-1", "0.67"},
		{"1E2", "100"},
		{"-2.5E-3", "-0.0025"},
		{"5e0", "5"},
		{"9.7e-7", "0.00000097"},
		{"9e-7", "0.0000009"},
		{"1.2e10", "12000000000"},
		{"1.2e+10", "12000000000"},
		{"12e10", "120000000000"},
		{"9.7E-7", "0.00000097"},
		{"1.2345E-24", "0.0000000000000000000000012345"},
		{"12345E-28", "0.0000000000000000000000012345"},
		{"1.2345E0", "1.2345"},
		{"1E28", "10000000000000000000000000000"},
		{"1.2345E-28", "0.0000000000000000000000000001"},
		{"8.7654E-28", "0.0000000000000000000000000009"},
		// doc comments of from_scientific_lossy
		{"2.710505431213761e-20", "0.0000000000000000000271050543"},
		{"2.5e-28", "0.0000000000000000000000000003"},
		{"-2.5e-28", "-0.0000000000000000000000000003"},
		// rust_decimal keeps the sign when the mantissa rounds to zero (rescale keeps the flags).
		{"-0.1e-28", "-0.0000000000000000000000000000"},
		{"-0.4e-28", "-0.0000000000000000000000000000"},
		{"-0.5e-28", "-0.0000000000000000000000000001"},
		{"0.4e-28", "0.0000000000000000000000000000"},
		// a product above 96 bits with a fraction: only the trailing zero is dropped (exact)
		{"1.1e28", "11000000000000000000000000000"},
		// The HTTP JSON header: "1e3"
		{"1e3", "1000"},
		// exponent not above the scale: only the scale moves, trailing zeros stay
		{"1.50e1", "15.0"},
	}
	for _, c := range cases {
		d, err := Parse(c.in)
		if err != nil || d.String() != c.out {
			t.Errorf("%q: got %q, %v, want %q", c.in, d.String(), err, c.out)
		}
	}
}

// rust_decimal tests/decimal_tests.rs: it_errors_parsing_large_scientific_notation_rounded and
// the error case of it_can_parse_scientific_notation_rounded
func TestScientificNotationErrors(t *testing.T) {
	cases := []struct{ in, msg string }{
		{"1.2345E-29", "Scale exceeds the maximum precision allowed: 29 > 28"},
		{"12345E29", "Scale exceeds the maximum precision allowed: 29 > 28"},
		{"12345E28", "Number exceeds maximum value that can be represented."},
		{"-20165.4676_e-+4294967292", "Scale exceeds the maximum precision allowed: 4294967292 > 28"},
		// the base error and "Failed to parse" replace the first error when the text has an e
		{"1e", "Failed to parse"},
		{"e5", "Invalid decimal: empty"},
		{"1e-", "Failed to parse"},
		{"1ex", "Failed to parse"},
		{"1e99999999999", "Failed to parse"},
		{"1e--5", "Failed to parse"},
	}
	for _, c := range cases {
		_, err := Parse(c.in)
		if err == nil || err.Error() != c.msg {
			t.Errorf("%q: %v, want %q", c.in, err, c.msg)
		}
	}
}

// rust_decimal tests/decimal_tests.rs: it_can_parse_highly_significant_numbers
func TestParsesHighlySignificantNumbers(t *testing.T) {
	cases := []struct{ in, out string }{
		{"11.111111111111111111111111111", "11.111111111111111111111111111"},
		{"11.11111111111111111111111111111", "11.111111111111111111111111111"},
		{"11.1111111111111111111111111115", "11.111111111111111111111111112"},
		{"115.111111111111111111111111111", "115.11111111111111111111111111"},
		{"1115.11111111111111111111111111", "1115.1111111111111111111111111"},
		{"11.1111111111111111111111111195", "11.111111111111111111111111120"},
		{"99.9999999999999999999999999995", "100.00000000000000000000000000"},
		{"-11.1111111111111111111111111195", "-11.111111111111111111111111120"},
		{"-99.9999999999999999999999999995", "-100.00000000000000000000000000"},
		{"3.1415926535897932384626433832", "3.1415926535897932384626433832"},
		{"8808257419827262908.5944405087133154018", "8808257419827262908.594440509"},
		{"8097370036018690744.2590371109596744091", "8097370036018690744.259037111"},
		{"8097370036018690744.2590371149596744091", "8097370036018690744.259037115"},
		{"8097370036018690744.2590371159596744091", "8097370036018690744.259037116"},
		{"1.234567890123456789012345678949999", "1.2345678901234567890123456789"},
		{".00000000000000000000000000001", "0.0000000000000000000000000000"},
		{".10000000000000000000000000000", "0.1000000000000000000000000000"},
	}
	for _, c := range cases {
		if got := mustParse(t, c.in).String(); got != c.out {
			t.Errorf("%q: got %q, want %q", c.in, got, c.out)
		}
	}
}

// rust_decimal tests/decimal_tests.rs: it_can_parse_alternative_formats,
// it_can_parse_fractional_numbers_with_underscore_separators,
// it_can_parse_numbers_with_underscore_separators_before_decimal_point,
// it_can_parse_numbers_and_round_correctly_with_underscore_separators_before_decimal_point
func TestParsesUnderscoreSeparators(t *testing.T) {
	cases := []struct{ in, out string }{
		{"1_000", "1000"},
		{"1_000_000", "1000000"},
		{"10_000_000", "10000000"},
		{"100_000", "100000"},
		{"1_____________0", "10"},
		{"0.1_23_456", "0.123456"},
		{"1_234.56", "1234.56"},
		{"8_097_370_036_018_690_744.2590371159596744091", "8097370036018690744.259037116"},
		{"8097370036018690744.259_037_115_959_674_409_1", "8097370036018690744.259037116"},
		{"8_097_370_036_018_690_744.259_037_115_959_674_409_1", "8097370036018690744.259037116"},
	}
	for _, c := range cases {
		if got := mustParse(t, c.in).String(); got != c.out {
			t.Errorf("%q: got %q, want %q", c.in, got, c.out)
		}
	}
}

// rust_decimal tests/decimal_tests.rs: it_can_reject_invalid_formats and
// it_can_reject_large_numbers_with_panic
func TestRejectsInvalidAndHugeNumbers(t *testing.T) {
	for _, s := range []string{"_1", "1.0.0", "10_00.0_00.0",
		"79228162514264337593543950336", "79228162514264337593543950337", "79228162514264337593543950338",
		"79228162514264337593543950339", "79228162514264337593543950340",
		"abc", "1 2", " 1", "1 ", "--1", "+-1", "1-", "NaN", "inf", "0x10", "1,5"} {
		if d, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = %v, want an error", s, d)
		}
	}
}

// rust_decimal src/str.rs: from_str_rounding_1 to from_str_rounding_4
func TestFromStrRounding(t *testing.T) {
	cases := []struct {
		in       string
		mantissa string
		scale    uint
	}{
		{"11111_11111_11111.11111_11111_11111", "11111111111111111111111111111", 14},
		{"11111_11111_11111.11111_11111_11115", "11111111111111111111111111112", 14},
		{"11111_11111_11111.11111_11111_11195", "11111111111111111111111111120", 14},
		{"99999_99999_99999.99999_99999_99995", "10000000000000000000000000000", 13},
		{"1.234", "1234", 3},
	}
	for _, c := range cases {
		got := mustParse(t, c.in)
		if want := withScale(t, c.mantissa, c.scale); got != want {
			t.Errorf("%q: got %v (scale %d), want %v (scale %d)", c.in, got, got.Scale(), want, want.Scale())
		}
	}
}

// rust_decimal src/str.rs: from_str_many_pointless_chars, from_str_leading_0s_1..3,
// from_str_trailing_0s_1..2, from_str_overflow_4, from_str_mantissa_overflow_1, _3, _4
func TestFromStrShapes(t *testing.T) {
	cases := []struct {
		in       string
		mantissa string
		scale    uint
	}{
		{"00________________________________________________________________001.1", "11", 1},
		{"00001.1", "11", 1},
		{"00000_00000_00000_00000_00001.00001", "100001", 5},
		{"0.00000_00000_00000_00000_00000_00100", "1", 28},
		{"0.00001_00000_00000", "10000000000", 15},
		{"0.00001_00000_00000_00000_00000_00000", "100000000000000000000000", 28},
		{"99999_99999_99999_99999_99999_999.99", "10000000000000000000000000000", 0},
		{"79_228_162_514_264_337_593_543_950_33.56", "7922816251426433759354395034", 0},
		{"7.92281625142643375935439503356", "7922816251426433759354395034", 27},
		{"7.9_228_162_514_264_337_593_543_950_335_6", "7922816251426433759354395034", 27},
	}
	for _, c := range cases {
		got := mustParse(t, c.in)
		if want := withScale(t, c.mantissa, c.scale); got != want {
			t.Errorf("%q: got %v (scale %d), want %v (scale %d)", c.in, got, got.Scale(), want, want.Scale())
		}
	}
}

// rust_decimal src/str.rs: from_str_overflow_1..3, from_str_mantissa_overflow_2, invalid_input_1..2,
// trailing_garbage_after_rounding_position_*, second_decimal_point_after_rounding_position_is_error,
// from_str_edge_cases_1..6
func TestFromStrErrors(t *testing.T) {
	cases := []struct{ in, msg string }{
		{"99999_99999_99999_99999_99999_99999.99999", "Invalid decimal: overflow from too many digits"},
		{"99999_99999_99999_99999_99999_11111.11111", ""},
		{"99999_99999_99999_99999_99999_99994", ""},
		{"79_228_162_514_264_337_593_543_950_335.6", "Invalid decimal: overflow from mantissa after rounding"},
		{"79_228_162_514_264_337_593_543_950_335.99999", "Invalid decimal: overflow from mantissa after rounding"},
		{"1.0000000000000000000000000000.5", "Invalid decimal: two decimal points"},
		{"1.0.5", "Invalid decimal: two decimal points"},
		{"1.00000000000000000000000000005zzz", "Invalid decimal: unknown character"},
		{"79228162514264337593543950335.4zzz", "Invalid decimal: unknown character"},
		{"1.00000000000000000000000000005.5", "Invalid decimal: two decimal points"},
		{"", "Invalid decimal: empty"},
		{"0.1.", "Invalid decimal: two decimal points"},
		{"_", "Invalid decimal: must start lead with a number"},
		{"1?2", "Invalid decimal: unknown character"},
		{".", "Invalid decimal: no digits found"},
		{"-", "Invalid decimal: no digits found"},
		{"+", "Invalid decimal: no digits found"},
		{"-_1", "Invalid decimal: must start lead with a number"},
	}
	for _, c := range cases {
		_, err := Parse(c.in)
		if err == nil {
			t.Errorf("%q: no error", c.in)
		} else if c.msg != "" && err.Error() != c.msg {
			t.Errorf("%q: %q, want %q", c.in, err.Error(), c.msg)
		}
	}
}

// rust_decimal src/str.rs: character_at_rounding_position
func TestCharacterAtRoundingPosition(t *testing.T) {
	cases := []struct {
		in       string
		mantissa string
		scale    uint
	}{
		{"1.000_000_000_000_000_000_000_000_000_04", "10000000000000000000000000000", 28},
		{"1.000_000_000_000_000_000_000_000_000_06", "10000000000000000000000000001", 28},
		{"1_000_000_000_000_000_000_000_000_000_0.4", "10000000000000000000000000000", 0},
		{"1_000_000_000_000_000_000_000_000_000_0.6", "10000000000000000000000000001", 0},
		{"1.000_000_000_000_000_000_000_000_000_0_4", "10000000000000000000000000000", 28},
		{"1.000_000_000_000_000_000_000_000_000_0_6", "10000000000000000000000000001", 28},
		{"1.000_000_000_000_000_000_000_000_000_0__4", "10000000000000000000000000000", 28},
		{"1.000_000_000_000_000_000_000_000_000_0__6", "10000000000000000000000000001", 28},
		{"1.234567890123456789012345678_9", "12345678901234567890123456789", 28},
		{"0.234567890123456789012345678_9", "2345678901234567890123456789", 28},
		{"0.1234567890123456789012345678_9", "1234567890123456789012345679", 28},
		{"0.1234567890123456789012345678_4", "1234567890123456789012345678", 28},
	}
	for _, c := range cases {
		got := mustParse(t, c.in)
		if want := withScale(t, c.mantissa, c.scale); got != want {
			t.Errorf("%q: got %v (scale %d), want %v (scale %d)", c.in, got, got.Scale(), want, want.Scale())
		}
	}
}

// rust_decimal Display: the scale is kept; tests/decimal_tests.rs it_can_parse_individual_parts
func TestDisplayKeepsScale(t *testing.T) {
	cases := []struct {
		d   Decimal
		out string
	}{
		{New(1250, 2), "12.50"},
		{New(-5, 3), "-0.005"},
		{New(0, 0), "0"},
		{New(0, 3), "0.000"},
		{New(125, 2), "1.25"},
		{FromInt(-9223372036854775808), "-9223372036854775808"},
		{New(31415926535897932, 16), "3.1415926535897932"},
	}
	for _, c := range cases {
		if got := c.d.String(); got != c.out {
			t.Errorf("got %q, want %q", got, c.out)
		}
	}
	pi := build(u128{hi: 1703060790, lo: 185874565<<32 | 1102470952}, 28, false)
	if pi.String() != "3.1415926535897932384626433832" {
		t.Errorf("pi = %s", pi)
	}
}

// rust_decimal Decimal::normalize doc test (3.100 -> 3.1), PartialEq/Ord (numeric comparison)
func TestNormalizeAndCompare(t *testing.T) {
	if got := mustParse(t, "3.100").Normalize().String(); got != "3.1" {
		t.Errorf("normalize: %s", got)
	}
	if got := mustParse(t, "100").Normalize().String(); got != "100" {
		t.Errorf("normalize 100: %s", got)
	}
	if got := mustParse(t, "0.000").Normalize(); got != (Decimal{}) {
		t.Errorf("normalize 0.000: %v", got)
	}
	if d := mustParse(t, "-0.4e-28"); !d.IsNegative() || !d.IsZero() || !d.Equal(Decimal{}) {
		t.Errorf("negative zero of an exponent form: %v", d)
	}
	eq := [][2]string{{"1.0", "1.00"}, {"0", "0.000"}, {"-0", "0"}, {"1e3", "1000"}}
	for _, p := range eq {
		if !mustParse(t, p[0]).Equal(mustParse(t, p[1])) {
			t.Errorf("%s != %s", p[0], p[1])
		}
	}
	lt := [][2]string{{"-1", "1"}, {"1.5", "1.50001"}, {"-2", "-1"}, {"0.9", "1"}}
	for _, p := range lt {
		a, b := mustParse(t, p[0]), mustParse(t, p[1])
		if a.Cmp(b) != -1 || b.Cmp(a) != 1 {
			t.Errorf("%s should be below %s", p[0], p[1])
		}
	}
	if got := mustParse(t, "-1234.5").MantissaDigits(); got != 5 {
		t.Errorf("digits = %d", got)
	}
	if got := mustParse(t, "0.00").MantissaDigits(); got != 0 {
		t.Errorf("zero digits = %d", got)
	}
}

func TestNewPanicsAboveMaxScale(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	New(1, 29)
}
