package httpapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/tvstore/forgebase-go/internal/core"
	"github.com/tvstore/forgebase-go/internal/db"
)

// The error envelope (the Rust module error.rs): every error response is
//
//	{"error": {"code": "<snake_case>", "message": "<text>", "details": ["..."]}}
//
// with these codes and statuses.
//
//	unauthenticated                       401  no valid session or token, or a failed login (one message each)
//	permission_denied                     403  the caller lacks the right (for an update: the edit_roles of the state)
//	csrf_failed                           403  missing or wrong X-Forgebase-CSRF
//	not_found                             404  unknown route, DocType or document (also a document the caller cannot read)
//	method_not_allowed                    405
//	conflict                              409  the document changed (version), or changed while being read
//	invalid_docstatus, already_amended,
//	field_locked                          409  the document's state forbids it
//	workflow_required                     409  submit or cancel on a DocType with a workflow (use a transition)
//	unique_violation,
//	linked_document_exists                409
//	not_submittable                       422  submit, cancel or amend of a DocType that is not submittable
//	validation_failed                     422  the data breaks the DocType's rules; details lists every problem
//	field_not_permitted                   422  a value for a field the caller can read but not write (perm_level)
//	hook_rejected                         422  a lifecycle hook refused; the message is the hook's
//	bad_request                           400  malformed body or parameters
//	invalid_query                         400  a filter, sort, limit or offset cannot be used
//	invalid_host                          400  the Host header is missing, repeated, malformed or not allowed
//	payload_too_large                     413  the body is over the limit
//	unsupported_media_type                415  the body is not JSON
//	too_many_logins                       503  too many logins hash a password at the moment; Retry-After
//	lock_timeout                          503  a database lock stayed busy; Retry-After
//	internal                              500  generic message; the real error goes to standard error
//
//	method_error                          400 or 422  an app method refused the call; the message is the app's, written
//	                                              for users (M9.1)
//
//	export_too_large, import_too_large   422  an export would have more rows than the limit (details: count, limit); an
//	                                     import file has more rows than the limit (details: limit) (M9.3)
//	invalid_import                        422  the import file cannot be used at all: not UTF-8, bad syntax, an unknown,
//	                                     duplicate or table column; details lists the problems (M9.3)
//	import_failed                         422  at least one row of an import has an error, so nothing was imported;
//	                                     details is the summary with the per-row errors (M9.3)
//
// The customize routes (M10.1) and the user permission routes (M10.3) use the codes above: 403 permission_denied for a
// caller who is not an Administrator, 404 not_found, 409 conflict for a custom field that exists, 422 validation_failed
// for a field or a permission that breaks a rule, and 503 lock_timeout (with Retry-After) while a migrate holds the lock.
//
// Every error body, and every import summary, is bounded where it is built: the message and the details share a budget of
// maxErrorBodyBytes (64 KiB of JSON-escaped bytes), each string is cut to maxMessageChars characters
// and a list has at most maxReportedErrors entries and an "and N more" entry.
//
// Messages never contain passwords, tokens, secrets or the database URL. Database errors that are not
// the caller's fault are logged and replaced by a generic message.

// apiError is an error response.
type apiError struct {
	status  int
	code    string
	message string
	details []string
	// detailsJSON is structured details (an object), instead of the list of strings.
	detailsJSON any
	retryAfter  int
}

