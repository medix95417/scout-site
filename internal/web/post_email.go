package web

// Emailing a post, or an update to a den or patrol.
//
// Nothing here sends mail on its own initiative. Publishing a news post does
// not email anyone — a leader chooses to, per post, after reading it as
// published, and chooses who: the whole unit, or particular dens/patrols. That
// is the whole of the rule this file exists to keep, and it is why the
// trigger is an explicit action on a published post rather than a box on the
// publish button.
//
// The sending itself is the newsletter's (internal/newsletter): the same
// claim, the same paced background send, the same record of every address.
// What is here is the part that is specific to posts: turning plain text into
// an email, deciding who may start one, and the screens.

import (
	"context"
	"errors"
	"html"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/mailer"
	"github.com/47-yonkers/scout-site/internal/newsletter"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// --- the message -------------------------------------------------------------

// postEmailLetter is what one post email says, before it is HTML.
type postEmailLetter struct {
	Title string
	Body  string // plain text, as the post was typed
	// Sender is the person who chose to send it, named in the footer so a
	// family can tell who it came from: the message comes from the site's own
	// address, which says nothing about that.
	Sender string
	// For is who it was sent to, in words: "Troop 47", "Eagle Patrol".
	For string
	// Unit is the unit's name, for "you are receiving this because…".
	Unit string
	// SiteURL and Link say where it lives on the site: "https://host" and a
	// path. Members-only posts need a sign-in to open, which the footer says.
	SiteURL string
	Link    string
}

var paragraphBreak = regexp.MustCompile(`\n[ \t]*\n+`)

// renderPostEmail turns a post into the HTML of an email.
//
// The body is the post's plain text, rendered the way the site renders it —
// links recognised, labelled links honoured, everything else escaped — by the
// same linkify the site uses, so a web address that is a link on the page is a
// link in the email and nothing the author typed can become markup. Two
// things differ from the page: a YouTube address stays a link rather than
// becoming an embedded player, which mail clients do not run; and a link into
// the file library is made absolute, since a relative address in an email
// points at nothing.
//
// It returns a plain string and is passed through newsletter.Sanitize on the
// way into the record. It is not template.HTML, and must not become it:
// renderPostBody is the one place that cast is allowed (see its test).
func renderPostEmail(l postEmailLetter) string {
	var b strings.Builder

	b.WriteString("<h2>" + html.EscapeString(l.Title) + "</h2>\n")

	text := strings.ReplaceAll(strings.TrimSpace(l.Body), "\r\n", "\n")
	for _, para := range paragraphBreak.Split(text, -1) {
		if strings.TrimSpace(para) == "" {
			continue
		}
		lines := strings.Split(para, "\n")
		rendered := make([]string, 0, len(lines))
		for _, line := range lines {
			rendered = append(rendered, linkify(line))
		}
		b.WriteString("<p>" + strings.Join(rendered, "<br>\n") + "</p>\n")
	}

	site := strings.TrimRight(l.SiteURL, "/")
	out := b.String()
	// A path into the file library, rendered by linkify as a relative address.
	out = strings.ReplaceAll(out, `href="/files/`, `href="`+html.EscapeString(site)+`/files/`)

	var foot strings.Builder
	foot.WriteString(`<p style="font-size:12px;color:#666">`)
	if l.Sender != "" {
		foot.WriteString(html.EscapeString(l.Sender) + " sent this to " + html.EscapeString(l.For) + ". ")
	} else {
		foot.WriteString("Sent to " + html.EscapeString(l.For) + ". ")
	}
	if l.Link != "" {
		foot.WriteString(`<a href="` + html.EscapeString(site+l.Link) + `">Read it on the ` + html.EscapeString(l.Unit) +
			` site</a> (you may be asked to sign in).<br>`)
	}
	foot.WriteString("You are receiving this because a member of your family is on " + html.EscapeString(l.Unit) + "&#39;s roster.</p>")

	return out + foot.String()
}

// --- starting and sending ----------------------------------------------------

// mailReady reports whether the site can send mail at all. A site with no mail
// configured is a normal state, not an error: the screens say so and offer
// nothing that cannot work.
func (h *Handlers) mailReady(ctx context.Context) bool {
	return h.Mailer != nil && h.Mailer.Enabled(mailCtx(ctx))
}

// mailCtx marks a send — or the question "can mail go out?" — as being for the
// unit this request is on, so it is sent from that unit's own From address
// rather than the site-wide one (see mailer.WithUnit). Every send from a
// handler goes through it; TestEverySendNamesItsUnit fails one that doesn't,
// since the mistake is silent — the mail still goes, from the wrong address.
func mailCtx(ctx context.Context) context.Context {
	if unit, ok := units.UnitFromContext(ctx); ok {
		return mailer.WithUnit(ctx, unit.ID)
	}
	return ctx
}

// emailFeatureOn reports whether this unit has email broadcasts switched on.
// The same switch as the newsletters: the setting is "this unit emails its
// families through this site", and a unit that turned it off because it does
// that some other way does not want a second way to do it appearing under news
// and group pages.
func (h *Handlers) emailFeatureOn(ctx context.Context, unitID string) (bool, error) {
	return settings.GetForUnit(ctx, h.Pool, unitID, settings.NewsletterEnabled)
}

// startPostEmail starts an email and sends it in the background.
func (h *Handlers) startPostEmail(ctx context.Context, in newsletter.PostEmail) (newsletter.Newsletter, error) {
	n, err := newsletter.StartPostEmail(ctx, h.Pool, h.Mailer, in)
	if err != nil {
		return newsletter.Newsletter{}, err
	}
	h.sendInBackground(n, in.ActorID)
	return n, nil
}

// sendInBackground runs the slow half of a send — the per-recipient mail —
// detached from the request that started it.
//
// context.Background(), bounded by its own generous timeout, never r.Context():
// a browser that disconnects, or a proxy that times out, must not cut a send
// short or lose track of what went out. A panic here would otherwise take the
// whole process down, since this goroutine is outside any request's call
// stack and so outside net/http's own recovery.
func (h *Handlers) sendInBackground(n newsletter.Newsletter, actorID string) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("web: email %s panicked: %v", n.ID, rec)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), newsletterSendTimeout)
		defer cancel()
		result := newsletter.SendNow(ctx, h.Pool, h.Mailer, n, actorID)
		log.Printf("web: email %s (%s) sent: %d succeeded, %d failed", n.ID, n.Kind, result.Sent, result.Failed)
	}()
}

