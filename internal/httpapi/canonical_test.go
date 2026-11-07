package httpapi

import (
	"strings"
	"testing"
)

// The canonical JSON and the checksum of a fixture record (ADR 0022), ported from the tests of
// crates/forgebase/src/fixtures.rs, with a set of test vectors for the parity with the Rust binary.

func canonicalInput(t *testing.T, text string) any {
	t.Helper()
	v, err := ParseJSON(text)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return v
}

// The vectors: input text, the canonical text the Rust function canonical() writes for it (derived by hand
// from the rules of ADR 0022 and checked against Rust's {:e} on the digits), and the SHA-256 of that text,
// computed with an implementation of SHA-256 that is not this code (Python's hashlib). The first is the one
// vector that is in the Rust tests too (the_checksum_is_a_known_sha256). A database fixtured by the Rust
// binary stores these checksums; if one of these changes, a Go migrate would update every record.
//
// Not in Rust: the other vectors, for the parity (numbers like 1e21, 0.1, -0.0, a 30-digit integer).
var checksumVectors = []struct{ input, canonical, sha256 string }{
	{`{ "a" : 1 }`, `{"a":1}`, "015abd7f5cc57a2dd94b7590f04ad8084273905ee33ec5cebeae62276a97f862"},
	{`1e21`, `1.0e+21`, "a1b0aae97eda6d4b3585a40b9b641e6965225e11109b5bea6b7add73d26c378d"},
	{`0.1`, `0.1`, "14be4b45f18e0d8c67b4f719b5144eee88497e413709d11d85b096d8e2346310"},
	{`-0.0`, `-0.0`, "c26617c7ccbcaa6631b45d851b8cf56e21d2ca624bdb1193afdbd4b560702cec"},
	{`123456789012345678901234567890`, `1.2345678901234568e+29`, "a2379507385d6bf639d8eb6afb3c4c1937b9f994691c57b1f2cc08a0ece3ab4a"},
	{`{"title": "Retail"}`, `{"title":"Retail"}`, "6a061e2c4863835319e9a5aa1af2bf549c6f2c4c5c250e026810a424f2d4b465"},
	{`{"doctype_less": true, "records": [{"b": 1, "a": [1.5, null]}]}`, `{"doctype_less":true,"records":[{"a":[1.5,null],"b":1}]}`,
		"d0d5252381e20c21a19955ef52c8c29537cc2a7c086ed02503108bfdaaae8e5e"},
	{`[0, 12, -12, 18446744073709551615, 1.0, 2.5e3, 1e-7, 0.1]`, `[0,12,-12,18446744073709551615,1.0,2500.0,1.0e-7,0.1]`,
		"6ca016ceddd00a6844fbb9d936cf31c48bdcd31c592457de15fd7412f8f176e9"},
	{`[1e15, 1e16, 123456789012345.0, 0.00001, 0.000001, 5e-324, 1.7976931348623157e308, 0.30000000000000004]`,
		`[1000000000000000.0,1.0e+16,123456789012345.0,0.00001,1.0e-6,5.0e-324,1.7976931348623157e+308,0.30000000000000004]`,
		"60441b0b2d9752a2ff7c1e9ae884650a5b9631181537bc346e15bceb36bf0f92"},
	{`"\u0001\u001f\u007f\b\f\n\r\t\"\\/é€😀"`, "\"\\u0001\\u001f\x7f\\b\\f\\n\\r\\t\\\"\\\\/é€\U0001F600\"",
		"a8b535ea6b8775ddbfb5cc7aceb7bd69e7f3f9a58ca913297e21e03dfad5cbf8"},
	{`-9223372036854775808`, `-9223372036854775808`, "85386477f3af47e4a0b308ee3b3a688df16e8b2228105dd7d4dcd42a9807cb78"},
	{`18446744073709551616`, `1.8446744073709552e+19`, "7e7e9a94b89d2f5e1a317a4896bacf04ce3e6fb1cf2417fcacbd840113196b2f"},
	{`{"k\u0001": ""}`, `{"k\u0001":""}`, "2e6c0e9787a4c0a0906d9acfffff6b9c3cee8e070118bf7d2fa5d494fa3188a4"},
}

