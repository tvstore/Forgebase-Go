package db

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/argon2"

	"github.com/tvstore/forgebase-go/internal/core"
)

// Users, roles, sessions, API tokens and login lockout (M3.1, ADR 0010; the Rust module auth.rs).
//
// Everything here works on the framework tables of migration 0003 (fb_user, fb_user_role,
// fb_session, fb_api_token, fb_login_failure). There is no HTTP, cookie or permission handling here
// (internal/httpapi has it, from P3.2): this package only stores and checks credentials. The roles
// it stores are what the permission rules of M3.2 match against: AuthUser.Caller turns an
// authenticated user into the core.Caller that the document functions take. So are the user
// permissions of M10.3 (ADR 0025), read with the roles: ValidateSession and ValidateAPIToken load
// them for every request, so a change applies on the user's next request.
//
// Secrets. A password is stored as an Argon2id PHC string (m = 19456 KiB, t = 2, p = 1, random
// 16-byte salt, 32-byte output): the string is byte-compatible with the one the Rust argon2 and
// password-hash crates write and read, so a hash written by either implementation verifies in the
// other. Verification uses the parameters inside the stored string, so the parameters can be raised
// later without breaking old hashes. Session tokens, CSRF tokens and API secrets are 32 random bytes
// (from the operating system) written as 64 lowercase hex characters; only their SHA-256 digest
// (computed by PostgreSQL) is stored. An API key (the public identifier of an API token) is 16
// random bytes as 32 hex characters. No password, token or secret is ever stored in plaintext, or
// logged, or part of an error or of fmt output by Forgebase; the only places that return a secret
// are Login and CreateAPIToken, once.
//
// Known limit: PostgreSQL logging. Because sha256() runs in SQL, a plaintext session token, CSRF
// token or API secret reaches PostgreSQL as a bind parameter. PostgreSQL itself can log bind
// parameters (log_statement = all, log_min_duration_statement, statements that fail). Operators
// should set log_parameter_max_length = 0 and log_parameter_max_length_on_error = 0 (see ADR
// 0010). Over a network that is not private, connect with sslmode=require in the database URL (TLS
// with full verification, ADR 0018; available with P5); without it the connection is not encrypted.
//
// Rules. A username is 1 to 140 characters, ASCII letters, digits and _ . @ -, and is case-sensitive
// (Alice and alice are two users). A role is 1 to 140 characters, not blank, without control
// characters. A password is 8 to 1024 bytes: the upper bound keeps a client from making the server
// hash megabytes. Argon2 runs in the calling goroutine, which never blocks other goroutines.
//
// Transactions. Every function that changes more than one row runs in client.Begin() (a transaction
// on a pool or a connection, a savepoint inside a caller's transaction), like the document
// functions. Login additionally must be called on a pool or connection, not inside a caller-owned
// transaction: its failure accounting is committed together with the failed login, and a caller's
// transaction that rolls back (which a failed login usually causes) would undo it, so the lockout
// would never take effect.
//
// Differences to Rust that are known: a stored hash with a parameter the Go Argon2 implementation
// cannot run (version 0x10, more than 255 lanes, Argon2d) is not verified (the login fails like any
// other wrong password); Forgebase itself never writes one.

// Failed logins of one username, each at most failureWindowSecs after the previous one, before it is
// locked, and the other lockout and session constants of the Rust module.
const (
	// maxFailures is the number of failed logins that locks a username.
	maxFailures int32 = 5
	// lockSecs is how long a locked username cannot log in, in seconds (15 minutes).
	lockSecs float64 = 15.0 * 60.0
	// failureWindowSecs: a failure only counts with the previous one if that was at most this long
	// ago, in seconds (15 minutes). Otherwise the count restarts at 1.
	failureWindowSecs float64 = 15.0 * 60.0
	// sessionAbsoluteSecs is the absolute lifetime of a session, in seconds (7 days from login).
	sessionAbsoluteSecs float64 = 7.0 * 24.0 * 3600.0
	// sessionIdleSecs: a session with no use for this long expires, in seconds (24 hours).
	sessionIdleSecs float64 = 24.0 * 3600.0

	usernameMax = 140
	// passwordMin and passwordMax are the password length bounds in bytes.
	passwordMin = 8
	passwordMax = 1024

	// secretHexLen is the hex length of a session token, CSRF token and API secret (32 bytes).
	secretHexLen = 64
	// keyHexLen is the hex length of an API key (16 bytes).
	keyHexLen = 32
)

// digest is the SQL expression for the SHA-256 digest of text parameter $n.
func digest(n int) string {
	return fmt.Sprintf("sha256(convert_to($%d::text, 'UTF8'))", n)
}

// AuthErrorKind is the variant of an AuthError (the Rust enum AuthError).
type AuthErrorKind int