func newError(status int, code, message string) *apiError {
	return &apiError{status: status, code: code, message: message}
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

// errUnauthenticated is the one 401: the same for a missing, invalid, expired or revoked credential.
func errUnauthenticated() *apiError {
	return newError(http.StatusUnauthorized, "unauthenticated", "authentication required")
}

func errCSRF() *apiError {
	return newError(http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token")
}

func errBadRequest(message string) *apiError {
	return newError(http.StatusBadRequest, "bad_request", message)
}

func errInvalidQuery(message string) *apiError {
	return newError(http.StatusBadRequest, "invalid_query", message)
}

func errNotFound(message string) *apiError {
	return newError(http.StatusNotFound, "not_found", message)
}

func errInvalidHost() *apiError {
	return newError(http.StatusBadRequest, "invalid_host", "invalid Host header")
}

func errTooManyLogins() *apiError {
	e := newError(http.StatusServiceUnavailable, "too_many_logins", "too many logins at the moment; retry shortly")
	e.retryAfter = 1
	return e
}

// errExportTooLarge is the 422 of an export whose filters match more rows than the limit.
func errExportTooLarge(count, limit uint64) *apiError {
	e := newError(http.StatusUnprocessableEntity, "export_too_large",
		fmt.Sprintf("the export would have %d rows, over the limit of %d; narrow the filters", count, limit))
	e.detailsJSON = map[string]any{"count": count, "limit": limit}
	return e
}

// errImportTooLarge is the 422 of an import file with more rows than the limit; nothing ran.
func errImportTooLarge(limit int) *apiError {
	e := newError(http.StatusUnprocessableEntity, "import_too_large", fmt.Sprintf("the file has more than %d rows; split it", limit))
	e.detailsJSON = map[string]any{"limit": limit}
	return e
}

// errInvalidImport is the 422 of a file that cannot be imported at all (unknown column, bad syntax).
func errInvalidImport(problems []string) *apiError {
	e := newError(http.StatusUnprocessableEntity, "invalid_import", "the file cannot be imported")
	e.details = problems
	return e
}

// errImportFailed is the 422 of an import in which at least one row has an error, so nothing was imported.
// summary is the summary of the import, as in the success answer.
func errImportFailed(summary map[string]any, count uint64) *apiError {
	e := newError(http.StatusUnprocessableEntity, "import_failed", fmt.Sprintf("%d row(s) have errors; nothing was imported", count))
	e.detailsJSON = summary
	return e
}

func errValidation(details []string) *apiError {
	e := newError(http.StatusUnprocessableEntity, "validation_failed", "validation failed")
	e.details = details
	return e
}

// logWriter is where internal errors are logged: standard error. A test replaces it.
var (
	logMu     sync.Mutex
	logWriter io.Writer = os.Stderr
)

// logInternal writes a line to the log, ignoring a failed write: a closed standard error must not
// stop a request.
func logInternal(format string, args ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	_, _ = fmt.Fprintf(logWriter, format, args...)
}

// errInternal is a 500 with a generic message. The real error goes to standard error only.
func errInternal(err any) *apiError {
	logInternal("internal error: %v\n", err)
	return newError(http.StatusInternalServerError, "internal", "internal server error")
}

// maxMessageChars is the longest message or detail of an error body, in characters, "…" included.
const maxMessageChars = 512

// maxErrorBodyBytes is the total of serialized (JSON-escaped) bytes that the message and the details of
// an error body may use: 64 KiB. The punctuation and the "and N more" entry are paid from the budget
// too; only the envelope and a small overshoot after the last entry admitted (a cut string's "…") come
// on top.
const maxErrorBodyBytes = 64 * 1024

// maxReportedErrors is the most entries a list of problems keeps (the Rust MAX_REPORTED_ERRORS).
const maxReportedErrors = 100

// budget is what is left of the bytes of one body.
type budget int

// escapedLen is the bytes a character takes in a JSON string, as serde_json writes it.
func escapedLen(c rune) int {
	switch {
	case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t' || c == '\b' || c == '\f':
		return 2
	case c < 0x20:
		return 6
	}
	return utf8.RuneLen(c)
}

func (b *budget) spend(n int) {
	if int(*b) < n {
		*b = 0
		return
	}
	*b -= budget(n)
}

// cut is text for a body: whole if it has at most maxMessageChars characters and fits the budget, else
// its first characters (at most 511, fewer if the budget is short) and "…". The budget is charged the
// escaped bytes and the quotes. It reads at most 513 characters.
func cut(text string, b *budget) string {
	room := max(int(*b)-2, 0)
	used, count, whole := 0, 0, true
	for _, c := range text {
		if count >= maxMessageChars || used+escapedLen(c) > room {
			whole = false
			break
		}
		used += escapedLen(c)
		count++
	}
	if whole {
		*b = budget(max(room-used, 0))
		return text
	}
	room = max(room-3, 0)
	var out []rune
	used = 0
	for _, c := range text {
		if len(out) >= maxMessageChars-1 || used+escapedLen(c) > room {
			break
		}
		used += escapedLen(c)
		out = append(out, c)
	}
	out = append(out, '…')
	*b = budget(max(room-used, 0))
	return string(out)
}

// capped is a list of problems bounded for a response: at most maxReportedErrors entries and an "and N
// more" entry, within the budget, each cut to maxMessageChars.
func capped(problems []string, b *budget) []string {
	total := len(problems)
	b.spend(2)
	out := []string{}
	for _, problem := range problems {
		if len(out) >= maxReportedErrors || *b < 8 {
			break
		}
		out = append(out, cut(problem, b))
		b.spend(1)
	}
	if len(out) < total {
		text := fmt.Sprintf("and %d more", total-len(out))
		b.spend(len(text) + 3)
		out = append(out, text)
	}
	return out
}

// freshBudget is a budget of its own, for what a source builds before it is answered.
func freshBudget() *budget { b := budget(maxErrorBodyBytes); return &b }

// cutText is a string cut to maxMessageChars (for the text of a report row and the like).
func cutText(text string) string { return cut(text, freshBudget()) }

// more is the "and N more" entry of an array that stops early.
func more(n int, b *budget) string {
	text := fmt.Sprintf("and %d more", n)
	b.spend(len(text) + 3)
	return text
}

// room says whether an array or object may take another entry: fewer than maxReportedErrors so far and some
// budget left.
func room(taken int, b *budget) bool { return taken < maxReportedErrors && *b >= 8 }

// boundIn is value within the budget. The budget is exact up to the last entry admitted: it is charged the
// strings and keys as escaped with their quotes, the numbers, booleans and null as written, 2 bytes per
// container (the brackets), 1 per array entry and 2 per object entry (the comma, and the colon). Strings and
// keys are cut, arrays take entries in order while the count and the budget last and end with "and N more".
// An object is walked with its keys sorted, in three groups: numbers, booleans and null first (the counts of a
// summary), then strings (each at most maxMessageChars characters, so mode, code and message survive), then
// arrays and objects; an entry that finds no room is left out. What is dropped is only counted.
func boundIn(value any, b *budget) any {
	switch v := value.(type) {
	case string:
		return cut(v, b)
	case []any:
		b.spend(2)
		out := make([]any, 0, min(len(v), maxReportedErrors+1))
		for _, item := range v {
			if !room(len(out), b) {
				break
			}
			out = append(out, boundIn(item, b))
			b.spend(1)
		}
		if len(out) < len(v) {
			out = append(out, more(len(v)-len(out), b))
		}
		return out
	case map[string]any:
		b.spend(2)
		group := func(x any) int {
			switch x.(type) {
			case string:
				return 1
			case []any, map[string]any:
				return 2
			}
			return 0
		}
		keys := sortedKeys(v)
		sort.SliceStable(keys, func(i, j int) bool { return group(v[keys[i]]) < group(v[keys[j]]) })
		out := map[string]any{}
		for _, key := range keys {
			if !room(len(out), b) {
				break
			}
			cutKey := cut(key, b)
			b.spend(2)
			out[cutKey] = boundIn(v[key], b)
		}
		return out
	}
	text, _ := marshal(value)
	b.spend(len(text))
	return value
}

// boundedJSON is boundIn with a budget of its own: the bound of an import summary that is answered as a
// success (a dry run lists the errors of its rows too).
func boundedJSON(value any) any { return boundIn(value, freshBudget()) }

// body is the JSON value of the envelope, bounded.
func (e *apiError) body() map[string]any {
	b := budget(maxErrorBodyBytes)
	message := cut(e.message, &b)
	inner := map[string]any{"code": e.code, "message": message}
	if e.detailsJSON != nil {
		inner["details"] = boundIn(e.detailsJSON, &b)
	} else if len(e.details) > 0 {
		inner["details"] = capped(e.details, &b)
	}
	return map[string]any{"error": inner}
}

// write answers with the error.
func (e *apiError) write(w http.ResponseWriter) {
	if e.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.retryAfter))
	}
	writeJSON(w, e.status, e.body())
}

