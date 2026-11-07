package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/httpapi"
)

// App method endpoints (M9.1, ADR 0019; the Rust module is methods.rs): Method, MethodHandler,
// MethodContext and MethodError, and the dispatcher that runs a method for internal/httpapi. The root
// package re-exports these as type aliases, so this file is where the godoc of the public API lives.
//
// An app registers a method with [App.Method]; a client calls it with POST /api/v1/method/<app>.<method>.
// A method runs as the caller, in one database transaction.

// MethodHandler is what an app method does. Register it with [NewMethod] and [App.Method]. It returns the
// value that is sent as {"data": value} (anything encoding/json can marshal; nil is null), or an error.
//
//	func OrderCount(ctx context.Context, m *forgebase.MethodContext) (any, error) {
//		var in struct{ Customer string `json:"customer"` }
//		if err := m.DecodeBody(&in); err != nil || in.Customer == "" {
//			return nil, forgebase.MethodBadRequest("Say which customer.")
//		}
//		q := forgebase.NewListQuery().Filter("customer", forgebase.FilterEq, forgebase.Text(in.Customer))
//		orders, err := m.Documents().List(ctx, "sales_order", q)
//		if err != nil {
//			return nil, err
//		}
//		return map[string]int{"orders": len(orders)}, nil
//	}
//
//	forgebase.NewApp("sales").Method("order_count", forgebase.NewMethod(OrderCount).Roles("Sales User"))
//
// # Rules (ADR 0019)
//
//   - As the caller. [MethodContext.Documents] acts as the authenticated user, so the permission rules of
//     the DocTypes (if_owner, read rules, read-only fields, workflow roles) are the only decision. A method
//     cannot act as the system context; work that must is a job ([MethodContext.Enqueue]).
//   - One transaction. Everything the handler does through the context commits when it returns a nil
//     error, and is rolled back when it returns an error or panics (the server survives, the client gets
//     a generic 500). A document write that is refused leaves the transaction usable, so the handler may
//     handle the error and go on; reads run in a savepoint too.
//   - Errors. [MethodBadRequest] and [MethodUnprocessable] carry a message written for users. A
//     [DocError] (return the error of a [Documents] call as it is) keeps the status and code of the
//     document API. Anything else is a generic 500 and the error goes to the server's log.
//   - The ctx is not cancelled when the client goes away: the method finishes and commits or rolls back.
//     Pass it to every call anyway.
//   - No time limit is applied to the handler in this version; keep it short and put long work in a job.
type MethodHandler func(ctx context.Context, m *MethodContext) (any, error)

// Method is a method and who may call it, for [App.Method]. Exactly one of [Method.Roles] and
// [Method.AnyUser] is required; there are no guest methods. A method with neither, with an empty role list,
// or with both is a startup error, like the other mistakes of an app. Build one with [NewMethod].
type Method struct {
	handler MethodHandler
	// roles is nil until Roles is called; an empty non-nil slice is Roles() with no roles.
	roles   []string
	hasRole bool
	anyUser bool
}

// NewMethod is a method run by handler. Say who may call it with [Method.Roles] or [Method.AnyUser].
func NewMethod(handler MethodHandler) *Method { return &Method{handler: handler} }

// Roles adds the roles that may call the method. The Administrator role always may. Calling it again adds
// more roles; none at all is a startup error.
func (m *Method) Roles(roles ...string) *Method {
	m.hasRole = true
	m.roles = append(m.roles, roles...)
	return m
}

// AnyUser lets any authenticated user call the method, whatever their roles. Use it deliberately: the
// documents the method touches are still checked for the caller.
func (m *Method) AnyUser() *Method {
	m.anyUser = true
	return m
}

