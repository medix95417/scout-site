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
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/newsletter"
)

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
	_, _, ok := h.requireContentEditor(w, r, "/admin/newsletters/new")
	if !ok {
		return
	}

	data := struct {
		baseData
		IsEdit     bool
		Newsletter newsletter.Newsletter
	}{baseData: h.base(r, "New Newsletter")}
	h.render(w, h.newsletterForm, data)
}

func (h *Handlers) AdminNewsletterEdit(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/newsletters")
	if !ok {
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	subject := strings.TrimSpace(r.PostFormValue("subject"))
	body := r.PostFormValue("body")
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	subject := strings.TrimSpace(r.PostFormValue("subject"))
	body := r.PostFormValue("body")
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

func (h *Handlers) AdminNewsletterSend(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/newsletters")
	if !ok {
		return
	}

	id := r.PathValue("id")
	result, err := newsletter.Send(r.Context(), h.Pool, h.Mailer, id, unit.ID, actor.ID)
	if err != nil {
		switch {
		case errors.Is(err, newsletter.ErrNotFound):
			http.NotFound(w, r)
		case errors.Is(err, newsletter.ErrAlreadySent):
			http.Error(w, "this newsletter has already been sent", http.StatusBadRequest)
		case errors.Is(err, newsletter.ErrNoRecipients):
			http.Error(w, "no recipients found — is anyone on this unit's roster yet?", http.StatusBadRequest)
		default:
			log.Printf("web: sending newsletter %s: %v", id, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}

	log.Printf("web: newsletter %s sent: %d succeeded, %d failed", id, result.Sent, result.Failed)
	http.Redirect(w, r, "/admin/newsletters", http.StatusSeeOther)
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
