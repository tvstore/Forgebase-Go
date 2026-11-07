package app

import (
	"context"
	"fmt"

	"github.com/tvstore/forgebase-go/internal/db"
)

// The clean-up jobs the framework owns (ADR 0013, owner decision 8; the Rust module is builtin.rs). They are
// registered in every binary under the reserved app name `forgebase`, with the default job options, and
// scheduled like an app's jobs. The SQL is in internal/db; this file only runs it, in batches, in the
// transaction of the job.
//
//	| Job                              | Deletes                                               | Schedule         |
//	| forgebase.purge_jobs             | done and dead jobs finished more than 7 days ago      | daily 03:00 UTC  |
//	| forgebase.purge_login_failures   | login failure rows that no longer count or lock       | every 60 minutes |
//	| forgebase.purge_sessions         | sessions that are past their expiry or idle limit     | every 60 minutes |
//
// forgebase.deliver_webhook (M9.2, ADR 0020) is the framework's one job that is not a clean-up and has no
// schedule; it arrives with the webhooks (P7).

const (
	// purgeBatch is the rows deleted per statement.
	purgeBatch = 1000
	// purgeMaxBatches is the statements per run. A run that hits the limit leaves the rest for the next
	// scheduled run, so one run stays short whatever the backlog.
	purgeMaxBatches = 100
)

// builtinJob is a built-in job with its schedule (nil: not scheduled).
type builtinJob struct {
	def  jobDef
	spec *db.Spec
}

// builtinJobs are the built-in jobs with their schedules, in a fixed order.
func builtinJobs() []builtinJob {
	purges := []struct {
		name  string
		purge func(context.Context, db.Queryer, int64) (uint64, error)
		spec  db.Spec
	}{
		{"purge_jobs", db.PurgeFinishedJobs, db.DailyAt(3, 0)},
		{"purge_login_failures", db.PurgeLoginFailures, db.EveryMinutes(60)},
		{"purge_sessions", db.PurgeSessions, db.EveryMinutes(60)},
	}
	out := make([]builtinJob, len(purges))
	for i, p := range purges {
		spec := p.spec
		out[i] = builtinJob{
			def:  jobDef{name: frameworkApp + "." + p.name, handler: purgeHandler(p.purge), options: NewJobOptions()},
			spec: &spec,
		}
	}
	return out
}

// purgeHandler deletes in batches in the job's transaction until a batch is not full.
func purgeHandler(purge func(context.Context, db.Queryer, int64) (uint64, error)) JobHandler {
	return func(ctx context.Context, job *JobContext) error {
		var total uint64
		for range purgeMaxBatches {
			deleted, err := purge(ctx, job.tx, purgeBatch)
			if err != nil {
				return fromDB(asDBError(err))
			}
			total += deleted
			if deleted < purgeBatch {
				break
			}
		}
		if total > 0 {
			job.log(fmt.Sprintf("%s: deleted %d row(s)", job.name, total))
		}
		return nil
	}
}
