package db

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A fake server that speaks only the TLS start of the PostgreSQL protocol: it answers the SSLRequest
// with `S` (or `N`) and, for `S`, completes a TLS handshake with its certificate. It lets the tests
// show what a real server with the right certificate cannot: a certificate for another host name, one
// from another CA, and a server without TLS. (Rust's test of the host name uses `hostaddr`, which the
// Go implementation does not support.)

func fakeServer(t *testing.T, answer byte, cert *tls.Certificate) (port int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				request := make([]byte, 8)
				if _, err := io.ReadFull(conn, request); err != nil {
					return
				}
				if _, err := conn.Write([]byte{answer}); err != nil || answer != 'S' {
					return
				}
				server := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{*cert}})
				if server.Handshake() != nil {
					return
				}
				// The client verified us; the fake server has nothing more to say. (Reading first makes
				// the TLS 1.3 handshake complete on the client side before the connection goes away.)
				_, _ = server.Read(make([]byte, 1))
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func connectFake(t *testing.T, host string, port int, root string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := Connect(ctx, "postgres://u:s3cret@"+host+":"+strconv.Itoa(port)+"/db?sslmode=require&sslrootcert="+root+"&connect_timeout=5")
	if err == nil {
		pool.Close()
	}
	return err
}

func tlsErrorText(t *testing.T, err error) string {
	t.Helper()
	e, ok := err.(*Error)
	if !ok || e.Kind != ErrTLS {
		t.Fatalf("not a TLS error: %v", err)
	}
	if strings.Contains(e.Message, "s3cret") || strings.Contains(e.Message, "28P01") {
		t.Fatalf("leaks the password: %v", err)
	}
	return e.Message
}

// Rust: tls.rs::a_host_name_that_is_not_in_the_certificate_fails (against a fake server)
func TestAHostNameThatIsNotInTheCertificateFails(t *testing.T) {
	ca := newTestCA(t, "root")
	root := ca.file(t, "ca.pem")
	cert := ca.serverCert(t, []string{"wrong.example"}, nil)
	port := fakeServer(t, 'S', &cert)
	msg := tlsErrorText(t, connectFake(t, "localhost", port, root))
	if !strings.HasPrefix(msg, "error performing TLS handshake: ") || !strings.Contains(msg, "valid for wrong.example, not localhost") {
		t.Errorf("%s", msg)
	}
	// An IP address is checked against the IP names: this certificate has none.
	msg = tlsErrorText(t, connectFake(t, "127.0.0.1", port, root))
	if !strings.Contains(msg, "127.0.0.1") {
		t.Errorf("%s", msg)
	}
}

// The same certificate with the right names is accepted by the handshake (the fake server then closes the
// connection, so the connect fails later and not as a TLS error).
func TestACertificateWithTheRightNamesPassesTheHandshake(t *testing.T) {
	ca := newTestCA(t, "root")
	root := ca.file(t, "ca.pem")
	cert := ca.serverCert(t, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	port := fakeServer(t, 'S', &cert)
	for _, host := range []string{"localhost", "127.0.0.1"} {
		err := connectFake(t, host, port, root)
		if err == nil {
			t.Fatalf("%s: connected to a server that does not speak the protocol", host)
		}
		if e, ok := err.(*Error); ok && e.Kind == ErrTLS {
			t.Errorf("%s: the handshake failed: %v", host, err)
		}
	}
}

// Rust: tls.rs::a_wrong_ca_fails_with_a_verification_error (against a fake server)
func TestACertificateOfAnotherCAFails(t *testing.T) {
	ca, other := newTestCA(t, "root"), newTestCA(t, "other")
	cert := other.serverCert(t, []string{"localhost"}, nil)
	port := fakeServer(t, 'S', &cert)
	msg := tlsErrorText(t, connectFake(t, "localhost", port, ca.file(t, "ca.pem")))
	if !strings.Contains(msg, "unknown authority") {
		t.Errorf("%s", msg)
	}
}

// ADR 0018: there is no fallback to plain text. A server that answers the SSLRequest with `N` is an error.
func TestAServerWithoutTLSIsAnErrorNotAFallback(t *testing.T) {
	ca := newTestCA(t, "root")
	port := fakeServer(t, 'N', nil)
	msg := tlsErrorText(t, connectFake(t, "localhost", port, ca.file(t, "ca.pem")))
	if msg != "error performing TLS handshake: server does not support TLS" {
		t.Errorf("%s", msg)
	}
}
