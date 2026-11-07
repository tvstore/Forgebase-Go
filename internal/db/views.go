package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Saved list views (M8.3, architecture section 11): a named filter, sort and column set per DocType (the
// Rust module views.rs).
//
// A view is a row of fb_saved_view (migration 0009). It is private (it has an owner, the user who saved
// it) or shared (no owner; everyone who can read the DocType sees it, and only an Administrator, or the
// system context, creates, changes or deletes one).
//
//   - Privacy. ListViews returns the caller's own private views and the shared ones. Another user's
//     private view is ErrNotFound for UpdateView and DeleteView, not a refusal, so nothing tells that it
//     exists, an Administrator included. A shared view that a non-administrator tries to change is
//     ErrViewDenied: they can see it anyway.
//   - Rights. Every call needs the read right on the DocType, decided by the one evaluator (accessTo),
//     like List. A view grants nothing: applying it runs the ordinary list query, with the caller's own
//     read scope.
//   - Definition. {"filters": [...], "sort": [...], "columns": [...]}, the filters and the sort in the
//     form of the list API. It is checked when it is written, by the code that runs a list
//     (ViewDefinition), and not when it is read: a DocType may lose a field that a view names, and the
//     client then gets invalid_query from the list and offers to fix the view.
//   - Limits. A label is 1 to 64 characters, trimmed; a definition is at most 16 KiB; a user has at most
//     100 private views per DocType and a DocType at most 100 shared ones.
//   - Concurrency. UpdateView and DeleteView need the version the caller read.
//   - A view is never converted between private and shared (not supported in M8): save a new one.
//
// The system context has no user id: it manages shared views only.

const (
	// MaxLabelChars is the longest label, in characters.
	MaxLabelChars = 64
	// MaxDefinitionBytes is the largest serialized definition, in bytes.
	MaxDefinitionBytes = 16 * 1024
	// MaxPrivateViews is the most private views one user has per DocType.
	MaxPrivateViews = 100
	// MaxSharedViews is the most shared views a DocType has.
	MaxSharedViews = 100
	// MaxViewColumns is the most columns a view shows (the cap of a DocType's list_fields).
	MaxViewColumns = core.MaxListFields
)

// viewName is what the DocType is called in ErrNotFound and ErrConflict errors about a view.
const viewName = "saved view"

// View is a saved view as stored.
type View struct {
	// ID is the id (UUID v7).
	ID uuid.UUID
	// DocType is the DocType.
	DocType string
	// Label is the label.
	Label string
	// Owner is the user name of the owner; "" for a shared view.
	Owner string
	// Definition is {"filters", "sort", "columns"}, as written (JSON text).
	Definition json.RawMessage
	// Version is incremented by every update.
	Version int64
	// ModifiedAt is when the view was last written.
	ModifiedAt time.Time
}

// Shared is true for a shared view.
func (v *View) Shared() bool { return v.Owner == "" }

// ViewDefinition is a definition to store: the JSON as it is kept, and the filters and sort it holds,
// parsed by the caller with the parser of the list API (that parser lives in internal/httpapi, which
// depends on this package). The parsed query is checked here by the list's own validation; the JSON must
// have exactly the keys filters, sort and columns and the same number of filters and sort keys as the
// query.
type ViewDefinition struct {
	// JSON is what is stored.
	JSON json.RawMessage
	// Query is the filters and sort of JSON, parsed. Limit and Offset stay unset.
	Query ListQuery
}

// ViewChanges are the changes UpdateView makes; nil keeps what the view has.
type ViewChanges struct {
	// Label is a new label.
	Label *string
	// Definition is a new definition.
	Definition *ViewDefinition
}

const viewColumns = "v.id, v.doctype, v.label, u.username, v.definition::text, v.version, v.modified_at"

func invalidView(format string, args ...any) error {
	return &Error{Kind: ErrInvalidView, Message: fmt.Sprintf(format, args...)}
}

func viewNotFound(id uuid.UUID) error {
	return &Error{Kind: ErrNotFound, DocType: viewName, Name: id.String()}
}

func scanView(row pgx.Row) (*View, error) {
	var v View
	var owner *string
	var definition string
	if err := row.Scan(&v.ID, &v.DocType, &v.Label, &owner, &definition, &v.Version, &v.ModifiedAt); err != nil {
		return nil, err
	}
	if owner != nil {
		v.Owner = *owner
	}
	v.Definition = json.RawMessage(definition)
	v.ModifiedAt = v.ModifiedAt.UTC()
	return &v, nil
}

// callerID is the id of the user caller is; false for the system context or a user that does not exist.
func callerID(ctx context.Context, q Queryer, caller core.Caller) (uuid.UUID, bool, error) {
	if caller.Kind != core.CallerUser {
		return uuid.UUID{}, false, nil
	}
	var id uuid.UUID
	err := q.QueryRow(ctx, "SELECT id FROM fb_user WHERE username = $1", caller.Username).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.UUID{}, false, nil
	}
	if err != nil {
		return uuid.UUID{}, false, wrap(err)
	}
	return id, true, nil
}

