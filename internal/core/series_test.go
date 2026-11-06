package core

import (
	"strings"
	"testing"
	"time"
)

func seriesDate(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func mustSeries(t *testing.T, s string) *SeriesPattern {
	t.Helper()
	p, err := ParseSeriesPattern(s)
	if err != nil {
		t.Fatalf("ParseSeriesPattern(%q): %v", s, err)
	}
	return p
}

// Rust: series.rs::renders_the_documented_example
func TestRendersTheDocumentedExample(t *testing.T) {
	p := mustSeries(t, "INV-.YYYY.-.#####")
	d := seriesDate(2026, time.October, 29)
	if got := p.Render(d, 7); got != "INV-2026-00007" {
		t.Errorf("render = %q", got)
	}
	if got := p.Prefix(d); got != "INV-2026-" {
		t.Errorf("prefix = %q", got)
	}
	if p.String() != "INV-.YYYY.-.#####" {
		t.Errorf("string = %q", p.String())
	}
	// the module documentation example
	if got := p.Render(d, 1_234_567); got != "INV-2026-1234567" {
		t.Errorf("wider = %q", got)
	}
	if _, err := ParseSeriesPattern("INV-.####.-.####"); err == nil {
		t.Error("two number parts accepted")
	}
}

// Rust: series.rs::renders_every_date_part_zero_padded
func TestRendersEveryDatePartZeroPadded(t *testing.T) {
	p := mustSeries(t, "X.YY.MM.DD.#.-.YYYY")
	d := seriesDate(2005, time.March, 4)
	if got := p.Prefix(d); got != "X050304" {
		t.Errorf("prefix = %q", got)
	}
	if got := p.Render(d, 9); got != "X0503049-2005" {
		t.Errorf("render = %q", got)
	}
	d = seriesDate(2026, time.December, 31)
	if got := p.Render(d, 10); got != "X26123110-2026" {
		t.Errorf("render = %q", got)
	}
}

// Rust: series.rs::a_number_wider_than_the_hashes_is_printed_in_full
func TestANumberWiderThanTheHashesIsPrintedInFull(t *testing.T) {
	p := mustSeries(t, "N-.##")
	d := seriesDate(2026, time.January, 1)
	for _, c := range []struct {
		n    uint64
		want string
	}{{5, "N-05"}, {99, "N-99"}, {100, "N-100"}, {^uint64(0), "N-18446744073709551615"}} {
		if got := p.Render(d, c.n); got != c.want {
			t.Errorf("render(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// Rust: series.rs::the_widest_number_part_is_accepted_and_padded
func TestTheWidestNumberPartIsAcceptedAndPadded(t *testing.T) {
	p := mustSeries(t, strings.Repeat("#", MaxNumberWidth))
	d := seriesDate(2026, time.January, 1)
	if got, want := p.Render(d, 1), strings.Repeat("0", MaxNumberWidth-1)+"1"; got != want {
		t.Errorf("render = %q, want %q", got, want)
	}
	if got := p.Prefix(d); got != "" {
		t.Errorf("prefix = %q", got)
	}
}

// Rust: series.rs::the_prefix_is_the_text_before_the_number_only
func TestThePrefixIsTheTextBeforeTheNumberOnly(t *testing.T) {
	p := mustSeries(t, "A.#.-.YYYY")
	d := seriesDate(2026, time.June, 1)
	if got := p.Prefix(d); got != "A" {
		t.Errorf("prefix = %q", got)
	}
	if got := p.Render(d, 3); got != "A3-2026" {
		t.Errorf("render = %q", got)
	}
	q := mustSeries(t, "A.#####")
	if q.Prefix(d) != p.Prefix(d) {
		t.Error("the same prefix must share the key")
	}
}

// Rust: series.rs::literals_that_look_like_tokens_are_literals_unless_exact
func TestLiteralsThatLookLikeTokensAreLiteralsUnlessExact(t *testing.T) {
	p := mustSeries(t, "yyyy.YYY.Mm.#")
	if got := p.Render(seriesDate(2026, time.June, 1), 1); got != "yyyyYYYMm1" {
		t.Errorf("render = %q", got)
	}
}

// Rust: series.rs::bad_patterns_are_rejected
func TestBadPatternsAreRejected(t *testing.T) {
	cases := []struct {
		pattern string
		want    SeriesError
	}{
		{"", SeriesError{Kind: SeriesEmpty}},
		{"INV-", SeriesError{Kind: SeriesNoNumber}},
		{"YYYY", SeriesError{Kind: SeriesNoNumber}},
		{"A-.####.-.##", SeriesError{Kind: SeriesMultipleNumbers, Count: 2}},
		{".#", SeriesError{Kind: SeriesEmptyPart}},
		{"#.", SeriesError{Kind: SeriesEmptyPart}},
		{"A..#", SeriesError{Kind: SeriesEmptyPart}},
		{".", SeriesError{Kind: SeriesEmptyPart}},
		{"A#.##", SeriesError{Kind: SeriesMixedNumber, Part: "A#"}},
		{"A\x00.##", SeriesError{Kind: SeriesNul}},
		{"#####################", SeriesError{Kind: SeriesNumberTooWide, Width: 21}},
	}
	for _, c := range cases {
		_, err := ParseSeriesPattern(c.pattern)
		if err != error(c.want) {
			t.Errorf("pattern %q: got %v, want %v", c.pattern, err, c.want)
		}
	}
}

func TestSeriesErrorMessages(t *testing.T) {
	cases := map[SeriesError]string{
		{Kind: SeriesEmpty}:                     "must not be empty",
		{Kind: SeriesEmptyPart}:                 "has an empty part (a leading, trailing or doubled `.`)",
		{Kind: SeriesMixedNumber, Part: "A#"}:   "part `A#` mixes `#` with other characters (the number part must be only `#`)",
		{Kind: SeriesNul}:                       "must not contain a NUL character",
		{Kind: SeriesNoNumber}:                  "needs exactly one number part such as `#####`, found none",
		{Kind: SeriesMultipleNumbers, Count: 2}: "needs exactly one number part such as `#####`, found 2",
		{Kind: SeriesNumberTooWide, Width: 21}:  "number part is 21 wide, the maximum is 20",
	}
	for e, want := range cases {
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
