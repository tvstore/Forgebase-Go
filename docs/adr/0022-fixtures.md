# 0022 Fixtures

- Status: Accepted (owner decision 10 of the M9 plan, 2026-10-30, *"setuju semua rencana mu"*; the design below is the specification of the M9.4 working session and is reviewed with its pull request).
- Date: 2026-11-01

## Context

An app often needs records to exist on every site that installs it: the customer groups of a sales app, the territories, a default price list. Without a feature they are written as SQL data migrations ([ADR 0012](0012-app-model-and-public-api-boundary.md)), which has three problems. A migration is **append-only**, so "change the wording of a record" is a new migration for every change. The SQL has to know the **columns** of the DocType (and the generated `id`, the standard columns and the link ids), so it breaks when a field is added. And it **bypasses** the document pipeline: no hooks, no validation, no naming rules, no webhooks.

The [feature matrix](../blueprint/feature-matrix.md) listed fixtures as planned and asked, as open question 11, what happens when a site edited a record that the app now ships in a new version. The owner decided (decision 10 of the M9 plan): **fixtures are inserted if missing, updated only when the fixture's record changed, and never deleted.** This ADR records the design that follows from it.

## Decision

### Format and API

`App::fixtures(fixtures!["fixtures/customer_group.json"])` embeds JSON files with `include_str!`, like `doctypes!` and `migrations!`; `fixtures!` gives a `&'static [FixtureFile]` (`FixtureFile` is public, `#[non_exhaustive]`, with a `__new` constructor for the macro and a `new` for tests).

One file holds the records of **one DocType**:

```json
{"doctype": "customer_group", "records": [{"title": "Retail"}, {"title": "Wholesale", "description": "..."}]}
```

A record has the shape of the body of `POST /api/v1/resource/<doctype>`: Table fields are arrays of row objects, a Link is the name of the target. Any other key in the file is refused.

### Only field naming, and why

A record is identified by **its DocType and its name**, and the name is the value of the naming field. That is the only naming that gives the same identity on every site and on every run:

- **Random naming** generates a different name each time: a second `migrate` could not tell that the record exists.
- **Series naming** hands out numbers per database: the same record would be `INV-0001` on one site and `INV-0007` on another, and a fixture that is applied twice would create two documents.

A DocType with random or series naming is a startup error that says this. Two more kinds of DocType are refused, because a fixture **update** must not fight another state machine:

- **Submittable DocTypes** have a lifecycle (draft, submitted, cancelled) in which a submitted document cannot be updated. (A submittable DocType cannot use field naming today, so this check is a second line of defence that also gives a clearer message than "random naming".)
- **DocTypes with a workflow** own the state of their documents: a fixture that wrote fields of a document in `Approved` would bypass the roles and rules of the transition.

**Child DocTypes** have no documents or names of their own: ship the parent's fixture with the rows in its table field. The DocType must belong to the app or to an app it depends on (directly or not), the rule that already holds for Link fields and hooks.

### The checksum and the canonical JSON

`fb_fixture` (framework migration 0011) has one row per record: `(doctype, name)` is the primary key, with `app`, `checksum` and `applied_at`. The checksum is the **SHA-256, as lower-case hex, of the record's canonical JSON**. A changed checksum would make the next `migrate` update every record and overwrite the site's edits, so the canonical text is **produced entirely by Forgebase**: it depends neither on the features of `serde_json` that an app's dependency graph enables (`preserve_order`, `arbitrary_precision`, `float_roundtrip`) nor on its version (its number formatter has changed between releases). The form:

