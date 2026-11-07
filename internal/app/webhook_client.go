package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// The HTTP side of outgoing webhooks (M9.2, ADR 0020; the Rust module webhook_client.rs): the URL rules, the
// SSRF guard and the signed POST. Only the standard library: net/http, crypto/hmac and crypto/tls (ADR 0026,
// G2).
//
// The rules, in the order they are applied to a delivery:
//
//  1. The URL (parseWebhookURL): https, or http only for a webhook added with --insecure-http; a host; no
//     user info (user:pass@).
//  2. Resolve, then vet (vetWebhookHost): the host is resolved here, by us, and every address is checked
//     against isBlockedAddr. One blocked address refuses the delivery, unless the webhook has allow_private.
//  3. Pin (pinnedDialer): the transport gets only the vetted addresses and never resolves the name again, so a
//     DNS answer that changes between the check and the connection (DNS rebinding) cannot send the request
//     elsewhere. TLS still uses the host name of the URL for the SNI and for verifying the certificate.
//  4. Send: HTTP/1.1, one request on one fresh connection, no redirect followed, no proxy, a total time limit
//     (webhookTimeout) that covers the DNS lookup, the connection, TLS, the request and the response, and a
//     response body that is read up to webhookBodyLimit bytes and thrown away.
//
// Nothing here logs or returns a secret, a signature or a URL path or query: a URL often carries a token
// (Slack style), so messages name only the scheme, the host and the port (webhookTarget.label). The errors of
// net/http are never printed as they are (a *url.Error holds the whole URL).

const (
	// webhookTimeout is the longest a delivery may take, in total.
	webhookTimeout = 10 * time.Second
	// webhookBodyLimit is how much of a response body is read before the connection is dropped.
	webhookBodyLimit = 64 * 1024
	// maxWebhookURLBytes is the longest URL that is accepted.
	maxWebhookURLBytes = 2048
)

// webhookUserAgent is the User-Agent of deliveries.
func webhookUserAgent() string { return "Forgebase-Webhook/" + Version }

// ---- the blocklist ------------------------------------------------------------------------------

// azureWireServer is the Azure WireServer (host and platform services of an Azure virtual machine): not a
// private range, so it needs its own entry. Also blocked as an IPv4-mapped or NAT64 address.
var azureWireServer = netip.AddrFrom4([4]byte{168, 63, 129, 16})

// isBlockedAddr reports whether an address is one a webhook must not reach (without allow_private).
//
//	IPv4  0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16 (link-local, with the cloud
//	      metadata address 169.254.169.254), 172.16.0.0/12, 192.0.0.0/24, 192.168.0.0/16, 198.18.0.0/15,
//	      224.0.0.0/4 (multicast), 240.0.0.0/4 (reserved, with 255.255.255.255); and, as an explicit extra,
//	      168.63.129.16/32, the Azure WireServer, which is reachable from Azure virtual machines and is a
//	      public address otherwise
//	IPv6  ::/96 (this includes :: and ::1), fc00::/7 (unique local), fe80::/10 (link-local), ff00::/8
//	      (multicast); ::ffff:0:0/96 (IPv4-mapped) and 64:ff9b::/96 (the NAT64 well-known prefix) are checked
//	      as the IPv4 address they embed
func isBlockedAddr(ip netip.Addr) bool {
	if ip.Is4() {
		return blockedV4(ip)
	}
	return blockedV6(ip)
}

func blockedV4(ip netip.Addr) bool {
	o := ip.As4()
	a, b, c := o[0], o[1], o[2]
	return a == 0 || // 0.0.0.0/8
		a == 10 || // 10.0.0.0/8
		(a == 100 && b >= 64 && b <= 127) || // 100.64.0.0/10
		a == 127 || // 127.0.0.0/8
		(a == 169 && b == 254) || // 169.254.0.0/16
		ip == azureWireServer || // 168.63.129.16/32: reachable from Azure VMs, public otherwise
		(a == 172 && b >= 16 && b <= 31) || // 172.16.0.0/12
		(a == 192 && b == 0 && c == 0) || // 192.0.0.0/24
		(a == 192 && b == 168) || // 192.168.0.0/16
		(a == 198 && (b == 18 || b == 19)) || // 198.18.0.0/15
		a >= 224 // 224.0.0.0/4 multicast, 240.0.0.0/4 reserved, broadcast
}

