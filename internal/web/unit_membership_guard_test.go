package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Pages that show one unit's own families' information must gate on unit
// MEMBERSHIP, not merely on being signed in.
//
// The difference is invisible in single-unit thinking and load-bearing
// here: one login deliberately works on both subdomains, so "signed in"
// is true on the other unit's site too. That is how the Troop roster —
// names, and released parent emails and phone numbers — became readable
// by any Pack family, in both directions, including the PDF export.
//
// This test fails if one of these handlers goes back to deciding access
// from auth.UserFromContext's loggedIn flag alone, without reaching
// requireUnitMember or viewerIsUnitMember. It is a structural check, not
// a behavioural one: it cannot prove the gate is correct, only that a
// gate is still there — which is the part a later refactor is likely to
// drop by accident.
func TestUnitScopedHandlersCheckMembership(t *testing.T) {
	mustGate := map[string]string{
		"Roster":             "web.go",
		"RosterExportPDF":    "web.go",
		"CalendarRSVP":       "web.go",
		"FamilyDirectory":    "my_family.go",
		"DirectoryExportPDF": "my_family.go",
		"Advancement":        "advancement.go",
		"GroupView":          "groups.go",
		"CalendarSubscribe":  "calendar_subscribe.go",
		"CalendarFeed":       "calendar_feed.go",
	}

	fset := token.NewFileSet()
	pkg, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parsing package: %v", err)
	}

	found := map[string]bool{}
	for _, p := range pkg {
		for _, file := range p.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || fn.Body == nil {
					return true
				}
				name := fn.Name.Name
				if _, want := mustGate[name]; !want {
					return true
				}
				found[name] = true

				var gated bool
				ast.Inspect(fn.Body, func(inner ast.Node) bool {
					sel, ok := inner.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					switch sel.Sel.Name {
					case "requireUnitMember", "viewerIsUnitMember", "isUnitMember":
						gated = true
					}
					return true
				})
				if !gated {
					t.Errorf("%s shows one unit's own data but never checks unit membership — "+
						"a family from the other unit can read it. Use requireUnitMember "+
						"(to block) or viewerIsUnitMember (to show the public view instead).", name)
				}
				return true
			})
		}
	}

	for name := range mustGate {
		if !found[name] {
			t.Errorf("handler %s no longer exists under that name — this guard is now blind to it; "+
				"update the list rather than deleting the entry", name)
		}
	}
}
