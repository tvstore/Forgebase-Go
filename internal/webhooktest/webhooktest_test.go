package webhooktest

import (
	"encoding/hex"
	"testing"
)

// Rust: common/mod.rs::the_independent_hmac_matches_a_known_vector
func TestTheIndependentHMACMatchesAKnownVector(t *testing.T) {
	// RFC 4231, test case 2.
	mac := hmacSHA256([]byte("Jefe"), []byte("what do ya want for nothing?"))
	if got := hex.EncodeToString(mac[:]); got != "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843" {
		t.Errorf("%s", got)
	}
}
