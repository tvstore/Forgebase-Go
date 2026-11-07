package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Site custom fields (M10.1, ADR 0023; the Rust module custom_fields.rs): fields a site adds to a DocType in
// its own database.
//
// A field is a row of fb_custom_field (migration 0012), kept for ever: disabling hides it and keeps its
// column and data, and enabling brings both back. The effective metadata of a site is the registry of the
// apps (with their overlays) plus the enabled fields, appended after the fields of the app, in (doctype,
// created_at, name) order (EffectiveRegistry).
//
// Every change runs in one transaction under the advisory lock of migrate, so schema changes are serialised
// with it; it adds the column with the statements of the schema sync (fieldSteps) and increments
// fb_meta_version, which running servers poll (RegistryHolder). Nothing here drops a column.
//
// A stored field that no longer validates (for example its Link target left the apps) is skipped with a
// warning, never an error: a bad custom field must not take a site down.

// CustomField is a custom field as stored.
type CustomField struct {
	// DocType is the DocType the field was added to.
	DocType string
	// Name is the field name (it starts with custom_).
	Name string
	// Definition is the field as JSON text, with the keys of a TOML [[fields]] entry, as PostgreSQL writes the
	// jsonb value.
	Definition json.RawMessage
	// Enabled is false for a disabled field: hidden everywhere, column and data kept.
	Enabled bool
	// CreatedAt is when the field was added.
	CreatedAt time.Time
	// ModifiedAt is when the field was last changed.
	ModifiedAt time.Time
	// ModifiedBy is who changed it last (cli or a user name).
	ModifiedBy string
}

// definitionString is the string member key of the definition, or "" when it is not there (a damaged row).
func (f *CustomField) definitionString(key string) (string, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(f.Definition, &m); err != nil {
		return "", false
	}
	var s string
	if err := json.Unmarshal(m[key], &s); err != nil {
		return "", false
	}
	return s, true
}

// TypeName is the type key of the definition ("?" when it is not there: a damaged row).
func (f *CustomField) TypeName() string {
	if s, ok := f.definitionString("type"); ok {
		return s
	}
	return "?"
}

// Label is the label key of the definition (empty when it is not there).
func (f *CustomField) Label() string {
	s, _ := f.definitionString("label")
	return s
}

const customFieldColumns = "doctype, name, definition::text, enabled, created_at, modified_at, modified_by"

func scanCustomField(row interface{ Scan(...any) error }) (*CustomField, error) {
	var f CustomField
	var text string
	if err := row.Scan(&f.DocType, &f.Name, &text, &f.Enabled, &f.CreatedAt, &f.ModifiedAt, &f.ModifiedBy); err != nil {
		return nil, wrap(err)
	}
	// A definition that is not JSON cannot be written (the column is jsonb), so this only matters for a row
	// that someone edited around the framework: it reads as null, like Rust's unwrap_or(Json::Null).
	if json.Valid([]byte(text)) {
		f.Definition = json.RawMessage(text)
	} else {
		f.Definition = json.RawMessage("null")
	}
	return &f, nil
}

// ListCustomFields are the custom fields of doctype (every DocType when nil), enabled and disabled, in the
// order they are added to the DocType: DocType, creation time, name.
func ListCustomFields(ctx context.Context, q Queryer, doctype *string) ([]*CustomField, error) {
	rows, err := q.Query(ctx,
		"SELECT "+customFieldColumns+" FROM fb_custom_field "+
			"WHERE ($1::text IS NULL OR doctype = $1) ORDER BY doctype, created_at, name", doctype)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()
	out := []*CustomField{}
	for rows.Next() {
		f, err := scanCustomField(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, wrap(rows.Err())
}

// Effective is the metadata of a site as it is now.
type Effective struct {
	// Registry is the registry of the apps with the site's custom fields.
	Registry *core.Registry
	// Version is the value of fb_meta_version the registry was built from.
	Version int64
	// Warnings is one message per stored field that was skipped because it no longer validates.
	Warnings []string
}

// customFieldTablesExist is whether the tables of migration 0012 exist. A database that an older binary
// migrated does not have them yet, and a newer binary must still serve it (without custom fields) until
// migrate has run.
func customFieldTablesExist(ctx context.Context, q Queryer) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx,
		"SELECT to_regclass('fb_custom_field') IS NOT NULL AND to_regclass('fb_meta_version') IS NOT NULL").Scan(&exists)
	return exists, wrap(err)
}