const (
	// AuthInvalidCredentials: the login failed. The same error for an unknown user, a wrong
	// password, a disabled user and a locked username, so it reveals nothing about which usernames
	// exist.
	AuthInvalidCredentials AuthErrorKind = iota + 1
	// AuthInvalidUsername: the username breaks the username rule (Detail is the reason).
	AuthInvalidUsername
	// AuthInvalidRole: the role breaks the role rule (Detail).
	AuthInvalidRole
	// AuthInvalidPassword: the password breaks the password rule (Detail).
	AuthInvalidPassword
	// AuthUserExists: a user with this username exists already.
	AuthUserExists
	// AuthUserNotFound: no user has this username (Detail).
	AuthUserNotFound
	// AuthInvalidUserPermission: a user permission cannot be added (M10.3, ADR 0025): the DocType
	// is unknown or a child DocType, the document does not exist, the value is not usable, or the
	// user has the limit of core.MaxUserPermissions documents for the DocType. Detail says what to
	// change.
	AuthInvalidUserPermission
	// AuthTokenNotFound: no API token has this key (Detail).
	AuthTokenNotFound
	// AuthInvalidTokenKey: the API token key is malformed. The message never repeats the input: a
	// pasted key:secret must not leak its secret.
	AuthInvalidTokenKey
	// AuthUserDisabled: the user is disabled, so no API token can be created for it (Detail).
	AuthUserDisabled
	// AuthHash: hashing or verifying a password failed for a reason other than a wrong password.
	AuthHash
	// AuthRandom: the operating system could not provide random bytes.
	AuthRandom
	// AuthDB: a database error (Err is the *Error).
	AuthDB
)

// AuthError is everything that can go wrong in the auth functions. It is always returned as
// *AuthError; use errors.As to get it. A database error is an AuthError of kind AuthDB that wraps
// the *Error, so errors.As reaches that too.
type AuthError struct {
	Kind   AuthErrorKind
	Detail string
	Err    error
}

// Error is the Rust Display text. It never contains a password or a secret.
func (e *AuthError) Error() string {
	switch e.Kind {
	case AuthInvalidCredentials:
		return "invalid username or password"
	case AuthInvalidUsername:
		return "invalid username: " + e.Detail
	case AuthInvalidRole:
		return "invalid role: " + e.Detail
	case AuthInvalidPassword:
		return "invalid password: " + e.Detail
	case AuthUserExists:
		return "a user with this username already exists"
	case AuthUserNotFound:
		return "user `" + e.Detail + "` not found"
	case AuthInvalidUserPermission:
		return "invalid user permission: " + e.Detail
	case AuthTokenNotFound:
		return "API token `" + e.Detail + "` not found"
	case AuthInvalidTokenKey:
		return "invalid API token key: must be 32 lowercase hex characters"
	case AuthUserDisabled:
		return "user `" + e.Detail + "` is disabled"
	case AuthHash:
		return "password hashing failed"
	case AuthRandom:
		return "cannot get random bytes from the operating system"
	case AuthDB:
		if e.Err != nil {
			return e.Err.Error()
		}
	}
	return "authentication error"
}

// Unwrap gives errors.As access to the database error.
func (e *AuthError) Unwrap() error { return e.Err }

func authErr(kind AuthErrorKind, detail string) *AuthError {
	return &AuthError{Kind: kind, Detail: detail}
}

// authDB makes err an AuthError of kind AuthDB: a driver error is wrapped like everywhere (wrap), an
// *Error or an *AuthError passes through (as the Rust From conversions).
func authDB(err error) error {
	if err == nil {
		return nil
	}
	var own *AuthError
	if errors.As(err, &own) {
		return err
	}
	return &AuthError{Kind: AuthDB, Err: wrap(err)}
}

// UserID is the id of a user.
type UserID = [16]byte

// AuthUser is an authenticated user.
type AuthUser struct {
	// ID is the user id.
	ID UserID
	// Username is the username.
	Username string
	// Roles are the user's roles, sorted.
	Roles []string
	// Restrictions are the documents the user is restricted to (user permissions, M10.3, ADR 0025).
	Restrictions core.UserRestrictions
}

// Caller is the core.Caller for the document operations of this package: the username, the roles and
// the user permissions. Build it from the result of ValidateSession or ValidateAPIToken (they are
// read from the database then), not from request data.
func (u AuthUser) Caller() core.Caller {
	return core.UserCaller(u.Username, u.Roles).WithRestrictions(u.Restrictions)
}

// LoginSession is the result of a successful Login. The tokens are returned only here and cannot be
// read back later: the database holds their digests only. fmt prints them as <redacted>. (Format works on
// a value that fmt reaches directly: a struct that holds a LoginSession or an APITokenSecret in an unexported
// field is printed field by field by %v and bypasses Format, so never put one in such a struct that is
// logged.)
type LoginSession struct {
	// Token is the session token: send it to the client (the HTTP API puts it in a cookie), never
	// log it.
	Token string
	// CSRFToken is the CSRF token that belongs to this session.
	CSRFToken string
	// User is the user the session belongs to.
	User AuthUser
	// ExpiresAt is when the session expires at the latest (7 days after login), regardless of use.
	ExpiresAt time.Time
}

