package db

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TLS to PostgreSQL (ADR 0018; the Rust module tls.rs). The two TLS parameters of the database URL,
// sslmode and sslrootcert, are taken out of the URL by checkTLSSettings (connect.go) and become a
// tlsSetting; this file builds what the connection needs from it. The standard library's crypto/tls
// does the handshake and the verification, through pgx's TLS configuration (ADR 0026: no new
// dependency).
//
// | sslmode                            | behaviour                                           |
// |------------------------------------|-----------------------------------------------------|
// | absent, disable                    | no TLS                                              |
// | require, verify-ca, verify-full    | TLS, always verifying the chain and the host name   |
// | prefer, allow, anything else       | an error: there is never a silent downgrade         |
//
// The roots are the operating system's trust store (the default, and sslrootcert=system), or only
// the certificates of the PEM file sslrootcert=<path>. Client certificates (sslcert, sslkey) and the
// other ssl* parameters are errors, not ignored. No error contains the URL or the password.

// tlsSetting is what the URL asks for: no TLS (the zero value), or TLS with full verification.
type tlsSetting struct {
	verify bool
	// rootFile is the PEM file of sslrootcert=<path>; empty with verify: the operating system's store.
	rootFile string
}

func tlsError(message string) error { return &Error{Kind: ErrTLS, Message: message} }

// checkTLSHost: TLS needs a host name to verify, and is not offered over a Unix socket. It is used
// for the connections and for pg_environment, so pg_dump and pg_restore refuse the same URLs.
func (c *urlConfig) checkTLSHost() error {
	if len(c.hosts) == 0 {
		return tlsError("TLS needs a host name in the database URL")
	}
	for _, host := range c.hosts {
		if strings.HasPrefix(host, "/") {
			return tlsError("TLS is not supported over a Unix socket host")
		}
	}
	return nil
}

// loadRoots is the pool of trusted roots of a verifying setting.
func loadRoots(ssl tlsSetting) (*x509.CertPool, error) {
	if ssl.rootFile == "" {
		pool, err := x509.SystemCertPool()
		cause := "it is empty"
		if err != nil {
			cause = err.Error()
		} else if !pool.Equal(x509.NewCertPool()) {
			return pool, nil
		}
		return nil, tlsError("no root certificate in the operating system's trust store (" + cause + "); " +
			"install the CA certificates (for example the `ca-certificates` package) " +
			"or set `sslrootcert=<path to a PEM file>`")
	}
	data, err := os.ReadFile(ssl.rootFile)
	if err != nil {
		reason := err.Error()
		var pe *fs.PathError
		if errors.As(err, &pe) {
			reason = pe.Err.Error()
		}
		return nil, tlsError("cannot read the `sslrootcert` file " + quote(ssl.rootFile) + ": " + reason)
	}
	certs, err := pemCertificates(data)
	if err != nil {
		return nil, tlsError("cannot read the `sslrootcert` file " + quote(ssl.rootFile) + ": " + err.Error())
	}
	// Certificates that do not parse are ignored (rustls: add_parsable_certificates).
	pool := x509.NewCertPool()
	added := 0
	for _, der := range certs {
		if cert, err := x509.ParseCertificate(der); err == nil {
			pool.AddCert(cert)
			added++
		}
	}
	if added == 0 {
		return nil, tlsError("the `sslrootcert` file " + quote(ssl.rootFile) + " has no usable PEM certificate")
	}
	return pool, nil
}

