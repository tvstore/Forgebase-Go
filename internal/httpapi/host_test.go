package httpapi

import "testing"

// Rust: forgebase-http lib.rs is_wellformed_host (exercised by config.rs::host_rules_match_the_allowed_hosts_rules).
func TestIsWellformedHost(t *testing.T) {
	for _, good := range []string{"localhost", "127.0.0.1", "[::1]", "erp.example.com", "a_b-c", "[2001:DB8::1]"} {
		if !IsWellformedHost(good) {
			t.Errorf("%q should be a well-formed host", good)
		}
	}
	for _, bad := range []string{"", "a:80", "[::1]:80", "::1", "a b", "[::1", "é.test", "a/b", "a@b", "[]", "a,b", "*.x", "[g::1]", "::1]"} {
		if IsWellformedHost(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
}
