package forgebase_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go"
	fbtest "github.com/tvstore/forgebase-go/testing"
)

// The worker of a site, with the apps installed on it (M7.2, ADR 0014): a binary with two apps, base and extra
// (which depends on base), run as a real process with a config file whose sites install different apps. Ported
// from the scenario worker_per_site of crates/forgebase/tests/site_apps.rs (the scenarios that run commands in
// process are in internal/app/site_apps_test.go).

const (
	siteWorkerItem      = "name = \"item\"\nlabel = \"Item\"\nnaming = { field = \"code\" }\n[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n"
	siteWorkerExtraItem = "name = \"extra_item\"\nlabel = \"Extra item\"\nnaming = { field = \"code\" }\n[[fields]]\nname = \"code\"\ntype = \"data\"\nrequired = true\nunique = true\n"
)

// stampExtra is extra.stamp: it inserts an extra_item with the code of its payload.
func stampExtra(ctx context.Context, job *forgebase.JobContext) error {
	var p struct {
		Code string `json:"code"`
	}
	code := "none"
	if err := job.DecodePayload(&p); err == nil && p.Code != "" {
		code = p.Code
	}
	_, err := job.Documents().Insert(ctx, "extra_item", forgebase.Document{Values: map[string]forgebase.Value{"code": forgebase.Text(code)}})
	return err
}

// siteHello is base.hello and extra.hello: it answers with its own name.
func siteHello(_ context.Context, m *forgebase.MethodContext) (any, error) {
	return map[string]any{"method": m.Name()}, nil
}

func siteBase() *forgebase.App {
	return forgebase.NewApp("base").Version("1.0.0").
		DocTypes(docTypes("doctypes/item.toml", siteWorkerItem)).
		Job("tick", noJob).
		Schedule("tick", forgebase.DailyAt(4, 0)).
		Method("hello", forgebase.NewMethod(siteHello).AnyUser())
}

func siteExtra() *forgebase.App {
	return forgebase.NewApp("extra").DependsOn("base").
		DocTypes(docTypes("doctypes/extra_item.toml", siteWorkerExtraItem)).
		Job("stamp", stampExtra).
		Job("mark", noJob).
		Schedule("mark", forgebase.EveryMinutes(60)).
		Method("hello", forgebase.NewMethod(siteHello).AnyUser())
}

type siteWorld struct {
	t    *testing.T
	a, b *fbtest.DB
	dir  string
}

func newSiteWorld(t *testing.T) *siteWorld {
	t.Helper()
	w := &siteWorld{t: t, a: fbtest.NewDB(t), b: fbtest.NewDB(t), dir: t.TempDir()}
	config := "[[sites]]\nname = \"a\"\nhosts = [\"a.localhost\"]\ndatabase_url_env = \"SITE_A_URL\"\napps = [\"base\"]\n\n" +
		"[[sites]]\nname = \"b\"\nhosts = [\"b.localhost\"]\ndatabase_url_env = \"SITE_B_URL\"\napps = [\"base\", \"extra\"]\n"
	if err := os.WriteFile(filepath.Join(w.dir, "forgebase.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *siteWorld) command(args ...string) *exec.Cmd {
	full := append([]string{"--config", filepath.Join(w.dir, "forgebase.toml")}, args...)
	cmd := exec.Command(os.Args[0], full...)
	cmd.Env = append(os.Environ(), binaryVar+"=sites", "SITE_A_URL="+w.a.URL(), "SITE_B_URL="+w.b.URL())
	return cmd
}

func (w *siteWorld) migrate(site string) {
	w.t.Helper()
	cmd := w.command("migrate", "--site", site)
	if out, err := cmd.CombinedOutput(); err != nil {
		w.t.Fatalf("migrate %s: %v\n%s", site, err, out)
	}
}

// runWorker starts the worker of site, waits until ready is true and stops it with SIGTERM; it returns the log.
func (w *siteWorld) runWorker(site string, what string, ready func() bool) string {
	w.t.Helper()
	cmd := w.command("worker", "--site", site)
	var log bytes.Buffer
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	deadline := time.Now().Add(30 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			w.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		w.t.Fatal(err)
	}
	if err := <-exited; err != nil {
		w.t.Fatalf("the worker of site %s exited with %v:\n%s", site, err, log.String())
	}
	return log.String()
}

func jobStatus(d *fbtest.DB, name string) string {
	rows := d.Rows(fmt.Sprintf("SELECT status FROM fb_job WHERE name = '%s'", name))
	if len(rows) == 0 {
		return ""
	}
	return rows[0][0]
}

const insertSiteJob = "INSERT INTO fb_job (id, name, payload, status, enqueued_by) " +
	"VALUES (gen_random_uuid(), '%s', '{\"code\":\"from-job\"}', 'queued', 'test')"

// Rust: site_apps.rs::worker_per_site
func TestAWorkerRunsOnlyTheJobsOfItsSite(t *testing.T) {
	w := newSiteWorld(t)
	w.migrate("a")
	w.migrate("b")
	// extra.stamp is queued in both databases, directly (job enqueue would refuse it on A).
	for _, d := range []*fbtest.DB{w.a, w.b} {
		d.Exec(fmt.Sprintf(insertSiteJob, "extra.stamp"))
	}
	// A built-in job after it, on A: when it is done the worker has been through the queue.
	const purge = "forgebase.purge_sessions"
	w.a.Exec(fmt.Sprintf(insertSiteJob, purge))

	// A stale extra.mark schedule row, due now, in A's database (the app is not installed on A, so migrate
	// never made one: this is what a leftover would look like), and the real one of B made due too, as the
	// control that a due schedule does tick.
	for _, d := range []*fbtest.DB{w.a, w.b} {
		d.Exec("INSERT INTO fb_schedule (name, spec, next_run_at) VALUES ('extra.mark', 'every 60m', now() - interval '1 hour') " +
			"ON CONFLICT (name) DO UPDATE SET next_run_at = now() - interval '1 hour'")
	}
	log := w.runWorker("a", "site a's worker to run the built-in job", func() bool { return jobStatus(w.a, purge) == "done" })
	// The job of the app that is not installed on A stays queued, untouched.
	if got := jobStatus(w.a, "extra.stamp"); got != "queued" {
		t.Errorf("extra.stamp on A is %q", got)
	}
	expect(t, w.a.Rows("SELECT attempts::text FROM fb_job WHERE name = 'extra.stamp'"), [][]string{{"0"}})
	if strings.Contains(log, "extra.stamp") {
		t.Errorf("%s", log)
	}
	// The worker of A does not tick the stale extra.mark row: nothing was enqueued and the row is untouched.
	expect(t, w.a.Rows("SELECT count(*)::text FROM fb_job WHERE name = 'extra.mark'"), [][]string{{"0"}})
	expect(t, w.a.Rows("SELECT (last_run_at IS NULL)::text FROM fb_schedule WHERE name = 'extra.mark'"), [][]string{{"true"}})

	// B's worker runs it: the job's handler inserts an extra_item of B.
	w.runWorker("b", "site b's worker to run extra.stamp", func() bool { return jobStatus(w.b, "extra.stamp") == "done" })
	// The control: B's worker did tick its due extra.mark schedule.
	expect(t, w.b.Rows("SELECT (count(*) >= 1)::text FROM fb_job WHERE name = 'extra.mark'"), [][]string{{"true"}})
	expect(t, w.b.Rows("SELECT count(*)::text FROM doc_extra_item"), [][]string{{"1"}})
}
