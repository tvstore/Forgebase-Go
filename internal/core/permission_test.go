package core

import (
	"slices"
	"testing"
)

func userCaller(roles ...string) Caller { return UserCaller("u", append([]string{}, roles...)) }

// permDocType is a submittable DocType with the given rules and no fields.
func permDocType(permissions ...PermissionRule) *DocType {
	return &DocType{Name: "thing", Label: "Thing", Submittable: true, Permissions: permissions}
}

// only is a rule granting exactly right.
func only(role string, right Right, ifOwner bool) PermissionRule {
	rule := NewPermissionRule(role)
	rule.IfOwner = ifOwner
	switch right {
	case RightRead:
		rule.Read = true
	case RightWrite:
		rule.Write = true
	case RightCreate:
		rule.Create = true
	case RightDelete:
		rule.Delete = true
	case RightSubmit:
		rule.Submit = true
	case RightCancel:
		rule.Cancel = true
	case RightAmend:
		rule.Amend = true
	case RightExport:
		rule.Export = true
	case RightImport:
		rule.Import = true
	}
	return rule
}

// Rust: permission.rs::a_report_is_for_its_roles_the_administrator_and_the_system
func TestAReportIsForItsRolesTheAdministratorAndTheSystem(t *testing.T) {
	roles := []string{"Manager"}
	for _, c := range []struct {
		caller Caller
		roles  []string
		want   bool
	}{
		{userCaller("Manager"), roles, true},
		{userCaller("Clerk", "Manager"), roles, true},
		{userCaller("Clerk"), roles, false},
		{userCaller("manager"), roles, false},
		{userCaller(), roles, false},
		{userCaller(Administrator), roles, true},
		{SystemCaller("cli"), roles, true},
		// No roles: only the callers that bypass.
		{userCaller("Manager"), nil, false},
		{userCaller(Administrator), nil, true},
	} {
		if got := CanRunReport(c.roles, c.caller); got != c.want {
			t.Errorf("CanRunReport(%v, %+v) = %v", c.roles, c.caller, got)
		}
	}
}

// Rust: permission.rs::a_method_is_for_its_roles_the_administrator_or_any_user
func TestAMethodIsForItsRolesTheAdministratorOrAnyUser(t *testing.T) {
	roles := MethodRoles("Manager")
	check := func(want bool, access MethodAccess, caller Caller, label string) {
		t.Helper()
		if got := CanCallMethod(access, caller); got != want {
			t.Errorf("%s: got %v", label, got)
		}
	}
	check(true, roles, userCaller("Manager"), "manager")
	check(false, roles, userCaller("Clerk"), "clerk")
	check(false, roles, userCaller(), "no roles")
	check(true, roles, userCaller(Administrator), "administrator")
	check(true, MethodAnyUser(), userCaller(), "any user, no roles")
	check(true, MethodAnyUser(), userCaller("Clerk"), "any user, clerk")
	// A list that somehow is empty admits nobody but the Administrator.
	empty := MethodRoles()
	check(false, empty, userCaller("Manager"), "empty, manager")
	check(true, empty, userCaller(Administrator), "empty, administrator")
}

// Rust: permission.rs::matrix_of_rights_roles_and_if_owner
func TestMatrixOfRightsRolesAndIfOwner(t *testing.T) {
	for _, granted := range AllRights {
		for _, ifOwner := range []bool{false, true} {
			d := permDocType(only("Clerk", granted, ifOwner))
			for _, asked := range AllRights {
				want := AccessDenied
				if asked == granted {
					want = AccessAll
					if ifOwner {
						want = AccessOwnerOnly
					}
				}
				if got := Evaluate(d, userCaller("Clerk"), asked); got != want {
					t.Errorf("granted %s if_owner=%v, asked %s: got %v, want %v", granted, ifOwner, asked, got, want)
				}
				// Without the role nothing is granted; a similar role is not the role.
				for _, other := range [][]string{{}, {"Other"}, {"clerk"}, {"Clerk "}} {
					if got := Evaluate(d, userCaller(other...), asked); got != AccessDenied {
						t.Errorf("role %v: got %v", other, got)
					}
				}
			}
		}
	}
}