// pemCertificates are the CERTIFICATE sections of a PEM text, as rustls-pki-types' pem_file_iter reads them
// (Rust: CertificateDer::pem_file_iter): text outside the sections is ignored, sections of other kinds (keys,
// CRLs) are skipped, and a malformed section (no end line, an end line of another kind, base64 that does not
// decode) makes the whole file an error. A file with no section at all yields none (the caller refuses it).
func pemCertificates(data []byte) ([][]byte, error) {
	var certs [][]byte
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		label, ok := strings.CutPrefix(line, "-----BEGIN ")
		if !ok || !strings.HasSuffix(label, "-----") {
			continue
		}
		label = strings.TrimSuffix(label, "-----")
		end := "-----END " + label + "-----"
		var body strings.Builder
		closed := false
		for i++; i < len(lines); i++ {
			l := strings.TrimSpace(lines[i])
			if l == end {
				closed = true
				break
			}
			if strings.HasPrefix(l, "-----") {
				return nil, errors.New("section end marker does not match its start")
			}
			body.WriteString(l)
		}
		if !closed {
			return nil, errors.New("section end marker is missing")
		}
		der, err := base64.StdEncoding.DecodeString(body.String())
		if err != nil {
			return nil, errors.New("section is not valid base64")
		}
		if label == "CERTIFICATE" {
			certs = append(certs, der)
		}
	}
	return certs, nil
}

// quote is a path as Rust's {:?} writes it.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// setTLSRoots gives every TLS configuration of the pool (the first host's and the fallbacks' for the
// other hosts) the roots, and checks that none of them is lax: pgx built them for verify-full, which
// verifies the chain and the host name of each host; with roots nil they would use no roots at all.
func setTLSRoots(config *pgxpool.Config, roots *x509.CertPool) error {
	cc := &config.ConnConfig.Config
	all := []*tls.Config{cc.TLSConfig}
	for _, fb := range cc.Fallbacks {
		all = append(all, fb.TLSConfig)
	}
	for _, tc := range all {
		if tc == nil || tc.InsecureSkipVerify || tc.ServerName == "" || tc.VerifyPeerCertificate != nil {
			// Not reachable with sslmode=verify-full; a refusal rather than a connection that does not verify.
			return tlsError("cannot set up TLS")
		}
		tc.RootCAs = roots
		tc.MinVersion = tls.VersionTLS12
	}
	return nil
}

// pgSSLEnvironment is the PGSSLMODE and PGSSLROOTCERT that give pg_dump and pg_restore the same TLS
// behaviour as the connections of this package: disable, or verify-full with the roots (a path, or
// system, which needs PostgreSQL 16 or later).
func pgSSLEnvironment(ssl tlsSetting) []EnvVar {
	if !ssl.verify {
		return []EnvVar{{"PGSSLMODE", "disable"}}
	}
	roots := "system"
	if ssl.rootFile != "" {
		roots = ssl.rootFile
	}
	return []EnvVar{{"PGSSLMODE", "verify-full"}, {"PGSSLROOTCERT", roots}}
}

// tlsConnectError is the error of a connection attempt whose TLS handshake failed, as TLS with its
// cause (Rust: "error performing TLS handshake: invalid peer certificate: UnknownIssuer"), so that it
// is never mistaken for a password failure. The text of the driver error names the user, database and
// address (never the password); only the cause is kept.
func tlsConnectError(err error) (*Error, bool) {
	var ce *pgconn.ConnectError
	if !errors.As(err, &ce) {
		return nil, false
	}
	text := err.Error()
	var cause string
	var verification *tls.CertificateVerificationError
	switch {
	case errors.As(err, &verification):
		// crypto/tls handshakes lazily: pgx sees this error when it writes the startup message.
		cause = verification.Err.Error()
	case strings.Contains(text, "server refused TLS connection"):
		cause = "server does not support TLS"
	case strings.Contains(text, "tls error: "):
		cause = text[strings.Index(text, "tls error: ")+len("tls error: "):]
	case strings.Contains(text, "tls: "):
		cause = text[strings.Index(text, "tls: ")+len("tls: "):]
	default:
		return nil, false
	}
	if nl := strings.IndexByte(cause, '\n'); nl >= 0 {
		cause = cause[:nl]
	}
	return &Error{Kind: ErrTLS, Message: "error performing TLS handshake: " + cause, Err: err}, true
}
