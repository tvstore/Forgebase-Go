package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Ported from crates/forgebase/tests/new_app.rs and the unit tests of crates/forgebase/src/new_app.rs.
// No database is needed. That the generated module builds and passes its own tests is checked by the CI
// job app-template (it needs the network for nothing but the module cache; CONTRIBUTING.md says how to run
// it locally).

const fakeGoMod = "module github.com/tvstore/forgebase-go\n\ngo 1.26.0\n\ntoolchain go1.27.3\n\nrequire (\n\tgithub.com/b/b v1.2.0\n\tgithub.com/a/a v0.1.0\n)\n\nrequire github.com/c/c v2.0.0 // indirect\n\nignore ./desk/node_modules\n"

const fakeGoSum = "github.com/a/a v0.1.0 h1:x=\n"

// newAppScratch is a temporary directory (resolved) and a fake Forgebase checkout in it.
type newAppScratch struct {
	t    *testing.T
	root string
}

func newScratch(t *testing.T) *newAppScratch {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &newAppScratch{t: t, root: root}
}

func (s *newAppScratch) checkoutAt(dir string) string {
	s.t.Helper()
	if err := os.MkdirAll(dir, 0o777); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(fakeGoMod), 0o666); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), []byte(fakeGoSum), 0o666); err != nil {
		s.t.Fatal(err)
	}
	return dir
}

func (s *newAppScratch) checkout() string { return s.checkoutAt(filepath.Join(s.root, "checkout")) }

// newAppCmd runs `forgebase new-app args...` in process, with cwd as the working directory.
func (s *newAppScratch) newAppCmd(cwd string, args ...string) result {
	s.t.Helper()
	s.t.Chdir(cwd)
	return runCLI(map[string]string{}, "", append([]string{"new-app"}, args...)...)
}

func filesOf(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		found = append(found, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found)
	return found
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Rust: new_app.rs::it_writes_the_crate_with_every_placeholder_replaced
func TestNewAppWritesTheModuleWithEveryPlaceholderReplaced(t *testing.T) {
	s := newScratch(t)
	checkout := s.checkout()
	r := s.newAppCmd(s.root, "my_app", "--forgebase-path", checkout)
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	created := filepath.Join(s.root, "my_app")
	// The default --dir is the current directory: the path is shown as ./my_app.
	if !strings.HasPrefix(r.stdout, "created ./my_app\n") {
		t.Errorf("%q", r.stdout)
	}
	expectContains(t, r.stdout, "cd ./my_app", "go test ./...")
	want := []string{".gitignore", "AGENTS.md", "README.md", "app.go", "app_test.go", "cmd/my_app/main.go", "cmd/my_app/main_test.go",
		"doctypes/my_app_note.toml", "go.mod", "go.sum", "migrations/README.md"}
	if got := filesOf(t, created); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("files %v, want %v", got, want)
	}
	for _, file := range want {
		if file != "go.sum" && strings.Contains(read(t, filepath.Join(created, file)), "{{") {
			t.Errorf("%s still has a placeholder", file)
		}
	}
	goMod := read(t, filepath.Join(created, "go.mod"))
	for _, part := range []string{
		"module my_app\n", "\ngo 1.26.0\n", "\ntoolchain go1.27.3\n",
		"require github.com/tvstore/forgebase-go v0.0.0\n",
		"replace github.com/tvstore/forgebase-go => " + checkout + "\n",
		// Sorted, all indirect, from every require of the checkout and nothing else.
		"\tgithub.com/a/a v0.1.0 // indirect\n\tgithub.com/b/b v1.2.0 // indirect\n\tgithub.com/c/c v2.0.0 // indirect\n",
	} {
		if !strings.Contains(goMod, part) {
			t.Errorf("go.mod lacks %q:\n%s", part, goMod)
		}
	}
	if strings.Contains(goMod, "ignore") {
		t.Errorf("the ignore directive of the checkout must not be copied:\n%s", goMod)
	}
	lib := read(t, filepath.Join(created, "app.go"))
	expectContains(t, lib, `forgebase.NewApp("my_app")`, "package my_app", `Hooks("my_app_note", NoteRules{})`, "//go:embed doctypes/*.toml")
	expectContains(t, read(t, filepath.Join(created, "cmd/my_app/main.go")), `apppkg "my_app"`, "apppkg.App()")
	// The go.sum of the checkout is copied as it is.
	if got := read(t, filepath.Join(created, "go.sum")); got != fakeGoSum {
		t.Errorf("go.sum %q", got)
	}
	expectContains(t, read(t, filepath.Join(created, "README.md")), "Needs Go 1.26.0 or newer")
}

