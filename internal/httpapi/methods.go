package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tvstore/forgebase-go/internal/core"
)

// App method endpoints (M9.1, ADR 0019; the Rust module methods.rs of forgebase-http):
// POST /api/v1/method/{app}.{method}.
//
// This package knows names and access only (the registry of the site) and nothing of handlers: the
// package internal/app implements MethodDispatch, runs the handler in one transaction as the caller, and
// is given to the State with WithMethods. A method that does not exist, that is not installed on the
// site, or that the caller may not call is the same 404 not_found, so that method names are not
// revealed. Without a dispatcher every method call is a 404.

// MethodDispatch runs a method. Implemented by internal/app: one transaction, the documents handle as
// the caller, the handler, then commit on success and rollback on every error (a panic included).
//
// name is the full name (<app>.<method>) of a method that the site's registry lists and that caller may
// call; body is a JSON object (the text of the request body, "{}" when it had none). The result is the
// JSON the handler returned, sent as {"data": result}. An error is mapped like this: a *MethodFailure
// is the app's own refusal (status, code and message as the app wrote them), a *db.Error (also wrapped)
// is mapped as the document API maps it, anything else is logged and answered with a generic 500.
type MethodDispatch interface {
	Call(ctx context.Context, pool *pgxpool.Pool, registry *core.Registry, name string, caller core.Caller, body json.RawMessage) (json.RawMessage, error)
}

// MethodFailure is an error a method handler chose: a client error status with the code and the message
// it gave, which are sent as they are (written for users). A status that is not 4xx is a mistake of the
// app and is answered with a generic 500.
type MethodFailure struct {
	// Status is the HTTP status (400 or 422 today).
	Status int
	// Code is the code in the error envelope.
	Code string
	// Message is the message, as the app wrote it.
	Message string
}

func (e *MethodFailure) Error() string { return e.Code + ": " + e.Message }

// WithMethods gives the site the dispatcher that runs its app methods and returns the State. Call it
// before the State serves requests.
func (s *State) WithMethods(m MethodDispatch) *State {
	s.methods = m
	return s
}

// readMethodBody is the JSON object body of a method call. An empty body is {} (and needs no content
// type); a body that is not empty must be application/json (415) and a JSON object (400). Over the
// body limit is 413.
func (s *State) readMethodBody(w http.ResponseWriter, r *request) (json.RawMessage, error) {
	jsonType := jsonContentType(r.Header)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.config.BodyLimitBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, newError(http.StatusRequestEntityTooLarge, "payload_too_large", "the request body is too large")
		}
		return nil, errBadRequest("the body could not be read")
	}
	if len(body) == 0 {
		return json.RawMessage("{}"), nil
	}
	if !jsonType {
		return nil, newError(http.StatusUnsupportedMediaType, "unsupported_media_type",
			"the body must be JSON (Content-Type: application/json)")
	}
	if !utf8.Valid(body) {
		return nil, errBadRequest("the body is not valid JSON")
	}
	v, err := decodeJSON(string(body))
	if err != nil {
		return nil, errBadRequest("the body is not valid JSON")
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, errBadRequest("the body must be a JSON object")
	}
	return json.RawMessage(body), nil
}

// handleMethodCall is POST /api/v1/method/{app}.{method} with a JSON object body (optional): calls the
// method and returns 200 {"data": <what the handler returned>}.
func handleMethodCall(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	// The order of the Rust extractors: the body is read (413, 415, 400) before the path is decoded.
	body, err := s.readMethodBody(w, r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(1)
	if err != nil {
		return err
	}
	caller := auth.caller()
	// Whether the method exists on this site and whether the caller may call it are one answer. This
	// asks the one evaluator of internal/core; the handler's documents are checked again, per call,
	// by internal/db.
	registry := s.snapshot()
	method := registry.Method(path[0])
	if method == nil || !core.CanCallMethod(method.Access, caller) {
		return errNotFound("method not found")
	}
	if _, err := params(r); err != nil {
		return err
	}
	if s.methods == nil {
		return errNotFound("method not found")
	}
	result, err := s.methods.Call(r.Context(), s.pool, registry, method.FullName(), caller, body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
	return nil
}

// fromMethodFailure maps the failure a handler chose.
func fromMethodFailure(e *MethodFailure) *apiError {
	if e.Status >= 400 && e.Status < 500 {
		return newError(e.Status, e.Code, e.Message)
	}
	return errInternal("method error with status " + strconv.Itoa(e.Status) + ": " + e.Message)
}
