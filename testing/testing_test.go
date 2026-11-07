package testing_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// Tests of the helpers themselves (crates/forgebase/src/testing.rs has none of its own: the Rust helpers are
// exercised by every integration test; here the parts an app relies on are pinned directly). The guard
// AssertDocTypesListed is tested in guard_test.go at the root of the module, with the Rust tests of it.

const noteDoc = `
name = "note"
label = "Note"
[[fields]]
name = "title"
type = "data"
required = true
[[permissions]]
role = "Writer"
read = true
write = true
create = true
delete = true
`

func noteBinary() *forgebase.Binary {
	return forgebase.New().App(forgebase.NewApp("notes").DocTypes(fstest.MapFS{"doctypes/note.toml": {Data: []byte(noteDoc)}}))
}

func newClient(t *testing.T) (*fbtest.DB, *fbtest.Client) {
	t.Helper()
	db := fbtest.NewDB(t)
	binary := noteBinary()
	if _, err := db.Migrate(binary, false); err != nil {
		t.Fatal(err)
	}
	db.CreateUser("ann", "Writer")
	return db, db.Client(binary)
}

// fatalTB records a failure instead of stopping the test.
type fatalTB struct {
	testing.TB
	message string
}

type stopped struct{}

func (f *fatalTB) Helper() {}
func (f *fatalTB) Fatalf(format string, args ...any) {
	f.message = fmt.Sprintf(format, args...)
	panic(stopped{})
}

func failure(t *testing.T, run func(tb testing.TB)) (message string) {
	t.Helper()
	f := &fatalTB{TB: t}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stopped); !ok {
				panic(r)
			}
			message = f.message
		}
	}()
	run(f)
	return ""
}

func TestNewDBFailsInsteadOfSkippingWhenTheServerIsNotConfigured(t *testing.T) {
	t.Setenv("FORGEBASE_TEST_DATABASE_URL", "")
	msg := failure(t, func(tb testing.TB) { fbtest.NewDB(tb) })
	if !strings.Contains(msg, "FORGEBASE_TEST_DATABASE_URL is not set") || !strings.Contains(msg, "Tests never skip") {
		t.Errorf("%q", msg)
	}
}

func TestADatabaseIsDroppedWhenTheTestEnds(t *testing.T) {
	var url string
	t.Run("inner", func(t *testing.T) {
		url = fbtest.NewDB(t).URL()
		if !strings.Contains(url, "fb_test_app_") {
			t.Errorf("%s", url)
		}
	})
	// The inner test ended: its cleanup dropped the database, so a new database of the same server does not
	// list it.
	other := fbtest.NewDB(t)
	name := url[strings.LastIndex(url, "/")+1:]
	if rows := other.Rows("SELECT count(*)::text FROM pg_database WHERE datname = '" + name + "'"); rows[0][0] != "0" {
		t.Errorf("the database %s was not dropped", name)
	}
}

