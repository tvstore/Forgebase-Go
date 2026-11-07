# Migrations

SQL data migrations, applied once per database by `migrate`, after the DocType schema sync.
Add one when you must change or backfill data, not to create tables for DocTypes (the sync does
that). Name files `NNNN_what.sql` (the digits are the version, `what` is the name) and embed
them in `{{name}}.go`:

    //go:embed migrations/*.sql
    var migrations embed.FS

    ... .Migrations(migrations)

Versions only increase. Never edit or remove a migration that was applied: it is refused (its
checksum is verified); add a new one instead.
