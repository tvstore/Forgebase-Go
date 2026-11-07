// Package webhooktest has what the webhook tests need and nothing else: a receiver that records what it is
// sent, with or without TLS, a throwaway certificate authority, and the signature written out from RFC 2104,
// independent of the code under test. Only tests import it (the Rust counterpart is tests/common/mod.rs of
// the crate forgebase).
//
// Every receiver listens on 127.0.0.1:0; Close stops it. A test never needs the network or the openssl tool.
package webhooktest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Received is one request the receiver got.
type Received struct {
	// RequestLine is the request line, for example `POST /hook HTTP/1.1`.
	RequestLine string
	// Headers are the headers, with lower-case names.
	Headers map[string]string
	// Body is the body.
	Body []byte
}

// Header is the value of a header (name in lower case), or "" when absent.
func (r Received) Header(name string) string { return r.Headers[name] }

// JSON is the body decoded as a JSON object.
func (r Received) JSON(t testing.TB) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("the body is not a JSON object: %v: %s", err, r.Body)
	}
	return v
}

// Reply is what the receiver answers.
type Reply struct {
	kind     string
	status   int
	size     int
	location string
	list     []Reply
}

// Status is this status with an empty body.
func Status(status int) Reply { return Reply{kind: "status", status: status} }

// Large is 200 with a body of this many bytes.
func Large(size int) Reply { return Reply{kind: "large", size: size} }

// Redirect is a redirect (this status) to this location.
func Redirect(status int, location string) Reply {
	return Reply{kind: "redirect", status: status, location: location}
}

// Hang accepts the request and never answers.
func Hang() Reply { return Reply{kind: "hang"} }

// Sequence answers with the first replies in order, then the last one forever.
func Sequence(replies ...Reply) Reply { return Reply{kind: "sequence", list: replies} }

// Receiver is a server that records the requests it gets.
type Receiver struct {
	server *httptest.Server
	tls    bool
	stop   chan struct{}

	mu       sync.Mutex
	requests []Received
	reply    Reply
	served   int
}

// NewHTTP starts a plain HTTP receiver. It is closed when the test ends.
func NewHTTP(t testing.TB, reply Reply) *Receiver { return start(t, reply, nil) }

// NewHTTPS starts an HTTPS receiver with the certificate of pki. It is closed when the test ends.
func NewHTTPS(t testing.TB, reply Reply, pki *PKI) *Receiver { return start(t, reply, pki) }

func start(t testing.TB, reply Reply, pki *PKI) *Receiver {
	r := &Receiver{reply: reply, stop: make(chan struct{}), tls: pki != nil}
	r.server = httptest.NewUnstartedServer(http.HandlerFunc(r.handle))
	// A refused handshake is the point of some tests: do not print it.
	r.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	if pki != nil {
		r.server.TLS = &tls.Config{Certificates: []tls.Certificate{pki.serverCert}}
		r.server.StartTLS()
	} else {
		r.server.Start()
	}
	t.Cleanup(r.Close)
	return r
}

// Close stops the receiver: a request that hangs is released first.
func (r *Receiver) Close() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	r.server.Close()
}

// Port is the port the receiver listens on.
func (r *Receiver) Port() int { return r.server.Listener.Addr().(*net.TCPAddr).Port }

// URL is the URL of path on this receiver, with the IP address as host.
func (r *Receiver) URL(path string) string { return r.url("127.0.0.1", path) }

// LocalhostURL is the URL with the name localhost as host.
func (r *Receiver) LocalhostURL(path string) string { return r.url("localhost", path) }

func (r *Receiver) url(host, path string) string {
	scheme := "http"
	if r.tls {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d%s", scheme, host, r.Port(), path)
}

// SetReply changes what the receiver answers from now on.
func (r *Receiver) SetReply(reply Reply) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reply = reply
}

// Requests are the requests received so far, oldest first.
func (r *Receiver) Requests() []Received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Received(nil), r.requests...)
}

func (r *Receiver) next() Reply {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := r.served
	r.served++
	if r.reply.kind != "sequence" {
		return r.reply
	}
	if len(r.reply.list) == 0 {
		return Status(200)
	}
	return r.reply.list[min(index, len(r.reply.list)-1)]
}

func (r *Receiver) handle(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	headers := map[string]string{}
	for name, values := range req.Header {
		headers[strings.ToLower(name)] = values[0]
	}
	// The Host header is not in req.Header in Go.
	headers["host"] = req.Host
	r.mu.Lock()
	r.requests = append(r.requests, Received{RequestLine: req.Method + " " + req.RequestURI + " " + req.Proto, Headers: headers, Body: body})
	r.mu.Unlock()
	switch reply := r.next(); reply.kind {
	case "hang":
		select {
		case <-r.stop:
		case <-req.Context().Done():
		}
	case "large":
		w.Header().Set("Content-Length", strconv.Itoa(reply.size))
		w.WriteHeader(200)
		// The client may close early (it discards the body after a limit): the write errors are ignored.
		_, _ = w.Write(bytes.Repeat([]byte("x"), reply.size))
	case "redirect":
		w.Header().Set("Location", reply.location)
		w.WriteHeader(reply.status)
	default:
		w.WriteHeader(reply.status)
	}
}

// PKI is a certificate authority and a server certificate for localhost and 127.0.0.1.
type PKI struct {
	// CAPEM is the PEM certificate of the authority: what a client must trust.
	CAPEM      string
	serverCert tls.Certificate
}

// NewPKI makes a fresh CA and a server certificate signed by it.
func NewPKI(t testing.TB, name string) *PKI {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("the test CA: %v", err)
		}
	}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Forgebase test CA " + name},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	must(err)
	caCert, err := x509.ParseCertificate(caDER)
	must(err)
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	serverTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	must(err)
	return &PKI{
		CAPEM:      string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		serverCert: tls.Certificate{Certificate: [][]byte{serverDER}, PrivateKey: serverKey},
	}
}

// ExpectedSignature is the signature a delivery must carry: sha256= and the lower-case hex of
// HMAC-SHA256(secret, "<timestamp>.<body>"), written out from RFC 2104 so that it does not depend on the
// crypto/hmac package the code under test uses.
func ExpectedSignature(secret, timestamp string, body []byte) string {
	message := append([]byte(timestamp+"."), body...)
	sum := hmacSHA256([]byte(secret), message)
	return "sha256=" + hex.EncodeToString(sum[:])
}

func hmacSHA256(key, message []byte) [32]byte {
	const block = 64
	if len(key) > block {
		sum := sha256.Sum256(key)
		key = sum[:]
	}
	var inner, outer [block]byte
	for i := range block {
		var k byte
		if i < len(key) {
			k = key[i]
		}
		inner[i], outer[i] = k^0x36, k^0x5c
	}
	innerSum := sha256.Sum256(append(inner[:], message...))
	return sha256.Sum256(append(outer[:], innerSum[:]...))
}
