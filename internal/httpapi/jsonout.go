package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// The output half of json.rs: the response bodies. db.DocumentToJSON writes a document as plain
// maps, which encoding/json writes with sorted keys like serde_json's map.

// marshal is v as compact JSON as serde_json writes it: no HTML escaping, no final newline, and
// U+2028 / U+2029 written as themselves.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	text := strings.TrimSuffix(buf.String(), "\n")
	if strings.Contains(text, "\\u202") {
		text = unescapeLineSeparators(text)
	}
	return []byte(text), nil
}

// unescapeLineSeparators turns the six-character escapes for U+2028 and U+2029 (which encoding/json
// always writes) into the characters, as serde_json does. An escaped backslash before the "u" is not an
// escape.
func unescapeLineSeparators(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) {
			switch {
			case strings.HasPrefix(text[i:], lineSepEscape):
				b.WriteRune(0x2028)
				i += 5
			case strings.HasPrefix(text[i:], paraSepEscape):
				b.WriteRune(0x2029)
				i += 5
			default:
				b.WriteByte(text[i])
				b.WriteByte(text[i+1])
				i++
			}
			continue
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

// The escapes of U+2028 and U+2029, written so that no tool turns them into the characters.
const (
	lineSepEscape = "\\" + "u2028"
	paraSepEscape = "\\" + "u2029"
)

// writeJSON answers with status and v as the body: Content-Type application/json, the body written
// at once.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := marshal(v)
	if err != nil {
		// Cannot happen for the values built here (string keys, finite numbers); never leak a detail.
		e := errInternal(err)
		body, _ = marshal(e.body())
		status = e.status
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeData answers {"data": value}.
func writeData(w http.ResponseWriter, status int, value any) {
	writeJSON(w, status, map[string]any{"data": value})
}
