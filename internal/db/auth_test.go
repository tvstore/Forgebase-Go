package db_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tvstore/forgebase-go/internal/db"
	"github.com/tvstore/forgebase-go/internal/db/dbtest"
)

// Users, sessions, API tokens and login lockout against a real PostgreSQL. Ported from
// crates/forgebase-db/tests/auth.rs.

const authPassword = "correct horse battery"

// authSetup is a database with the framework tables and the user alice (password authPassword).
func authSetup(t *testing.T) *dbtest.DB {
	t.Helper()
	d := dbtest.Migrated(t)
	if _, err := db.CreateUser(bg, d.Pool, "alice", authPassword, []string{"Sales", "Accounts"}); err != nil {
		t.Fatal(err)
	}
	return d
}

func loginAlice(t *testing.T, d *dbtest.DB) *db.LoginSession {
	t.Helper()
	s, err := db.Login(bg, d.Pool, "alice", authPassword)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func tryLogin(d *dbtest.DB, username, password string) (*db.LoginSession, error) {
	return db.Login(bg, d.Pool, username, password)
}

func sessionUser(t *testing.T, d *dbtest.DB, token string) *db.AuthUser {
	t.Helper()
	u, err := db.ValidateSession(bg, d.Pool, token)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func scalar(d *dbtest.DB, sql string) string { return d.Rows(sql)[0][0] }

// authKind checks that err is an *db.AuthError of kind.
func authKind(t *testing.T, err error, kind db.AuthErrorKind) *db.AuthError {
	t.Helper()
	var ae *db.AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("expected an *AuthError of kind %d, got %T: %v", kind, err, err)
	}
	if ae.Kind != kind {
		t.Fatalf("expected an AuthError of kind %d, got kind %d: %v", kind, ae.Kind, err)
	}
	return ae
}

func mustLogout(t *testing.T, d *dbtest.DB, token string) bool {
	t.Helper()
	ok, err := db.Logout(bg, d.Pool, token)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func mustCheckCSRF(t *testing.T, d *dbtest.DB, session, csrf string) bool {
	t.Helper()
	ok, err := db.CheckCSRF(bg, d.Pool, session, csrf)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func mustRotate(t *testing.T, d *dbtest.DB, session string) string {
	t.Helper()
	s, err := db.RotateCSRF(bg, d.Pool, session)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validAPI(t *testing.T, d *dbtest.DB, key, secret string) *db.AuthUser {
	t.Helper()
	u, err := db.ValidateAPIToken(bg, d.Pool, key, secret)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Rust: auth.rs::the_auth_tables_have_the_documented_columns
func TestTheAuthTablesHaveTheDocumentedColumns(t *testing.T) {
	d := dbtest.Migrated(t)
	columns := func(table string) []string {
		var out []string
		for _, c := range d.Columns(table) {
			out = append(out, c[0]+" "+c[1])
		}
		return out
	}
	for table, want := range map[string][]string{
		"fb_user": {"id uuid/NO", "username text/NO", "password_hash text/NO", "enabled boolean/NO",
			"created_at timestamp with time zone/NO"},
		"fb_session": {"token_hash bytea/NO", "csrf_hash bytea/NO", "user_id uuid/NO",
			"created_at timestamp with time zone/NO", "last_seen_at timestamp with time zone/NO",
			"expires_at timestamp with time zone/NO"},
		"fb_api_token": {"key text/NO", "secret_hash bytea/NO", "user_id uuid/NO",
			"created_at timestamp with time zone/NO", "revoked_at timestamp with time zone/YES"},
		"fb_login_failure": {"username text/NO", "failures integer/NO", "locked_until timestamp with time zone/YES",
			"last_failure_at timestamp with time zone/YES"},
		"fb_user_role": {"user_id uuid/NO", "role text/NO"},
	} {
		if got := columns(table); !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", table, got, want)
		}
	}
}

// Rust: auth.rs::create_user_stores_a_hash_and_the_roles
func TestCreateUserStoresAHashAndTheRoles(t *testing.T) {
	d := authSetup(t)
	hash := scalar(d, "SELECT password_hash FROM fb_user WHERE username = 'alice'")
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") || strings.Contains(hash, authPassword) {
		t.Error(hash)
	}
	want(t, d.Rows("SELECT role FROM fb_user_role ORDER BY role"), [][]string{{"Accounts"}, {"Sales"}}, "roles")
	if scalar(d, "SELECT enabled::text FROM fb_user") != "true" {
		t.Error("not enabled")
	}
}

// Rust: auth.rs::duplicate_and_case_different_usernames
func TestDuplicateAndCaseDifferentUsernames(t *testing.T) {
	d := authSetup(t)
	_, err := db.CreateUser(bg, d.Pool, "alice", authPassword, nil)
	authKind(t, err, db.AuthUserExists)
	// Usernames are case-sensitive.
	if _, err := db.CreateUser(bg, d.Pool, "Alice", authPassword, nil); err != nil {
		t.Fatal(err)
	}
	if scalar(d, "SELECT count(*)::text FROM fb_user") != "2" {
		t.Error("two users expected")
	}
}

// Rust: auth.rs::invalid_usernames_roles_and_passwords_are_rejected
func TestInvalidUsernamesRolesAndPasswordsAreRejected(t *testing.T) {
	d := dbtest.Migrated(t)
	for _, bad := range []string{"", "a b", "a/b", "ä", strings.Repeat("a", 141)} {
		_, err := db.CreateUser(bg, d.Pool, bad, authPassword, nil)
		authKind(t, err, db.AuthInvalidUsername)
	}
	if _, err := db.CreateUser(bg, d.Pool, strings.Repeat("a", 140), authPassword, nil); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "  ", "a\nb", strings.Repeat("r", 141)} {
		_, err := db.CreateUser(bg, d.Pool, "bob", authPassword, []string{bad})
		authKind(t, err, db.AuthInvalidRole)
	}
	for _, bad := range []string{"1234567", strings.Repeat("p", 1025)} {
		_, err := db.CreateUser(bg, d.Pool, "bob", bad, nil)
		authKind(t, err, db.AuthInvalidPassword)
		// The message never contains the password.
		if strings.Contains(err.Error(), bad) {
			t.Error("the error contains the password")
		}
	}
	if _, err := db.CreateUser(bg, d.Pool, "bob", "12345678", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateUser(bg, d.Pool, "carol", strings.Repeat("p", 1024), nil); err != nil {
		t.Fatal(err)
	}
	// Nothing was written for the rejected ones.
	if scalar(d, "SELECT count(*)::text FROM fb_user") != "3" {
		t.Error("three users expected")
	}
}

// Rust: auth.rs::roles_can_be_added_and_removed
func TestRolesCanBeAddedAndRemoved(t *testing.T) {
	d := authSetup(t)
	step := func(f func() (bool, error), wantResult bool, label string) {
		t.Helper()
		got, err := f()
		if err != nil || got != wantResult {
			t.Errorf("%s: %v %v", label, got, err)
		}
	}
	step(func() (bool, error) { return db.AddRole(bg, d.Pool, "alice", "Admin") }, true, "add Admin")
	step(func() (bool, error) { return db.AddRole(bg, d.Pool, "alice", "Admin") }, false, "add Admin again")
	step(func() (bool, error) { return db.RemoveRole(bg, d.Pool, "alice", "Sales") }, true, "remove Sales")
	step(func() (bool, error) { return db.RemoveRole(bg, d.Pool, "alice", "Sales") }, false, "remove Sales again")
	session := loginAlice(t, d)
	want(t, session.User.Roles, []string{"Accounts", "Admin"}, "roles")
	_, err := db.AddRole(bg, d.Pool, "nobody", "Admin")
	authKind(t, err, db.AuthUserNotFound)
	_, err = db.AddRole(bg, d.Pool, "alice", " ")
	authKind(t, err, db.AuthInvalidRole)
}

// Rust: auth.rs::login_returns_tokens_and_the_database_holds_only_hashes
func TestLoginReturnsTokensAndTheDatabaseHoldsOnlyHashes(t *testing.T) {
	d := authSetup(t)
	session := loginAlice(t, d)
	if session.User.Username != "alice" || !slices.Equal(session.User.Roles, []string{"Accounts", "Sales"}) {
		t.Errorf("%+v", session.User)
	}
	if len(session.Token) != 64 || len(session.CSRFToken) != 64 || session.Token == session.CSRFToken {
		t.Error("token shapes")
	}
	// No column of any auth table equals (or contains) a secret.
	for _, table := range []string{"fb_user", "fb_session", "fb_api_token", "fb_login_failure"} {
		dump := scalar(d, "SELECT coalesce(string_agg(t::text, ' '), '') FROM "+table+" t")
		for _, secret := range []string{authPassword, session.Token, session.CSRFToken} {
			if strings.Contains(dump, secret) {
				t.Errorf("%s contains a secret", table)
			}
		}
	}
	// The stored digest is the SHA-256 of the token.
	got := scalar(d, fmt.Sprintf("SELECT (token_hash = sha256(convert_to('%s', 'UTF8')) AND "+
		"csrf_hash = sha256(convert_to('%s', 'UTF8')))::text FROM fb_session", session.Token, session.CSRFToken))
	if got != "true" {
		t.Error("the stored digests are not the SHA-256 of the tokens")
	}
	// Absolute expiry: 7 days.
	if got := scalar(d, "SELECT round(extract(epoch FROM expires_at - created_at) / 86400)::text FROM fb_session"); got != "7" {
		t.Error(got)
	}
	if d := time.Until(session.ExpiresAt); d < 6*24*time.Hour || d > 7*24*time.Hour {
		t.Errorf("ExpiresAt in %v", d)
	}
}

// Rust: auth.rs::every_login_creates_a_new_session
func TestEveryLoginCreatesANewSession(t *testing.T) {
	d := authSetup(t)
	first, second := loginAlice(t, d), loginAlice(t, d)
	if first.Token == second.Token || first.CSRFToken == second.CSRFToken {
		t.Error("tokens repeat")
	}
	if scalar(d, "SELECT count(*)::text FROM fb_session") != "2" {
		t.Error("two sessions expected")
	}
	if sessionUser(t, d, first.Token) == nil || sessionUser(t, d, second.Token) == nil {
		t.Error("both sessions must be valid")
	}
}

// Rust: auth.rs::bad_logins_all_give_the_same_error
func TestBadLoginsAllGiveTheSameError(t *testing.T) {
	d := authSetup(t)
	if _, err := db.CreateUser(bg, d.Pool, "dave", authPassword, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetEnabled(bg, d.Pool, "dave", false); err != nil {
		t.Fatal(err)
	}
	var messages []string
	for _, c := range [][2]string{
		{"alice", "wrong password"},
		{"nobody", authPassword},
		{"dave", authPassword},
		// Rule violations fail the same way and are not stored.
		{"bad name!", authPassword},
		{"alice", "short"},
	} {
		_, err := tryLogin(d, c[0], c[1])
		authKind(t, err, db.AuthInvalidCredentials)
		messages = append(messages, fmt.Sprintf("%v %#v", err, err))
	}
	for _, m := range messages {
		if m != messages[0] {
			t.Errorf("%q differs from %q", m, messages[0])
		}
	}
	if !strings.HasPrefix(messages[0], "invalid username or password") {
		t.Error(messages[0])
	}
	if scalar(d, "SELECT count(*)::text FROM fb_session") != "0" {
		t.Error("a session exists")
	}
	// Unknown users are counted too, under the submitted name; invalid names are not.
	want(t, d.Rows("SELECT username, failures::text FROM fb_login_failure ORDER BY username"),
		[][]string{{"alice", "1"}, {"dave", "1"}, {"nobody", "1"}}, "failures")
}

// Rust: auth.rs::five_failures_lock_the_username_for_fifteen_minutes
func TestFiveFailuresLockTheUsernameForFifteenMinutes(t *testing.T) {
	d := authSetup(t)
	for range 4 {
		_, _ = tryLogin(d, "alice", "wrong password")
	}
	// Still not locked: the right password works and resets the counter.
	loginAlice(t, d)
	if scalar(d, "SELECT count(*)::text FROM fb_login_failure") != "0" {
		t.Error("the counter must be reset")
	}
	for range 5 {
		_, err := tryLogin(d, "alice", "wrong password")
		authKind(t, err, db.AuthInvalidCredentials)
	}
	want(t, d.Rows("SELECT failures::text, round(extract(epoch FROM locked_until - now()) / 60)::text FROM fb_login_failure"),
		[][]string{{"0", "15"}}, "locked")
	// Locked: even the correct password fails, with the same error, and does not extend the lock.
	_, err := tryLogin(d, "alice", authPassword)
	authKind(t, err, db.AuthInvalidCredentials)
	if scalar(d, "SELECT failures::text FROM fb_login_failure") != "0" {
		t.Error("a locked attempt was counted")
	}
	// Other usernames are not affected.
	if _, err := db.CreateUser(bg, d.Pool, "bob", authPassword, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tryLogin(d, "bob", authPassword); err != nil {
		t.Fatal(err)
	}
	// After the lock has passed the correct password works and clears the row.
	d.Exec("UPDATE fb_login_failure SET locked_until = now() - interval '1 second'")
	loginAlice(t, d)
	if scalar(d, "SELECT count(*)::text FROM fb_login_failure") != "0" {
		t.Error("the row must be cleared")
	}
}

// Rust: auth.rs::unknown_usernames_are_locked_too
func TestUnknownUsernamesAreLockedToo(t *testing.T) {
	d := authSetup(t)
	for range 5 {
		_, err := tryLogin(d, "ghost", authPassword)
		authKind(t, err, db.AuthInvalidCredentials)
	}
	if scalar(d, "SELECT (locked_until > now())::text FROM fb_login_failure WHERE username = 'ghost'") != "true" {
		t.Error("ghost is not locked")
	}
}

// Rust: auth.rs::concurrent_failures_cannot_exceed_the_limit
func TestConcurrentFailuresCannotExceedTheLimit(t *testing.T) {
	d := authSetup(t)
	// 7 is deliberately not a multiple of the limit (5): without the row lock the attempts would read
	// the same count and overwrite each other, ending at a different (non-zero) count.
	var wg sync.WaitGroup
	errs := make([]error, 7)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = db.Login(bg, d.Pool, "alice", "wrong password")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		authKind(t, err, db.AuthInvalidCredentials)
	}
	// Exactly five attempts were counted (failures reset to 0, locked); the other two met the lock and
	// were not counted. A no-lock implementation would end with failures = 2.
	want(t, d.Rows("SELECT failures::text, (locked_until > now())::text FROM fb_login_failure"),
		[][]string{{"0", "true"}}, "counted")
	_, err := tryLogin(d, "alice", authPassword)
	authKind(t, err, db.AuthInvalidCredentials)
}

// Rust: auth.rs::failure_accounting_survives_a_caller_transaction_only_when_committed
func TestFailureAccountingSurvivesACallerTransactionOnlyWhenCommitted(t *testing.T) {
	// Documented limit: inside a caller's transaction that rolls back, the accounting is lost.
	d := authSetup(t)
	tx, err := d.Pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Login(bg, tx, "alice", "wrong password")
	authKind(t, err, db.AuthInvalidCredentials)
	if err := tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	if scalar(d, "SELECT count(*)::text FROM fb_login_failure") != "0" {
		t.Error("the accounting must be rolled back with the caller's transaction")
	}
}

// Rust: auth.rs::sessions_are_valid_then_touched_then_expire
func TestSessionsAreValidThenTouchedThenExpire(t *testing.T) {
	d := authSetup(t)
	session := loginAlice(t, d)
	// Valid, and the use is recorded.
	d.Exec("UPDATE fb_session SET last_seen_at = now() - interval '23 hours'")
	user := sessionUser(t, d, session.Token)
	if user == nil || user.Username != "alice" || !slices.Equal(user.Roles, []string{"Accounts", "Sales"}) {
		t.Fatalf("%+v", user)
	}
	if scalar(d, "SELECT (now() - last_seen_at < interval '1 minute')::text FROM fb_session") != "true" {
		t.Error("the use was not recorded")
	}
	// Idle for 24 hours or more: expired, and the row is deleted.
	d.Exec("UPDATE fb_session SET last_seen_at = now() - interval '24 hours 1 second'")
	if sessionUser(t, d, session.Token) != nil {
		t.Error("an idle session is valid")
	}
	if scalar(d, "SELECT count(*)::text FROM fb_session") != "0" {
		t.Error("the idle session was not deleted")
	}
	// Absolute expiry applies even to a session in constant use.
	session = loginAlice(t, d)
	d.Exec("UPDATE fb_session SET expires_at = now() - interval '1 second'")
	if sessionUser(t, d, session.Token) != nil {
		t.Error("an expired session is valid")
	}
	if scalar(d, "SELECT count(*)::text FROM fb_session") != "0" {
		t.Error("the expired session was not deleted")
	}
}

// Rust: auth.rs::tampered_unknown_and_malformed_tokens_are_rejected
func TestTamperedUnknownAndMalformedTokensAreRejected(t *testing.T) {
	d := authSetup(t)
	session := loginAlice(t, d)
	tampered := session.Token[:63]
	if strings.HasSuffix(session.Token, "0") {
		tampered += "1"
	} else {
		tampered += "0"
	}
	for _, bad := range []string{tampered, "", "x", session.CSRFToken, strings.ToUpper(session.Token), session.Token + "0"} {
		if sessionUser(t, d, bad) != nil {
			t.Errorf("%q is accepted", bad)
		}
	}
	if sessionUser(t, d, session.Token) == nil {
		t.Error("the token is not accepted")
	}
}

// Rust: auth.rs::logout_ends_only_that_session
func TestLogoutEndsOnlyThatSession(t *testing.T) {
	d := authSetup(t)
	first, second := loginAlice(t, d), loginAlice(t, d)
	if !mustLogout(t, d, first.Token) || mustLogout(t, d, first.Token) {
		t.Error("logout results")
	}
	if sessionUser(t, d, first.Token) != nil || sessionUser(t, d, second.Token) == nil {
		t.Error("only the first session must end")
	}
}

// Rust: auth.rs::changing_the_password_ends_all_sessions
func TestChangingThePasswordEndsAllSessions(t *testing.T) {
	d := authSetup(t)
	first, second := loginAlice(t, d), loginAlice(t, d)
	if err := db.SetPassword(bg, d.Pool, "alice", "a brand new password"); err != nil {
		t.Fatal(err)
	}
	if sessionUser(t, d, first.Token) != nil || sessionUser(t, d, second.Token) != nil {
		t.Error("a session survived")
	}
	_, err := tryLogin(d, "alice", authPassword)
	authKind(t, err, db.AuthInvalidCredentials)
	if _, err := tryLogin(d, "alice", "a brand new password"); err != nil {
		t.Fatal(err)
	}
	authKind(t, db.SetPassword(bg, d.Pool, "nobody", authPassword), db.AuthUserNotFound)
	authKind(t, db.SetPassword(bg, d.Pool, "alice", "short"), db.AuthInvalidPassword)
}

// Rust: auth.rs::disabling_a_user_ends_sessions_and_blocks_login
func TestDisablingAUserEndsSessionsAndBlocksLogin(t *testing.T) {
	d := authSetup(t)
	session := loginAlice(t, d)
	api, err := db.CreateAPIToken(bg, d.Pool, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetEnabled(bg, d.Pool, "alice", false); err != nil {
		t.Fatal(err)
	}
	if sessionUser(t, d, session.Token) != nil {
		t.Error("the session survived")
	}
	if scalar(d, "SELECT count(*)::text FROM fb_session") != "0" {
		t.Error("sessions were not deleted")
	}
	if validAPI(t, d, api.Key, api.Secret) != nil {
		t.Error("the API token works for a disabled user")
	}
	_, err = tryLogin(d, "alice", authPassword)
	authKind(t, err, db.AuthInvalidCredentials)
	// Enabling restores the account (the lockout counter of the failed attempt is below the limit).
	if err := db.SetEnabled(bg, d.Pool, "alice", true); err != nil {
		t.Fatal(err)
	}
	if validAPI(t, d, api.Key, api.Secret) == nil {
		t.Error("the API token does not work again")
	}
	loginAlice(t, d)
}

// Rust: auth.rs::a_disabled_user_cannot_use_an_existing_session_even_if_the_row_survives
func TestADisabledUserCannotUseAnExistingSessionEvenIfTheRowSurvives(t *testing.T) {
	d := authSetup(t)
	session := loginAlice(t, d)
	d.Exec("UPDATE fb_user SET enabled = false")
	if sessionUser(t, d, session.Token) != nil {
		t.Error("the session works")
	}
	if mustCheckCSRF(t, d, session.Token, session.CSRFToken) {
		t.Error("the CSRF token works")
	}
}

// Rust: auth.rs::csrf_tokens_belong_to_their_session
func TestCSRFTokensBelongToTheirSession(t *testing.T) {
	d := authSetup(t)
	first, second := loginAlice(t, d), loginAlice(t, d)
	for _, c := range []struct {
		session, csrf string
		ok            bool
	}{
		{first.Token, first.CSRFToken, true},
		{first.Token, second.CSRFToken, false},
		{first.Token, first.Token, false},
		{first.Token, "", false},
		{"nope", first.CSRFToken, false},
	} {
		if mustCheckCSRF(t, d, c.session, c.csrf) != c.ok {
			t.Errorf("%+v", c)
		}
	}
	// An idle-expired session has no valid CSRF token either (and the check does not touch it).
	d.Exec("UPDATE fb_session SET last_seen_at = now() - interval '24 hours 1 second'")
	if mustCheckCSRF(t, d, first.Token, first.CSRFToken) {
		t.Error("idle")
	}
	d.Exec("UPDATE fb_session SET last_seen_at = now()")
	if !mustCheckCSRF(t, d, first.Token, first.CSRFToken) {
		t.Error("the check touched the session")
	}
	// Nor has an absolutely expired one.
	d.Exec("UPDATE fb_session SET expires_at = now() - interval '1 second'")
	if mustCheckCSRF(t, d, first.Token, first.CSRFToken) {
		t.Error("expired")
	}
}

// Rust: auth.rs::rotating_the_csrf_token_replaces_it_and_stores_only_a_digest
func TestRotatingTheCSRFTokenReplacesItAndStoresOnlyADigest(t *testing.T) {
	d := authSetup(t)
	first, other := loginAlice(t, d), loginAlice(t, d)
	fresh := mustRotate(t, d, first.Token)
	if len(fresh) != 64 || strings.Trim(fresh, "0123456789abcdef") != "" || fresh == first.CSRFToken {
		t.Fatalf("fresh %q", fresh)
	}
	// The new token works, the old one does not, another session is untouched.
	if !mustCheckCSRF(t, d, first.Token, fresh) || mustCheckCSRF(t, d, first.Token, first.CSRFToken) ||
		!mustCheckCSRF(t, d, other.Token, other.CSRFToken) {
		t.Error("rotation")
	}
	// Only a digest is stored.
	if stored := scalar(d, "SELECT string_agg(encode(csrf_hash, 'hex'), ',') FROM fb_session"); strings.Contains(stored, fresh) {
		t.Error(stored)
	}
	// Rotating again replaces it again.
	again := mustRotate(t, d, first.Token)
	if again == "" || again == fresh || mustCheckCSRF(t, d, first.Token, fresh) {
		t.Error("second rotation")
	}
	// Unknown and malformed session tokens get nothing.
	for _, bad := range []string{"nope", strings.Repeat("0", 64), first.CSRFToken} {
		if mustRotate(t, d, bad) != "" {
			t.Errorf("%q rotated", bad)
		}
	}
}

// Rust: auth.rs::an_invalid_session_cannot_rotate_its_csrf_token
func TestAnInvalidSessionCannotRotateItsCSRFToken(t *testing.T) {
	d := authSetup(t)
	session := loginAlice(t, d)
	d.Exec("UPDATE fb_session SET last_seen_at = now() - interval '24 hours 1 second'")
	if mustRotate(t, d, session.Token) != "" {
		t.Error("idle")
	}
	d.Exec("UPDATE fb_session SET last_seen_at = now()")
	d.Exec("UPDATE fb_session SET expires_at = now() - interval '1 second'")
	if mustRotate(t, d, session.Token) != "" {
		t.Error("expired")
	}
	d.Exec("UPDATE fb_session SET expires_at = now() + interval '1 day'")
	d.Exec("UPDATE fb_user SET enabled = false")
	if mustRotate(t, d, session.Token) != "" {
		t.Error("disabled")
	}
	// Nothing changed: the original token still matches once the user is enabled again.
	d.Exec("UPDATE fb_user SET enabled = true")
	if !mustCheckCSRF(t, d, session.Token, session.CSRFToken) {
		t.Error("the token changed")
	}
}

// Rust: auth.rs::api_tokens_validate_until_revoked
func TestAPITokensValidateUntilRevoked(t *testing.T) {
	d := authSetup(t)
	token, err := db.CreateAPIToken(bg, d.Pool, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(token.Key) != 32 || len(token.Secret) != 64 {
		t.Error("shapes")
	}
	// Only the digest is stored.
	dump := scalar(d, "SELECT string_agg(t::text, ' ') FROM fb_api_token t")
	if !strings.Contains(dump, token.Key) || strings.Contains(dump, token.Secret) {
		t.Error(dump)
	}
	user := validAPI(t, d, token.Key, token.Secret)
	if user == nil || user.Username != "alice" || !slices.Equal(user.Roles, []string{"Accounts", "Sales"}) {
		t.Fatalf("%+v", user)
	}
	other, err := db.CreateAPIToken(bg, d.Pool, "alice")
	if err != nil || other.Key == token.Key {
		t.Fatal(err)
	}
	for _, c := range [][2]string{
		{token.Key, other.Secret},
		{token.Key, "wrong"},
		{token.Key, ""},
		{"00000000000000000000000000000000", token.Secret},
		{"", ""},
	} {
		if validAPI(t, d, c[0], c[1]) != nil {
			t.Errorf("%v accepted", c)
		}
	}
	for range 2 {
		if err := db.RevokeAPIToken(bg, d.Pool, token.Key); err != nil {
			t.Fatal(err)
		}
	}
	if validAPI(t, d, token.Key, token.Secret) != nil {
		t.Error("a revoked token works")
	}
	if validAPI(t, d, other.Key, other.Secret) == nil {
		t.Error("the other token stopped working")
	}
	authKind(t, db.RevokeAPIToken(bg, d.Pool, "00000000000000000000000000000000"), db.AuthTokenNotFound)
	_, err = db.CreateAPIToken(bg, d.Pool, "nobody")
	authKind(t, err, db.AuthUserNotFound)
}

// Rust: auth.rs::revoking_a_malformed_key_never_echoes_it
func TestRevokingAMalformedKeyNeverEchoesIt(t *testing.T) {
	d := authSetup(t)
	token, err := db.CreateAPIToken(bg, d.Pool, "alice")
	if err != nil {
		t.Fatal(err)
	}
	// Someone pastes key:secret instead of the key.
	pasted := token.Key + ":" + token.Secret
	for _, bad := range []string{pasted, "nope", "", token.Secret, strings.ToUpper(token.Key)} {
		err := db.RevokeAPIToken(bg, d.Pool, bad)
		authKind(t, err, db.AuthInvalidTokenKey)
		text := fmt.Sprintf("%v %+v %#v", err, err, err)
		for _, part := range []string{token.Secret, token.Key, bad} {
			if bad != "" && strings.Contains(text, part) {
				t.Errorf("%q contains %q", text, part)
			}
		}
	}
	// The token is still valid: nothing was revoked.
	if validAPI(t, d, token.Key, token.Secret) == nil {
		t.Error("the token was revoked")
	}
}

// Rust: auth.rs::no_api_token_for_a_disabled_user
func TestNoAPITokenForADisabledUser(t *testing.T) {
	d := authSetup(t)
	if err := db.SetEnabled(bg, d.Pool, "alice", false); err != nil {
		t.Fatal(err)
	}
	_, err := db.CreateAPIToken(bg, d.Pool, "alice")
	authKind(t, err, db.AuthUserDisabled)
	if scalar(d, "SELECT count(*)::text FROM fb_api_token") != "0" {
		t.Error("a token was written")
	}
}

// Rust: auth.rs::failures_only_count_within_fifteen_minutes_of_each_other
func TestFailuresOnlyCountWithinFifteenMinutesOfEachOther(t *testing.T) {
	d := authSetup(t)
	for range 3 {
		_, _ = tryLogin(d, "alice", "wrong password")
	}
	if scalar(d, "SELECT failures::text FROM fb_login_failure") != "3" {
		t.Error("3 failures expected")
	}
	// The last failure is older than 15 minutes: the count restarts at 1 (not 4).
	d.Exec("UPDATE fb_login_failure SET last_failure_at = now() - interval '15 minutes 1 second'")
	_, _ = tryLogin(d, "alice", "wrong password")
	want(t, d.Rows("SELECT failures::text, (now() - last_failure_at < interval '1 minute')::text, (locked_until IS NULL)::text FROM fb_login_failure"),
		[][]string{{"1", "true", "true"}}, "restart")
	// Within the window the chain continues: 1 + 3 more = 4 (not locked), the 5th locks.
	for _, expected := range []string{"2", "3", "4"} {
		_, _ = tryLogin(d, "alice", "wrong password")
		if got := scalar(d, "SELECT failures::text FROM fb_login_failure"); got != expected {
			t.Errorf("failures %s, want %s", got, expected)
		}
	}
	_, _ = tryLogin(d, "alice", "wrong password")
	if scalar(d, "SELECT (failures = 0 AND locked_until > now())::text FROM fb_login_failure") != "true" {
		t.Error("not locked")
	}
}

// Rust: auth.rs::a_failure_just_inside_the_window_continues_the_count
func TestAFailureJustInsideTheWindowContinuesTheCount(t *testing.T) {
	d := authSetup(t)
	for range 3 {
		_, _ = tryLogin(d, "alice", "wrong password")
	}
	// 14 minutes 59 seconds ago is inside the 15 minute window: the chain continues with 4.
	d.Exec("UPDATE fb_login_failure SET last_failure_at = now() - interval '14 minutes 59 seconds'")
	_, _ = tryLogin(d, "alice", "wrong password")
	want(t, d.Rows("SELECT failures::text, (locked_until IS NULL)::text FROM fb_login_failure"),
		[][]string{{"4", "true"}}, "continue")
}

// waitFor polls pg_stat_activity of the test database until a backend matches condition.
func waitFor(t *testing.T, d *dbtest.DB, condition string) {
	t.Helper()
	sql := "SELECT count(*)::text FROM pg_stat_activity WHERE datname = current_database() AND " + condition
	for range 800 {
		if scalar(d, sql) != "0" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", condition)
}

// Rust: auth.rs::a_login_racing_a_password_change_leaves_no_old_password_session
//
// Regression test for the login vs. password-change race. A BEFORE INSERT trigger on fb_session holds
// the login open (after it verified the old password and read the user row) until the test opens a
// gate, so the interleaving does not depend on timing: SetPassword starts while the login is stuck
// inside its transaction. The test waits until SetPassword either waits for the login (the correct
// behaviour with FOR SHARE) or has finished (the racy behaviour), then opens the gate.
//
// Without the lock, SetPassword commits and deletes sessions while the login's session is not yet
// committed, so the old-password session survives. With it, SetPassword waits and then deletes the new
// session. Either way the returned token must not be valid afterwards.
func TestALoginRacingAPasswordChangeLeavesNoOldPasswordSession(t *testing.T) {
	d := authSetup(t)
	d.Exec(`CREATE TABLE test_gate (open boolean NOT NULL);
		INSERT INTO test_gate VALUES (false);
		CREATE FUNCTION test_wait() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  FOR i IN 1..400 LOOP
		    EXIT WHEN (SELECT open FROM test_gate);
		    PERFORM pg_sleep(0.025);
		  END LOOP;
		  RETURN NEW;
		END $$;
		CREATE TRIGGER test_wait BEFORE INSERT ON fb_session
		  FOR EACH ROW EXECUTE FUNCTION test_wait();`)

	type loginResult struct {
		session *db.LoginSession
		err     error
	}
	loginDone := make(chan loginResult, 1)
	go func() {
		s, err := db.Login(bg, d.Pool, "alice", authPassword)
		loginDone <- loginResult{s, err}
	}()
	// Wait until the login is inside the trigger.
	waitFor(t, d, "wait_event = 'PgSleep'")

	changeDone := make(chan error, 1)
	go func() { changeDone <- db.SetPassword(bg, d.Pool, "alice", "a brand new password") }()
	// Wait until SetPassword is blocked on a lock, or has finished.
	var changeErr error
	changed := false
	for range 800 {
		if scalar(d, "SELECT count(*)::text FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'") != "0" {
			break
		}
		select {
		case changeErr = <-changeDone:
			changed = true
		default:
		}
		if changed {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	d.Exec("UPDATE test_gate SET open = true")
	if !changed {
		changeErr = <-changeDone
	}
	if changeErr != nil {
		t.Fatal(changeErr)
	}
	result := <-loginDone
	if result.err == nil {
		// The login finished first: its session must have been deleted by SetPassword.
		if sessionUser(t, d, result.session.Token) != nil {
			t.Error("a session from the old password survived")
		}
	} else {
		// Or it lost the race and failed against the new hash.
		authKind(t, result.err, db.AuthInvalidCredentials)
	}
	if scalar(d, "SELECT count(*)::text FROM fb_session") != "0" {
		t.Error("a session exists")
	}
	if _, err := tryLogin(d, "alice", "a brand new password"); err != nil {
		t.Fatal(err)
	}
}

// Rust: auth.rs::secrets_never_appear_in_errors_or_debug_output
//
// fmt stands for Debug and Display here: every verb must leave the secrets out.
func TestSecretsNeverAppearInErrorsOrDebugOutput(t *testing.T) {
	d := authSetup(t)
	session := loginAlice(t, d)
	api, err := db.CreateAPIToken(bg, d.Pool, "alice")
	if err != nil {
		t.Fatal(err)
	}
	wrong := "this is the wrong password"
	format := func(v any) []string {
		return []string{fmt.Sprintf("%v", v), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v), fmt.Sprintf("%s", v)}
	}
	var texts []string
	for _, v := range []any{session, *session, api, *api, session.User} {
		texts = append(texts, format(v)...)
	}
	_, err = tryLogin(d, "alice", wrong)
	texts = append(texts, format(err)...)
	_, e1 := db.CreateUser(bg, d.Pool, "alice", wrong, nil)
	_, e2 := db.CreateUser(bg, d.Pool, "x y", wrong, nil)
	_, e3 := db.CreateUser(bg, d.Pool, "bob", "short", nil)
	e4 := db.SetPassword(bg, d.Pool, "nobody", wrong)
	for _, e := range []error{e1, e2, e3, e4} {
		texts = append(texts, format(e)...)
	}
	for _, text := range texts {
		for _, secret := range []string{authPassword, wrong, "short", session.Token, session.CSRFToken, api.Secret} {
			if strings.Contains(text, secret) {
				t.Errorf("%q contains a secret", text)
			}
		}
	}
	// The key is public and shown.
	if !strings.Contains(fmt.Sprintf("%v", api), api.Key) || !strings.Contains(fmt.Sprintf("%v", session), "<redacted>") {
		t.Error("the key must be shown and the tokens redacted")
	}
}

// Go addition: the purge functions (Rust: the built-in jobs of P5 call them; the Rust tests are in
// schedule.rs, P5) delete exactly the rows that the login and the validation no longer use.
func TestThePurgeFunctionsDeleteOnlyRowsThatNoLongerMatter(t *testing.T) {
	d := authSetup(t)
	live, idle, expired := loginAlice(t, d), loginAlice(t, d), loginAlice(t, d)
	d.Exec("UPDATE fb_session SET last_seen_at = now() - interval '24 hours' WHERE token_hash = sha256(convert_to('" + idle.Token + "', 'UTF8'))")
	d.Exec("UPDATE fb_session SET expires_at = now() - interval '1 second' WHERE token_hash = sha256(convert_to('" + expired.Token + "', 'UTF8'))")
	n, err := db.PurgeSessions(bg, d.Pool, 1)
	if err != nil || n != 1 {
		t.Fatalf("a batch of one: %d %v", n, err)
	}
	n, err = db.PurgeSessions(bg, d.Pool, 100)
	if err != nil || n != 1 {
		t.Fatalf("the rest: %d %v", n, err)
	}
	if scalar(d, "SELECT count(*)::text FROM fb_session") != "1" || sessionUser(t, d, live.Token) == nil {
		t.Error("only the valid session must remain")
	}

	d.Exec(`INSERT INTO fb_login_failure (username, failures, locked_until, last_failure_at) VALUES
		('old', 3, NULL, now() - interval '15 minutes 1 second'),
		('window', 3, NULL, now() - interval '14 minutes 59 seconds'),
		('locked', 0, now() + interval '1 minute', now() - interval '1 hour'),
		('lockover', 0, now() - interval '1 second', now() - interval '1 hour'),
		('never', 0, NULL, NULL)`)
	n, err = db.PurgeLoginFailures(bg, d.Pool, 100)
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	want(t, d.Rows("SELECT username FROM fb_login_failure ORDER BY username"), [][]string{{"locked"}, {"window"}}, "kept")
}
