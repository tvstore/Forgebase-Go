package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tvstore/forgebase-go/internal/db"
)

// The job commands: list, show, retry and enqueue (ADR 0013; the Rust module is job_cmd.rs). Operators use
// the command line only; there is no HTTP or desk interface for jobs.

// errorChars is how many characters of the last error `job list` shows.
const errorChars = 40

// job runs a `job` command on site. The site's registry decides which jobs are known: the jobs of the apps
// installed on the site, and the built-in ones.
func (r *runner) job(ctx context.Context, site *site) error {
	composed, err := r.siteComposed(site, "")
	if err != nil {
		return err
	}
	verb := r.m.path[1]
	// The payload (standard input too) is read and checked before the database is opened, so a bad payload
	// never touches it.
	var payload any
	if verb == "enqueue" {
		text, err := r.readData(r.m.str("payload"))
		if err != nil {
			return err
		}
		if payload, err = parseJobPayload(text); err != nil {
			return err
		}
	}
	sc, err := site.open(ctx, r.o.Getenv, composed.registry, 0)
	if err != nil {
		return err
	}
	defer sc.close()
	switch verb {
	case "list":
		filter := db.JobFilter{Status: r.m.str("status"), Name: r.m.str("name")}
		found, err := db.ListJobs(ctx, sc.pool, filter, int64(r.m.u64("limit")))
		if err != nil {
			return err
		}
		return r.write(jobTable(found))
	case "show":
		id := r.m.str("id")
		found, err := db.GetJob(ctx, sc.pool, id)
		if err != nil {
			return err
		}
		if found == nil {
			return errf("no job with id %s", id)
		}
		text, err := prettyJSON(jobJSON(found))
		if err != nil {
			return errf("%s", err)
		}
		return r.write(text + "\n")
	case "retry":
		id := r.m.str("id")
		result, status, err := db.RetryDeadJob(ctx, sc.pool, id)
		if err != nil {
			return err
		}
		switch result {
		case db.RetryQueued:
			return r.write("job " + id + " is queued again\n")
		case db.RetryNotFound:
			return errf("no job with id %s", id)
		}
		return errf("job %s is %s: only a dead job can be retried", id, status)
	case "enqueue":
		id, err := db.Enqueue(ctx, sc.pool, composed.registry, r.m.str("name"), payload, r.m.str("actor"))
		if err != nil {
			return enqueueError(err)
		}
		return r.write("enqueued job " + id.String() + "\n")
	}
	return errf("unknown job command %q", verb)
}

// enqueueError is a refused enqueue (an unknown name, a payload that is too large) as the plain message the
// command line prints: Rust maps DbError::Job to CliError::Job there only. The other job commands print the
// database error as it is (`job: ...`, for an id that is no UUID).
func enqueueError(err error) error {
	var dbe *db.Error
	if errors.As(err, &dbe) && dbe.Kind == db.ErrJob {
		return errf("%s", dbe.Message)
	}
	return err
}

// parseJobPayload is the payload of `job enqueue` as JSON; numbers keep their digits.
func parseJobPayload(text string) (any, error) {
	data := []byte(strings.TrimSpace(text))
	var v any
	if !json.Valid(data) {
		// Unmarshal says what is wrong.
		err := json.Unmarshal(data, &v)
		if err == nil {
			err = errors.New("invalid JSON")
		}
		return nil, errf("the payload is not valid JSON: %s", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, errf("the payload is not valid JSON: %s", err)
	}
	return v, nil
}

func jobTime(t time.Time) string {
	text, err := db.UTCRFC3339(t)
	if err != nil {
		return t.String()
	}
	return text
}

func optionalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return jobTime(*t)
}

func optionalString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func jobJSON(job *db.Job) map[string]any {
	var payload any
	dec := json.NewDecoder(bytes.NewReader(job.Payload))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		payload = nil
	}
	return map[string]any{
		"id":           job.ID.String(),
		"name":         job.Name,
		"status":       job.Status,
		"attempts":     job.Attempts,
		"run_at":       jobTime(job.RunAt),
		"locked_until": optionalTime(job.LockedUntil),
		"locked_by":    optionalString(job.LockedBy),
		"last_error":   optionalString(job.LastError),
		"enqueued_by":  job.EnqueuedBy,
		"created_at":   jobTime(job.CreatedAt),
		"finished_at":  optionalTime(job.FinishedAt),
		"payload":      payload,
	}
}

// shortError is the start of an error on one line.
func shortError(message string) string {
	line := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message))
	if len(line) > errorChars {
		return string(line[:errorChars-1]) + "…"
	}
	return string(line)
}

func jobTable(jobs []db.Job) string {
	header := []string{"ID", "NAME", "STATUS", "ATTEMPTS", "RUN_AT", "LAST_ERROR"}
	rows := make([][]string, len(jobs))
	for i, job := range jobs {
		lastError := ""
		if job.LastError != nil {
			lastError = shortError(*job.LastError)
		}
		rows[i] = []string{job.ID.String(), job.Name, job.Status, strconv.Itoa(int(job.Attempts)), jobTime(job.RunAt), lastError}
	}
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
		b.WriteString(strings.TrimRight(out.String(), " "))
		b.WriteString("\n")
	}
	line(header)
	for _, row := range rows {
		line(row)
	}
	return b.String()
}