// nullableUUID is id as a query argument: NULL when absent.
func nullableUUID(id uuid.UUID, ok bool) any {
	if !ok {
		return nil
	}
	return id
}

// ListViews are the views caller sees for doctype: the shared ones first, then the caller's own, each
// group by label in byte order. Needs the read right on the DocType (ErrPermissionDenied). The
// definitions are returned as stored, valid or not.
func ListViews(ctx context.Context, q Queryer, registry *core.Registry, doctype string, caller core.Caller) ([]*View, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	if _, err := accessTo(dt, caller, core.RightRead); err != nil {
		return nil, err
	}
	ownerID, ok, err := callerID(ctx, q, caller)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, "SELECT "+viewColumns+" FROM fb_saved_view v LEFT JOIN fb_user u ON u.id = v.owner_id "+
		"WHERE v.doctype = $1 AND (v.owner_id IS NULL OR v.owner_id = $2) "+
		"ORDER BY (v.owner_id IS NOT NULL), v.label COLLATE \"C\"", dt.Name, nullableUUID(ownerID, ok))
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	// A view that names a field the caller cannot read is not shown to them: its definition would tell
	// the field's name (ADR 0024).
	access := core.FieldAccessOf(dt, caller)
	views := []*View{}
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, wrap(err)
		}
		if !namesHiddenField(dt, access, v.Definition) {
			views = append(views, v)
		}
	}
	return views, wrap(rows.Err())
}

// namesHiddenField says whether a view definition (filters, sort, columns) names a field that access
// does not let the caller read. A name that is not a field of the DocType is not hidden (the view is just
// as broken for everybody).
func namesHiddenField(doctype *core.DocType, access core.FieldAccess, definition json.RawMessage) bool {
	var d struct {
		Filters []map[string]any `json:"filters"`
		Sort    []any            `json:"sort"`
		Columns []any            `json:"columns"`
	}
	// A definition that does not have this shape names nothing that can be read from it.
	var raw map[string]json.RawMessage
	if json.Unmarshal(definition, &raw) != nil {
		return false
	}
	_ = json.Unmarshal(raw["filters"], &d.Filters)
	_ = json.Unmarshal(raw["sort"], &d.Sort)
	_ = json.Unmarshal(raw["columns"], &d.Columns)
	hidden := func(name string) bool {
		f := doctype.Field(name)
		return f != nil && !access.Shows(f.PermLevel)
	}
	for _, f := range d.Filters {
		if name, ok := f["field"].(string); ok && hidden(name) {
			return true
		}
	}
	for _, s := range d.Sort {
		if spec, ok := s.(string); ok {
			name, _, _ := strings.Cut(spec, ":")
			if hidden(name) {
				return true
			}
		}
	}
	for _, c := range d.Columns {
		if name, ok := c.(string); ok && hidden(name) {
			return true
		}
	}
	return false
}