// definition is the definition for app and name and everything wrong with it, one message per problem:
// how access was given, then MethodDef.Problems. A definition with problems is not registered.
func (m *Method) definition(app, name string) (core.MethodDef, []string) {
	var problems []string
	var access core.MethodAccess
	switch {
	case m.hasRole && m.anyUser:
		problems = append(problems, "both .Roles(..) and .AnyUser() are given; use exactly one")
		access = core.MethodRoles(m.roles...)
	case !m.hasRole && !m.anyUser:
		problems = append(problems,
			"no access given: name the roles that may call it with .Roles(..), or use .AnyUser(); there are no guest methods")
		access = core.MethodAnyUser()
	case m.hasRole:
		access = core.MethodRoles(m.roles...)
	default:
		access = core.MethodAnyUser()
	}
	def := core.MethodDef{App: app, Name: name, Access: access}
	return def, append(problems, def.Problems()...)
}

// MethodCaller is the user who called a method: the name and the roles, as the database has them now.
// Read-only.
type MethodCaller struct {
	username string
	roles    []string
}

// Username is the username (recorded as owner and modified_by of what the method writes).
func (c MethodCaller) Username() string { return c.username }

// Roles are the user's roles. Do not change the slice.
func (c MethodCaller) Roles() []string { return c.roles }

// HasRole reports whether the user has role.
func (c MethodCaller) HasRole(role string) bool {
	for _, r := range c.roles {
		if r == role {
			return true
		}
	}
	return false
}

// MethodContext is what a running method knows and may do. The framework builds it; a handler gets it as a
// pointer and must not keep it after it returns.
//
// It owns the transaction the method runs in: [MethodContext.Documents] and [MethodContext.Enqueue] work
// inside it, so their effects commit together, or not at all.
type MethodContext struct {
	tx       pgx.Tx
	registry *core.Registry
	caller   core.Caller
	name     string
	body     json.RawMessage
}

// Name is the full name of the method, <app>.<method>.
func (c *MethodContext) Name() string { return c.name }

// Caller is the user who called the method.
func (c *MethodContext) Caller() MethodCaller {
	roles := c.caller.Roles
	if roles == nil {
		roles = []string{} // a user without roles has an empty list, which is JSON [] and not null
	}
	return MethodCaller{username: c.caller.Name(), roles: roles}
}

// Body is the request body: a JSON object ({} when the request had none).
func (c *MethodContext) Body() json.RawMessage { return c.body }

// DecodeBody decodes the body into v, as json.Unmarshal does.
func (c *MethodContext) DecodeBody(v any) error { return json.Unmarshal(c.body, v) }

// Documents reads and writes documents as the caller: the permission rules of the DocTypes decide every
// call. See [Documents].
func (c *MethodContext) Documents() *Documents {
	return &Documents{tx: c.tx, registry: c.registry, caller: c.caller}
}

// Enqueue enqueues a background job (<app>.<job>) in this method's transaction: it exists if and only if the
// method succeeds. The job runs later, in the worker, as the system context, like a job a hook enqueued, and
// is recorded as enqueued by the caller. payload is anything encoding/json can marshal. An unknown name or a
// payload over 64 KiB of JSON is an error of the app (a *[DocError]; a generic 500 for the client). Never put
// secrets in a payload: it is stored in clear text.
func (c *MethodContext) Enqueue(ctx context.Context, name string, payload any) error {
	// A savepoint, like JobContext.Enqueue: a refused enqueue leaves the transaction usable.
	sp, err := c.tx.Begin(ctx)
	if err != nil {
		return docError(err)
	}
	if _, err := db.Enqueue(ctx, sp, c.registry, name, payload, c.caller.Name()); err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		return docError(err)
	}
	return docError(sp.Commit(ctx))
}

// MethodError is why a method failed on purpose: a client error with a message written for users, sent as
// {"error": {"code": "method_error", "message": ...}}. Make one with [MethodBadRequest] or
// [MethodUnprocessable]. A [DocError] needs no wrapping: return it and it keeps the status and code the
// document API gives the same error. Every error rolls the transaction back.
type MethodError struct {
	// Status is 400 or 422.
	Status int
	// Message is sent to the client as it is: write it for users, and put nothing internal in it.
	Message string
}

