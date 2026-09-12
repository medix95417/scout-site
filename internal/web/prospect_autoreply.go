package web

// The automatic reply to a new "interested in joining" enquiry.
//
// A family who fills in the form is, right then, at their most
// interested — and until now the site's answer was a line on a
// confirmation page and silence until a leader next opened the
// Prospects page. This sends them something back while they are still
// at the keyboard: someone has it, here's what happens next, here's
// where to look in the meantime.
//
// It follows the rules the rest of the prospect mail follows, because
// it goes to the same people: every copy carries the unsubscribe link
// (see appendUnsubscribeFooter), an opted-out address is never written
// to, and the leader-edited template is HTML if it has any markup in it
// and plain paragraphs if it doesn't — the same treatment the welcome
// email gets (see welcome_email.go), for the same reason.
//
// Off until a unit writes its own message and turns it on, on
// /admin/prospects.

import (
	"html"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/47-yonkers/scout-site/internal/prospect"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// The defaults, used when a unit has turned the reply on without
// writing its own. Deliberately says nothing it can't keep: no promise
// about how soon someone will be in touch, no meeting times, nothing a
// unit would have to remember to correct.
const (
	defaultProspectAutoReplySubject = "Thanks for getting in touch with {{unit_name}}"
	defaultProspectAutoReplyBody    = `Hi {{parent_name}},

Thanks for asking about joining {{unit_name}} — we've got your message and one of our leaders will be in touch.

In the meantime you're welcome to look around our website: {{site_url}}

We're glad you're interested.

{{unit_name}}`
)

// prospectPlaceholderPattern matches a placeholder with any whitespace
// inside the braces, so "{{ parent_name }}" works as well as
// "{{parent_name}}" — the variation people actually type, and a
// mismatch here is silent and lands on a real family.
var prospectPlaceholderPattern = regexp.MustCompile(`\{\{\s*(parent_name|child_name|unit_name|site_url)\s*\}\}`)

func canonicalizeProspectPlaceholders(body string) string {
	return prospectPlaceholderPattern.ReplaceAllString(body, "{{$1}}")
}

// prospectAutoReplyReplacer substitutes the placeholders.
//
// escape must be true whenever the result is sent as HTML: every value
// here was typed by a stranger into a public form, so a child's name
// containing "<" is not a hypothetical. The template itself is never
// escaped — a leader writing HTML is the point — only the values
// dropped into it.
func prospectAutoReplyReplacer(parentName, childName, unitName, siteURL string, escape bool) *strings.Replacer {
	esc := func(v string) string {
		if escape {
			return html.EscapeString(v)
		}
		return v
	}
	return strings.NewReplacer(
		"{{parent_name}}", esc(parentName),
		"{{child_name}}", esc(childName),
		"{{unit_name}}", esc(unitName),
		"{{site_url}}", esc(siteURL),
	)
}

// renderProspectAutoReply turns the stored templates into the subject
// and HTML body for one enquiry.
//
// A pure function, taking the templates rather than reading them, so
// the substitution and escaping can be tested without a database or a
// mail server — which is the half that matters, since it is the half
// that handles what a stranger typed.
func renderProspectAutoReply(subjectTmpl, bodyTmpl string, p prospect.Prospect, unitName, siteURL string) (subject, body string) {
	if strings.TrimSpace(subjectTmpl) == "" {
		subjectTmpl = defaultProspectAutoReplySubject
	}
	if strings.TrimSpace(bodyTmpl) == "" {
		bodyTmpl = defaultProspectAutoReplyBody
	}

	// A subject line is never rendered as markup, so its values are
	// substituted unescaped — and then stripped of anything that could
	// start a new header line, the same as every other subject this app
	// sends (see headerSafe).
	subject = headerSafe(prospectAutoReplyReplacer(p.ParentName, p.ChildName, unitName, siteURL, false).
		Replace(canonicalizeProspectPlaceholders(subjectTmpl)))

	// A template a leader wrote HTML into is sent as-is; one without any
	// markup is converted to paragraphs, so a plainly-typed message
	// doesn't arrive as one run-on line.
	if !looksLikeHTML(bodyTmpl) {
		bodyTmpl = textToHTML(bodyTmpl)
	}
	body = prospectAutoReplyReplacer(p.ParentName, p.ChildName, unitName, siteURL, true).
		Replace(canonicalizeProspectPlaceholders(bodyTmpl))
	return subject, body
}

// sendProspectAutoReply emails the family back, if this unit has turned
// the reply on.
//
// Best-effort and never fails the request, exactly like notifyProspect:
// the enquiry is already stored and already on the Prospects page, so a
// mail failure must not turn a successful submission into an error page
// for someone who has just asked to join.
func (h *Handlers) sendProspectAutoReply(r *http.Request, unit units.Unit, p prospect.Prospect) {
	on, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, settings.ProspectAutoReplyEnabled)
	if err != nil {
		log.Printf("web: checking the prospect auto-reply setting: %v", err)
		return
	}
	if !on {
		return
	}
	// Belt and braces: a brand-new enquiry can't have opted out, but the
	// rule is that an opted-out address is never written to, and a rule
	// with an exception in it is not a rule.
	if p.EmailOptOut || strings.TrimSpace(p.ParentEmail) == "" {
		return
	}
	if !h.Mailer.Enabled(r.Context()) {
		log.Printf("web: prospect auto-reply is on but email isn't configured, so nothing was sent")
		return
	}

	subjectTmpl, err := settings.GetUnitText(r.Context(), h.Pool, unit.ID, settings.ProspectAutoReplySubject)
	if err != nil {
		log.Printf("web: loading the prospect auto-reply subject: %v", err)
	}
	bodyTmpl, err := settings.GetUnitText(r.Context(), h.Pool, unit.ID, settings.ProspectAutoReplyBody)
	if err != nil {
		log.Printf("web: loading the prospect auto-reply body: %v", err)
	}

	base := h.siteURL(r)
	subject, body := renderProspectAutoReply(subjectTmpl, bodyTmpl, p, unit.Name, base)
	body = appendUnsubscribeFooter(body, base, prospect.Recipient{
		ProspectID: p.ID, Name: p.ParentName, Email: p.ParentEmail,
	}, h.UnsubscribeSecret)

	if err := h.Mailer.SendHTML(r.Context(), p.ParentEmail, subject, body); err != nil {
		log.Printf("web: sending the automatic reply to a new enquiry: %v", err)
	}
}

