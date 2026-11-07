package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
)

// The webhook client without a network (M9.2, ADR 0020): the blocklist, the URL rules, resolving and vetting,
// the pinning and the signature. Ported from the #[cfg(test)] module of crates/forgebase/src/webhook_client.rs.
// The deliveries over real sockets, TLS included, are in the root package (webhooks_test.go).

func ip(t *testing.T, text string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(text)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return a
}

// Rust: webhook_client.rs::every_blocked_ipv4_range_with_its_edges
//
// Each range with its first and last address; the addresses next to the ranges are public.
func TestEveryBlockedIPv4RangeWithItsEdges(t *testing.T) {
	for _, r := range [][3]string{
		{"0.0.0.0/8", "0.0.0.0", "0.255.255.255"},
		{"10.0.0.0/8", "10.0.0.0", "10.255.255.255"},
		{"100.64.0.0/10", "100.64.0.0", "100.127.255.255"},
		{"127.0.0.0/8", "127.0.0.0", "127.255.255.255"},
		{"169.254.0.0/16", "169.254.0.0", "169.254.255.255"},
		{"172.16.0.0/12", "172.16.0.0", "172.31.255.255"},
		{"192.0.0.0/24", "192.0.0.0", "192.0.0.255"},
		{"192.168.0.0/16", "192.168.0.0", "192.168.255.255"},
		{"198.18.0.0/15", "198.18.0.0", "198.19.255.255"},
		{"224.0.0.0/4", "224.0.0.0", "239.255.255.255"},
		{"240.0.0.0/4", "240.0.0.0", "255.255.255.255"},
	} {
		if !isBlockedAddr(ip(t, r[1])) {
			t.Errorf("%s: first %s", r[0], r[1])
		}
		if !isBlockedAddr(ip(t, r[2])) {
			t.Errorf("%s: last %s", r[0], r[2])
		}
	}
	for _, public := range []string{
		"1.0.0.0", "9.255.255.255", "11.0.0.0", "100.63.255.255", "100.128.0.0", "126.255.255.255", "128.0.0.0",
		"169.253.255.255", "169.255.0.0", "172.15.255.255", "172.32.0.0", "191.255.255.255", "192.0.1.0",
		"192.167.255.255", "192.169.0.0", "198.17.255.255", "198.20.0.0", "223.255.255.255", "1.1.1.1", "8.8.8.8",
		"93.184.216.34",
	} {
		if isBlockedAddr(ip(t, public)) {
			t.Errorf("%s is public", public)
		}
	}
	if !isBlockedAddr(ip(t, "169.254.169.254")) {
		t.Error("the metadata address")
	}
	// The Azure WireServer is blocked as one address, not as a range: its neighbours are public.
	if !isBlockedAddr(ip(t, "168.63.129.16")) {
		t.Error("the Azure WireServer")
	}
	for _, neighbour := range []string{"168.63.129.15", "168.63.129.17", "168.63.128.16", "168.63.130.16", "168.62.129.16", "168.64.129.16"} {
		if isBlockedAddr(ip(t, neighbour)) {
			t.Errorf("%s is public", neighbour)
		}
	}
	if !isBlockedAddr(ip(t, "255.255.255.255")) {
		t.Error("broadcast")
	}
}

// Rust: webhook_client.rs::every_blocked_ipv6_range_with_its_edges
func TestEveryBlockedIPv6RangeWithItsEdges(t *testing.T) {
	for _, blocked := range []string{
		"::", "::1", "::2", "::7f00:1", "::ffff:ffff", "fc00::", "fd12:3456::1", "fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
		"fe80::", "fe80::1", "febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "ff00::", "ff02::1",
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
	} {
		if !isBlockedAddr(ip(t, blocked)) {
			t.Errorf("%s", blocked)
		}
	}
	for _, public := range []string{
		"2606:4700:4700::1111", "2001:4860:4860::8888", "fbff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "fe00::", "fec0::1",
		"::1:0:0", "1::",
	} {
		if isBlockedAddr(ip(t, public)) {
			t.Errorf("%s", public)
		}
	}
}

