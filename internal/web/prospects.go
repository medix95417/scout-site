package web

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/47-yonkers/scout-site/internal/emailtemplate"
	"github.com/47-yonkers/scout-site/internal/prospect"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file is the "interested in joining" enquiry form and the leader-
// facing page that tracks what happened to each enquiry.
//
// The form at /join is the second route (after the fundraiser
// storefront) where a member of the public can write to this database,
// and it is the only one that sends mail off the back of that write. It
// therefore gets the same treatment as the storefront and then some:
// a per-address rate limit checked before any query runs, hard length
// caps in both the handler and the schema, and CR/LF stripped from
// anything that reaches a mail header.
//
// Deliberately not linked from the homepage or the nav yet. The page
// works and can be tested by URL; where it eventually gets linked from
// is a separate decision.

// requireProspectFormEnabled reports whether this unit is accepting
// enquiries (settings.ProspectFormEnabled), writing a 404 if not.
//
// 404 rather than 403: with the form switched off there is no such page
// for this unit, and a public visitor has no business being told the
// difference.
func (h *Handlers) requireProspectFormEnabled(w http.ResponseWriter, r *http.Request, unitID string) bool {
	enabled, err := settings.GetForUnit(r.Context(), h.Pool, unitID, settings.ProspectFormEnabled)
	if err != nil {
		log.Printf("web: checking prospect-form-enabled setting: %v", err)
		http.NotFound(w, r)
		return false
	}
	if !enabled {
		http.NotFound(w, r)
		return false
	}
	return true
}

// joinFormData is the form's own state, so a rejected submission comes
// back with what was typed rather than an empty form.
type joinFormData struct {
	baseData
	Error  string
	Sent   bool
	Values map[string]string
}

// JoinForm renders the public enquiry form.
func (h *Handlers) JoinForm(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if !h.requireProspectFormEnabled(w, r, unit.ID) {
		return
	}
	h.render(w, h.joinPage, joinFormData{
		baseData: h.base(r, "Join "+unit.Name),
		Sent:     r.URL.Query().Get("sent") == "1",
		Values:   map[string]string{},
	})
}

// JoinSubmit records an enquiry and notifies whoever the unit listed.
func (h *Handlers) JoinSubmit(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if !h.requireProspectFormEnabled(w, r, unit.ID) {
		return
	}

	// Checked before any query, so a flood costs a map lookup rather than
	// database work — same posture as the storefront's order limiter.
	//
	// Blocked() peeks rather than consuming; the quota is only spent once
	// an enquiry is actually stored, further down. A family who mistypes
	// their email three times would otherwise burn most of an hour's
	// allowance on typos and be locked out of a recruitment form, which
	// is the one form on this site where turning someone away is the
	// worst possible outcome.
	ip := clientIP(r, h.TrustProxyHeaders)
	if h.joinLimiter.Blocked(ip) {
		retry := h.joinLimiter.RetryAfter(ip)
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		http.Error(w, "That's several enquiries from one place in a short time. Please wait a little while and "+
			"try again, or email us directly — see the Our Leaders page for how to reach us.",
			http.StatusTooManyRequests)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	values := map[string]string{
		"parent_name":  strings.TrimSpace(r.FormValue("parent_name")),
		"parent_email": strings.TrimSpace(r.FormValue("parent_email")),
		"parent_phone": strings.TrimSpace(r.FormValue("parent_phone")),
		"child_name":   strings.TrimSpace(r.FormValue("child_name")),
		"child_age":    strings.TrimSpace(r.FormValue("child_age")),
		"child_grade":  strings.TrimSpace(r.FormValue("child_grade")),
		"child_school": strings.TrimSpace(r.FormValue("child_school")),
		"message":      strings.TrimSpace(r.FormValue("message")),
	}

	reject := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		h.render(w, h.joinPage, joinFormData{
			baseData: h.base(r, "Join "+unit.Name),
			Error:    msg,
			Values:   values,
		})
	}

	var age *int
	if raw := values["child_age"]; raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			reject("Enter your child's age as a number, or leave it blank.")
			return
		}
		age = &n
	}

	p, err := prospect.Create(r.Context(), h.Pool, prospect.New{
		UnitID:      unit.ID,
		ParentName:  values["parent_name"],
		ParentEmail: values["parent_email"],
		ParentPhone: values["parent_phone"],
		ChildName:   values["child_name"],
		ChildAge:    age,
		ChildGrade:  values["child_grade"],
		ChildSchool: values["child_school"],
		Message:     values["message"],
	})
	if err != nil {
		if errors.Is(err, prospect.ErrInvalid) {
			// The wrapped message names the field and what's wrong with
			// it — written for the person filling in the form.
			msg := strings.TrimPrefix(err.Error(), "prospect: invalid submission: ")
			reject(strings.ToUpper(msg[:1]) + msg[1:] + ".")
			return
		}
		log.Printf("web: recording prospect: %v", err)
		http.Error(w, "Something went wrong saving that. Please try again in a moment.", http.StatusInternalServerError)
		return
	}

	// Spend the quota now that something was actually recorded — see the
	// Blocked() check above for why a rejected submission doesn't.
	h.joinLimiter.Allow(ip)

	// Best-effort, deliberately after the row is safely stored: an
	// enquiry that is recorded but not emailed still reaches a leader via
	// the Prospects page, whereas one that fails the request because mail
	// is down is simply lost.
	h.notifyProspect(r, unit, p)

	http.Redirect(w, r, "/join?sent=1", http.StatusSeeOther)
}

