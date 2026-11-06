// Code generated from the Rust crate's src/args.rs by a one-off script, then maintained by hand.
// Help texts are the doc comments of the Rust command line, joined as clap derive joins them.

package app

// rootCommand is the whole command tree of `forgebase` (the Rust `Cli` and its subcommands).
func rootCommand() *command {
	return &command{
		name: "forgebase", about: "Forgebase command-line tool", longAbout: "Forgebase command-line tool.\n\nDatabase commands work on one site, which is one database. Without a config file there is one site, `default`, and its connection URL comes from the `FORGEBASE_DATABASE_URL` environment variable (`postgres://user:password@host:port/database`). With a config file (`--config`) the sites are listed in it, each naming the environment variable that holds its URL.",
		args: []argSpec{
			{id: "config", long: "config", valueName: "PATH", kind: kindPath, help: "The config file (`forgebase.toml`) that lists the sites. Also read from the environment variable `FORGEBASE_CONFIG`; this option wins. It is never looked for in the current directory. Without one, the only site is `default`"},
			{id: "site", long: "site", valueName: "NAME", help: "The site a database command works on. Required when the config file has several sites, except for `serve`, which serves every site without it. Optional with one site, and without a config file (where only `default` exists)"},
		},
		subs: []*command{
			{name: "migrate", about: "Apply framework migrations, sync the DocType schema (additive changes only) and apply the SQL data migrations of the apps installed on the site (all the binary's apps unless the site lists `apps`). The apps recorded in the database but not installed are reported as a warning; their tables and data are kept. The schedules of apps that are not installed are deleted",
				args: []argSpec{
					doctypesArg(),
					{id: "dry_run", long: "dry-run", flag: true, help: "Print what would change and keep nothing"},
					{id: "lock_timeout_ms", long: "lock-timeout-ms", valueName: "MS", kind: kindU64, def: "5000", hasDefault: true, help: "Longest wait for a database lock, in milliseconds"},
				},
			},
			{name: "doc", about: "Insert, read, list, update, delete, submit, cancel, amend and (with a workflow) transition documents. These commands run as the trusted system context: no permission rules apply (see --actor)",
				subs: []*command{
					{name: "insert", about: "Insert a document and print it as stored",
						args: []argSpec{
							{id: "doctype", positional: true, valueName: "DOCTYPE", required: true, help: "DocType name"},
							doctypesArg(),
							dataArg(),
							actorArg(),
						},
					},
					{name: "get", about: "Print a document",
						args: []argSpec{
							{id: "doctype", positional: true, valueName: "DOCTYPE", required: true, help: "DocType name"},
							{id: "name", positional: true, valueName: "NAME", required: true, help: "Document name"},
							doctypesArg(),
						},
					},
					{name: "list", about: "Print a page of documents as a JSON array, filtered and sorted. Child table rows are not listed. The default page is 20 documents, at most 500", longAbout: "Print a page of documents as a JSON array, filtered and sorted. Child table rows are not listed. The default page is 20 documents, at most 500.\n\n--filter is a JSON array of conditions, combined with AND, each `{\"field\": F, \"op\": OP, \"value\": V}`. OP is one of `=`, `!=`, `<`, `<=`, `>`, `>=`, `in` (V is an array), `like` (text fields only; V is a pattern with % and _) or `is null` / `is not null` (no `value`). V is written as in `--data`. Example: --filter '[{\"field\":\"status\",\"op\":\"=\",\"value\":\"open\"},{\"field\":\"total\",\"op\":\">\",\"value\":\"100.00\"}]'",
						args: []argSpec{
							{id: "doctype", positional: true, valueName: "DOCTYPE", required: true, help: "DocType name"},
							doctypesArg(),
							{id: "filter", long: "filter", valueName: "JSON", help: "A JSON array of filters, see above"},
							{id: "sort", long: "sort", valueName: "FIELD[:desc]", multiple: true, help: "Sort key `FIELD` or `FIELD:desc` (also `FIELD:asc`); repeat for several. `name` is always the last key"},
							{id: "limit", long: "limit", valueName: "N", kind: kindU32, help: "Page size, 1 to 500 (default 20)"},
							{id: "offset", long: "offset", valueName: "N", kind: kindU64, help: "Documents to skip (default 0)"},
						},
					},
					{name: "update", about: "Change fields of a document and print it as stored. Fails if the document's version is not the one given with --version",
						args: []argSpec{
							{id: "doctype", positional: true, valueName: "DOCTYPE", required: true, help: "DocType name"},
							{id: "name", positional: true, valueName: "NAME", required: true, help: "Document name"},
							versionArg(),
							doctypesArg(),
							dataArg(),
							actorArg(),
						},
					},
					{name: "submit", about: "Submit a draft of a submittable DocType (it becomes locked) and print it as stored. Fails if the document's version is not the one given with --version",
						args: []argSpec{
							{id: "doctype", positional: true, valueName: "DOCTYPE", required: true, help: "DocType name"},
							{id: "name", positional: true, valueName: "NAME", required: true, help: "Document name"},
							versionArg(),
							doctypesArg(),
							actorArg(),
						},
					},
					{name: "cancel", about: "Cancel a submitted document and print it as stored. Fails if the document's version is not the one given with --version",
						args: []argSpec{
							{id: "doctype", positional: true, valueName: "DOCTYPE", required: true, help: "DocType name"},
							{id: "name", positional: true, valueName: "NAME", required: true, help: "Document name"},
							versionArg(),
							doctypesArg(),
							actorArg(),
						},
					},
					{name: "transition", about: "Take a workflow transition of a document and print it as stored (M8.1, ADR 0015): ACTION is the action of the transition from the document's current state, for example `Approve`. For a DocType with a workflow only; on one, plain `submit` and `cancel` are refused. Fails if the document's version is not the one given with --version. Like every `doc` command it runs as the trusted system context: the roles of the transition and the caller's rights are **not** checked (see --actor), but the `before_transition` hooks and the validation run",
						args: []argSpec{
							{id: "doctype", positional: true, valueName: "DOCTYPE", required: true, help: "DocType name"},
							{id: "name", positional: true, valueName: "NAME", required: true, help: "Document name"},
							{id: "action", positional: true, valueName: "ACTION", required: true, help: "The action, as written in the DocType's workflow (case-sensitive)"},
							versionArg(),
							doctypesArg(),
							actorArg(),
						},
					},
					{name: "amend", about: "Amend a cancelled document: create a new draft named `<name>-<n>` with a copy of its values and rows, and print it. Takes no data; --version is the cancelled document's",
						args: []argSpec{
							{id: "doctype", positional: true, valueName: "DOCTYPE", required: true, help: "DocType name"},
							{id: "name", positional: true, valueName: "NAME", required: true, help: "Document name (of the cancelled document)"},
							versionArg(),
							doctypesArg(),
							actorArg(),
						},
					},
					{name: "delete", about: "Delete a document (prints nothing). Fails if the document's version is not the one given with --version, or if other documents still link to it",
						args: []argSpec{
							{id: "doctype", positional: true, valueName: "DOCTYPE", required: true, help: "DocType name"},
							{id: "name", positional: true, valueName: "NAME", required: true, help: "Document name"},
							versionArg(),
							doctypesArg(),
							actorArg(),
						},
					},
				},
			},
			{name: "data", about: "Export documents to a CSV or JSON file and import them from one (M9.3, ADR 0021). These commands run as the trusted system context: no permission rules apply (see --actor of `import`). Both use the document pipeline, so an import runs the hooks, validation and webhooks of every row",
				subs: []*command{
					{name: "export", about: "Write the documents of a DocType to standard output (or --out) as CSV or JSON, in one consistent snapshot, in pages. Child tables are written to JSON only. Text that a spreadsheet could take for a formula gets a leading `'` in CSV (numbers never do); use JSON for exact text. An export of more rows than the limit (100000, see --limit-override) is refused before anything is written", longAbout: "Write the documents of a DocType to standard output (or --out) as CSV or JSON, in one consistent snapshot, in pages. Child tables are written to JSON only. Text that a spreadsheet could take for a formula gets a leading `'` in CSV (numbers never do); use JSON for exact text. An export of more rows than the limit (100000, see --limit-override) is refused before anything is written.\n\n--filter and --sort are those of `doc list`; there is no paging.",
						args: []argSpec{
							{id: "doctype", positional: true, valueName: "DOCTYPE", required: true, help: "DocType name"},
							doctypesArg(),
							{id: "format", long: "format", valueName: "FORMAT", possible: []string{"csv", "json"}, def: "csv", hasDefault: true, help: "The file format"},
							{id: "filter", long: "filter", valueName: "JSON", help: "A JSON array of filters, as for `doc list`"},
							{id: "sort", long: "sort", valueName: "FIELD[:desc]", multiple: true, help: "Sort key `FIELD` or `FIELD:desc`; repeat for several. `name` is always the last key"},
							{id: "fields", long: "fields", valueName: "FIELDS", multiple: true, delimiter: ",", help: "The keys to write, comma separated: field names and the standard columns `name`, `owner`, `modified_by`, `created_at`, `modified_at`, `version` (and `docstatus`, `workflow_state` where the DocType has them). Default: `name` and every field"},
							{id: "out", long: "out", valueName: "FILE", kind: kindPath, help: "Write to this new file instead of standard output. An existing file is never overwritten, and a failed export leaves no partial file"},
							{id: "limit_override", long: "limit-override", valueName: "N", kind: kindU64, help: "Allow this many rows instead of the default limit (100000)"},
						},
					},
					{name: "import", about: "Import documents from a CSV or JSON file in one transaction: every row goes through the document pipeline (hooks, validation, naming, webhooks), each in a savepoint, and if any row has an error **nothing** is imported. Prints the summary as JSON (as the API's `data`), with the first 100 row errors; exit code 1 when any row failed. With --dry-run nothing is ever kept. At most 10000 rows", longAbout: "Import documents from a CSV or JSON file in one transaction: every row goes through the document pipeline (hooks, validation, naming, webhooks), each in a savepoint, and if any row has an error **nothing** is imported. Prints the summary as JSON (as the API's `data`), with the first 100 row errors; exit code 1 when any row failed. With --dry-run nothing is ever kept. At most 10000 rows.\n\nCSV: the first row is the header of field keys (a `name` column is allowed; child tables are not, use JSON); an empty cell is not given (insert) or set to null (upsert of an existing document). JSON: an array of objects as for `doc insert`, child rows as arrays.",
						args: []argSpec{
							{id: "doctype", positional: true, valueName: "DOCTYPE", required: true, help: "DocType name"},
							{id: "file", positional: true, valueName: "FILE", kind: kindPath, required: true, help: "The file to read"},
							doctypesArg(),
							{id: "format", long: "format", valueName: "FORMAT", possible: []string{"csv", "json"}, required: true, help: "The file format"},
							{id: "upsert", long: "upsert", flag: true, help: "Update the documents that exist (by `name`) and insert the others, instead of inserting every row"},
							{id: "dry_run", long: "dry-run", flag: true, help: "Check everything and report, but keep nothing"},
							actorArg(),
						},
					},
				},
			},
			{name: "user", about: "Create and manage users and their roles. Passwords are read from standard input",
				subs: []*command{
					{name: "create", about: "Create an enabled user. The password is read from the first line of standard input",
						args: []argSpec{
							{id: "username", positional: true, valueName: "USERNAME", required: true, help: "Username: 1 to 140 ASCII letters, digits and `_ . @ -` (case-sensitive)"},
							{id: "roles", long: "role", valueName: "ROLE", multiple: true, help: "A role to give the user; repeat for several"},
						},
					},
					{name: "set-password", about: "Change a user's password (read from the first line of standard input) and end all of the user's sessions",
						args: []argSpec{
							{id: "username", positional: true, valueName: "USERNAME", required: true, help: "Username"},
						},
					},
					{name: "add-role", about: "Give a user a role",
						args: []argSpec{
							{id: "username", positional: true, valueName: "USERNAME", required: true, help: "Username"},
							{id: "role", positional: true, valueName: "ROLE", required: true, help: "Role name"},
						},
					},
					{name: "remove-role", about: "Take a role from a user",
						args: []argSpec{
							{id: "username", positional: true, valueName: "USERNAME", required: true, help: "Username"},
							{id: "role", positional: true, valueName: "ROLE", required: true, help: "Role name"},
						},
					},
					{name: "disable", about: "Disable a user: no login, and its sessions and API tokens stop working",
						args: []argSpec{
							{id: "username", positional: true, valueName: "USERNAME", required: true, help: "Username"},
						},
					},
					{name: "enable", about: "Enable a disabled user",
						args: []argSpec{
							{id: "username", positional: true, valueName: "USERNAME", required: true, help: "Username"},
						},
					},
					{name: "permission", about: "Restrict what a user sees (user permissions, ADR 0025): add, remove and list the documents a user is limited to. A user with permissions for a DocType sees only those documents of it, and only the documents of other DocTypes whose Links to it are empty or point at them. Changes apply on the user's next request. Administrators are never restricted",
						subs: []*command{
							{name: "add", about: "Restrict a user to one document of a DocType (repeat to allow several; at most 1000 per DocType). The user and the document must exist, and the DocType must be one of the site's (a child DocType cannot be restricted). Adding a row that exists succeeds",
								args: []argSpec{
									{id: "user", long: "user", valueName: "USERNAME", required: true, help: "Username"},
									{id: "doctype", long: "doctype", valueName: "DOCTYPE", required: true, help: "The DocType to restrict"},
									{id: "value", long: "value", valueName: "NAME", required: true, help: "The name of the document the user may see"},
									doctypesArg(),
								},
							},
							{name: "remove", about: "Take one restriction from a user. With the last one of a DocType the user is no longer restricted on it. Removing a row that does not exist succeeds and says so",
								args: []argSpec{
									{id: "user", long: "user", valueName: "USERNAME", required: true, help: "Username"},
									{id: "doctype", long: "doctype", valueName: "DOCTYPE", required: true, help: "The DocType"},
									{id: "value", long: "value", valueName: "NAME", required: true, help: "The name of the document"},
								},
							},
							{name: "list", about: "List the restrictions, sorted by user, DocType and value, as a table",
								args: []argSpec{
									{id: "user", long: "user", valueName: "USERNAME", help: "Only this user's"},
									{id: "doctype", long: "doctype", valueName: "DOCTYPE", help: "Only this DocType's"},
								},
							},
						},
					},
				},
			},
			{name: "token", about: "Create and revoke API tokens",
				subs: []*command{
					{name: "create", about: "Create an API token for a user and print `key:secret` once. The secret cannot be shown again; store it now",
						args: []argSpec{
							{id: "username", positional: true, valueName: "USERNAME", required: true, help: "Username"},
						},
					},
					{name: "revoke", about: "Revoke an API token by its key",
						args: []argSpec{
							{id: "key", positional: true, valueName: "KEY", required: true, help: "The key (the part before the colon)"},
						},
					},
				},
			},
			{name: "webhook", about: "Manage the outgoing webhooks of a site (M9.2, ADR 0020): a webhook posts a signed JSON snapshot of a document to a URL after it was inserted, updated, deleted, submitted, cancelled or moved by a workflow transition. A document operation enqueues the delivery in its own transaction, and `forgebase worker` sends it (so a site that has webhooks needs a running worker). Secrets are printed once, by `add` and `rotate-secret`, and never again",
				subs: []*command{
					{name: "add", about: "Add a webhook and print its secret once. The secret signs every delivery (`X-Forgebase-Signature`, HMAC-SHA256); store it now, it cannot be shown again. It is kept in the site's database in plain text (the signature needs it), so anyone who can read that database can read it. The URL must be `https://` and must not contain user info; the DocType must exist and must not be a child DocType. At delivery time the host is resolved and must not be a loopback, private, link-local or reserved address (see --allow-private)",
						args: []argSpec{
							{id: "name", positional: true, valueName: "NAME", required: true, help: "The webhook name: lower-case letters, digits and underscores, starting with a letter"},
							{id: "doctype", long: "doctype", valueName: "DOCTYPE", required: true, help: "The DocType whose documents trigger the webhook"},
							{id: "events", long: "events", valueName: "EVENTS", multiple: true, delimiter: ",", help: "The events, comma separated: insert, update, delete, submit, cancel, transition. A workflow transition that submits or cancels the document also fires `submit` or `cancel`; an amendment fires `insert`"},
							{id: "url", long: "url", valueName: "URL", required: true, help: "The URL deliveries are posted to"},
							{id: "allow_private", long: "allow-private", flag: true, help: "Allow the URL to resolve to a loopback, private, link-local or reserved address (including the cloud metadata address). Off by default: it is the SSRF guard. Use it only for a receiver inside your own network"},
							{id: "insecure_http", long: "insecure-http", flag: true, help: "Allow `http://` instead of `https://`. For development only: the payload and its signature travel unencrypted"},
							{id: "secret_stdin", long: "secret-stdin", flag: true, help: "Read the secret from the first line of standard input instead of generating 32 random bytes. It is not printed back"},
							doctypesArg(),
						},
					},
					{name: "list", about: "Print the webhooks as a table: name, DocType, events, the scheme, host and port of the URL (never its path or query, which may hold a token), whether it is enabled and its two flags. Never a secret"},
					{name: "disable", about: "Stop a webhook: no new delivery is enqueued, and queued ones are dropped when they run",
						args: []argSpec{
							{id: "name", positional: true, valueName: "NAME", required: true, help: "The webhook name"},
						},
					},
					{name: "enable", about: "Enable a disabled webhook",
						args: []argSpec{
							{id: "name", positional: true, valueName: "NAME", required: true, help: "The webhook name"},
						},
					},
					{name: "remove", about: "Delete a webhook. Queued deliveries are dropped when they run",
						args: []argSpec{
							{id: "name", positional: true, valueName: "NAME", required: true, help: "The webhook name"},
						},
					},
					{name: "rotate-secret", about: "Give a webhook a new secret and print it once (or read it from standard input with --secret-stdin). Deliveries signed with the old one are no longer valid for the receiver once it switches: update the receiver first or accept both for a while",
						args: []argSpec{
							{id: "name", positional: true, valueName: "NAME", required: true, help: "The webhook name"},
							{id: "secret_stdin", long: "secret-stdin", flag: true, help: "Read the new secret from the first line of standard input"},
						},
					},
					{name: "test", about: "Send one test delivery (`event` is `test`) now, signed and checked like a real one, without the queue. Prints the HTTP status of the answer, or why the delivery was refused. Exit code 0 for a 2xx answer, 1 otherwise. It works even if the webhook is disabled",
						args: []argSpec{
							{id: "name", positional: true, valueName: "NAME", required: true, help: "The webhook name"},
						},
					},
				},
			},
			{name: "customize", about: "Customize the DocTypes of one site (M10.1, ADR 0023). Administrators of the server use the command line (or the Administrator-only API); there is no admin screen",
				subs: []*command{
					{name: "field", about: "Site custom fields: fields this site adds to a DocType of its own database. They are stored in the site (`fb_custom_field`), get a column at once, and are seen by every server and worker of the site within 10 seconds. Only adding is possible: a field cannot be changed or removed, only disabled (which hides it everywhere and keeps its column and data) and enabled again",
						subs: []*command{
							{name: "add", about: "Add a custom field to a DocType. The field is one TOML table body, written like a `[[fields]]` entry of a DocType file (for example `name = \"custom_vip\"` and `type = \"check\"` on two lines). The name must start with `custom_`; the type is any except `table`; `required` and `read_only` are refused (existing documents have no value, and no hook can write the field); `unique` and `allow_on_submit` (on a submittable DocType) are allowed. The DocType must exist, must not be a child DocType and must have been migrated",
								args: []argSpec{
									{id: "doctype", long: "doctype", valueName: "DOCTYPE", required: true, help: "The DocType to add the field to"},
									{id: "field", long: "field", valueName: "FILE", required: true, help: "The file with the field definition, or `-` to read it from standard input"},
									doctypesArg(),
								},
							},
							{name: "list", about: "Print the custom fields as a table: DocType, name, type, label, whether enabled and when and by whom they were last changed",
								args: []argSpec{
									{id: "doctype", long: "doctype", valueName: "DOCTYPE", help: "Only the fields of this DocType"},
								},
							},
							{name: "disable", about: "Hide a custom field everywhere (the API, the desk, filters, export, import). Its column and its data are kept",
								args: []argSpec{
									{id: "doctype", long: "doctype", valueName: "DOCTYPE", required: true, help: "The DocType of the field"},
									{id: "name", positional: true, valueName: "NAME", required: true, help: "The field name"},
								},
							},
							{name: "enable", about: "Show a disabled custom field again, with its data",
								args: []argSpec{
									{id: "doctype", long: "doctype", valueName: "DOCTYPE", required: true, help: "The DocType of the field"},
									{id: "name", positional: true, valueName: "NAME", required: true, help: "The field name"},
								},
							},
						},
					},
				},
			},
			{name: "site", about: "List the sites of the config file, create the database of a site, and back it up and restore it. `--site` is not used here: the commands take the site name as an argument",
				subs: []*command{
					{name: "list", about: "Print each site as a table: its name, hosts, apps (`all` when the config file lists none) and the name of the environment variable that holds its database URL (never the URL). Needs no database. Without a config file it shows the implicit site `default`"},
					{name: "create", about: "Create the database of a site, then migrate it as `migrate --site NAME` does. The site must be in the config file (or be `default` without one). Refuses, with exit code 1, when the database exists already: it never migrates an existing database implicitly (use `migrate`). The database name is the one in the site's URL; the role of the URL needs the `CREATEDB` privilege. If the migrate fails the database is kept: fix the problem and run `migrate --site NAME`. With `--empty` it only creates the empty database (for `restore`)",
						args: []argSpec{
							{id: "name", positional: true, valueName: "NAME", required: true, help: "The site name"},
							{id: "maintenance_db", long: "maintenance-db", valueName: "NAME", def: "postgres", hasDefault: true, help: "The database to connect to for `CREATE DATABASE` (on the same server, with the same user and password as the site's URL)"},
							{id: "empty", long: "empty", flag: true, help: "Create the database and do not migrate it: it has no tables, which is what `site restore` needs. No apps and no `--doctypes` are used"},
							doctypesArg(),
						},
					},
					{name: "backup", about: "Write a backup of the site's database to a new file, with `pg_dump` (PostgreSQL client tools of the server's major version, on `PATH` or in `FORGEBASE_PG_BIN`; they are not part of Forgebase). The file is a custom-format dump of everything in the database, **password hashes, session and API token digests included**: it is created with mode 0600; keep it safe. The dump is written to `FILE.partial` and published as FILE (a hard link) only when it is complete: a failed backup leaves nothing, and an interrupted one (SIGINT, SIGTERM) can leave only `FILE.partial`, which can be removed. An existing file is never overwritten (exit 1; there is no `--force`). The database URL and password are passed to `pg_dump` in its environment, never on its command line",
						args: []argSpec{
							{id: "name", positional: true, valueName: "NAME", required: true, help: "The site name"},
							{id: "out", long: "out", valueName: "FILE", kind: kindPath, required: true, help: "The file to create. It must not exist"},
						},
					},
					{name: "restore", about: "Restore a backup of `site backup` into the site's database with `pg_restore`, in one transaction. `pg_restore` must have the server's major version, and the backup must come from a `pg_dump` of the same or an older major version. The database must exist and be **empty** (no tables, schemas other than `public`, extensions other than `plpgsql`, functions or types: the kinds of objects checked): create it with `site create NAME --empty`. Nothing is migrated: run `migrate --site NAME` afterwards if this binary is newer than the backup",
						args: []argSpec{
							{id: "name", positional: true, valueName: "NAME", required: true, help: "The site name"},
							{id: "from", long: "from", valueName: "FILE", kind: kindPath, required: true, help: "The backup file"},
						},
					},
				},
			},
			{name: "new-app", about: "Create a new app: a Rust crate (library and binary) with a sample DocType, a hook, tests, a README and an AGENTS.md, in `<DIR>/<NAME>`. It builds and passes its tests as created. Needs no database. Never overwrites: an existing target directory is an error",
				args: []argSpec{
					{id: "name", positional: true, valueName: "NAME", required: true, help: "The app and crate name: lower-case ASCII letters, digits and underscores, starting with a letter (the rules of a DocType name), at most 54 bytes"},
					{id: "forgebase_path", long: "forgebase-path", valueName: "DIR", kind: kindPath, required: true, help: "The Forgebase checkout the app depends on (it contains `crates/forgebase`). Written into the app's `Cargo.toml` as an absolute path"},
					{id: "dir", long: "dir", valueName: "DIR", kind: kindPath, def: ".", hasDefault: true, help: "The directory in which `<NAME>` is created"},
				},
			},
			{name: "worker", about: "Run background jobs until Ctrl-C or SIGTERM. It claims only the jobs of the apps installed on the site (and the built-in ones), runs each in a database transaction with its completion, retries a failure with a growing delay and marks a job `dead` when the attempts are used up. The first signal stops claiming and lets running jobs finish for 30 seconds (then they are given back, not counted as an attempt); a second signal exits at once. It does not run migrations: run `migrate` first",
				args: []argSpec{
					doctypesArg(),
					{id: "concurrency", long: "concurrency", valueName: "N", kind: kindU32, min: 1, max: 64, ranged: true, def: "1", hasDefault: true, help: "How many jobs run at the same time, 1 to 64"},
				},
			},
			{name: "job", about: "Inspect and operate the job queue. There is no HTTP or desk interface for jobs: the command line is the interface",
				subs: []*command{
					{name: "list", about: "Print the newest jobs as a table: id, name, status, attempts, run_at and the start of the last error",
						args: []argSpec{
							{id: "status", long: "status", valueName: "STATUS", possible: []string{"queued", "running", "done", "dead"}, help: "Only jobs with this status"},
							{id: "name", long: "name", valueName: "NAME", help: "Only jobs with this full name (`<app>.<job>`)"},
							{id: "limit", long: "limit", valueName: "N", kind: kindU32, min: 1, max: 1000, ranged: true, def: "50", hasDefault: true, help: "How many jobs, 1 to 1000"},
						},
					},
					{name: "show", about: "Print every field of a job as JSON, the payload included",
						args: []argSpec{
							{id: "id", positional: true, valueName: "ID", required: true, help: "The job id"},
						},
					},
					{name: "retry", about: "Queue a dead job again: attempts back to 0, due now, error cleared. Any other status is an error",
						args: []argSpec{
							{id: "id", positional: true, valueName: "ID", required: true, help: "The job id"},
						},
					},
					{name: "enqueue", about: "Enqueue a job by its full name (`<app>.<job>`), which the binary must know. The job is recorded as enqueued by `--actor`. The payload is stored in clear text: never put a secret in it",
						args: []argSpec{
							{id: "name", positional: true, valueName: "NAME", required: true, help: "The full job name"},
							{id: "payload", long: "payload", valueName: "JSON|-", def: "{}", hasDefault: true, help: "The payload, any JSON (at most 64 KiB), or `-` to read it from standard input"},
							actorArg(),
						},
					},
				},
			},
			{name: "serve", about: "Serve the HTTP API (`/api/v1`) until Ctrl-C or SIGTERM (running requests get 10 seconds; a second signal exits at once). It does not run migrations: run `migrate` first. Environment: `FORGEBASE_LISTEN` (default `127.0.0.1:8000`), `FORGEBASE_ALLOWED_HOSTS` (comma separated host names without ports, default `localhost,127.0.0.1`) and `FORGEBASE_DEV_INSECURE_COOKIES=1` (development only: no `Secure` on the session cookie) and `FORGEBASE_DESK_DIR` (the built desk, typically `desk/dist`, served under `/desk/`; unset: no desk)",
				args: []argSpec{
					doctypesArg(),
				},
			},
		},
	}
}

