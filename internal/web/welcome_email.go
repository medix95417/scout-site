package web

import (
	"html"
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

// familyCrossUnitNoteHTML is the same sentence for an HTML body. Kept as
// its own constant rather than run through the text-to-HTML conversion
// below, so the markup around it is deliberate rather than incidental.
const familyCrossUnitNoteHTML = "<p>Once logged in, you'll be able to see every child linked to your family's account — across both our Pack and Troop sites, using this same login.</p>"

// welcomeEmailReplacer builds the {{name}}/{{email}}/{{password}}/
// {{login_url}}/{{unit_name}} substitution for a welcome email template
// — plain string substitution, not html/template, since this is a
// leader-edited field (see settings.WelcomeEmailBody), not code to
// execute.
//
// escape controls whether the substituted VALUES are HTML-escaped. It
// must be true whenever the result is sent as HTML: a member named
// "Ben & Jo" or a generated password containing a "<" would otherwise
// land as raw markup, at best mangling the email and at worst letting a
// value close a tag the template opened. The template itself is never
// escaped — that's the whole point of letting a leader write HTML — only
// the values dropped into it.
func welcomeEmailReplacer(name, email, password, loginURL, unitName string, escape bool) *strings.Replacer {
	esc := func(v string) string {
		if escape {
			return html.EscapeString(v)
		}
		return v
	}
	return strings.NewReplacer(
		"{{name}}", esc(name),
		"{{email}}", esc(email),
		"{{password}}", esc(password),
		"{{login_url}}", esc(loginURL),
		"{{unit_name}}", esc(unitName),
	)
}

// looksLikeHTML reports whether a stored template is meant to be HTML.
//
// The test is deliberately crude — does it contain a "<" at all — because
// the alternative is worse in both directions. Parsing to decide would
// call a template with one stray "<" HTML and mangle the rest; a separate
// "this template is HTML" checkbox would be one more thing to set wrong,
// and every existing template predates it.
//
// A plain-text template (the default, and every template customized
// before this) has no "<" and takes the textToHTML path below, so it
// renders exactly as it always did. A leader who writes any tag at all
// gets their markup through untouched.
func looksLikeHTML(body string) bool { return strings.Contains(body, "<") }

// textToHTML renders a plain-text template as HTML: escaped, with blank
// lines becoming paragraphs and single newlines becoming breaks. Without
// this, switching to an HTML send would collapse every existing template
// into one run-on paragraph, since HTML ignores newlines.
func textToHTML(body string) string {
	var out strings.Builder
	for i, para := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString("<p>")
		out.WriteString(strings.ReplaceAll(html.EscapeString(para), "\n", "<br>\n"))
		out.WriteString("</p>")
	}
	return out.String()
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

	// The subject is always plain text — mail clients don't render markup
	// in a subject line — so its values are substituted unescaped.
	subject := welcomeEmailReplacer(name, email, password, loginURL, unit.Name, false).Replace(subjectTmpl)

	// A template a leader wrote HTML into is sent as-is. One without any
	// markup — the default, and anything customized before HTML was
	// allowed — is converted, so it reads the same as it always has.
	htmlTemplate := looksLikeHTML(bodyTmpl)
	if !htmlTemplate {
		bodyTmpl = textToHTML(bodyTmpl)
	}
	body := welcomeEmailReplacer(name, email, password, loginURL, unit.Name, true).Replace(bodyTmpl)
	if familyAccount {
		body += "\n" + familyCrossUnitNoteHTML
	}

	if err := h.Mailer.SendHTML(r.Context(), email, subject, body); err != nil {
		log.Printf("web: sending welcome email to %s: %v", email, err)
		return false
	}
	return true
}