// Format keeps the tokens out of every fmt verb (%v, %+v, %#v, %s).
func (s LoginSession) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "LoginSession{Token: <redacted>, CSRFToken: <redacted>, User: %+v, ExpiresAt: %v}", s.User, s.ExpiresAt)
}

// MarshalJSON keeps the tokens out of encoding/json: they are written as "[redacted]".
func (s LoginSession) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Token     string    `json:"token"`
		CSRFToken string    `json:"csrf_token"`
		User      AuthUser  `json:"user"`
		ExpiresAt time.Time `json:"expires_at"`
	}{redacted, redacted, s.User, s.ExpiresAt})
}

// LogValue keeps the tokens out of log/slog.
func (s LoginSession) LogValue() slog.Value {
	return slog.GroupValue(slog.String("token", redacted), slog.String("csrf_token", redacted),
		slog.String("user", s.User.Username), slog.Time("expires_at", s.ExpiresAt))
}

// redacted is what a secret is written as by MarshalJSON and LogValue.
const redacted = "[redacted]"

// APITokenSecret is a new API token. The secret is returned only here. fmt prints it as <redacted>.
type APITokenSecret struct {
	// Key is the public identifier of the token.
	Key string
	// Secret is the secret. Show it to the owner once and never log it.
	Secret string
}

// Format keeps the secret out of every fmt verb.
func (t APITokenSecret) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "APITokenSecret{Key: %s, Secret: <redacted>}", t.Key)
}

// MarshalJSON keeps the secret out of encoding/json: it is written as "[redacted]".
func (t APITokenSecret) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Key    string `json:"key"`
		Secret string `json:"secret"`
	}{t.Key, redacted})
}

// LogValue keeps the secret out of log/slog.
func (t APITokenSecret) LogValue() slog.Value {
	return slog.GroupValue(slog.String("key", t.Key), slog.String("secret", redacted))
}

// ---- rules ----------------------------------------------------------------------------------------

func checkUsername(username string) error {
	valid := true
	for i := 0; i < len(username); i++ {
		c := username[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '.' || c == '@' || c == '-') {
			valid = false
			break
		}
	}
	switch {
	case username == "" || len(username) > usernameMax:
		return authErr(AuthInvalidUsername, "must be 1 to 140 characters")
	case !valid:
		return authErr(AuthInvalidUsername, "may only contain ASCII letters, digits and _ . @ -")
	}
	return nil
}

// checkRole: the role rule is shared with the [[permissions]] rules of DocTypes (one definition in
// internal/core), so a rule can always name a role a user can have.
func checkRole(role string) error {
	if reason := core.CheckRoleName(role); reason != "" {
		return authErr(AuthInvalidRole, reason)
	}
	return nil
}

func checkPassword(password string) error {
	if len(password) < passwordMin || len(password) > passwordMax {
		return authErr(AuthInvalidPassword, "must be 8 to 1024 bytes long")
	}
	return nil
}

// ---- randomness and password hashing ----------------------------------------------------------------

// randomHex is n random bytes as lowercase hex.
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", authErr(AuthRandom, "")
	}
	return hex.EncodeToString(buf), nil
}

// isHex: whether text has exactly length lowercase hex characters (what randomHex produces).
// Anything else cannot be one of our tokens, so it is rejected without touching the database.
func isHex(text string, length int) bool {
	if len(text) != length {
		return false
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// The Argon2 parameters of a new hash.
const (
	argonMemory  uint32 = 19456
	argonTime    uint32 = 2
	argonLanes   uint8  = 1
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// phcB64 is the encoding of the salt and the hash in a PHC string: standard base64 without padding.
var phcB64 = base64.RawStdEncoding

// encodePHC is the PHC string of an Argon2id hash with the current parameters.
func encodePHC(salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonLanes,
		phcB64.EncodeToString(salt), phcB64.EncodeToString(key))
}

// hashWithSalt is the PHC string of password with the current parameters and the given salt.
func hashWithSalt(password, salt []byte) string {
	return encodePHC(salt, argon2.IDKey(password, salt, argonTime, argonMemory, argonLanes, argonKeyLen))
}

// hashPassword hashes a password into a PHC string with a random salt. CPU and memory heavy.
func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", authErr(AuthRandom, "")
	}
	return hashWithSalt([]byte(password), salt), nil
}

// dummyHash is a hash of a constant password with a constant salt and the current parameters,
// verified for unknown and disabled users so that they cost the same time as a real check. It is not
// secret (it protects nothing, and verifyPassword never reports a match for it). Computed once.
var dummyHash = sync.OnceValue(func() string {
	return hashWithSalt([]byte("forgebase dummy password"), []byte("forgebase-dummy!"))
})

// verifyPassword verifies password against hash; with nil it verifies the dummy hash and is always
// false. A malformed stored hash is also false (the login then fails like any other).
func verifyPassword(password string, hash *string) bool {
	if hash == nil {
		verifyPHC([]byte(password), dummyHash())
		return false
	}
	return verifyPHC([]byte(password), *hash)
}

