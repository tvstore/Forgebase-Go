package core

import (
	"strings"
	"testing"
)

func methodDef(name string, access MethodAccess) MethodDef {
	return MethodDef{App: "sales", Name: name, Access: access}
}

// Rust: method.rs::the_full_name_is_app_dot_name
func TestTheFullNameIsAppDotName(t *testing.T) {
	if got := methodDef("ping", MethodAnyUser()).FullName(); got != "sales.ping" {
		t.Errorf("got %q", got)
	}
}

// Rust: method.rs::a_valid_definition_has_no_problems
func TestAValidDefinitionHasNoProblems(t *testing.T) {
	if p := methodDef("ping", MethodAnyUser()).Problems(); len(p) != 0 {
		t.Errorf("problems: %v", p)
	}
	if p := methodDef("order_summary", MethodRoles("Sales User")).Problems(); len(p) != 0 {
		t.Errorf("problems: %v", p)
	}
}

// Rust: method.rs::a_bad_name_an_empty_list_and_a_bad_role_are_each_a_problem
func TestABadNameAnEmptyListAndABadRoleAreEachAProblem(t *testing.T) {
	problems := methodDef("Bad Name", MethodRoles()).Problems()
	if len(problems) != 2 || !strings.HasPrefix(problems[0], "name \"Bad Name\"") || !strings.Contains(problems[1], "empty role list") {
		t.Errorf("problems: %v", problems)
	}
	problems = methodDef("ok", MethodRoles(" ")).Problems()
	if len(problems) != 1 || !strings.HasPrefix(problems[0], "invalid role") {
		t.Errorf("problems: %v", problems)
	}
	// The whole text of a name problem is the Rust text.
	if got, want := methodDef("Bad", MethodAnyUser()).Problems()[0],
		"name \"Bad\" must match ^[a-z][a-z0-9_]*$ (lowercase ASCII letters, digits and underscores, starting with a letter)"; got != want {
		t.Errorf("got %q", got)
	}
}