func blockedV6(ip netip.Addr) bool {
	b := ip.As16()
	var s [8]uint16
	for i := range s {
		s[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
	}
	zero := func(from, to int) bool {
		for _, v := range s[from:to] {
			if v != 0 {
				return false
			}
		}
		return true
	}
	// ::ffff:0:0/96, IPv4-mapped: the embedded IPv4 address decides.
	if zero(0, 5) && s[5] == 0xffff {
		return blockedV4(embeddedV4(s[6], s[7]))
	}
	// 64:ff9b::/96, NAT64: the embedded IPv4 address decides.
	if s[0] == 0x64 && s[1] == 0xff9b && zero(2, 6) {
		return blockedV4(embeddedV4(s[6], s[7]))
	}
	return zero(0, 6) || // ::/96: the unspecified and loopback addresses and the old IPv4-compatible form
		s[0]&0xfe00 == 0xfc00 || // fc00::/7
		s[0]&0xffc0 == 0xfe80 || // fe80::/10
		s[0]&0xff00 == 0xff00 // ff00::/8
}

func embeddedV4(high, low uint16) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(high >> 8), byte(high), byte(low >> 8), byte(low)})
}

// ---- the URL ------------------------------------------------------------------------------------

// webhookTarget is a webhook URL that passed the rules.
type webhookTarget struct {
	// url is what is requested: the scheme, the authority without user info and the path and query as they were
	// written (the fragment is dropped).
	url *url.URL
	// host is the host without the brackets of an IPv6 literal.
	host  string
	port  uint16
	https bool
}

// label is scheme://host:port, the only part of the URL that is logged or printed.
func (t *webhookTarget) label() string {
	scheme := "http"
	if t.https {
		scheme = "https"
	}
	if strings.Contains(t.host, ":") {
		return fmt.Sprintf("%s://[%s]:%d", scheme, t.host, t.port)
	}
	return fmt.Sprintf("%s://%s:%d", scheme, t.host, t.port)
}

// uriChar is the set of bytes a URL may hold (what the Rust http crate accepts).
func uriChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'()*+,-./:;=?@[]_~", c) >= 0
}

// parseWebhookURL checks a webhook URL. insecureHTTP allows http://. The error never contains the URL (it may
// hold a token); it says what is wrong with it. The rules and the messages are those of the Rust
// parse_url, which used the http crate's Uri.
func parseWebhookURL(raw string, insecureHTTP bool) (*webhookTarget, error) {
	if len(raw) > maxWebhookURLBytes {
		return nil, fmt.Errorf("the URL is longer than %d bytes", maxWebhookURLBytes)
	}
	invalid := errors.New("the URL is not a valid absolute URL")
	if raw == "" {
		return nil, invalid
	}
	for i := 0; i < len(raw); i++ {
		if !uriChar(raw[i]) {
			return nil, invalid
		}
	}
	scheme := ""
	rest := raw
	if i := strings.Index(raw, "://"); i > 0 && validScheme(raw[:i]) {
		scheme, rest = strings.ToLower(raw[:i]), raw[i+3:]
	}
	if scheme == "" {
		// No scheme: "/path" (origin form) or "host:port" (authority form) parse as URIs, anything else
		// ("example.com/hook") does not.
		if !strings.HasPrefix(raw, "/") && strings.ContainsAny(raw, "/?") {
			return nil, invalid
		}
		return nil, errors.New("the URL must start with https://")
	}
	// The authority runs to the first /, ? or #.
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority, tail := rest[:end], rest[end:]
	if authority == "" || strings.Count(authority, "@") > 1 {
		return nil, invalid
	}
	https := false
	switch scheme {
	case "https":
		https = true
	case "http":
		if !insecureHTTP {
			return nil, errors.New("the URL uses http://; webhooks use https:// (for development only, add the webhook with --insecure-http)")
		}
	default:
		return nil, errors.New("the URL scheme must be https")
	}
	if strings.Contains(authority, "@") {
		return nil, errors.New("the URL has user info (user:password@): credentials in a URL are logged and leaked; " +
			"put the token in the path or sign with the secret instead")
	}
	host, port, ok := splitHostPort(authority)
	if !ok {
		return nil, invalid
	}
	if host == "" {
		return nil, errors.New("the URL has no host")
	}
	portNumber := uint16(80)
	if https {
		portNumber = 443
	}
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return nil, invalid
		}
		portNumber = uint16(n)
	}
	// The request target is the path and query as written, without the fragment.
	if i := strings.IndexByte(tail, '#'); i >= 0 {
		tail = tail[:i]
	}
	if tail == "" || tail[0] == '?' {
		tail = "/" + tail
	}
	u, err := url.Parse(scheme + "://" + authority + tail)
	if err != nil {
		return nil, invalid
	}
	return &webhookTarget{url: u, host: host, port: portNumber, https: https}, nil
}

func validScheme(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.')) {
			return false
		}
	}
	return s != ""
}

