package web

import (
	"errors"
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file is the site-wide settings page — /admin/settings — a small,
// generic list of on/off toggles (see internal/settings) for
// configuration that affects the whole install rather than one unit's
// content or books. It's deliberately the narrowest admin surface in the
// app (super_admin only, not the wider AdultLeaderRoles/CanManageLedger
// tiers other admin pages use), and deliberately generic: adding a new
// toggle later is adding one entry to internal/settings.Toggles, not a
// new page.

// requireSuperAdmin resolves the current unit/user and checks
// units.IsSuperAdmin, writing an HTTP error/redirect and returning
// ok=false if the request should stop here. Mirrors requireTreasurer's
// shape in treasury.go.
func (h *Handlers) requireSuperAdmin(w http.ResponseWriter, r *http.Request, redirectPath string) (actor family.Member, ok bool) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next="+redirectPath, http.StatusSeeOther)
		return family.Member{}, false
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.IsSuperAdmin(caps) {
		http.Error(w, "you don't have permission to view site settings", http.StatusForbidden)
		return family.Member{}, false
	}

	actor, err = h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return family.Member{}, false
	}
	return actor, true
}

// toggleView decorates a settings.Toggle with its current on/off state,
// for the template.
type toggleView struct {
	settings.Toggle
	Enabled bool
}

// textSettingView decorates a settings.TextSetting with its current
// stored value (empty if unset — falls back to whatever the environment
// provides, see internal/mailer.Mailer.effective), for the template.
type textSettingView struct {
	settings.TextSetting
	Value string
}

func (h *Handlers) SystemSettingsView(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireSuperAdmin(w, r, "/admin/settings"); !ok {
		return
	}

	values, err := settings.All(r.Context(), h.Pool)
	if err != nil {
		log.Printf("web: loading system settings: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	textValues, err := settings.AllText(r.Context(), h.Pool)
	if err != nil {
		log.Printf("web: loading system text settings: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	views := make([]toggleView, 0, len(settings.Toggles))
	for _, t := range settings.Toggles {
		views = append(views, toggleView{Toggle: t, Enabled: values[t.Key]})
	}
	textViews := make([]textSettingView, 0, len(settings.TextSettings))
	for _, t := range settings.TextSettings {
		textViews = append(textViews, textSettingView{TextSetting: t, Value: textValues[t.Key]})
	}

	data := struct {
		baseData
		Toggles      []toggleView
		TextSettings []textSettingView
	}{
		baseData:     h.base(r, "Site Settings"),
		Toggles:      views,
		TextSettings: textViews,
	}
	h.render(w, h.systemSettings, data)
}

// SystemSettingsUpdateText saves every text setting (currently just the
// SMTP host/port/username/from fields) from one combined form submission —
// see admin-settings.html. Each field is independent (a blank field
// clears that one override back to "" / falls back to the environment),
// so this loops settings.TextSettings and calls SetText once per key
// rather than needing a dedicated handler per field the way the boolean
// toggles do (those are one-click flips; these are free-text inputs
// submitted together).
func (h *Handlers) SystemSettingsUpdateText(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireSuperAdmin(w, r, "/admin/settings")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	for _, t := range settings.TextSettings {
		if err := settings.SetText(r.Context(), h.Pool, t.Key, r.FormValue(t.Key), actor.ID); err != nil {
			log.Printf("web: updating text setting %q: %v", t.Key, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// SystemSettingsToggle flips one setting to its opposite value. There's
// no separate "on"/"off" choice in the form — a toggle only ever needs
// to become whatever it currently isn't, same as a light switch, so a
// single button per row (see admin-settings.html) is enough.
func (h *Handlers) SystemSettingsToggle(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.requireSuperAdmin(w, r, "/admin/settings")
	if !ok {
		return
	}

	key := r.PathValue("key")
	current, err := settings.Get(r.Context(), h.Pool, key)
	if err != nil {
		log.Printf("web: reading setting %q: %v", key, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := settings.Set(r.Context(), h.Pool, key, !current, actor.ID); err != nil {
		if errors.Is(err, settings.ErrUnknownSetting) {
			http.Error(w, "unrecognized setting", http.StatusBadRequest)
			return
		}
		log.Printf("web: updating setting %q: %v", key, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}
