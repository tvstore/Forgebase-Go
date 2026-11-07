package db

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The TLS settings that need no server (ADR 0018). The tests against a server with TLS are in
// tls_test.go, the ones against a fake server that speaks only the TLS start of the protocol in
// tls_fake_test.go.

// testCA is a throwaway CA and what it signs.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// file writes the CA certificate to a file of the test's directory.
func (ca *testCA) file(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, ca.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// serverCert is a certificate for the given DNS names and IPs, signed by the CA.
func (ca *testCA) serverCert(t *testing.T, dns []string, ips []net.IP) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "server"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: dns, IPAddresses: ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// Rust: tls.rs::prepare_sets_the_ssl_mode_and_checks_the_hosts
func TestPoolConfigSetsTLSAndChecksTheHosts(t *testing.T) {
	// No TLS: nothing is tried, whatever the driver defaults are.
	config, err := parsePoolConfig("postgres://u@localhost/db")
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.TLSConfig != nil || len(config.ConnConfig.Fallbacks) != 0 {
		t.Error("TLS without a request for it")
	}
	// TLS: verifying, with the roots of the file, the host name of each host as the server name, and no
	// plain fallback.
	ca := newTestCA(t, "root")
	root := ca.file(t, "ca.pem")
	config, err = parsePoolConfig("postgres://u@db1.example.com,db2.example.com:5433/db?sslmode=require&sslrootcert=" + root)
	if err != nil {
		t.Fatal(err)
	}
	cc := config.ConnConfig.Config
	all := []*tls.Config{cc.TLSConfig}
	names := []string{"db1.example.com"}
	for _, fb := range cc.Fallbacks {
		all = append(all, fb.TLSConfig)
		names = append(names, fb.Host)
	}
	if len(all) != 2 {
		t.Fatalf("%d TLS configurations for two hosts", len(all))
	}
	for i, tc := range all {
		if tc == nil || tc.InsecureSkipVerify || tc.VerifyPeerCertificate != nil || tc.ServerName != names[i] ||
			tc.RootCAs == nil || tc.MinVersion < tls.VersionTLS12 {
			t.Errorf("host %s: %+v", names[i], tc)
		}
		want := x509.NewCertPool()
		want.AddCert(ca.cert)
		if tc != nil && !tc.RootCAs.Equal(want) {
			t.Errorf("host %s: the roots are not exactly the file", names[i])
		}
	}
	// An explicit file that is missing is an error that names the file, not the URL.
	_, err = parsePoolConfig("postgres://u:hunter2@localhost/db?sslmode=require&sslrootcert=/nonexistent/ca.pem")
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/ca.pem") || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("%v", err)
	}
	// A Unix socket host, or no host, with TLS.
	for _, c := range []struct{ url, want string }{
		{"postgres://u@%2Fvar%2Frun%2Fpostgresql/db?sslmode=require&sslrootcert=" + root, "Unix socket"},
		{"postgres://u@h/db?host=%2Ftmp&sslmode=require&sslrootcert=" + root, "Unix socket"},
		{"postgres:///db?sslmode=require&sslrootcert=" + root, "TLS needs a host name"},
	} {
		_, err := parsePoolConfig(c.url)
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrTLS || !strings.Contains(e.Message, c.want) {
			t.Errorf("%q: %v", c.url, err)
		}
	}
	// A file without a certificate.
	junk := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(junk, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = parsePoolConfig("postgres://u@localhost/db?sslmode=require&sslrootcert=" + junk)
	if err == nil || !strings.Contains(err.Error(), "no usable PEM certificate") {
		t.Errorf("%v", err)
	}
	// sslnegotiation=direct reaches the TLS configuration (and means nothing without TLS).
	config, err = parsePoolConfig("postgres://u@localhost/db?sslmode=require&sslrootcert=" + root + "&sslnegotiation=direct")
	if err != nil || len(config.ConnConfig.TLSConfig.NextProtos) != 1 || config.ConnConfig.TLSConfig.NextProtos[0] != "postgresql" {
		t.Errorf("%v", err)
	}
}

// Not in Rust: the roots of a file are exactly its certificates (rustls: add_parsable_certificates).
func TestLoadRootsFromAFile(t *testing.T) {
	a, b := newTestCA(t, "a"), newTestCA(t, "b")
	path := filepath.Join(t.TempDir(), "both.pem")
	text := append(append([]byte("some text\n"), a.pem...), b.pem...)
	if err := os.WriteFile(path, text, 0o600); err != nil {
		t.Fatal(err)
	}
	pool, err := loadRoots(tlsSetting{verify: true, rootFile: path})
	if err != nil {
		t.Fatal(err)
	}
	want := x509.NewCertPool()
	want.AddCert(a.cert)
	want.AddCert(b.cert)
	if !pool.Equal(want) {
		t.Error("the roots are not exactly the two certificates of the file")
	}
}

// Not in Rust: only a connection error can be a TLS handshake error (the real ones are in tls_fake_test.go).
func TestAnUnrelatedErrorIsNotATLSError(t *testing.T) {
	if _, ok := tlsConnectError(errors.New("boom")); ok {
		t.Error("an unrelated error")
	}
}

// Rust: tls.rs::the_pg_environment_matches_the_verification
func TestThePgSSLEnvironmentMatchesTheVerification(t *testing.T) {
	for _, c := range []struct {
		ssl  tlsSetting
		want []EnvVar
	}{
		{tlsSetting{}, []EnvVar{{"PGSSLMODE", "disable"}}},
		{tlsSetting{verify: true}, []EnvVar{{"PGSSLMODE", "verify-full"}, {"PGSSLROOTCERT", "system"}}},
		{tlsSetting{verify: true, rootFile: "/ca.pem"}, []EnvVar{{"PGSSLMODE", "verify-full"}, {"PGSSLROOTCERT", "/ca.pem"}}},
	} {
		got := pgSSLEnvironment(c.ssl)
		if len(got) != len(c.want) {
			t.Fatalf("%v", got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%v, want %v", got, c.want)
			}
		}
	}
}
