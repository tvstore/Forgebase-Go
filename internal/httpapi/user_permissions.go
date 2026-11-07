package httpapi

import (
	"net/http"

	"github.com/tvstore/forgebase-go/internal/db"
)

// The user permission routes (M10.3, ADR 0025; the Rust module user_permissions.rs): which documents a user
// is restricted to, for the Administrator role only. internal/db does the work (the checks, the limit, the
// row); these handlers check the role and map the body and the errors. A change applies on the affected
// user's next request: the permissions are read with the roles of every authenticated request, so no registry
// is reloaded.

func userPermissionJSON(p *db.UserPermission) (map[string]any, error) {
	created, err := db.UTCRFC3339(p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return map[string]any{"user": p.User, "doctype": p.DocType, "value": p.Value, "created_at": created}, nil
}

// handleUserPermissionList is GET /api/v1/user-permissions?user=U&doctype=D: the restrictions, sorted by user,
// DocType and value; both parameters are optional filters. An unknown user is 404.
func handleUserPermissionList(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	if err := administrator(auth, "manage user permissions"); err != nil {
		return err
	}
	query, err := params(r, "user", "doctype")
	if err != nil {
		return err
	}
	var user, doctype *string
	if v, ok := query["user"]; ok {
		user = &v
	}
	if v, ok := query["doctype"]; ok {
		doctype = &v
	}
	rows, err := db.ListUserPermissions(r.Context(), s.pool, user, doctype)
	if err != nil {
		return err
	}
	out := make([]any, len(rows))
	for i := range rows {
		if out[i], err = userPermissionJSON(&rows[i]); err != nil {
			return err
		}
	}
	writeData(w, http.StatusOK, out)
	return nil
}

// permissionTriple is the three strings of a body {"user", "doctype", "value"}.
func permissionTriple(body any) (user, doctype, value string, err error) {
	m, err := object(body, "user", "doctype", "value")
	if err != nil {
		return
	}
	text := func(key string) (string, error) {
		if v, ok := m[key].(string); ok {
			return v, nil
		}
		return "", errBadRequest("`" + key + "` must be a string")
	}
	if user, err = text("user"); err != nil {
		return
	}
	if doctype, err = text("doctype"); err != nil {
		return
	}
	value, err = text("value")
	return
}

// handleUserPermissionAdd is POST /api/v1/user-permissions: body {"user", "doctype", "value"}. 201 with
// {"data": {"user", "doctype", "value", "added": true}}, or 200 with "added": false when the row existed
// already. 404 for an unknown user; 422 for an unknown or child DocType, a document that does not exist, or
// the limit of 1000 documents per user and DocType.
func handleUserPermissionAdd(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	body, err := s.readJSON(w, r)
	if err != nil {
		return err
	}
	if err := administrator(auth, "manage user permissions"); err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	user, doctype, value, err := permissionTriple(body)
	if err != nil {
		return err
	}
	added, err := db.AddUserPermission(r.Context(), s.pool, s.snapshot(), user, doctype, value)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if added {
		status = http.StatusCreated
	}
	writeData(w, status, map[string]any{"user": user, "doctype": doctype, "value": value, "added": added})
	return nil
}

// handleUserPermissionRemove is POST /api/v1/user-permissions/remove: the same body. 200 with
// {"data": {"user", "doctype", "value", "removed": bool}} (false: the user did not have the row). 404 for an
// unknown user. (There is no DELETE with a body in this API: a delete is a POST to .../remove here, as the
// other actions are, for example .../disable.)
func handleUserPermissionRemove(s *State, w http.ResponseWriter, r *request) error {
	auth, err := s.authenticate(r)
	if err != nil {
		return err
	}
	body, err := s.readJSON(w, r)
	if err != nil {
		return err
	}
	if err := administrator(auth, "manage user permissions"); err != nil {
		return err
	}
	if _, err := params(r); err != nil {
		return err
	}
	user, doctype, value, err := permissionTriple(body)
	if err != nil {
		return err
	}
	removed, err := db.RemoveUserPermission(r.Context(), s.pool, user, doctype, value)
	if err != nil {
		return err
	}
	writeData(w, http.StatusOK, map[string]any{"user": user, "doctype": doctype, "value": value, "removed": removed})
	return nil
}
