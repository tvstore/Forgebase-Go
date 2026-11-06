package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ported from crates/forgebase-http/tests/desk.rs and the unit tests of src/desk.rs: the static desk,
// the strict allow-list, the headers and the Host check (M4.1).

const wantCSP = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// deskDir is a throwaway desk directory. secret.txt and secret.js (an allowed extension) sit next to the
// desk directory, in its parent, which no request may ever reach; index.js and top.js sit in the desk
// root, outside assets/, which is not served either.
type deskDir struct{ root, desk string }

func newDeskDir(t *testing.T) deskDir {
	t.Helper()
	root := t.TempDir()
	desk := filepath.Join(root, "dist")
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(desk, "assets", "dir.js"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(root, "secret.txt"), "TOP SECRET")
	write(filepath.Join(root, "secret.js"), "TOP SECRET")
	write(filepath.Join(desk, "index.js"), "NOT AN ASSET")
	write(filepath.Join(desk, "top.js"), "NOT AN ASSET")
	write(filepath.Join(desk, "index.html"), "<!doctype html><title>desk</title>")
	write(filepath.Join(desk, "secret.txt"), "NOT AN ASSET")
	for name, content := range map[string]string{
		"app-AbC123.js": "console.log(1)", "app-AbC123.css": "body{}", "logo.svg": "<svg/>", "data.exe": "MZ",
		"noextension": "x", ".hidden.js": "x", "..js": "x", "app.js.map": "{}",
	} {
		write(filepath.Join(desk, "assets", name), content)
	}
	return deskDir{root: root, desk: desk}
}

func (d deskDir) env(t *testing.T) *env {
	config := DefaultConfig()
	config.DeskDir = d.desk
	return newEnvWith(t, config)
}

func assertDeskHeaders(t *testing.T, r reply) {
	t.Helper()
	for name, want := range map[string]string{
		"Content-Security-Policy": wantCSP, "X-Frame-Options": "DENY", "Referrer-Policy": "same-origin",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := r.header.Get(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if r.header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS header")
	}
}

// Rust: desk.rs::the_index_and_the_assets_are_served_with_their_headers
func TestTheIndexAndTheAssetsAreServedWithTheirHeaders(t *testing.T) {
	e := newDeskDir(t).env(t)
	for _, path := range []string{"/desk/", "/desk/index.html"} {
		rep := e.send(get(path))
		wantStatus(t, rep, http.StatusOK, path)
		if rep.text != "<!doctype html><title>desk</title>" ||
			rep.header.Get("Content-Type") != "text/html; charset=utf-8" || rep.header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %v %q", path, rep.header, rep.text)
		}
		assertDeskHeaders(t, rep)
	}
	for _, c := range [][3]string{
		{"app-AbC123.js", "text/javascript; charset=utf-8", "console.log(1)"},
		{"app-AbC123.css", "text/css; charset=utf-8", "body{}"},
		{"logo.svg", "image/svg+xml", "<svg/>"},
		{"app.js.map", "application/json", "{}"},
	} {
		rep := e.send(get("/desk/assets/" + c[0]))
		wantStatus(t, rep, http.StatusOK, c[0])
		if rep.text != c[2] || rep.header.Get("Content-Type") != c[1] ||
			rep.header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Errorf("%s: %v %q", c[0], rep.header, rep.text)
		}
		assertDeskHeaders(t, rep)
	}
	// The API keeps no-store and gets no desk headers.
	rep := e.send(get("/api/v1/meta"))
	wantStatus(t, rep, http.StatusUnauthorized)
	if rep.header.Get("Cache-Control") != "no-store" || rep.header.Get("Content-Security-Policy") != "" {
		t.Error(rep.header)
	}
}

