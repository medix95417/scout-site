package web

// This file is the members-only patrol/den page feature: each sub_group
// (see internal/roster.SubGroup) gets its own page with a short blurb and
// a handful of photos, similar in spirit to the public homepage's "Our
// Program" section and gallery strip, but never shown to a logged-out
// visitor — every route here requires login, and none of it is reachable
// from the public homepage. Viewing is open to any logged-in member (same
// as /roster); editing a sub-group's own blurb/photos requires being able
// to manage that specific sub-group (roster.Scope.CanManageSubGroup — a
// Den Leader can edit their own den's page, not another den's).

import (
	"log"
	"net/http"
	"strings"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/units"
)

// GroupsList lists every patrol/den in the unit, linking to each one's own
// page — the members-only landing point for /groups.
func (h *Handlers) GroupsList(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/groups", http.StatusSeeOther)
		return
	}

	groups, err := roster.SubGroupsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing sub-groups: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Scope drives the "Edit" link next to each group on this page — the
	// only way a Den Leader/Patrol Leader (not unit-wide) reaches their own
	// sub-group's edit page, since /admin/roster's own "Dens & Patrols"
	// list is unit-wide-leader-only.
	scope, err := h.rosterScope(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: computing roster scope: %v", err)
	}

	title := "Patrols"
	if unit.UnitType != "troop" {
		title = "Dens"
	}
	data := struct {
		baseData
		SubGroupNoun string
		Groups       []roster.SubGroup
		Scope        roster.Scope
	}{
		baseData:     h.base(r, title),
		SubGroupNoun: subGroupNoun(unit.UnitType),
		Groups:       groups,
		Scope:        scope,
	}
	h.render(w, h.groupsList, data)
}

// GroupView is one patrol/den's own members-only page: its blurb, member
// list, and linked photos.
func (h *Handlers) GroupView(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/groups/"+r.PathValue("id"), http.StatusSeeOther)
		return
	}

	group, found, err := roster.GetSubGroup(r.Context(), h.Pool, r.PathValue("id"), unit.ID)
	if err != nil {
		log.Printf("web: loading sub-group: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found || !group.Active {
		http.NotFound(w, r)
		return
	}

	members, err := family.RosterForSubGroup(r.Context(), h.Pool, group.ID)
	if err != nil {
		log.Printf("web: loading sub-group roster: %v", err)
	}

	photos, err := files.ListForSubGroup(r.Context(), h.Pool, group.ID)
	if err != nil {
		log.Printf("web: loading sub-group photos: %v", err)
	}

	// Events for this sub-group's own page: its own scoped events plus
	// every whole-unit event (a Troop event for a patrol, a Pack event for
	// a den) — the same visibility FilterVisibleToViewer already gives a
	// member of this one sub-group on /calendar, just without needing
	// canEditContent's "see every sub-group" escape hatch here.
	allEvents, err := calendar.ListUpcomingForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading events for sub-group page: %v", err)
	}
	events := calendar.FilterVisibleToViewer(allEvents, map[string]bool{group.ID: true}, false)
	if len(events) > 8 {
		events = events[:8]
	}

	news, err := content.ListPublishedForSubGroup(r.Context(), h.Pool, group.ID, unit.ID)
	if err != nil {
		log.Printf("web: loading sub-group news: %v", err)
	}
	newsViews := make([]publicPostView, 0, len(news))
	for _, p := range news {
		newsViews = append(newsViews, publicPostView{ID: p.ID, Title: p.Title, PostedOn: postedOn(p.CreatedAt), Excerpt: p.Body})
	}

	scope, err := h.rosterScope(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: computing roster scope: %v", err)
	}

	data := struct {
		baseData
		SubGroupNoun string
		Group        roster.SubGroup
		Members      []family.RosterEntry
		Photos       []files.File
		Events       []calendar.Event
		News         []publicPostView
		Scope        roster.Scope
	}{
		baseData:     h.base(r, group.Name),
		SubGroupNoun: subGroupNoun(unit.UnitType),
		Group:        group,
		Members:      members,
		Photos:       photos,
		Events:       events,
		News:         newsViews,
		Scope:        scope,
	}
	h.render(w, h.groupView, data)
}

