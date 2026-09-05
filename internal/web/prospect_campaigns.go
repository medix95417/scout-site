package web

// Mass email to prospective families, and the record of what went out.
//
// The composer is deliberately the same shape as the newsletter's — same
// editor, same "start from a template" picker, same draft-then-send
// one-way transition — because it is the same job with a different
// audience, and a leader who has sent one newsletter should not have to
// learn a second tool.
//
// What differs is everything that follows from the audience being members
// of the public rather than families with logins: every message carries
// an unsubscribe link, an opt-out is permanent unless the family asks
// otherwise, and prospect_campaign_recipients keeps the per-address
// record. See internal/prospect/campaign.go and optout.go.

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/emailtemplate"
	"github.com/47-yonkers/scout-site/internal/newsletter"
	"github.com/47-yonkers/scout-site/internal/prospect"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// campaignSendTimeout bounds the detached half of a send, same reasoning
// and same generosity as newsletterSendTimeout.
const campaignSendTimeout = 30 * time.Minute

// requireProspectManager gates every campaign page.
//
// Deliberately the same gate as the prospects list itself
// (units.CanEditUnitContent, via requireContentEditor): somebody trusted
// to read enquiries and phone the families is trusted to write to them.
// Adding a separate capability for it would mean a leader who can see the
// list but not act on it, which is not a real job.
func (h *Handlers) requireProspectManager(w http.ResponseWriter, r *http.Request, next string) (units.Unit, prospectActor, bool) {
	unit, actor, ok := h.requireContentEditor(w, r, next)
	if !ok {
		return unit, prospectActor{}, false
	}
	return unit, prospectActor{ID: actor.ID}, true
}

type prospectActor struct{ ID string }

// campaignStatusChoice is one audience checkbox on the composer.
type campaignStatusChoice struct {
	Value    string
	Label    string
	Selected bool
	// Count is how many prospects currently hold this status and have not
	// opted out — shown next to the checkbox so a leader can see the size
	// of the audience before sending rather than after.
	Count int
}

// campaignFormData is the composer, for both a new campaign and an edit.
type campaignFormData struct {
	baseData
	IsEdit           bool
	Campaign         prospect.Campaign
	Statuses         []campaignStatusChoice
	RecipientCount   int
	SavedTemplates   []emailtemplate.Template
	StarterTemplates template.JS
	MailerReady      bool
}

func (h *Handlers) AdminCampaignNew(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireProspectManager(w, r, "/admin/prospects")
	if !ok {
		return
	}

	// A new campaign starts aimed at the two statuses a recruiting email
	// is nearly always for: people who enquired and people someone has
	// spoken to. Families already marked joined or not-joining are not
	// preselected, since writing to them is the unusual case.
	draft := prospect.Campaign{TargetStatuses: []string{prospect.StatusNew, prospect.StatusContacted}}
	h.renderCampaignForm(w, r, unit, draft, false)
}

