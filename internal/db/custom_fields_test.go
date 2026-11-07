package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Site custom fields (M10.1, ADR 0023) against a real PostgreSQL: adding a field (rules, column, indexes,
// version), disable and enable (data kept), the effective registry (order, skipped bad rows), migrate giving
// back a missing column, and the registry holder (poll, reload, snapshots, a swap under concurrent readers).
// Ported from crates/forgebase-db/tests/custom_fields.rs.

const cfLock = 5 * time.Second

func cfSite(t *testing.T) (*dbtest.DB, *core.Registry) {
	t.Helper()
	d := dbtest.New(t)
	base := registry(t, dbtest.Customer, dbtest.Person)
	if _, err := db.Migrate(context.Background(), d.Pool, base, db.DefaultSyncOptions()); err != nil {
		t.Fatal(err)
	}
	return d, base
}

func cfField(t *testing.T, text string) core.DocField {
	t.Helper()
	f, err := core.ParseFieldTOML(text)
	if err != nil {
		t.Fatalf("valid field: %v", err)
	}
	return f
}

func cfAdd(d *dbtest.DB, t *testing.T, base *core.Registry, doctype, text string) (*db.CustomField, error) {
	t.Helper()
	return db.AddCustomField(context.Background(), d.Pool, base, "t", doctype, cfField(t, text), "cli", cfLock)
}

func cfVersion(t *testing.T, d *dbtest.DB) int64 {
	t.Helper()
	v, err := db.MetaVersion(context.Background(), d.Pool)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func cfList(t *testing.T, d *dbtest.DB, doctype *string) []*db.CustomField {
	t.Helper()
	fields, err := db.ListCustomFields(context.Background(), d.Pool, doctype)
	if err != nil {
		t.Fatal(err)
	}
	return fields
}

func cfSetEnabled(d *dbtest.DB, doctype, name string, enabled bool, actor string) (*db.CustomField, error) {
	return db.SetCustomFieldEnabled(context.Background(), d.Pool, doctype, name, enabled, actor, cfLock)
}

func cfEffective(t *testing.T, d *dbtest.DB, base *core.Registry, site string, includeDisabled bool) *db.Effective {
	t.Helper()
	e, err := db.EffectiveRegistry(context.Background(), d.Pool, base, site, includeDisabled)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// Rust: custom_fields.rs::adding_a_field_stores_it_adds_the_column_and_bumps_the_version
func TestAddingACustomFieldStoresItAddsTheColumnAndBumpsTheVersion(t *testing.T) {
	d, base := cfSite(t)
	if v := cfVersion(t, d); v != 0 {
		t.Fatalf("version %d", v)
	}
	stored, err := cfAdd(d, t, base, "customer", "name = \"custom_vip\"\ntype = \"check\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if stored.DocType != "customer" || stored.Name != "custom_vip" || !stored.Enabled || stored.ModifiedBy != "cli" ||
		stored.TypeName() != "check" || stored.Label() != "Custom Vip" {
		t.Fatalf("%+v", stored)
	}
	found := false
	for _, c := range d.Columns("doc_customer") {
		found = found || c == dbtest.Column{"custom_vip", "boolean/YES"}
	}
	if !found {
		t.Fatalf("%v", d.Columns("doc_customer"))
	}
	if v := cfVersion(t, d); v != 1 {
		t.Fatalf("version %d", v)
	}
	listed := cfList(t, d, ptr("customer"))
	if len(listed) != 1 || listed[0].Name != "custom_vip" || string(listed[0].Definition) != string(stored.Definition) ||
		!listed[0].CreatedAt.Equal(stored.CreatedAt) || !listed[0].ModifiedAt.Equal(stored.ModifiedAt) {
		t.Fatalf("%+v", listed)
	}
	if len(cfList(t, d, ptr("person"))) != 0 {
		t.Fatal("person has no custom field")
	}
}

// Rust: custom_fields.rs::unique_and_link_fields_get_their_keys_and_indexes
func TestUniqueAndLinkCustomFieldsGetTheirKeysAndIndexes(t *testing.T) {
	d, base := cfSite(t)
	if _, err := cfAdd(d, t, base, "customer", "name = \"custom_code\"\ntype = \"data\"\nunique = true\n"); err != nil {
		t.Fatalf("unique: %v", err)
	}
	if _, err := cfAdd(d, t, base, "customer", "name = \"custom_owner\"\ntype = \"link\"\ntarget = \"person\"\n"); err != nil {
		t.Fatalf("link: %v", err)
	}
	want := func(sql, count string) {
		t.Helper()
		if rows := d.Rows(sql); len(rows) != 1 || rows[0][0] != count {
			t.Errorf("%s: %v", sql, rows)
		}
	}
	want("SELECT count(*)::text FROM pg_indexes WHERE tablename = 'doc_customer' AND indexdef LIKE '%UNIQUE%' AND indexdef LIKE '%custom_code%'", "1")
	want("SELECT count(*)::text FROM pg_constraint c JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1] "+
		"WHERE c.contype = 'f' AND c.conrelid = 'doc_customer'::regclass AND a.attname = 'custom_owner'", "1")
	want("SELECT count(*)::text FROM pg_indexes WHERE tablename = 'doc_customer' AND indexdef LIKE '%custom_owner%'", "1")
}

// Rust: custom_fields.rs::the_rules_are_enforced_and_nothing_is_written_on_a_refusal
func TestTheRulesOfACustomFieldAreEnforcedAndNothingIsWrittenOnARefusal(t *testing.T) {
	d, base := cfSite(t)
	for _, c := range []struct {
		doctype, text string
		kind          core.OverlayErrorKind
	}{
		{"customer", "name = \"vip\"\ntype = \"check\"\n", core.OverlayMissingPrefix},
		{"customer", "name = \"custom_a\"\ntype = \"check\"\nrequired = true\n", core.OverlayRequired},
		{"customer", "name = \"custom_a\"\ntype = \"check\"\nread_only = true\n", core.OverlayReadOnly},
		{"customer", "name = \"custom_a\"\ntype = \"table\"\ntarget = \"person\"\n", core.OverlayTableField},
		{"customer", "name = \"custom_a\"\ntype = \"link\"\ntarget = \"ghost\"\n", core.OverlayInvalid},
		{"customer", "name = \"email\"\ntype = \"check\"\n", core.OverlayMissingPrefix},
		{"ghost", "name = \"custom_a\"\ntype = \"check\"\n", core.OverlayUnknownDocType},
	} {
		_, err := cfAdd(d, t, base, c.doctype, c.text)
		var e *db.Error
		if !errors.As(err, &e) || e.Kind != db.ErrCustomField || e.Overlay.Kind != c.kind {
			t.Errorf("%s: %v", c.text, err)
		}
	}
	if len(cfList(t, d, nil)) != 0 || cfVersion(t, d) != 0 {
		t.Fatal("something was written")
	}
}

// Rust: custom_fields.rs::a_duplicate_is_refused_also_when_the_field_is_disabled
func TestADuplicateCustomFieldIsRefusedAlsoWhenTheFieldIsDisabled(t *testing.T) {
	d, base := cfSite(t)
	if _, err := cfAdd(d, t, base, "customer", "name = \"custom_vip\"\ntype = \"check\"\n"); err != nil {
		t.Fatal(err)
	}
	exists := func() {
		t.Helper()
		_, err := cfAdd(d, t, base, "customer", "name = \"custom_vip\"\ntype = \"int\"\n")
		var e *db.Error
		if !errors.As(err, &e) || e.Kind != db.ErrCustomFieldExists {
			t.Fatalf("%v", err)
		}
	}
	exists()
	if _, err := cfSetEnabled(d, "customer", "custom_vip", false, "cli"); err != nil {
		t.Fatal(err)
	}
	exists()
}

// Rust: custom_fields.rs::adding_to_a_doctype_whose_table_does_not_exist_is_refused
func TestAddingACustomFieldToADocTypeWhoseTableDoesNotExistIsRefused(t *testing.T) {
	d := dbtest.Migrated(t)
	base := registry(t, dbtest.Customer)
	_, err := cfAdd(d, t, base, "customer", "name = \"custom_a\"\ntype = \"check\"\n")
	var e *db.Error
	if !errors.As(err, &e) || e.Kind != db.ErrDocTypeNotMigrated {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(err.Error(), "run `forgebase migrate` first") {
		t.Fatalf("%v", err)
	}
	if len(cfList(t, d, nil)) != 0 {
		t.Fatal("something was written")
	}
}

// Rust: custom_fields.rs::disable_hides_the_field_and_keeps_the_column_and_the_data
func TestDisablingACustomFieldHidesItAndKeepsTheColumnAndTheData(t *testing.T) {
	d, base := cfSite(t)
	if _, err := cfAdd(d, t, base, "customer", "name = \"custom_vip\"\ntype = \"check\"\n"); err != nil {
		t.Fatal(err)
	}
	d.Exec("INSERT INTO doc_customer (id, name, owner, created_at, modified_at, modified_by, email, custom_vip) " +
		"VALUES (gen_random_uuid(), 'c1', 'x', now(), now(), 'x', 'a@b.c', true)")

	disabled, err := cfSetEnabled(d, "customer", "custom_vip", false, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled || disabled.ModifiedBy != "ann" || disabled.ModifiedAt.Before(disabled.CreatedAt) {
		t.Fatalf("%+v", disabled)
	}
	if v := cfVersion(t, d); v != 2 {
		t.Fatalf("version %d", v)
	}
	if cfEffective(t, d, base, "t", false).Registry.Get("customer").Field("custom_vip") != nil {
		t.Fatal("the disabled field is in the registry")
	}
	if cfEffective(t, d, base, "t", true).Registry.Get("customer").Field("custom_vip") == nil {
		t.Fatal("the disabled field is not in the registry with include_disabled")
	}
	if rows := d.Rows("SELECT custom_vip::text FROM doc_customer"); len(rows) != 1 || rows[0][0] != "true" {
		t.Fatalf("%v", rows)
	}

	enabled, err := cfSetEnabled(d, "customer", "custom_vip", true, "ann")
	if err != nil || !enabled.Enabled {
		t.Fatalf("%v %+v", err, enabled)
	}
	if v := cfVersion(t, d); v != 3 {
		t.Fatalf("version %d", v)
	}
	if cfEffective(t, d, base, "t", false).Registry.Get("customer").Field("custom_vip") == nil {
		t.Fatal("the enabled field is not in the registry")
	}

	_, err = cfSetEnabled(d, "customer", "custom_nope", true, "ann")
	var e *db.Error
	if !errors.As(err, &e) || e.Kind != db.ErrCustomFieldNotFound {
		t.Fatalf("%v", err)
	}
	if v := cfVersion(t, d); v != 3 {
		t.Fatalf("version %d", v)
	}
}

// Rust: custom_fields.rs::the_effective_registry_appends_in_creation_order_per_doctype
func TestTheEffectiveRegistryAppendsInCreationOrderPerDocType(t *testing.T) {
	d, base := cfSite(t)
	for _, c := range [][2]string{
		{"customer", "name = \"custom_z\"\ntype = \"int\"\n"},
		{"person", "name = \"custom_p\"\ntype = \"int\"\n"},
		{"customer", "name = \"custom_a\"\ntype = \"int\"\n"},
	} {
		if _, err := cfAdd(d, t, base, c[0], c[1]); err != nil {
			t.Fatal(err)
		}
	}
	effective := cfEffective(t, d, base, "t", false)
	if effective.Version != 3 || len(effective.Warnings) != 0 {
		t.Fatalf("%d %v", effective.Version, effective.Warnings)
	}
	fields := effective.Registry.Get("customer").Fields
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	n := len(names)
	if names[n-2] != "custom_z" || names[n-1] != "custom_a" {
		t.Fatalf("%v", names)
	}
	// The base registry is not touched.
	if base.Get("customer").Field("custom_z") != nil {
		t.Fatal("the base registry changed")
	}
}

// Rust: custom_fields.rs::a_stored_field_that_no_longer_validates_is_skipped_with_a_warning
func TestAStoredCustomFieldThatNoLongerValidatesIsSkippedWithAWarning(t *testing.T) {
	d, base := cfSite(t)
	if _, err := cfAdd(d, t, base, "customer", "name = \"custom_ok\"\ntype = \"check\"\n"); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][3]string{
		// Its Link target left the apps.
		{"customer", "custom_gone", `{"name":"custom_gone","type":"link","target":"ghost"}`},
		// Not a field definition at all.
		{"customer", "custom_junk", `{"name":"custom_junk","type":"nope"}`},
		// Its DocType left the apps.
		{"vanished", "custom_x", `{"name":"custom_x","type":"check"}`},
		// Violates a rule of M10.1 (written around the framework).
		{"customer", "custom_req", `{"name":"custom_req","type":"check","required":true}`},
		// The definition names another field than the row.
		{"customer", "custom_mismatch", `{"name":"custom_other","type":"check"}`},
	} {
		d.Exec(fmt.Sprintf("INSERT INTO fb_custom_field (doctype, name, definition, modified_by) VALUES ('%s', '%s', '%s', 'sql')", c[0], c[1], c[2]))
	}
	effective := cfEffective(t, d, base, "shop", false)
	if len(effective.Warnings) != 5 {
		t.Fatalf("%v", effective.Warnings)
	}
	for _, w := range effective.Warnings {
		if !strings.HasPrefix(w, "site `shop`: custom field `custom_") {
			t.Errorf("%s", w)
		}
	}
	customer := effective.Registry.Get("customer")
	if customer.Field("custom_ok") == nil {
		t.Fatal("the valid field is missing")
	}
	for _, gone := range []string{"custom_gone", "custom_junk", "custom_req", "custom_mismatch", "custom_other"} {
		if customer.Field(gone) != nil {
			t.Errorf("%s is in the registry", gone)
		}
	}
}

// Rust: custom_fields.rs::migrate_gives_back_a_missing_column_enabled_or_disabled
func TestMigrateGivesBackAMissingCustomColumnEnabledOrDisabled(t *testing.T) {
	d, base := cfSite(t)
	ctx := context.Background()
	if _, err := cfAdd(d, t, base, "customer", "name = \"custom_a\"\ntype = \"int\"\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := cfAdd(d, t, base, "customer", "name = \"custom_b\"\ntype = \"data\"\nunique = true\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := cfSetEnabled(d, "customer", "custom_b", false, "cli"); err != nil {
		t.Fatal(err)
	}
	d.Exec("ALTER TABLE doc_customer DROP COLUMN custom_a, DROP COLUMN custom_b")

	report, err := db.Migrate(ctx, d.Pool, base, db.DefaultSyncOptions())
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, c := range d.Columns("doc_customer") {
		have[c[0]] = true
	}
	if !have["custom_a"] || !have["custom_b"] {
		t.Fatalf("%v", have)
	}
	mentioned := false
	for _, s := range report.Sync.Steps {
		mentioned = mentioned || strings.Contains(s.String(), "custom_a")
	}
	if !mentioned {
		t.Fatalf("%v", report.Sync.Steps)
	}
	// The unique index of the disabled field is back too.
	if rows := d.Rows("SELECT count(*)::text FROM pg_indexes WHERE tablename = 'doc_customer' AND indexdef LIKE '%UNIQUE%custom_b%'"); rows[0][0] != "1" {
		t.Fatalf("%v", rows)
	}
	// Idempotent: nothing left to do.
	again, err := db.Migrate(ctx, d.Pool, base, db.DefaultSyncOptions())
	if err != nil || len(again.Sync.Steps) != 0 {
		t.Fatalf("%v %v", err, again)
	}
	// A field that does not validate is skipped, and migrate still works.
	d.Exec(`INSERT INTO fb_custom_field (doctype, name, definition, modified_by) VALUES ('customer', 'custom_bad', '{"name":"custom_bad","type":"link","target":"ghost"}', 'sql')`)
	withBad, err := db.Migrate(ctx, d.Pool, base, db.DefaultSyncOptions())
	if err != nil {
		t.Fatalf("migrate with a bad row: %v", err)
	}
	if len(withBad.CustomFieldWarnings) != 1 || !strings.Contains(withBad.CustomFieldWarnings[0], "custom field `custom_bad` of DocType `customer` is skipped") {
		t.Fatalf("%v", withBad.CustomFieldWarnings)
	}
}

func hasField(r *core.Registry, doctype, field string) bool {
	return r.Get(doctype).Field(field) != nil
}

// Rust: custom_fields.rs::the_holder_serves_a_snapshot_and_rebuilds_on_a_version_change
func TestTheHolderServesASnapshotAndRebuildsOnAVersionChange(t *testing.T) {
	d, base := cfSite(t)
	ctx := context.Background()
	holder, err := db.LoadRegistryHolder(ctx, "t", base, d.Pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := holder.Snapshot()
	if hasField(before, "customer", "custom_vip") {
		t.Fatal("the field exists already")
	}
	poll := func() bool {
		t.Helper()
		changed, err := holder.Poll(ctx, d.Pool)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	if poll() {
		t.Fatal("nothing changed")
	}
	if _, err := cfAdd(d, t, base, "customer", "name = \"custom_vip\"\ntype = \"check\"\n"); err != nil {
		t.Fatal(err)
	}
	// The old snapshot is untouched; a new one sees the field after the poll.
	if hasField(holder.Snapshot(), "customer", "custom_vip") {
		t.Fatal("the holder changed without a poll")
	}
	if !poll() {
		t.Fatal("version changed")
	}
	if !hasField(holder.Snapshot(), "customer", "custom_vip") {
		t.Fatal("the poll did not bring the field")
	}
	if hasField(before, "customer", "custom_vip") {
		t.Fatal("the old snapshot changed")
	}
	if poll() {
		t.Fatal("nothing changed again")
	}
	if _, err := cfSetEnabled(d, "customer", "custom_vip", false, "cli"); err != nil {
		t.Fatal(err)
	}
	if v, err := holder.Reload(ctx, d.Pool); err != nil || v != 2 {
		t.Fatalf("%d %v", v, err)
	}
	if hasField(holder.Snapshot(), "customer", "custom_vip") {
		t.Fatal("the disabled field is back")
	}
	if holder.Base() != base {
		t.Fatal("the base changed")
	}
}

// Rust: custom_fields.rs::a_database_without_migration_12_serves_the_registry_of_the_apps
func TestADatabaseWithoutMigration12ServesTheRegistryOfTheApps(t *testing.T) {
	d := dbtest.New(t)
	base := registry(t, dbtest.Customer)
	// Not even the framework tables: the holder still builds, from the apps alone.
	holder, err := db.LoadRegistryHolder(context.Background(), "t", base, d.Pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	if holder.Snapshot().Get("customer") == nil {
		t.Fatal("no customer")
	}
	if changed, err := holder.Poll(context.Background(), d.Pool); err != nil || changed {
		t.Fatalf("%v %v", changed, err)
	}
}

// Rust: custom_fields.rs::a_failed_poll_keeps_the_current_registry
func TestAFailedPollKeepsTheCurrentRegistry(t *testing.T) {
	d, base := cfSite(t)
	ctx := context.Background()
	holder, err := db.LoadRegistryHolder(ctx, "t", base, d.Pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := holder.Snapshot()
	d.Pool.Close()
	if _, err := holder.Poll(ctx, d.Pool); err == nil {
		t.Fatal("the poll of a closed pool succeeded")
	}
	if _, err := holder.Reload(ctx, d.Pool); err == nil {
		t.Fatal("the reload of a closed pool succeeded")
	}
	if holder.Snapshot() != before {
		t.Fatal("the registry changed")
	}
}

// Rust: custom_fields.rs::swapping_under_concurrent_readers_never_shows_a_half_built_registry
func TestSwappingUnderConcurrentReadersNeverShowsAHalfBuiltRegistry(t *testing.T) {
	d, base := cfSite(t)
	ctx := context.Background()
	holder, err := db.LoadRegistryHolder(ctx, "t", base, d.Pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"custom_a", "custom_b"} {
		if _, err := cfAdd(d, t, base, "customer", "name = \""+n+"\"\ntype = \"int\"\n"); err != nil {
			t.Fatal(err)
		}
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	seen := make([]uint64, 8)
	problems := make(chan string, 8)
	for i := range seen {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				snapshot := holder.Snapshot()
				// A consistent registry: the fields of one rebuild, in order, whatever the version.
				var custom []string
				for _, f := range snapshot.Get("customer").Fields {
					if strings.HasPrefix(f.Name, "custom_") {
						custom = append(custom, f.Name)
					}
				}
				switch strings.Join(custom, ",") {
				case "", "custom_a", "custom_a,custom_b", "custom_b":
				default:
					select {
					case problems <- strings.Join(custom, ","):
					default:
					}
				}
				seen[i]++
			}
		}()
	}
	for round := range 20 {
		if _, err := cfSetEnabled(d, "customer", "custom_a", round%2 == 0, "cli"); err != nil {
			t.Fatal(err)
		}
		if _, err := holder.Reload(ctx, d.Pool); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	wg.Wait()
	close(problems)
	for p := range problems {
		t.Errorf("a reader saw %q", p)
	}
	for i, n := range seen {
		if n == 0 {
			t.Errorf("reader %d saw nothing", i)
		}
	}
}

// Not in Rust: the warning about a skipped stored field is written at the point Rust writes it, before the
// sync, so it is shown when the sync that follows fails.
func TestTheCustomFieldWarningIsWrittenBeforeASyncThatFails(t *testing.T) {
	d, base := cfSite(t)
	d.Exec(`INSERT INTO fb_custom_field (doctype, name, definition, modified_by) VALUES ('customer', 'custom_bad', '{"name":"custom_bad","type":"link","target":"ghost"}', 'sql')`)
	// A column of another type than the DocType says: the sync refuses it.
	d.Exec("ALTER TABLE doc_customer ALTER COLUMN email TYPE integer USING NULL")
	var out strings.Builder
	options := db.DefaultSyncOptions()
	options.Warn = &out
	_, err := db.Migrate(context.Background(), d.Pool, base, options)
	if err == nil {
		t.Fatal("the sync was not refused")
	}
	want := "warning: site ``: custom field `custom_bad` of DocType `customer` is skipped:"
	if !strings.HasPrefix(out.String(), want) || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("%q (error %v)", out.String(), err)
	}
}