// MetaVersion is the current fb_meta_version (0 in a database that migration 0012 has not reached).
func MetaVersion(ctx context.Context, q Queryer) (int64, error) {
	exists, err := customFieldTablesExist(ctx, q)
	if err != nil || !exists {
		return 0, err
	}
	var version int64
	err = q.QueryRow(ctx, "SELECT version FROM fb_meta_version WHERE id = 1").Scan(&version)
	return version, wrap(err)
}

// EffectiveRegistry is base plus the custom fields stored in the database: the enabled ones, or with
// includeDisabled every one (the schema sync of migrate needs the column of a disabled field too). Fields are
// appended per DocType in creation order. A field that does not validate against the registry built so far is
// skipped and reported in Effective.Warnings, naming site, the DocType and the field. base is not changed.
//
// The version and the rows are read by separate statements: for a consistent pair run it in a REPEATABLE READ
// transaction (the registry holder does), or under the migrate lock.
func EffectiveRegistry(ctx context.Context, q Queryer, base *core.Registry, site string, includeDisabled bool) (*Effective, error) {
	exists, err := customFieldTablesExist(ctx, q)
	if err != nil {
		return nil, err
	}
	if !exists {
		return &Effective{Registry: base.Clone()}, nil
	}
	version, err := MetaVersion(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx,
		"SELECT "+customFieldColumns+" FROM fb_custom_field WHERE enabled OR $1 ORDER BY doctype, created_at, name",
		includeDisabled)
	if err != nil {
		return nil, wrap(err)
	}
	var stored []*CustomField
	for rows.Next() {
		f, err := scanCustomField(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		stored = append(stored, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrap(err)
	}
	registry := base.Clone()
	var warnings []string
	for _, f := range stored {
		skipped := func(why string) string {
			return fmt.Sprintf("site `%s`: custom field `%s` of DocType `%s` is skipped: %s", site, f.Name, f.DocType, why)
		}
		field, err := core.ParseFieldJSON(decodeStoredDefinition(f.Definition))
		if err != nil {
			warnings = append(warnings, skipped(err.Error()))
			continue
		}
		if field.Name != f.Name {
			warnings = append(warnings, skipped(fmt.Sprintf("the definition names the field `%s`", field.Name)))
			continue
		}
		if err := registry.AddCustomField(f.DocType, field); err != nil {
			warnings = append(warnings, skipped(err.Error()))
		}
	}
	return &Effective{Registry: registry, Version: version, Warnings: warnings}, nil
}

// decodeStoredDefinition is the definition as a JSON tree with its numbers kept as json.Number (what
// core.ParseFieldJSON reads); a text that does not decode is nil, which parses as an error.
func decodeStoredDefinition(definition json.RawMessage) any {
	d := json.NewDecoder(bytes.NewReader(definition))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil
	}
	return v
}

func customFieldProblem(err error) error {
	var oe core.OverlayError
	if errors.As(err, &oe) {
		return &Error{Kind: ErrCustomField, Overlay: oe}
	}
	return err
}

// AddCustomField adds a custom field to doctype of the site (M10.1), in one transaction under the advisory
// lock of migrate (waiting at most lockTimeout):
//
//  1. the field is validated against the site's effective registry built inside the transaction
//     (core.Registry.AddCustomField: the prefix, no required, no read_only, no Table type, a DocType that
//     exists and is not a child, a free name, a valid Link target); a name used by a disabled field is
//     ErrCustomFieldExists;
//  2. the row is inserted (modified_by is actor);
//  3. the column is added, with the foreign key and index of a Link and the unique index of a unique field,
//     by the statements of the schema sync;
//  4. fb_meta_version is incremented.
//
// base is the registry of the apps (with their overlays). The table of the DocType must exist
// (ErrDocTypeNotMigrated). Running servers see the field within their reload interval; the caller can reload
// its own registry at once.
func AddCustomField(ctx context.Context, client TxStarter, base *core.Registry, site, doctype string, field core.DocField, actor string, lockTimeout time.Duration) (*CustomField, error) {
	tx, end, err := begin(ctx, client)
	if err != nil {
		return nil, err
	}
	defer end()
	if err := lockCustomFields(ctx, tx, lockTimeout); err != nil {
		return nil, err
	}
	if err := requireCustomFieldTables(ctx, tx); err != nil {
		return nil, err
	}
	var one int32
	err = tx.QueryRow(ctx, "SELECT 1 FROM fb_custom_field WHERE doctype = $1 AND name = $2", doctype, field.Name).Scan(&one)
	if err == nil {
		return nil, &Error{Kind: ErrCustomFieldExists, DocType: doctype, Name: field.Name}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, wrap(err)
	}
	effective, err := EffectiveRegistry(ctx, tx, base, site, false)
	if err != nil {
		return nil, err
	}
	if err := effective.Registry.AddCustomField(doctype, field); err != nil {
		return nil, customFieldProblem(err)
	}
	var present bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass($1::text) IS NOT NULL", core.TableName(doctype)).Scan(&present); err != nil {
		return nil, wrap(err)
	}
	if !present {
		return nil, &Error{Kind: ErrDocTypeNotMigrated, DocType: doctype}
	}
	definition, err := json.Marshal(core.FieldToJSON(field))
	if err != nil {
		return nil, fmt.Errorf("cannot write the definition of field %s: %w", field.Name, err)
	}
	stored, err := scanCustomField(tx.QueryRow(ctx,
		"INSERT INTO fb_custom_field (doctype, name, definition, modified_by) VALUES ($1, $2, $3::text::jsonb, $4) RETURNING "+customFieldColumns,
		doctype, field.Name, string(definition), actor))
	if err != nil {
		return nil, err
	}
	for _, step := range fieldSteps(doctype, &field) {
		if _, err := tx.Exec(ctx, step.SQL); err != nil {
			return nil, wrap(err)
		}
	}
	if err := bumpMetaVersion(ctx, tx); err != nil {
		return nil, err
	}
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return stored, nil
}

