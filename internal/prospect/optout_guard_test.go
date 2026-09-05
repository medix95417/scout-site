package prospect

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// An opt-out that depends on every future caller remembering to check a
// flag is not an opt-out. The design puts the check in one place —
// RecipientsForStatuses' WHERE clause — and these two tests are what keep
// it there, because neither property can be exercised without a live
// database and both fail silently if broken: the send works, the email
// goes out, and the only person who notices is the family who asked not
// to receive it.

// The one query that decides who a campaign reaches must exclude anyone
// who has opted out.
func TestRecipientQueryExcludesOptOuts(t *testing.T) {
	body := functionSource(t, "campaign.go", "RecipientsForStatuses")
	if !strings.Contains(body, "NOT p.email_opt_out") {
		t.Fatal("RecipientsForStatuses no longer filters out opted-out prospects — " +
			"every campaign would email families who asked not to be emailed")
	}
	// Excluding opted-out ROWS is not enough on its own: a family who
	// enquired twice has two rows, the opt-out sits on one, and
	// de-duplication picks the other. The exclusion has to be by address.
	if !strings.Contains(body, "NOT EXISTS") || !strings.Contains(body, "q.email_opt_out") {
		t.Error("RecipientsForStatuses no longer excludes by address — a family who enquired twice " +
			"would keep receiving mail after unsubscribing")
	}
	// De-duplication is the other half: one family enquiring twice must
	// not be written to twice.
	if !strings.Contains(body, "DISTINCT ON (lower(p.parent_email))") {
		t.Error("RecipientsForStatuses no longer de-duplicates addresses, so a family who enquired twice gets two copies")
	}
}

// Nothing else may build a recipient list. A second query selecting
// prospect email addresses is how the filter above gets bypassed without
// anybody editing it.
func TestOnlyOneFunctionSelectsProspectAddressesToEmail(t *testing.T) {
	fset := token.NewFileSet()
	for _, file := range []string{"campaign.go", "prospect.go", "optout.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		f, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			name := fn.Name.Name
			// The three functions allowed to read an address: the one
			// that picks recipients, the one that reads back who was
			// already written to, and the single-row loads.
			switch name {
			case "RecipientsForStatuses", "CampaignRecipients", "Get", "GetAnyUnit", "Create", "scan":
				return true
			}

			body := src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset]
			text := string(body)
			if strings.Contains(text, "parent_email") && strings.Contains(strings.ToUpper(text), "SELECT") {
				t.Errorf("%s selects parent_email directly — recipient lists must go through "+
					"RecipientsForStatuses so the opt-out filter cannot be bypassed", name)
			}
			return true
		})
	}
}

// functionSource returns one function's body text, for the substring
// checks above. Parsing rather than grepping the whole file means a
// comment elsewhere mentioning the clause can't make the test pass.
func functionSource(t *testing.T, file, fnName string) string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName || fn.Body == nil {
			continue
		}
		return string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
	}
	t.Fatalf("no function %s in %s", fnName, file)
	return ""
}
