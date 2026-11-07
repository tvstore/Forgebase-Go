package forgebase_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fbtest "github.com/tvstore/forgebase-go/testing"
)

// One router, two sites (M7.2, ADR 0014): the real binary of site_workers_test.go serves a config file whose
// sites install different apps, over a real socket. Ported from the serve step of removed_app of
// crates/forgebase/tests/site_apps.rs (the other steps of removed_app and the scenario http_per_site are in
// internal/app/site_apps_test.go, which sends the requests into the routing handler without a socket).

// siteServer is a running `forgebase serve`, killed when the test ends.
type siteServer struct {
	t       *testing.T
	address string
}

func (w *siteWorld) writeConfig(aApps, bApps string) {
	w.t.Helper()
	config := "[[sites]]\nname = \"a\"\nhosts = [\"a.localhost\"]\ndatabase_url_env = \"SITE_A_URL\"\napps = " + aApps + "\n\n" +
		"[[sites]]\nname = \"b\"\nhosts = [\"b.localhost\"]\ndatabase_url_env = \"SITE_B_URL\"\napps = " + bApps + "\n"
	if err := os.WriteFile(filepath.Join(w.dir, "forgebase.toml"), []byte(config), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// setUp migrates both sites and creates the Administrator root on each.
func (w *siteWorld) setUp() {
	w.t.Helper()
	for site, db := range map[string]*fbtest.DB{"a": w.a, "b": w.b} {
		w.migrate(site)
		db.CreateUser("root", "Administrator")
	}
}

func (w *siteWorld) serve() *siteServer {
	w.t.Helper()
	cmd := w.command("serve")
	cmd.Env = append(cmd.Env, "FORGEBASE_LISTEN=127.0.0.1:0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		w.t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil {
		w.t.Fatalf("the listening line: %v", err)
	}
	address, ok := strings.CutPrefix(strings.TrimSpace(line), "listening on http://")
	if !ok {
		w.t.Fatalf("unexpected first line %q", line)
	}
	// The site lines follow; keep reading until the server stops.
	go func() { _, _ = io.Copy(io.Discard, reader) }()
	return &siteServer{t: w.t, address: address}
}

// request sends one request to the host, with the cookie and CSRF headers of cookie ("" for none).
func (s *siteServer) request(method, path, host string, headers map[string]string, body string) (int, string, http.Header) {
	s.t.Helper()
	req, err := http.NewRequest(method, "http://"+s.address+path, strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(data), res.Header
}

// session logs in as root and returns the Cookie and CSRF headers.
func (s *siteServer) session(host string) map[string]string {
	s.t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "root", "password": fbtest.Password})
	status, text, header := s.request("POST", "/api/v1/login", host, map[string]string{"Content-Type": "application/json"}, string(body))
	if status != 200 {
		s.t.Fatalf("login on %s: %d %s", host, status, text)
	}
	var reply struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &reply); err != nil {
		s.t.Fatal(err)
	}
	cookie, _, _ := strings.Cut(header.Get("Set-Cookie"), ";")
	return map[string]string{"Cookie": cookie, "X-Forgebase-CSRF": reply.Data.CSRF, "Content-Type": "application/json"}
}

// Dropping an app from a site's list keeps its tables and data; the DocType is no longer served.
//
// Rust: site_apps.rs::removed_app (the serve step)
func TestADocTypeOfARemovedAppIsNotServed(t *testing.T) {
	w := newSiteWorld(t)
	w.setUp()
	w.writeConfig(`["base"]`, `["base"]`)
	cmd := w.command("migrate", "--site", "b")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	server := w.serve()
	session := server.session("b.localhost")
	status, text, _ := server.request("GET", "/api/v1/resource/extra_item", "b.localhost", session, "")
	if status != 404 {
		t.Fatalf("%d %s", status, text)
	}
	if status, text, _ := server.request("GET", "/api/v1/resource/item", "b.localhost", session, ""); status != 200 {
		t.Fatalf("%d %s", status, text)
	}
}
