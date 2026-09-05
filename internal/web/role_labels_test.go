package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// No page may render a role slug.
//
// role_assignments.role is a stable key — "den_leader",
// "assistant_scoutmaster" — and every page that listed somebody's roles
// was printing it, so parents read internal identifiers. The fix was to
// carry RoleLabels alongside, and this is what stops the next page from
// quietly going back to the slug: the mistake is invisible in review,
// because `{{range .Roles}}` looks exactly as correct as
// `{{range .RoleLabels}}`.
//
// Scoped to the roster-ish templates that carry a RosterEntry or a role
// assignment. Templates elsewhere use "Roles" for unrelated things — the
// role-picker dropdown on the roster form iterates AllowedRoles and
// renders .Value/.Label, which is right.
func TestNoTemplateRendersARoleSlug(t *testing.T) {
	// Renders the loop variable itself ({{.}} or {{$r}}) out of a range
	// over a field literally named Roles — which on a RosterEntry is the
	// slug list.
	rangeOverRoles := regexp.MustCompile(`\{\{range (\$\w+, )?(\$\w+ :?= )?\.Roles\}\}`)

	pages, err := filepath.Glob("templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 40 {
		t.Fatalf("only found %d templates — this test is not looking where it thinks it is", len(pages))
	}

	for _, page := range pages {
		b, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		body := string(b)
		name := filepath.Base(page)

		// admin-custom-roles.html ranges over its own .Roles — a list of
		// custom role definitions, each rendered via .Label. That is the
		// labelled form already.
		if name == "admin-custom-roles.html" {
			continue
		}

		for _, m := range rangeOverRoles.FindAllString(body, -1) {
			idx := strings.Index(body, m)
			// Look at what the loop body actually prints. A range over
			// .Roles that immediately renders the element is the slug.
			after := body[idx+len(m):]
			if end := strings.Index(after, "{{end}}"); end >= 0 {
				after = after[:end]
			}
			if strings.Contains(after, "{{.}}") || regexp.MustCompile(`\{\{\$\w+\}\}`).MatchString(after) {
				t.Errorf("%s renders a raw role slug — range over .RoleLabels instead:\n  %s%s{{end}}",
					name, m, strings.TrimSpace(after))
			}
		}

		// The member edit page shows one assignment at a time rather than
		// ranging over a list of strings; .Role there is the slug and
		// .Label is what to show.
		if strings.Contains(body, "{{.Role}}") {
			t.Errorf("%s renders {{.Role}}, which is a slug — use {{.Label}}", name)
		}
	}
}