// Rust: webhook_client.rs::mapped_and_nat64_addresses_are_judged_by_the_ipv4_address_inside
func TestMappedAndNAT64AddressesAreJudgedByTheIPv4AddressInside(t *testing.T) {
	for _, blocked := range []string{
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "::ffff:168.63.129.16", "64:ff9b::a83f:8110",
		"::ffff:192.168.1.1", "::ffff:0.0.0.0", "64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "64:ff9b::10.1.2.3",
	} {
		if !isBlockedAddr(ip(t, blocked)) {
			t.Errorf("%s", blocked)
		}
	}
	for _, public := range []string{"::ffff:8.8.8.8", "::ffff:1.1.1.1", "64:ff9b::808:808"} {
		if isBlockedAddr(ip(t, public)) {
			t.Errorf("%s", public)
		}
	}
	// The prefix itself matters: 64:ff9b:1::/48 is not the well-known prefix.
	if isBlockedAddr(ip(t, "64:ff9b:1::7f00:1")) {
		t.Error("64:ff9b:1::7f00:1")
	}
}

func parseOK(t *testing.T, raw string, insecure bool) *webhookTarget {
	t.Helper()
	target, err := parseWebhookURL(raw, insecure)
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return target
}

func fakeResolver(answers map[string][]string) resolver {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		texts, ok := answers[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, text := range texts {
			out = append(out, netip.MustParseAddr(text))
		}
		return out, nil
	}
}

// Rust: webhook_client.rs::a_name_that_resolves_to_a_private_address_is_refused_unless_allowed
func TestANameThatResolvesToAPrivateAddressIsRefusedUnlessAllowed(t *testing.T) {
	target := parseOK(t, "https://localhost/hook", false)
	_, failure := vetWebhookHost(context.Background(), systemResolver, target, false)
	if failure == nil || !failure.refused || !strings.Contains(failure.message, "--allow-private") || strings.Contains(failure.message, "/hook") {
		t.Fatalf("expected a refusal without the path, got %+v", failure)
	}
	addrs, failure := vetWebhookHost(context.Background(), systemResolver, target, true)
	if failure != nil || len(addrs) == 0 {
		t.Fatalf("allowed: %+v %v", failure, addrs)
	}
	for _, a := range addrs {
		if a.Port() != 443 || !a.Addr().IsLoopback() {
			t.Errorf("%v", a)
		}
	}
}

// A name that resolves to several addresses is refused when any one of them is blocked: a public address
// next to a private one is no way in.
func TestOneBlockedAddressAmongSeveralRefusesTheDelivery(t *testing.T) {
	lookup := fakeResolver(map[string][]string{
		"mixed.test":  {"93.184.216.34", "10.0.0.5"},
		"public.test": {"93.184.216.34", "2606:4700:4700::1111"},
		"empty.test":  {},
	})
	ctx := context.Background()
	_, failure := vetWebhookHost(ctx, lookup, parseOK(t, "https://mixed.test/x", false), false)
	if failure == nil || !failure.refused || !strings.Contains(failure.message, "10.0.0.5") {
		t.Errorf("%+v", failure)
	}
	if _, failure := vetWebhookHost(ctx, lookup, parseOK(t, "https://mixed.test/x", false), true); failure != nil {
		t.Errorf("allowed: %+v", failure)
	}
	addrs, failure := vetWebhookHost(ctx, lookup, parseOK(t, "https://public.test:8443/x", false), false)
	if failure != nil || len(addrs) != 2 || addrs[0].String() != "93.184.216.34:8443" {
		t.Errorf("%v %+v", addrs, failure)
	}
	// No answer, or a lookup that fails, is a network problem (retryable), not a refusal.
	for _, host := range []string{"empty.test", "unknown.test"} {
		_, failure := vetWebhookHost(ctx, lookup, parseOK(t, "https://"+host+"/x", false), false)
		if failure == nil || failure.refused {
			t.Errorf("%s: %+v", host, failure)
		}
	}
}

// Rust: webhook_client.rs::ip_literals_are_vetted_without_a_lookup
func TestIPLiteralsAreVettedWithoutALookup(t *testing.T) {
	neverLooksUp := func(context.Context, string) ([]netip.Addr, error) {
		t.Error("an IP literal was looked up")
		return nil, errors.New("no")
	}
	for _, blocked := range []string{
		"https://127.0.0.1/", "https://10.1.2.3:8443/x", "https://169.254.169.254/latest/meta-data", "https://[::1]/",
		"https://[::ffff:127.0.0.1]/", "https://[fe80::1]/",
	} {
		target := parseOK(t, blocked, false)
		if _, failure := vetWebhookHost(context.Background(), neverLooksUp, target, false); failure == nil || !failure.refused {
			t.Errorf("%s: %+v", blocked, failure)
		}
		if _, failure := vetWebhookHost(context.Background(), neverLooksUp, target, true); failure != nil {
			t.Errorf("%s allowed: %+v", blocked, failure)
		}
	}
	public := parseOK(t, "https://93.184.216.34:8443/x", false)
	addrs, failure := vetWebhookHost(context.Background(), neverLooksUp, public, false)
	if failure != nil || len(addrs) != 1 || addrs[0].String() != "93.184.216.34:8443" {
		t.Errorf("%v %+v", addrs, failure)
	}
}

