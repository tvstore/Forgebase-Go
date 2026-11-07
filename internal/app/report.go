package app

import (
	"fmt"
	"strings"

	"github.com/tvstore/forgebase-go/internal/db"
)

// migrateReport is the human-readable output of `forgebase migrate`: what a migrate did, or in a
// dry run would do (the Rust function migrate_report). The app migrations are mentioned only when
// the binary has apps (apps is how many).
func migrateReport(report *db.MigrateReport, dryRun bool, apps int) string {
	var b strings.Builder
	if len(report.Migrations) == 0 {
		b.WriteString("framework migrations: up to date\n")
	} else {
		verb := "applied"
		if dryRun {
			verb = "pending"
		}
		fmt.Fprintf(&b, "framework migrations: %d %s\n", len(report.Migrations), verb)
		for _, m := range report.Migrations {
			fmt.Fprintf(&b, "  %04d %s\n", m.Version, m.Name)
		}
	}
	sync := report.Sync
	if sync == nil {
		b.WriteString("schema sync: not planned yet, it needs the tables the pending framework migrations create; run without --dry-run to apply them\n")
		appMigrations(&b, report, dryRun, apps)
		fixtures(&b, report, dryRun)
		return b.String()
	}
	if len(sync.Steps) == 0 {
		b.WriteString("schema sync: up to date\n")
	} else {
		verb := "applied"
		if dryRun {
			verb = "would apply"
		}
		fmt.Fprintf(&b, "schema sync: %s %d step(s)\n", verb, len(sync.Steps))
		for _, step := range sync.Steps {
			fmt.Fprintf(&b, "%s\n", step)
		}
	}
	appMigrations(&b, report, dryRun, apps)
	schedules(&b, report, dryRun)
	fixtures(&b, report, dryRun)
	if len(sync.Warnings) > 0 {
		fmt.Fprintf(&b, "warnings: %d\n", len(sync.Warnings))
		for _, warning := range sync.Warnings {
			fmt.Fprintf(&b, "  - %s\n", warning)
		}
	}
	if dryRun {
		b.WriteString("dry run: nothing was changed\n")
	}
	return b.String()
}

// schedules lists the changes to the schedules, when there are any (nothing is printed when they are
// up to date).
func schedules(b *strings.Builder, report *db.MigrateReport, dryRun bool) {
	if len(report.Schedules) == 0 {
		return
	}
	verb := "changed"
	if dryRun {
		verb = "would change"
	}
	fmt.Fprintf(b, "schedules: %s %d\n", verb, len(report.Schedules))
	for _, line := range report.Schedules {
		fmt.Fprintf(b, "  %s\n", line)
	}
}

// fixtures is one line per app that has fixtures (nothing for the others): what was done, or in a dry
// run what would be.
func fixtures(b *strings.Builder, report *db.MigrateReport, dryRun bool) {
	for _, app := range report.Fixtures {
		switch {
		case !report.FixturesPlanned:
			fmt.Fprintf(b, "fixtures: %s: %d record(s), not planned yet (they are applied after the pending framework migrations)\n", app.App, app.Records)
		case dryRun:
			fmt.Fprintf(b, "fixtures: %s: would insert %d, update %d, keep %d, %d unchanged\n", app.App, app.Inserted, app.Updated, app.Kept, app.Unchanged)
		default:
			fmt.Fprintf(b, "fixtures: %s: %d inserted, %d updated, %d kept, %d unchanged\n", app.App, app.Inserted, app.Updated, app.Kept, app.Unchanged)
		}
	}
}

// appMigrations lists the app migrations, in the order they run (or would run).
func appMigrations(b *strings.Builder, report *db.MigrateReport, dryRun bool, apps int) {
	if apps == 0 && len(report.AppMigrations) == 0 {
		return
	}
	if len(report.AppMigrations) == 0 {
		b.WriteString("app migrations: up to date\n")
		return
	}
	verb := "applied"
	if dryRun {
		verb = "pending"
	}
	fmt.Fprintf(b, "app migrations: %d %s\n", len(report.AppMigrations), verb)
	for _, m := range report.AppMigrations {
		fmt.Fprintf(b, "  %s %04d %s\n", m.App, m.Version, m.Name)
	}
}
