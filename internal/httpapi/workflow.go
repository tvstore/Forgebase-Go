package httpapi

import (
	"net/http"

	"github.com/tvstore/forgebase-go/internal/db"
)

// The workflow routes (M8.1, ADR 0015; the Rust functions transition, transitions and history of
// routes.rs): who may take a transition is decided by internal/db (the transition's role and the right of
// its docstatus move).

// handleTransition is POST /api/v1/resource/{doctype}/{name}/transition: body {"action": "...", "version":
// N}. 200 with the document as stored, like submit.
func handleTransition(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	body, err := s.readJSON(w, r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(2)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	m, err := object(body, "action", "version")
	if err != nil {
		return err
	}
	expected, err := version(m)
	if err != nil {
		return err
	}
	action, ok := m["action"].(string)
	if !ok {
		return errBadRequest("`action` must be a string")
	}
	stored, err := db.Transition(r.Context(), s.pool, registry, doctype.Name, path[1], action, expected, auth.caller())
	if err != nil {
		return err
	}
	return writeDocument(w, http.StatusOK, stored)
}

// handleTransitions is GET /api/v1/resource/{doctype}/{name}/transitions: the state of the document and
// the transitions from it that the caller may take now (role and right, if_owner applied; a transition
// whose from-state has another docstatus than the document is left out). Hooks are not run, so a hook may
// still refuse one.
func handleTransitions(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(2)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	available, err := db.AvailableTransitions(r.Context(), s.pool, registry, doctype.Name, path[1], auth.caller())
	if err != nil {
		return err
	}
	transitions := make([]any, len(available.Transitions))
	for i, t := range available.Transitions {
		transitions[i] = map[string]any{"action": t.Action, "to": t.To}
	}
	var state any
	if available.State != "" {
		state = available.State
	}
	writeData(w, http.StatusOK, map[string]any{"state": state, "transitions": transitions})
	return nil
}

// handleHistory is GET /api/v1/resource/{doctype}/{name}/history: the transitions of the document, oldest
// first, at most the newest 500.
func handleHistory(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(2)
	if err != nil {
		return err
	}
	registry := s.snapshot()
	doctype, err := topLevel(registry, path[0])
	if err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	entries, err := db.History(r.Context(), s.pool, registry, doctype.Name, path[1], auth.caller())
	if err != nil {
		return err
	}
	history := make([]any, len(entries))
	for i, e := range entries {
		at, err := db.UTCRFC3339(e.At)
		if err != nil {
			return err
		}
		history[i] = map[string]any{"action": e.Action, "from": e.From, "to": e.To, "actor": e.Actor, "at": at}
	}
	writeData(w, http.StatusOK, map[string]any{"history": history})
	return nil
}