// Rust: new_app.rs::the_sample_doctype_is_valid_and_open_to_the_sample_role
func TestNewAppSampleDocTypeIsValidAndOpenToTheSampleRole(t *testing.T) {
	s := newScratch(t)
	if r := s.newAppCmd(s.root, "my_app", "--forgebase-path", s.checkout()); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	doctype, err := core.ParseDocType(read(t, filepath.Join(s.root, "my_app/doctypes/my_app_note.toml")))
	if err != nil {
		t.Fatalf("a valid DocType: %v", err)
	}
	if doctype.Name != "my_app_note" || len(doctype.Permissions) != 1 || doctype.Permissions[0].Role != "My App User" {
		t.Errorf("%+v", doctype)
	}
	if !strings.Contains(read(t, filepath.Join(s.root, "my_app/app_test.go")), `"My App User"`) {
		t.Error("the test uses the same role")
	}
}

// Rust: new_app.rs::a_relative_forgebase_path_is_resolved_from_where_the_command_runs
func TestNewAppRelativePathsAreResolvedFromTheWorkingDirectory(t *testing.T) {
	s := newScratch(t)
	checkout := s.checkout()
	// --dir is relative too, and does not exist yet.
	r := s.newAppCmd(s.root, "demo", "--forgebase-path", "checkout", "--dir", "apps/here")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	goMod := read(t, filepath.Join(s.root, "apps/here/demo/go.mod"))
	if !strings.Contains(goMod, "=> "+checkout+"\n") {
		t.Errorf("%s", goMod)
	}
}

// Rust: new_app.rs::it_works_without_a_lock_file_in_the_checkout
func TestNewAppWorksWithoutAGoSumInTheCheckout(t *testing.T) {
	s := newScratch(t)
	checkout := s.checkout()
	if err := os.Remove(filepath.Join(checkout, "go.sum")); err != nil {
		t.Fatal(err)
	}
	if r := s.newAppCmd(s.root, "demo", "--forgebase-path", checkout); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(s.root, "demo/go.sum")); !os.IsNotExist(err) {
		t.Errorf("go.sum was created: %v", err)
	}
}

// Rust: new_app.rs::it_refuses_an_existing_directory_and_leaves_it_alone
func TestNewAppRefusesAnExistingDirectoryAndLeavesItAlone(t *testing.T) {
	s := newScratch(t)
	checkout := s.checkout()
	existing := filepath.Join(s.root, "demo")
	if err := os.MkdirAll(existing, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "mine.txt"), []byte("keep"), 0o666); err != nil {
		t.Fatal(err)
	}
	r := s.newAppCmd(s.root, "demo", "--forgebase-path", checkout)
	if r.code != 1 || !strings.HasPrefix(r.stderr, "error: ") || !strings.Contains(r.stderr, "already exists") {
		t.Fatalf("%+v", r)
	}
	if got := filesOf(t, existing); len(got) != 1 || got[0] != "mine.txt" || read(t, filepath.Join(existing, "mine.txt")) != "keep" {
		t.Errorf("%v", got)
	}
}