// splitHostPort splits an authority into the host (without the brackets of an IPv6 literal) and the port text
// ("" when there is none). ok is false when it is malformed.
func splitHostPort(authority string) (host, port string, ok bool) {
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", "", false
		}
		host, rest := authority[1:end], authority[end+1:]
		switch {
		case rest == "":
			return host, "", true
		case rest[0] == ':' && allDigits(rest[1:]):
			return host, rest[1:], true
		}
		return "", "", false
	}
	if i := strings.LastIndexByte(authority, ':'); i >= 0 {
		if !allDigits(authority[i+1:]) || strings.Contains(authority[:i], ":") {
			return "", "", false
		}
		return authority[:i], authority[i+1:], true
	}
	return authority, "", true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// ---- resolve, vet, pin --------------------------------------------------------------------------

// webhookFailure is why a delivery was not sent or got no answer.
type webhookFailure struct {
	// refused: the URL or the address is not allowed. Retrying cannot help. Otherwise a network problem or a
	// timeout, which retrying may help.
	refused bool
	message string
}

func (f *webhookFailure) Error() string { return f.message }

func refused(message string) *webhookFailure   { return &webhookFailure{refused: true, message: message} }
func transport(message string) *webhookFailure { return &webhookFailure{message: message} }

// resolver looks a host name up: the system's by default, a fake in a test.
type resolver func(ctx context.Context, host string) ([]netip.Addr, error)

func systemResolver(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// vetWebhookHost resolves the host of target and checks every address. An IP literal is used as it is. With
// allowPrivate nothing is blocked. The port of the returned addresses is the URL's.
func vetWebhookHost(ctx context.Context, lookup resolver, target *webhookTarget, allowPrivate bool) ([]netip.AddrPort, *webhookFailure) {
	label := target.label()
	var addrs []netip.AddrPort
	if ip, err := netip.ParseAddr(target.host); err == nil && ip.Zone() == "" {
		addrs = []netip.AddrPort{netip.AddrPortFrom(ip, target.port)}
	} else {
		found, err := lookup(ctx, target.host)
		if err != nil {
			return nil, transport(fmt.Sprintf("cannot resolve the host of %s", label))
		}
		for _, ip := range found {
			// An IPv4 address that the resolver returns in its IPv6-mapped form is the IPv4 address.
			addrs = append(addrs, netip.AddrPortFrom(ip.WithZone("").Unmap(), target.port))
		}
	}
	if len(addrs) == 0 {
		return nil, transport(fmt.Sprintf("the host of %s has no address", label))
	}
	if failure := checkWebhookAddresses(label, addrs, allowPrivate); failure != nil {
		return nil, failure
	}
	return addrs, nil
}

// checkWebhookAddresses is the refusal for a blocked address (the first one found), or nil.
func checkWebhookAddresses(label string, addrs []netip.AddrPort, allowPrivate bool) *webhookFailure {
	if allowPrivate {
		return nil
	}
	for _, a := range addrs {
		if isBlockedAddr(a.Addr()) {
			return refused(fmt.Sprintf("%s resolves to %s, a loopback, private, link-local or reserved address; add the webhook with --allow-private to allow it",
				label, a.Addr()))
		}
	}
	return nil
}

// pinnedDialer connects to the vetted addresses and nothing else, whatever address the transport asks for:
// the transport never looks the name up itself (no DNS rebinding).
func pinnedDialer(addrs []netip.AddrPort) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		var last error
		for _, a := range addrs {
			conn, err := d.DialContext(ctx, "tcp", a.String())
			if err == nil {
				return conn, nil
			}
			last = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, last
	}
}

// ---- TLS and the test seam ----------------------------------------------------------------------

// webhookSettings are what a test replaces for the whole process: the roots it trusts and the time limit. Set
// once by SetWebhookTestSettings; environment variables are not used because changing them in a running
// process is racy.
type webhookSettings struct {
	roots   *x509.CertPool
	timeout time.Duration
}

var webhookTest atomic.Pointer[webhookSettings]

// SetWebhookTestSettings sets, for the whole process, how outgoing webhooks reach https:// receivers: the PEM
// certificate of the test CA they trust (in place of the operating system's roots) and the time limit of one
// delivery (in place of 10 seconds). The first call wins and later ones are ignored, so tests that run in
// parallel agree. It reports false when caPEM holds no certificate. It does not change the other rules: the
// address check, the signature and the redirect rule stay as they are.
func SetWebhookTestSettings(caPEM string, timeout time.Duration) bool {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return false
	}
	webhookTest.CompareAndSwap(nil, &webhookSettings{roots: roots, timeout: timeout})
	return true
}

func webhookLimit() time.Duration {
	if s := webhookTest.Load(); s != nil {
		return s.timeout
	}
	return webhookTimeout
}

func webhookRoots() *x509.CertPool {
	if s := webhookTest.Load(); s != nil {
		return s.roots
	}
	return nil // the system's roots (SSL_CERT_FILE and SSL_CERT_DIR are honoured by crypto/x509)
}

// ---- signing ------------------------------------------------------------------------------------