- compact, no white space; arrays in order; the keys of every object sorted by their bytes;
- strings in double quotes, with `"`, `\`, backspace, form feed, newline, carriage return and tab escaped as `\"`, `\\`, `\b`, `\f`, `\n`, `\r`, `\t`, any other control character below U+0020 as `\u00xx` (lower-case hex), everything else as is;
- an integer that fits `i64` or `u64` as its decimal digits;
- any other number as an `f64`: the shortest digits that read back as the same value (Rust's `{:e}`, which is deterministic), written as a plain decimal when the decimal exponent is from -5 to 15 (`0.00001`, `12.5`, `100.0`: always a `.` and at least one fractional digit) and otherwise as `d.ddde+N` or `d.ddde-N` (`1.5e+17`, `6.0e-22`).

The parser is still `serde_json`'s, and its features can change how a number **parses**. So numbers are **restricted at startup**. A record with a negative zero (`-0`, `-0.0`, or a negative number that underflows to it), a number that is not finite, a non-zero number that underflows to zero, an integer outside the `i64`/`u64` range, or a number written as a float whose shortest `f64` text has more than 15 significant digits or a decimal exponent outside ±15 is a startup error ("cannot be represented the same way in every build; write it as a string, or with fewer significant digits"; money and other exact values belong in a Decimal field, which is a JSON string). The real guarantee is this: **if two builds both accept a number, its canonical text is the same.** A number that one build accepts may be a startup error in another (a build without `arbitrary_precision` cannot see that `1e-400` underflowed, and accepts it as `0.0`; a build with `arbitrary_precision` refuses it). That is fail-closed: a refusal at startup, never a silent overwrite. Accepted floats are below 2^63 in magnitude (an integral float at or above it is refused as an integer outside the 64-bit range); with the exponent window of ±15 they are in fact below 10^16. The window was narrowed from ±22 to ±15 so that it stays inside the range where the plain form is written. The test is applied to the parsed value, so a long or very small input (for example a 15-digit value around `1e-12`) may be refused by a default build, whose parser rounds it to a neighbouring `f64`, and accepted by a `float_roundtrip` or `arbitrary_precision` build; that is fail-closed too.

White space and the order of keys in the file are therefore not a change; a changed value, an added or removed key, and the order of table rows are. Unit tests build a map out of order and compare the text and the checksum with a sorted construction, and have golden values for the float forms and their edges, for the escapes, and for the refused and accepted numbers. The SHA-256 comes from `sha2`, already a direct dependency of `forgebase` since M9.2 (the webhook signature): **no new dependency**.

The `forgebase` crate reads and checks the files, computes the checksums and hands `forgebase-db` parsed documents (as import does); `forgebase-db` applies them. `forgebase-db` does not parse JSON by the rules of the HTTP layer, which it cannot depend on.

### The conflict table

For each record, with the checksum of the record as shipped now:

| `fb_fixture` row | Document | Action | Reported as |
|---|---|---|---|
| none | missing | **insert** as `Caller::System` (actor `fixture:<app>`), then insert the row | inserted |
| none | exists (the site made it before the app shipped it) | **leave it as it is**; insert the row with the checksum (it is adopted) | kept |
| same checksum | any | **nothing**: the document is not even read. A site edit **or deletion** stands until the app ships a new version of that record | unchanged |
| other checksum | exists | **update**, sending **only the fields the record has** (a table the record has replaces the rows, as in the API), with the version read in the same transaction; then update the row | updated |
| other checksum | missing (the site deleted it) | **insert** again: the app shipped a new version; then update the row | inserted |
| row exists, record no longer in any file | any | **nothing**: never delete, and the row stays | not reported |

Consequences worth stating plainly:

- **A site edit survives until the app ships a new version of that record**, and then the fields the record has are overwritten with the app's. Fields the record does not have are never touched. An app that wants a site to be free to change a value should leave that field out of the record, or ship the record once and never change it.
- **A record the site deleted stays deleted** until the app changes it. A site that does not want a record deletes it; an app that really wants it back changes the record (a changed description is enough).
- **The naming field is never sent in an update**: it is the identity and cannot be changed by the pipeline. A hook that changes it on an insert is an error: the app's fixtures fail with `a hook changed the naming field of fixture record X to Y; fixtures need a stable name`, because the record would be tracked under a name its document does not have.
- A record that **leaves a file** keeps its document and its row. If it returns later with different content, it is an update.
- **A record moves between apps** (the same DocType and name in another app's file): the row's `app` follows the app that last **wrote** it; a moved record whose content is unchanged writes nothing, so it stays attributed to the old app until its content changes. Two apps shipping the same record at the same time is a startup error.

### Through the pipeline, in one transaction per app

Fixtures are applied by `migrate` **after the app migrations** (which may create what a record links to), the workflow backfill and the schedule sync, **in app order** (dependencies first), then file order, then record order. Each record goes through `forgebase_db::insert` or `update` as the system caller, so **hooks, validation, naming, links and webhooks run as for any system write**. A fixture insert or update fires the webhooks of its DocType like any other write: a site that has a webhook on a fixture DocType gets one delivery per record written (and none for a record that is unchanged). The `fixture:<app>` actor is recorded as `owner` and `modified_by`.

All the fixtures of **one app run in one transaction**. Any error (a record the pipeline refuses, a link to a document that does not exist, a hook that rejects) rolls back that app's fixtures, including the `fb_fixture` rows, and fails `migrate` with `fixtures of app <app> were not applied: <file>: record <n> (<name>): <reason>`; the command prints the report of what was applied first, as for a failed report check. (If the `COMMIT` itself fails, the outcome is unknown and the message says so; running `migrate` again is safe, because the checksums decide.) What is committed before stays: the framework migrations, the schema sync, the app migrations, and the fixtures of apps earlier in the order. After the fix, `migrate` runs again; nothing is half applied. The apps are not one transaction together because the migrations before them are not either, and an app's fixtures only depend on apps before it.

### Startup checks

Everything that needs no database is checked when the binary starts, with the other app problems, each naming the app, the file and the record: invalid JSON or a wrong shape; an unknown DocType; a DocType of an app that is not a dependency; the DocType rules above; a record that is not an object or has no non-empty text value for the naming field; a key that is not a field of the DocType or is `read_only` (also in the rows of a Table field, against the child DocType); a value of the wrong JSON type for its field; a number that could parse differently in another build (above); a duplicate name in the app; the same DocType and name in another app (both are named); two files of one app for the same DocType (use one file per DocType).

Whether a value is **valid** (a required field, a length, a Select option, a link that exists) is decided at apply time by the pipeline, not at startup: a `before_insert` or `validate` hook may fill or change fields, and a check without the hooks would refuse records that are valid.

### Dry run

`migrate --dry-run` reports, per app, what would be inserted, updated and kept, and how many records are unchanged, **without writing** and without running the pipeline: the plan reads `fb_fixture` and the DocType tables, and a table that does not exist yet (the dry-run sync is rolled back) counts as "no document". While framework migrations are pending, the plan cannot read `fb_fixture` and the report says so, as it does for the schema sync. Nothing is validated in a dry run, so a dry run can say "would insert" for a record that the real run then refuses.

### Multi-site

Each site applies its own fixtures and keeps its own `fb_fixture`. The apps of a site are composed from the apps it installs, so **fixtures of an app that is not installed on a site are not applied there**. `forgebase::testing::TestDb::migrate` runs the same code, so a test database has the fixtures of the apps it migrates, and `Migrated::fixtures` gives the counts (`fixtures_planned` is false in a dry run on a database whose framework migrations are pending, where the counts are zero).

### Non-goals

- **Deleting records through fixtures**, and exporting records from a database into fixture files.
- **A merge of site edits with the app's new version.** The rule is per record and per field the record has: the app's value wins when the app changes the record.
- Fixtures for random or series naming, submittable DocTypes, DocTypes with a workflow, child DocTypes, or users, roles and permissions (a role is not a DocType here).
- Running a fixture again by hand. Change the record, or delete its row in `fb_fixture` (SQL) to force an adoption check on the next `migrate`.

## Alternatives considered

- **SQL data migrations.** Rejected as the mechanism: append-only (every wording change is a new file), they need the column names and the generated ids of the DocType, and they bypass hooks, validation, naming and webhooks. They remain the tool for data changes that are not "records the app owns".
- **Always overwrite** (apply every record at every `migrate`). Rejected: it destroys every site edit at the next deploy, writes (and fires webhooks) for every record every time, and makes `migrate` slow for no reason.
- **Insert only** (never update). Rejected: the app could never correct or extend a record it ships. This is the weaker half of decision 10.
- **A three-way merge** (the previous fixture, the new fixture and the site's document, field by field). Rejected: it needs the previous version of every record stored, a rule for every conflicting field, and a way to explain a merge to the person who runs `migrate`. The checksum per record plus "only the fields the record has" gives a rule that can be explained in one sentence.
- **A checksum of the document instead of the fixture.** Rejected: it would treat every site edit as a conflict and cannot tell "the app changed" from "the site changed".
- **Random or series naming with a hidden key.** Rejected: it adds a second identity (an extra column or field) to every such DocType for a case the field naming already covers.
- **One transaction for all apps.** Rejected: the migrations before the fixtures are not in it either, and one failing app would undo the fixtures of the apps before it for no gain.

## Consequences

- **Framework migration 0011** creates `fb_fixture`. A database is upgraded by the next `migrate`. The new migration is checksummed like the others.
- **Public API (0.x, additive):** `App::fixtures`, `fixtures!`, `FixtureFile`; and `testing::Migrated::fixtures` with `testing::AppliedFixtures` (feature `testing`). See [ADR 0012, public surface update (M9.4)](0012-app-model-and-public-api-boundary.md#public-surface-update-m94).
- **`forgebase-db` (internal):** `AppSpec` has a `fixtures` field and `MigrateReport` has three new fields, `fixtures`, `fixtures_planned` and `fixtures_failure` (a refused fixture is returned in the report, not as an `Err`, so that `migrate` can print what was applied first; a caller must check it).
- **`migrate` prints one line per app with fixtures**, for example `fixtures: sales: 2 inserted, 1 updated, 0 kept, 5 unchanged`, and in a dry run `fixtures: sales: would insert 2, update 1, keep 0, 5 unchanged`.
- **A new failure mode of `migrate`:** a record that the pipeline refuses fails the run after everything else is applied. It is reported with the app, file, record and reason and fixed by changing the file; `migrate` can be run again.
- **Webhooks fire for fixture writes.** A site that does not want that disables the webhook around a deploy.
- **No new dependency.**