// Rust: webhook_client.rs::the_connector_is_given_only_the_vetted_addresses
func TestTheDialerIsGivenOnlyTheVettedAddresses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	dial := pinnedDialer([]netip.AddrPort{netip.MustParseAddrPort(listener.Addr().String())})
	// Whatever address it is asked for, it connects to the vetted one and nothing else.
	for _, asked := range []string{"example.com:443", "localhost:80", "rebind.test:8443", "203.0.113.7:9"} {
		conn, err := dial(context.Background(), "tcp", asked)
		if err != nil {
			t.Fatalf("%s: %v", asked, err)
		}
		if conn.RemoteAddr().String() != listener.Addr().String() {
			t.Errorf("%s: connected to %s", asked, conn.RemoteAddr())
		}
		_ = conn.Close()
	}
	// A vetted address that refuses the connection is an error, not a lookup of something else.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := closed.Addr().String()
	_ = closed.Close()
	if _, err := pinnedDialer([]netip.AddrPort{netip.MustParseAddrPort(address)})(context.Background(), "tcp", "example.com:443"); err == nil {
		t.Error("connected to a closed port")
	}
}

// Rust: webhook_client.rs::the_name_of_the_url_is_never_resolved_again
//
// The delivery connects to the vetted address and never resolves the name itself: a host that no resolver can
// answer (.invalid, RFC 6761) is reached through the address it is given. With a transport that looked the name
// up, this would fail.
func TestTheNameOfTheURLIsNeverResolvedAgain(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	seen := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			seen <- ""
			return
		}
		defer func() { _ = conn.Close() }()
		var request []byte
		chunk := make([]byte, 1024)
		for !strings.Contains(string(request), "\r\n\r\n") {
			n, err := conn.Read(chunk)
			if err != nil {
				break
			}
			request = append(request, chunk[:n]...)
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 204 No Content\r\nconnection: close\r\n\r\n")
		seen <- string(request)
	}()
	target := parseOK(t, fmt.Sprintf("http://rebind.invalid:%d/hook", port), true)
	// The name does not resolve for us either.
	if _, failure := vetWebhookHost(context.Background(), systemResolver, target, true); failure == nil || failure.refused {
		t.Fatalf("%+v", failure)
	}
	out := outgoingWebhook{url: "unused", secret: "s", allowPrivate: true, insecureHTTP: true, event: "test", delivery: "d-1", body: []byte("{}")}
	vetted := []netip.AddrPort{netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port))}
	status, failure := sendPinned(context.Background(), target, vetted, out)
	if status != 204 || failure != nil {
		t.Fatalf("%d %+v", status, failure)
	}
	request := <-seen
	if !strings.HasPrefix(request, "POST /hook HTTP/1.1\r\n") {
		t.Errorf("%s", request)
	}
	// The Host header keeps the name of the URL.
	if !strings.Contains(strings.ToLower(request), fmt.Sprintf("host: rebind.invalid:%d", port)) {
		t.Errorf("%s", request)
	}
}