// Not in Rust: the cross-implementation vectors.
func TestTheChecksumOfAFixtureRecordMatchesTheTestVectors(t *testing.T) {
	for _, c := range checksumVectors {
		v := canonicalInput(t, c.input)
		if got := CanonicalJSON(v); got != c.canonical {
			t.Errorf("%s: canonical %q, want %q", c.input, got, c.canonical)
		}
		if got := FixtureChecksum(v); got != c.sha256 {
			t.Errorf("%s: checksum %s, want %s", c.input, got, c.sha256)
		}
	}
}

// Rust: fixtures.rs::tests::whitespace_and_key_order_do_not_change_the_checksum
func TestWhitespaceAndKeyOrderDoNotChangeTheChecksum(t *testing.T) {
	a := FixtureChecksum(canonicalInput(t, `{"title": "Retail", "rows": [{"b": 1, "a": 2}], "x": null}`))
	b := FixtureChecksum(canonicalInput(t, "{\n  \"x\": null,\n  \"rows\": [ { \"a\": 2,\n \"b\": 1 } ],\n  \"title\": \"Retail\"\n}"))
	if a != b || len(a) != 64 || strings.ToLower(a) != a {
		t.Errorf("%s %s", a, b)
	}
}

// Rust: fixtures.rs::tests::a_changed_value_changes_the_checksum
func TestAChangedValueChangesTheChecksum(t *testing.T) {
	sum := func(text string) string { return FixtureChecksum(canonicalInput(t, text)) }
	base := sum(`{"title": "Retail", "n": 1}`)
	for _, other := range []string{`{"title": "Retail ", "n": 1}`, `{"title": "Retail", "n": 2}`, `{"title": "Retail"}`,
		`{"title": "Retail", "n": 1, "extra": null}`} {
		if sum(other) == base {
			t.Errorf("%s has the checksum of the base", other)
		}
	}
	// Row order is a value: it is the order of the rows.
	if sum(`{"rows": [{"a": 1}, {"a": 2}]}`) == sum(`{"rows": [{"a": 2}, {"a": 1}]}`) {
		t.Error("the order of rows is not in the checksum")
	}
}

// Rust: fixtures.rs::tests::the_canonical_form_does_not_depend_on_the_order_of_a_map
func TestTheCanonicalFormDoesNotDependOnTheOrderOfAMap(t *testing.T) {
	left := canonicalInput(t, `{"b": {"z": 1, "y": [{"q": 2, "p": 1}, 3]}, "c": [true, null, "x\ny", 1.5, -7], "a": 3}`)
	right := canonicalInput(t, `{"a": 3, "b": {"y": [{"p": 1, "q": 2}, 3], "z": 1}, "c": [true, null, "x\ny", 1.5, -7]}`)
	want := `{"a":3,"b":{"y":[{"p":1,"q":2},3],"z":1},"c":[true,null,"x\ny",1.5,-7]}`
	if CanonicalJSON(left) != want || CanonicalJSON(right) != want || FixtureChecksum(left) != FixtureChecksum(right) {
		t.Errorf("%s\n%s", CanonicalJSON(left), CanonicalJSON(right))
	}
}

// Rust: fixtures.rs::tests::floats_have_one_fixed_form
func TestFloatsHaveOneFixedForm(t *testing.T) {
	// Golden values: the plain form for an exponent from -5 to 15, with a . and at least one fractional digit;
	// d.ddde+N / d.ddde-N outside it. Checked on the writer, so the edges are covered even for values that
	// startup refuses.
	for text, expected := range map[string]string{
		"0.0": "0.0", "1.0": "1.0", "100.0": "100.0", "12.5": "12.5", "-12.5": "-12.5", "0.1": "0.1", "0.5": "0.5",
		"0.001": "0.001", "0.00001": "0.00001", "0.000012345": "0.000012345", "123456789012345.0": "123456789012345.0",
		"1e15": "1000000000000000.0", "1.5e15": "1500000000000000.0", "9.999e15": "9999000000000000.0",
		"1e16": "1.0e+16", "1.5e17": "1.5e+17", "-1.5e17": "-1.5e+17", "1e23": "1.0e+23", "0.000001": "1.0e-6",
		"0.0000012": "1.2e-6", "6e-22": "6.0e-22", "-6.192e-20": "-6.192e-20",
		"1.7976931348623157e308": "1.7976931348623157e+308", "5e-324": "5.0e-324", "0.30000000000000004": "0.30000000000000004",
	} {
		n := canonicalInput(t, text).(jsonNumber)
		var out strings.Builder
		writeCanonicalFloat(n.f, &out)
		if out.String() != expected {
			t.Errorf("%s: %s, want %s", text, out.String(), expected)
		}
	}
	// And through the canonical form of a parsed value.
	if got := CanonicalJSON(canonicalInput(t, "[0, 12, -12, 18446744073709551615, 1.0, 2.5e3, 1e-7, 0.1]")); got != "[0,12,-12,18446744073709551615,1.0,2500.0,1.0e-7,0.1]" {
		t.Error(got)
	}
}

