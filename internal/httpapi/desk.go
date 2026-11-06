package httpapi

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Serves the built desk (the React app of desk/) from a directory on the same origin under /desk/ (M4.1;
// the Rust module desk.rs). The routes exist only when Config.DeskDir is set; otherwise /desk/* is the
// ordinary 404.
//
// Strict allow-list. Only index.html (at /desk/ and /desk/index.html) and files directly in assets/ are
// served. The file name is taken from the raw request path (no percent-decoding), must match
// ^[A-Za-z0-9][A-Za-z0-9._-]*$ and has an extension with a known content type; anything else, including
// "..", "%2e%2e", "%2f", backslashes, NUL bytes and sub-directories, is a 404 that never touches the
// file system. A file is read whole into memory (the assets of a built desk are small); a directory, a
// missing file and an unknown extension are 404, another read error is a generic 500 (logged to
// standard error).
//
// Headers. On top of the headers every response gets (nosniff): a strict Content-Security-Policy
// (same-origin scripts and styles only, no inline code, no framing), X-Frame-Options: DENY and
// Referrer-Policy: same-origin. index.html is no-store (it names the hashed assets); the hashed files
// under assets/ are "public, max-age=31536000, immutable".

// deskCSP is the Content-Security-Policy of every desk response.
const deskCSP = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

const (
	deskNoStore   = "no-store"
	deskImmutable = "public, max-age=31536000, immutable"
)

// assetContentType is the content type of a file name by its extension; false (so 404) for an unknown
// one.
func assetContentType(name string) (string, bool) {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return "", false
	}
	switch strings.ToLower(name[i+1:]) {
	case "js":
		return "text/javascript; charset=utf-8", true
	case "css":
		return "text/css; charset=utf-8", true
	case "html":
		return "text/html; charset=utf-8", true
	case "svg":
		return "image/svg+xml", true
	case "png":
		return "image/png", true
	case "ico":
		return "image/x-icon", true
	case "woff2":
		return "font/woff2", true
	case "json", "map":
		return "application/json", true
	}
	return "", false
}

// validAssetName is ^[A-Za-z0-9][A-Za-z0-9._-]*$, at most 255 bytes.
func validAssetName(name string) bool {
	if len(name) == 0 || len(name) > 255 {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		alnum := b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
		if !(alnum || i > 0 && (b == '.' || b == '_' || b == '-')) {
			return false
		}
	}
	return true
}

func serveFile(w http.ResponseWriter, path, contentType, cache string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.EISDIR) {
			return errNotFound("no such route")
		}
		return errInternal(err)
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", cache)
	h.Set("Content-Security-Policy", deskCSP)
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return nil
}

// deskIndex is GET /desk/ and GET /desk/index.html.
func deskIndex(s *State, w http.ResponseWriter, _ *request) error {
	return serveFile(w, filepath.Join(s.config.DeskDir, "index.html"), "text/html; charset=utf-8", deskNoStore)
}

// deskAsset is GET /desk/assets/{name}; the name is checked on the raw path (see the comment above).
func deskAsset(s *State, w http.ResponseWriter, r *request) error {
	name, ok := strings.CutPrefix(r.rawPath, "/desk/assets/")
	if !ok {
		return errNotFound("no such route")
	}
	contentType, known := assetContentType(name)
	if !known || !validAssetName(name) {
		return errNotFound("no such route")
	}
	return serveFile(w, filepath.Join(s.config.DeskDir, "assets", name), contentType, deskImmutable)
}
