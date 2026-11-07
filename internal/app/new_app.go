package app

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// `forgebase new-app`: writes a small app module that builds and passes its own tests (the Rust module
// new_app.rs).
//
// The files are in templates/app/ and embedded with //go:embed. They are plain text with placeholders
// that are replaced in one pass over the text (no template engine, and a replaced value is never scanned
// again): {{name}}, {{title}} (the name as words, for labels and the role), {{forgebase_path}} (the
// Forgebase checkout), {{go_version}}, {{go_directives}} (the `go` and `toolchain` lines of the checkout)
// and {{requires}} (its requirements, which the new module needs as indirect ones). The template files
// end in .tpl so that the Go tool never compiles them.

//go:embed templates/app
var templates embed.FS

// newAppFiles are (path in the new module, template). A path may contain placeholders too.
var newAppFiles = [][2]string{
	{"go.mod", "go.mod.tpl"},
	{".gitignore", "gitignore.tpl"},
	{"README.md", "README.md.tpl"},
	{"AGENTS.md", "AGENTS.md.tpl"},
	{"app.go", "app.go.tpl"},
	{"app_test.go", "app_test.go.tpl"},
	{"cmd/{{name}}/main.go", "main.go.tpl"},
	{"cmd/{{name}}/main_test.go", "main_test.go.tpl"},
	{"doctypes/{{name}}_note.toml", "note.toml.tpl"},
	{"migrations/README.md", "migrations_README.md.tpl"},
}

// notAPackageName are names that cannot be the name of the app: a Go keyword, a predeclared identifier, or
// an import path that means something else (a package of the standard library). The generated main
// imports the app package by its name.
var notAPackageName = map[string]bool{}

// goKeywords are the keywords of Go: the one list the name rule and the error message use.
var goKeywords = map[string]bool{}

func init() {
	for _, name := range strings.Fields(`
		break default func interface select case defer go map struct chan else goto package switch const
		fallthrough if range type continue for import return var`) {
		goKeywords[name] = true
		notAPackageName[name] = true
	}
	for _, name := range strings.Fields(`
		bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint uint8
		uint16 uint32 uint64 uintptr any comparable true false iota nil append cap clear close complex copy
		delete imag len make max min new panic print println real recover`) {
		notAPackageName[name] = true
	}
	// The roots of the standard library's import paths (go list std), and the names the generated files use
	// for imports. Nothing else the templates declare can clash: the app package is imported under the alias
	// apppkg, and every other identifier is local to a function or private to the package under test.
	for _, name := range strings.Fields(`
		archive bufio bytes cmp compress container context crypto database debug embed encoding errors expvar
		flag fmt hash html image index io iter log maps math mime net os path plugin reflect regexp runtime
		slices sort strconv strings structs sync syscall testing text time unicode unique unsafe uuid vendor
		weak cmd main internal simd fbtest`) {
		notAPackageName[name] = true
	}
}

// maxAppNameLen is the longest app name: the sample DocType <name>_note must fit the DocType name limit.
const maxAppNameLen = 54