// Rust: fixtures.rs::tests::strings_are_escaped_by_our_own_rule
func TestStringsAreEscapedByOurOwnRule(t *testing.T) {
	var out strings.Builder
	writeCanonicalString("a\"b\\c\b\f\n\r\t\x00\x1f\x7fé€\U0001F600/", &out)
	if want := "\"a\\\"b\\\\c\\b\\f\\n\\r\\t\\u0000\\u001f\x7fé€\U0001F600/\""; out.String() != want {
		t.Errorf("%q, want %q", out.String(), want)
	}
	if got := CanonicalJSON(canonicalInput(t, `{"k\u0001": ""}`)); got != "{\"k\\u0001\":\"\"}" {
		t.Error(got)
	}
}

// Rust: fixtures.rs::tests::the_checksum_is_a_known_sha256
func TestTheChecksumIsAKnownSHA256(t *testing.T) {
	if got := FixtureChecksum(canonicalInput(t, `{ "a" : 1 }`)); got != "015abd7f5cc57a2dd94b7590f04ad8084273905ee33ec5cebeae62276a97f862" {
		t.Error(got)
	}
}

func problemOf(t *testing.T, text string) string {
	t.Helper()
	problems := NumberProblems(canonicalInput(t, text))
	if len(problems) > 1 {
		t.Fatalf("%s: %v", text, problems)
	}
	if len(problems) == 0 {
		return ""
	}
	return problems[0]
}

// Rust: fixtures.rs::tests::numbers_that_could_parse_differently_are_refused
func TestNumbersThatCouldParseDifferentlyAreRefused(t *testing.T) {
	for _, bad := range []string{
		// negative zero, integer or float
		"-0", "-0.0", "-0e5",
		// 16 or more significant digits
		"9007199254740993.0", "0.1234567890123456", "100000000000000000000000.5", "2.2250738585072011e-308",
		// a decimal exponent beyond +-15
		"1e23", "1e-23", "1e16", "1e-16", "-1e-400", "6.192E-20", "1e18", "1.5e100",
		// integers outside i64 and u64
		"18446744073709551616", "-9223372036854775809", "100000000000000000000", "1e19",
	} {
		if problemOf(t, bad) == "" {
			t.Errorf("%s was accepted", bad)
		}
	}
	problem := problemOf(t, "0.1234567890123456")
	if !strings.Contains(problem, "cannot be represented the same way in every build") ||
		!strings.Contains(problem, "write it as a string, or with fewer significant digits") {
		t.Error(problem)
	}
}

// Rust: fixtures.rs::tests::numbers_that_parse_the_same_everywhere_are_accepted
func TestNumbersThatParseTheSameEverywhereAreAccepted(t *testing.T) {
	for _, good := range []string{"0", "0.0", "0.1", "12.50", "1e2", "1e15", "2.5e3", "123456789012345", "-7", "-1.5",
		"18446744073709551615", "-9223372036854775808", "9223372036854775807"} {
		if p := problemOf(t, good); p != "" {
			t.Errorf("%s was refused: %s", good, p)
		}
	}
}

// Rust: fixtures.rs::tests::an_out_of_range_number_is_refused
func TestAnOutOfRangeNumberIsRefused(t *testing.T) {
	// By default it does not even parse.
	if _, err := ParseJSON("1e400"); err == nil {
		t.Error("1e400 parsed")
	}
}