// Rust: permission.rs::no_rules_means_denied_except_for_administrator_and_system
func TestNoRulesMeansDeniedExceptForAdministratorAndSystem(t *testing.T) {
	d := permDocType()
	for _, right := range AllRights {
		if Evaluate(d, userCaller("Clerk"), right) != AccessDenied || Evaluate(d, userCaller(), right) != AccessDenied {
			t.Errorf("%s: a plain user has access", right)
		}
		if Evaluate(d, userCaller(Administrator), right) != AccessAll ||
			Evaluate(d, userCaller("Clerk", Administrator), right) != AccessAll ||
			Evaluate(d, SystemCaller("cli"), right) != AccessAll {
			t.Errorf("%s: the administrator or system is refused", right)
		}
	}
}

// Rust: permission.rs::administrator_is_not_limited_by_if_owner_rules
func TestAdministratorIsNotLimitedByIfOwnerRules(t *testing.T) {
	d := permDocType(only("Clerk", RightRead, true))
	if Evaluate(d, userCaller(Administrator), RightRead) != AccessAll || Evaluate(d, userCaller(Administrator), RightDelete) != AccessAll {
		t.Error("administrator limited")
	}
}

// Rust: permission.rs::roles_union_and_the_wider_rule_wins
func TestRolesUnionAndTheWiderRuleWins(t *testing.T) {
	d := permDocType(
		only("Reader", RightRead, false),
		only("Writer", RightWrite, false),
		only("Own", RightRead, true),
		only("Own", RightDelete, true),
	)
	both := userCaller("Reader", "Writer")
	if Evaluate(d, both, RightRead) != AccessAll || Evaluate(d, both, RightWrite) != AccessAll || Evaluate(d, both, RightDelete) != AccessDenied {
		t.Error("reader + writer")
	}
	// OwnerOnly from one role and All from another: All.
	mixed := userCaller("Own", "Reader")
	if Evaluate(d, mixed, RightRead) != AccessAll || Evaluate(d, mixed, RightDelete) != AccessOwnerOnly {
		t.Error("mixed")
	}
	// Declaration order does not matter.
	reversed := slices.Clone(d.Permissions)
	slices.Reverse(reversed)
	if Evaluate(permDocType(reversed...), mixed, RightRead) != AccessAll {
		t.Error("reversed")
	}
	// One rule can grant several rights.
	multi := permDocType(PermissionRule{Role: "Clerk", Read: true, Write: true})
	if Evaluate(multi, userCaller("Clerk"), RightWrite) != AccessAll || Evaluate(multi, userCaller("Clerk"), RightCreate) != AccessDenied {
		t.Error("multi")
	}
}

// Rust: permission.rs::caller_helpers
func TestCallerHelpers(t *testing.T) {
	if UserCaller("ann", nil).Name() != "ann" || SystemCaller("cli").Name() != "cli" {
		t.Error("names")
	}
	if !SystemCaller("cli").BypassesPermissions() || !userCaller(Administrator).BypassesPermissions() ||
		userCaller("administrator").BypassesPermissions() || userCaller().BypassesPermissions() {
		t.Error("bypass")
	}
}

// Rust: permission.rs::right_names
func TestRightNames(t *testing.T) {
	var names []string
	for _, r := range AllRights {
		names = append(names, r.Name())
	}
	sameValue(t, names, []string{"read", "write", "create", "delete", "submit", "cancel", "amend", "export", "import"}, "names")
	if RightAmend.String() != "amend" {
		t.Error("String")
	}
}