// phcHash is a parsed Argon2 PHC string.
type phcHash struct {
	id      string
	memory  uint32
	time    uint32
	lanes   uint32
	salt    []byte
	hash    []byte
	version int
}

// parsePHC reads $argon2id$v=19$m=..,t=..,p=..$salt$hash like the Rust password-hash and argon2
// crates: the version is optional (it then is 19, as the Rust Version::default), the parameters m,
// t and p are the memory in KiB, the passes and the lanes, the salt and the hash are base64 without
// padding, and the length of the hash is the output length. Anything else is not a hash this code
// verifies.
func parsePHC(encoded string) (*phcHash, bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) < 5 || len(parts) > 6 || parts[0] != "" {
		return nil, false
	}
	h := &phcHash{id: parts[1], version: 19}
	rest := parts[2:]
	if strings.HasPrefix(rest[0], "v=") {
		v, ok := uintDigits(rest[0][2:])
		if !ok || len(rest) != 4 {
			return nil, false
		}
		h.version = int(v)
		rest = rest[1:]
	}
	if len(rest) != 3 {
		return nil, false
	}
	seen := map[string]bool{}
	for _, pair := range strings.Split(rest[0], ",") {
		name, value, ok := strings.Cut(pair, "=")
		n, okNum := uintDigits(value)
		if !ok || !okNum || seen[name] {
			return nil, false
		}
		seen[name] = true
		switch name {
		case "m":
			h.memory = n
		case "t":
			h.time = n
		case "p":
			h.lanes = n
		default:
			return nil, false
		}
	}
	if !seen["m"] || !seen["t"] || !seen["p"] {
		return nil, false
	}
	var err error
	if h.salt, err = phcB64.Strict().DecodeString(rest[1]); err != nil {
		return nil, false
	}
	if h.hash, err = phcB64.Strict().DecodeString(rest[2]); err != nil {
		return nil, false
	}
	return h, true
}

// uintDigits reads a non-empty string of ASCII digits that fits a uint32.
func uintDigits(s string) (uint32, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 32)
	return uint32(n), err == nil
}

// verifyPHC verifies password against an Argon2 PHC string, with the parameters inside the string,
// and compares the hashes in constant time.
func verifyPHC(password []byte, encoded string) bool {
	h, ok := parsePHC(encoded)
	if !ok || h.version != 19 {
		return false
	}
	// The limits of the Argon2 algorithm, as the Rust crate checks them, and of this implementation
	// (at most 255 lanes).
	if h.time < 1 || h.lanes < 1 || h.lanes > 255 || h.memory < 8*h.lanes || len(h.salt) < 8 || len(h.hash) < 4 {
		return false
	}
	var key []byte
	switch h.id {
	case "argon2id":
		key = argon2.IDKey(password, h.salt, h.time, h.memory, uint8(h.lanes), uint32(len(h.hash)))
	case "argon2i":
		key = argon2.Key(password, h.salt, h.time, h.memory, uint8(h.lanes), uint32(len(h.hash)))
	default:
		return false
	}
	return subtle.ConstantTimeCompare(key, h.hash) == 1
}

// ---- users and roles ------------------------------------------------------------------------------

// CreateUser creates an enabled user with the given roles (duplicates are ignored) and returns its
// id.
//
// It fails with AuthUserExists if the username is taken, and with the rule errors for a bad
// username, role or password (nothing is hashed or written then).
func CreateUser(ctx context.Context, client TxStarter, username, password string, roles []string) (UserID, error) {
	if err := checkUsername(username); err != nil {
		return UserID{}, err
	}
	if err := checkPassword(password); err != nil {
		return UserID{}, err
	}
	for _, role := range roles {
		if err := checkRole(role); err != nil {
			return UserID{}, err
		}
	}
	hash, err := hashPassword(password)
	if err != nil {
		return UserID{}, err
	}
	id := UserID(newID())
	tx, end, err := begin(ctx, client)
	if err != nil {
		return UserID{}, authDB(err)
	}
	defer end()
	if _, err := tx.Exec(ctx, "INSERT INTO fb_user (id, username, password_hash) VALUES ($1, $2, $3)", id, username, hash); err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == sqlStateUniqueViolation {
			return UserID{}, authErr(AuthUserExists, "")
		}
		return UserID{}, authDB(err)
	}
	for _, role := range roles {
		if _, err := tx.Exec(ctx, "INSERT INTO fb_user_role (user_id, role) VALUES ($1, $2) ON CONFLICT DO NOTHING", id, role); err != nil {
			return UserID{}, authDB(err)
		}
	}
	if err := commit(ctx, tx); err != nil {
		return UserID{}, authDB(err)
	}
	return id, nil
}

