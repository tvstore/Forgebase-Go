package db

import (
	"slices"
	"strings"

	"github.com/tvstore/forgebase-go/internal/core"
)

// The permission checks of the document operations (ADR 0011, ADR 0025). Every decision comes from
// core.Evaluate, core.RowScopeOf and the other pure evaluators of internal/core; nothing here
// compares roles itself.

// grant is what a caller may do with one existing document for an operation, decided by the
// evaluator.
type grant struct {
	right core.Right
	// read is what the caller can see of the DocType: the owner of an if_owner read and the user
	// permissions (M10.3). Applied in the SQL that finds the document.
	read readScope
	// rightOwner is, with an if_owner right, the owner of the only documents the right applies to;
	// rightOwned says there is such a limit.
	rightOwner string
	rightOwned bool
}

// target is the document an operation works on, for the errors of a write that matched no row.
type target struct {
	name            string
	expectedVersion int64
	caller          core.Caller
	read            readScope
}

func permissionDenied(doctype *core.DocType, right core.Right) error {
	return &Error{Kind: ErrPermissionDenied, DocType: doctype.Name, Right: right}
}

// accessTo is the caller's access for right on the DocType, or ErrPermissionDenied when there is
// none. The single call to the evaluator for everything but links.
func accessTo(doctype *core.DocType, caller core.Caller, right core.Right) (core.Access, error) {
	access := core.Evaluate(doctype, caller, right)
	if access == core.AccessDenied {
		return access, permissionDenied(doctype, right)
	}
	return access, nil
}

// ownerScope is the owner a statement must be limited to (AND "owner" = $n) for this access, and
// whether there is such a limit.
func ownerScope(access core.Access, caller core.Caller) (string, bool) {
	if access == core.AccessOwnerOnly {
		return caller.Name(), true
	}
	return "", false
}

// readScopeOf is what the caller may see of doctype: the owner scope of their read right
// (if_owner) and the scope of their user permissions, both from the one evaluator of internal/core.
// No read right at all is ErrPermissionDenied. Every statement that finds a document for a caller
// (get, list, summary, the locking reads, ...) takes its conditions from here.
func readScopeOf(doctype *core.DocType, caller core.Caller) (readScope, error) {
	access, err := accessTo(doctype, caller, core.RightRead)
	if err != nil {
		return readScope{}, err
	}
	owner, owned := ownerScope(access, caller)
	return readScope{ownerOnly: owned, owner: owner, rows: core.RowScopeOf(doctype, caller)}, nil
}

// refuseRestrictedCreate: a caller restricted to some documents of the DocType (user permissions)
// cannot create one: its name is not among the allowed ones, so they could not see the result.
// Refused with ErrPermissionDenied for right (create, or amend, which makes a new document), before
// anything is looked up.
func refuseRestrictedCreate(doctype *core.DocType, caller core.Caller, right core.Right) error {
	if !core.MayMakeDocuments(doctype, caller) {
		return permissionDenied(doctype, right)
	}
	return nil
}

// grantFor checks the caller's name and its right to work on existing documents (right and read),
// before any document is looked up. It fails with ErrPermissionDenied when either is denied at the
// DocType level, which says nothing about any document.
func grantFor(doctype *core.DocType, caller core.Caller, right core.Right) (*grant, error) {
	if err := checkActor(caller.Name()); err != nil {
		return nil, err
	}
	read, err := readScopeOf(doctype, caller)
	if err != nil {
		return nil, permissionDenied(doctype, right)
	}
	needed, err := accessTo(doctype, caller, right)
	if err != nil {
		return nil, err
	}
	owner, owned := ownerScope(needed, caller)
	return &grant{right: right, read: read, rightOwner: owner, rightOwned: owned}, nil
}

// checkActor refuses an actor that is empty or has a NUL character.
func checkActor(actor string) error {
	if actor == "" || strings.ContainsRune(actor, 0) {
		return &Error{Kind: ErrInvalidActor}
	}
	return nil
}

// requireNoWorkflow: submit and cancel are refused on a DocType with a workflow: its documents
// change state only through a transition (ADR 0015). The first check of both, so it says nothing
// about a document.
func requireNoWorkflow(doctype *core.DocType, operation string) error {
	if doctype.Workflow != nil {
		return &Error{Kind: ErrWorkflowRequired, DocType: doctype.Name, Operation: operation}
	}
	return nil
}

// checkEditRoles: a draft in a state with edit_roles can be updated only by a caller who has one
// of them (on top of the write right). The system context and an administrator pass, like
// everywhere. A state without edit_roles, an unknown state and a document that is not a draft
// change nothing.
func checkEditRoles(doctype *core.DocType, current *StoredDocument, caller core.Caller) error {
	if current.DocStatus != nil && (*current.DocStatus == core.Submitted || *current.DocStatus == core.Cancelled) {
		return nil
	}
	if current.WorkflowState == "" || doctype.Workflow == nil {
		return nil
	}
	state := doctype.Workflow.State(current.WorkflowState)
	if state == nil || len(state.EditRoles) == 0 {
		return nil
	}
	allowed := caller.IsSystem()
	if !allowed {
		for _, r := range state.EditRoles {
			if slices.Contains(caller.Roles, r) {
				allowed = true
				break
			}
		}
	}
	if allowed || caller.BypassesPermissions() {
		return nil
	}
	return &Error{Kind: ErrEditDenied, DocType: doctype.Name, State: state.Name}
}

// canReadDoctype: whether the caller has any read access to the DocType (used to decide what an
// error may say).
func canReadDoctype(registry *core.Registry, doctype string, caller core.Caller) bool {
	d := registry.Get(doctype)
	return d != nil && core.Evaluate(d, caller, core.RightRead) != core.AccessDenied
}

// owningDoctype is, for a link from a child table row, the DocType that owns the row; any other
// name unchanged.
func owningDoctype(registry *core.Registry, referencing string) string {
	child := registry.Get(referencing)
	if child == nil || !child.IsChild {
		return referencing
	}
	if parent, _ := registry.ParentOf(child.Name); parent != nil {
		return parent.Name
	}
	return referencing
}

// documentDocType is the DocType that documents of name belong to: it must be in the registry and
// not a child.
func documentDocType(registry *core.Registry, name string) (*core.DocType, error) {
	d := registry.Get(name)
	switch {
	case d == nil:
		return nil, &Error{Kind: ErrUnknownDocType, Name: name}
	case d.IsChild:
		return nil, &Error{Kind: ErrChildDocType, DocType: name}
	}
	return d, nil
}

// lookupDocType is the DocType a Link or Table field points at. A registry that passed
// CheckReferences has it.
func lookupDocType(registry *core.Registry, target string) (*core.DocType, error) {
	d := registry.Get(target)
	if d == nil {
		return nil, &Error{Kind: ErrUnknownDocType, Name: target}
	}
	return d, nil
}
