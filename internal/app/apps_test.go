package app

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The App builder (apps.go). Not in Rust: the Rust builder takes explicit lists (the macros doctypes! and
// migrations!), the Go one reads an fs.FS; these tests pin how.

type named struct {
	core.BaseHooks
	name string
}

func paths(a *App) []string {
	var out []string
	for _, f := range a.doctypes {
		out = append(out, f.Path)
	}
	return out
}

func TestDocTypesReadsEveryTomlFileInWalkOrderWithItsPath(t *testing.T) {
	fsys := files(
		"doctypes/b.toml", "b", "doctypes/a.toml", "a", "doctypes/sub/c.toml", "c", "z.toml", "z",
		"doctypes/readme.md", "x", "doctypes/UPPER.TOML", "x", "doctypes/.hidden.toml", "x", ".dir/d.toml", "x")
	a := NewApp("x").DocTypes(fsys)
	want := []string{"doctypes/a.toml", "doctypes/b.toml", "doctypes/sub/c.toml", "z.toml"}
	if got := paths(a); !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
	if a.doctypes[0].TOML != "a" {
		t.Errorf("%+v", a.doctypes[0])
	}
	// Calling it again adds more files.
	a.DocTypes(files("more/m.toml", "m"))
	if got := paths(a); len(got) != 5 || got[4] != "more/m.toml" {
		t.Errorf("%v", got)
	}
	if !reflect.DeepEqual(DocTypePaths(a), paths(a)) {
		t.Error("DocTypePaths")
	}
}

func TestADocTypeFileThatIsNotUTF8IsAFileProblem(t *testing.T) {
	a := NewApp("x").DocTypes(fstest.MapFS{"doctypes/bad.toml": {Data: []byte{0xff, 0xfe}}})
	if len(a.doctypes) != 0 || !reflect.DeepEqual(a.fileProblems, []string{"app `x`: doctypes/bad.toml: cannot read: stream did not contain valid UTF-8"}) {
		t.Errorf("%+v %v", a.doctypes, a.fileProblems)
	}
}

// failing is a file system whose files cannot be read.
type failing struct{ fstest.MapFS }

func (f failing) ReadFile(name string) ([]byte, error) { return nil, errors.New("boom") }
func (f failing) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, ".toml") || strings.HasSuffix(name, ".sql") {
		return nil, &fs.PathError{Op: "open", Path: name, Err: errors.New("boom")}
	}
	return f.MapFS.Open(name)
}

func TestAnUnreadableFileIsAProblemOfTheApp(t *testing.T) {
	fsys := failing{files("doctypes/a.toml", "a")}
	a := NewApp("x").DocTypes(fsys).Migrations(failing{files("migrations/0001_a.sql", "a")})
	want := []string{
		"app `x`: DocTypes: doctypes/a.toml: cannot read: boom",
		"app `x`: Migrations: migrations/0001_a.sql: cannot read: boom",
	}
	if !reflect.DeepEqual(a.appProblems, want) {
		t.Errorf("%v", a.appProblems)
	}
}

func TestMigrationsAreOrderedByVersionNotByName(t *testing.T) {
	// 0010 sorts before 0002 as text only if the digits are not zero padded: here the versions decide.
	a := NewApp("x").Migrations(files(
		"migrations/10_ten.sql", "10", "migrations/2_two.sql", "2", "migrations/0003_three.sql", "3", "migrations/README.md", "x"))
	var got []int32
	for _, m := range a.migrations {
		got = append(got, m.Version)
	}
	if !reflect.DeepEqual(got, []int32{2, 3, 10}) {
		t.Errorf("%v", got)
	}
	if m := a.migrations[0]; m.Name != "two" || m.Path != "migrations/2_two.sql" || m.SQL != "2" {
		t.Errorf("%+v", m)
	}
	if len(a.appProblems) != 0 {
		t.Errorf("%v", a.appProblems)
	}
}

func TestANameWithUnderscoresKeepsThemInTheMigrationName(t *testing.T) {
	a := NewApp("x").Migrations(files("0001_default_customer_groups.sql", "x"))
	if a.migrations[0].Name != "default_customer_groups" {
		t.Errorf("%+v", a.migrations[0])
	}
}