// newApp creates <dir>/<name>/ and returns its path.
func newApp(name, forgebasePath, dir string) (string, error) {
	problems := AppNameProblems(name)
	if len(problems) == 0 && len(name) > maxAppNameLen {
		problems = append(problems, fmt.Sprintf(
			"app name %s is %d bytes long, the maximum is %d (the sample DocType `%s_note` must fit the 59 bytes of a DocType name)",
			rustQuote(name), len(name), maxAppNameLen, name))
	}
	switch {
	case goKeywords[name]:
		problems = append(problems, fmt.Sprintf("app name `%s` is a Go keyword and cannot be the name of a package here", name))
	case notAPackageName[name]:
		problems = append(problems, fmt.Sprintf("app name `%s` cannot be used as a package name here", name))
	}
	if len(problems) > 0 {
		return "", errf("invalid app name:\n%s", bullets(problems))
	}
	checkout, err := forgebaseCheckout(forgebasePath)
	if err != nil {
		return "", err
	}
	target := joinPath(dir, name)
	if _, statErr := os.Lstat(target); statErr == nil {
		return "", errf("%s already exists; new-app never overwrites", target)
	}
	goMod, err := os.ReadFile(filepath.Join(checkout.root, "go.mod"))
	if err != nil {
		return "", errf("cannot read %s: %s", filepath.Join(checkout.root, "go.mod"), ioErrorText(err))
	}
	module := parseGoMod(string(goMod))
	if module.goDirective == "" {
		return "", errf("%s has no `go` line", filepath.Join(checkout.root, "go.mod"))
	}
	if err := checkPathText(checkout.root); err != nil {
		return "", err
	}
	values := [][2]string{
		{"{{name}}", name},
		{"{{title}}", title(name)},
		{"{{forgebase_path}}", goModPath(checkout.root)},
		{"{{go_version}}", strings.TrimPrefix(module.goDirective, "go ")},
		{"{{go_directives}}", module.directives()},
		{"{{requires}}", module.requireLines()},
	}
	render := func(text string) string { return renderTemplate(text, values) }

	// A missing --dir (and its missing parents) is created and stays when a later step fails: only the
	// directory of the app itself is removed again.
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", errf("cannot create %s: %s", dir, ioErrorText(err))
	}
	files := make([][2]string, 0, len(newAppFiles))
	for _, f := range newAppFiles {
		text, readErr := templates.ReadFile("templates/app/" + f[1])
		if readErr != nil {
			return "", fmt.Errorf("template %s is missing from the build: %w", f[1], readErr)
		}
		files = append(files, [2]string{f[0], string(text)})
	}
	if err := generate(target, render, checkout.root, files); err != nil {
		return "", err
	}
	return target, nil
}

// generate creates target and writes files (and the go.sum of the checkout) into it. From the moment the
// directory exists it is ours: when anything fails it is removed again, so a failed run leaves nothing
// half written.
func generate(target string, render func(string) string, checkout string, files [][2]string) error {
	if err := os.Mkdir(target, 0o777); err != nil {
		return errf("cannot create %s: %s", target, ioErrorText(err))
	}
	if err := writeNewAppFiles(target, render, checkout, files); err != nil {
		_ = os.RemoveAll(target)
		return err
	}
	return nil
}