// SetCustomFieldEnabled enables or disables a custom field (M10.1): it flips the flag, sets modified_at and
// modified_by, and increments fb_meta_version, under the lock of migrate. It never drops a column or data: a
// disabled field is only left out of the effective registry, and enabling it brings back the field with its
// data. Setting the state it has already is not an error and still increments the version. An unknown field is
// ErrCustomFieldNotFound.
//
// Enabling re-validates nothing here: a field that no longer validates is skipped with a warning when the
// registry is built.
func SetCustomFieldEnabled(ctx context.Context, client TxStarter, doctype, name string, enabled bool, actor string, lockTimeout time.Duration) (*CustomField, error) {
	tx, end, err := begin(ctx, client)
	if err != nil {
		return nil, err
	}
	defer end()
	if err := lockCustomFields(ctx, tx, lockTimeout); err != nil {
		return nil, err
	}
	if err := requireCustomFieldTables(ctx, tx); err != nil {
		return nil, err
	}
	stored, err := scanCustomField(tx.QueryRow(ctx,
		"UPDATE fb_custom_field SET enabled = $3, modified_at = now(), modified_by = $4 WHERE doctype = $1 AND name = $2 RETURNING "+customFieldColumns,
		doctype, name, enabled, actor))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &Error{Kind: ErrCustomFieldNotFound, DocType: doctype, Name: name}
		}
		return nil, err
	}
	if err := bumpMetaVersion(ctx, tx); err != nil {
		return nil, err
	}
	if err := commit(ctx, tx); err != nil {
		return nil, err
	}
	return stored, nil
}

// schemaRegistry makes sure that every stored custom field, enabled or disabled, has its column (and foreign
// key and indexes): it is the registry the schema sync of migrate works from. It returns the registry (base
// plus every stored field that validates) and the warnings for the skipped ones. Idempotent: the sync adds
// only what is missing.
func schemaRegistry(ctx context.Context, q Queryer, base *core.Registry) (*Effective, error) {
	return EffectiveRegistry(ctx, q, base, "", true)
}

func lockCustomFields(ctx context.Context, tx Queryer, lockTimeout time.Duration) error {
	if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", lockTimeoutSetting(lockTimeout)); err != nil {
		return wrap(err)
	}
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", MigrateLockKey)
	return wrap(err)
}

func requireCustomFieldTables(ctx context.Context, tx Queryer) error {
	exists, err := customFieldTablesExist(ctx, tx)
	if err != nil {
		return err
	}
	if !exists {
		return &Error{Kind: ErrFrameworkNotMigrated}
	}
	return nil
}

func bumpMetaVersion(ctx context.Context, tx Queryer) error {
	_, err := tx.Exec(ctx, "UPDATE fb_meta_version SET version = version + 1 WHERE id = 1")
	return wrap(err)
}