// SetPassword changes a user's password and ends all of the user's sessions. API tokens stay valid.
func SetPassword(ctx context.Context, client TxStarter, username, password string) error {
	if err := checkUsername(username); err != nil {
		return err
	}
	if err := checkPassword(password); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, end, err := begin(ctx, client)
	if err != nil {
		return authDB(err)
	}
	defer end()
	id, err := userID(ctx, tx, username)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE fb_user SET password_hash = $1 WHERE id = $2", hash, id); err != nil {
		return authDB(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM fb_session WHERE user_id = $1", id); err != nil {
		return authDB(err)
	}
	return authDB(commit(ctx, tx))
}

// SetEnabled enables or disables a user. A disabled user cannot log in, and its sessions and API
// tokens stop working; disabling also deletes its sessions.
func SetEnabled(ctx context.Context, client TxStarter, username string, enabled bool) error {
	if err := checkUsername(username); err != nil {
		return err
	}
	tx, end, err := begin(ctx, client)
	if err != nil {
		return authDB(err)
	}
	defer end()
	id, err := userID(ctx, tx, username)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE fb_user SET enabled = $1 WHERE id = $2", enabled, id); err != nil {
		return authDB(err)
	}
	if !enabled {
		if _, err := tx.Exec(ctx, "DELETE FROM fb_session WHERE user_id = $1", id); err != nil {
			return authDB(err)
		}
	}
	return authDB(commit(ctx, tx))
}

// AddRole gives a user a role. It returns false if the user had it already.
func AddRole(ctx context.Context, client TxStarter, username, role string) (bool, error) {
	if err := checkUsername(username); err != nil {
		return false, err
	}
	if err := checkRole(role); err != nil {
		return false, err
	}
	tx, end, err := begin(ctx, client)
	if err != nil {
		return false, authDB(err)
	}
	defer end()
	id, err := userID(ctx, tx, username)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, "INSERT INTO fb_user_role (user_id, role) VALUES ($1, $2) ON CONFLICT DO NOTHING", id, role)
	if err != nil {
		return false, authDB(err)
	}
	if err := commit(ctx, tx); err != nil {
		return false, authDB(err)
	}
	return tag.RowsAffected() > 0, nil
}

// RemoveRole takes a role from a user. It returns false if the user did not have it.
func RemoveRole(ctx context.Context, client TxStarter, username, role string) (bool, error) {
	if err := checkUsername(username); err != nil {
		return false, err
	}
	if err := checkRole(role); err != nil {
		return false, err
	}
	tx, end, err := begin(ctx, client)
	if err != nil {
		return false, authDB(err)
	}
	defer end()
	id, err := userID(ctx, tx, username)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, "DELETE FROM fb_user_role WHERE user_id = $1 AND role = $2", id, role)
	if err != nil {
		return false, authDB(err)
	}
	if err := commit(ctx, tx); err != nil {
		return false, authDB(err)
	}
	return tag.RowsAffected() > 0, nil
}

// userID is the id of the user with this username, or AuthUserNotFound.
func userID(ctx context.Context, q Queryer, username string) (UserID, error) {
	var id UserID
	err := q.QueryRow(ctx, "SELECT id FROM fb_user WHERE username = $1", username).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserID{}, authErr(AuthUserNotFound, username)
	}
	if err != nil {
		return UserID{}, authDB(err)
	}
	return id, nil
}

func loadRoles(ctx context.Context, q Queryer, id UserID) ([]string, error) {
	rows, err := q.Query(ctx, "SELECT role FROM fb_user_role WHERE user_id = $1", id)
	if err != nil {
		return nil, authDB(err)
	}
	defer rows.Close()
	roles := []string{}
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, authDB(err)
		}
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, authDB(err)
	}
	sort.Strings(roles)
	return roles, nil
}

// loadRestrictions are the user permissions of a user (M10.3, ADR 0025): one pair per row of
// fb_user_permission. A database that an older binary migrated has no such table yet; its users have
// none.
func loadRestrictions(ctx context.Context, q Queryer, id UserID) (core.UserRestrictions, error) {
	var present bool
	if err := q.QueryRow(ctx, "SELECT to_regclass('fb_user_permission') IS NOT NULL").Scan(&present); err != nil {
		return core.UserRestrictions{}, authDB(err)
	}
	if !present {
		return core.UserRestrictions{}, nil
	}
	rows, err := q.Query(ctx, "SELECT doctype, value FROM fb_user_permission WHERE user_id = $1", id)
	if err != nil {
		return core.UserRestrictions{}, authDB(err)
	}
	defer rows.Close()
	var pairs [][2]string
	for rows.Next() {
		var pair [2]string
		if err := rows.Scan(&pair[0], &pair[1]); err != nil {
			return core.UserRestrictions{}, authDB(err)
		}
		pairs = append(pairs, pair)
	}
	if err := rows.Err(); err != nil {
		return core.UserRestrictions{}, authDB(err)
	}
	return core.NewUserRestrictions(pairs), nil
}

