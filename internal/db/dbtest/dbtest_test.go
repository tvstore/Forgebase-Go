package dbtest

import (
	"fmt"
	"strings"
	"testing"
)

// failRecorder is a testing.TB whose Fatalf panics with the message instead of ending the goroutine,
// so a test can see what the helper reports.
type failRecorder struct {
	testing.TB
}

func (failRecorder) Helper() {}

func (failRecorder) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }

func TestWithDatabase(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"postgres://u:p@h:5432/postgres", "postgres://u:p@h:5432/other"},
		{"postgres://u:p@h:5432/postgres?sslmode=disable", "postgres://u:p@h:5432/other?sslmode=disable"},
		{"postgres://u:p@h:5432", "postgres://u:p@h:5432/other"},
	} {
		if got := withDatabase(c.url, "other"); got != c.want {
			t.Errorf("withDatabase(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestURLWithPassword(t *testing.T) {
	t.Setenv(envVar, "postgres://forgebase:forgebase@localhost:5432/postgres")
	if got, want := URLWithPassword(t, "wrong"), "postgres://forgebase:wrong@localhost:5432/postgres"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := URLForDatabase(t, "x"), "postgres://forgebase:forgebase@localhost:5432/x"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A missing variable fails the test with guidance; it never skips.
func TestAMissingVariableFailsWithGuidance(t *testing.T) {
	t.Setenv(envVar, "")
	defer func() {
		message, _ := recover().(string)
		for _, part := range []string{envVar + " is not set", "PostgreSQL 16", "The tests never skip."} {
			if !strings.Contains(message, part) {
				t.Errorf("the message lacks %q: %q", part, message)
			}
		}
	}()
	adminURL(failRecorder{t})
	t.Error("adminURL did not fail")
}
