package web

// This file is the public "Our Leaders" page and its admin CRUD — a
// simple, admin-maintained listing of a unit's adult leaders (name, role
// title, bio, and an optional photo). Built on internal/leaders, which
// gives each leader profile the same draft/published lifecycle
// news posts and photo albums already have (see content_posts.go) so a
// half-written profile never shows on the public page. Gated the same way
// /admin/home, /admin/news, and /admin/gallery already are —
// units.CanEditUnitContent, i.e. any adult leader role, not just
// super_admin — via the same requireContentEditor helper content_posts.go
// defines.

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/leaders"
	"github.com/47-yonkers/scout-site/internal/units"
)

func (h *Handlers) LeadersList(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())

	list, err := leaders.ListPublishedForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing leaders: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := struct {
		baseData
		Leaders []leaders.Leader
	}{baseData: h.base(r, "Our Leaders"), Leaders: list}
	h.render(w, h.leadersList, data)
}

func (h *Handlers) AdminLeadersList(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/leaders")
	if !ok {
		return
	}

	list, err := leaders.ListForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing leaders for admin: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := struct {
		baseData
		Leaders []leaders.Leader
	}{baseData: h.base(r, "Manage Our Leaders"), Leaders: list}
	h.render(w, h.adminLeadersList, data)
}

// adminLeadersForm renders both the create form (id == "") and the edit
// form (id != "") — same one-template-serves-both pattern as
// adminContentForm.
func (h *Handlers) adminLeadersForm(w http.ResponseWriter, r *http.Request, id string) {
	unit, _, ok := h.requireContentEditor(w, r, "/admin/leaders")
	if !ok {
		return
	}

	var leader leaders.Leader
	isEdit := id != ""
	if isEdit {
		var found bool
		var err error
		leader, found, err = leaders.Get(r.Context(), h.Pool, id, unit.ID)
		if err != nil {
			log.Printf("web: loading leader %s for edit: %v", id, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !found {
			http.NotFound(w, r)
			return
		}
	}

	publicImageGroups, publicImagesUngrouped, err := files.ListImageFilesGroupedByEvent(r.Context(), h.Pool, unit.ID, true)
	if err != nil {
		log.Printf("web: loading public images for leader photo picker: %v", err)
	}

	data := struct {
		baseData
		IsEdit                bool
		Leader                leaders.Leader
		PublicImageGroups     []files.EventFileGroup
		PublicImagesUngrouped []files.File
	}{baseData: h.base(r, "Our Leaders"), IsEdit: isEdit, Leader: leader, PublicImageGroups: publicImageGroups, PublicImagesUngrouped: publicImagesUngrouped}
	h.render(w, h.adminLeadersFormTmpl, data)
}

func (h *Handlers) AdminLeadersNew(w http.ResponseWriter, r *http.Request) {
	h.adminLeadersForm(w, r, "")
}

func (h *Handlers) AdminLeadersEdit(w http.ResponseWriter, r *http.Request) {
	h.adminLeadersForm(w, r, r.PathValue("id"))
}

// leaderFormFields pulls and validates the fields shared by create and
// update — same title-required, trim-whitespace posture as
// adminContentCreate/adminContentUpdate.
func leaderFormFields(r *http.Request) (name, roleTitle, bio, photoURL string, sortOrder int, ok bool) {
	name = strings.TrimSpace(r.PostFormValue("name"))
	roleTitle = strings.TrimSpace(r.PostFormValue("role_title"))
	bio = r.PostFormValue("bio")
	photoURL = strings.TrimSpace(r.PostFormValue("photo_url"))
	sortOrder, _ = strconv.Atoi(r.PostFormValue("sort_order")) // blank/invalid just falls back to 0 — not worth rejecting the whole save over
	return name, roleTitle, bio, photoURL, sortOrder, name != ""
}

func (h *Handlers) AdminLeadersCreate(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/leaders")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	name, roleTitle, bio, photoURL, _, valid := leaderFormFields(r)
	if !valid {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	l, err := leaders.Create(r.Context(), h.Pool, unit.ID, name, roleTitle, bio, photoURL, actor.ID)
	if err != nil {
		log.Printf("web: creating leader: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/leaders/"+l.ID+"/edit", http.StatusSeeOther)
}

func (h *Handlers) AdminLeadersUpdate(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/leaders")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	id := r.PathValue("id")
	if _, found, err := leaders.Get(r.Context(), h.Pool, id, unit.ID); err != nil {
		log.Printf("web: loading leader %s for update: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}

	name, roleTitle, bio, photoURL, sortOrder, valid := leaderFormFields(r)
	if !valid {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	if _, err := leaders.Update(r.Context(), h.Pool, id, unit.ID, name, roleTitle, bio, photoURL, sortOrder, actor.ID); err != nil {
		log.Printf("web: updating leader %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/leaders/"+id+"/edit", http.StatusSeeOther)
}

func (h *Handlers) AdminLeadersPublishToggle(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/leaders")
	if !ok {
		return
	}

	id := r.PathValue("id")
	leader, found, err := leaders.Get(r.Context(), h.Pool, id, unit.ID)
	if err != nil {
		log.Printf("web: loading leader %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	if err := leaders.SetPublished(r.Context(), h.Pool, id, unit.ID, leader.Status != "published", actor.ID); err != nil {
		log.Printf("web: toggling publish state of leader %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/leaders", http.StatusSeeOther)
}

func (h *Handlers) AdminLeadersDelete(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireContentEditor(w, r, "/admin/leaders")
	if !ok {
		return
	}

	id := r.PathValue("id")
	if _, found, err := leaders.Get(r.Context(), h.Pool, id, unit.ID); err != nil {
		log.Printf("web: loading leader %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}

	if err := leaders.Delete(r.Context(), h.Pool, id, unit.ID, actor.ID); err != nil {
		log.Printf("web: deleting leader %s: %v", id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/leaders", http.StatusSeeOther)
}