func loadUser(ctx context.Context, q Queryer, id UserID, username string) (AuthUser, error) {
	roles, err := loadRoles(ctx, q, id)
	if err != nil {
		return AuthUser{}, err
	}
	restrictions, err := loadRestrictions(ctx, q, id)
	if err != nil {
		return AuthUser{}, err
	}
	return AuthUser{ID: id, Username: username, Roles: roles, Restrictions: restrictions}, nil
}

// ---- login, sessions, CSRF ------------------------------------------------------------------------

// Login checks a username and password and starts a new session.
//
// Uniform failure. An unknown user, a disabled user, a wrong password and a locked username all fail
// with AuthInvalidCredentials, and each of them spends one Argon2 verification (a dummy hash when
// there is no real one to check), so neither the error nor the time reveals which usernames exist. A
// username or password that breaks the rules cannot match anything and fails the same way, without
// being stored. Database and internal errors are other errors.
//
// Lockout. Failed attempts are counted per submitted username in fb_login_failure, also for names
// that do not exist. A failure counts with the previous ones only if the previous failure
// (last_failure_at) was at most 15 minutes ago (exactly 15 minutes still counts); otherwise the
// count restarts at 1. The fifth failure in such a chain locks the username for 15 minutes and
// resets the counter; while it is locked every attempt fails without checking the password (the
// correct password too) and does not extend the lock. A successful login deletes the counter. The
// counting runs in a transaction of its own that holds the username's row lock until the outcome is
// known, so concurrent attempts for one username are serialized and cannot exceed the limit. The
// counting is committed even though the login fails. For that this function must get a pool or a
// connection, not a caller-owned transaction (see the package comment above). Rows that no longer
// matter are deleted by the built-in job forgebase.purge_login_failures (PurgeLoginFailures, ADR
// 0013).
//
// Password change race. The user row is read with FOR SHARE and kept locked until the session
// exists, so a concurrent SetPassword (or SetEnabled) waits for this login to finish and then
// deletes its session, or has finished first and this login verifies against the new hash. A session
// authenticated with an old password therefore never survives.
//
// Session. A successful login always creates a new session (new token, new CSRF token); it is valid
// until 7 days after login, and expires earlier after 24 hours without use (see ValidateSession).
func Login(ctx context.Context, client TxStarter, username, password string) (*LoginSession, error) {
	if checkUsername(username) != nil || checkPassword(password) != nil {
		verifyPassword("", nil)
		return nil, authErr(AuthInvalidCredentials, "")
	}
	tx, end, err := begin(ctx, client)
	if err != nil {
		return nil, authDB(err)
	}
	defer end()
	// Insert-or-lock in one statement: the row exists afterwards and is locked by this transaction,
	// even if another transaction deletes or creates it at the same moment.
	var locked bool
	err = tx.QueryRow(ctx,
		"INSERT INTO fb_login_failure (username, failures) VALUES ($1, 0) "+
			"ON CONFLICT (username) DO UPDATE SET failures = fb_login_failure.failures "+
			"RETURNING coalesce(locked_until > now(), false)", username).Scan(&locked)
	if err != nil {
		return nil, authDB(err)
	}
	if locked {
		if err := commit(ctx, tx); err != nil {
			return nil, authDB(err)
		}
		verifyPassword("", nil)
		return nil, authErr(AuthInvalidCredentials, "")
	}

	var (
		found    *UserID
		hash     *string
		rowID    UserID
		rowHash  string
		rowValid bool
	)
	err = tx.QueryRow(ctx, "SELECT id, password_hash, enabled FROM fb_user WHERE username = $1 FOR SHARE", username).
		Scan(&rowID, &rowHash, &rowValid)
	switch {
	case err == nil && rowValid:
		found, hash = &rowID, &rowHash
	case err == nil || errors.Is(err, pgx.ErrNoRows):
	default:
		return nil, authDB(err)
	}
	verified := verifyPassword(password, hash)
	if found == nil || !verified {
		if _, err := tx.Exec(ctx,
			"UPDATE fb_login_failure f SET "+
				"failures = CASE WHEN c.n >= $2 THEN 0 ELSE c.n END, "+
				"locked_until = CASE WHEN c.n >= $2 "+
				"THEN now() + make_interval(secs => $3::float8) ELSE f.locked_until END, "+
				"last_failure_at = now() "+
				"FROM (SELECT username, "+
				"CASE WHEN last_failure_at >= now() - make_interval(secs => $4::float8) "+
				"THEN failures ELSE 0 END + 1 AS n "+
				"FROM fb_login_failure WHERE username = $1) c "+
				"WHERE f.username = c.username",
			username, maxFailures, lockSecs, failureWindowSecs); err != nil {
			return nil, authDB(err)
		}
		if err := commit(ctx, tx); err != nil {
			return nil, authDB(err)
		}
		return nil, authErr(AuthInvalidCredentials, "")
	}

	if _, err := tx.Exec(ctx, "DELETE FROM fb_login_failure WHERE username = $1", username); err != nil {
		return nil, authDB(err)
	}
	token, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	csrf, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	var expiresAt time.Time
	err = tx.QueryRow(ctx,
		"INSERT INTO fb_session (token_hash, csrf_hash, user_id, expires_at) VALUES ("+digest(1)+", "+digest(2)+
			", $3, now() + make_interval(secs => $4::float8)) RETURNING expires_at",
		token, csrf, *found, sessionAbsoluteSecs).Scan(&expiresAt)
	if err != nil {
		return nil, authDB(err)
	}
	user, err := loadUser(ctx, tx, *found, username)
	if err != nil {
		return nil, err
	}
	if err := commit(ctx, tx); err != nil {
		return nil, authDB(err)
	}
	return &LoginSession{Token: token, CSRFToken: csrf, User: user, ExpiresAt: expiresAt.UTC()}, nil
}