// Rust: permission.rs::role_name_rules
func TestRoleNameRules(t *testing.T) {
	for _, ok := range []string{"Sales Manager", "Verkäufer", string(make([]rune, 0)) + repeat("r", 140)} {
		if reason := CheckRoleName(ok); reason != "" {
			t.Errorf("%q: %s", ok, reason)
		}
	}
	if CheckRoleName(repeat("r", 141)) != "must be 1 to 140 characters" {
		t.Error("141 characters")
	}
	for _, bad := range []string{"", "   ", "a\tb", "a\x00b", "\n"} {
		if CheckRoleName(bad) == "" {
			t.Errorf("%q accepted", bad)
		}
	}
	if CheckRoleName("") != "must be 1 to 140 characters" || CheckRoleName("  ") != "must not be blank" || CheckRoleName("a\tb") != "must not contain control characters" {
		t.Error("reasons")
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// atLevel is a rule at level granting read and/or write.
func atLevel(role string, level uint8, read, write bool) PermissionRule {
	return PermissionRule{Role: role, Read: read, Write: write, Level: level}
}

// leveled is a DocType with fields at levels 0, 1 and 2.
func leveled(permissions ...PermissionRule) *DocType {
	d := permDocType(permissions...)
	for _, f := range []struct {
		name  string
		level uint8
	}{{"plain", 0}, {"limit", 1}, {"secret", 2}} {
		field := intField(f.name)
		field.PermLevel = f.level
		d.Fields = append(d.Fields, field)
	}
	return d
}

// Rust: permission.rs::a_rule_of_a_level_grants_no_document_right
func TestARuleOfALevelGrantsNoDocumentRight(t *testing.T) {
	// Level 1 rules with read and write, and nothing at level 0: no document access at all.
	d := leveled(atLevel("Clerk", 1, true, true))
	for _, right := range AllRights {
		if Evaluate(d, userCaller("Clerk"), right) != AccessDenied {
			t.Errorf("%s granted", right)
		}
	}
	// Next to a level 0 rule the level 1 rule changes nothing about the document rights.
	d = leveled(only("Clerk", RightRead, true), atLevel("Clerk", 1, true, true))
	if Evaluate(d, userCaller("Clerk"), RightRead) != AccessOwnerOnly || Evaluate(d, userCaller("Clerk"), RightWrite) != AccessDenied {
		t.Error("level rule changed the document rights")
	}
}

// Rust: permission.rs::field_access_system_and_administrator_have_every_level
func TestFieldAccessSystemAndAdministratorHaveEveryLevel(t *testing.T) {
	d := leveled()
	for _, caller := range []Caller{userCaller(Administrator), SystemCaller("cli")} {
		access := FieldAccessOf(d, caller)
		if !access.IsAll() {
			t.Error("not all")
		}
		for level := uint8(0); level <= MaxPermLevel; level++ {
			if !access.CanRead(level) || !access.CanWrite(level) {
				t.Errorf("level %d", level)
			}
		}
	}
	// Even with rules that name only another role and level.
	d = leveled(atLevel("Clerk", 1, true, true))
	if !FieldAccessOf(d, userCaller(Administrator)).IsAll() {
		t.Error("administrator with other rules")
	}
}

// Rust: permission.rs::field_access_matrix_of_level_zero_rights_if_owner_and_level_rules
func TestFieldAccessMatrixOfLevelZeroRightsIfOwnerAndLevelRules(t *testing.T) {
	// Level 0: which rights the caller has; level 1: which of read/write the rule grants.
	for _, l0 := range [][3]bool{
		{false, false, false}, {true, false, false}, {false, true, false}, {false, false, true},
		{true, true, false}, {true, false, true}, {true, true, true},
	} {
		r0, w0, c0 := l0[0], l0[1], l0[2]
		for _, ifOwner := range []bool{false, true} {
			for _, l1 := range [][2]bool{{false, false}, {true, false}, {true, true}, {false, true}} {
				r1, w1 := l1[0], l1[1]
				level0 := NewPermissionRule("Clerk")
				level0.Read, level0.Write, level0.Create, level0.IfOwner = r0, w0, c0, ifOwner
				// An empty rule would be a definition error, but the evaluator does not care.
				d := leveled(level0, atLevel("Clerk", 1, r1, w1))
				access := FieldAccessOf(d, userCaller("Clerk"))
				label := []any{r0, w0, c0, ifOwner, r1, w1}
				// Level 0 follows the rights; if_owner changes nothing about fields.
				if access.CanRead(0) != r0 || access.CanWrite(0) != (w0 || c0) {
					t.Errorf("level 0 %v", label)
				}
				// Level 1 needs level 0 and the rule.
				if access.CanRead(1) != (r0 && r1) || access.CanWrite(1) != ((w0 || c0) && w1) {
					t.Errorf("level 1 %v", label)
				}
				// No rule names level 2, and the other levels are closed.
				for level := uint8(2); level <= MaxPermLevel; level++ {
					if access.CanRead(level) || access.CanWrite(level) {
						t.Errorf("level %d open %v", level, label)
					}
				}
				// Without the role nothing is granted, at any level.
				other := FieldAccessOf(d, userCaller("Other"))
				for level := uint8(0); level <= MaxPermLevel; level++ {
					if other.CanRead(level) || other.CanWrite(level) {
						t.Errorf("other role, level %d %v", level, label)
					}
				}
			}
		}
	}
}

// Rust: permission.rs::field_access_unions_roles_and_levels_are_independent
func TestFieldAccessUnionsRolesAndLevelsAreIndependent(t *testing.T) {
	d := leveled(
		only("Reader", RightRead, false),
		only("Writer", RightWrite, false),
		atLevel("Lead", 1, true, false),
		atLevel("Boss", 1, true, true),
		atLevel("Boss", 2, true, false),
		atLevel("Spy", 2, true, true),
	)
	// A level rule alone gives nothing without a level 0 right.
	spy := FieldAccessOf(d, userCaller("Spy"))
	if spy.CanRead(2) || spy.CanWrite(2) || spy.CanRead(0) {
		t.Error("spy")
	}
	// Reader + Lead: reads level 1 but cannot write it.
	a := FieldAccessOf(d, userCaller("Reader", "Lead"))
	if !a.CanRead(1) || a.CanWrite(1) || a.CanRead(2) {
		t.Error("reader + lead")
	}
	// Reader + Writer + Boss: level 1 read and write, level 2 read only (the rule), level 0 both.
	b := FieldAccessOf(d, userCaller("Reader", "Writer", "Boss"))
	if !(b.CanRead(1) && b.CanWrite(1) && b.CanRead(2) && !b.CanWrite(2) && b.CanRead(0) && b.CanWrite(0)) {
		t.Error("reader + writer + boss")
	}
	// Roles of different levels add up.
	c := FieldAccessOf(d, userCaller("Reader", "Writer", "Lead", "Spy"))
	if !(c.CanRead(1) && !c.CanWrite(1) && c.CanRead(2) && c.CanWrite(2)) {
		t.Error("roles of different levels")
	}
	// A level above the maximum is never accessible, and the bit helpers do not overflow.
	if AllFieldAccess.CanRead(10) || AllFieldAccess.CanWrite(200) {
		t.Error("level above the maximum")
	}
}

func fieldNames(fields []*DocField) []string {
	var out []string
	for _, f := range fields {
		out = append(out, f.Name)
	}
	return out
}

// Rust: permission.rs::readable_fields_and_names_hide_what_the_caller_cannot_read
func TestReadableFieldsAndNamesHideWhatTheCallerCannotRead(t *testing.T) {
	d := leveled(only("Reader", RightRead, false), atLevel("Reader", 1, true, false))
	access := FieldAccessOf(d, userCaller("Reader"))
	if got := fieldNames(ReadableFields(d, access)); !slices.Equal(got, []string{"plain", "limit"}) {
		t.Errorf("readable = %v", got)
	}
	if ReadableField(d, access, "limit") == nil {
		t.Error("limit hidden")
	}
	// A hidden field is exactly like a name that is not a field.
	if ReadableField(d, access, "secret") != nil || ReadableField(d, access, "nope") != nil {
		t.Error("hidden field visible")
	}
	plainReader := FieldAccessOf(d, userCaller())
	// Level 0 is never hidden by field levels (the document rights decide about it).
	if got := fieldNames(ReadableFields(d, plainReader)); !slices.Equal(got, []string{"plain"}) {
		t.Errorf("plain reader = %v", got)
	}
	if !plainReader.Shows(0) || plainReader.Shows(1) || plainReader.CanRead(0) {
		t.Error("shows/can_read")
	}
	// The caller's DocType has the readable fields only, and borrows when nothing is hidden.
	view := ReadableDocType(d, access)
	if len(view.Fields) != 2 || view.Field("secret") != nil {
		t.Errorf("view = %v", fieldNames(ReadableFields(view, AllFieldAccess)))
	}
	if len(d.Fields) != 3 {
		t.Error("the original DocType was changed")
	}
	if ReadableDocType(d, AllFieldAccess) != d {
		t.Error("ALL did not borrow")
	}
	plain := permDocType(only("Reader", RightRead, false))
	if ReadableDocTypeFor(plain, userCaller("Nobody")) != plain {
		t.Error("a DocType without levels was copied")
	}
	if len(ReadableDocTypeFor(d, userCaller("Reader")).Fields) != 2 {
		t.Error("reader view")
	}
	if len(ReadableDocTypeFor(d, SystemCaller("cli")).Fields) != 3 {
		t.Error("system view")
	}
}

const workflowOrderTOML = `
name = "order"
label = "Order"
submittable = true
[workflow]
[[workflow.states]]
name = "Draft"
docstatus = 0
[[workflow.states]]
name = "Pending"
docstatus = 0
[[workflow.states]]
name = "Approved"
docstatus = 1
[[workflow.states]]
name = "Cancelled"
docstatus = 2
[[workflow.transitions]]
action = "Send"
from = "Draft"
to = "Pending"
roles = ["Clerk"]
[[workflow.transitions]]
action = "Approve"
from = "Pending"
to = "Approved"
roles = ["Manager"]
[[workflow.transitions]]
action = "Cancel"
from = "Approved"
to = "Cancelled"
roles = ["Manager"]
`

func withWorkflowDoc(t *testing.T, permissions ...PermissionRule) *DocType {
	t.Helper()
	d := mustDocType(t, workflowOrderTOML)
	d.Permissions = permissions
	return d
}

func take(t *testing.T, d *DocType, caller Caller, action string) Access {
	t.Helper()
	for i := range d.Workflow.Transitions {
		if d.Workflow.Transitions[i].Action == action {
			return TransitionAccess(d, caller, &d.Workflow.Transitions[i])
		}
	}
	t.Fatalf("no transition %q", action)
	return 0
}

// Rust: permission.rs::a_transition_needs_a_role_and_the_right_of_its_move
func TestATransitionNeedsARoleAndTheRightOfItsMove(t *testing.T) {
	d := withWorkflowDoc(t,
		only("Clerk", RightWrite, false),
		only("Manager", RightSubmit, false),
		only("Auditor", RightCancel, false),
		only("Owner", RightSubmit, true),
	)
	// Draft to draft needs write: Clerk has the role and the right.
	check := func(want Access, caller Caller, action string) {
		t.Helper()
		if got := take(t, d, caller, action); got != want {
			t.Errorf("%s by %v: got %v, want %v", action, caller.Roles, got, want)
		}
	}
	check(AccessAll, userCaller("Clerk"), "Send")
	// Approve moves 0 to 1, which needs the submit right: Manager has the role and the right.
	check(AccessAll, userCaller("Manager"), "Approve")
	// The role without the right: Manager has no cancel right.
	check(AccessDenied, userCaller("Manager"), "Cancel")
	// The right without the role: Auditor may cancel but is not a role of Cancel.
	check(AccessDenied, userCaller("Auditor"), "Cancel")
	check(AccessAll, userCaller("Manager", "Auditor"), "Cancel")
	// A user with neither.
	check(AccessDenied, userCaller("Other"), "Send")
	check(AccessDenied, userCaller(), "Send")
	// A similar role name is not the role.
	check(AccessDenied, userCaller("clerk"), "Send")
}

// Rust: permission.rs::if_owner_limits_the_right_of_a_transition
func TestIfOwnerLimitsTheRightOfATransition(t *testing.T) {
	d := withWorkflowDoc(t,
		only("Clerk", RightWrite, true),
		only("Manager", RightSubmit, true),
		only("Manager", RightCancel, false),
	)
	if take(t, d, userCaller("Clerk"), "Send") != AccessOwnerOnly ||
		take(t, d, userCaller("Manager"), "Approve") != AccessOwnerOnly ||
		take(t, d, userCaller("Manager"), "Cancel") != AccessAll {
		t.Error("if_owner")
	}
}

// Rust: permission.rs::administrator_and_system_pass_the_role_check
func TestAdministratorAndSystemPassTheRoleCheck(t *testing.T) {
	// No rules at all, and the roles of the transitions are not theirs.
	d := withWorkflowDoc(t)
	for _, action := range []string{"Send", "Approve", "Cancel"} {
		if take(t, d, userCaller(Administrator), action) != AccessAll || take(t, d, SystemCaller("cli"), action) != AccessAll ||
			take(t, d, userCaller("Clerk", "Manager"), action) != AccessDenied {
			t.Errorf("%s", action)
		}
	}
}

// Rust: permission.rs::no_workflow_or_a_foreign_transition_is_denied
func TestNoWorkflowOrAForeignTransitionIsDenied(t *testing.T) {
	plain := permDocType(only("Clerk", RightWrite, false))
	foreign := Transition{Action: "Send", From: "Draft", To: "Pending", Roles: []string{"Clerk"}}
	if TransitionAccess(plain, SystemCaller("cli"), &foreign) != AccessDenied {
		t.Error("no workflow")
	}
	d := withWorkflowDoc(t)
	unknownState := foreign
	unknownState.To = "Nowhere"
	if TransitionAccess(d, SystemCaller("cli"), &unknownState) != AccessDenied {
		t.Error("unknown state")
	}
}

// ---- user permissions (M10.3, ADR 0025) ----

func permLinkField(name, target string, permLevel uint8) DocField {
	f := linkField(name, target)
	f.PermLevel = permLevel
	return f
}

func withLinks(name string, links ...[2]string) *DocType {
	d := permDocType(only("Clerk", RightRead, false))
	d.Name = name
	d.Fields = nil
	for _, l := range links {
		d.Fields = append(d.Fields, permLinkField(l[0], l[1], 0))
	}
	return d
}

func restricted(roles []string, pairs ...[2]string) Caller {
	return userCaller(roles...).WithRestrictions(NewUserRestrictions(pairs))
}

// Rust: permission.rs::restrictions_are_sorted_deduplicated_and_empty_means_unrestricted
func TestRestrictionsAreSortedDeduplicatedAndEmptyMeansUnrestricted(t *testing.T) {
	r := NewUserRestrictions([][2]string{{"group", "b"}, {"group", "a"}, {"group", "b"}})
	sameValue(t, r.Allowed("group"), []string{"a", "b"}, "allowed")
	if r.Allowed("other") != nil || r.IsEmpty() {
		t.Error("other / is_empty")
	}
	none := UserRestrictions{}
	if !none.IsEmpty() || none.Allowed("group") != nil {
		t.Error("default")
	}
	// An empty list is the same as no entry.
	empty := UserRestrictions{m: map[string][]string{"group": {}}}
	if !empty.IsEmpty() || empty.Allowed("group") != nil {
		t.Error("empty list")
	}
}

// Rust: permission.rs::a_user_without_restrictions_has_an_empty_scope
func TestAUserWithoutRestrictionsHasAnEmptyScope(t *testing.T) {
	d := withLinks("customer", [2]string{"group", "group"})
	if !RowScopeOf(d, userCaller("Clerk")).IsEmpty() {
		t.Error("no restrictions")
	}
	// A restriction on something the DocType does not touch changes nothing.
	c := restricted([]string{"Clerk"}, [2]string{"territory", "North"})
	if !RowScopeOf(d, c).IsEmpty() {
		t.Error("unrelated restriction")
	}
}

// Rust: permission.rs::the_own_name_is_restricted_when_the_doctype_is
func TestTheOwnNameIsRestrictedWhenTheDoctypeIs(t *testing.T) {
	d := withLinks("group")
	c := restricted([]string{"Clerk"}, [2]string{"group", "Retail"}, [2]string{"group", "Online"})
	scope := RowScopeOf(d, c)
	sameValue(t, scope.Names, []string{"Online", "Retail"}, "names")
	if len(scope.Links) != 0 || scope.IsEmpty() {
		t.Error("links / is_empty")
	}
	if MayMakeDocuments(d, c) {
		t.Error("a user restricted on the DocType may make documents")
	}
	if !MayMakeDocuments(d, userCaller("Clerk")) {
		t.Error("an unrestricted user may not make documents")
	}
}

// Rust: permission.rs::a_link_to_a_restricted_target_is_restricted_and_a_link_to_another_is_not
func TestALinkToARestrictedTargetIsRestrictedAndALinkToAnotherIsNot(t *testing.T) {
	d := withLinks("customer", [2]string{"group", "group"}, [2]string{"region", "region"})
	c := restricted([]string{"Clerk"}, [2]string{"group", "Retail"})
	scope := RowScopeOf(d, c)
	if scope.Names != nil {
		t.Error("names")
	}
	sameValue(t, scope.Links, []LinkScope{{Field: "group", Target: "group", Allowed: []string{"Retail"}}}, "links")
}

// Rust: permission.rs::two_links_to_the_same_target_both_restrict
func TestTwoLinksToTheSameTargetBothRestrict(t *testing.T) {
	d := withLinks("customer", [2]string{"group", "group"}, [2]string{"billing_group", "group"})
	c := restricted([]string{"Clerk"}, [2]string{"group", "Retail"})
	var fields []string
	for _, l := range RowScopeOf(d, c).Links {
		fields = append(fields, l.Field)
	}
	sameValue(t, fields, []string{"group", "billing_group"}, "fields")
}

// Rust: permission.rs::links_to_two_restricted_targets_all_restrict
func TestLinksToTwoRestrictedTargetsAllRestrict(t *testing.T) {
	d := withLinks("order", [2]string{"customer", "customer"}, [2]string{"group", "group"})
	c := restricted([]string{"Clerk"}, [2]string{"group", "Retail"}, [2]string{"customer", "C-1"})
	var targets []string
	for _, l := range RowScopeOf(d, c).Links {
		targets = append(targets, l.Target)
	}
	sameValue(t, targets, []string{"customer", "group"}, "targets")
	// The own name and the links together.
	d = withLinks("customer", [2]string{"group", "group"})
	c = restricted([]string{"Clerk"}, [2]string{"group", "Retail"}, [2]string{"customer", "C-1"})
	scope := RowScopeOf(d, c)
	sameValue(t, scope.Names, []string{"C-1"}, "names")
	if len(scope.Links) != 1 {
		t.Error("links")
	}
}

// Rust: permission.rs::a_hidden_link_field_still_restricts
func TestAHiddenLinkFieldStillRestricts(t *testing.T) {
	d := withLinks("customer")
	d.Fields = append(d.Fields, permLinkField("group", "group", 3))
	c := restricted([]string{"Clerk"}, [2]string{"group", "Retail"})
	if len(RowScopeOf(d, c).Links) != 1 {
		t.Error("hidden link field does not restrict")
	}
}

// Rust: permission.rs::administrator_and_system_are_never_restricted
func TestAdministratorAndSystemAreNeverRestricted(t *testing.T) {
	d := withLinks("customer", [2]string{"group", "group"})
	pairs := [][2]string{{"group", "Retail"}, {"customer", "C-1"}}
	admin := restricted([]string{Administrator}, pairs...)
	if !RowScopeOf(d, admin).IsEmpty() {
		t.Error("administrator restricted")
	}
	system := SystemCaller("cli").WithRestrictions(NewUserRestrictions(pairs))
	sameValue(t, system, SystemCaller("cli"), "system caller")
	if !RowScopeOf(d, system).IsEmpty() {
		t.Error("system restricted")
	}
}

// Rust: permission.rs::a_child_doctype_has_an_empty_scope
func TestAChildDoctypeHasAnEmptyScope(t *testing.T) {
	line := withLinks("line", [2]string{"group", "group"})
	line.IsChild = true
	c := restricted([]string{"Clerk"}, [2]string{"group", "Retail"}, [2]string{"line", "x"})
	if !RowScopeOf(line, c).IsEmpty() {
		t.Error("child scope")
	}
	// The parent does not look at the Link fields of its child rows.
	parent := withLinks("order")
	parent.Fields = append(parent.Fields, tableField("lines", "line"))
	if !RowScopeOf(parent, c).IsEmpty() {
		t.Error("parent scope")
	}
}
