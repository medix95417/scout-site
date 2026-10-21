package web

// Emailing a den or patrol: the people in a group hear about updates that come
// up in it, from the people who run it.
//
// There are two ways in, and they end in the same place. On a group's page a
// leader who posts an update can tick "also email it", and from the group's
// "Email this den/patrol" page anyone allowed to email it can write a message
// — optionally also posting it on the page. The second exists because the
// first needs permission to post to the page, and a Patrol Leader has none:
// they can email their own patrol and nothing else.
//
// Who may email which group is groupEmailAccess, and only that. It is not
// "can manage the group's page" — that is a different, wider permission over a
// different thing, and borrowing it is how the Patrol Leader would have
// ended up either unable to send or able to edit the homepage.

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/newsletter"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// groupEmailAccess is which dens/patrols the signed-in person may email.
type groupEmailAccess struct {
	// everyGroup is a unit-wide leader who holds the capability: any group.
	everyGroup bool
	// groups are the ones a role assignment ties the capability to: a Patrol
	// Leader's own patrol, a Den Leader's own den.
	groups map[string]bool
}

func (a groupEmailAccess) can(groupID string) bool {
	return a.everyGroup || (groupID != "" && a.groups[groupID])
}

// groupEmailAccess works out what this login may email.
//
// Two things have to hold for the unit-wide case — the role grants the
// capability (units.CanEmailGroup) AND the person is unit-wide
// (roster.Scope.UnitWide), the way every other "manage any group" check in
// the roster is decided. For a particular group it is a role assigned to
// that group which grants it (roster.SubGroupIDsWithCapability). A shared
// login carries the household's roles and an individual one only its own,
// exactly as everywhere else.
func (h *Handlers) groupEmailAccess(ctx context.Context, user auth.User, unitID string) (groupEmailAccess, error) {
	caps, err := h.capabilitiesFor(ctx, user, unitID)
	if err != nil {
		return groupEmailAccess{}, err
	}
	scope, err := h.rosterScope(ctx, user, unitID)
	if err != nil {
		return groupEmailAccess{}, err
	}
	ids, err := roster.SubGroupIDsWithCapability(ctx, h.Pool, user.FamilyID, user.MemberID, unitID, units.CapEmailGroup)
	if err != nil {
		return groupEmailAccess{}, err
	}
	acc := groupEmailAccess{
		everyGroup: units.CanEmailGroup(caps) && scope.UnitWide,
		groups:     make(map[string]bool, len(ids)),
	}
	for _, id := range ids {
		acc.groups[id] = true
	}
	return acc, nil
}

// groupEmailDoor is the front of both group-email routes: signed in, on this
// unit's roster, email switched on, the group real and running, and the person
// allowed to email it. It answers the request itself and reports false, or
// hands back everything a route needs.
type groupEmailDoor struct {
	unit  units.Unit
	user  auth.User
	actor family.Member
	group roster.SubGroup
}

