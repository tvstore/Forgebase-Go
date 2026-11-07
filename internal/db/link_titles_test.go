package db_test

import (
	"fmt"
	"testing"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Link titles (UX.4) against a real PostgreSQL: titles of readable targets, the caller's read rules
// (deny and if_owner), targets without a title_field, child-row links and a page of many documents.
// Ported from crates/forgebase-db/tests/link_titles.rs.

// ltClient is a titled target everybody with a role reads: Reader all, OwnReader only own documents.
const ltClient = `
name = "client"
label = "Client"
naming = { field = "code" }
title_field = "full_name"
[[fields]]
name = "code"
type = "data"
required = true
unique = true
[[fields]]
name = "full_name"
type = "data"
[[permissions]]
role = "Reader"
read = true
[[permissions]]
role = "OwnReader"
read = true
if_owner = true
`

// ltPlain is a target without a title_field.
const ltPlain = `
name = "plain"
label = "Plain"
naming = { field = "code" }
[[fields]]
name = "code"
type = "data"
required = true
unique = true
[[permissions]]
role = "Reader"
read = true
`

// ltHidden is a titled target that only the administrator may read (no rules).
const ltHidden = `
name = "hidden"
label = "Hidden"
naming = { field = "code" }
title_field = "full_name"
[[fields]]
name = "code"
type = "data"
required = true
unique = true
[[fields]]
name = "full_name"
type = "data"
`

// ltBadge is a titled target whose title is a Link: shown as the name of what it points to.
const ltBadge = `
name = "badge"
label = "Badge"
naming = { field = "code" }
title_field = "holder"
[[fields]]
name = "code"
type = "data"
required = true
unique = true
[[fields]]
name = "holder"
type = "link"
target = "plain"
[[permissions]]
role = "Reader"
read = true
`

const ltInvoice = `
name = "invoice"
label = "Invoice"
[[fields]]
name = "client"
type = "link"
target = "client"
[[fields]]
name = "plain"
type = "link"
target = "plain"
[[fields]]
name = "hidden"
type = "link"
target = "hidden"
[[fields]]
name = "badge"
type = "link"
target = "badge"
[[fields]]
name = "lines"
type = "table"
target = "invoice_line"
[[permissions]]
role = "Reader"
read = true
create = true
[[permissions]]
role = "OwnReader"
read = true
create = true
`

const ltInvoiceLine = `
name = "invoice_line"
label = "Invoice Line"
is_child = true
[[fields]]
name = "client"
type = "link"
target = "client"
`

func ltSetup(t *testing.T) (*dbtest.DB, *core.Registry) {
	t.Helper()
	return setup(t, ltClient, ltPlain, ltHidden, ltBadge, ltInvoice, ltInvoiceLine)
}

func ltAdmin() core.Caller { return user("root", core.Administrator) }

func ltTitles(t *testing.T, d *dbtest.DB, reg *core.Registry, caller core.Caller, documents ...*db.StoredDocument) db.LinkTitleMap {
	t.Helper()
	got, err := db.LinkTitles(bg, d.Pool, reg, caller, "invoice", documents)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Rust: link_titles.rs::readable_targets_get_titles_and_others_get_no_entry
func TestReadableTargetsGetTitlesAndOthersGetNoEntry(t *testing.T) {
	d, reg := ltSetup(t)
	s := sys("sys")
	mustInsert(t, d, reg, "client", doc("code", text("C-1"), "full_name", text("Ada Lovelace")), s)
	mustInsert(t, d, reg, "plain", doc("code", text("P-1")), s)
	mustInsert(t, d, reg, "hidden", doc("code", text("H-1"), "full_name", text("Secret Name")), s)
	mustInsert(t, d, reg, "badge", doc("code", text("B-1"), "holder", text("P-1")), s)
	invoice := mustInsert(t, d, reg, "invoice", doc("client", text("C-1"), "plain", text("P-1"), "hidden", text("H-1"), "badge", text("B-1")), s)

	got := ltTitles(t, d, reg, user("rita", "Reader"), invoice)
	// The client has a title; the Link title of the badge is the name of what it points to. No entry for
	// plain (no title_field) or hidden (no read right).
	want(t, got, db.LinkTitleMap{
		"client": {"C-1": "Ada Lovelace"},
		"badge":  {"B-1": "P-1"},
	}, "titles")

	// The administrator may read the hidden target.
	got = ltTitles(t, d, reg, ltAdmin(), invoice)
	if got["hidden"]["H-1"] != "Secret Name" {
		t.Error(got)
	}
	if _, ok := got["plain"]; ok {
		t.Error("plain has no title_field")
	}
}

// Rust: link_titles.rs::an_if_owner_target_gives_titles_only_for_own_documents
func TestAnIfOwnerTargetGivesTitlesOnlyForOwnDocuments(t *testing.T) {
	d, reg := ltSetup(t)
	rita, sam := user("rita", "OwnReader"), user("sam", "OwnReader")
	mustInsert(t, d, reg, "client", doc("code", text("C-RITA"), "full_name", text("Rita's")), sys("rita"))
	mustInsert(t, d, reg, "client", doc("code", text("C-SAM"), "full_name", text("Sam's")), sys("sam"))
	var invoices []*db.StoredDocument
	for _, client := range []string{"C-RITA", "C-SAM"} {
		invoices = append(invoices, mustInsert(t, d, reg, "invoice", doc("client", text(client)), sys("sys")))
	}
	// Sam's client has no title for rita.
	want(t, ltTitles(t, d, reg, rita, invoices...)["client"], map[string]string{"C-RITA": "Rita's"}, "rita")
	want(t, ltTitles(t, d, reg, sam, invoices...)["client"], map[string]string{"C-SAM": "Sam's"}, "sam")
}

// Rust: link_titles.rs::a_caller_without_any_role_gets_nothing
func TestACallerWithoutAnyRoleGetsNothing(t *testing.T) {
	d, reg := ltSetup(t)
	mustInsert(t, d, reg, "client", doc("code", text("C-1"), "full_name", text("Ada")), sys("sys"))
	invoice := mustInsert(t, d, reg, "invoice", doc("client", text("C-1")), sys("sys"))
	if got := ltTitles(t, d, reg, user("nobody", "Other"), invoice); len(got) != 0 {
		t.Error(got)
	}
}

// Rust: link_titles.rs::child_row_links_are_covered_and_a_null_title_is_omitted
func TestChildRowLinksAreCoveredAndANullTitleIsOmitted(t *testing.T) {
	d, reg := ltSetup(t)
	s := sys("sys")
	mustInsert(t, d, reg, "client", doc("code", text("C-1"), "full_name", text("Ada")), s)
	// No full_name: a null title is omitted.
	mustInsert(t, d, reg, "client", doc("code", text("C-2")), s)
	line := func(client string) core.Row { return row("client", text(client)) }
	invoice := doc()
	invoice.Tables["lines"] = []core.Row{line("C-1"), line("C-2"), line("C-1")}
	made := mustInsert(t, d, reg, "invoice", invoice, s)
	stored := mustGet(t, d, reg, "invoice", made.Name, s)
	want(t, ltTitles(t, d, reg, user("rita", "Reader"), stored)["client"], map[string]string{"C-1": "Ada"}, "titles")
}

// Rust: link_titles.rs::a_page_of_fifty_documents_is_titled_correctly
func TestAPageOfFiftyDocumentsIsTitledCorrectly(t *testing.T) {
	d, reg := ltSetup(t)
	s := sys("sys")
	for i := range 10 {
		mustInsert(t, d, reg, "client", doc("code", text(fmt.Sprintf("C-%d", i)), "full_name", text(fmt.Sprintf("Client %d", i))), s)
	}
	for i := range 50 {
		mustInsert(t, d, reg, "invoice", doc("client", text(fmt.Sprintf("C-%d", i%10))), s)
	}
	reader := user("rita", "Reader")
	page := listDocs(t, d, reg, "invoice", reader, db.ListQuery{Limit: u32(50)})
	if len(page) != 50 {
		t.Fatal(len(page))
	}
	got, err := db.LinkTitles(bg, d.Pool, reg, reader, "invoice", page)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["client"]) != 10 {
		t.Fatal(got)
	}
	for i := range 10 {
		if got["client"][fmt.Sprintf("C-%d", i)] != fmt.Sprintf("Client %d", i) {
			t.Error(got["client"])
		}
	}
	// The titles agree with the documents on the page.
	for _, document := range page {
		if _, ok := got["client"][document.Values["client"].Text]; !ok {
			t.Error(document.Values["client"])
		}
	}
}

// Rust: link_titles.rs::an_unknown_or_child_doctype_is_an_error
func TestAnUnknownOrChildDoctypeIsAnError(t *testing.T) {
	d, reg := ltSetup(t)
	_, err := db.LinkTitles(bg, d.Pool, reg, ltAdmin(), "nope", nil)
	wantKind(t, err, db.ErrUnknownDocType)
	_, err = db.LinkTitles(bg, d.Pool, reg, ltAdmin(), "invoice_line", nil)
	wantKind(t, err, db.ErrChildDocType)
}
