package db

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Unit tests of the rules and the password hashing of auth.go (no database). Ported from the
// #[cfg(test)] module of crates/forgebase-db/src/auth.rs, plus the tests of the PHC compatibility.

// Rust: auth.rs::username_rules
func TestUsernameRules(t *testing.T) {
	for _, ok := range []string{"alice", "a.b_c@d-e1", strings.Repeat("a", 140)} {
		if checkUsername(ok) != nil {
			t.Errorf("%q", ok)
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", 141), "a b", "a/b", "é", "a\nb", "a;b"} {
		if checkUsername(bad) == nil {
			t.Errorf("%q", bad)
		}
	}
	// The messages are the Rust ones.
	if got := checkUsername("").Error(); got != "invalid username: must be 1 to 140 characters" {
		t.Error(got)
	}
	if got := checkUsername("a b").Error(); got != "invalid username: may only contain ASCII letters, digits and _ . @ -" {
		t.Error(got)
	}
}

// Rust: auth.rs::role_rules
func TestRoleRules(t *testing.T) {
	for _, ok := range []string{"Sales Manager", "Verkäufer", strings.Repeat("r", 140)} {
		if checkRole(ok) != nil {
			t.Errorf("%q", ok)
		}
	}
	for _, bad := range []string{"", "   ", "a\tb", "a\x00b", strings.Repeat("r", 141)} {
		if checkRole(bad) == nil {
			t.Errorf("%q", bad)
		}
	}
}

// Rust: auth.rs::an_auth_user_becomes_a_user_caller
func TestAnAuthUserBecomesAUserCaller(t *testing.T) {
	u := AuthUser{Username: "ann", Roles: []string{"Sales User"}}
	if got, want := fmt.Sprintf("%#v", u.Caller()), fmt.Sprintf("%#v", core.UserCaller("ann", []string{"Sales User"})); got != want {
		t.Errorf("%s\n%s", got, want)
	}
	// The user permissions go into the caller.
	u.Restrictions = core.NewUserRestrictions([][2]string{{"group", "Retail"}})
	want := core.UserCaller("ann", []string{"Sales User"}).WithRestrictions(core.NewUserRestrictions([][2]string{{"group", "Retail"}}))
	if got := fmt.Sprintf("%#v", u.Caller()); got != fmt.Sprintf("%#v", want) {
		t.Errorf("%s", got)
	}
	if names := u.Caller().Restrictions.Allowed("group"); len(names) != 1 || names[0] != "Retail" {
		t.Error(names)
	}
}

// Rust: auth.rs::password_rules
func TestPasswordRules(t *testing.T) {
	for _, ok := range []int{8, 1024} {
		if checkPassword(strings.Repeat("p", ok)) != nil {
			t.Error(ok)
		}
	}
	for _, bad := range []int{0, 7, 1025} {
		if checkPassword(strings.Repeat("p", bad)) == nil {
			t.Error(bad)
		}
	}
	// The limits are in bytes: three two-byte characters are six bytes.
	if checkPassword("éééé") != nil || checkPassword("ééé") == nil {
		t.Error("the limits are bytes")
	}
}

// Rust: auth.rs::random_hex_is_lowercase_hex_of_the_right_length
func TestRandomHexIsLowercaseHexOfTheRightLength(t *testing.T) {
	a, err := randomHex(32)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := randomHex(32)
	if !isHex(a, 64) || a == b || isHex(strings.ToUpper(a), 64) || isHex("abc", 64) {
		t.Error(a, b)
	}
	if k, _ := randomHex(16); !isHex(k, 32) {
		t.Error(k)
	}
}

// Rust: auth.rs::hashes_verify_and_use_the_documented_parameters
func TestHashesVerifyAndUseTheDocumentedParameters(t *testing.T) {
	hash, err := hashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Error(hash)
	}
	if !verifyPHC([]byte("correct horse"), hash) || verifyPHC([]byte("wrong horse"), hash) {
		t.Error("verification")
	}
	again, _ := hashPassword("correct horse")
	if hash == again {
		t.Error("the salt must be random")
	}
	// 16 bytes of salt and 32 of output, both unpadded base64.
	parsed, ok := parsePHC(hash)
	if !ok || len(parsed.salt) != 16 || len(parsed.hash) != 32 || strings.HasSuffix(hash, "=") || strings.Contains(hash, "=$") {
		t.Errorf("%+v", parsed)
	}
}

