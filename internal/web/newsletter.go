package web

// This file is the newsletter feature — scout-website-requirements.md's
// Phase 3 "newsletter email" candidate. A leader (same units.CanEditUnitContent
// gate as /admin/news and /admin/home — see requireContentEditor in
// content_posts.go, reused here unchanged) drafts a subject/body, edits
// it freely, then sends it once to every family currently on the unit's
// roster (internal/newsletter.RecipientEmailsForUnit) via
// internal/mailer. Unlike news/gallery there's no draft<->published
// toggle: sending is a one-way transition (see internal/newsletter's
// package comment for why), so the admin list just shows each
// newsletter's current state (draft, or sent — with when and to how
// many).

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/newsletter"
	"github.com/47-yonkers/scout-site/internal/settings"
)

// requireNewsletterEnabled reports whether the newsletter feature is on
// for unitID (see settings.NewsletterEnabled), writing a clear 403 if
// not — hiding the "Newsletters" nav link (see base.html) isn't itself a
// security boundary, so every newsletter route checks this directly too,
// the same way advancement.go's requireAdvancementEnabled does for
// /admin/advancement.
func (h *Handlers) requireNewsletterEnabled(w http.ResponseWriter, r *http.Request, unitID string) bool {
	enabled, err := settings.GetForUnit(r.Context(), h.Pool, unitID, settings.NewsletterEnabled)
	if err != nil {
		log.Printf("web: checking newsletter-enabled setting: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if !enabled {
		http.Error(w, "newsletters are turned off for this unit — an admin can re-enable them from /admin/settings", http.StatusForbidden)
		return false
	}
	return true
}

// newsletterRow is one newsletter on the admin list, with its pointer
// fields (SentAt, RecipientCount — nil for a still-draft newsletter)
// resolved to plain display values: html/template's eq/ne funcs reject a
// raw pointer argument outright (they only indirect through interfaces,
// not pointers), so a *int/*time.Time field can't be compared directly
// in a template action — see admin-newsletter-list.html's pluralization.
type newsletterRow struct {
	ID             string
	Subject        string
	Status         string
	SentOn         string
	RecipientCount int
}

func (h *Handlers) AdminNewsletterList(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/newsletters")
	if !ok {
		return
	}
	if !h.requireNewsletterEnabled(w, r, unit.ID) {
		return
	}

	items, err := newsletter.ListForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing newsletters: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rows := make([]newsletterRow, 0, len(items))
	for _, n := range items {
		row := newsletterRow{ID: n.ID, Subject: n.Subject, Status: n.Status, SentOn: newsletterSentOn(n.SentAt)}
		if n.RecipientCount != nil {
			row.RecipientCount = *n.RecipientCount
		}
		rows = append(rows, row)
	}

	data := struct {
		baseData
		Items []newsletterRow
	}{baseData: h.base(r, "Newsletters"), Items: rows}
	h.render(w, h.newsletterList, data)
}

func (h *Handlers) AdminNewsletterNew(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/newsletters/new")
	if !ok {
		return
	}
	if !h.requireNewsletterEnabled(w, r, unit.ID) {
		return
	}

	data := struct {
		baseData
		IsEdit           bool
		Newsletter       newsletter.Newsletter
		StarterTemplates template.JS
	}{
		baseData:         h.base(r, "New Newsletter"),
		StarterTemplates: starterTemplatesJSON(unit.UnitType),
	}
	h.render(w, h.newsletterForm, data)
}

// starterTemplatesJSON marshals the unit's built-in starter templates
// (internal/newsletter.StarterTemplates) to a JSON array for the "start
// from a template" picker on admin-newsletter-form.html — server-authored
// Go constants, not user input, so embedding the result directly in a
// <script> block (see the template) is safe. Marshal failing here would
// mean a bug in a Go struct literal, not bad input, so it's logged and
// swallowed rather than failing the whole page.
func starterTemplatesJSON(unitType string) template.JS {
	b, err := json.Marshal(newsletter.StarterTemplates(unitType))
	if err != nil {
		log.Printf("web: marshaling newsletter starter templates: %v", err)
		return template.JS("[]")
	}
	return template.JS(b) //nolint:gosec // server-authored constants, not user input — see comment above
}

func (h *Handlers) AdminNewsletterEdit(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/newsletters")
	if !ok {
		return
	}
	if !h.requireNewsletterEnabled(w, r, unit.ID) {
		return
	}

	n, err := newsletter.GetNewsletter(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if err != nil {
		if errors.Is(err, newsletter.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("web: loading newsletter for edit: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := struct {
		baseData
		IsEdit     bool
		Newsletter newsletter.Newsletter
	}{baseData: h.base(r, "Edit Newsletter"), IsEdit: true, Newsletter: n}
	h.render(w, h.newsletterForm, data)
}

func (h *Handlers) AdminNewsletterCreate(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/newsletters")
	if !ok {
		return
	}
	if !h.requireNewsletterEnabled(w, r, unit.ID) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	subject := strings.TrimSpace(r.PostFormValue("subject"))
	body := r.PostFormValue("body")
	if len(body) > settings.MaxEmailTemplateBytes {
		http.Error(w, settings.ErrTemplateTooLarge.Error(), http.StatusBadRequest)
		return
	}
	if subject == "" {
		http.Error(w, "subject is required", http.StatusBadRequest)
		return
	}

	n, err := newsletter.CreateDraft(r.Context(), h.Pool, unit.ID, subject, body, actor.ID)
	if err != nil {
		log.Printf("web: creating newsletter: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/newsletters/"+n.ID+"/edit", http.StatusSeeOther)
}

func (h *Handlers) AdminNewsletterUpdate(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/newsletters")
	if !ok {
		return
	}
	if !h.requireNewsletterEnabled(w, r, unit.ID) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	subject := strings.TrimSpace(r.PostFormValue("subject"))
	body := r.PostFormValue("body")
	if len(body) > settings.MaxEmailTemplateBytes {
		http.Error(w, settings.ErrTemplateTooLarge.Error(), http.StatusBadRequest)
		return
	}
	if subject == "" {
		http.Error(w, "subject is required", http.StatusBadRequest)
		return
	}

	id := r.PathValue("id")
	if _, err := newsletter.UpdateDraft(r.Context(), h.Pool, id, unit.ID, subject, body, actor.ID); err != nil {
		if errors.Is(err, newsletter.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, newsletter.ErrAlreadySent) {
			http.Error(w, "this newsletter has already been sent and can't be edited", http.StatusBadRequest)
			return
		}
		log.Printf("web: updating newsletter %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/newsletters/"+id+"/edit", http.StatusSeeOther)
}

// newsletterSendTimeout bounds how long the background half of a send
// (see newsletter.SendNow) is allowed to run — generous enough to cover
// a full roster each taking internal/mailer's own worst-case per-message
// time, but finite, so a fundamentally stuck send can't run forever.
const newsletterSendTimeout = 30 * time.Minute

func (h *Handlers) AdminNewsletterSend(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/newsletters")
	if !ok {
		return
	}
	if !h.requireNewsletterEnabled(w, r, unit.ID) {
		return
	}

	id := r.PathValue("id")
	n, err := newsletter.BeginSend(r.Context(), h.Pool, h.Mailer, id, unit.ID)
	if err != nil {
		switch {
		case errors.Is(err, newsletter.ErrNotFound):
			http.NotFound(w, r)
		case errors.Is(err, newsletter.ErrAlreadySent):
			http.Error(w, "this newsletter is already sending, or has already been sent", http.StatusBadRequest)
		case errors.Is(err, newsletter.ErrNoRecipients):
			http.Error(w, "no recipients found — is anyone on this unit's roster yet?", http.StatusBadRequest)
		default:
			log.Printf("web: starting newsletter send %s: %v", id, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}

	// The actual per-recipient sending happens here, detached from this
	// request: context.Background() (bounded by its own generous timeout),
	// never r.Context() — so a browser disconnect or reverse-proxy
	// timeout can no longer cut a send short or lose track of what
	// actually went out, which is exactly what used to happen (see
	// newsletter.BeginSend/SendNow's comments). A panic here would
	// otherwise crash the whole process — this goroutine runs outside any
	// request's callstack, so it isn't covered by net/http's own
	// per-request recovery.
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("web: newsletter send %s panicked: %v", id, rec)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), newsletterSendTimeout)
		defer cancel()
		result := newsletter.SendNow(ctx, h.Pool, h.Mailer, n, actor.ID)
		log.Printf("web: newsletter %s sent: %d succeeded, %d failed", id, result.Sent, result.Failed)
	}()

	http.Redirect(w, r, "/admin/newsletters/"+id+"/edit", http.StatusSeeOther)
}

// newsletterSentOn formats a newsletter's SentAt for the admin list —
// mirrors postedOn's shape (content_posts.go) for the same "Mon Jan 2"
// display everywhere else in the admin UI uses.
func newsletterSentOn(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("Mon Jan 2, 2006 3:04 PM")
}

// AdminNewsletterView is the record of a sent newsletter: what went out
// and who received it.
//
// A separate page from the edit form because it answers a different
// question. The form is for changing a draft; this is for looking back at
// something that can no longer change — which is why it renders the body
// as the families saw it rather than in an editor, and why it lists every
// address rather than a count.
func (h *Handlers) AdminNewsletterView(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/newsletters")
	if !ok {
		return
	}
	if !h.requireNewsletterEnabled(w, r, unit.ID) {
		return
	}

	n, err := newsletter.GetNewsletter(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if errors.Is(err, newsletter.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("web: loading newsletter: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A draft has nothing to look back at, so send it to the editor
	// instead of showing an empty record.
	if n.Status == "draft" {
		http.Redirect(w, r, "/admin/newsletters/"+n.ID+"/edit", http.StatusSeeOther)
		return
	}

	recipients, err := newsletter.RecipientsFor(r.Context(), h.Pool, n.ID)
	if err != nil {
		log.Printf("web: loading newsletter recipients: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rows := make([]newsletterRecipientRow, 0, len(recipients))
	delivered := 0
	for _, rec := range recipients {
		row := newsletterRecipientRow{Email: rec.Email, Err: rec.Err}
		if rec.SentAt != nil {
			row.SentOn = rec.SentAt.Format("Mon Jan 2, 2006 3:04 PM")
			delivered++
		}
		rows = append(rows, row)
	}

	// Newsletters sent before per-recipient recording existed have a
	// count but no rows. Saying so is better than an empty table that
	// reads as "it reached nobody".
	legacy := len(rows) == 0 && n.RecipientCount != nil && *n.RecipientCount > 0

	data := struct {
		baseData
		Newsletter newsletter.Newsletter
		// Body went through newsletter.Sanitize before it was stored and
		// before it was emailed — the same allowlist that made it safe to
		// send makes it safe to show here.
		Body           template.HTML
		Recipients     []newsletterRecipientRow
		Delivered      int
		Failed         int
		SentOn         string
		RecipientCount int
		LegacySend     bool
	}{
		baseData:   h.base(r, "Newsletter"),
		Newsletter: n,
		Body:       template.HTML(n.Body), //nolint:gosec // sanitized by newsletter.Sanitize before storage
		Recipients: rows,
		Delivered:  delivered,
		Failed:     len(rows) - delivered,
		SentOn:     newsletterSentOn(n.SentAt),
		LegacySend: legacy,
	}
	if n.RecipientCount != nil {
		data.RecipientCount = *n.RecipientCount
	}
	h.render(w, h.newsletterView, data)
}

type newsletterRecipientRow struct {
	Email  string
	SentOn string
	Err    string
}