// CreateView creates a view and returns it. A private view (shared false) belongs to the caller; a shared
// one needs the Administrator role or the system context (ErrViewDenied), and the system context can
// create shared views only. Needs the read right on the DocType.
//
// The label and definition are checked (ErrInvalidView, or the list's ErrInvalidQuery reworded as one); a
// label that is taken, for the same user and DocType or for a shared view, is ErrViewExists; over the
// limit of private or shared views is ErrInvalidView. The count and the insert run in one transaction
// under an advisory lock, so the limit holds for concurrent requests too.
func CreateView(ctx context.Context, client Client, registry *core.Registry, doctype string, caller core.Caller,
	label string, definition *ViewDefinition, shared bool,
) (*View, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	if _, err := accessTo(dt, caller, core.RightRead); err != nil {
		return nil, err
	}
	if shared && !caller.BypassesPermissions() {
		return nil, &Error{Kind: ErrViewDenied, Message: "only an Administrator can create a shared view"}
	}
	var ownerID uuid.UUID
	owned := false
	if !shared {
		if ownerID, owned, err = callerID(ctx, client, caller); err != nil {
			return nil, err
		}
		if !owned {
			return nil, &Error{Kind: ErrViewDenied, Message: "the system context can manage shared views only"}
		}
	}
	if err := checkLabel(label); err != nil {
		return nil, err
	}
	// A field the caller cannot read is unknown to the definition (ADR 0024).
	if err := checkDefinition(core.ReadableDocTypeFor(dt, caller), definition); err != nil {
		return nil, err
	}

	tx, end, err := begin(ctx, client)
	if err != nil {
		return nil, err
	}
	defer end()
	scope := "shared"
	if owned {
		scope = ownerID.String()
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "fb_saved_view:"+dt.Name+":"+scope); err != nil {
		return nil, wrap(err)
	}
	var count, limit int64
	var what string
	if owned {
		err = tx.QueryRow(ctx, "SELECT count(*) FROM fb_saved_view WHERE doctype = $1 AND owner_id = $2", dt.Name, ownerID).Scan(&count)
		limit, what = MaxPrivateViews, "private views for this DocType"
	} else {
		err = tx.QueryRow(ctx, "SELECT count(*) FROM fb_saved_view WHERE doctype = $1 AND owner_id IS NULL", dt.Name).Scan(&count)
		limit, what = MaxSharedViews, "shared views for this DocType"
	}
	if err != nil {
		return nil, wrap(err)
	}
	if count >= limit {
		return nil, invalidView("at most %d %s: delete one first", limit, what)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, wrap(err)
	}
	v, err := scanView(tx.QueryRow(ctx, "WITH v AS (INSERT INTO fb_saved_view (id, doctype, label, owner_id, definition) "+
		"VALUES ($1, $2, $3, $4, $5::text::jsonb) "+
		"RETURNING id, doctype, label, owner_id, definition, version, modified_at) "+
		"SELECT "+viewColumns+" FROM v LEFT JOIN fb_user u ON u.id = v.owner_id",
		id, dt.Name, label, nullableUUID(ownerID, owned), string(definition.JSON)))
	if err != nil {
		return nil, existsOr(err, dt.Name, label)
	}
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return v, nil
}

// UpdateView changes the label and/or the definition of a view and returns it with the next version.
//
// Who may: the owner of a private view; an Administrator (or the system context) for a shared view.
// Anyone else gets ErrNotFound for a private view (also when it is in another DocType than doctype),
// ErrViewDenied for a shared one. The permission is decided before the version is compared (ErrConflict),
// so a hidden view is never told from a missing one. Without a change: ErrNoChanges.
func UpdateView(ctx context.Context, q Queryer, registry *core.Registry, doctype string, id uuid.UUID,
	expectedVersion int64, caller core.Caller, changes ViewChanges,
) (*View, error) {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return nil, err
	}
	if _, err := accessTo(dt, caller, core.RightRead); err != nil {
		return nil, err
	}
	if err := authorizeView(ctx, q, dt, id, expectedVersion, caller); err != nil {
		return nil, err
	}
	if changes.Label == nil && changes.Definition == nil {
		return nil, &Error{Kind: ErrNoChanges}
	}
	if changes.Label != nil {
		if err := checkLabel(*changes.Label); err != nil {
			return nil, err
		}
	}
	var definition any
	if changes.Definition != nil {
		if err := checkDefinition(core.ReadableDocTypeFor(dt, caller), changes.Definition); err != nil {
			return nil, err
		}
		definition = string(changes.Definition.JSON)
	}
	var label any
	labelText := ""
	if changes.Label != nil {
		label, labelText = *changes.Label, *changes.Label
	}
	v, err := scanView(q.QueryRow(ctx, "WITH v AS (UPDATE fb_saved_view SET label = coalesce($3::text, label), "+
		"definition = coalesce($4::text::jsonb, definition), modified_at = now(), "+
		"version = version + 1 WHERE id = $1 AND doctype = $2 AND version = $5 "+
		"RETURNING id, doctype, label, owner_id, definition, version, modified_at) "+
		"SELECT "+viewColumns+" FROM v LEFT JOIN fb_user u ON u.id = v.owner_id",
		id, dt.Name, label, definition, expectedVersion))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Changed or deleted between the check and the write.
		return nil, missingOrConflict(ctx, q, dt, id, expectedVersion)
	case err != nil:
		return nil, existsOr(err, dt.Name, labelText)
	}
	return v, nil
}

