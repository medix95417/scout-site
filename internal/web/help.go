package web

import (
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/help"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// Help renders the in-app help, scoped to what this login may actually
// see — see internal/help for why the catalog is data rather than a
// hand-written page.
//
// Signed-out visitors get no help at all, not a reduced version: every
// topic describes something behind the login, so a public help page would
// be a list of things the reader can't reach, and several of them
// (approval thresholds, who authorizes spending, what the activity log
// records) describe the unit's internal controls to anyone who asks.
func (h *Handlers) Help(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/help", http.StatusSeeOther)
		return
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: loading capabilities for help: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Only the settings the catalog actually gates on, asked for by the
	// help package itself rather than listed here — a new gated topic
	// can't be left out of this lookup and silently show up switched-off.
	enabled := make(map[string]bool, len(help.SettingKeys()))
	for _, key := range help.SettingKeys() {
		on, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, key)
		if err != nil {
			log.Printf("web: checking setting %q for help: %v", key, err)
			// Fail closed: a setting we can't read is treated as off, so
			// help stays quiet about a feature rather than describing one
			// that might not be there.
			on = false
		}
		enabled[key] = on
	}

	sections := help.For(help.Viewer{
		Capabilities: caps,
		// user.MemberID is set only for an individual Scout's own login;
		// a shared family login leaves it nil. Same test the treasury
		// handlers use to tell the two apart.
		IsIndividualScout: user.MemberID != nil,
		Enabled:           enabled,
	})

	data := struct {
		baseData
		Sections []help.Section
	}{
		baseData: h.base(r, "Help"),
		Sections: sections,
	}
	h.render(w, h.helpPage, data)
}