func (h *Handlers) groupEmailDoor(w http.ResponseWriter, r *http.Request) (groupEmailDoor, bool) {
	path := "/groups/" + r.PathValue("id") + "/email"
	unit, user, ok := h.requireUnitMember(w, r, path)
	if !ok {
		return groupEmailDoor{}, false
	}
	if !h.requireNewsletterEnabled(w, r, unit.ID) {
		return groupEmailDoor{}, false
	}

	group, found, err := roster.GetSubGroup(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if err != nil {
		log.Printf("web: loading group to email it: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return groupEmailDoor{}, false
	}
	if !found || !group.Active {
		http.NotFound(w, r)
		return groupEmailDoor{}, false
	}

	acc, err := h.groupEmailAccess(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: working out who may email groups: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return groupEmailDoor{}, false
	}
	if !acc.can(group.ID) {
		http.Error(w, "you can email the "+subGroupNoun(unit.UnitType)+" you lead, and this isn't it", http.StatusForbidden)
		return groupEmailDoor{}, false
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return groupEmailDoor{}, false
	}
	return groupEmailDoor{unit: unit, user: user, actor: actor, group: group}, true
}

// groupEmailDoorFor is the door's permission checks for a route that has
// already resolved the unit and who is acting — posting an update with "also
// email" ticked, which got that far through the page-editing gate. Whether the
// email feature is on, the group is real and running, and this login may email
// it are decided here exactly as for the composer, so the two cannot disagree.
func (h *Handlers) groupEmailDoorFor(w http.ResponseWriter, r *http.Request, unit units.Unit, actor family.Member, groupID string) (groupEmailDoor, bool) {
	user, _ := auth.UserFromContext(r.Context())
	if !h.requireNewsletterEnabled(w, r, unit.ID) {
		return groupEmailDoor{}, false
	}
	group, found, err := roster.GetSubGroup(r.Context(), h.Pool, groupID, unit.ID)
	if err != nil {
		log.Printf("web: loading group to email it: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return groupEmailDoor{}, false
	}
	if !found || !group.Active {
		http.NotFound(w, r)
		return groupEmailDoor{}, false
	}
	acc, err := h.groupEmailAccess(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: working out who may email groups: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return groupEmailDoor{}, false
	}
	if !acc.can(group.ID) {
		http.Error(w, "you can post here, but emailing this "+subGroupNoun(unit.UnitType)+" isn't something your role allows — post without ticking \"also email\"", http.StatusForbidden)
		return groupEmailDoor{}, false
	}
	return groupEmailDoor{unit: unit, user: user, actor: actor, group: group}, true
}

// canPostToGroupPage reports whether this person may also publish to the
// group's own page: the existing permission for that, unchanged — the right to
// edit content, over this group.
func (h *Handlers) canPostToGroupPage(ctx context.Context, user auth.User, unitID, groupID string) bool {
	caps, err := h.capabilitiesFor(ctx, user, unitID)
	if err != nil || !units.CanEditUnitContent(caps) {
		return false
	}
	scope, err := h.rosterScope(ctx, user, unitID)
	if err != nil {
		return false
	}
	return scope.CanManageSubGroup(groupID)
}

// publishGroupPost posts an update to a group's page and publishes it at once —
// the one thing "post an update" has always done, here so the form on the page
// and the email composer do it the same way.
func (h *Handlers) publishGroupPost(ctx context.Context, unitID, groupID, actorID, title, body string) (content.Post, error) {
	p, err := content.CreatePost(ctx, h.Pool, unitID, "post", title, body, "members", groupID, actorID)
	if err != nil {
		return content.Post{}, err
	}
	if err := content.SetPublished(ctx, h.Pool, p.ID, unitID, true, actorID); err != nil {
		return content.Post{}, err
	}
	return p, nil
}

// groupUpdateEmail is the email for one update to one group, ready to start.
func (h *Handlers) groupUpdateEmail(r *http.Request, d groupEmailDoor, title, body string, postID *string) newsletter.PostEmail {
	return newsletter.PostEmail{
		UnitID: d.unit.ID, Kind: newsletter.KindGroupUpdate, PostID: postID,
		// Named for the group in the subject, because "Campout" in an inbox
		// does not say which patrol it is for.
		Subject: d.group.Name + ": " + title,
		Body: renderPostEmail(postEmailLetter{
			Title: title, Body: body, Sender: senderName(d.actor), For: d.group.Name, Unit: d.unit.Name,
			SiteURL: h.siteURL(r), Link: "/groups/" + d.group.ID,
		}),
		ActorID: d.actor.ID,
		Groups:  []string{d.group.ID},
	}
}

// --- the composer ------------------------------------------------------------

type groupEmailData struct {
	baseData
	SubGroupNoun string
	Group        roster.SubGroup
	// Addresses is how many email addresses it would go to. A count, never the
	// addresses: a Patrol Leader emailing their patrol must not be handed the
	// parents' addresses to do it.
	Addresses int
	MailReady bool
	CanPost   bool
	Sent      []emailHistoryRow
	JustSent  bool
}

// maxGroupEmailSubject keeps a subject to something a mail client shows.
const maxGroupEmailSubject = 200

// GroupEmailForm is the page to write an email to one den or patrol.
func (h *Handlers) GroupEmailForm(w http.ResponseWriter, r *http.Request) {
	d, ok := h.groupEmailDoor(w, r)
	if !ok {
		return
	}

	addresses, err := newsletter.RecipientEmailsForGroups(r.Context(), h.Pool, d.unit.ID, []string{d.group.ID})
	if err != nil {
		log.Printf("web: counting who a group email would reach: %v", err)
	}
	var sent []emailHistoryRow
	if recent, err := newsletter.ListForGroup(r.Context(), h.Pool, d.unit.ID, d.group.ID, 10); err != nil {
		log.Printf("web: listing emails sent to a group: %v", err)
	} else {
		sent = h.emailHistory(r.Context(), d.unit.Name, recent)
	}

	data := groupEmailData{
		baseData:     h.base(r, "Email "+d.group.Name),
		SubGroupNoun: subGroupNoun(d.unit.UnitType),
		Group:        d.group,
		Addresses:    len(addresses),
		MailReady:    h.mailReady(r.Context()),
		CanPost:      h.canPostToGroupPage(r.Context(), d.user, d.unit.ID, d.group.ID),
		Sent:         sent,
		JustSent:     uuidPattern.MatchString(r.URL.Query().Get("sent")),
	}
	h.render(w, h.groupEmail, data)
}

// GroupEmailSend sends a message to one den or patrol.
func (h *Handlers) GroupEmailSend(w http.ResponseWriter, r *http.Request) {
	d, ok := h.groupEmailDoor(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	subject := strings.TrimSpace(r.PostFormValue("subject"))
	body := strings.TrimSpace(r.PostFormValue("body"))
	switch {
	case subject == "":
		http.Error(w, "give it a subject", http.StatusBadRequest)
		return
	case body == "":
		http.Error(w, "write the message", http.StatusBadRequest)
		return
	case len(subject) > maxGroupEmailSubject:
		http.Error(w, "that subject is too long for an email", http.StatusBadRequest)
		return
	case len(body) > settings.MaxEmailTemplateBytes:
		http.Error(w, settings.ErrTemplateTooLarge.Error(), http.StatusBadRequest)
		return
	}

	postToPage := r.PostFormValue("post_to_page") == "1"
	if postToPage && !h.canPostToGroupPage(r.Context(), d.user, d.unit.ID, d.group.ID) {
		http.Error(w, "you can email this "+subGroupNoun(d.unit.UnitType)+", but posting to its page needs more than your role has", http.StatusForbidden)
		return
	}
	if !h.mailReady(r.Context()) {
		http.Error(w, "email isn't set up for this site yet — an Admin can configure it from /admin/settings", http.StatusServiceUnavailable)
		return
	}

	// Everything that can refuse is checked before anything is written, so a
	// refusal never leaves the update posted to the page but not sent.
	if msg, status, refused := h.groupEmailPrecheck(r.Context(), d, subject); refused {
		http.Error(w, msg, status)
		return
	}

	var postID *string
	if postToPage {
		p, err := h.publishGroupPost(r.Context(), d.unit.ID, d.group.ID, d.actor.ID, subject, body)
		if err != nil {
			log.Printf("web: posting a group update that is also being emailed: %v", err)
			http.Error(w, "couldn't post it — nothing was sent", http.StatusInternalServerError)
			return
		}
		postID = &p.ID
	}

	n, err := h.startPostEmail(r.Context(), h.groupUpdateEmail(r, d, subject, body, postID))
	if err != nil {
		if msg, status, expected := postEmailRefusal(err, subGroupNoun(d.unit.UnitType)); expected {
			http.Error(w, msg, status)
			return
		}
		log.Printf("web: emailing group %s: %v", d.group.ID, err)
		http.Error(w, "couldn't start the email", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/groups/"+d.group.ID+"/email?sent="+n.ID, http.StatusSeeOther)
}

// groupEmailPrecheck refuses what would fail or double up, before a post
// exists. refused is false when it is fine to go ahead.
func (h *Handlers) groupEmailPrecheck(ctx context.Context, d groupEmailDoor, subject string) (message string, status int, refused bool) {
	addresses, err := newsletter.RecipientEmailsForGroups(ctx, h.Pool, d.unit.ID, []string{d.group.ID})
	if err != nil {
		log.Printf("web: checking who a group email would reach: %v", err)
		return "internal error", http.StatusInternalServerError, true
	}
	if len(addresses) == 0 {
		return "Nobody would receive this: no family is in " + d.group.Name + " yet.", http.StatusBadRequest, true
	}

	// A double click, or a page resubmitted, would otherwise send every family
	// the same message twice. The same subject to the same group in the last
	// minute is that; sending the same thing on purpose a minute later is not.
	recent, err := newsletter.ListForGroup(ctx, h.Pool, d.unit.ID, d.group.ID, 3)
	if err == nil {
		for _, n := range recent {
			if time.Since(n.CreatedAt) < time.Minute && strings.TrimPrefix(n.Subject, d.group.Name+": ") == subject {
				return "That was just sent to " + d.group.Name + " — it's on its way, so it hasn't been sent again.", http.StatusConflict, true
			}
		}
	}
	return "", 0, false
}