func TestExecAndRowsReturnTextWithNullAsNULL(t *testing.T) {
	db := fbtest.NewDB(t)
	db.Exec("CREATE TABLE zz (a text, b integer); INSERT INTO zz VALUES ('x', 1), (NULL, 2)")
	got := db.Rows("SELECT a, b::text FROM zz ORDER BY b")
	if want := [][]string{{"x", "1"}, {"NULL", "2"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("%v", got)
	}
	if got := db.Rows("SELECT a FROM zz WHERE false"); got == nil || len(got) != 0 {
		t.Errorf("no rows is an empty list, got %#v", got)
	}
	// A statement that fails fails the test.
	msg := failure(t, func(tb testing.TB) { fbtest.NewDB(tb).Exec("SELECT nope") })
	if !strings.Contains(msg, "SELECT nope") {
		t.Errorf("%q", msg)
	}
}

func TestLoginTokenAndTheCSRFHeader(t *testing.T) {
	db, client := newClient(t)
	// Not logged in: 401 and the error helpers.
	anon := client.Get("/api/v1/resource/note")
	if anon.Status != 401 || anon.ErrorCode() != "unauthenticated" || anon.ErrorMessage() == "" {
		t.Errorf("%d %s", anon.Status, anon.Text)
	}
	if anon.Header("Cache-Control") != "no-store" {
		t.Errorf("headers: %q", anon.Header("Cache-Control"))
	}

	ann := client.Login("ann")
	created := ann.Post("/api/v1/resource/note", map[string]any{"title": "Hi"})
	if created.Status != 201 {
		t.Fatalf("%d %s", created.Status, created.Text)
	}
	name := created.DataObject()["name"].(string)
	if list := ann.Get("/api/v1/resource/note"); list.Status != 200 || len(list.DataList()) != 1 {
		t.Errorf("%d %s", list.Status, list.Text)
	}
	if got := ann.Get("/api/v1/resource/note/" + name); got.DataObject()["title"] != "Hi" || got.DataList() != nil {
		t.Errorf("%s", got.Text)
	}
	version := fetch(t, ann, name)["version"]
	if patch := ann.Patch("/api/v1/resource/note/"+name, map[string]any{"version": version, "data": map[string]any{"title": "Yo"}}); patch.Status != 200 {
		t.Errorf("%d %s", patch.Status, patch.Text)
	}

	// A cookie call without the CSRF header is refused.
	refused := ann.WithoutCSRF().Post("/api/v1/resource/note", map[string]any{"title": "x"})
	if refused.Status != 403 || refused.ErrorCode() != "csrf_failed" {
		t.Errorf("%d %s", refused.Status, refused.Text)
	}
	// A client without a session is the same client.
	if client.WithoutCSRF().Get("/api/v1/resource/note").Status != 401 {
		t.Error("a client without a session stays logged out")
	}

	// An API token needs no cookie and no CSRF header.
	token := client.WithToken(db.CreateToken("ann"))
	if reply := token.Post("/api/v1/resource/note", map[string]any{"title": "by token"}); reply.Status != 201 {
		t.Errorf("%d %s", reply.Status, reply.Text)
	}
	if reply := client.WithToken("nope:nope").Get("/api/v1/resource/note"); reply.Status != 401 {
		t.Errorf("%d %s", reply.Status, reply.Text)
	}

	// Delete and a raw body.
	version = fetch(t, ann, name)["version"]
	if del := ann.Delete(fmt.Sprintf("/api/v1/resource/note/%s?version=%v", name, version)); del.Status != 204 || del.Body != nil || del.Data() != nil {
		t.Errorf("%d %s", del.Status, del.Text)
	}
	raw := ann.PostRaw("/api/v1/resource/note", "text/plain", []byte("title=x"))
	if raw.Status != 415 && raw.Status != 400 {
		t.Errorf("a body that is not JSON: %d %s", raw.Status, raw.Text)
	}
	if raw := ann.PostRaw("/api/v1/resource/note", "", []byte(`{"title":"no content type"}`)); raw.Status == 201 {
		t.Errorf("a body without a content type must be refused: %d", raw.Status)
	}
}

func fetch(t *testing.T, c *fbtest.Client, name string) map[string]any {
	t.Helper()
	return c.Get("/api/v1/resource/note/" + name).DataObject()
}

func TestAFailedLoginFailsTheTest(t *testing.T) {
	msg := failure(t, func(tb testing.TB) {
		db := fbtest.NewDB(tb)
		if _, err := db.Migrate(noteBinary(), false); err != nil {
			t.Fatal(err)
		}
		db.Client(noteBinary()).Login("nobody")
	})
	if !strings.Contains(msg, "login of nobody failed: 401") {
		t.Errorf("%q", msg)
	}
}

func TestMigrateReportsTheStartupErrorsAndClientFailsTheTest(t *testing.T) {
	broken := forgebase.New().App(forgebase.NewApp("sales").DependsOn("crm"))
	var migrateErr error
	msg := failure(t, func(tb testing.TB) {
		db := fbtest.NewDB(tb)
		_, migrateErr = db.Migrate(broken, false)
		db.Client(broken)
	})
	if _, ok := migrateErr.(*forgebase.StartupError); !ok {
		t.Errorf("%T %v", migrateErr, migrateErr)
	}
	if !strings.Contains(msg, "the apps do not fit together: invalid app setup:") {
		t.Errorf("%q", msg)
	}
}