// Rust: auth.rs::the_dummy_hash_is_a_constant_valid_hash_with_the_current_parameters
func TestTheDummyHashIsAConstantValidHashWithTheCurrentParameters(t *testing.T) {
	hash := dummyHash()
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") || hash != dummyHash() {
		t.Error(hash)
	}
	if !verifyPHC([]byte("forgebase dummy password"), hash) {
		t.Error("the dummy hash does not verify")
	}
	// verifyPassword never reports a match for the dummy.
	if verifyPassword("forgebase dummy password", nil) {
		t.Error("the dummy password matched")
	}
}

// Rust: auth.rs::debug_output_redacts_secrets
func TestFormattedOutputRedactsSecrets(t *testing.T) {
	session := LoginSession{Token: "tok-secret", CSRFToken: "csrf-secret", User: AuthUser{Username: "alice"}}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		for _, v := range []any{session, &session} {
			if text := fmt.Sprintf(verb, v); strings.Contains(text, "tok-secret") || strings.Contains(text, "csrf-secret") || !strings.Contains(text, "alice") {
				t.Errorf("%s: %s", verb, text)
			}
		}
	}
	token := APITokenSecret{Key: "the-key", Secret: "api-secret"}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		if text := fmt.Sprintf(verb, token); !strings.Contains(text, "the-key") || strings.Contains(text, "api-secret") {
			t.Errorf("%s: %s", verb, text)
		}
	}
}

// ---- compatibility with the Rust argon2 and password-hash crates --------------------------------------

// The reference example of the Argon2 authors (phc-winner-argon2, README): the password "password",
// the salt "somesalt", Argon2i (the default type of that program), t = 2, m = 65536 KiB, p = 4, 24
// bytes of output. It has other
// parameters than ours and another output length than ours, so verification must read them from the
// string, as the Rust verifier does.
const (
	referenceHashHex = "45d7ac72e76f242b20b77b9bf9bf9d5915894e669a24e6c6"
	referencePHC     = "$argon2i$v=19$m=65536,t=2,p=4$c29tZXNhbHQ$RdescudvJCsgt3ub+b+dWRWJTmaaJObG"
)

// A PHC string written by another implementation (the reference one) verifies, and a wrong password
// does not: the parameters come from the string.
func TestAPHCStringFromAnotherImplementationVerifiesWithItsOwnParameters(t *testing.T) {
	key := argon2.Key([]byte("password"), []byte("somesalt"), 2, 65536, 4, 24)
	if hex.EncodeToString(key) != referenceHashHex {
		t.Fatalf("the reference hash is %x", key)
	}
	if !verifyPHC([]byte("password"), referencePHC) {
		t.Error("the reference hash does not verify")
	}
	if verifyPHC([]byte("passwore"), referencePHC) {
		t.Error("a wrong password verifies")
	}
	parsed, ok := parsePHC(referencePHC)
	if !ok || parsed.memory != 65536 || parsed.time != 2 || parsed.lanes != 4 || string(parsed.salt) != "somesalt" || len(parsed.hash) != 24 {
		t.Errorf("%+v", parsed)
	}
}

// What the Rust code writes, as the PHC specification and the Rust crates define it: the string of the
// current parameters for a given password and salt, built by hand from the reference function. Both
// directions: the string we write is the string of that construction, and a string of that
// construction verifies.
func TestOurHashIsTheStringTheRustCodeWouldWrite(t *testing.T) {
	salt := []byte("forgebase-dummy!") // the salt of the dummy hash of the Rust code: 16 bytes
	key := argon2.IDKey([]byte("forgebase dummy password"), salt, 2, 19456, 1, 32)
	want := "$argon2id$v=19$m=19456,t=2,p=1$" + phcB64.EncodeToString(salt) + "$" + phcB64.EncodeToString(key)
	if got := hashWithSalt([]byte("forgebase dummy password"), salt); got != want {
		t.Errorf("%s\n%s", got, want)
	}
	// The salt part is the base64 of the 16 raw bytes (no padding), the hash part that of 32 bytes.
	if !strings.HasPrefix(want, "$argon2id$v=19$m=19456,t=2,p=1$Zm9yZ2ViYXNlLWR1bW15IQ$") {
		t.Error(want)
	}
	if !verifyPHC([]byte("forgebase dummy password"), want) {
		t.Error("a string of that construction does not verify")
	}
	// Our own hashes verify under another reading of the same string: parse and recompute by hand.
	own, _ := hashPassword("another password")
	p, ok := parsePHC(own)
	if !ok {
		t.Fatal(own)
	}
	if got := argon2.IDKey([]byte("another password"), p.salt, p.time, p.memory, uint8(p.lanes), uint32(len(p.hash))); hex.EncodeToString(got) != hex.EncodeToString(p.hash) {
		t.Error("our hash is not the plain Argon2id of the password")
	}
}