// fromError turns any error of the layers below into a response. Anything that is not the caller's
// fault is logged and answered generically.
func fromError(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	var mf *MethodFailure
	if errors.As(err, &mf) {
		return fromMethodFailure(mf)
	}
	var dbe *db.Error
	if errors.As(err, &dbe) {
		return fromDB(dbe)
	}
	var auth *db.AuthError
	if errors.As(err, &auth) {
		return fromAuth(auth)
	}
	var je *JSONError
	if errors.As(err, &je) {
		return fromJSON(je)
	}
	return errInternal(err)
}

// fromDB maps a database error (the Rust impl From<DbError> for ApiError).
func fromDB(e *db.Error) *apiError {
	text := e.Error()
	switch e.Kind {
	case db.ErrValidation:
		details := make([]string, len(e.Validation))
		notPermitted := false
		for i, v := range e.Validation {
			details[i] = v.Error()
			notPermitted = notPermitted || v.Kind == core.ErrNotPermitted
		}
		api := errValidation(details)
		// A field the caller can read but not write (ADR 0024): still a 422 with the problems listed,
		// under a code that says what is wrong.
		if notPermitted {
			api.code = "field_not_permitted"
			api.message = "a field cannot be changed with your permissions"
		}
		return api
	case db.ErrNaming:
		return errValidation([]string{e.Message})
	case db.ErrExportTooLarge:
		return errExportTooLarge(e.Count, e.Limit)
	// Custom fields (M10.1): an unknown DocType is a 404 like everywhere, a name that is taken a conflict,
	// anything else the field's own problem.
	case db.ErrCustomField:
		switch e.Overlay.Kind {
		case core.OverlayUnknownDocType:
			return errNotFound("unknown doctype")
		case core.OverlayCollision:
			return newError(http.StatusConflict, "conflict", text)
		}
		return errValidation([]string{e.Overlay.Error()})
	case db.ErrCustomFieldExists:
		return newError(http.StatusConflict, "conflict", text)
	case db.ErrCustomFieldNotFound:
		return errNotFound(text)
	case db.ErrNotFound:
		return errNotFound(text)
	case db.ErrLockTimeout:
		// The migrate lock is held by a `migrate` or another schema change: a clear, retryable answer.
		api := newError(http.StatusServiceUnavailable, "lock_timeout",
			"a database lock is busy (for example a migrate is running); retry shortly")
		api.retryAfter = 5
		return api
	case db.ErrUnknownDocType, db.ErrChildDocType:
		return errNotFound("unknown doctype")
	case db.ErrPermissionDenied, db.ErrEditDenied, db.ErrTransitionDenied:
		return newError(http.StatusForbidden, "permission_denied", text)
	case db.ErrWorkflowRequired:
		return newError(http.StatusConflict, "workflow_required", text)
	case db.ErrNoWorkflow:
		return newError(http.StatusConflict, "no_workflow", text)
	case db.ErrUnknownTransition:
		return newError(http.StatusConflict, "unknown_transition", text)
	case db.ErrInvalidView:
		return errValidation([]string{e.Message})
	case db.ErrViewExists:
		return newError(http.StatusConflict, "conflict", text)
	case db.ErrViewDenied:
		return newError(http.StatusForbidden, "permission_denied", text)
	// The same answer for a report that does not exist and one the caller may not run.
	case db.ErrReportNotFound:
		return errNotFound("report not found")
	case db.ErrReportTimeout:
		return newError(http.StatusGatewayTimeout, "report_timeout", "the report took too long; narrow the parameters")
	case db.ErrInvalidQuery:
		return errInvalidQuery(text)
	case db.ErrNoChanges:
		return errBadRequest(text)
	case db.ErrConflict, db.ErrChangedWhileReading:
		return newError(http.StatusConflict, "conflict", text)
	case db.ErrInvalidDocStatus:
		return newError(http.StatusConflict, "invalid_docstatus", text)
	case db.ErrAlreadyAmended:
		return newError(http.StatusConflict, "already_amended", text)
	case db.ErrFieldLocked:
		return newError(http.StatusConflict, "field_locked", text)
	case db.ErrLinkedDocumentExists:
		return newError(http.StatusConflict, "linked_document_exists", text)
	case db.ErrUniqueViolation:
		// The server's message names the constraint and the conflicting key: not sent.
		return newError(http.StatusConflict, "unique_violation", "a document with the same unique value already exists")
	case db.ErrNotSubmittable:
		return newError(http.StatusUnprocessableEntity, "not_submittable", text)
	case db.ErrHook:
		return newError(http.StatusUnprocessableEntity, "hook_rejected", text)
	}
	// ErrReportReadOnly and ErrReport are mistakes of the app: the message names the report and never the
	// SQL; it is logged, not sent. ErrJob (a mistake of the app, not of the caller), ErrPostgres, ErrPool, ErrDecode and everything
	// else: the caller cannot help.
	return errInternal(e)
}

// fromAuth maps an authentication error (the Rust impl From<AuthError> for ApiError).
func fromAuth(e *db.AuthError) *apiError {
	switch e.Kind {
	case db.AuthInvalidCredentials:
		return newError(http.StatusUnauthorized, "unauthenticated", "invalid username or password")
	case db.AuthDB:
		var inner *db.Error
		if errors.As(e.Err, &inner) {
			return fromDB(inner)
		}
		return errInternal(e)
	case db.AuthUserNotFound:
		return errNotFound("user not found")
	case db.AuthInvalidUserPermission:
		return errValidation([]string{e.Detail})
	case db.AuthInvalidUsername:
		return errValidation([]string{"invalid username: " + e.Detail})
	}
	return errInternal(e)
}

// fromJSON maps a problem with JSON input (the Rust impl From<JsonError> for ApiError).
func fromJSON(e *JSONError) *apiError {
	switch e.Kind {
	case JSONSyntax:
		return errBadRequest("the body is not valid JSON")
	case JSONNotAnObject:
		return errBadRequest("the data must be a JSON object")
	case JSONField:
		return errValidation([]string{e.Error()})
	case JSONQuery:
		return errInvalidQuery(e.Problem)
	}
	return errInternal(e)
}
