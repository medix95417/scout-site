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

// unitToggleView is toggleView's per-unit sibling — see
// settings.UnitToggle.
type unitToggleView struct {
	settings.UnitToggle
	Enabled bool
}

// textSettingView decorates a settings.TextSetting with its current
// stored value (empty if unset — falls back to whatever the environment
// provides, see internal/mailer.Mailer.effective), for the template.
type textSettingView struct {
	settings.TextSetting
	Value string
}

// unitTextSettingView decorates a settings.UnitTextSetting for the
// Payments section of /admin/settings. Value holds the actual stored
// value for a normal field, but is always "" for a Secret one — a
// credential is never rendered back into the page once saved (see
// settings.UnitTextSettingIsSet); HasValue is what the template shows in
// its place ("already set" vs. "not set").
type unitTextSettingView struct {
	settings.UnitTextSetting
	Value    string
	HasValue bool
}

func (h *Handlers) SystemSettingsView(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
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
	unitValues, err := settings.AllForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading unit settings: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	unitTextValues, err := settings.AllUnitText(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading unit payment settings: %v", err)
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

	// Split UnitToggles by Section so "This Unit's Settings" and
	// "Payments" render as two distinct groups on the page, even though
	// both are stored/toggled through the exact same mechanism.
	var unitViews, paymentToggleViews []unitToggleView
	for _, t := range settings.UnitToggles {
		v := unitToggleView{UnitToggle: t, Enabled: unitValues[t.Key]}
		if t.Section == "payments" {
			paymentToggleViews = append(paymentToggleViews, v)
		} else {
			unitViews = append(unitViews, v)
		}
	}

	unitTextViews := make([]unitTextSettingView, 0, len(settings.UnitTextSettings))
	for _, t := range settings.UnitTextSettings {
		v := unitTextSettingView{UnitTextSetting: t}
		if t.Secret {
			v.HasValue = unitTextValues[t.Key] != ""
		} else {
			v.Value = unitTextValues[t.Key]
		}
		unitTextViews = append(unitTextViews, v)
	}

	data := struct {
		baseData
		Toggles             []toggleView
		TextSettings        []textSettingView
		UnitToggles         []unitToggleView
		PaymentToggles      []unitToggleView
		PaymentTextSettings []unitTextSettingView
	}{
		baseData:            h.base(r, "Site Settings"),
		Toggles:             views,
		TextSettings:        textViews,
		UnitToggles:         unitViews,
		PaymentToggles:      paymentToggleViews,
		PaymentTextSettings: unitTextViews,
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

// UnitSettingsUpdateText saves every per-unit text setting (currently
// the Stripe/PayPal credential fields — see settings.UnitTextSettings)
// from one combined Payments-section form submission, mirroring
// SystemSettingsUpdateText's shape. A Secret field left blank is a no-op
// (see settings.SetUnitText) rather than clearing it, so resubmitting the
// form to change one credential never wipes another the admin didn't
// retype.
func (h *Handlers) UnitSettingsUpdateText(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	actor, ok := h.requireSuperAdmin(w, r, "/admin/settings")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	for _, t := range settings.UnitTextSettings {
		if err := settings.SetUnitText(r.Context(), h.Pool, unit.ID, t.Key, r.FormValue(t.Key), actor.ID); err != nil {
			log.Printf("web: updating unit text setting %q: %v", t.Key, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// UnitSettingsToggle is SystemSettingsToggle's per-unit sibling — flips
// one of settings.UnitToggles for the currently-viewed unit only.
func (h *Handlers) UnitSettingsToggle(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	actor, ok := h.requireSuperAdmin(w, r, "/admin/settings")
	if !ok {
		return
	}

	key := r.PathValue("key")
	current, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, key)
	if err != nil {
		log.Printf("web: reading unit setting %q: %v", key, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := settings.SetForUnit(r.Context(), h.Pool, unit.ID, key, !current, actor.ID); err != nil {
		if errors.Is(err, settings.ErrUnknownSetting) {
			http.Error(w, "unrecognized setting", http.StatusBadRequest)
			return
		}
		log.Printf("web: updating unit setting %q: %v", key, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}