func writeNewAppFiles(target string, render func(string) string, checkout string, files [][2]string) error {
	for _, f := range files {
		file := filepath.Join(target, filepath.FromSlash(render(f[0])))
		if err := os.MkdirAll(filepath.Dir(file), 0o777); err != nil {
			return errf("cannot write %s: %s", filepath.Dir(file), ioErrorText(err))
		}
		if err := os.WriteFile(file, []byte(render(f[1])), 0o666); err != nil {
			return errf("cannot write %s: %s", file, ioErrorText(err))
		}
	}
	// The versions Forgebase was tested with: the checkout's go.sum. `go mod tidy` drops the entries the
	// app does not use; see docs/guide/first-app.md.
	sum, err := os.ReadFile(filepath.Join(checkout, "go.sum"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errf("cannot read %s: %s", filepath.Join(checkout, "go.sum"), ioErrorText(err))
	}
	to := filepath.Join(target, "go.sum")
	if err := os.WriteFile(to, sum, 0o666); err != nil {
		return errf("cannot write %s: %s", to, ioErrorText(err))
	}
	return nil
}

// renderTemplate is text with every known placeholder replaced, in one pass: the replaced values are not
// scanned again (a path may contain {{name}}), and an unknown {{ is left as it is.
func renderTemplate(text string, values [][2]string) string {
	var out strings.Builder
	out.Grow(len(text))
	rest := text
	for {
		at := strings.Index(rest, "{{")
		if at < 0 {
			break
		}
		out.WriteString(rest[:at])
		rest = rest[at:]
		replaced := false
		for _, kv := range values {
			if strings.HasPrefix(rest, kv[0]) {
				out.WriteString(kv[1])
				rest = rest[len(kv[0]):]
				replaced = true
				break
			}
		}
		if !replaced {
			out.WriteString("{{")
			rest = rest[2:]
		}
	}
	out.WriteString(rest)
	return out.String()
}

// newAppReport is what the command prints: where the app is and what to do next.
func newAppReport(created string) string {
	quoted := shellQuote(created)
	return fmt.Sprintf("created %s\n\nnext steps:\n  cd %s\n  export FORGEBASE_TEST_DATABASE_URL=postgres://user:password@localhost:5432/postgres\n  go test ./...\n\nREADME.md says how to migrate, serve and extend the app; AGENTS.md has the rules for whoever maintains it.\n",
		created, quoted)
}

// shellQuote is text as one shell word: unchanged when it has only safe characters, else in single quotes.
func shellQuote(text string) string {
	safe := func(c rune) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_./-+:@%,=~", c)
	}
	ok := text != ""
	for _, c := range text {
		if !safe(c) {
			ok = false
		}
	}
	if ok {
		return text
	}
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

type checkoutDir struct{ root string }

// forgebaseCheckout checks that path is a checkout of Forgebase (its go.mod names the module) and gives
// its absolute path: the path as given when it is absolute, else made absolute (a relative path is relative
// to where the command runs, not to the new module).
func forgebaseCheckout(path string) (*checkoutDir, error) {
	manifest := filepath.Join(path, "go.mod")
	text, err := os.ReadFile(manifest)
	isForgebase := false
	if err == nil {
		for _, line := range strings.Split(string(text), "\n") {
			if strings.TrimSpace(line) == "module github.com/tvstore/forgebase-go" {
				isForgebase = true
			}
		}
	}
	if !isForgebase {
		return nil, errf("%s is not a Forgebase checkout: %s is missing or is not the module github.com/tvstore/forgebase-go", path, manifest)
	}
	root := path
	if !filepath.IsAbs(path) {
		if root, err = filepath.Abs(path); err != nil {
			return nil, errf("cannot resolve %s: %s", path, ioErrorText(err))
		}
	}
	return &checkoutDir{root: root}, nil
}

// goModPath is a directory as the path to write after => in go.mod: a replace path that has spaces,
// quotes, backslashes or other odd characters is written as a quoted Go string.
func goModPath(path string) string {
	plain := path != ""
	for _, c := range path {
		if c <= ' ' || c == '"' || c == '\'' || c == '`' || c == '\\' || c == '(' || c == ')' || c == ',' || c == '[' || c == ']' || c > '~' {
			plain = false
		}
	}
	if plain {
		return path
	}
	return fmt.Sprintf("%q", path)
}

// checkPathText rejects a path that go.mod cannot hold: it must be UTF-8 without control characters.
func checkPathText(path string) error {
	if !validUTF8([]byte(path)) {
		return errf("the path %q is not valid UTF-8", path)
	}
	for _, c := range path {
		if c < ' ' || c == 0x7f {
			return errf("the path %q contains a control character", path)
		}
	}
	return nil
}

// title is my_app as My App, for labels and the name of the sample role.
func title(name string) string {
	var words []string
	for _, word := range strings.Split(name, "_") {
		if word == "" {
			continue
		}
		words = append(words, strings.ToUpper(word[:1])+word[1:])
	}
	return strings.Join(words, " ")
}

// goModFile is what new-app needs of the go.mod of the checkout.
type goModFile struct {
	goDirective string // "go 1.26.0"
	toolchain   string // "toolchain go1.27.3", or ""
	requires    [][2]string
}

// parseGoMod reads the go, toolchain and require lines of a go.mod. Comments, replace and other
// directives are not needed.
func parseGoMod(text string) goModFile {
	var m goModFile
	inRequire := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "":
		case inRequire && line == ")":
			inRequire = false
		case inRequire:
			if f := strings.Fields(line); len(f) == 2 {
				m.requires = append(m.requires, [2]string{f[0], f[1]})
			}
		case strings.HasPrefix(line, "require ("):
			inRequire = true
		case strings.HasPrefix(line, "require "):
			if f := strings.Fields(line); len(f) == 3 {
				m.requires = append(m.requires, [2]string{f[1], f[2]})
			}
		case strings.HasPrefix(line, "go "):
			m.goDirective = line
		case strings.HasPrefix(line, "toolchain "):
			m.toolchain = line
		}
	}
	sort.Slice(m.requires, func(i, j int) bool { return m.requires[i][0] < m.requires[j][0] })
	return m
}

// directives are the go and toolchain lines.
func (m goModFile) directives() string {
	if m.toolchain == "" {
		return m.goDirective
	}
	return m.goDirective + "\n\n" + m.toolchain
}

// requireLines are the requirements as the lines of one require block, all marked indirect (the new
// module imports only the Forgebase packages; they are what the build needs).
func (m goModFile) requireLines() string {
	lines := make([]string, len(m.requires))
	for i, r := range m.requires {
		lines[i] = fmt.Sprintf("\t%s %s // indirect", r[0], r[1])
	}
	return strings.Join(lines, "\n")
}
