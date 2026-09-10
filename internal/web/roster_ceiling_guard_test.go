package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Every action on the roster page that touches a login or silences a
// member has to go through requireCeiling. Scope already gates each of
// them, and scope is the check that looked sufficient for a long time —
// a unit-wide leader's scope covers everyone, the Admin included, so
// scope alone let an Assistant Scoutmaster reset the Admin's password.
//
// The ceiling is one call at the top of each handler, and one call is
// exactly what gets dropped in a refactor: the handler still compiles,
// still checks scope, still passes every test that exercises it as an
// Admin. So this reads the source and fails if any of these handlers no
// longer makes the call. Role assignment is not listed because
// roster.IsAllowedRole cannot be called without the editor's
// capabilities — the compiler is the guard there.
func TestAccountAffectingRosterHandlersApplyTheCeiling(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "admin_roster.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing admin_roster.go: %v", err)
	}

	want := map[string]string{
		"AdminRosterResetPassword":            "FamilyCapabilitiesAcrossUnits",
		"AdminRosterResetMemberLoginPassword": "MemberCapabilitiesAcrossUnits",
		"AdminRosterCreateMemberLogin":        "MemberCapabilitiesAcrossUnits",
		"AdminRosterMemberDeactivate":         "MemberCapabilitiesAcrossUnits",
		"AdminRosterRemoveRole":               "RoleCapabilities",
	}

	found := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		gather, needs := want[fn.Name.Name]
		if !needs {
			return true
		}
		found[fn.Name.Name] = true

		var callsCeiling, gathersRight bool
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fun.Name == "requireCeiling" {
					callsCeiling = true
				}
			case *ast.SelectorExpr:
				if pkg, ok := fun.X.(*ast.Ident); ok && pkg.Name == "roster" && fun.Sel.Name == gather {
					gathersRight = true
				}
			}
			return true
		})
		if !callsCeiling {
			t.Errorf("%s no longer calls requireCeiling — a unit-wide leader can use it against the Admin", fn.Name.Name)
		}
		if !gathersRight {
			t.Errorf("%s should measure the target with roster.%s", fn.Name.Name, gather)
		}
		return true
	})
	for name := range want {
		if !found[name] {
			t.Errorf("%s is gone; if it moved, move this guard with it", name)
		}
	}
}
