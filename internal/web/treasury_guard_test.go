package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// treasuryGuards are the permission checks a treasury route may rely on
// to establish who is asking:
//
//   - requireTreasurer — the Treasurer/super_admin gate most routes use.
//   - requireExpenseApprover — the Cubmaster/Scoutmaster gate on the
//     expense-authorization routes, deliberately a different role so that
//     one person can't both spend and approve.
//   - resolveAccountAccess / isAccountOwner — the narrower owner-or-manager
//     checks the routes an account's own family can reach use instead
//     (viewing a Scout's account, exporting its statement, moving money
//     into a trip fund).
//
// Recognizing the mechanisms rather than keeping a list of exempt handler
// names is deliberate: the first draft of this test carried such a list
// and was already wrong when written — it named two owner-reachable
// routes when there are three, and missed the expense-approval gate
// entirely. A list of names goes stale silently; a list of checks fails
// loudly the moment a route is added that uses none of them.
var treasuryGuards = map[string]bool{
	"requireTreasurer":       true,
	"requireExpenseApprover": true,
	"resolveAccountAccess":   true,
	"isAccountOwner":         true,
}

// TestEveryTreasuryRouteChecksPermission walks the route table and the
// handlers it names, and fails if a route under /treasury or
// /expense-approvals lands in a handler that establishes no permission at
// all.
//
// This is a structural test for the same reason the stored-bytes one is:
// the treasury has grown by a handful of routes at a time — reports, then
// expense authorization, then reconciliation — and a permission check is
// exactly the kind of thing that gets left off one handler in a batch of
// six and looks fine in review, because every route around it has one.
// The blast radius of getting it wrong here is a stranger reading or
// moving a unit's money.
func TestEveryTreasuryRouteChecksPermission(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing package files: %v", err)
	}

	fset := token.NewFileSet()
	handlers := map[string]*ast.FuncDecl{}
	var routes []struct{ path, handler string }

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}

		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				handlers[fn.Name.Name] = fn
			}
		}

		// Collect mux.HandleFunc("<METHOD> <path>", h.Handler) pairs.
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HandleFunc" {
				return true
			}
			pattern, ok := call.Args[0].(*ast.BasicLit)
			if !ok || pattern.Kind != token.STRING {
				return true
			}
			handlerSel, ok := call.Args[1].(*ast.SelectorExpr)
			if !ok {
				return true
			}
			routes = append(routes, struct{ path, handler string }{
				strings.Trim(pattern.Value, `"`), handlerSel.Sel.Name,
			})
			return true
		})
	}

	if len(routes) == 0 {
		t.Fatal("found no routes at all — this test is not looking where it thinks it is")
	}

	checked := 0
	for _, rt := range routes {
		_, path, found := strings.Cut(rt.path, " ")
		if !found {
			path = rt.path
		}
		if !strings.HasPrefix(path, "/treasury") && !strings.HasPrefix(path, "/expense-approvals") {
			continue
		}
		fn, ok := handlers[rt.handler]
		if !ok {
			t.Errorf("route %q names handler %s, which this package doesn't define", path, rt.handler)
			continue
		}
		checked++

		var guarded bool
		// A guard is called either as a method on the handler
		// (h.requireTreasurer) or as a plain package function
		// (isAccountOwner), so both call shapes have to be recognized —
		// looking only for the method form is what made an earlier draft
		// of this test report a false failure on a route that was in fact
		// correctly guarded.
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return !guarded
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				guarded = guarded || treasuryGuards[fun.Sel.Name]
			case *ast.Ident:
				guarded = guarded || treasuryGuards[fun.Name]
			}
			return !guarded
		})
		if !guarded {
			t.Errorf("route %q is handled by %s, which calls none of the checks in "+
				"treasuryGuards — a treasury route has to establish who is asking before it "+
				"shows or moves anyone's money", path, rt.handler)
		}
	}

	if checked < 20 {
		t.Errorf("only %d treasury routes were checked — the route table or this test has drifted, "+
			"and a permission guard that silently checks nothing is worse than none", checked)
	}
}
