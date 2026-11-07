package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Managing user permissions (M10.3, ADR 0025): add, remove and list the documents a user is
// restricted to (the Rust module user_permissions.rs). The rows are in fb_user_permission (migration
// 0013) and are read with the roles of every authenticated request (ValidateSession), so a change
// applies on the user's next request, with no reload. The command line and the Administrator-only API
// both call these functions.
//
// A restriction says: of this DocType, the user sees only these documents (and only documents of
// other DocTypes whose Links to it are empty or point at them). What it means for every read and write
// is decided by core.RowScopeOf and applied in SQL.

// UserPermission is one restriction of a user: they may see the document Value of DocType (and, when
// it is the only one they have for the DocType, nothing else of it).
type UserPermission struct {
	// User is the username.
	User string
	// DocType is the DocType the restriction is on.
	DocType string
	// Value is the name of the allowed document.
	Value string
	// CreatedAt is when the row was added.
	CreatedAt time.Time
}

func invalidPermission(message string) error { return authErr(AuthInvalidUserPermission, message) }

// AddUserPermission restricts username to the document value of doctype (in addition to the ones they
// have). It returns false when the row existed already.
//
// Checked in this order, with nothing written on failure: the username rule; the DocType must be in
// registry (the site's effective registry) and not a child DocType; the value must not be empty or
// contain NUL; the user must exist (AuthUserNotFound); the document must exist (looked up as the
// system context, so a user permission names a document that is there when it is added); a row that
// exists already then succeeds (false); and the user must have fewer than core.MaxUserPermissions for
// the DocType (the names are bound as one array in every read, so the number is kept small). The user
// row is locked, so two adds cannot both pass the limit.
func AddUserPermission(ctx context.Context, client TxStarter, registry *core.Registry, username, doctype, value string) (bool, error) {
	if err := checkUsername(username); err != nil {
		return false, err
	}
	target := registry.Get(doctype)
	if target == nil {
		return false, invalidPermission(fmt.Sprintf("unknown DocType `%s`", core.Shorten(doctype)))
	}
	if target.IsChild {
		return false, invalidPermission(fmt.Sprintf("`%s` is a child DocType: its rows follow the parent, restrict the parent", doctype))
	}
	if value == "" || strings.ContainsRune(value, 0) {
		return false, invalidPermission("the value must be a document name: not empty, no NUL character")
	}
	tx, end, err := begin(ctx, client)
	if err != nil {
		return false, authDB(err)
	}
	defer end()
	// Locks the user for the whole add (and fails for a user that does not exist).
	var id UserID
	err = tx.QueryRow(ctx, "SELECT id FROM fb_user WHERE username = $1 FOR NO KEY UPDATE", username).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, authErr(AuthUserNotFound, username)
	}
	if err != nil {
		return false, authDB(err)
	}
	exists, err := Get(ctx, tx, registry, doctype, value, core.SystemCaller("cli"))
	if err != nil {
		return false, authDB(err)
	}
	if exists == nil {
		return false, invalidPermission(fmt.Sprintf("no `%s` document named `%s`", doctype, core.Shorten(value)))
	}
	var one int32
	err = tx.QueryRow(ctx, "SELECT 1 FROM fb_user_permission WHERE user_id = $1 AND doctype = $2 AND value = $3", id, doctype, value).Scan(&one)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, authDB(err)
	}
	var count int64
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM fb_user_permission WHERE user_id = $1 AND doctype = $2", id, doctype).Scan(&count); err != nil {
		return false, authDB(err)
	}
	if count >= core.MaxUserPermissions {
		return false, invalidPermission(fmt.Sprintf("user `%s` is restricted to %d `%s` documents already, the most allowed",
			username, core.MaxUserPermissions, doctype))
	}
	if _, err := tx.Exec(ctx, "INSERT INTO fb_user_permission (user_id, doctype, value) VALUES ($1, $2, $3)", id, doctype, value); err != nil {
		return false, authDB(err)
	}
	if err := commit(ctx, tx); err != nil {
		return false, authDB(err)
	}
	return true, nil
}

// RemoveUserPermission takes one restriction from a user. It returns false when the user did not have
// it. The DocType and the document are not checked: a row for a DocType or a document that is gone
// can always be removed. An unknown user is AuthUserNotFound.
func RemoveUserPermission(ctx context.Context, client TxStarter, username, doctype, value string) (bool, error) {
	if err := checkUsername(username); err != nil {
		return false, err
	}
	tx, end, err := begin(ctx, client)
	if err != nil {
		return false, authDB(err)
	}
	defer end()
	id, err := userID(ctx, tx, username)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, "DELETE FROM fb_user_permission WHERE user_id = $1 AND doctype = $2 AND value = $3", id, doctype, value)
	if err != nil {
		return false, authDB(err)
	}
	if err := commit(ctx, tx); err != nil {
		return false, authDB(err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListUserPermissions are the restrictions, sorted by username, DocType and value; of one user and/or
// one DocType (nil: all). Rows for a DocType the site no longer has are listed too (they are ignored
// when reading, and can be removed). An unknown user is AuthUserNotFound.
func ListUserPermissions(ctx context.Context, q Queryer, user, doctype *string) ([]UserPermission, error) {
	if user != nil {
		if err := checkUsername(*user); err != nil {
			return nil, err
		}
		if _, err := userID(ctx, q, *user); err != nil {
			return nil, err
		}
	}
	rows, err := q.Query(ctx,
		"SELECT u.username, p.doctype, p.value, p.created_at "+
			"FROM fb_user_permission p JOIN fb_user u ON u.id = p.user_id "+
			"WHERE ($1::text IS NULL OR u.username = $1) AND ($2::text IS NULL OR p.doctype = $2) "+
			"ORDER BY u.username, p.doctype, p.value", user, doctype)
	if err != nil {
		return nil, authDB(err)
	}
	defer rows.Close()
	out := []UserPermission{}
	for rows.Next() {
		var p UserPermission
		if err := rows.Scan(&p.User, &p.DocType, &p.Value, &p.CreatedAt); err != nil {
			return nil, authDB(err)
		}
		p.CreatedAt = p.CreatedAt.UTC()
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, authDB(err)
	}
	return out, nil
}
