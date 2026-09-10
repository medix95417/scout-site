package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Turning two-factor off must ask for the password, the same as
// re-enrolling does. The check is one call that a handler is complete
// without, so it is read from the source — and the form has to carry the
// field, or the check refuses everyone and the feature reads as broken.
func TestTwoFactorDisableRequiresThePassword(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "twofactor.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing twofactor.go: %v", err)
	}
	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "TwoFactorDisable" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("TwoFactorDisable is gone; if it moved, move this guard with it")
	}
	verifies := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "auth" && sel.Sel.Name == "VerifyPassword" {
				verifies = true
			}
		}
		return true
	})
	if !verifies {
		t.Error("TwoFactorDisable no longer verifies the current password — a stolen session can turn two-factor off")
	}

	tmpl, err := os.ReadFile("templates/two-factor-settings.html")
	if err != nil {
		t.Fatal(err)
	}
	form := string(tmpl)
	start := strings.Index(form, `action="/settings/2fa/disable"`)
	if start < 0 {
		t.Fatal("the disable form is gone from two-factor-settings.html")
	}
	end := strings.Index(form[start:], "</form>")
	if end < 0 || !strings.Contains(form[start:start+end], `name="password"`) {
		t.Error("the disable form has no password field, so the handler's check would refuse every submission")
	}
}