// prospectNotifyRecipients parses the configured notify list — one
// address per line or comma-separated, blanks ignored.
func prospectNotifyRecipients(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ';'
	}) {
		if addr := strings.TrimSpace(part); addr != "" {
			out = append(out, addr)
		}
	}
	return out
}

// headerSafe strips anything that could start a new header line. Used on
// every value that reaches a Subject — a name is attacker-controlled
// here, and a bare CR or LF in a header is how an extra Bcc gets
// injected into an otherwise ordinary email.
func headerSafe(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

// notifyProspect emails whoever the unit listed. Never fails the
// request: see the call site.
func (h *Handlers) notifyProspect(r *http.Request, unit units.Unit, p prospect.Prospect) {
	raw, err := settings.GetUnitText(r.Context(), h.Pool, unit.ID, settings.ProspectNotifyEmails)
	if err != nil {
		log.Printf("web: loading prospect notify list: %v", err)
		return
	}
	recipients := prospectNotifyRecipients(raw)
	if len(recipients) == 0 {
		return // nobody configured; the Prospects page is the record
	}
	if !h.Mailer.Enabled(r.Context()) {
		log.Printf("web: prospect enquiry recorded but email isn't configured, so nobody was notified")
		return
	}

	subject := headerSafe("New enquiry about joining " + unit.Name + " — " + p.ParentName)

	var b strings.Builder
	b.WriteString(p.ParentName + " asked about joining " + unit.Name + ".\n\n")
	b.WriteString("Parent/guardian: " + p.ParentName + "\n")
	b.WriteString("Email: " + p.ParentEmail + "\n")
	if p.ParentPhone != "" {
		b.WriteString("Phone: " + p.ParentPhone + "\n")
	}
	b.WriteString("\nChild: " + p.ChildName + "\n")
	if p.ChildAge != nil {
		b.WriteString("Age: " + strconv.Itoa(*p.ChildAge) + "\n")
	}
	if p.ChildGrade != "" {
		b.WriteString("Grade: " + p.ChildGrade + "\n")
	}
	if p.ChildSchool != "" {
		b.WriteString("School: " + p.ChildSchool + "\n")
	}
	if p.Message != "" {
		b.WriteString("\nWhat they said:\n" + p.Message + "\n")
	}
	b.WriteString("\nTrack this enquiry: " + h.requestOrigin(r) + "/admin/prospects\n")

	// Plain text on purpose. The body is entirely made of what a stranger
	// typed, and plain text is the one format where that can't render as
	// anything but the characters they wrote.
	for _, to := range recipients {
		if err := h.Mailer.Send(r.Context(), to, subject, b.String()); err != nil {
			log.Printf("web: notifying %s of a new prospect: %v", to, err)
		}
	}
}

// --- Leader-facing tracking ------------------------------------------------

// ProspectsList shows this unit's enquiries and what's happened to them.
// Leader-only — an enquiry carries a child's name, age and school, which
// is not something the rest of the unit needs to read.
func (h *Handlers) ProspectsList(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/prospects")
	if !ok {
		return
	}

	showAll := r.URL.Query().Get("all") == "1"
	list, err := prospect.ListForUnit(r.Context(), h.Pool, unit.ID, !showAll)
	if err != nil {
		log.Printf("web: listing prospects: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	openCount, err := prospect.CountOpenForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: counting open prospects: %v", err)
	}

	// The campaign history sits on this page rather than one of its own:
	// "who have we written to, and did they get it" is the same question
	// as "where is this family up to", and splitting them across two
	// pages means a leader has to remember to check the second.
	campaigns, err := prospect.ListCampaigns(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing prospect campaigns: %v", err)
	}
	campaignRows := make([]campaignRow, 0, len(campaigns))
	for _, c := range campaigns {
		campaignRows = append(campaignRows, campaignRow{
			ID: c.ID, Subject: c.Subject, Status: c.Status, Sent: c.Sent(),
			Audience: c.StatusLabels(), SentOn: newsletterSentOn(c.SentAt),
			RecipientCount: c.RecipientCount,
		})
	}

	saved, err := emailtemplate.ListForUnit(r.Context(), h.Pool, unit.ID, emailtemplate.KindProspect)
	if err != nil {
		log.Printf("web: listing saved prospect templates: %v", err)
	}

	// How many opted-out prospects there are, so the page can say so
	// rather than a leader wondering why a campaign reached fewer people
	// than the status counts suggest.
	optedOut := 0
	for _, p := range list {
		if p.EmailOptOut {
			optedOut++
		}
	}

	data := struct {
		baseData
		Prospects      []prospect.Prospect
		Statuses       []prospect.StatusOption
		ShowAll        bool
		OpenCount      int
		OptedOutCount  int
		Campaigns      []campaignRow
		SavedTemplates []emailtemplate.Template
	}{
		baseData:       h.base(r, "Prospects"),
		Prospects:      list,
		Statuses:       prospect.Statuses,
		ShowAll:        showAll,
		OpenCount:      openCount,
		OptedOutCount:  optedOut,
		Campaigns:      campaignRows,
		SavedTemplates: saved,
	}
	h.render(w, h.prospectsPage, data)
}

// campaignRow is one past or pending campaign as the prospects page lists
// it. Flattened out of prospect.Campaign because html/template can't
// compare a *time.Time in an action — the same reason newsletterRow
// exists (see newsletter.go).
type campaignRow struct {
	ID             string
	Subject        string
	Status         string
	Sent           bool
	Audience       string
	SentOn         string
	RecipientCount int
}

// ProspectOptOut is the leader-operated side of email consent — for a
// family who asked to come off the list by replying, or in person, rather
// than through the link.
func (h *Handlers) ProspectOptOut(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/prospects")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	optOut := r.FormValue("opt_out") == "1"
	if _, err := prospect.SetEmailOptOut(r.Context(), h.Pool, unit.ID, r.PathValue("id"), optOut, actor.ID); err != nil {
		writeProspectError(w, err)
		return
	}
	http.Redirect(w, r, prospectReturnTo(r), http.StatusSeeOther)
}

// ProspectUpdate records where an enquiry has got to and what was said.
func (h *Handlers) ProspectUpdate(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/prospects")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	_, err := prospect.UpdateStatus(r.Context(), h.Pool, unit.ID, r.PathValue("id"),
		r.FormValue("status"), r.FormValue("notes"), actor.ID)
	if err != nil {
		writeProspectError(w, err)
		return
	}
	http.Redirect(w, r, prospectReturnTo(r), http.StatusSeeOther)
}

// ProspectDelete removes an enquiry — for the spam a public form
// eventually attracts.
func (h *Handlers) ProspectDelete(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/prospects")
	if !ok {
		return
	}
	if err := prospect.Delete(r.Context(), h.Pool, unit.ID, r.PathValue("id"), actor.ID); err != nil {
		writeProspectError(w, err)
		return
	}
	http.Redirect(w, r, prospectReturnTo(r), http.StatusSeeOther)
}

// prospectReturnTo keeps the leader on whichever filter they were
// looking at rather than bouncing them back to the default view.
func prospectReturnTo(r *http.Request) string {
	if r.FormValue("all") == "1" {
		return "/admin/prospects?all=1"
	}
	return "/admin/prospects"
}

func writeProspectError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, prospect.ErrNotFound):
		http.Error(w, "that enquiry doesn't belong to this unit", http.StatusNotFound)
	case errors.Is(err, prospect.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		log.Printf("web: prospect error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