// AdminGroupEdit shows the edit form for one patrol's/den's blurb and
// photo selection — gated to leaders who can manage that specific
// sub-group (unit-wide leaders, or the Den Leader/Patrol Leader scoped to
// it).
func (h *Handlers) AdminGroupEdit(w http.ResponseWriter, r *http.Request) {
	unit, _, scope, ok := h.requireRosterEditor(w, r, r.URL.Path)
	if !ok {
		return
	}
	subGroupID := r.PathValue("id")
	if !scope.CanManageSubGroup(subGroupID) {
		http.Error(w, "this "+subGroupNoun(unit.UnitType)+" is outside what you can manage", http.StatusForbidden)
		return
	}

	group, found, err := roster.GetSubGroup(r.Context(), h.Pool, subGroupID, unit.ID)
	if err != nil {
		log.Printf("web: loading sub-group: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	// A den/patrol page is members-only, so both its hero banner and its
	// Photos section can offer any image file regardless of the "Public"
	// flag (publicOnly=false) — no logged-out visitor ever sees either.
	imageGroups, imagesUngrouped, err := files.ListImageFilesGroupedByEvent(r.Context(), h.Pool, unit.ID, false)
	if err != nil {
		log.Printf("web: listing image files: %v", err)
	}

	linked, err := files.ListForSubGroup(r.Context(), h.Pool, subGroupID)
	if err != nil {
		log.Printf("web: loading linked photos: %v", err)
	}
	linkedFileID := make(map[string]bool, len(linked))
	for _, f := range linked {
		linkedFileID[f.ID] = true
	}

	data := struct {
		baseData
		SubGroupNoun    string
		Group           roster.SubGroup
		ImageGroups     []files.EventFileGroup
		ImagesUngrouped []files.File
		LinkedFileID    map[string]bool
	}{
		baseData:        h.base(r, "Edit "+group.Name),
		SubGroupNoun:    subGroupNoun(unit.UnitType),
		Group:           group,
		ImageGroups:     imageGroups,
		ImagesUngrouped: imagesUngrouped,
		LinkedFileID:    linkedFileID,
	}
	h.render(w, h.groupAdmin, data)
}

// AdminGroupUpdate saves a patrol's/den's blurb.
func (h *Handlers) AdminGroupUpdate(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, r.URL.Path)
	if !ok {
		return
	}
	subGroupID := r.PathValue("id")
	if !scope.CanManageSubGroup(subGroupID) {
		http.Error(w, "this "+subGroupNoun(unit.UnitType)+" is outside what you can manage", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	heroImageURL := strings.TrimSpace(r.PostFormValue("hero_image_url"))
	heroSize := content.NormalizeHeroSize(r.PostFormValue("hero_size"))
	if err := roster.UpdateSubGroupPage(r.Context(), h.Pool, subGroupID, unit.ID, r.PostFormValue("description"), heroImageURL, heroSize, actor.ID); err != nil {
		log.Printf("web: updating sub-group page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/groups/"+subGroupID, http.StatusSeeOther)
}

// AdminGroupSetPhotos saves which existing library photos a patrol's/den's
// page shows — the same "linked events" checkbox pattern files.html
// already uses, just linking to a sub-group instead of a calendar event.
func (h *Handlers) AdminGroupSetPhotos(w http.ResponseWriter, r *http.Request) {
	unit, _, scope, ok := h.requireRosterEditor(w, r, r.URL.Path)
	if !ok {
		return
	}
	subGroupID := r.PathValue("id")
	if !scope.CanManageSubGroup(subGroupID) {
		http.Error(w, "this "+subGroupNoun(unit.UnitType)+" is outside what you can manage", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if err := files.SetSubGroupLinks(r.Context(), h.Pool, subGroupID, unit.ID, r.Form["file_ids"]); err != nil {
		log.Printf("web: setting sub-group photo links: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/groups/"+subGroupID, http.StatusSeeOther)
}

// AdminGroupCreateNews posts a short news item to one patrol's/den's own
// page — same "manage your own sub-group" gate as AdminGroupUpdate/
// AdminGroupSetPhotos above. Published immediately rather than going
// through the unit-wide /admin/news draft workflow: this is a short,
// low-stakes update visible only within the site (the group page is
// already members-only), not worth a separate publish step.
func (h *Handlers) AdminGroupCreateNews(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, r.URL.Path)
	if !ok {
		return
	}
	subGroupID := r.PathValue("id")
	if !scope.CanManageSubGroup(subGroupID) {
		http.Error(w, "this "+subGroupNoun(unit.UnitType)+" is outside what you can manage", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.PostFormValue("title"))
	body := r.PostFormValue("body")
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}

	p, err := content.CreatePost(r.Context(), h.Pool, unit.ID, "post", title, body, "members", subGroupID, actor.ID)
	if err != nil {
		log.Printf("web: creating sub-group news post: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := content.SetPublished(r.Context(), h.Pool, p.ID, unit.ID, true, actor.ID); err != nil {
		log.Printf("web: publishing sub-group news post: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/groups/"+subGroupID, http.StatusSeeOther)
}

// AdminGroupDeleteNews removes one of this sub-group's own news posts —
// implemented as unpublishing (content.SetPublished false) rather than a
// hard delete, same "keep the history, just hide it" posture /admin/news
// already uses for its own publish toggle.
func (h *Handlers) AdminGroupDeleteNews(w http.ResponseWriter, r *http.Request) {
	unit, actor, scope, ok := h.requireRosterEditor(w, r, r.URL.Path)
	if !ok {
		return
	}
	subGroupID := r.PathValue("id")
	if !scope.CanManageSubGroup(subGroupID) {
		http.Error(w, "this "+subGroupNoun(unit.UnitType)+" is outside what you can manage", http.StatusForbidden)
		return
	}

	postID := r.PathValue("postID")
	post, found, err := content.GetPostAnyType(r.Context(), h.Pool, postID, unit.ID)
	if err != nil {
		log.Printf("web: loading sub-group news post: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found || post.SubGroupID != subGroupID {
		http.NotFound(w, r)
		return
	}

	if err := content.SetPublished(r.Context(), h.Pool, postID, unit.ID, false, actor.ID); err != nil {
		log.Printf("web: unpublishing sub-group news post: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/groups/"+subGroupID, http.StatusSeeOther)
}
