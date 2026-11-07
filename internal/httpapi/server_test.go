package httpapi

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// "OPTIONS *" goes through the handler of a real server: the Host check, then the 404 of an unknown route,
// with the headers every answer has (net/http would answer a bare 200 by itself without
// DisableGeneralOptionsHandler).
func TestOptionsStarGoesThroughTheHandler(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ImplicitSites(lazyState(t)))
	go func() { _ = srv.Serve(l) }()
	defer func() { _ = srv.Close() }()
	ask := func(host string) (string, string) {
		conn, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		fmt.Fprintf(conn, "OPTIONS * HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
		var head strings.Builder
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			head.WriteString(line)
			if err != nil || line == "\r\n" {
				break
			}
		}
		body, _ := r.ReadString(0)
		return head.String(), body
	}
	head, body := ask("localhost")
	if !strings.HasPrefix(head, "HTTP/1.1 404") || !strings.Contains(body, `"no such route"`) ||
		!strings.Contains(head, "No-Store") && !strings.Contains(strings.ToLower(head), "cache-control: no-store") {
		t.Errorf("%s%s", head, body)
	}
	head, body = ask("evil.example")
	if !strings.HasPrefix(head, "HTTP/1.1 400") || !strings.Contains(body, `"invalid_host"`) {
		t.Errorf("%s%s", head, body)
	}
}