// The argument groups that several commands flatten in (Rust DocTypesDir, DataArg, ActorArg, VersionArg).
func doctypesArg() argSpec {
	return argSpec{id: "doctypes", long: "doctypes", valueName: "DIR", kind: kindPath, help: "Directory with one DocType `*.toml` file per DocType (not searched recursively). It becomes the app `local`, which depends on the apps installed on the site (every app of the binary unless the site lists `apps`) and is part of every site's registry. Required when the binary has no apps (the stock `forgebase` binary); a binary with apps does not need it"}
}

func dataArg() argSpec {
	return argSpec{id: "data", long: "data", valueName: "JSON|-", required: true, help: "A JSON object with the field values, or `-` to read it from standard input"}
}

func actorArg() argSpec {
	return argSpec{id: "actor", long: "actor", valueName: "NAME", def: "cli", hasDefault: true, help: "The name of the trusted system context these commands run as: recorded as owner / modified_by on insert, update, submit, cancel, amend and transition (and in the workflow log); given to hooks (on delete it is only given to hooks, not stored). Not authenticated, and no permission rules apply to it"}
}

func versionArg() argSpec {
	return argSpec{id: "expected_version", long: "version", valueName: "N", kind: kindI64, required: true, help: "The version of the document you last read (the `version` field of `doc get`). The change is refused if someone else changed the document since"}
}
