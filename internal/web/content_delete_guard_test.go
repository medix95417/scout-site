package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// Two things about adminContentDelete are load-bearing and invisible in
// a diff.
//
// First, it must be gated on super_admin, not on the content-editor
// capability that guards every other action on the page. Deleting is the
// one content action that cannot be undone; swapping the gate back to
// requireContentEditor is a one-word edit that compiles, passes every
// other test, and quietly hands an irreversible button to every leader.
//
// Second, it has to look the item up with content.GetPost, which
// checks the page type, and not content.GetPostAnyType, which does not.
// The difference is whether a photo album's id posted to
// /admin/news/{id}/delete removes the album: both calls find the row,
// both hand back a post, and nothing else in the request looks wrong.
//
// Checked by reading the source because the alternative is standing up a
// signed-in request with a content-editor role, and a test that heavy
// tends not to get written — which is how the guard would go missing.
func TestContentDeleteChecksThePageType(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "content_posts.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing content_posts.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "adminContentDelete" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("adminContentDelete is gone; if delete moved, move this guard with it")
	}

	var calls []string
	var usesPageType bool
	ast.Inspect(fn, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "content" {
				calls = append(calls, sel.Sel.Name)
			}
			if sel.Sel.Name == "PageType" {
				usesPageType = true
			}
		}
		return true
	})

	// The gate. requireContentDeleter is the super_admin one;
	// requireContentEditor is the capability every other handler here
	// uses and is exactly what must NOT appear.
	var gates []string
	ast.Inspect(fn, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && strings.HasPrefix(sel.Sel.Name, "require") {
			gates = append(gates, sel.Sel.Name)
		}
		return true
	})
	gateNames := strings.Join(gates, " ")
	if strings.Contains(gateNames, "requireContentEditor") {
		t.Error("adminContentDelete is gated on requireContentEditor — deleting is super_admin only, and this hands an irreversible action to every content editor")
	}
	if !strings.Contains(gateNames, "requireContentDeleter") {
		t.Errorf("adminContentDelete does not go through requireContentDeleter (gates: %v)", gates)
	}

	joined := strings.Join(calls, " ")
	if strings.Contains(joined, "GetPostAnyType") {
		t.Error("adminContentDelete uses GetPostAnyType — a gallery id posted to the news delete route would delete the album")
	}
	if !strings.Contains(joined, "GetPost") {
		t.Errorf("adminContentDelete does not look the item up before deleting it (content calls: %v)", calls)
	}
	if !usesPageType {
		t.Error("adminContentDelete never mentions the kind's PageType, so nothing constrains which type it deletes")
	}
	if !strings.Contains(joined, "DeletePost") {
		t.Errorf("adminContentDelete does not call content.DeletePost (content calls: %v)", calls)
	}
}

// TestRequireContentDeleterChecksSuperAdmin — the gate itself has to ask
// units.IsSuperAdmin. A gate that goes through the motions and checks
// CanEditUnitContent would satisfy the test above and change nothing.
func TestRequireContentDeleterChecksSuperAdmin(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "content_posts.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing content_posts.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "requireContentDeleter" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("requireContentDeleter is gone; if the delete gate moved, move this guard with it")
	}

	var checks []string
	ast.Inspect(fn, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "units" {
				checks = append(checks, sel.Sel.Name)
			}
		}
		return true
	})

	joined := strings.Join(checks, " ")
	if !strings.Contains(joined, "IsSuperAdmin") {
		t.Errorf("requireContentDeleter never checks units.IsSuperAdmin (units calls: %v)", checks)
	}
	if strings.Contains(joined, "CanEditUnitContent") {
		t.Error("requireContentDeleter checks CanEditUnitContent — that is the gate it exists to be stricter than")
	}
}
