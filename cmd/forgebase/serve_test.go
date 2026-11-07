package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// The tests of `forgebase serve` through the real process and real sockets. Ported from
// crates/forgebase/tests/cli.rs (serve_*) and tests/sites.rs (the serve tests).

// syncBuffer is a buffer that the process and the test may use at the same time.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// server is a running forgebase serve, killed when the test ends so that a failing test leaves no process
// behind.
type server struct {
	t       *testing.T
	cmd     *exec.Cmd
	address string
	lines   []string
	stderr  *syncBuffer
	exited  chan error
}

// startServe starts serve on an ephemeral port with exactly the environment env (and FORGEBASE_LISTEN),
// the arguments after "serve" and --doctypes, and waits for its "listening on" line and the siteLines
// lines after it.
func startServe(t *testing.T, env []string, args []string, siteLines int) *server {
	t.Helper()
	full := append([]string{"serve", "--doctypes", doctypes}, args...)
	cmd := exec.Command(binary, full...)
	cmd.Env = append(append([]string{}, env...), "FORGEBASE_LISTEN=127.0.0.1:0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &server{t: t, cmd: cmd, stderr: &syncBuffer{}, exited: make(chan error, 1)}
	cmd.Stderr = s.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("the listening line: %v; stderr: %s", err, s.stderr.String())
	}
	address, ok := strings.CutPrefix(strings.TrimSpace(line), "listening on http://")
	if !ok {
		t.Fatalf("unexpected first line %q; stderr: %s", line, s.stderr.String())
	}
	s.address = address
	for i := 0; i < siteLines; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("a site line: %v", err)
		}
		s.lines = append(s.lines, strings.TrimSpace(line))
	}
	// Anything printed later is read too, so that the server never meets a full pipe.
	go func() { _, _ = io.Copy(io.Discard, reader) }()
	go func() { s.exited <- cmd.Wait() }()
	return s
}