// Rust: new_app.rs::it_refuses_a_path_that_is_not_a_forgebase_checkout
func TestNewAppRefusesAPathThatIsNotAForgebaseCheckout(t *testing.T) {
	s := newScratch(t)
	elsewhere := filepath.Join(s.root, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o777); err != nil {
		t.Fatal(err)
	}
	// Missing go.mod.
	r := s.newAppCmd(s.root, "demo", "--forgebase-path", elsewhere)
	if r.code != 1 || !strings.Contains(r.stderr, "not a Forgebase checkout") {
		t.Fatalf("%+v", r)
	}
	// A go.mod of another module.
	if err := os.WriteFile(filepath.Join(elsewhere, "go.mod"), []byte("module example.com/other\n\ngo 1.26.0\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	r = s.newAppCmd(s.root, "demo", "--forgebase-path", elsewhere)
	if r.code != 1 || !strings.Contains(r.stderr, "not a Forgebase checkout") {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(s.root, "demo")); !os.IsNotExist(err) {
		t.Error("nothing is created")
	}
}

// Rust: new_app.rs::it_refuses_names_that_are_not_app_names_or_crate_names
func TestNewAppRefusesNamesThatAreNotAppNamesOrPackageNames(t *testing.T) {
	s := newScratch(t)
	checkout := s.checkout()
	long := strings.Repeat("a", 55)
	for _, c := range []struct{ name, want string }{
		{"My-App", "invalid app name:\n  - app name \"My-App\""},
		{"My-App", "must match"},
		{"1app", "must match"},
		{"", "must not be empty"},
		{"local", "reserved"},
		{"forgebase", "reserved"},
		{"fmt", "cannot be used as a package name"},
		{"main", "cannot be used as a package name"},
		{"string", "cannot be used as a package name"},
		{long, "the maximum is 54"},
	} {
		r := s.newAppCmd(s.root, c.name, "--forgebase-path", checkout)
		if r.code != 1 || !strings.Contains(r.stderr, c.want) {
			t.Errorf("name %q: %+v", c.name, r)
		}
	}
	// Nothing but the checkout was created.
	if entries, _ := os.ReadDir(s.root); len(entries) != 1 {
		t.Errorf("%d entries", len(entries))
	}
}

// Rust: new_app.rs::it_refuses_rust_keywords_as_names (here: Go keywords)
func TestNewAppRefusesGoKeywordsAsNames(t *testing.T) {
	s := newScratch(t)
	checkout := s.checkout()
	for _, name := range []string{"func", "type", "package", "go", "map", "range", "select", "var", "import", "chan", "defer"} {
		r := s.newAppCmd(s.root, name, "--forgebase-path", checkout)
		if r.code != 1 || !strings.Contains(r.stderr, "`"+name+"` is a Go keyword") {
			t.Errorf("name %q: %+v", name, r)
		}
		if _, err := os.Stat(filepath.Join(s.root, name)); !os.IsNotExist(err) {
			t.Errorf("name %q: created", name)
		}
	}
	// Names that merely contain a keyword, and Rust keywords that are fine in Go.
	for _, name := range []string{"match", "fn", "type_a", "func_rules", "union", "binary", "client", "t"} {
		if r := s.newAppCmd(s.root, name, "--forgebase-path", checkout); r.code != 0 {
			t.Errorf("name %q: %+v", name, r)
		}
	}
}

// Rust: new_app.rs::a_path_with_a_space_a_quote_and_a_backslash_is_escaped_in_cargo_toml (go.mod here)
func TestNewAppQuotesAnOddCheckoutPathInGoMod(t *testing.T) {
	s := newScratch(t)
	root := s.checkoutAt(filepath.Join(s.root, `my "odd" check\out`))
	if r := s.newAppCmd(s.root, "demo", "--forgebase-path", root); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	goMod := read(t, filepath.Join(s.root, "demo/go.mod"))
	// A quoted Go string: the quote and the backslash are escaped.
	want := `replace github.com/tvstore/forgebase-go => "` + s.root + `/my \"odd\" check\\out"` + "\n"
	if !strings.Contains(goMod, want) {
		t.Errorf("go.mod lacks %q:\n%s", want, goMod)
	}
}

// Rust: new_app.rs::placeholder_text_in_the_checkout_path_is_not_replaced
func TestNewAppDoesNotReplacePlaceholderTextInTheCheckoutPath(t *testing.T) {
	s := newScratch(t)
	root := s.checkoutAt(filepath.Join(s.root, "{{name}}-{{title}}"))
	if r := s.newAppCmd(s.root, "demo", "--forgebase-path", root); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	goMod := read(t, filepath.Join(s.root, "demo/go.mod"))
	if !strings.Contains(goMod, "=> "+goModPath(root)+"\n") || !strings.Contains(goMod, "module demo\n") {
		t.Errorf("%s", goMod)
	}
	if !strings.Contains(goMod, "{{name}}-{{title}}") {
		t.Errorf("the path was changed:\n%s", goMod)
	}
}

// Rust: new_app.rs::the_next_steps_quote_a_path_with_spaces
func TestNewAppNextStepsQuoteAPathWithSpaces(t *testing.T) {
	s := newScratch(t)
	r := s.newAppCmd(s.root, "demo", "--forgebase-path", s.checkout(), "--dir", "my apps")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	expectContains(t, r.stdout, "created my apps/demo\n", "  cd 'my apps/demo'\n")
}

// Rust: new_app.rs::a_name_of_54_bytes_is_accepted
func TestNewAppAcceptsANameOf54Bytes(t *testing.T) {
	s := newScratch(t)
	name := strings.Repeat("a", 54)
	if r := s.newAppCmd(s.root, name, "--forgebase-path", s.checkout()); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if _, err := core.ParseDocType(read(t, filepath.Join(s.root, name, "doctypes", name+"_note.toml"))); err != nil {
		t.Errorf("the longest sample DocType name is valid: %v", err)
	}
}

// Rust: new_app.rs::the_path_option_is_required
func TestNewAppTheForgebasePathIsRequired(t *testing.T) {
	s := newScratch(t)
	r := s.newAppCmd(s.root, "demo")
	if r.code != 2 || !strings.Contains(r.stderr, "--forgebase-path") {
		t.Fatalf("%+v", r)
	}
}

// Rust: new_app.rs::titles
func TestTitles(t *testing.T) {
	for in, want := range map[string]string{"demo": "Demo", "my_app": "My App", "a1_b2": "A1 B2"} {
		if got := title(in); got != want {
			t.Errorf("title(%q) = %q, want %q", in, got, want)
		}
	}
}

// Rust: new_app.rs::render_replaces_in_one_pass
func TestRenderReplacesInOnePass(t *testing.T) {
	values := [][2]string{{"{{name}}", "a{{title}}b"}, {"{{title}}", "T"}}
	// A replaced value is not scanned again; an unknown or unfinished {{ stays.
	if got, want := renderTemplate("x {{name}} {{title}} {{other}} {{ {{name", values), "x a{{title}}b T {{other}} {{ {{name"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if got := renderTemplate("no placeholder", values); got != "no placeholder" {
		t.Errorf("%q", got)
	}
}

// Rust: new_app.rs::shell_quoting
func TestShellQuoting(t *testing.T) {
	for in, want := range map[string]string{
		"./my_app": "./my_app", "/tmp/a-b/c.d": "/tmp/a-b/c.d", "/tmp/my apps/x": "'/tmp/my apps/x'",
		"it's": `'it'\''s'`, "a$b;c": "'a$b;c'", "": "''",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// Rust: new_app.rs::a_failed_write_removes_the_directory_it_created
func TestAFailedWriteRemovesTheDirectoryItCreated(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "demo")
	// a.txt is a file, so writing a.txt/b.txt fails after the first file was written.
	files := [][2]string{{"a.txt", "one"}, {"a.txt/b.txt", "two"}}
	err := generate(target, func(text string) string { return text }, parent, files)
	if err == nil || !strings.Contains(err.Error(), "cannot write") {
		t.Fatalf("%v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Error("the half-written directory is removed")
	}
	// The parent, which was not created by generate, stays.
	if info, statErr := os.Stat(parent); statErr != nil || !info.IsDir() {
		t.Error("the parent must stay")
	}
}

// Rust: new_app.rs::every_template_placeholder_is_known
func TestEveryTemplatePlaceholderIsKnown(t *testing.T) {
	known := map[string]bool{"{{name}}": true, "{{title}}": true, "{{forgebase_path}}": true, "{{go_version}}": true,
		"{{go_directives}}": true, "{{requires}}": true}
	check := func(where, text string) {
		for _, part := range strings.Split(text, "{{")[1:] {
			placeholder := "{{" + strings.SplitN(part, "}}", 2)[0] + "}}"
			if !known[placeholder] {
				t.Errorf("%s: unknown placeholder %s", where, placeholder)
			}
		}
	}
	for _, f := range newAppFiles {
		check(f[0], f[0])
		text, err := templates.ReadFile("templates/app/" + f[1])
		if err != nil {
			t.Errorf("template %s: %v", f[1], err)
			continue
		}
		check(f[1], string(text))
	}
}

// Not in Rust: the go.mod reading (the Rust template takes the MSRV from the crate).
func TestParseGoMod(t *testing.T) {
	m := parseGoMod(fakeGoMod)
	if m.goDirective != "go 1.26.0" || m.toolchain != "toolchain go1.27.3" || len(m.requires) != 3 || m.requires[0][0] != "github.com/a/a" {
		t.Errorf("%+v", m)
	}
	if got := m.directives(); got != "go 1.26.0\n\ntoolchain go1.27.3" {
		t.Errorf("%q", got)
	}
	if only := parseGoMod("module x\n\ngo 1.26.0\n"); only.directives() != "go 1.26.0" || only.requireLines() != "" {
		t.Errorf("%+v", only)
	}
}

// Not in Rust: every name that is accepted must give a module that compiles. The names are the ones that the
// templates' own identifiers could clash with if the app package were imported under its name (t,
// binary, doctype, ...), and names that Go gives a meaning to in file names (_test, _linux, _windows_amd64).
// The template imports the app package as apppkg, so none of them may break; apppkg itself is one.
func TestEveryAcceptedNameGeneratesAModuleThatVets(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	names := []string{"t", "binary", "doctype", "apps", "apppkg", "rules", "ctx", "doc", "ann", "testdb", "doctypes", "app", "foo_test", "x_linux", "x_windows_amd64", "title", "forgebase_x", "migrations", "db",
		"fbtest", "com0", "conin", "x_test", "ext", "test", "go1", "example", "vet", "modx", "sum", "bug", "env", "internal_", "a"}
	for _, name := range names {
		if r := runCLI(map[string]string{}, "", "new-app", name, "--forgebase-path", root, "--dir", dir); r.code != 0 {
			t.Fatalf("%s: %+v", name, r)
		}
		cmd := exec.Command("go", "vet", "./...")
		cmd.Dir = filepath.Join(dir, name)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("go vet of the module generated for %q: %v\n%s", name, err, out)
		}
	}
}

// Not in Rust: every name in the refused list is refused, the list itself being iterated (keywords, predeclared
// identifiers, the standard library's roots, the names the go command treats specially, the Windows device names).
// Each of the special ones was verified once by generating a module and running go build: std, all, tool and
// work are not importable, toolchain is not a module path, mod is "disallowed", builtin and arena are
// ambiguous with $GOROOT/src, the device names are not path elements, and ./... skips testdata. internal and
// vendor are refused as a precaution only.
func TestNewAppRefusesEveryNameOfTheRefusedList(t *testing.T) {
	s := newScratch(t)
	checkout := s.checkout()
	for _, must := range []string{"std", "all", "tool", "work", "toolchain", "mod", "builtin", "arena", "testdata", "con", "com1", "lpt9", "main", "unsafe"} {
		if !notAPackageName[must] {
			t.Errorf("%q is not in the refused list", must)
		}
	}
	s.t.Chdir(s.root)
	for name := range notAPackageName {
		r := runCLI(map[string]string{}, "", "new-app", name, "--forgebase-path", checkout)
		want := "cannot be used as a package name here"
		if goKeywords[name] {
			want = "is a Go keyword"
		}
		if r.code != 1 || !strings.Contains(r.stderr, want) {
			t.Errorf("name %q: %+v", name, r)
		}
		if _, err := os.Stat(filepath.Join(s.root, name)); !os.IsNotExist(err) {
			t.Errorf("name %q: created", name)
		}
	}
}

// Not in Rust: no file of a generated app is ignored by its own .gitignore, whatever the name (a line
// /NAME would have ignored the directories doctypes/ and migrations/ of an app with that name).
func TestNoFileOfAGeneratedAppIsIgnoredByItsGitignore(t *testing.T) {
	s := newScratch(t)
	checkout := s.checkout()
	for _, name := range []string{"doctypes", "migrations", "cmd", "bin", "demo"} {
		if r := s.newAppCmd(s.root, name, "--forgebase-path", checkout); r.code != 0 && name != "cmd" {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	for _, name := range []string{"doctypes", "migrations", "bin", "demo"} {
		dir := filepath.Join(s.root, name)
		if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		out, err := exec.Command("git", "-C", dir, "add", "-A", "--dry-run").CombinedOutput()
		if err != nil {
			t.Fatalf("git add: %v %s", err, out)
		}
		added := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			added[strings.Trim(strings.TrimPrefix(line, "add "), "'")] = true
		}
		for _, file := range filesOf(t, dir) {
			if strings.HasPrefix(file, ".git/") {
				continue
			}
			if !added[file] {
				t.Errorf("app %q: %s is ignored by its .gitignore", name, file)
			}
		}
	}
}