// DeleteView deletes a view. The same rules as UpdateView.
func DeleteView(ctx context.Context, q Queryer, registry *core.Registry, doctype string, id uuid.UUID,
	expectedVersion int64, caller core.Caller,
) error {
	dt, err := documentDocType(registry, doctype)
	if err != nil {
		return err
	}
	if _, err := accessTo(dt, caller, core.RightRead); err != nil {
		return err
	}
	if err := authorizeView(ctx, q, dt, id, expectedVersion, caller); err != nil {
		return err
	}
	tag, err := q.Exec(ctx, "DELETE FROM fb_saved_view WHERE id = $1 AND doctype = $2 AND version = $3", id, dt.Name, expectedVersion)
	if err != nil {
		return wrap(err)
	}
	if tag.RowsAffected() == 0 {
		return missingOrConflict(ctx, q, dt, id, expectedVersion)
	}
	return nil
}

// authorizeView decides whether caller may change the view, then compares the version.
func authorizeView(ctx context.Context, q Queryer, doctype *core.DocType, id uuid.UUID, expectedVersion int64, caller core.Caller) error {
	var owner *uuid.UUID
	var current int64
	err := q.QueryRow(ctx, "SELECT owner_id, version FROM fb_saved_view WHERE id = $1 AND doctype = $2", id, doctype.Name).Scan(&owner, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return viewNotFound(id)
	}
	if err != nil {
		return wrap(err)
	}
	switch {
	case owner != nil:
		mine, ok, err := callerID(ctx, q, caller)
		if err != nil {
			return err
		}
		if !ok || mine != *owner {
			return viewNotFound(id)
		}
	case !caller.BypassesPermissions():
		return &Error{Kind: ErrViewDenied, Message: "only an Administrator can change a shared view"}
	}
	if current != expectedVersion {
		return &Error{Kind: ErrConflict, DocType: viewName, Name: id.String(), ExpectedVersion: expectedVersion, CurrentVersion: current}
	}
	return nil
}

// missingOrConflict says why a write that matched no row (after the authorization passed) did nothing.
func missingOrConflict(ctx context.Context, q Queryer, doctype *core.DocType, id uuid.UUID, expected int64) error {
	var current int64
	err := q.QueryRow(ctx, "SELECT version FROM fb_saved_view WHERE id = $1 AND doctype = $2", id, doctype.Name).Scan(&current)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return viewNotFound(id)
	case err != nil:
		return wrap(err)
	}
	return &Error{Kind: ErrConflict, DocType: viewName, Name: id.String(), ExpectedVersion: expected, CurrentVersion: current}
}

// existsOr turns a unique violation of the label indexes into ErrViewExists.
func existsOr(err error, doctype, label string) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == sqlStateUniqueViolation {
		return &Error{Kind: ErrViewExists, DocType: doctype, Label: label}
	}
	return wrap(err)
}

// checkLabel: a label is 1 to MaxLabelChars characters, has no leading or trailing whitespace and no
// control characters.
func checkLabel(label string) error {
	length := utf8.RuneCountInString(label)
	switch {
	case length == 0 || length > MaxLabelChars:
		return invalidView("the label must be 1 to %d characters", MaxLabelChars)
	case strings.TrimSpace(label) != label:
		return invalidView("the label must not start or end with whitespace")
	case strings.ContainsFunc(label, unicode.IsControl):
		return invalidView("the label must not contain control characters")
	}
	return nil
}