// request is one HTTP/1.1 request; it returns the status code, the head (lower-cased) and the body.
func (s *server) request(method, path, host string, headers []string, body string) (int, string, string) {
	s.t.Helper()
	conn, err := net.Dial("tcp", s.address)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	text := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n", method, path, host)
	for _, h := range headers {
		text += h + "\r\n"
	}
	text += fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	if _, err := io.WriteString(conn, text); err != nil {
		s.t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	reply, err := io.ReadAll(conn)
	if err != nil {
		s.t.Fatal(err)
	}
	head, rest, ok := strings.Cut(string(reply), "\r\n\r\n")
	if !ok {
		s.t.Fatalf("no head in %q", reply)
	}
	status, err := strconv.Atoi(strings.Fields(head)[1])
	if err != nil {
		s.t.Fatal(err)
	}
	// Go writes a chunked body only for a response of unknown length; the API sets the length.
	return status, head, rest
}

// header is the value of a header of a head (any case), "" when absent.
func header(head, name string) string {
	for _, line := range strings.Split(head, "\r\n")[1:] {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(k, name) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// stop stops the server and returns what it wrote to standard error.
func (s *server) stop() string {
	_ = s.cmd.Process.Kill()
	<-s.exited
	return s.stderr.String()
}

func migrateAndRoot(t *testing.T, env []string) {
	t.Helper()
	if r := runBinary(t, env, "", "migrate", "--doctypes", doctypes); r.code != 0 {
		t.Fatalf("migrate: %+v", r)
	}
	if r := runBinary(t, env, "correct horse battery\n", "user", "create", "root", "--role", "Administrator"); r.code != 0 {
		t.Fatalf("user create: %+v", r)
	}
}

// Rust: cli.rs::serve_answers_http_requests
func TestServeAnswersHTTPRequests(t *testing.T) {
	d := dbtest.New(t)
	env := []string{"FORGEBASE_DATABASE_URL=" + d.URL()}
	migrateAndRoot(t, env)
	s := startServe(t, env, nil, 0)
	host := s.address
	// No credential: the envelope with a 401.
	status, _, body := s.request("GET", "/api/v1/resource/customer", host, nil, "")
	if status != 401 || !strings.Contains(body, `"unauthenticated"`) {
		t.Fatalf("%d %s", status, body)
	}
	// A Host that is not allowed: refused before anything else.
	status, _, body = s.request("GET", "/api/v1/resource/customer", "evil.example", nil, "")
	if status != 400 || !strings.Contains(body, `"invalid_host"`) {
		t.Fatalf("%d %s", status, body)
	}
	// Login and read, with the cookie as a browser would send it.
	status, head, body := s.request("POST", "/api/v1/login", host, []string{"Content-Type: application/json"},
		`{"username":"root","password":"correct horse battery"}`)
	if status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	cookie := header(head, "Set-Cookie")
	if !strings.Contains(cookie, "; HttpOnly; Secure; SameSite=Lax;") {
		t.Fatalf("cookie %q", cookie)
	}
	pair, _, _ := strings.Cut(cookie, ";")
	status, _, body = s.request("GET", "/api/v1/resource/customer", host, []string{"Cookie: " + pair}, "")
	if status != 200 || body != `{"data":[]}` {
		t.Fatalf("%d %s", status, body)
	}
	errors := s.stop()
	// Nothing secret on standard error, and no warning in the default mode.
	for _, leak := range []string{"correct horse", d.URL(), "warning"} {
		if strings.Contains(errors, leak) {
			t.Errorf("%q on standard error: %s", leak, errors)
		}
	}
}

// Rust: cli.rs::serve_dev_mode_and_configuration
func TestServeDevModeAndConfiguration(t *testing.T) {
	d := dbtest.New(t)
	base := []string{"FORGEBASE_DATABASE_URL=" + d.URL()}
	migrateAndRoot(t, base)

	// Dev mode: no Secure on the cookie, and a warning at startup. The allowed hosts come from the
	// environment.
	s := startServe(t, append(base, "FORGEBASE_DEV_INSECURE_COOKIES=1", "FORGEBASE_ALLOWED_HOSTS=Erp.Local, localhost"), nil, 0)
	status, head, body := s.request("POST", "/api/v1/login", "erp.local:8000", []string{"Content-Type: application/json"},
		`{"username":"root","password":"correct horse battery"}`)
	if status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	if cookie := header(head, "Set-Cookie"); !strings.HasSuffix(cookie, "; HttpOnly; SameSite=Lax; Path=/; Max-Age=604800") {
		t.Errorf("cookie %q", cookie)
	}
	// 127.0.0.1 is not in this list.
	if status, _, _ := s.request("GET", "/api/v1/login", "127.0.0.1", nil, ""); status != 400 {
		t.Errorf("status %d", status)
	}
	if errors := s.stop(); !strings.Contains(errors, "FORGEBASE_DEV_INSECURE_COOKIES=1") {
		t.Errorf("%s", errors)
	}

	// An unusable host list or address ends the command with exit code 1 and no secret.
	for _, bad := range []string{"FORGEBASE_ALLOWED_HOSTS= , ", "FORGEBASE_ALLOWED_HOSTS=localhost:8000", "FORGEBASE_LISTEN=not an address"} {
		r := runBinary(t, append(append([]string{}, base...), bad), "", "serve", "--doctypes", doctypes)
		if r.code != 1 || !strings.HasPrefix(r.stderr, "error: ") || strings.Contains(r.stderr, d.URL()) {
			t.Errorf("%s: %+v", bad, r)
		}
	}
	// serve needs the DocType directory like migrate.
	if r := runBinary(t, base, "", "serve"); r.code != 2 {
		t.Errorf("%+v", r)
	}
}

// Rust: cli.rs::serve_keeps_running_when_its_standard_output_is_closed
func TestServeKeepsRunningWhenItsStandardOutputIsClosed(t *testing.T) {
	d := dbtest.New(t)
	env := []string{"FORGEBASE_DATABASE_URL=" + d.URL()}
	if r := runBinary(t, env, "", "migrate", "--doctypes", doctypes); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	// A free port, so the address is known without reading the "listening on" line.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	_ = l.Close()
	// A pipe whose read end is closed at once: the server's first write to standard output fails. (/dev/null
	// would not do: writes to it succeed.)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = read.Close()
	cmd := exec.Command(binary, "serve", "--doctypes", doctypes)
	cmd.Env = append(env, "FORGEBASE_LISTEN="+address)
	cmd.Stdout = write
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = write.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	// Wait until the server listens, then ask once: a server that stopped over the failed write resets
	// the connection or never listens.
	started := time.Now()
	var conn net.Conn
	for {
		conn, err = net.Dial("tcp", address)
		if err == nil {
			break
		}
		if time.Since(started) > 30*time.Second {
			t.Fatalf("the server never listened: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer func() { _ = conn.Close() }()
	_, _ = fmt.Fprintf(conn, "GET /api/v1/resource/customer HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", address)
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	reply, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("the server did not answer: %v", err)
	}
	select {
	case err := <-exited:
		t.Fatalf("the server exited: %v", err)
	default:
	}
	if !strings.HasPrefix(string(reply), "HTTP/1.1 401") {
		t.Errorf("%s", reply)
	}
}

// Rust: cli.rs::serve_serves_the_desk_only_when_configured
func TestServeServesTheDeskOnlyWhenConfigured(t *testing.T) {
	d := dbtest.New(t)
	base := []string{"FORGEBASE_DATABASE_URL=" + d.URL()}
	if r := runBinary(t, base, "", "migrate", "--doctypes", doctypes); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	desk := t.TempDir()
	if err := os.MkdirAll(filepath.Join(desk, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"index.html": "<title>desk</title>", "assets/app.js": "1"} {
		if err := os.WriteFile(filepath.Join(desk, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Not configured: no desk.
	s := startServe(t, base, nil, 0)
	if status, _, _ := s.request("GET", "/desk/", s.address, nil, ""); status != 404 {
		t.Errorf("status %d", status)
	}
	s.stop()

	// Configured: the index and the asset, with the security headers.
	s = startServe(t, append(append([]string{}, base...), "FORGEBASE_DESK_DIR="+desk), nil, 1)
	if want := "serving the desk from " + desk + " at /desk/"; s.lines[0] != want {
		t.Errorf("line %q, want %q", s.lines[0], want)
	}
	status, head, body := s.request("GET", "/desk/", s.address, nil, "")
	if status != 200 || body != "<title>desk</title>" || !strings.HasPrefix(header(head, "Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("%d %s %s", status, head, body)
	}
	if status, _, body := s.request("GET", "/desk/assets/app.js", s.address, nil, ""); status != 200 || body != "1" {
		t.Errorf("%d %s", status, body)
	}
	s.stop()

	// A directory without index.html ends the command with exit code 1.
	r := runBinary(t, append(append([]string{}, base...), "FORGEBASE_DESK_DIR="+filepath.Join(desk, "assets")), "", "serve", "--doctypes", doctypes)
	if r.code != 1 || !strings.Contains(r.stderr, "FORGEBASE_DESK_DIR") {
		t.Errorf("%+v", r)
	}
}

// The first stop signal lets the server finish and exit with code 0; a second signal ends a stuck one at
// once (Rust: run.rs::run_until_stopped, here through the real process).
func TestServeStopsOnTheFirstSignalAndAtOnceOnTheSecond(t *testing.T) {
	d := dbtest.New(t)
	env := []string{"FORGEBASE_DATABASE_URL=" + d.URL()}
	if r := runBinary(t, env, "", "migrate", "--doctypes", doctypes); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	// One signal and an idle server: it stops at once with code 0.
	s := startServe(t, env, nil, 0)
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-s.exited:
		if err != nil {
			t.Fatalf("exit: %v; stderr: %s", err, s.stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not stop")
	}
	if !strings.Contains(s.stderr.String(), "shutting down: waiting up to 10 s") {
		t.Errorf("%s", s.stderr.String())
	}

	// A request that never finishes holds the shutdown; the second signal drops it.
	s = startServe(t, env, nil, 0)
	stuck, err := net.Dial("tcp", s.address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stuck.Close() }()
	_, _ = io.WriteString(stuck, "POST /api/v1/login HTTP/1.1\r\nHost: "+s.address+"\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{")
	time.Sleep(300 * time.Millisecond) // let the server read the head
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	time.Sleep(300 * time.Millisecond)
	select {
	case <-s.exited:
		t.Fatal("the server did not wait for the open request")
	default:
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-s.exited:
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second signal did not stop the server")
	}
	if !strings.Contains(s.stderr.String(), "exiting now (second signal)") {
		t.Errorf("%s", s.stderr.String())
	}
}

// ---- sites (tests/sites.rs) -----------------------------------------------------------------------

const (
	aURL      = "FB_TEST_SITE_A_URL"
	bURL      = "FB_TEST_SITE_B_URL"
	passwordA = "password of site a"
	passwordB = "password of site b"
)

// pair is two sites, a (host a.localhost) and b (b.localhost), each with its own database, and a config
// file that lists them.
type pair struct {
	a, b *dbtest.DB
	conf string
}

func newPair(t *testing.T) *pair {
	t.Helper()
	p := &pair{a: dbtest.New(t), b: dbtest.New(t), conf: filepath.Join(t.TempDir(), "forgebase.toml")}
	text := fmt.Sprintf("[[sites]]\nname = \"a\"\nhosts = [\"a.localhost\"]\ndatabase_url_env = %q\n\n"+
		"[[sites]]\nname = \"b\"\nhosts = [\"b.localhost\", \"B2.localhost\"]\ndatabase_url_env = %q\npool_size = 4\n", aURL, bURL)
	if err := os.WriteFile(p.conf, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *pair) env(extra ...string) []string {
	return append([]string{aURL + "=" + p.a.URL(), bURL + "=" + p.b.URL()}, extra...)
}

// run runs a command with the config file.
func (p *pair) run(t *testing.T, stdin string, args ...string) output {
	t.Helper()
	return runBinary(t, p.env(), stdin, append([]string{"--config", p.conf}, args...)...)
}

// setUp migrates both sites and gives each an Administrator root with its own password.
func (p *pair) setUp(t *testing.T) {
	t.Helper()
	for site, password := range map[string]string{"a": passwordA, "b": passwordB} {
		if r := p.run(t, "", "migrate", "--site", site, "--doctypes", doctypes); r.code != 0 {
			t.Fatalf("migrate %s: %+v", site, r)
		}
		if r := p.run(t, password+"\n", "user", "create", "root", "--role", "Administrator", "--site", site); r.code != 0 {
			t.Fatalf("user create %s: %+v", site, r)
		}
	}
}

// login logs in as root; it returns the status and the name=value of the cookie, if any.
func (s *server) login(host, password string) (int, string) {
	status, head, _ := s.request("POST", "/api/v1/login", host, []string{"Content-Type: application/json"},
		fmt.Sprintf(`{"username":"root","password":%q}`, password))
	cookie, _, _ := strings.Cut(header(head, "Set-Cookie"), ";")
	return status, cookie
}

// Rust: sites.rs::serve_with_a_config_routes_by_host_and_keeps_the_sites_apart
func TestServeWithAConfigRoutesByHostAndKeepsTheSitesApart(t *testing.T) {
	p := newPair(t)
	p.setUp(t)
	// FORGEBASE_ALLOWED_HOSTS is ignored with a config file, with one warning.
	s := startServe(t, p.env("FORGEBASE_ALLOWED_HOSTS=other.example"), []string{"--config", p.conf}, 2)
	if got := strings.Join(s.lines, "|"); got != "site a: a.localhost|site b: b.localhost, b2.localhost" {
		t.Fatalf("lines %q", got)
	}
	get := func(host, cookie string) (int, string, string) {
		var headers []string
		if cookie != "" {
			headers = []string{"Cookie: " + cookie}
		}
		return s.request("GET", "/api/v1/resource/customer", host, headers, "")
	}
	// Each site has its own password for the same username.
	status, cookieA := s.login("a.localhost:8000", passwordA)
	if status != 200 || cookieA == "" {
		t.Fatalf("login a: %d", status)
	}
	if status, _ := s.login("b.localhost", passwordA); status != 401 {
		t.Errorf("a's password on b: %d", status)
	}
	if status, _ := s.login("a.localhost", passwordB); status != 401 {
		t.Errorf("b's password on a: %d", status)
	}
	status, cookieB := s.login("B2.localhost", passwordB)
	if status != 200 || cookieB == "" {
		t.Fatalf("login b: %d", status)
	}
	// A's session works on A and is refused on B, and the other way round.
	for _, c := range []struct {
		host, cookie string
		want         int
	}{{"a.localhost", cookieA, 200}, {"b.localhost", cookieA, 401}, {"b.localhost", cookieB, 200}, {"a.localhost", cookieB, 401}} {
		if status, _, body := get(c.host, c.cookie); status != c.want {
			t.Errorf("%s: %d %s", c.host, status, body)
		}
	}
	// A host that no site owns is a 404 with the envelope; a malformed one is a 400.
	status, head, body := get("c.localhost", cookieA)
	if status != 404 || !strings.Contains(body, `"not_found"`) || header(head, "Cache-Control") != "no-store" {
		t.Errorf("%d %s %s", status, head, body)
	}
	if status, _, _ := get("a.localhost:80@evil.example", ""); status != 400 {
		t.Errorf("status %d", status)
	}
	// The forwarded host is ignored.
	status, _, _ = s.request("GET", "/api/v1/resource/customer", "c.localhost",
		[]string{"X-Forwarded-Host: a.localhost", "Cookie: " + cookieA}, "")
	if status != 404 {
		t.Errorf("status %d", status)
	}
	errors := s.stop()
	if n := strings.Count(errors, "FORGEBASE_ALLOWED_HOSTS is ignored"); n != 1 {
		t.Errorf("%d warnings: %s", n, errors)
	}
	for _, leak := range []string{p.a.URL(), p.b.URL(), passwordA, passwordB} {
		if strings.Contains(errors, leak) {
			t.Errorf("%q on standard error", leak)
		}
	}
}

// Rust: sites.rs::serve_with_site_serves_only_that_site
func TestServeWithSiteServesOnlyThatSite(t *testing.T) {
	p := newPair(t)
	p.setUp(t)
	s := startServe(t, p.env(), []string{"--config", p.conf, "--site", "b"}, 1)
	if s.lines[0] != "site b: b.localhost, b2.localhost" {
		t.Fatalf("lines %q", s.lines)
	}
	if status, _ := s.login("b.localhost", passwordB); status != 200 {
		t.Errorf("b: %d", status)
	}
	// The host of the other site is unknown here: its database was never opened.
	if status, _ := s.login("a.localhost", passwordA); status != 404 {
		t.Errorf("a: %d", status)
	}
	if errors := s.stop(); strings.Contains(errors, "warning") {
		t.Errorf("%s", errors)
	}
	// A site whose variable is unset stops serve before it listens.
	r := runBinary(t, []string{bURL + "=" + p.b.URL()}, "", "--config", p.conf, "serve", "--doctypes", doctypes)
	if r.code != 1 || !strings.Contains(r.stderr, "site `a`") || r.stdout != "" {
		t.Errorf("%+v", r)
	}
}
