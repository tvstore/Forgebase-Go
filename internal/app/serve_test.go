package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ported from the unit tests of crates/forgebase/src/run.rs (the serve part). The end-to-end tests of
// serve run the real binary and are in cmd/forgebase.

// Rust: run.rs::tests::allowed_hosts_are_trimmed_and_refuse_ports
func TestAllowedHostsAreTrimmedAndRefusePorts(t *testing.T) {
	got, err := parseAllowedHosts(" a.example , [::1],,localhost ")
	if err != nil || strings.Join(got, "|") != "a.example|[::1]|localhost" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"", " , ", "localhost:8000", "[::1]:8000", "::1", "a,b:1"} {
		if _, err := parseAllowedHosts(bad); err == nil {
			t.Errorf("%q", bad)
		}
	}
	_, err = parseAllowedHosts("localhost:8000")
	if err == nil || !strings.HasPrefix(err.Error(), `FORGEBASE_ALLOWED_HOSTS entry "localhost:8000" must be a host name without a port`) {
		t.Errorf("%v", err)
	}
}

// fakeServer is a server that ignores the stop request when stuck is true, like one with a stuck
// connection.
type fakeServer struct {
	stuck  bool
	stop   chan struct{}
	closed chan struct{}
	once   sync.Once
}

func newFake(stuck bool) *fakeServer {
	return &fakeServer{stuck: stuck, stop: make(chan struct{}), closed: make(chan struct{})}
}

func (f *fakeServer) Serve(net.Listener) error {
	<-f.stop
	if f.stuck {
		<-f.closed
	}
	return http.ErrServerClosed
}

func (f *fakeServer) Shutdown(ctx context.Context) error {
	close(f.stop)
	if f.stuck {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (f *fakeServer) Close() error { f.once.Do(func() { close(f.closed) }); return nil }

func run(t *testing.T, srv *fakeServer, signals int, grace time.Duration) (time.Duration, string, error) {
	t.Helper()
	ch := make(chan struct{}, signals)
	for i := 0; i < signals; i++ {
		ch <- struct{}{}
	}
	var log strings.Builder
	started := time.Now()
	err := runUntilStopped(srv, nil, ch, grace, &log)
	return time.Since(started), log.String(), err
}

// Rust: run.rs::tests::a_second_signal_exits_at_once
func TestASecondSignalExitsAtOnce(t *testing.T) {
	srv := newFake(true)
	elapsed, log, err := run(t, srv, 2, time.Minute)
	if err != nil || elapsed > 5*time.Second {
		t.Fatalf("%v %v", elapsed, err)
	}
	if !strings.Contains(log, "shutting down: waiting up to 60 s") || !strings.Contains(log, "exiting now (second signal)") {
		t.Error(log)
	}
	select {
	case <-srv.closed:
	default:
		t.Error("the connections were not dropped")
	}
}

// Rust: run.rs::tests::the_grace_period_ends_the_wait
func TestTheGracePeriodEndsTheWait(t *testing.T) {
	srv := newFake(true)
	elapsed, log, err := run(t, srv, 1, 50*time.Millisecond)
	if err != nil || elapsed < 50*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("%v %v", elapsed, err)
	}
	if !strings.Contains(log, "exiting now (grace period over); open connections are dropped") {
		t.Error(log)
	}
}

// Rust: run.rs::tests::the_first_signal_asks_the_server_to_stop
func TestTheFirstSignalAsksTheServerToStop(t *testing.T) {
	srv := newFake(false)
	elapsed, log, err := run(t, srv, 1, time.Minute)
	if err != nil || elapsed > 5*time.Second {
		t.Fatalf("%v %v", elapsed, err)
	}
	if strings.Contains(log, "exiting now") {
		t.Error(log)
	}
}

// A server that ends by itself reports its error; one that was asked to stop does not.
func TestAServerThatEndsByItselfReportsItsError(t *testing.T) {
	boom := errors.New("boom")
	srv := &endingServer{err: boom}
	err := runUntilStopped(srv, nil, make(chan struct{}), time.Minute, &strings.Builder{})
	if !errors.Is(err, boom) {
		t.Errorf("%v", err)
	}
}

type endingServer struct{ err error }

func (e *endingServer) Serve(net.Listener) error       { return e.err }
func (e *endingServer) Shutdown(context.Context) error { return nil }
func (e *endingServer) Close() error                   { return nil }

// A closed standard output must not stop the server: the announcements ignore a failed write.
func TestAnnounceIgnoresAClosedStandardOutput(t *testing.T) {
	r := &runner{o: Options{Stdout: failingWriter{}, Stderr: failingWriter{}}}
	r.announce("listening on http://127.0.0.1:1\n")
	r.warn("warning: x\n")
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestListenErrorText(t *testing.T) {
	_, err := net.Listen("tcp", "not an address")
	if got := listenErrorText(err); got != "invalid socket address" {
		t.Error(got)
	}
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	_, err = net.Listen("tcp", first.Addr().String())
	if got := listenErrorText(err); got != "Address already in use (os error 98)" {
		t.Error(got)
	}
}