// postEmailRefusal puts an expected reason for not starting an email into
// words for the person, with the status that goes with it. ok is false for an
// error that is not one of the expected refusals, which the caller logs and
// reports as an internal error.
func postEmailRefusal(err error, noun string) (message string, status int, ok bool) {
	switch {
	case errors.Is(err, newsletter.ErrNoRecipients):
		return "Nobody would receive this: no family on the roster is in the " + noun + "s you chose yet.", http.StatusBadRequest, true
	case errors.Is(err, newsletter.ErrNoAudience):
		return "Choose which " + noun + " to send it to.", http.StatusBadRequest, true
	case errors.Is(err, newsletter.ErrBadAudience):
		return "One of the " + noun + "s you chose isn't one of this unit's, or is no longer running.", http.StatusBadRequest, true
	}
	return "", 0, false
}

// emailHistoryRow is one past email, for the page that offers sending another.
type emailHistoryRow struct {
	ID       string
	When     string
	Audience string
	Subject  string
	Status   string // "sending" | "sent"
	Count    int
}

// emailHistory describes records for display: when, to whom in words, how many.
func (h *Handlers) emailHistory(ctx context.Context, unitName string, ns []newsletter.Newsletter) []emailHistoryRow {
	rows := make([]emailHistoryRow, 0, len(ns))
	for _, n := range ns {
		row := emailHistoryRow{ID: n.ID, Subject: n.Subject, Status: n.Status, Audience: "everyone in " + unitName}
		when := n.CreatedAt
		if n.SentAt != nil {
			when = *n.SentAt
		}
		row.When = when.In(time.Local).Format("Mon Jan 2, 3:04 PM")
		if n.RecipientCount != nil {
			row.Count = *n.RecipientCount
		}
		if n.Audience == newsletter.AudienceGroups {
			names, err := newsletter.GroupNamesFor(ctx, h.Pool, n.ID)
			if err != nil {
				log.Printf("web: naming the audience of email %s: %v", n.ID, err)
			}
			row.Audience = strings.Join(names, " and ")
			if row.Audience == "" {
				row.Audience = "a group that no longer exists"
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// senderName is how the person who started an email is named in its footer.
func senderName(m family.Member) string {
	return strings.TrimSpace(m.FirstName + " " + m.LastName)
}

// --- unit news ---------------------------------------------------------------

// postEmailPanel is what the news editor shows under a published post: the
// choice of who to email it to, and what has already been sent.
type postEmailPanel struct {
	// Show is false for everything that cannot be emailed: a new post, a draft,
	// or a unit that has email switched off.
	Show bool
	// Hint is set instead of Show for a new post or a draft, in a unit that has
	// email on: the section is not there yet, and a leader looking for it should
	// be told when it will be rather than left to wonder where it went.
	Hint bool
	// MailReady is false when the site has no mail configured.
	MailReady bool
	// Groups are the dens/patrols the post can be addressed to.
	Groups    []roster.SubGroup
	GroupNoun string
	Sent      []emailHistoryRow
	// NeedsConfirm is set after a refused attempt to send a post that has
	// already been emailed: the leader has to say they mean it.
	NeedsConfirm bool
	// StartedID is the email that was just started, to link to its record.
	StartedID string
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// newsEmailPanel builds the panel for one news post.
func (h *Handlers) newsEmailPanel(r *http.Request, unit units.Unit, post content.Post) postEmailPanel {
	ctx := r.Context()
	// Only a published, unit-wide post can be emailed from here. A group's own
	// update is sent from that group's page, to that group.
	if post.SubGroupID != "" {
		return postEmailPanel{}
	}
	on, err := h.emailFeatureOn(ctx, unit.ID)
	if err != nil {
		log.Printf("web: checking the email setting: %v", err)
		return postEmailPanel{}
	}
	if !on {
		return postEmailPanel{}
	}
	if post.Status != "published" {
		return postEmailPanel{Hint: true}
	}

	panel := postEmailPanel{Show: true, MailReady: h.mailReady(ctx), GroupNoun: subGroupNoun(unit.UnitType)}
	if groups, err := roster.SubGroupsForUnit(ctx, h.Pool, unit.ID); err != nil {
		log.Printf("web: listing groups for the email panel: %v", err)
	} else {
		panel.Groups = groups
	}
	if sent, err := newsletter.ListForPost(ctx, h.Pool, unit.ID, post.ID); err != nil {
		log.Printf("web: listing emails made from post %s: %v", post.ID, err)
	} else {
		panel.Sent = h.emailHistory(ctx, unit.Name, sent)
	}
	panel.NeedsConfirm = r.URL.Query().Get("email") == "again"
	if id := r.URL.Query().Get("emailed"); uuidPattern.MatchString(id) {
		panel.StartedID = id
	}
	return panel
}

// AdminNewsEmail emails one published news post, to the whole unit or to the
// dens/patrols chosen.
//
// Opt-in per post and never automatic. A post that has been emailed before is
// not emailed again by a stray second click: the leader has to tick that they
// mean to, which is what stops a double submit, or a page left open and
// resubmitted, from sending forty families the same message twice.
func (h *Handlers) AdminNewsEmail(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, newsKind.BasePath)
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

	id := r.PathValue("id")
	post, found, err := content.GetPost(r.Context(), h.Pool, id, unit.ID, newsKind.PageType)
	if err != nil {
		log.Printf("web: loading news post %s to email it: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// A den's or patrol's own update is not unit news, whatever its type
	// says: emailing it from here would send one group's message to everyone.
	if !found || post.SubGroupID != "" {
		http.NotFound(w, r)
		return
	}
	if post.Status != "published" {
		http.Error(w, "publish this post before emailing it — an email says \"read it on the site\", and a draft isn't there to read", http.StatusBadRequest)
		return
	}
	if !h.mailReady(r.Context()) {
		http.Error(w, "email isn't set up for this site yet — an Admin can configure it from /admin/settings", http.StatusServiceUnavailable)
		return
	}

	var groups []string
	if r.PostFormValue("audience") == "groups" {
		groups = r.PostForm["group_ids"]
		if len(groups) == 0 {
			http.Error(w, "choose at least one "+subGroupNoun(unit.UnitType)+", or send it to everyone", http.StatusBadRequest)
			return
		}
	}

	prior, err := newsletter.ListForPost(r.Context(), h.Pool, unit.ID, post.ID)
	if err != nil {
		log.Printf("web: checking whether post %s was already emailed: %v", post.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if len(prior) > 0 && r.PostFormValue("again") != "1" {
		http.Redirect(w, r, newsKind.BasePath+"/"+post.ID+"/edit?email=again#email", http.StatusSeeOther)
		return
	}

	forLabel := unit.Name
	if len(groups) > 0 {
		forLabel = "selected " + subGroupNoun(unit.UnitType) + "s of " + unit.Name
	}
	body := renderPostEmail(postEmailLetter{
		Title: post.Title, Body: post.Body, Sender: senderName(actor), For: forLabel, Unit: unit.Name,
		SiteURL: h.siteURL(r), Link: newsKind.PublicPath + "/" + post.ID,
	})

	n, err := h.startPostEmail(r.Context(), newsletter.PostEmail{
		UnitID: unit.ID, Kind: newsletter.KindNews, PostID: &post.ID,
		Subject: post.Title, Body: body, ActorID: actor.ID, Groups: groups,
	})
	if err != nil {
		if msg, status, expected := postEmailRefusal(err, subGroupNoun(unit.UnitType)); expected {
			http.Error(w, msg, status)
			return
		}
		log.Printf("web: emailing news post %s: %v", post.ID, err)
		http.Error(w, "couldn't start the email — nothing was sent", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, newsKind.BasePath+"/"+post.ID+"/edit?emailed="+n.ID+"#email", http.StatusSeeOther)
}