func (h *Handlers) AdminCampaignEdit(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireProspectManager(w, r, "/admin/prospects")
	if !ok {
		return
	}

	c, err := prospect.GetCampaign(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if errors.Is(err, prospect.ErrCampaignNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("web: loading campaign: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A sent campaign is a record, not a draft — it opens read-only.
	if c.Sent() {
		http.Redirect(w, r, "/admin/prospect-campaigns/"+c.ID, http.StatusSeeOther)
		return
	}
	h.renderCampaignForm(w, r, unit, c, true)
}

func (h *Handlers) renderCampaignForm(w http.ResponseWriter, r *http.Request, unit units.Unit, c prospect.Campaign, isEdit bool) {
	selected := map[string]bool{}
	for _, s := range c.TargetStatuses {
		selected[s] = true
	}

	choices := make([]campaignStatusChoice, 0, len(prospect.Statuses))
	for _, s := range prospect.Statuses {
		n, err := prospect.RecipientsForStatuses(r.Context(), h.Pool, unit.ID, []string{s.Value})
		if err != nil {
			log.Printf("web: counting prospects for status %s: %v", s.Value, err)
		}
		choices = append(choices, campaignStatusChoice{
			Value: s.Value, Label: s.Label, Selected: selected[s.Value], Count: len(n),
		})
	}

	reach, err := prospect.RecipientsForStatuses(r.Context(), h.Pool, unit.ID, c.TargetStatuses)
	if err != nil {
		log.Printf("web: counting campaign recipients: %v", err)
	}

	saved, err := emailtemplate.ListForUnit(r.Context(), h.Pool, unit.ID, emailtemplate.KindProspect)
	if err != nil {
		log.Printf("web: listing saved prospect templates: %v", err)
	}

	title := "New Message to Prospects"
	if isEdit {
		title = "Edit Message"
	}

	h.render(w, h.campaignForm, campaignFormData{
		baseData:         h.base(r, title),
		IsEdit:           isEdit,
		Campaign:         c,
		Statuses:         choices,
		RecipientCount:   len(reach),
		SavedTemplates:   saved,
		StarterTemplates: prospectStarterTemplatesJSON(unit),
		MailerReady:      h.Mailer.Enabled(r.Context()),
	})
}

// prospectStarterTemplatesJSON gives the composer something to start
// from, the way the newsletter composer already does.
//
// Server-authored Go constants with the unit's own name substituted in —
// not user input — so embedding the result in a <script> is safe, same as
// starterTemplatesJSON. Marshal failing would mean a bug in a struct
// literal, so it's logged and swallowed rather than failing the page.
func prospectStarterTemplatesJSON(unit units.Unit) template.JS {
	b, err := json.Marshal(newsletter.ProspectStarterTemplates(unit.Name, unit.UnitType))
	if err != nil {
		log.Printf("web: marshaling prospect starter templates: %v", err)
		return template.JS("[]")
	}
	return template.JS(b) //nolint:gosec // server-authored constants, not user input — see comment above
}

func (h *Handlers) AdminCampaignCreate(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireProspectManager(w, r, "/admin/prospects")
	if !ok {
		return
	}

	// Same ceiling the newsletter composer enforces (see
	// settings.MaxEmailTemplateBytes) — this path had none, which
	// mattered little while the body was only ever stored, and matters
	// now that images inside it get decoded and written to storage.
	if len(r.FormValue("body")) > settings.MaxEmailTemplateBytes {
		http.Error(w, settings.ErrTemplateTooLarge.Error(), http.StatusBadRequest)
		return
	}
	body := h.hostInlineImages(r.Context(), unit.ID, h.siteURL(r), &actor.ID, r.FormValue("body"))
	c, err := prospect.CreateCampaign(r.Context(), h.Pool, unit.ID,
		r.FormValue("subject"), body,
		r.Form["statuses"], actor.ID)
	if err != nil {
		writeProspectError(w, err)
		return
	}
	h.maybeSaveCampaignTemplate(r, unit.ID, actor.ID, c.Subject, c.Body)
	http.Redirect(w, r, "/admin/prospect-campaigns/"+c.ID+"/edit", http.StatusSeeOther)
}

func (h *Handlers) AdminCampaignUpdate(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireProspectManager(w, r, "/admin/prospects")
	if !ok {
		return
	}

	// Same ceiling the newsletter composer enforces (see
	// settings.MaxEmailTemplateBytes) — this path had none, which
	// mattered little while the body was only ever stored, and matters
	// now that images inside it get decoded and written to storage.
	if len(r.FormValue("body")) > settings.MaxEmailTemplateBytes {
		http.Error(w, settings.ErrTemplateTooLarge.Error(), http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	body := h.hostInlineImages(r.Context(), unit.ID, h.siteURL(r), &actor.ID, r.FormValue("body"))
	c, err := prospect.UpdateCampaign(r.Context(), h.Pool, id, unit.ID,
		r.FormValue("subject"), body,
		r.Form["statuses"], actor.ID)
	switch {
	case errors.Is(err, prospect.ErrCampaignNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, prospect.ErrCampaignSent):
		http.Error(w, "that message has already been sent, so it can't be changed", http.StatusBadRequest)
		return
	case err != nil:
		writeProspectError(w, err)
		return
	}
	h.maybeSaveCampaignTemplate(r, unit.ID, actor.ID, c.Subject, c.Body)
	http.Redirect(w, r, "/admin/prospect-campaigns/"+id+"/edit", http.StatusSeeOther)
}

// maybeSaveCampaignTemplate honours the "also save this as a template"
// field on the composer.
//
// A failure here is logged, not surfaced: the campaign itself saved
// fine, and losing the draft because the template name collided with
// something would be a worse outcome than a template that quietly
// didn't save. The composer shows the saved list, so a leader can see
// whether it worked.
func (h *Handlers) maybeSaveCampaignTemplate(r *http.Request, unitID, actorID, subject, body string) {
	name := strings.TrimSpace(r.FormValue("save_as_template"))
	if name == "" {
		return
	}
	if _, err := emailtemplate.Save(r.Context(), h.Pool, unitID, emailtemplate.KindProspect,
		name, subject, body, actorID); err != nil {
		log.Printf("web: saving prospect email template %q: %v", name, err)
	}
}

func (h *Handlers) AdminCampaignDelete(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireProspectManager(w, r, "/admin/prospects")
	if !ok {
		return
	}
	err := prospect.DeleteCampaign(r.Context(), h.Pool, r.PathValue("id"), unit.ID, actor.ID)
	switch {
	case errors.Is(err, prospect.ErrCampaignNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, prospect.ErrCampaignSent):
		http.Error(w, "a sent message is kept as the record of what went out, so it can't be deleted", http.StatusBadRequest)
		return
	case err != nil:
		log.Printf("web: deleting campaign: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/prospects", http.StatusSeeOther)
}

func (h *Handlers) AdminCampaignSend(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireProspectManager(w, r, "/admin/prospects")
	if !ok {
		return
	}

	id := r.PathValue("id")
	c, recipients, err := prospect.BeginSendCampaign(r.Context(), h.Pool, h.Mailer, id, unit.ID)
	switch {
	case errors.Is(err, prospect.ErrCampaignNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, prospect.ErrCampaignSent):
		http.Error(w, "that message is already sending, or has already been sent", http.StatusBadRequest)
		return
	case errors.Is(err, prospect.ErrNoCampaignRecipients):
		http.Error(w, "nobody would receive this — no prospects hold the statuses you picked, "+
			"or the ones who do have opted out of emails", http.StatusBadRequest)
		return
	case errors.Is(err, prospect.ErrMailerDisabled):
		http.Error(w, "email isn't configured for this site, so nothing can be sent", http.StatusBadRequest)
		return
	case err != nil:
		log.Printf("web: starting campaign send %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// The unsubscribe link has to be built here rather than in the
	// prospect package: it needs the signing secret and this request's
	// own hostname, so that a message sent from the Pack site points back
	// at the Pack site.
	base := h.siteURL(r)
	secret := h.UnsubscribeSecret
	personalize := func(body string, rec prospect.Recipient) string {
		return appendUnsubscribeFooter(body, base, rec, secret)
	}

	// Detached from this request for the same reasons as the newsletter
	// send: a browser disconnect must not cut a send short, and a panic
	// in a bare goroutine would take the whole process down.
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("web: campaign send %s panicked: %v", id, rec)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), campaignSendTimeout)
		defer cancel()
		sent, failed := prospect.SendCampaign(ctx, h.Pool, h.Mailer, c, recipients, personalize, actor.ID)
		log.Printf("web: campaign %s sent: %d succeeded, %d failed", id, sent, failed)
	}()

	http.Redirect(w, r, "/admin/prospect-campaigns/"+id, http.StatusSeeOther)
}

// AdminCampaignView is the record of a campaign: what was sent, to whom,
// and whether it arrived.
func (h *Handlers) AdminCampaignView(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireProspectManager(w, r, "/admin/prospects")
	if !ok {
		return
	}

	c, err := prospect.GetCampaign(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if errors.Is(err, prospect.ErrCampaignNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("web: loading campaign: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	recipients, err := prospect.CampaignRecipients(r.Context(), h.Pool, c.ID)
	if err != nil {
		log.Printf("web: loading campaign recipients: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rows := make([]campaignRecipientRow, 0, len(recipients))
	delivered := 0
	for _, rec := range recipients {
		row := campaignRecipientRow{Name: rec.Name, Email: rec.Email, Err: rec.Err}
		if rec.SentAt != nil {
			row.SentOn = rec.SentAt.Format("Mon Jan 2, 2006 3:04 PM")
			delivered++
		}
		rows = append(rows, row)
	}

	data := struct {
		baseData
		Campaign prospect.Campaign
		// Body is the exact HTML that was sent, shown in a sandboxed
		// iframe (see the template) rather than rendered into this page.
		// A plain string, not template.HTML: it goes into a srcdoc
		// attribute, and html/template's attribute escaping is exactly
		// what is wanted there.
		Body       string
		Recipients []campaignRecipientRow
		Delivered  int
		Failed     int
		SentOn     string
	}{
		baseData:   h.base(r, "Message to Prospects"),
		Campaign:   c,
		Body:       c.Body,
		Recipients: rows,
		Delivered:  delivered,
		Failed:     len(rows) - delivered,
		SentOn:     newsletterSentOn(c.SentAt),
	}
	h.render(w, h.campaignView, data)
}

type campaignRecipientRow struct {
	Name   string
	Email  string
	SentOn string
	Err    string
}

// AdminCampaignTemplate serves one saved template as JSON, for the
// composer's "start from a template" picker.
//
// Fetched on demand rather than embedded in the page with every other
// saved body: a unit that has kept a dozen full HTML letters would
// otherwise send all of them down on every page load, to use one.
func (h *Handlers) AdminCampaignTemplate(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireProspectManager(w, r, "/admin/prospects")
	if !ok {
		return
	}

	t, err := emailtemplate.Get(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if errors.Is(err, emailtemplate.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("web: loading saved email template: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Held privately: the body is the unit's own writing, and there is no
	// reason for a shared cache to keep it.
	w.Header().Set("Cache-Control", "private, no-store")
	if err := json.NewEncoder(w).Encode(struct {
		Name    string
		Subject string
		Body    string
	}{t.Name, t.Subject, t.Body}); err != nil {
		log.Printf("web: writing saved email template: %v", err)
	}
}

// AdminCampaignTemplateDelete removes a saved template.
func (h *Handlers) AdminCampaignTemplateDelete(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireProspectManager(w, r, "/admin/prospects")
	if !ok {
		return
	}
	err := emailtemplate.Delete(r.Context(), h.Pool, r.PathValue("id"), unit.ID, actor.ID)
	if err != nil && !errors.Is(err, emailtemplate.ErrNotFound) {
		log.Printf("web: deleting saved email template: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/prospects", http.StatusSeeOther)
}