// columnAllowed: the columns a view may show are the column fields except Text (not Table: it has no
// column), and the standard columns name, owner, modified_by, created_at, modified_at, and docstatus and
// workflow_state where the DocType has them.
func columnAllowed(doctype *core.DocType, name string) error {
	switch {
	case name == "name" || name == "owner" || name == "modified_by" || name == "created_at" || name == "modified_at":
		return nil
	case name == "docstatus" && doctype.Submittable:
		return nil
	case name == "workflow_state" && doctype.Workflow != nil:
		return nil
	}
	f := doctype.Field(name)
	switch {
	case f == nil:
		return invalidView("unknown column %s for DocType `%s`", core.DebugQuote(core.Shorten(name)), doctype.Name)
	case f.Type.Kind == core.FieldTable:
		return invalidView("column %s is a table field: its rows cannot be shown in a list", core.DebugQuote(name))
	case f.Type.Kind == core.FieldText:
		return invalidView("column %s is a text field: long text cannot be shown in a list", core.DebugQuote(name))
	}
	return nil
}

// jsonStrings is the list of strings at key.
func jsonStrings(object map[string]json.RawMessage, key string) ([]string, error) {
	var items []json.RawMessage
	if raw, ok := object[key]; !ok || json.Unmarshal(raw, &items) != nil || items == nil {
		return nil, invalidView("`%s` must be a list", key)
	}
	out := make([]string, len(items))
	for i, item := range items {
		if err := json.Unmarshal(item, &out[i]); err != nil || len(item) == 0 || item[0] != '"' {
			return nil, invalidView("`%s` must be a list of strings", key)
		}
	}
	return out, nil
}

// validateQuery checks that query can be run on doctype (fields, operators, operand kinds, limit and
// offset) without running it. Saved views use it, so a view is validated by the very code that later
// runs it.
func validateQuery(doctype *core.DocType, query ListQuery) error {
	_, _, err := buildList(doctype, readScope{}, query)
	return err
}

// checkDefinition checks the definition: the keys, the size, the columns, and the filters and sort
// through the list's own validation (validateQuery).
func checkDefinition(doctype *core.DocType, definition *ViewDefinition) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(definition.JSON, &object); err != nil || object == nil {
		return invalidView("the definition must be an object")
	}
	keys := make([]string, 0, len(object))
	for k := range object {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k != "filters" && k != "sort" && k != "columns" {
			return invalidView("unknown key %s in the definition (use filters, sort, columns)", core.DebugQuote(core.Shorten(k)))
		}
	}
	if len(definition.JSON) > MaxDefinitionBytes {
		return invalidView("the definition is larger than %d KiB", MaxDefinitionBytes/1024)
	}
	var filters []json.RawMessage
	if raw, ok := object["filters"]; !ok || json.Unmarshal(raw, &filters) != nil || filters == nil {
		return invalidView("`filters` must be a list")
	}
	sorts, err := jsonStrings(object, "sort")
	if err != nil {
		return err
	}
	if len(filters) != len(definition.Query.Filters) || len(sorts) != len(definition.Query.Sort) {
		return invalidView("the filters and sort do not match their parsed form")
	}
	if err := validateQuery(doctype, definition.Query); err != nil {
		var e *Error
		if errors.As(err, &e) && e.Kind == ErrInvalidQuery {
			return invalidView("the filters or sort: %s", e.Message)
		}
		return err
	}
	columns, err := jsonStrings(object, "columns")
	if err != nil {
		return err
	}
	if len(columns) > MaxViewColumns {
		return invalidView("a view shows at most %d columns, got %d", MaxViewColumns, len(columns))
	}
	for i, column := range columns {
		for _, earlier := range columns[:i] {
			if earlier == column {
				return invalidView("column %s is listed twice", core.DebugQuote(column))
			}
		}
		if err := columnAllowed(doctype, column); err != nil {
			return err
		}
	}
	return nil
}
