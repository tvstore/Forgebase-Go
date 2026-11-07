package forgebase_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	fbtest "github.com/tvstore/forgebase-go/testing"
)

// Not in Rust (a Go handler cannot be aborted): the worker process of a job whose handler ignores its context
// exits at once on the second signal, however long the handler runs, and leaves the job running with its lease,
// which another worker recovers (as in Rust, where the task is simply dropped). The behaviour after one signal
// (the 30 s grace, then the bounded wait for the handler) is covered in process by
// internal/app/worker_test.go, where the times are shortened.
func TestASecondSignalExitsAWorkerWhoseHandlerIgnoresItsContext(t *testing.T) {
	db := fbtest.NewDB(t)
	run := func(args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = append(os.Environ(), binaryVar+"=stubborn", "FORGEBASE_DATABASE_URL="+db.URL())
		return cmd
	}
	if out, err := run("migrate").CombinedOutput(); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	db.Exec("DELETE FROM fb_schedule")
	db.Exec("INSERT INTO fb_job (id, name, payload, status, enqueued_by) VALUES (gen_random_uuid(), 'lab.stubborn', '{}', 'queued', 'test')")
	cmd := run("worker")
	var log bytes.Buffer
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	// The handler is running: the job is claimed and its transaction is open.
	deadline := time.Now().Add(30 * time.Second)
	for {
		rows := db.Rows(fmt.Sprintf("SELECT count(*)::text FROM pg_stat_activity WHERE datname = '%s' AND state = 'idle in transaction'",
			db.Rows("SELECT current_database()")[0][0]))
		if rows[0][0] == "1" && db.Rows("SELECT status FROM fb_job")[0][0] == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the handler never started: %s", log.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	started := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("the worker exited with %v:\n%s", err, log.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the worker did not exit after the second signal:\n%s", log.String())
	}
	if took := time.Since(started); took > 2*time.Second {
		t.Errorf("the worker took %s to exit", took)
	}
	if !strings.Contains(log.String(), "exiting now (second signal)") {
		t.Errorf("%s", log.String())
	}
	// Nobody released the job: it is running, and its lease has not expired.
	expect(t, db.Rows("SELECT status, (locked_until > now())::text FROM fb_job"), [][]string{{"running", "true"}})
}