// Rust: desk.rs::only_the_allow_list_is_served
func TestOnlyTheAllowListIsServed(t *testing.T) {
	e := newDeskDir(t).env(t)
	hostile := []string{
		// Traversal in every spelling.
		"/desk/assets/../secret.js", "/desk/assets/../../secret.js", "/desk/assets/%2e%2e%2fsecret.js",
		"/desk/assets/%2E%2E%2Fsecret.js", "/desk/assets/..%2fsecret.js", "/desk/assets/a%2f..%2f..%2fsecret.js",
		"/desk/assets/dir.js%2f..%2f..%2f..%2fsecret.js", "/desk/assets/..%5csecret.js", "/desk/assets/%2e%2e%5csecret.js",
		"/desk/assets/..\\secret.js", "/desk/assets/%2e%2e/secret.js", "/desk/assets/..%2findex.js",
		"/desk/assets/..%2f..%2fsecret.js", "/desk/index.js", "/desk/top.js", "/desk/assets/../index.js",
		"/desk/assets/../top.js", "/desk/assets/../secret.txt", "/desk/assets/../../secret.txt",
		"/desk/assets/%2e%2e/secret.txt", "/desk/assets/%2e%2e%2fsecret.txt", "/desk/assets/%2E%2E%2Fsecret.txt",
		"/desk/assets/..%2fsecret.txt", "/desk/assets/..%5csecret.txt", "/desk/assets/%2e%2e%5csecret.txt",
		"/desk/assets/..%00.js", "/desk/assets/app-AbC123.js%00.png", "/desk/assets/%2fetc%2fpasswd",
		"/desk/assets//etc/passwd", "/desk/assets/./app-AbC123.js", "/desk/assets/sub/app-AbC123.js",
		"/desk/assets/%61pp-AbC123.js", "/desk/assets/app-AbC123.js/", "/desk/assets/", "/desk/assets",
		// Not in the allow-list: other files of the desk directory, hidden files, unknown extensions, a
		// directory named like a script, the empty name pieces.
		"/desk/secret.txt", "/desk/assets/secret.txt", "/desk/assets/data.exe", "/desk/assets/noextension",
		"/desk/assets/.hidden.js", "/desk/assets/..js", "/desk/assets/dir.js", "/desk/assets/missing.js",
		"/desk/missing.html", "/desk/assets/index.html", "/desk/assets/..", "/desk/%2e%2e/secret.txt",
		"/desk/..%2fsecret.txt",
	}
	for _, path := range hostile {
		rep := e.send(get(path))
		wantCode(t, rep, http.StatusNotFound, "not_found", path)
		if strings.Contains(rep.text, "SECRET") || strings.Contains(rep.text, "NOT AN ASSET") {
			t.Errorf("%s: %s", path, rep.text)
		}
	}
}

// Rust: desk.rs::desk_routes_take_get_only
func TestDeskRoutesTakeGetOnly(t *testing.T) {
	e := newDeskDir(t).env(t)
	for _, path := range []string{"/desk/", "/desk/index.html", "/desk/assets/app-AbC123.js"} {
		rep := e.send(post(path))
		wantStatus(t, rep, http.StatusMethodNotAllowed, path)
		if rep.header.Get("Allow") != "GET,HEAD" {
			t.Errorf("Allow %q", rep.header.Get("Allow"))
		}
	}
}

// A HEAD request is answered like a GET without the body (the server drops it).
func TestDeskHeadIsAnsweredLikeGet(t *testing.T) {
	e := newDeskDir(t).env(t)
	rep := e.send(reqOf(http.MethodHead, "/desk/"))
	wantStatus(t, rep, http.StatusOK)
	assertDeskHeaders(t, rep)
}

// Rust: desk.rs::a_desk_directory_without_index_is_a_404
func TestADeskDirectoryWithoutIndexIsA404(t *testing.T) {
	d := newDeskDir(t)
	e := d.env(t)
	if err := os.Remove(filepath.Join(d.desk, "index.html")); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.send(get("/desk/")), http.StatusNotFound)
}

// Rust: desk.rs::without_a_desk_directory_nothing_is_served
func TestWithoutADeskDirectoryNothingIsServed(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/desk/", "/desk/index.html", "/desk/assets/app.js", "/desk", "/"} {
		rep := e.send(get(path))
		wantCode(t, rep, http.StatusNotFound, "not_found", path)
		if rep.header.Get("Content-Security-Policy") != "" {
			t.Error(path)
		}
	}
}

// Rust: desk.rs::the_host_check_applies_to_the_desk
func TestTheHostCheckAppliesToTheDesk(t *testing.T) {
	e := newDeskDir(t).env(t)
	for _, path := range []string{"/desk/", "/desk/index.html", "/desk/assets/app-AbC123.js"} {
		for _, host := range []*string{nil, ptr("evil.example"), ptr("localhost.evil.example")} {
			r := get(path)
			if host == nil {
				r.noHost()
			} else {
				r.onHost(*host)
			}
			rep := e.send(r)
			wantCode(t, rep, http.StatusBadRequest, "invalid_host", path)
			if strings.Contains(rep.text, "desk") {
				t.Error(rep.text)
			}
		}
	}
}

// Rust: desk.rs::tests::asset_names_are_a_strict_allow_list
func TestAssetNamesAreAStrictAllowList(t *testing.T) {
	for _, good := range []string{"index-AbC123.js", "a.css", "Z9_-.x.map", "0.png"} {
		if !validAssetName(good) {
			t.Errorf("%q", good)
		}
	}
	for _, bad := range []string{
		"", ".hidden", "-a.js", "_a.js", "..", "../a.js", "a/b.js", "a\\b.js", "a%2fb.js", "%2e%2e", "a b.js",
		"a\x00.js", "é.js", "a.js/",
	} {
		if validAssetName(bad) {
			t.Errorf("%q", bad)
		}
	}
	if validAssetName(strings.Repeat("a", 256)) {
		t.Error("256 bytes")
	}
}

// Rust: desk.rs::tests::content_types_by_extension
func TestContentTypesByExtension(t *testing.T) {
	for name, want := range map[string]string{
		"a.js": "text/javascript; charset=utf-8", "a.CSS": "text/css; charset=utf-8", "a.woff2": "font/woff2",
		"a.exe": "", "noextension": "",
	} {
		got, ok := assetContentType(name)
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q %v", name, got, ok)
		}
	}
}
