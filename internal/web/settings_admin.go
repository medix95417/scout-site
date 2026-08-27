package web

import (
	"errors"
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/ledger"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// categorySummaryView decorates a files.CategorySummary with a
// human-readable label and formatted size, and its own share of the
// unit's total storage, for the "File Storage" section of
// /admin/settings — see SystemSettingsView.
type categorySummaryView struct {
	files.CategorySummary
	Label       string
	SizeDisplay string
}

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

	// Split UnitToggles by Section so "This Unit's Settings", "Payments",
	// and "Social Media" render as three distinct groups on the page, even
	// though all three are stored/toggled through the exact same mechanism.
	var unitViews, paymentToggleViews, socialToggleViews []unitToggleView
	for _, t := range settings.UnitToggles {
		v := unitToggleView{UnitToggle: t, Enabled: unitValues[t.Key]}
		switch t.Section {
		case "payments":
			paymentToggleViews = append(paymentToggleViews, v)
		case "social":
			socialToggleViews = append(socialToggleViews, v)
		default:
			unitViews = append(unitViews, v)
		}
	}

	// legacySocialSlug maps a social UnitTextSetting's key back to the slug
	// it used to be saved under via /admin/home, before this page took it
	// over — see content.LegacySocialURL.
	legacySocialSlug := map[string]string{
		settings.SocialFacebookURL:  "home-facebook",
		settings.SocialInstagramURL: "home-instagram",
		settings.SocialTikTokURL:    "home-tiktok",
	}

	var paymentTextViews, socialTextViews, welcomeEmailTextViews []unitTextSettingView
	for _, t := range settings.UnitTextSettings {
		v := unitTextSettingView{UnitTextSetting: t}
		if t.Secret {
			v.HasValue = unitTextValues[t.Key] != ""
		} else {
			v.Value = unitTextValues[t.Key]
			if v.Value == "" {
				if legacySlug, ok := legacySocialSlug[t.Key]; ok {
					if legacy, err := content.LegacySocialURL(r.Context(), h.Pool, unit.ID, legacySlug); err != nil {
						log.Printf("web: loading legacy social URL for %q: %v", t.Key, err)
					} else {
						v.Value = legacy
					}
				}
			}
		}
		switch t.Section {
		case "social":
			socialTextViews = append(socialTextViews, v)
		case "welcome_email":
			welcomeEmailTextViews = append(welcomeEmailTextViews, v)
		default:
			paymentTextViews = append(paymentTextViews, v)
		}
	}

	// Fundraiser storefront: which single fundraiser (if any) shows its
	// "Buy Now" button on the homepage — see ledger.SetStorefrontFundraiser.
	fundraisers, err := ledger.ListFundraisersForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading fundraisers for settings: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var activeStorefrontID string
	for _, f := range fundraisers {
		if f.StorefrontEnabled {
			activeStorefrontID = f.ID
			break
		}
	}

	// File storage summary — see files.StorageSummaryForUnit. Read-only
	// reporting, so a site admin can see what's using storage without
	// having to browse /files' full listing themselves.
	byCategory, totalStorage, err := files.StorageSummaryForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading file storage summary: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	categoryLabels := map[string]string{
		files.CategoryGeneral:    "General documents",
		files.CategoryEventPhoto: "Event photos",
	}
	storageByCategory := make([]categorySummaryView, 0, len(byCategory))
	for _, c := range byCategory {
		label := categoryLabels[c.Category]
		if label == "" {
			label = c.Category
		}
		storageByCategory = append(storageByCategory, categorySummaryView{CategorySummary: c, Label: label, SizeDisplay: displaySize(c.SizeBytes)})
	}
	storageTotal := categorySummaryView{CategorySummary: totalStorage, Label: "Total", SizeDisplay: displaySize(totalStorage.SizeBytes)}

	data := struct {
		baseData
		Toggles             []toggleView
		TextSettings        []textSettingView
		UnitToggles         []unitToggleView
		PaymentToggles      []unitToggleView
		PaymentTextSettings []unitTextSettingView
		SocialToggles       []unitToggleView
		SocialTextSettings  []unitTextSettingView
		WelcomeEmailText    []unitTextSettingView
		Fundraisers         []ledger.Fundraiser
		ActiveStorefrontID  string
		StorageByCategory   []categorySummaryView
		StorageTotal        categorySummaryView
	}{
		baseData:            h.base(r, "Site Settings"),
		Toggles:             views,
		TextSettings:        textViews,
		UnitToggles:         unitViews,
		PaymentToggles:      paymentToggleViews,
		PaymentTextSettings: paymentTextViews,
		SocialToggles:       socialToggleViews,
		SocialTextSettings:  socialTextViews,
		WelcomeEmailText:    welcomeEmailTextViews,
		Fundraisers:         fundraisers,
		ActiveStorefrontID:  activeStorefrontID,
		StorageByCategory:   storageByCategory,
		StorageTotal:        storageTotal,
	}
	h.render(w, h.systemSettings, data)
}

