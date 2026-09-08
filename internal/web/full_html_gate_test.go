package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/newsletter"
)

// Full-HTML mode trades an allowlist for a blocklist. That is a real
// reduction in guarantee, and the two things holding it in place are
// that only an Admin can ask for it and that the composer only offers it
// to one. Both are easy to undo without noticing.

// TestFullHTMLIsGatedOnSuperAdmin reads the gate, because the
// alternative is standing up a signed-in request with a role, and a test
// that heavy tends not to get written — which is how a gate goes missing.
func TestFullHTMLIsGatedOnSuperAdmin(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "newsletter.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing newsletter.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == "fullHTMLRequest" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("fullHTMLRequest is gone; if the gate moved, move this guard with it")
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
		t.Errorf("fullHTMLRequest never checks units.IsSuperAdmin (units calls: %v)", checks)
	}
	if strings.Contains(joined, "CanEditUnitContent") {
		t.Error("fullHTMLRequest checks CanEditUnitContent — that is the gate it exists to be stricter than")
	}

	// And both save handlers must go through it, or the box works for
	// anyone on whichever one was missed.
	for _, handler := range []string{"AdminNewsletterCreate", "AdminNewsletterUpdate"} {
		var h *ast.FuncDecl
		ast.Inspect(file, func(n ast.Node) bool {
			if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == handler {
				h = d
				return false
			}
			return true
		})
		if h == nil {
			t.Fatalf("%s is gone", handler)
		}
		var calls []string
		ast.Inspect(h, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				calls = append(calls, id.Name)
			}
			return true
		})
		if !contains(calls, "fullHTMLRequest") {
			t.Errorf("%s does not go through fullHTMLRequest — the box would work for any content editor there", handler)
		}
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func newsletterForm(superAdmin, fullHTML bool) any {
	base := testBase("Newsletter")
	base.IsSuperAdmin = superAdmin
	return struct {
		baseData
		IsEdit           bool
		Newsletter       newsletter.Newsletter
		StarterTemplates any
	}{base, true, newsletter.Newsletter{
		ID: "n1", Subject: "Remembrance", Body: "<p>hi</p>", Status: "draft", FullHTML: fullHTML,
	}, nil}
}

func TestOnlyAnAdminIsOfferedFullHTML(t *testing.T) {
	admin := renderPage(t, "admin-newsletter-form.html", newsletterForm(true, false))
	if !strings.Contains(admin, `name="full_html"`) {
		t.Error("an Admin is not offered the full-HTML box")
	}
	// The warning is part of the feature: an Admin ticking this needs to
	// know what they are taking on.
	if !strings.Contains(admin, "only tick it for a file you trust") && !strings.Contains(admin, "Only tick it for a file you trust") {
		t.Error("the full-HTML box has lost the caution that goes with it")
	}

	editor := renderPage(t, "admin-newsletter-form.html", newsletterForm(false, false))
	if strings.Contains(editor, `name="full_html"`) {
		t.Error("a content editor is offered a box the handler will refuse")
	}
	if !strings.Contains(editor, "Save Draft") {
		t.Error("hiding the box broke the composer for everyone else")
	}
}

// TestTheBoxRemembersItsSetting — reopening a full-HTML draft with the
// box cleared would silently re-strip the template on the next save.
func TestTheBoxRemembersItsSetting(t *testing.T) {
	on := renderPage(t, "admin-newsletter-form.html", newsletterForm(true, true))
	if !strings.Contains(on, "checked") {
		t.Error("a full-HTML draft reopens with the box unticked, so saving it would strip the template")
	}
	off := renderPage(t, "admin-newsletter-form.html", newsletterForm(true, false))
	box := off[strings.Index(off, `name="full_html"`):]
	if strings.Contains(box[:min(200, len(box))], "checked") {
		t.Error("an ordinary draft reopens with full HTML already ticked")
	}
}