// ValidateSession looks up the user of a session token.
//
// The session is valid if its token matches, the user is enabled, it is younger than 7 days
// (expires_at) and was used less than 24 hours ago (last_seen_at). A valid session is touched
// (last_seen_at = now(): one small write per call); an expired one is deleted. Everything else,
// including a token of the wrong shape, returns nil and no error.
func ValidateSession(ctx context.Context, q Queryer, token string) (*AuthUser, error) {
	if !isHex(token, secretHexLen) {
		return nil, nil
	}
	var (
		id       UserID
		username string
	)
	err := q.QueryRow(ctx,
		"UPDATE fb_session s SET last_seen_at = now() FROM fb_user u WHERE s.token_hash = "+digest(1)+
			" AND u.id = s.user_id AND u.enabled AND now() < s.expires_at "+
			"AND now() < s.last_seen_at + make_interval(secs => $2::float8) "+
			"RETURNING u.id, u.username", token, sessionIdleSecs).Scan(&id, &username)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := q.Exec(ctx,
			"DELETE FROM fb_session WHERE token_hash = "+digest(1)+" AND (now() >= expires_at "+
				"OR now() >= last_seen_at + make_interval(secs => $2::float8))", token, sessionIdleSecs); err != nil {
			return nil, authDB(err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, authDB(err)
	}
	user, err := loadUser(ctx, q, id, username)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// PurgeLoginFailures deletes up to limit fb_login_failure rows that no longer affect a login, and
// returns how many it deleted (the built-in job forgebase.purge_login_failures calls it until a batch
// is not full).
//
// A row still matters while either of these holds, and both are the conditions Login uses (the same
// constants): the username is locked (locked_until > now()), or its last failure is within the
// failure window (last_failure_at >= now() - window, so a failure of exactly the window ago still
// counts) and the next failure would be counted with it. Every other row is deleted: a username that
// is not locked and whose last failure is older than the window (or that has none) starts again at
// one failure, exactly as if it had no row.
func PurgeLoginFailures(ctx context.Context, q Queryer, limit int64) (uint64, error) {
	tag, err := q.Exec(ctx,
		"DELETE FROM fb_login_failure WHERE username IN ( "+
			"SELECT username FROM fb_login_failure "+
			"WHERE (locked_until IS NULL OR locked_until <= now()) "+
			"AND (last_failure_at IS NULL "+
			"OR last_failure_at < now() - make_interval(secs => $1::float8)) "+
			"LIMIT $2 FOR UPDATE SKIP LOCKED)", failureWindowSecs, limit)
	if err != nil {
		return 0, wrap(err)
	}
	return uint64(tag.RowsAffected()), nil
}

// PurgeSessions deletes up to limit sessions that ValidateSession would refuse as expired, and
// returns how many it deleted: now() >= expires_at (7 days after login) or now() >= last_seen_at +
// 24 hours (the same constants). A valid session is kept.
func PurgeSessions(ctx context.Context, q Queryer, limit int64) (uint64, error) {
	tag, err := q.Exec(ctx,
		"DELETE FROM fb_session WHERE token_hash IN ( "+
			"SELECT token_hash FROM fb_session "+
			"WHERE now() >= expires_at "+
			"OR now() >= last_seen_at + make_interval(secs => $1::float8) "+
			"LIMIT $2 FOR UPDATE SKIP LOCKED)", sessionIdleSecs, limit)
	if err != nil {
		return 0, wrap(err)
	}
	return uint64(tag.RowsAffected()), nil
}

// CheckCSRF reports whether csrfToken is the CSRF token of the session sessionToken, and that session
// is still valid (same rules as ValidateSession; it does not touch the session). The digests are
// compared by PostgreSQL.
func CheckCSRF(ctx context.Context, q Queryer, sessionToken, csrfToken string) (bool, error) {
	if !isHex(sessionToken, secretHexLen) || !isHex(csrfToken, secretHexLen) {
		return false, nil
	}
	var ok bool
	err := q.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM fb_session s JOIN fb_user u ON u.id = s.user_id WHERE s.token_hash = "+digest(1)+
			" AND s.csrf_hash = "+digest(2)+" AND u.enabled AND now() < s.expires_at "+
			"AND now() < s.last_seen_at + make_interval(secs => $3::float8))",
		sessionToken, csrfToken, sessionIdleSecs).Scan(&ok)
	if err != nil {
		return false, authDB(err)
	}
	return ok, nil
}

// RotateCSRF replaces the CSRF token of the session sessionToken with a new one and returns it, so
// that a page can recover the token after a reload without ever storing it (the desk keeps it in
// memory only). The old token stops working at once. The session must be valid by the same rules as
// ValidateSession (token matches, user enabled, not expired, not idle); otherwise it returns "" and
// nothing changes. Only the digest of the new token is stored. It does not touch the session
// (last_seen_at): the caller has validated it already.
//
// Whoever holds the session token can obtain a token this way, which gives them nothing they did not
// have; a cross-site page cannot read the response (the HTTP API sends no CORS headers), so it cannot
// learn the new token.
func RotateCSRF(ctx context.Context, q Queryer, sessionToken string) (string, error) {
	if !isHex(sessionToken, secretHexLen) {
		return "", nil
	}
	csrf, err := randomHex(32)
	if err != nil {
		return "", err
	}
	tag, err := q.Exec(ctx,
		"UPDATE fb_session s SET csrf_hash = "+digest(2)+" FROM fb_user u WHERE s.token_hash = "+digest(1)+
			" AND u.id = s.user_id AND u.enabled AND now() < s.expires_at "+
			"AND now() < s.last_seen_at + make_interval(secs => $3::float8)",
		sessionToken, csrf, sessionIdleSecs)
	if err != nil {
		return "", authDB(err)
	}
	if tag.RowsAffected() == 0 {
		return "", nil
	}
	return csrf, nil
}

// Logout deletes a session. It returns false if there was none for this token.
func Logout(ctx context.Context, q Queryer, token string) (bool, error) {
	if !isHex(token, secretHexLen) {
		return false, nil
	}
	tag, err := q.Exec(ctx, "DELETE FROM fb_session WHERE token_hash = "+digest(1), token)
	if err != nil {
		return false, authDB(err)
	}
	return tag.RowsAffected() > 0, nil
}

// ---- API tokens -----------------------------------------------------------------------------------

// CreateAPIToken creates an API token for a user. The secret is returned here only; the database
// keeps its SHA-256 digest. A token does not expire; revoke it with RevokeAPIToken. A disabled user
// gets no token (AuthUserDisabled); an unknown one is AuthUserNotFound.
func CreateAPIToken(ctx context.Context, q Queryer, username string) (*APITokenSecret, error) {
	if err := checkUsername(username); err != nil {
		return nil, err
	}
	key, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	secret, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	tag, err := q.Exec(ctx,
		"INSERT INTO fb_api_token (key, secret_hash, user_id) SELECT $1, "+digest(2)+
			", id FROM fb_user WHERE username = $3 AND enabled", key, secret, username)
	if err != nil {
		return nil, authDB(err)
	}
	if tag.RowsAffected() == 0 {
		// Nothing inserted: the user is unknown or disabled.
		var one int32
		err := q.QueryRow(ctx, "SELECT 1 FROM fb_user WHERE username = $1", username).Scan(&one)
		switch {
		case err == nil:
			return nil, authErr(AuthUserDisabled, username)
		case errors.Is(err, pgx.ErrNoRows):
			return nil, authErr(AuthUserNotFound, username)
		}
		return nil, authDB(err)
	}
	return &APITokenSecret{Key: key, Secret: secret}, nil
}

// ValidateAPIToken looks up the user of an API token: the key must exist, the secret must match, the
// token must not be revoked and the user must be enabled. Otherwise it returns nil and no error.
func ValidateAPIToken(ctx context.Context, q Queryer, key, secret string) (*AuthUser, error) {
	if !isHex(key, keyHexLen) || !isHex(secret, secretHexLen) {
		return nil, nil
	}
	var (
		id       UserID
		username string
	)
	err := q.QueryRow(ctx,
		"SELECT u.id, u.username FROM fb_api_token t JOIN fb_user u ON u.id = t.user_id "+
			"WHERE t.key = $1 AND t.secret_hash = "+digest(2)+" AND t.revoked_at IS NULL AND u.enabled",
		key, secret).Scan(&id, &username)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, authDB(err)
	}
	user, err := loadUser(ctx, q, id, username)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// RevokeAPIToken revokes an API token. Revoking a revoked token succeeds; an unknown key is
// AuthTokenNotFound. A key of the wrong shape is AuthInvalidTokenKey, checked first and without
// repeating the input (so a pasted key:secret leaks nothing).
func RevokeAPIToken(ctx context.Context, q Queryer, key string) error {
	if !isHex(key, keyHexLen) {
		return authErr(AuthInvalidTokenKey, "")
	}
	tag, err := q.Exec(ctx, "UPDATE fb_api_token SET revoked_at = coalesce(revoked_at, now()) WHERE key = $1", key)
	if err != nil {
		return authDB(err)
	}
	if tag.RowsAffected() == 0 {
		return authErr(AuthTokenNotFound, key)
	}
	return nil
}