// FundraiserStorefrontSettingsUpdate sets which fundraiser (if any) shows
// its "Buy Now" button on the homepage — the Settings-page half of the
// fundraiser storefront feature; the fundraiser's own item catalog and
// button image are managed on its Treasury page instead (see
// treasury.go's TreasuryFundraiserAddItem/TreasuryFundraiserSetButtonImage).
func (h *Handlers) FundraiserStorefrontSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if _, ok := h.requireSuperAdmin(w, r, "/admin/settings"); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	enabled := r.FormValue("enabled") == "on"
	if err := ledger.SetStorefrontFundraiser(r.Context(), h.Pool, unit.ID, r.FormValue("fundraiser_id"), enabled); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
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

// UnitSettingsUpdateText saves every per-unit Payments text setting (the
// Stripe/PayPal credential fields — see settings.UnitTextSettings) from
// one combined Payments-section form submission, mirroring
// SystemSettingsUpdateText's shape. A Secret field left blank is a no-op
// (see settings.SetUnitText) rather than clearing it, so resubmitting the
// form to change one credential never wipes another the admin didn't
// retype. Only saves Section == "payments" — the Social Media section has
// its own form and its own handler (SocialSettingsUpdateText) below, so
// submitting one section's form never blanks out a field that belongs to
// the other (which isn't present in that <form> at all).
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
		if t.Section != "payments" {
			continue
		}
		if err := settings.SetUnitText(r.Context(), h.Pool, unit.ID, t.Key, r.FormValue(t.Key), actor.ID); err != nil {
			log.Printf("web: updating unit text setting %q: %v", t.Key, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// SocialSettingsUpdateText is UnitSettingsUpdateText's Social
// Media-section sibling — saves only the Facebook/Instagram/TikTok URL
// fields (Section == "social") from their own form, so this section and
// Payments never clobber each other.
func (h *Handlers) SocialSettingsUpdateText(w http.ResponseWriter, r *http.Request) {
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
		if t.Section != "social" {
			continue
		}
		if err := settings.SetUnitText(r.Context(), h.Pool, unit.ID, t.Key, r.FormValue(t.Key), actor.ID); err != nil {
			log.Printf("web: updating social setting %q: %v", t.Key, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}

// WelcomeEmailSettingsUpdateText is UnitSettingsUpdateText's Welcome
// Email-section sibling — saves only the subject/body template fields
// (Section == "welcome_email") from their own form. See
// Handlers.sendWelcomeEmail (welcome_email.go) for where these are
// actually used.
func (h *Handlers) WelcomeEmailSettingsUpdateText(w http.ResponseWriter, r *http.Request) {
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
		if t.Section != "welcome_email" {
			continue
		}
		if err := settings.SetUnitText(r.Context(), h.Pool, unit.ID, t.Key, r.FormValue(t.Key), actor.ID); err != nil {
			log.Printf("web: updating welcome email setting %q: %v", t.Key, err)
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
