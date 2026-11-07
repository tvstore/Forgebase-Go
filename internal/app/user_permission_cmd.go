package app

import (
	"context"
	"fmt"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// The user permission commands (M10.3, ADR 0025; the Rust module user_permission_cmd.rs): add, remove and list
// the documents a user is restricted to. They run as the system context, like the other user commands; the work
// is in internal/db, which the Administrator-only API uses too. A change applies on the user's next request, so
// no server needs a restart or a reload.

// userPermission runs a user permission command on site. add checks the DocType against the --doctypes
// directory too.
func (r *runner) userPermission(ctx context.Context, site *site) error {
	verb := r.m.path[2]
	local := ""
	if verb == "add" {
		local = r.m.str("doctypes")
	}
	composed, err := r.siteComposed(site, local)
	if err != nil {
		return err
	}
	sc, err := site.open(ctx, r.o, composed.registry, 0)
	if err != nil {
		return err
	}
	defer sc.close()
	user, doctype, value := r.m.str("user"), r.m.str("doctype"), r.m.str("value")
	var text string
	switch verb {
	case "add":
		added, err := db.AddUserPermission(ctx, sc.pool, sc.registry, user, doctype, value)
		if err != nil {
			return err
		}
		if added {
			text = fmt.Sprintf("restricted user %s to %s %s; it applies on the user's next request (at most %d per DocType)\n",
				user, doctype, value, core.MaxUserPermissions)
		} else {
			text = fmt.Sprintf("user %s is restricted to %s %s already\n", user, doctype, value)
		}
	case "remove":
		removed, err := db.RemoveUserPermission(ctx, sc.pool, user, doctype, value)
		if err != nil {
			return err
		}
		if removed {
			text = fmt.Sprintf("removed the restriction of user %s to %s %s; it applies on the user's next request\n", user, doctype, value)
		} else {
			text = fmt.Sprintf("user %s is not restricted to %s %s\n", user, doctype, value)
		}
	case "list":
		var onlyUser, onlyDocType *string
		if v, ok := r.m.opt("user"); ok {
			onlyUser = &v
		}
		if v, ok := r.m.opt("doctype"); ok {
			onlyDocType = &v
		}
		rows, err := db.ListUserPermissions(ctx, sc.pool, onlyUser, onlyDocType)
		if err != nil {
			return err
		}
		text = userPermissionTable(rows)
	default:
		return fmt.Errorf("unknown user permission command %q", verb)
	}
	return r.write(text)
}

// userPermissionTable is the table of list: sorted by user, DocType and value, the header always present.
func userPermissionTable(permissions []db.UserPermission) string {
	rows := make([][]string, len(permissions))
	for i, p := range permissions {
		rows[i] = []string{p.User, p.DocType, p.Value, rfc3339(p.CreatedAt)}
	}
	return textTable([]string{"USER", "DOCTYPE", "VALUE", "ADDED"}, rows)
}
