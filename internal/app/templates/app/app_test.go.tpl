// Tests of the {{name}} app. The last one needs PostgreSQL (see README.md) and fails, it does not skip,
// when FORGEBASE_TEST_DATABASE_URL is not set.
package {{name}}_test

import (
	"testing"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"

	"{{name}}"
)

const doctype = "{{name}}_note"

func TestEveryDocTypeFileIsEmbedded(t *testing.T) {
	fbtest.AssertDocTypesListed(t, {{name}}.App(), "doctypes")
}

func TestTheAppFitsTogether(t *testing.T) {
	if err := forgebase.New().App({{name}}.App()).Check(); err != nil {
		t.Fatal(err)
	}
}

func TestTheTitleIsTrimmedAndMayNotBeBlank(t *testing.T) {
	rules := {{name}}.NoteRules{}
	ctx := forgebase.NewHookContext(doctype, "ann", forgebase.OpInsert)
	doc := &forgebase.Document{Values: map[string]forgebase.Value{"title": forgebase.Text("  Hello  ")}}
	if err := rules.Validate(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.Values["title"]; !got.Equal(forgebase.Text("Hello")) {
		t.Errorf("title %+v", got)
	}

	doc.Values["title"] = forgebase.Text("   ")
	if err := rules.Validate(ctx, doc); err == nil {
		t.Error("a blank title must be refused")
	}
}

func TestAUserCanCreateAndReadANote(t *testing.T) {
	binary := forgebase.New().App({{name}}.App())
	testDB := fbtest.NewDB(t)
	if _, err := testDB.Migrate(binary, false); err != nil {
		t.Fatal(err)
	}
	testDB.CreateUser("ann", "{{title}} User")
	ann := testDB.Client(binary).Login("ann")

	path := "/api/v1/resource/" + doctype
	created := ann.Post(path, map[string]any{"title": "  Hello  "})
	if created.Status != 201 {
		t.Fatalf("%d %s", created.Status, created.Text)
	}
	name, _ := created.DataObject()["name"].(string)

	read := ann.Get(path + "/" + name)
	if read.Status != 200 {
		t.Fatalf("%d %s", read.Status, read.Text)
	}
	if title := read.DataObject()["title"]; title != "Hello" { // trimmed by the hook
		t.Errorf("title %v", title)
	}
}
