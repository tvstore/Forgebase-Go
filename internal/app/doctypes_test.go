package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from crates/forgebase/src/doctypes.rs (the unit tests of the module). The Rust tests load
// the directory with compose(&[], Some(dir)); here compose(nil, dir) is the same call.

const goodTOML = "name = \"{n}\"\nlabel = \"L\"\n[[fields]]\nname = \"f\"\ntype = \"text\"\n"

func good(name string) string { return strings.ReplaceAll(goodTOML, "{n}", name) }

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// loadDir is the DocTypes of one directory, loaded as the app local: their names, or the problems.
func loadDir(dir string) ([]string, []string) {
	c, startup := compose(nil, dir)
	if startup != nil {
		return nil, startup.Problems()
	}
	var names []string
	for _, d := range c.registry.Iter() {
		names = append(names, d.Name)
	}
	return names, nil
}

func mustLoad(t *testing.T, dir string) []string {
	t.Helper()
	names, problems := loadDir(dir)
	if problems != nil {
		t.Fatalf("must load: %v", problems)
	}
	return names
}

func mustFail(t *testing.T, dir string) []string {
	t.Helper()
	names, problems := loadDir(dir)
	if problems == nil {
		t.Fatalf("must fail, loaded %v", names)
	}
	return problems
}

func anyContains(list []string, parts ...string) bool {
	for _, item := range list {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(item, p)
		}
		if all {
			return true
		}
	}
	return false
}

// Rust: doctypes.rs::loads_toml_files_and_ignores_everything_else
func TestLoadsTomlFilesAndIgnoresEverythingElse(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"b.toml": good("beta"), "a.toml": good("alpha"), "notes.md": "not a doctype", "broken.toml.bak": "garbage",
	})
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, filepath.Join(dir, "sub"), map[string]string{"c.toml": good("gamma")})
	if err := os.Mkdir(filepath.Join(dir, "dir.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(mustLoad(t, dir), ","); got != "alpha,beta" {
		t.Fatalf("%s", got)
	}
}

// Rust: doctypes.rs::errors_name_the_file_and_are_all_reported
func TestErrorsNameTheFileAndAreAllReported(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"good.toml": good("good"), "bad.toml": "name = ", "dup.toml": good("good"), "invalid.toml": good("Bad Name"),
	})
	problems := mustFail(t, dir)
	if len(problems) != 3 {
		t.Fatalf("%v", problems)
	}
	for _, name := range []string{"bad.toml", "invalid.toml", "good.toml"} {
		if !anyContains(problems, name) {
			t.Errorf("no problem names %s: %v", name, problems)
		}
	}
}

// Rust: doctypes.rs::hidden_files_are_skipped_and_the_extension_is_case_sensitive
func TestHiddenFilesAreSkippedAndTheExtensionIsCaseSensitive(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.toml": good("alpha"), ".hidden.toml": "garbage", "._a.toml": "\x00\x05binary", "UPPER.TOML": "garbage",
	})
	if got := strings.Join(mustLoad(t, dir), ","); got != "alpha" {
		t.Fatalf("%s", got)
	}
}

// Rust: doctypes.rs::dangling_symlink_is_reported_with_its_path
func TestDanglingSymlinkIsReportedWithItsPath(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.toml": good("alpha")})
	if err := os.Symlink(filepath.Join(dir, "missing-target"), filepath.Join(dir, "link.toml")); err != nil {
		t.Fatal(err)
	}
	// A symlink to a real file is followed.
	writeFiles(t, dir, map[string]string{"real.txt": good("beta")})
	if err := os.Symlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "ok.toml")); err != nil {
		t.Fatal(err)
	}
	problems := mustFail(t, dir)
	if len(problems) != 1 || !strings.Contains(problems[0], "link.toml") {
		t.Fatalf("%v", problems)
	}
}

func linking(name, target string) string {
	return "name = \"" + name + "\"\nlabel = \"L\"\n[[fields]]\nname = \"f\"\ntype = \"link\"\ntarget = \"" + target + "\"\n"
}

