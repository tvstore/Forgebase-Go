package core

import (
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The permission model, phase 1 (M3.2, ADR 0011): rules, rights, the caller and the one evaluator
// that decides.
//
// A DocType declares [[permissions]] rules: each names a role and the Rights it grants, optionally
// only on documents the caller owns (if_owner). There is no inheritance and no deny rule: access
// is the union of what the caller's roles grant, and a DocType without rules is inaccessible to
// everyone except the Administrator role and the trusted system Caller (deny by default). Child
// DocTypes have no rules: their rows follow the parent.
//
// Evaluate is the only place that makes a permission decision. It is pure (no I/O, no clock):
// internal/db calls it and turns the Access it returns into SQL (for read access, WHERE owner =
// $caller) or into a refusal. Nothing else compares roles.
//
// Field levels (M10.2, ADR 0024). A field has a PermLevel (0 to 9, 0 by default) and a rule has a
// Level. Evaluate decides about documents with the rules of level 0 only; FieldAccessOf decides
// which levels of fields a caller reads and writes, and ReadableFields, ReadableField and
// ReadableDocType apply the answer to the metadata. A field the caller cannot read is unknown to
// the caller: it is not in the metadata the caller works with, so every lookup of it fails exactly
// like a lookup of a name that does not exist.
//
// User permissions (M10.3, ADR 0025). A user Caller carries its UserRestrictions: for some
// DocTypes, the names of the only documents the user may see. RowScopeOf turns them into a
// RowScope for one DocType, the rows of it the caller may see on top of the rights of Evaluate:
// internal/db only turns that into SQL, so what a caller may see is decided here and nowhere else.

// Administrator is the built-in role that bypasses every permission rule.
const Administrator = "Administrator"

// MaxPermLevel is the highest permission level of a field or a rule (levels are 0 to 9).
const MaxPermLevel uint8 = 9

// MaxUserPermissions is the most documents one user can be restricted to on one DocType (M10.3,
// ADR 0025). The names are bound as one array in every statement that reads documents, so the
// number is kept small.
const MaxUserPermissions = 1000

// MaxRoleLen is the longest allowed role name, in characters.
const MaxRoleLen = 140

// CheckRoleName checks a role name: 1 to MaxRoleLen characters, not blank, no control characters.
// The same rule applies to the roles of users (internal/db's auth) and to the roles in
// [[permissions]], so a rule can always name a role a user can have. The result is the reason,
// ready to follow "invalid role: ", or "" when the name is valid.
func CheckRoleName(role string) string {
	length := utf8.RuneCountInString(role)
	switch {
	case length == 0 || length > MaxRoleLen:
		return "must be 1 to 140 characters"
	case strings.TrimSpace(role) == "":
		return "must not be blank"
	case strings.IndexFunc(role, unicode.IsControl) >= 0:
		return "must not contain control characters"
	}
	return ""
}

// Right is one thing a caller may do with documents of a DocType.
type Right int

const (
	// RightRead: see documents (get and list).
	RightRead Right = iota + 1
	// RightWrite: change a document.
	RightWrite
	// RightCreate: create a document.
	RightCreate
	// RightDelete: delete a document.
	RightDelete
	// RightSubmit: submit a draft (submittable DocTypes).
	RightSubmit
	// RightCancel: cancel a submitted document (submittable DocTypes).
	RightCancel
	// RightAmend: amend a cancelled document (submittable DocTypes).
	RightAmend
	// RightExport: download documents as a file (M9.3, ADR 0021). Needs read as well.
	RightExport
	// RightImport: upload a file of documents (M9.3, ADR 0021). Needs create (and write to upsert).
	RightImport
)

// AllRights is every right, in the order of the [[permissions]] keys.
var AllRights = [9]Right{
	RightRead, RightWrite, RightCreate, RightDelete, RightSubmit, RightCancel, RightAmend,
	RightExport, RightImport,
}

// Name is the key used in [[permissions]] and in messages: read, write, ...
func (r Right) Name() string {
	switch r {
	case RightRead:
		return "read"
	case RightWrite:
		return "write"
	case RightCreate:
		return "create"
	case RightDelete:
		return "delete"
	case RightSubmit:
		return "submit"
	case RightCancel:
		return "cancel"
	case RightAmend:
		return "amend"
	case RightExport:
		return "export"
	case RightImport:
		return "import"
	}
	return "unknown"
}

func (r Right) String() string { return r.Name() }

// PermissionRule is a [[permissions]] entry of a DocType: the rights one role has, all false by
// default.
type PermissionRule struct {
	// Role the rule is for, see CheckRoleName.
	Role string
	// Read: may read.
	Read bool
	// Write: may write.
	Write bool
	// Create: may create.
	Create bool
	// Delete: may delete.
	Delete bool
	// Submit: may submit. Only allowed on a submittable DocType.
	Submit bool
	// Cancel: may cancel. Only allowed on a submittable DocType.
	Cancel bool
	// Amend: may amend. Only allowed on a submittable DocType.
	Amend bool
	// Export: may export documents as a file (needs read too).
	Export bool
	// Import: may import documents from a file (needs create, and write to upsert).
	Import bool
	// IfOwner: the rights apply to documents whose owner is the caller only. (A create is always
	// for a new document the caller owns, so IfOwner does not restrict create.)
	IfOwner bool
	// Level is the permission level of the rule (M10.2, ADR 0024), 0 to MaxPermLevel, 0 by
	// default. A rule at level 0 is a document rule as in ADR 0011. A rule at level n > 0 grants
	// read and write on the fields marked PermLevel = n and nothing else (see FieldAccessOf).
	Level uint8
}

// NewPermissionRule is a rule for role that grants nothing yet: set the rights on the result.
func NewPermissionRule(role string) PermissionRule { return PermissionRule{Role: role} }

// Grants reports whether the rule grants right (ignoring IfOwner).
func (r PermissionRule) Grants(right Right) bool {
	switch right {
	case RightRead:
		return r.Read
	case RightWrite:
		return r.Write
	case RightCreate:
		return r.Create
	case RightDelete:
		return r.Delete
	case RightSubmit:
		return r.Submit
	case RightCancel:
		return r.Cancel
	case RightAmend:
		return r.Amend
	case RightExport:
		return r.Export
	case RightImport:
		return r.Import
	}
	return false
}

// CallerKind says who performs an operation.
type CallerKind int

const (
	// CallerUser is a real user with the roles they have.
	CallerUser CallerKind = iota
	// CallerSystem is the trusted system context.
	CallerSystem
)

// Caller is who performs an operation: a real user, or the trusted system. Build one with
// UserCaller or SystemCaller. A Caller is a plain value; do not change a Caller's Roles after use.
type Caller struct {
	Kind CallerKind
	// Username is the user's name for a user caller, recorded as owner and modified_by and given
	// to hooks. Empty for the system.
	Username string
	// Roles come from the database (fb_user_role) at the time the caller is built; this type does
	// not check them. The user is an administrator if Roles contains Administrator.
	Roles []string
	// Restrictions are the documents the user is restricted to (M10.3, ADR 0025); empty for a user
	// without user permissions. Read from the database with the roles.
	Restrictions UserRestrictions
	// Actor is who to record for the system context, as owner and modified_by and given to hooks,
	// like a username. Empty for a user.
	Actor string
}

// UserCaller is a user caller.
func UserCaller(username string, roles []string) Caller {
	return Caller{Kind: CallerUser, Username: username, Roles: roles}
}

// SystemCaller is the trusted system caller recorded as actor: it bypasses every permission rule.
// For code that is not acting for a user, such as the command-line tool, migrations, jobs and
// tests. Never build one from request data.
func SystemCaller(actor string) Caller { return Caller{Kind: CallerSystem, Actor: actor} }

// WithRestrictions is this caller with the given user permissions (see UserRestrictions). The
// system context is returned unchanged: it is never restricted.
func (c Caller) WithRestrictions(r UserRestrictions) Caller {
	if c.Kind == CallerUser {
		c.Restrictions = r
	}
	return c
}

// Name is the name recorded as owner / modified_by and given to hooks: the username, or the
// system actor.
func (c Caller) Name() string {
	if c.Kind == CallerSystem {
		return c.Actor
	}
	return c.Username
}

// IsSystem reports whether this is the trusted system context.
func (c Caller) IsSystem() bool { return c.Kind == CallerSystem }

// BypassesPermissions is true for the system context and for a user with the Administrator role:
// the callers the rules do not apply to.
func (c Caller) BypassesPermissions() bool {
	if c.Kind == CallerSystem {
		return true
	}
	return slices.Contains(c.Roles, Administrator)
}

// UserRestrictions are the user permissions of one user (M10.3, ADR 0025): for a DocType, the
// names of the only documents of it the user may see. A DocType that is not listed is not
// restricted for the user, and so is one with an empty list (the empty value means unrestricted).
// It is immutable once built and cheap to copy (the map is shared).
type UserRestrictions struct {
	m map[string][]string
}

// NewUserRestrictions builds the restrictions from (doctype, name) pairs: each DocType's names
// are sorted and deduplicated.
func NewUserRestrictions(pairs [][2]string) UserRestrictions {
	m := map[string][]string{}
	for _, p := range pairs {
		m[p[0]] = append(m[p[0]], p[1])
	}
	for k, names := range m {
		sort.Strings(names)
		m[k] = slices.Compact(names)
	}
	return UserRestrictions{m: m}
}

// Allowed returns the names the user may see of doctype, sorted; nil when the user is not
// restricted on it.
func (r UserRestrictions) Allowed(doctype string) []string {
	names := r.m[doctype]
	if len(names) == 0 {
		return nil
	}
	return names
}

// IsEmpty is true when the user has no user permissions at all.
func (r UserRestrictions) IsEmpty() bool {
	for _, names := range r.m {
		if len(names) > 0 {
			return false
		}
	}
	return true
}

// LinkScope is one top-level Link field of a DocType whose target the caller is restricted on.
type LinkScope struct {
	// Field is the Link field.
	Field string
	// Target is the DocType the field points at.
	Target string
	// Allowed are the names of the target the caller may see (sorted, never empty).
	Allowed []string
}

// RowScope says which documents of one DocType a caller may see because of their user permissions,
// as decided by RowScopeOf. It is in addition to the rights of Evaluate (and to if_owner): every
// part must hold. Empty for a caller without a restriction that touches the DocType.
type RowScope struct {
	// Names: the DocType itself is restricted: the document's own name must be one of these. nil
	// when it is not restricted.
	Names []string
	// Links are the top-level Link fields to a restricted target: its value must be empty or one
	// of the allowed names of its target. All of them must hold (AND), in declaration order.
	Links []LinkScope
}

// IsEmpty is true when nothing is restricted: every statement is then exactly what it is without
// user permissions.
func (s RowScope) IsEmpty() bool { return s.Names == nil && len(s.Links) == 0 }

// MayMakeDocuments reports whether the caller may make new documents of doctype (create, and amend,
// which makes a new document) as far as user permissions go: a user restricted on the DocType itself
// may not, because the new name could not be checked against the allowed names and the user could
// not see the result (ADR 0025). The role right is Evaluate's business; internal/db and the
// metadata API both ask this one function.
func MayMakeDocuments(doctype *DocType, caller Caller) bool {
	return RowScopeOf(doctype, caller).Names == nil
}

// RowScopeOf decides which documents of doctype caller may see because of their user permissions
// (M10.3, ADR 0025). Pure, like Evaluate.
//
//   - The system context, an administrator and a user without restrictions get the empty scope.
//   - Own name. If the caller is restricted on doctype itself, the name of a document must be one
//     of the allowed names.
//   - Links. For each top-level Link field of doctype whose target the caller is restricted on,
//     the value must be empty or one of the allowed names of the target. Every such field must
//     hold, whatever its perm_level: a field the caller cannot read still restricts, and its value
//     is never revealed. A field that is not in doctype (a disabled custom field is not in the
//     effective registry) restricts nothing.
//   - Child rows are not considered: a Link inside a row of a Table field does not decide the
//     parent's visibility. A child DocType is never read alone and has an empty scope.
func RowScopeOf(doctype *DocType, caller Caller) RowScope {
	if caller.Kind != CallerUser {
		return RowScope{}
	}
	if doctype.IsChild || caller.Restrictions.IsEmpty() || caller.BypassesPermissions() {
		return RowScope{}
	}
	var links []LinkScope
	for i := range doctype.Fields {
		f := &doctype.Fields[i]
		if f.Type.Kind != FieldLink {
			continue
		}
		allowed := caller.Restrictions.Allowed(f.Type.Target)
		if allowed == nil {
			continue
		}
		links = append(links, LinkScope{Field: f.Name, Target: f.Type.Target, Allowed: allowed})
	}
	return RowScope{Names: caller.Restrictions.Allowed(doctype.Name), Links: links}
}

// Access is how much of a DocType's documents a caller may use a right on.
type Access int

const (
	// AccessAll: every document.
	AccessAll Access = iota + 1
	// AccessOwnerOnly: only documents whose owner is the caller (if_owner).
	AccessOwnerOnly
	// AccessDenied: no document. Not a decision about one document: it does not depend on the data.
	AccessDenied
)

// Evaluate decides how far caller may use right on documents of doctype. The one permission
// decision of Forgebase (see the comment at the top of this file).
//
//   - The system context and an administrator get AccessAll for every right on every DocType.
//   - Otherwise the result is the union over the rules whose role the caller has and that grant
//     right: a rule without IfOwner gives AccessAll; if there is none, a rule with IfOwner gives
//     AccessOwnerOnly; if there is none, AccessDenied.
//   - A DocType without rules (a child DocType too) is AccessDenied for everyone else.
//
// A role name is compared exactly (case-sensitive, like the roles of users).
func Evaluate(doctype *DocType, caller Caller, right Right) Access {
	if caller.BypassesPermissions() {
		return AccessAll
	}
	if caller.Kind != CallerUser {
		return AccessDenied // unreachable: the system caller bypasses above
	}
	access := AccessDenied
	// Only the rules of level 0 decide about documents; a rule of a higher level is about fields.
	for _, rule := range doctype.Permissions {
		if rule.Level != 0 {
			continue
		}
		if rule.Grants(right) && slices.Contains(caller.Roles, rule.Role) {
			if !rule.IfOwner {
				return AccessAll
			}
			access = AccessOwnerOnly
		}
	}
	return access
}

// FieldAccess says which permission levels of fields a caller may read and write on one DocType, as
// decided by FieldAccessOf: a bit per level 0 to MaxPermLevel.
//
// Level 0 is not a filter on the fields: a field at level 0 is governed by the document rights
// alone (the caller is refused or not before any field is looked at), so a DocType without
// perm_level behaves exactly as before. The level 0 bits are the base that the higher levels need
// (see FieldAccessOf).
type FieldAccess struct {
	read, write uint16
}

const fieldAccessMask uint16 = (1 << (uint16(MaxPermLevel) + 1)) - 1

// AllFieldAccess is every level readable and writable: what the system context and an
// administrator have.
var AllFieldAccess = FieldAccess{read: fieldAccessMask, write: fieldAccessMask}

// CanRead reports whether fields at level may be read. A level above MaxPermLevel never can.
func (a FieldAccess) CanRead(level uint8) bool {
	return level <= MaxPermLevel && a.read&(1<<level) != 0
}

// Shows reports whether a field at level is shown to the caller: every field at level 0 (the
// document rights decide about those, see the type comment) and a field at a higher level the
// caller can read. What the helpers that hide fields use, so a DocType without perm_level is never
// changed by them.
func (a FieldAccess) Shows(level uint8) bool { return level == 0 || a.CanRead(level) }

// CanWrite reports whether fields at level may be written. A level above MaxPermLevel never can.
func (a FieldAccess) CanWrite(level uint8) bool {
	return level <= MaxPermLevel && a.write&(1<<level) != 0
}

// IsAll is true when every level can be read and written (no field is hidden or protected).
func (a FieldAccess) IsAll() bool { return a == AllFieldAccess }

// FieldAccessOf decides which levels of fields caller may read and write on doctype (M10.2, ADR
// 0024). Pure, like Evaluate, and built on it:
//
//   - The system context and an administrator get every level.
//   - Level 0 is readable when Evaluate does not deny read, and writable when it does not deny
//     write or create. Whether the caller may write one particular document is decided by the
//     document checks of internal/db, which run first and name their own right (create for an
//     insert, write for an update); the bit only says that the caller has one of them at all, so
//     that the higher levels need it too.
//   - Level n > 0 is readable when level 0 is readable and a role of the caller has a rule
//     level = n with read; it is writable when level 0 is writable and a role of the caller has a
//     rule level = n with write. Rules of a level have no if_owner (a definition error), so
//     if_owner at level 0 alone limits the documents a level is seen on: an owner-only user sees a
//     level on their own documents only.
//   - A level that no rule names is readable by nobody but the administrator and the system.
func FieldAccessOf(doctype *DocType, caller Caller) FieldAccess {
	if caller.Kind != CallerUser || caller.BypassesPermissions() {
		return AllFieldAccess
	}
	readable := Evaluate(doctype, caller, RightRead) != AccessDenied
	writable := Evaluate(doctype, caller, RightWrite) != AccessDenied ||
		Evaluate(doctype, caller, RightCreate) != AccessDenied
	var access FieldAccess
	if readable {
		access.read |= 1
	}
	if writable {
		access.write |= 1
	}
	for _, rule := range doctype.Permissions {
		if rule.Level == 0 || rule.Level > MaxPermLevel {
			continue
		}
		if !slices.Contains(caller.Roles, rule.Role) {
			continue
		}
		if readable && rule.Read {
			access.read |= 1 << rule.Level
		}
		if writable && rule.Write {
			access.write |= 1 << rule.Level
		}
	}
	return access
}

// ReadableFields are the fields of doctype that access lets the caller read, in declaration order.
func ReadableFields(doctype *DocType, access FieldAccess) []*DocField {
	var out []*DocField
	for i := range doctype.Fields {
		if access.Shows(doctype.Fields[i].PermLevel) {
			out = append(out, &doctype.Fields[i])
		}
	}
	return out
}

// ReadableField is the field called name if the caller may read it. A field the caller cannot read
// gives nil, exactly like a name that is not a field: callers report both as an unknown field, so
// that nothing tells a hidden field from a missing one.
func ReadableField(doctype *DocType, access FieldAccess, name string) *DocField {
	f := doctype.Field(name)
	if f == nil || !access.Shows(f.PermLevel) {
		return nil
	}
	return f
}

// ReadableDocType is doctype as the caller sees it: the same DocType without the fields that access
// does not let it read. The same pointer (no copy) when nothing is hidden, which is always the case
// for a DocType without perm_level; otherwise a copy, and doctype is never changed. Everything that
// reads, filters, sorts, groups or exports on behalf of a caller works on this, so a hidden field is
// unknown to it everywhere. Never use it for a write: hooks and the stored document need every
// field.
func ReadableDocType(doctype *DocType, access FieldAccess) *DocType {
	all := true
	for i := range doctype.Fields {
		if !access.Shows(doctype.Fields[i].PermLevel) {
			all = false
			break
		}
	}
	if all {
		return doctype
	}
	visible := doctype.Clone()
	visible.Fields = slices.DeleteFunc(visible.Fields, func(f DocField) bool { return !access.Shows(f.PermLevel) })
	return visible
}

// ReadableDocTypeFor is ReadableDocType for what FieldAccessOf says about caller: the DocType as
// this caller sees it.
func ReadableDocTypeFor(doctype *DocType, caller Caller) *DocType {
	return ReadableDocType(doctype, FieldAccessOf(doctype, caller))
}

// CanRunReport reports whether caller may run a query report (M8.4, ADR 0016) that names roles: the
// system context and an administrator always may; anyone else needs one of the roles. A report
// with no roles is run by nobody else (deny by default). Row-level rules do not apply inside a
// report: this is the whole access check.
func CanRunReport(roles []string, caller Caller) bool {
	if caller.Kind == CallerSystem {
		return true
	}
	if caller.BypassesPermissions() {
		return true
	}
	for _, r := range roles {
		if slices.Contains(caller.Roles, r) {
			return true
		}
	}
	return false
}

// CanCallMethod reports whether caller may call an app method (M9.1, ADR 0019) with this access:
// with AnyUser any caller; with Roles the same rule as CanRunReport (one of the roles, or the
// Administrator; the system context always). This is only the gate: the documents a method reads
// and writes are checked by Evaluate for the caller, as for any other request.
func CanCallMethod(access MethodAccess, caller Caller) bool {
	if access.AnyUser {
		return true
	}
	return CanRunReport(access.Roles, caller)
}

// TransitionAccess decides how far caller may take transition of the workflow of doctype (M8.1, ADR
// 0015, the transition-role rule that extends ADR 0011): the role and the right.
//
//   - The system context and an administrator get AccessAll, as in Evaluate.
//   - Otherwise the caller needs one of the transition's roles and the right that the docstatus move
//     needs (Move.Right: write, submit or cancel). The answer is then what Evaluate says for that
//     right: AccessAll, or AccessOwnerOnly when only an if_owner rule grants it (the caller applies
//     it to the document). A role never grants a right: a transition role without the right is
//     AccessDenied, and so is the right without a transition role.
//   - A DocType without a workflow, or a transition that does not belong to it (an unknown state, a
//     docstatus move a workflow does not allow), is AccessDenied.
//
// The right is decided by Evaluate and nowhere else; this function only adds the role.
func TransitionAccess(doctype *DocType, caller Caller, transition *Transition) Access {
	if doctype.Workflow == nil {
		return AccessDenied
	}
	movement, ok := doctype.Workflow.MoveOf(transition)
	if !ok {
		return AccessDenied
	}
	hasRole := caller.Kind == CallerSystem
	if caller.Kind == CallerUser {
		for _, r := range transition.Roles {
			if slices.Contains(caller.Roles, r) {
				hasRole = true
				break
			}
		}
	}
	if !caller.BypassesPermissions() && !hasRole {
		return AccessDenied
	}
	return Evaluate(doctype, caller, movement.Right())
}
