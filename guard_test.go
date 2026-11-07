package forgebase_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// The guard for the embedded set (testing.AssertDocTypesListed), ported from the guard tests of
// crates/forgebase/tests/apps.rs.

const fixtureDir = "testdata/apps/doctypes"

// fatalTB is a testing.TB whose Fatalf does not stop the test but records the message, so a test can look
// at what the guard says (the Rust tests use #[should_panic(expected = ..)]).
type fatalTB struct {
	testing.TB
	message string
	failed  bool
}

func (f *fatalTB) Helper() {}

type stopped struct{}

func (f *fatalTB) Fatalf(format string, args ...any) {
	f.message, f.failed = fmt.Sprintf(format, args...), true
	panic(stopped{})
}

// guardFailure is what the guard said for app and dir; "" when it passed.
func guardFailure(t *testing.T, a *forgebase.App, dir string) string {
	t.Helper()
	f := &fatalTB{TB: t}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(stopped); !ok {
					panic(r)
				}
			}
		}()
		fbtest.AssertDocTypesListed(f, a, dir)
	}()
	return f.message
}

// Rust: apps.rs::the_guard_passes_when_the_list_and_the_directory_agree
func TestTheGuardPassesWhenTheEmbeddedSetAndTheDirectoryAgree(t *testing.T) {
	if msg := guardFailure(t, fixtureApp(), fixtureDir); msg != "" {
		t.Error(msg)
	}
}

// Rust: apps.rs::the_guard_ignores_a_leading_dot_slash_in_the_listed_paths_and_in_the_directory (Go embeds
// clean paths, so only the directory may carry the ./ and the /)
func TestTheGuardIgnoresALeadingDotSlashAndATrailingSlashInTheDirectory(t *testing.T) {
	for _, dir := range []string{"./" + fixtureDir, "./" + fixtureDir + "/", fixtureDir + "/"} {
		if msg := guardFailure(t, fixtureApp(), dir); msg != "" {
			t.Errorf("%s: %s", dir, msg)
		}
	}
}

// Rust: apps.rs::the_guard_fails_for_a_file_that_is_not_listed
func TestTheGuardFailsForAFileThatIsNotEmbedded(t *testing.T) {
	app := forgebase.NewApp("fixture").DocTypes(docTypes("testdata/apps/doctypes/widget.toml", "name = \"widget\"\nlabel = \"W\"\n"))
	msg := guardFailure(t, app, fixtureDir)
	if !strings.Contains(msg, `in the directory but not embedded: ["testdata/apps/doctypes/gizmo.toml"]`) {
		t.Errorf("%q", msg)
	}
}

// Rust: apps.rs::the_guard_fails_for_a_listed_file_that_is_not_in_the_directory
func TestTheGuardFailsForAnEmbeddedFileThatIsNotInTheDirectory(t *testing.T) {
	// A file of another directory in the embedded set.
	app := fixtureApp().DocTypes(docTypes("elsewhere/seed.toml", "name = \"seed\"\nlabel = \"S\"\n"))
	msg := guardFailure(t, app, fixtureDir)
	if !strings.Contains(msg, `embedded but not in the directory: ["elsewhere/seed.toml"]`) {
		t.Errorf("%q", msg)
	}
}

// Rust: apps.rs::the_guard_compares_paths_not_bare_file_names
func TestTheGuardComparesPathsNotBareFileNames(t *testing.T) {
	// other/widget.toml has the file name of a file in the directory, but it is another path: the
	// directory's widget.toml is still not embedded.
	app := forgebase.NewApp("fixture").DocTypes(docTypes(
		"other/widget.toml", "name = \"widget\"\nlabel = \"W\"\n",
		"testdata/apps/doctypes/gizmo.toml", "name = \"gizmo\"\nlabel = \"G\"\n"))
	msg := guardFailure(t, app, fixtureDir)
	if !strings.Contains(msg, `in the directory but not embedded: ["testdata/apps/doctypes/widget.toml"]`) {
		t.Errorf("%q", msg)
	}
}

// Rust: apps.rs::the_guard_fails_for_a_missing_directory
func TestTheGuardFailsForAMissingDirectory(t *testing.T) {
	msg := guardFailure(t, fixtureApp(), "no/such/doctypes")
	if !strings.Contains(msg, "cannot read DocType directory") {
		t.Errorf("%q", msg)
	}
}

// Not in Rust: the guard reads the directory with the rules of DocTypes (any depth, hidden names ignored),
// the same ones the embedded set was read with.
func TestTheGuardUsesTheRulesOfDocTypes(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, path := range []string{"doctypes/a.toml", "doctypes/deep/b.toml", "doctypes/.hidden.toml", "doctypes/c.txt", "doctypes/.git/d.toml"} {
		writeFile(t, path, "x")
	}
	app := forgebase.NewApp("x").DocTypes(docTypes("doctypes/a.toml", "x", "doctypes/deep/b.toml", "x"))
	if msg := guardFailure(t, app, "doctypes"); msg != "" {
		t.Errorf("%s", msg)
	}
	// The directory as ".": the paths have no prefix.
	writeFile(t, "top.toml", "x")
	rooted := forgebase.NewApp("x").DocTypes(docTypes("top.toml", "x", "doctypes/a.toml", "x", "doctypes/deep/b.toml", "x"))
	if msg := guardFailure(t, rooted, "."); msg != "" {
		t.Errorf("%s", msg)
	}
}
