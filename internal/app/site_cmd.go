package app

import (
	"context"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tvstore/forgebase-go/internal/db"
)

// The `site` commands (M7.2 and M7.3, ADR 0014; the Rust function site_command in run.rs): list the sites
// of the config file, create the database of a site, back it up and restore it. They take the site as an
// argument, so --site is refused.

func (r *runner) site(ctx context.Context, cfg *config) error {
	if _, named := r.m.opt("site"); named {
		return errUsage("`site` commands do not use --site; `site create` takes the site name as an argument")
	}
	switch r.m.path[1] {
	case "list":
		return r.write(r.siteList(cfg))
	case "create":
		site, err := cfg.find(r.m.str("name"))
		if err != nil {
			return err
		}
		return r.siteCreate(ctx, site)
	case "backup":
		site, err := cfg.find(r.m.str("name"))
		if err != nil {
			return err
		}
		text, err := r.siteBackup(ctx, site, r.m.str("out"))
		if err != nil {
			return err
		}
		return r.write(text)
	case "restore":
		site, err := cfg.find(r.m.str("name"))
		if err != nil {
			return err
		}
		text, err := r.siteRestore(ctx, site, r.m.str("from"))
		if err != nil {
			return err
		}
		return r.write(text)
	}
	return errf("unknown command %q", strings.Join(r.m.path, " "))
}

// siteCreate creates the database of the site and, unless --empty, migrates it as `migrate --site NAME`
// does. It refuses when the database exists already.
func (r *runner) siteCreate(ctx context.Context, site *site) error {
	name := site.name
	// Everything that can be checked without a database is checked before the database is created.
	// --empty uses no apps, so there is nothing to check.
	var composed *Composed
	if !r.m.flag("empty") {
		var err error
		if composed, err = r.siteComposed(site, r.m.str("doctypes")); err != nil {
			return err
		}
	}
	url, err := site.toolConnectionURL(r.o.Getenv)
	if err != nil {
		return err
	}
	// The flag has the default `postgres`; an explicit empty value is passed on as it is (Rust).
	maintenance, given := r.m.opt("maintenance_db")
	var maintenanceDB *string
	if given {
		maintenanceDB = &maintenance
	}
	created, err := db.CreateDatabase(ctx, url, maintenanceDB)
	if err != nil {
		return errSite(name, err)
	}
	if !created {
		return errf("database for site `%s` already exists; run `forgebase migrate --site %s`", name, name)
	}
	if err := r.write("created the database of site `" + name + "`\n"); err != nil {
		return err
	}
	if composed == nil {
		return r.write("the database is empty; restore into it with `forgebase site restore " + name + " --from FILE`\n")
	}
	text, err := r.migrateSite(ctx, site, composed, db.DefaultSyncOptions())
	if text != "" {
		// What was applied is reported first, also when the migrate fails after it.
		if werr := r.write(text); werr != nil {
			return werr
		}
	}
	if err != nil {
		return errf("the database of site `%s` was created, but migrate failed: %s\n"+
			"the database is kept; fix the problem and run `forgebase migrate --site %s`", name, describeError(err), name)
	}
	return nil
}

func (r *runner) tools() *toolRunner {
	return &toolRunner{binDir: r.o.Getenv(pgBinVar), stderr: r.o.Stderr}
}

// siteBackup is `site backup`: the message to print.
func (r *runner) siteBackup(ctx context.Context, site *site, file string) (string, error) {
	if err := checkNewFile(file); err != nil {
		return "", err
	}
	url, err := site.toolConnectionURL(r.o.Getenv)
	if err != nil {
		return "", err
	}
	env, err := db.PgEnvironment(url)
	if err != nil {
		return "", errSite(site.name, err)
	}
	info, err := db.InspectDatabase(ctx, url)
	if err != nil {
		return "", errSite(site.name, err)
	}
	size, err := r.tools().backup(env, info.ServerMajor(), file)
	if err != nil {
		return "", err
	}
	return "backed up site `" + site.name + "` to " + file + " (" + strconv.FormatInt(size, 10) + " bytes)\n", nil
}

// siteRestore is `site restore`: the message to print. The database must exist and be empty.
func (r *runner) siteRestore(ctx context.Context, site *site, file string) (string, error) {
	name := site.name
	if info, err := os.Stat(file); err != nil || !info.Mode().IsRegular() {
		return "", errf("%s is not a file; give the backup made by `forgebase site backup`", file)
	}
	url, err := site.toolConnectionURL(r.o.Getenv)
	if err != nil {
		return "", err
	}
	env, err := db.PgEnvironment(url)
	if err != nil {
		return "", errSite(name, err)
	}
	var database string
	for _, v := range env {
		if v.Name == "PGDATABASE" {
			database = v.Value
		}
	}
	info, err := db.InspectDatabase(ctx, url)
	if err != nil {
		if db.IsMissingDatabase(err) {
			return "", errf("the database of site `%s` does not exist; create it with `forgebase site create %s --empty`", name, name)
		}
		return "", errSite(name, err)
	}
	if !info.IsEmpty {
		return "", errf("the database of site `%s` is not empty; restore only into an empty database "+
			"(create one with `forgebase site create %s --empty`)", name, name)
	}
	if err := r.tools().restore(env, database, info.ServerMajor(), file); err != nil {
		return "", err
	}
	return "restored " + file + " into the database of site `" + name + "`\n" +
		"run `forgebase migrate --site " + name + "` if this binary is newer than the backup\n", nil
}

// siteList is the table `site list` prints: name, hosts, apps and the name of the URL variable (never the
// URL), in the layout of the other list outputs.
func (r *runner) siteList(cfg *config) string {
	header := []string{"SITE", "HOSTS", "APPS", "DATABASE_URL_ENV"}
	var rows [][]string
	for _, s := range cfg.sites {
		var hosts string
		if cfg.configured {
			hosts = strings.Join(s.hosts, ", ")
		} else {
			// The implicit site answers to FORGEBASE_ALLOWED_HOSTS (see serve).
			var list []string
			for _, h := range strings.Split(r.getenvOr("FORGEBASE_ALLOWED_HOSTS", "localhost,127.0.0.1"), ",") {
				if h = strings.TrimSpace(h); h != "" {
					list = append(list, h)
				}
			}
			hosts = strings.Join(list, ", ")
		}
		apps := "all"
		switch {
		case !s.appsListed:
		case len(s.apps) == 0:
			apps = "(none)"
		default:
			apps = strings.Join(s.apps, ", ")
		}
		rows = append(rows, []string{s.name, hosts, apps, s.databaseURLEnv})
	}
	return textTable(header, rows)
}

// textTable is a table of left-aligned columns separated by two spaces, without trailing white space (the
// trim_end of the Rust list commands).
func textTable(header []string, rows [][]string) string {
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len(h)
		for _, row := range rows {
			widths[i] = max(widths[i], utf8.RuneCountInString(row[i]))
		}
	}
	var b strings.Builder
	line := func(cells []string) {
		var out strings.Builder
		for i, cell := range cells {
			out.WriteString(cell)
			out.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)))
			out.WriteString("  ")
		}
		b.WriteString(strings.TrimRightFunc(out.String(), unicode.IsSpace))
		b.WriteString("\n")
	}
	line(header)
	for _, row := range rows {
		line(row)
	}
	return b.String()
}