// Rust: doctypes.rs::links_between_files_load_in_any_order_and_unknown_targets_name_the_file
func TestLinksBetweenFilesLoadInAnyOrderAndUnknownTargetsNameTheFile(t *testing.T) {
	dir := t.TempDir()
	// a links to b, which is loaded later; b links to itself.
	writeFiles(t, dir, map[string]string{"a.toml": linking("a", "b"), "b.toml": linking("b", "b")})
	if got := mustLoad(t, dir); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	writeFiles(t, dir, map[string]string{"c.toml": linking("c", "nowhere")})
	problems := mustFail(t, dir)
	if len(problems) != 1 || !strings.Contains(problems[0], "c.toml") || !strings.Contains(problems[0], "field `f` links to unknown doctype `nowhere`") {
		t.Fatalf("%v", problems)
	}
	// A target in a file that failed to load is not reported again.
	writeFiles(t, dir, map[string]string{"d.toml": "name = ", "c.toml": linking("c", "d")})
	problems = mustFail(t, dir)
	if len(problems) != 1 || !strings.Contains(problems[0], "d.toml") {
		t.Fatalf("%v", problems)
	}
}

// Rust: doctypes.rs::child_table_problems_name_the_file
func TestChildTableProblemsNameTheFile(t *testing.T) {
	dir := t.TempDir()
	parent := func(name, target string) string {
		return "name = \"" + name + "\"\nlabel = \"P\"\n[[fields]]\nname = \"rows\"\ntype = \"table\"\ntarget = \"" + target + "\"\n"
	}
	child := "name = \"row\"\nlabel = \"R\"\nis_child = true\n"
	writeFiles(t, dir, map[string]string{"p.toml": parent("p", "row"), "row.toml": child})
	if got := mustLoad(t, dir); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	// Used twice: reported once, with the child's file.
	writeFiles(t, dir, map[string]string{"q.toml": parent("q", "row")})
	problems := mustFail(t, dir)
	if len(problems) != 1 || !strings.Contains(problems[0], "row.toml") || !strings.Contains(problems[0], "used by more than one table field: p.rows, q.rows") {
		t.Fatalf("%v", problems)
	}
	// Unknown target and a target that is not a child, each with the parent's file.
	if err := os.Remove(filepath.Join(dir, "q.toml")); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{"p.toml": parent("p", "nowhere")})
	problems = mustFail(t, dir)
	if len(problems) != 2 || !anyContains(problems, "p.toml", "unknown doctype `nowhere`") || !anyContains(problems, "row.toml", "not used by any table field") {
		t.Fatalf("%v", problems)
	}
	writeFiles(t, dir, map[string]string{"p.toml": parent("p", "p")})
	if problems = mustFail(t, dir); !anyContains(problems, "p.toml", "not a child doctype") {
		t.Fatalf("%v", problems)
	}
}

// Rust: doctypes.rs::missing_directory_is_an_error
func TestMissingDirectoryIsAnError(t *testing.T) {
	problems := mustFail(t, "/nonexistent/forgebase/doctypes")
	if len(problems) != 1 || !strings.Contains(problems[0], "cannot read DocType directory") {
		t.Fatalf("%v", problems)
	}
	if !strings.Contains(problems[0], "No such file or directory (os error 2)") {
		t.Errorf("the file error reads like Rust's: %s", problems[0])
	}
}

// Rust: doctypes.rs::empty_directory_gives_an_empty_registry
func TestEmptyDirectoryGivesAnEmptyRegistry(t *testing.T) {
	if got := mustLoad(t, t.TempDir()); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

// A DocType file may not use the prefix of site custom fields (compose.rs, load).
func TestAFieldWithTheCustomPrefixIsRefusedNamingTheFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.toml": "name = \"a\"\nlabel = \"A\"\n[[fields]]\nname = \"custom_x\"\ntype = \"text\"\n",
	})
	problems := mustFail(t, dir)
	if len(problems) != 1 || !strings.Contains(problems[0], "a.toml") || !strings.Contains(problems[0], "a prefix reserved for site custom fields") {
		t.Fatalf("%v", problems)
	}
}

// The path in a message is the directory as the user wrote it plus the file name.
func TestPathsInMessagesKeepTheDirectoryAsWritten(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"broken.toml": "name = "})
	problems := mustFail(t, dir+"/")
	if len(problems) != 1 || !strings.HasPrefix(problems[0], dir+"/broken.toml: ") {
		t.Fatalf("%v", problems)
	}
}

func TestStartupErrorPrintsTheTwoListsInOrder(t *testing.T) {
	e := &StartupError{apps: []string{"a1"}, files: []string{"f1", "f2"}}
	want := "invalid app setup:\n  - a1\ncannot load DocType files:\n  - f1\n  - f2"
	if e.Error() != want {
		t.Fatalf("%q", e.Error())
	}
	if got := strings.Join(e.Problems(), ","); got != "a1,f1,f2" {
		t.Fatalf("%s", got)
	}
}
