package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestEverySendNamesItsUnit: a message is sent as the unit it is for — the
// Troop's From address on the Troop's mail, the Pack's on the Pack's — and the
// unit reaches the mailer on the context (mailer.WithUnit). Leave it off and
// nothing fails: the mail goes out, from the site-wide address, to families who
// were told it would come from their own unit. So this fails any function that
// sends mail, or asks whether mail can be sent, without naming the unit.
//
// Handlers name it with mailCtx (the unit the request is on); the packages
// that send from a record (a newsletter, a campaign, a reminder) name that
// record's own unit with mailer.WithUnit.
func TestEverySendNamesItsUnit(t *testing.T) {
	// The methods whose behaviour depends on which unit it is for.
	methods := map[string]bool{"Send": true, "SendHTML": true, "SendHTMLPaced": true, "Enabled": true, "From": true}

	type pkg struct {
		dir string
		// receiver is how this package refers to the *mailer.Mailer.
		receiver func(fn *ast.FuncDecl) map[string]bool
	}
	handlers := func(*ast.FuncDecl) map[string]bool { return map[string]bool{"h.Mailer": true} }
	// A function that takes a *mailer.Mailer names it by its parameter.
	byParameter := func(fn *ast.FuncDecl) map[string]bool {
		names := map[string]bool{}
		for _, field := range fn.Type.Params.List {
			if types.ExprString(field.Type) == "*mailer.Mailer" {
				for _, n := range field.Names {
					names[n.Name] = true
				}
			}
		}
		return names
	}
	pkgs := []pkg{
		{".", handlers},
		{"../newsletter", byParameter},
		{"../prospect", byParameter},
		{"../reminders", byParameter},
	}

	var offenders []string
	var examined int
	for _, p := range pkgs {
		files, err := filepath.Glob(filepath.Join(p.dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			f, err := parser.ParseFile(token.NewFileSet(), file, src, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", file, err)
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				receivers := p.receiver(fn)
				if len(receivers) == 0 {
					continue
				}
				var mails, names bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch fun := call.Fun.(type) {
					case *ast.SelectorExpr:
						if methods[fun.Sel.Name] && receivers[types.ExprString(fun.X)] {
							mails = true
						}
						if fun.Sel.Name == "WithUnit" && types.ExprString(fun.X) == "mailer" {
							names = true
						}
					case *ast.Ident:
						if fun.Name == "mailCtx" {
							names = true
						}
					}
					return true
				})
				if mails {
					examined++
					if !names {
						offenders = append(offenders, file+": "+fn.Name.Name)
					}
				}
			}
		}
	}

	// The guard is only worth anything if it is actually looking at the senders.
	if examined < 8 {
		t.Fatalf("only %d functions that send mail were examined — the guard has stopped finding them, "+
			"so it would pass whatever the code did; fix it before trusting it", examined)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("these functions send mail, or ask whether it can be sent, without naming the unit it is for:\n  %s\n"+
			"Wrap the context: mailCtx(r.Context()) in a handler, or mailer.WithUnit(ctx, unitID) where the unit "+
			"comes from a record. Without it the mail goes out from the site-wide From address, not the unit's own.",
			strings.Join(offenders, "\n  "))
	}
}
