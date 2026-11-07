package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/db"
)

// Ported from crates/forgebase-http/tests/link_titles.rs: link titles over HTTP (UX.4):
// ?link_titles=1 adds a sibling link_titles key, follows the caller's read rules, and leaves the output
// unchanged without the parameter.

// custDoc is a titled target: Reader reads all, Mine only its own documents.
const custDoc = `
name = "cust"
label = "Cust"
naming = { field = "code" }
title_field = "full_name"
icon = "users"
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
create = true
[[permissions]]
role = "Mine"
read = true
if_owner = true
`

const dealDoc = `
name = "deal"
label = "Deal"
[[fields]]
name = "cust"
type = "link"
target = "cust"
[[fields]]
name = "lines"
type = "table"
target = "deal_line"
[[permissions]]
role = "Reader"
read = true
create = true
[[permissions]]
role = "Mine"
read = true
if_owner = true
`

const dealLineDoc = `
name = "deal_line"
label = "Deal Line"
is_child = true
[[fields]]
name = "cust"
type = "link"
target = "cust"
`

func linkTitlesSetup(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnvWith(t, DefaultConfig(), custDoc, dealDoc, dealLineDoc)
	e.user("admin", "Administrator")
	e.user("reader", "Reader")
	e.user("mine", "Mine")
	e.user("nobody")
	admin := e.login("admin")
	e.adminInsert(admin, "cust", J{"code": "C-1", "full_name": "Ada"})
	e.adminInsert(admin, "cust", J{"code": "C-2"})
	e.adminInsert(admin, "deal", J{"cust": "C-1", "lines": []any{J{"cust": "C-2"}, J{"cust": "C-1"}}})
	return e, e.apiToken("reader")
}

// Rust: link_titles.rs::the_output_is_unchanged_without_the_parameter
func TestTheOutputIsUnchangedWithoutTheParameter(t *testing.T) {
	e, reader := linkTitlesSetup(t)
	list := e.send(get("/api/v1/resource/deal").token(reader))
	wantStatus(t, list, http.StatusOK)
	if len(list.obj().(map[string]any)) != 1 {
		t.Error(list.text)
	}
	name := str(list.dataList()[0], "name")
	one := e.send(get("/api/v1/resource/deal/" + name).token(reader))
	if len(one.obj().(map[string]any)) != 1 {
		t.Error(one.text)
	}
}

// Rust: link_titles.rs::list_and_read_carry_link_titles_when_asked
func TestListAndReadCarryLinkTitlesWhenAsked(t *testing.T) {
	e, reader := linkTitlesSetup(t)
	list := e.send(get("/api/v1/resource/deal?link_titles=1").token(reader))
	wantStatus(t, list, http.StatusOK)
	// The title of C-1 only: C-2 has none. The data is as without the parameter.
	eqJSON(t, list.obj().(map[string]any)["link_titles"], `{"cust":{"C-1":"Ada"}}`)
	eqJSON(t, at(list.dataList()[0], "cust"), `"C-1"`)
	name := str(list.dataList()[0], "name")
	one := e.send(get("/api/v1/resource/deal/" + name + "?link_titles=1").token(reader))
	wantStatus(t, one, http.StatusOK)
	eqJSON(t, one.obj().(map[string]any)["link_titles"], `{"cust":{"C-1":"Ada"}}`)
	if str(one.data(), "name") != name {
		t.Error(one.text)
	}
}

// Rust: link_titles.rs::an_if_owner_caller_gets_no_title_for_someone_elses_document
func TestAnIfOwnerCallerGetsNoTitleForSomeoneElsesDocument(t *testing.T) {
	e, _ := linkTitlesSetup(t)
	mine := e.apiToken("mine")
	// mine reads only its own documents: the deal is the admin's, so there is nothing to title; and the
	// target is the admin's too.
	list := e.send(get("/api/v1/resource/deal?link_titles=1").token(mine))
	wantStatus(t, list, http.StatusOK)
	eqJSON(t, list.data(), `[]`)
	eqJSON(t, list.obj().(map[string]any)["link_titles"], `{}`)
	// A caller with no right on the DocType is refused as before.
	wantStatus(t, e.send(get("/api/v1/resource/deal?link_titles=1").token(e.apiToken("nobody"))), http.StatusForbidden)
}

// Rust: link_titles.rs::a_bad_value_is_invalid_query
func TestABadValueIsInvalidQuery(t *testing.T) {
	e, reader := linkTitlesSetup(t)
	for _, value := range []string{"0", "true", "", "2"} {
		rep := e.send(get("/api/v1/resource/deal?link_titles=" + value).token(reader))
		wantCode(t, rep, http.StatusBadRequest, "invalid_query", value)
	}
	list := e.send(get("/api/v1/resource/deal").token(reader))
	name := str(list.dataList()[0], "name")
	wantCode(t, e.send(get("/api/v1/resource/deal/"+name+"?link_titles=yes").token(reader)), http.StatusBadRequest, "invalid_query")
	// A repeated parameter is refused like every other.
	wantCode(t, e.send(get("/api/v1/resource/deal?link_titles=1&link_titles=1").token(reader)), http.StatusBadRequest, "invalid_query")
}

// Rust: link_titles.rs::the_meta_carries_the_icon
func TestTheMetaCarriesTheIcon(t *testing.T) {
	e, reader := linkTitlesSetup(t)
	eqJSON(t, e.send(get("/api/v1/meta/cust").token(reader)).dataObj()["icon"], `"users"`)
	eqJSON(t, e.send(get("/api/v1/meta/deal").token(reader)).dataObj()["icon"], `null`)
	all := e.send(get("/api/v1/meta").token(reader)).dataList()
	eqJSON(t, at(entry(t, all, "cust"), "icon"), `"users"`)
	eqJSON(t, at(entry(t, all, "deal"), "icon"), `null`)
}

// Rust: link_titles.rs::link_titles_use_the_connection_the_handler_holds
//
// With a pool of one connection, link_titles=1 must not wait for a second connection while the handler
// holds the first (the pool would wait for ever). In Go a request takes one connection at a time from
// the pool, so a pool of one is enough.
func TestLinkTitlesUseTheConnectionTheHandlerHolds(t *testing.T) {
	e, reader := linkTitlesSetup(t)
	name := str(e.send(get("/api/v1/resource/deal").token(reader)).dataList()[0], "name")
	pool, err := db.ConnectSized(context.Background(), e.db.URL(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	state := NewState(pool, testRegistry(t, custDoc, dealDoc, dealLineDoc), DefaultConfig())
	h := ImplicitSites(state)
	for _, target := range []string{"/api/v1/resource/deal?link_titles=1", fmt.Sprintf("/api/v1/resource/deal/%s?link_titles=1", name)} {
		done := make(chan reply, 1)
		go func() { done <- sendTo(t, h, get(target).token(reader)) }()
		select {
		case rep := <-done:
			wantStatus(t, rep, http.StatusOK, target)
			eqJSON(t, rep.obj().(map[string]any)["link_titles"], `{"cust":{"C-1":"Ada"}}`)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s hung: a second connection was requested", target)
		}
	}
}
