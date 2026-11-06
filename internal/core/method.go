package core

import "fmt"

// App method endpoints (M9.1, ADR 0019): the definition of a method, and the checks that need no
// database.
//
// A method is a function an app compiled into the binary, called over HTTP as
// POST /api/v1/method/<app>.<method>. This file is pure data and rules: it holds the name and who
// may call it (never the handler, which lives in the root package), so that the per-site registry
// decides which methods exist on a site, as it does for reports.

// MethodAccess says who may call a method. There is no guest access: every call is authenticated.
// Build one with MethodRoles or MethodAnyUser.
type MethodAccess struct {
	// AnyUser: any authenticated user, whatever their roles.
	AnyUser bool
	// Roles: users that have at least one of these roles (and the Administrator). Never empty in
	// a valid method. Ignored when AnyUser is true.
	Roles []string
}

// MethodRoles is the access for users that have at least one of roles.
func MethodRoles(roles ...string) MethodAccess { return MethodAccess{Roles: roles} }

// MethodAnyUser is the access for any authenticated user.
func MethodAnyUser() MethodAccess { return MethodAccess{AnyUser: true} }

// MethodDef is a method: its app, its name and who may call it. Plain data, like ReportDef; check
// it with Problems.
type MethodDef struct {
	// App is the app that defines it.
	App string
	// Name is unique within the app. Same rule as a DocType name.
	Name string
	// Access says who may call it.
	Access MethodAccess
}

// FullName is <app>.<name>: how the method is known in the API.
func (m MethodDef) FullName() string { return m.App + "." + m.Name }

// Problems is what is wrong with the definition itself, one message per problem.
func (m MethodDef) Problems() []string {
	var problems []string
	if err := ValidateDocTypeName(m.Name); err != nil {
		problems = append(problems, fmt.Sprintf("name %s %s", debugQuote(m.Name), err))
	}
	if !m.Access.AnyUser {
		if len(m.Access.Roles) == 0 {
			problems = append(problems,
				"an empty role list: name the roles that may call it with .roles(&[..]), or use .any_user()")
		}
		for _, role := range m.Access.Roles {
			if reason := CheckRoleName(role); reason != "" {
				problems = append(problems, fmt.Sprintf("invalid role %s: %s", debugQuote(role), reason))
			}
		}
	}
	return problems
}
