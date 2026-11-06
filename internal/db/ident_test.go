package db

import "testing"

// Rust: ident.rs::quoting
func TestQuoting(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"doc_customer", `"doc_customer"`},
		{`a"b`, `"a""b"`},
		{`x"; DROP TABLE t; --`, `"x""; DROP TABLE t; --"`},
		{"", `""`},
	} {
		if got := QuoteIdent(c.in); got != c.want {
			t.Errorf("QuoteIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
