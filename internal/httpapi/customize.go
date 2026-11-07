package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// The customize routes (M10.1, ADR 0023; the Rust module customize.rs): site custom fields, for the
// Administrator role only.
//
// internal/db does the work (validation against the site's effective registry, the row, the column, the
// metadata version, all in one transaction under the migrate lock); these handlers check the role, map the
// body and the errors, and reload this site's registry once a change committed, so the next request on this
// server sees it. Other servers see it within their Config.MetaReloadInterval.

// customizeLockTimeout is how long a change waits for the migrate lock (the default of migrate).
const customizeLockTimeout = db.DefaultLockTimeout

// administrator refuses every caller without the Administrator role: 403 permission_denied, like a shared
// saved view. Checked before the DocType is looked at, so a non-administrator learns nothing.
func administrator(auth *authenticated, what string) error {
	if auth.caller().BypassesPermissions() {
		return nil
	}
	return newError(http.StatusForbidden, "permission_denied", "permission denied: only an Administrator can "+what)
}

// knownDocType is the DocType called name, which must exist on the site (a child DocType exists, and is
// refused as a target by the rules of a custom field).
func knownDocType(registry *core.Registry, name string) error {
	if registry.Get(name) == nil {
		return errNotFound("unknown doctype")
	}
	return nil
}

// reloadAfterCommit rebuilds this server's registry after a change committed. A failure is only logged: the
// change is made and the version poll catches up within the reload interval, so the caller must not be told
// that a committed change failed.
func (s *State) reloadAfterCommit(ctx context.Context) {
	if err := s.reloadRegistry(context.WithoutCancel(ctx)); err != nil {
		logInternal("warning: site `%s`: the change was saved but the registry could not be reloaded; the next poll will: %s\n",
			s.siteName(), fromError(err).message)
	}
}

func customFieldJSON(field *db.CustomField) (map[string]any, error) {
	created, err := db.UTCRFC3339(field.CreatedAt)
	if err != nil {
		return nil, err
	}
	modified, err := db.UTCRFC3339(field.ModifiedAt)
	if err != nil {
		return nil, err
	}
	definition, err := decodeJSON(string(field.Definition))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"doctype":     field.DocType,
		"name":        field.Name,
		"label":       field.Label(),
		"type":        field.TypeName(),
		"definition":  definition,
		"enabled":     field.Enabled,
		"created_at":  created,
		"modified_at": modified,
		"modified_by": field.ModifiedBy,
	}, nil
}

// handleCustomizeList is GET /api/v1/customize/{doctype}/fields: the custom fields of the DocType, enabled and
// disabled, in the order they were added.
func handleCustomizeList(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(1)
	if err != nil {
		return err
	}
	if err := administrator(auth, "customize fields"); err != nil {
		return err
	}
	if err := knownDocType(s.snapshot(), path[0]); err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	fields, err := db.ListCustomFields(r.Context(), s.pool, &path[0])
	if err != nil {
		return err
	}
	out := make([]any, len(fields))
	for i, f := range fields {
		if out[i], err = customFieldJSON(f); err != nil {
			return err
		}
	}
	writeData(w, http.StatusOK, out)
	return nil
}

// toCoreJSON is a body parsed by decodeJSON as core.ParseFieldJSON reads it: the numbers are json.Number.
func toCoreJSON(v any) any {
	switch x := v.(type) {
	case jsonNumber:
		return json.Number(x.String())
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, item := range x {
			m[k] = toCoreJSON(item)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = toCoreJSON(item)
		}
		return out
	}
	return v
}

// handleCustomizeAdd is POST /api/v1/customize/{doctype}/fields: the body is the field as JSON with the keys
// of a TOML [[fields]] entry. 201 with the stored field.
func handleCustomizeAdd(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	body, err := s.readJSON(w, r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(1)
	if err != nil {
		return err
	}
	if err := administrator(auth, "customize fields"); err != nil {
		return err
	}
	if err := knownDocType(s.snapshot(), path[0]); err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	field, err := core.ParseFieldJSON(toCoreJSON(body))
	if err != nil {
		return customFieldParseError(err)
	}
	stored, err := db.AddCustomField(r.Context(), s.pool, s.baseRegistry(), s.siteName(), path[0], field,
		auth.caller().Name(), customizeLockTimeout)
	if err != nil {
		return err
	}
	s.reloadAfterCommit(r.Context())
	out, err := customFieldJSON(stored)
	if err != nil {
		return err
	}
	writeData(w, http.StatusCreated, out)
	return nil
}

// customFieldParseError is the error of a body that is not a field: the same as when the database refuses it
// (Rust wraps the parse error in DbError::CustomField).
func customFieldParseError(err error) error {
	if oe, ok := err.(core.OverlayError); ok {
		return &db.Error{Kind: db.ErrCustomField, Overlay: oe}
	}
	return err
}

func customizeSetEnabled(s *State, w http.ResponseWriter, r *request, enabled bool) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	path, err := r.pathParams(2)
	if err != nil {
		return err
	}
	if err := administrator(auth, "customize fields"); err != nil {
		return err
	}
	if err := knownDocType(s.snapshot(), path[0]); err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	stored, err := db.SetCustomFieldEnabled(r.Context(), s.pool, path[0], path[1], enabled, auth.caller().Name(), customizeLockTimeout)
	if err != nil {
		return err
	}
	s.reloadAfterCommit(r.Context())
	out, err := customFieldJSON(stored)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, out)
	return nil
}

// handleCustomizeDisable is POST /api/v1/customize/{doctype}/fields/{name}/disable: hides the field
// everywhere; its column and data stay. 200 with the field.
func handleCustomizeDisable(s *State, w http.ResponseWriter, r *request) error {
	return customizeSetEnabled(s, w, r, false)
}

// handleCustomizeEnable is POST /api/v1/customize/{doctype}/fields/{name}/enable: brings back a disabled field
// with its data. 200 with the field.
func handleCustomizeEnable(s *State, w http.ResponseWriter, r *request) error {
	return customizeSetEnabled(s, w, r, true)
}