// Rust: webhook_client.rs::urls_are_checked
func TestWebhookURLsAreChecked(t *testing.T) {
	for _, ok := range []struct {
		url      string
		insecure bool
	}{
		{"https://example.com/hook", false},
		{"https://example.com:8443/hook?token=x", false},
		{"https://[::1]:9/", false},
		{"http://example.com/hook", true},
		{"https://example.com", false},
		{"https://example.com?x=1", false},
		{"HTTPS://Example.com/hook", false},
	} {
		if _, err := parseWebhookURL(ok.url, ok.insecure); err != nil {
			t.Errorf("%s: %v", ok.url, err)
		}
	}
	for _, bad := range []struct {
		url      string
		insecure bool
		message  string
	}{
		{"http://example.com/hook", false, "--insecure-http"},
		{"ftp://example.com/", true, "must be https"},
		{"https://user:pass@example.com/", false, "user info"},
		{"https://user@example.com/", false, "user info"},
		{"https://example.com@evil.example/", false, "user info"},
		{"example.com/hook", false, "valid absolute URL"},
		{"/hook", false, "must start with https://"},
		{"https://", false, "valid absolute URL"},
		{"", false, "valid absolute URL"},
		{"not a url", false, "valid absolute URL"},
		{"https://example.com:99999/", false, "valid absolute URL"},
		{"https://example.com:abc/", false, "valid absolute URL"},
		{"https://exam ple.com/", false, "valid absolute URL"},
		{"https://:443/", false, "no host"},
		{"https://a@b@c/", false, "valid absolute URL"},
		{"https://example.com/" + strings.Repeat("a", maxWebhookURLBytes), false, "longer than"},
	} {
		_, err := parseWebhookURL(bad.url, bad.insecure)
		if err == nil || !strings.Contains(err.Error(), bad.message) {
			t.Errorf("%q: %v, want %q", bad.url, err, bad.message)
		}
	}
	// The request target is the path and the query as written, without the fragment.
	target := parseOK(t, "https://example.com:8443/hook%20x/y?token=a%2Fb&k=v#frag", false)
	if got := target.url.RequestURI(); got != "/hook%20x/y?token=a%2Fb&k=v" {
		t.Errorf("%s", got)
	}
	if got := parseOK(t, "https://example.com", false).url.RequestURI(); got != "/" {
		t.Errorf("%s", got)
	}
	if got := parseOK(t, "https://example.com?x=1", false).url.RequestURI(); got != "/?x=1" {
		t.Errorf("%s", got)
	}
}

// Rust: webhook_client.rs::errors_and_labels_hide_the_path_and_the_query
func TestErrorsAndLabelsHideThePathAndTheQuery(t *testing.T) {
	target := parseOK(t, "https://hooks.example.com:8443/services/T0/B0/TOKEN?k=v", false)
	if target.label() != "https://hooks.example.com:8443" {
		t.Errorf("%s", target.label())
	}
	if got := parseOK(t, "https://[2001:db8::1]/x/TOKEN", false).label(); got != "https://[2001:db8::1]:443" {
		t.Errorf("%s", got)
	}
	if got := parseOK(t, "http://example.com/x", true).label(); got != "http://example.com:80" {
		t.Errorf("%s", got)
	}
	for _, bad := range []string{"http://example.com/TOKEN", "https://u:p@example.com/TOKEN"} {
		_, err := parseWebhookURL(bad, false)
		if err == nil || strings.Contains(err.Error(), "TOKEN") || strings.Contains(err.Error(), "example.com") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

// Rust: webhook_client.rs::the_signature_has_the_documented_form
//
// The signature is HMAC-SHA256 over timestamp.body, written as sha256=<hex>. The expected value is computed
// independently with Python's hmac: hmac.new(b"key", b"1700000000.{}", hashlib.sha256).hexdigest().
func TestTheSignatureHasTheDocumentedForm(t *testing.T) {
	signed := webhookSignature("key", 1_700_000_000, []byte("{}"))
	if signed != "sha256=9d713ed406bb7076d4123f0dc2c39d2df5c654ed4b0cd56b52c8b4c940bd63ae" {
		t.Errorf("%s", signed)
	}
	// Changing the timestamp, the body or the secret changes the signature.
	for _, other := range []string{
		webhookSignature("key", 1_700_000_001, []byte("{}")),
		webhookSignature("key", 1_700_000_000, []byte("{ }")),
		webhookSignature("other", 1_700_000_000, []byte("{}")),
	} {
		if other == signed {
			t.Error("the signature did not change")
		}
	}
}

// A message about a transport error never holds the URL, whatever net/http wrapped it in.
func TestTransportErrorsAreClassifiedWithoutTheURL(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()
	target := parseOK(t, fmt.Sprintf("http://127.0.0.1:%d/hook-path?token=TOPSECRET", port), true)
	vetted := []netip.AddrPort{netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port))}
	_, failure := sendPinned(context.Background(), target, vetted, outgoingWebhook{secret: "s", event: "test", delivery: "d", body: []byte("{}")})
	if failure == nil || failure.refused || !strings.HasPrefix(failure.message, "cannot connect to http://127.0.0.1:") {
		t.Fatalf("%+v", failure)
	}
	for _, forbidden := range []string{"hook-path", "TOPSECRET", "token="} {
		if strings.Contains(failure.message, forbidden) {
			t.Errorf("%s in %s", forbidden, failure.message)
		}
	}
}
