package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestEveryPermissionSlipRouteChecksTheFeatureToggle asserts that every
// route under /calendar/{id}/permission-slip consults
// settings.PermissionSlipsEnabled before doing anything.
//
// Structural rather than behavioral because the requirement is absolute
// and easy to half-implement: with the feature off, a slip must be
// unreachable for *everyone* — a family, a logged-out visitor, and a
// leader alike — even on an event explicitly flagged as needing one. The
// older PermissionSlipEnforcement narrowing deliberately keeps a leader
// escape hatch, so the two rules sit inches apart in the same file and
// the wrong one is easy to copy. Adding a fourth slip route without the
// gate fails here.
func TestEveryPermissionSlipRouteChecksTheFeatureToggle(t *testing.T) {
	fset := token.NewFileSet()

	routesSrc, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatalf("reading web.go: %v", err)
	}
	routes, err := parser.ParseFile(fset, "web.go", routesSrc, 0)
	if err != nil {
		t.Fatalf("parsing web.go: %v", err)
	}

	var handlerNames []string
	ast.Inspect(routes, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		pattern, ok := call.Args[0].(*ast.BasicLit)
		if !ok || !strings.Contains(pattern.Value, "permission-slip") {
			return true
		}
		if h, ok := call.Args[1].(*ast.SelectorExpr); ok {
			handlerNames = append(handlerNames, h.Sel.Name)
		}
		return true
	})

	if len(handlerNames) < 3 {
		t.Fatalf("found only %d permission-slip routes — this test is not looking where it thinks it is", len(handlerNames))
	}

	slipSrc, err := os.ReadFile("permission_slip.go")
	if err != nil {
		t.Fatalf("reading permission_slip.go: %v", err)
	}
	slipFile, err := parser.ParseFile(fset, "permission_slip.go", slipSrc, 0)
	if err != nil {
		t.Fatalf("parsing permission_slip.go: %v", err)
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range slipFile.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			funcs[fn.Name.Name] = fn
		}
	}

	for _, name := range handlerNames {
		fn, ok := funcs[name]
		if !ok {
			t.Errorf("permission-slip route names handler %s, which isn't in permission_slip.go", name)
			continue
		}
		var gated bool
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "permissionSlipsEnabled" {
				gated = true
			}
			return !gated
		})
		if !gated {
			t.Errorf("handler %s never calls permissionSlipsEnabled — with the feature switched off "+
				"this route would still serve a permission slip", name)
		}
	}
}