// Strings written by the reference C implementation (argon2-cffi 25.1, which writes what the Rust
// argon2 and password-hash crates write and read) verify here, and what we write verifies there (this
// was checked by hand with the same library for the dummy hash and a random one). The first has our
// parameters, the second the defaults of that library (m = 65536, t = 3, p = 4).
func TestStringsOfTheReferenceImplementationVerify(t *testing.T) {
	const ours = "$argon2id$v=19$m=19456,t=2,p=1$Zm9yZ2ViYXNlLWR1bW15IQ$ruLxhDpb32exyqjYzidGmSDO9yShDE61gf68BaQMmdU"
	const theirs = "$argon2id$v=19$m=65536,t=3,p=4$hhjhjRhaFG7jQTww3cvZxg$MzRzcPtaB6hdxDiNc2E724dI8AADfAP6VvfNnCEX+fI"
	if got := hashWithSalt([]byte("forgebase dummy password"), []byte("forgebase-dummy!")); got != ours {
		t.Errorf("we write %s", got)
	}
	if !verifyPHC([]byte("forgebase dummy password"), ours) || verifyPHC([]byte("forgebase dummy passwore"), ours) {
		t.Error("ours")
	}
	if !verifyPHC([]byte("hello world password"), theirs) || verifyPHC([]byte("hello world passworD"), theirs) {
		t.Error("theirs")
	}
}

// Parameters other than ours verify (the parameters can be raised later), and strings that are not
// verifiable hashes of this code never match.
func TestVerificationUsesTheParametersInsideTheStringAndRefusesMalformedOnes(t *testing.T) {
	salt := []byte("0123456789abcdef")
	other := func(version, params string, key []byte) string {
		return "$argon2id" + version + "$" + params + "$" + phcB64.EncodeToString(salt) + "$" + phcB64.EncodeToString(key)
	}
	raised := argon2.IDKey([]byte("raised password"), salt, 3, 64, 2, 32)
	for _, c := range []struct {
		name, hash string
		verifies   bool
	}{
		{"raised parameters", other("$v=19", "m=64,t=3,p=2", raised), true},
		{"no version: 19, as the Rust Version::default", other("", "m=64,t=3,p=2", raised), true},
		{"parameters in another order", other("$v=19", "p=2,t=3,m=64", raised), true},
		{"version 16 is not supported", other("$v=16", "m=64,t=3,p=2", raised), false},
		{"wrong parameters", other("$v=19", "m=64,t=2,p=2", raised), false},
		{"a missing parameter", other("$v=19", "m=64,t=3", raised), false},
		{"an unknown parameter", other("$v=19", "m=64,t=3,p=2,x=1", raised), false},
		{"a repeated parameter", other("$v=19", "m=64,m=64,t=3,p=2", raised), false},
		{"memory below 8 per lane", other("$v=19", "m=15,t=3,p=2", raised), false},
		{"no passes", other("$v=19", "m=64,t=0,p=2", raised), false},
		{"no lanes", other("$v=19", "m=64,t=3,p=0", raised), false},
		{"too many lanes", other("$v=19", "m=2048,t=3,p=256", raised), false},
		{"a short hash", other("$v=19", "m=64,t=3,p=2", raised[:3]), false},
		{"a signed number", other("$v=19", "m=+64,t=3,p=2", raised), false},
		{"an empty string", "", false},
		{"no hash part", "$argon2id$v=19$m=64,t=3,p=2$" + phcB64.EncodeToString(salt), false},
		{"another algorithm", strings.Replace(other("$v=19", "m=64,t=3,p=2", raised), "argon2id", "scrypt", 1), false},
		{"padded base64", other("$v=19", "m=64,t=3,p=2", raised) + "=", false},
		{"plain text", "raised password", false},
	} {
		if got := verifyPHC([]byte("raised password"), c.hash); got != c.verifies {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	// argon2i is read too (the Rust verifier takes the algorithm from the string).
	ki := argon2.Key([]byte("pw"), salt, 2, 64, 1, 32)
	if !verifyPHC([]byte("pw"), "$argon2i$v=19$m=64,t=2,p=1$"+phcB64.EncodeToString(salt)+"$"+phcB64.EncodeToString(ki)) {
		t.Error("argon2i")
	}
	// A malformed stored hash is never a match, whatever the password.
	bad := "not a hash"
	if verifyPassword("not a hash", &bad) {
		t.Error("a malformed hash matched")
	}
}