// webhookSignature is sha256=<lower-case hex of HMAC-SHA256(secret, "<timestamp>.<body>")>.
func webhookSignature(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ---- the request --------------------------------------------------------------------------------

// outgoingWebhook is what one delivery sends.
type outgoingWebhook struct {
	url          string
	secret       string
	allowPrivate bool
	insecureHTTP bool
	event        string
	delivery     string
	body         []byte
}

// sendWebhook sends one delivery: the checks in the order of the comment at the top. It returns the HTTP status
// of the answer, whatever it is; a refusal or a network problem is a *webhookFailure. The whole call, the DNS
// lookup included, is limited to the time limit.
func sendWebhook(ctx context.Context, out outgoingWebhook) (int, *webhookFailure) {
	return sendWebhookWith(ctx, systemResolver, out)
}

func sendWebhookWith(ctx context.Context, lookup resolver, out outgoingWebhook) (int, *webhookFailure) {
	target, err := parseWebhookURL(out.url, out.insecureHTTP)
	if err != nil {
		return 0, refused(err.Error())
	}
	limit := webhookLimit()
	label := target.label()
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	status, failure := func() (int, *webhookFailure) {
		addrs, failure := vetWebhookHost(runCtx, lookup, target, out.allowPrivate)
		if failure != nil {
			return 0, failure
		}
		return sendPinned(runCtx, target, addrs, out)
	}()
	// The limit covers everything, the discarded body included: a deadline that fired is the same failure
	// whatever the step it fired in.
	if runCtx.Err() != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return 0, transport(fmt.Sprintf("%s did not answer within %s s", label, strconv.FormatFloat(limit.Seconds(), 'f', -1, 64)))
	}
	if ctx.Err() != nil && failure != nil && !failure.refused {
		return 0, transport(fmt.Sprintf("the delivery to %s was cancelled", label))
	}
	return status, failure
}

// sendPinned connects to addrs only (the name of the URL is used for the Host header and for TLS, never
// resolved) and posts the delivery.
func sendPinned(ctx context.Context, target *webhookTarget, addrs []netip.AddrPort, out outgoingWebhook) (int, *webhookFailure) {
	label := target.label()
	transportFor := func(what string) *webhookFailure { return transport(what + " " + label) }

	tr := &http.Transport{
		// No proxy is used (HTTPS_PROXY is not read), no keep-alive (one request on a fresh connection), no
		// compression and HTTP/1.1 only (an empty TLSNextProto map turns HTTP/2 off).
		Proxy:                 nil,
		DialContext:           pinnedDialer(addrs),
		DisableKeepAlives:     true,
		DisableCompression:    true,
		MaxIdleConnsPerHost:   -1,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		TLSClientConfig:       &tls.Config{RootCAs: webhookRoots(), MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   webhookLimit(),
		ResponseHeaderTimeout: webhookLimit(),
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		// A redirect is how an allowed host would send us to a blocked one: it is the answer, not followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	timestamp := time.Now().Unix()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.url.String(), bytes.NewReader(out.body))
	if err != nil {
		return 0, refused("the request cannot be built")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", webhookUserAgent())
	request.Header.Set("X-Forgebase-Event", out.event)
	request.Header.Set("X-Forgebase-Delivery", out.delivery)
	request.Header.Set("X-Forgebase-Timestamp", strconv.FormatInt(timestamp, 10))
	request.Header.Set("X-Forgebase-Signature", webhookSignature(out.secret, timestamp, out.body))

	response, err := client.Do(request)
	if err != nil {
		return 0, transportFor(classifyWebhookError(err))
	}
	// Read and discard at most webhookBodyLimit bytes; the connection is dropped with the transport.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, webhookBodyLimit))
	_ = response.Body.Close()
	return response.StatusCode, nil
}

// classifyWebhookError is a short, URL-free description of a client error, completed by the caller with the
// label of the target.
func classifyWebhookError(err error) string {
	// *url.Error holds the whole URL: look at what it wraps.
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if isTLSError(err) {
		return fmt.Sprintf("TLS error (%s) with", short(err.Error()))
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return "cannot connect to"
	}
	return "the request to"
}

func isTLSError(err error) bool {
	var (
		unknown  x509.UnknownAuthorityError
		hostname x509.HostnameError
		invalid  x509.CertificateInvalidError
		verify   *tls.CertificateVerificationError
		record   tls.RecordHeaderError
		alert    tls.AlertError
	)
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) ||
		errors.As(err, &verify) || errors.As(err, &record) || errors.As(err, &alert) {
		return true
	}
	text := err.Error()
	return strings.HasPrefix(text, "tls:") || strings.Contains(text, "x509:")
}

// short is the first 120 characters of text.
func short(text string) string {
	runes := []rune(text)
	if len(runes) > 120 {
		runes = runes[:120]
	}
	return string(runes)
}
