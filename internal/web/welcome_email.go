package web

import (
	"log"
	"net/http"
	"strings"

	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file is the optional "email login details" step at account
// creation time — see AdminRosterCreateFamily/AdminRosterCreateMemberLogin,
// which are the only two callers of sendWelcomeEmail. Deliberately never
// sent automatically: a leader explicitly opts in per account by checking
// a box on the creation form (defaulting to checked), since not every
// unit wants every new family's inbox getting an email the moment their
// record is entered — e.g. a bulk roster import.

// defaultWelcomeEmailSubject/Body are used whenever a unit hasn't
// customized its own via /admin/settings (settings.WelcomeEmailSubject/
// Body) — see settings.UnitTextSettings' "welcome_email" section.
const (
	defaultWelcomeEmailSubject = "Welcome to {{unit_name}}!"
	defaultWelcomeEmailBody    = `Hi {{name}},

Welcome to {{unit_name}}! An account has been created for you on our website.

Email: {{email}}
Temporary password: {{password}}

Log in here: {{login_url}}

You'll be asked to choose your own password the first time you log in.`
)

// familyCrossUnitNote is appended, verbatim and not admin-editable, to a
// family account's welcome email only — describing real behavior of a
// family-wide login (one account, shared across the Troop and Pack
// sites — see internal/units.Middleware), not customizable marketing
// copy. Never appended for an individual member's own login, since that
// only ever sees that one person's own stuff, not "every child."
const familyCrossUnitNote = "\n\nOnce logged in, you'll be able to see every child linked to your family's account — across both our Pack and Troop sites, using this same login."

// welcomeEmailReplacer builds the {{name}}/{{email}}/{{password}}/
// {{login_url}}/{{unit_name}} substitution for a welcome email template
// — plain string substitution, not html/template, since this is a
// leader-edited plain-text field (see settings.WelcomeEmailBody), not
// code to execute.
func welcomeEmailReplacer(name, email, password, loginURL, unitName string) *strings.Replacer {
	return strings.NewReplacer(
		"{{name}}", name,
		"{{email}}", email,
		"{{password}}", password,
		"{{login_url}}", loginURL,
		"{{unit_name}}", unitName,
	)
}

// sendWelcomeEmail emails a brand-new login's address and temporary
// password using this unit's customized template (or the default, if
// never customized). familyAccount distinguishes a family-wide login
// (gets familyCrossUnitNote appended) from an individual member's own
// login (doesn't).
//
// Best-effort, same posture as sendResetEmail: logs and returns false on
// any failure — mail not configured, template load error, send error —
// rather than failing the account-creation request itself. The account
// is already created either way, and the caller still shows the
// credentials on-screen (see renderCredentials) so a leader can hand
// them off manually if the email didn't go out.
func (h *Handlers) sendWelcomeEmail(r *http.Request, name, email, password string, familyAccount bool) bool {
	if !h.Mailer.Enabled(r.Context()) {
		log.Printf("web: welcome email requested for %s but email isn't configured", email)
		return false
	}
	unit, _ := units.UnitFromContext(r.Context())

	subjectTmpl, err := settings.GetUnitText(r.Context(), h.Pool, unit.ID, settings.WelcomeEmailSubject)
	if err != nil {
		log.Printf("web: loading welcome email subject template: %v", err)
	}
	if strings.TrimSpace(subjectTmpl) == "" {
		subjectTmpl = defaultWelcomeEmailSubject
	}
	bodyTmpl, err := settings.GetUnitText(r.Context(), h.Pool, unit.ID, settings.WelcomeEmailBody)
	if err != nil {
		log.Printf("web: loading welcome email body template: %v", err)
	}
	if strings.TrimSpace(bodyTmpl) == "" {
		bodyTmpl = defaultWelcomeEmailBody
	}

	loginURL := h.requestOrigin(r) + "/login"
	replacer := welcomeEmailReplacer(name, email, password, loginURL, unit.Name)
	subject := replacer.Replace(subjectTmpl)
	body := replacer.Replace(bodyTmpl)
	if familyAccount {
		body += familyCrossUnitNote
	}

	if err := h.Mailer.Send(r.Context(), email, subject, body); err != nil {
		log.Printf("web: sending welcome email to %s: %v", email, err)
		return false
	}
	return true
}