func (e *MethodError) Error() string { return e.Message }

// MethodBadRequest is a 400 with the code method_error and this message, which is sent to the client as it
// is.
func MethodBadRequest(message string) *MethodError {
	return &MethodError{Status: http.StatusBadRequest, Message: message}
}

// MethodUnprocessable is a 422 with the code method_error and this message, which is sent to the client as it
// is.
func MethodUnprocessable(message string) *MethodError {
	return &MethodError{Status: http.StatusUnprocessableEntity, Message: message}
}

// methodFailure is how an error of a handler reaches the HTTP layer: the app's own refusal as a
// *httpapi.MethodFailure, a document or database error as the *db.Error it wraps (mapped like the document
// API maps it), anything else as it is (a generic 500 whose cause is only logged).
func methodFailure(name string, err error) error {
	var me *MethodError
	if errors.As(err, &me) {
		return &httpapi.MethodFailure{Status: me.Status, Code: "method_error", Message: me.Message}
	}
	var de *DocError
	if errors.As(err, &de) {
		return de.Err
	}
	var dbe *db.Error
	if errors.As(err, &dbe) {
		return dbe
	}
	return fmt.Errorf("method %s: %w", name, err)
}

// methods runs the handlers of all the apps of the binary, by full name. Which methods a site has is the
// registry's business; this only runs the one it is asked for. It implements httpapi.MethodDispatch.
type methods struct {
	handlers map[string]MethodHandler
}

// Call runs the method in one transaction as caller (see MethodHandler for the rules).
func (m *methods) Call(ctx context.Context, pool *pgxpool.Pool, registry *core.Registry, name string, caller core.Caller, body json.RawMessage) (json.RawMessage, error) {
	handler := m.handlers[name]
	if handler == nil {
		return nil, fmt.Errorf("method %s: the registry lists it but the binary has no handler", name)
	}
	if caller.Kind != core.CallerUser {
		return nil, fmt.Errorf("method %s: called with a system caller", name)
	}
	// The client going away does not cancel the method: it finishes and commits or rolls back.
	return runMethod(context.WithoutCancel(ctx), pool, registry, handler, name, caller, body)
}

// runMethod is one call: the handler and the commit in one transaction.
func runMethod(ctx context.Context, pool *pgxpool.Pool, registry *core.Registry, handler MethodHandler, name string, caller core.Caller, body json.RawMessage) (result json.RawMessage, err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, &db.Error{Kind: db.ErrPool, Message: err.Error(), Err: err}
	}
	// The connection goes back to the pool only when its transaction ended normally. After a panic, or an
	// error whose effect on the connection is unknown (a failed rollback or commit), it is closed: a pool
	// must never hand out a connection with a transaction open.
	healthy := false
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, fmt.Errorf("method %s: %s", name, panicText(r))
			healthy = false
		}
		if healthy {
			conn.Release()
			return
		}
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Hijack().Close(closeCtx)
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, wrapDB(err)
	}
	value, handlerErr := handler(ctx, &MethodContext{tx: tx, registry: registry, caller: caller, name: name, body: body})
	if handlerErr != nil {
		healthy = tx.Rollback(ctx) == nil
		return nil, methodFailure(name, handlerErr)
	}
	data, err := marshalMethodResult(value)
	if err != nil {
		healthy = tx.Rollback(ctx) == nil
		return nil, fmt.Errorf("method %s: the result cannot be written as JSON: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapDB(err)
	}
	healthy = true
	return data, nil
}

// marshalMethodResult is the JSON of what a handler returned, compact and without HTML escaping.
func marshalMethodResult(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), nil
}

// wrapDB is a database error of the driver as a *db.Error (mapped by the HTTP layer: a generic 500).
func wrapDB(err error) error { return asDBError(err) }