// ProspectAutoReplyUpdate saves the message and its on/off switch from
// the accordion on /admin/prospects.
//
// Gated by requireProspectManager — the same permission that already
// lets someone read every enquiry and send a recruiting campaign to the
// same families. Writing the note that goes out automatically is the
// smaller of those two powers, not a larger one.
func (h *Handlers) ProspectAutoReplyUpdate(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireProspectManager(w, r, "/admin/prospects")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	for _, key := range []string{settings.ProspectAutoReplySubject, settings.ProspectAutoReplyBody} {
		if err := settings.SetUnitText(r.Context(), h.Pool, unit.ID, key, r.FormValue(key), actor.ID); err != nil {
			if err == settings.ErrTemplateTooLarge {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			log.Printf("web: saving the prospect auto-reply %q: %v", key, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	if err := settings.SetForUnit(r.Context(), h.Pool, unit.ID,
		settings.ProspectAutoReplyEnabled, r.FormValue("enabled") == "1", actor.ID); err != nil {
		log.Printf("web: saving the prospect auto-reply switch: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/prospects#auto-email", http.StatusSeeOther)
}

// autoReplyView loads the automatic reply's current state for the
// Prospects page. Failures are logged and shown as the default rather
// than failing the whole page: the enquiry list is what a leader came
// for, and one unreadable setting shouldn't take it away.
func (h *Handlers) autoReplyView(r *http.Request, unitID string) autoReplyView {
	v := autoReplyView{MailerReady: h.Mailer.Enabled(r.Context())}

	on, err := settings.GetForUnit(r.Context(), h.Pool, unitID, settings.ProspectAutoReplyEnabled)
	if err != nil {
		log.Printf("web: loading the prospect auto-reply setting: %v", err)
	}
	v.Enabled = on

	if v.Subject, err = settings.GetUnitText(r.Context(), h.Pool, unitID, settings.ProspectAutoReplySubject); err != nil {
		log.Printf("web: loading the prospect auto-reply subject: %v", err)
	}
	if v.Body, err = settings.GetUnitText(r.Context(), h.Pool, unitID, settings.ProspectAutoReplyBody); err != nil {
		log.Printf("web: loading the prospect auto-reply body: %v", err)
	}
	if strings.TrimSpace(v.Subject) == "" {
		v.Subject = defaultProspectAutoReplySubject
	}
	if strings.TrimSpace(v.Body) == "" {
		v.Body = defaultProspectAutoReplyBody
		v.UsingDefault = true
	}
	return v
}
