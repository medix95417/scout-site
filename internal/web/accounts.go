package web

// This file is /accounts — a lightweight, every-logged-in-login page that
// answers "what's in my Scout's account?" without needing the Treasurer
// role. Phase 2's Treasury dashboard (treasury.go) already shows every
// account in the unit, but it's gated on units.CanManageLedger — a family
// with no treasury role had no discoverable way to see their own child's
// balance even though TreasuryAccountView's per-account ownership check
// (isAccountOwner, see web.go) would already have allowed it if they
// somehow knew the account's URL. This page is that missing entry point:
// - A family-wide login sees every one of its members' individual
//   accounts in the current unit (a parent checking on however many
//   Scouts they have registered here).
// - An individual member login (see auth.User.MemberID) sees only their
//   own — per the "an individual login sees just their own stuff" design
//   this feature was built around.
// Each row links to /treasury/accounts/{id} for the full statement/
// transfer-request UI, which already enforces the same ownership rule
// independently.

import (
	"log"
	"net/http"
	"strings"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/ledger"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

type accountRow struct {
	ledger.AccountWithBalance
	MemberName     string
	BalanceDisplay string
}

func (h *Handlers) AccountsView(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/accounts", http.StatusSeeOther)
		return
	}
	if !h.requireTreasuryEnabled(w, r, unit.ID) {
		return
	}

	// Family self-service view can be turned off per unit (see
	// settings.ScoutAccountSelfService). A treasurer/super_admin still
	// reaches accounts through the treasury area, so they're not blocked
	// here; everyone else gets a clear message rather than the page.
	selfService, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, settings.ScoutAccountSelfService)
	if err != nil {
		log.Printf("web: checking scout-account-self-service setting: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !selfService {
		caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
		if err != nil {
			log.Printf("web: loading capabilities: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !units.CanManageLedger(caps) {
			http.Error(w, "account balances aren't available to families for this unit — please contact your unit's treasurer", http.StatusForbidden)
			return
		}
	}

	var rows []accountRow

	if user.MemberID != nil {
		m, found, err := family.GetMember(r.Context(), h.Pool, *user.MemberID)
		if err != nil {
			log.Printf("web: loading member for /accounts: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if found {
			if a, has, err := ledger.ScoutAccountForMember(r.Context(), h.Pool, unit.ID, m.ID); err != nil {
				log.Printf("web: loading scout account: %v", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			} else if has {
				rows = append(rows, accountRow{AccountWithBalance: a, MemberName: m.FirstName + " " + m.LastName, BalanceDisplay: formatCents(a.BalanceCents)})
			}
		}
	} else {
		members, err := family.MembersForFamily(r.Context(), h.Pool, user.FamilyID)
		if err != nil {
			log.Printf("web: loading family members for /accounts: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, m := range members {
			a, has, err := ledger.ScoutAccountForMember(r.Context(), h.Pool, unit.ID, m.ID)
			if err != nil {
				log.Printf("web: loading scout account for %s: %v", m.ID, err)
				continue
			}
			if has {
				rows = append(rows, accountRow{AccountWithBalance: a, MemberName: m.FirstName + " " + m.LastName, BalanceDisplay: formatCents(a.BalanceCents)})
			}
		}
	}

	data := struct {
		baseData
		Accounts []accountRow
	}{
		baseData: h.base(r, "My Accounts"),
		Accounts: rows,
	}
	h.render(w, h.accounts, data)
}

// AccountsExportPDF is /accounts' printable sibling — a family (or an
// individual Scout login) can print their own account(s) for a date
// range, either one specific Scout or several (or the whole family) at
// once. More than one Scout selected is "sorted by Scout": each gets its
// own section/page, in the same order MembersForFamily already lists
// them in everywhere else in the app — no combined total across Scouts,
// matching how individual accounts stay separate books on-screen too.
func (h *Handlers) AccountsExportPDF(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/accounts", http.StatusSeeOther)
		return
	}
	if !h.requireTreasuryEnabled(w, r, unit.ID) {
		return
	}

	selfService, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, settings.ScoutAccountSelfService)
	if err != nil {
		log.Printf("web: checking scout-account-self-service setting: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !selfService {
		caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
		if err != nil {
			log.Printf("web: loading capabilities: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !units.CanManageLedger(caps) {
			http.Error(w, "account balances aren't available to families for this unit — please contact your unit's treasurer", http.StatusForbidden)
			return
		}
	}

	// Every member this login may see, in the app's usual family order —
	// same "just their own stuff" split AccountsView uses.
	var visibleMembers []family.Member
	if user.MemberID != nil {
		m, found, err := family.GetMember(r.Context(), h.Pool, *user.MemberID)
		if err != nil {
			log.Printf("web: loading member for /accounts/export.pdf: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if found {
			visibleMembers = []family.Member{m}
		}
	} else {
		visibleMembers, err = family.MembersForFamily(r.Context(), h.Pool, user.FamilyID)
		if err != nil {
			log.Printf("web: loading family members for /accounts/export.pdf: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// A blank/absent member_ids means "the whole family" (or just the one
	// member, for an individual login) — otherwise only the requested
	// subset, still narrowed to just members this login could already
	// see. Repeated query params (member_ids=a&member_ids=b), matching how
	// a plain HTML checkbox group submits.
	var selected map[string]bool
	if ids := r.URL.Query()["member_ids"]; len(ids) > 0 {
		selected = make(map[string]bool, len(ids))
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" {
				selected[id] = true
			}
		}
	}

	from, to := parseDateRangeParam(r)
	var sections []ledgerStatementSection
	for _, m := range visibleMembers {
		if selected != nil && !selected[m.ID] {
			continue
		}
		account, has, err := ledger.ScoutAccountForMember(r.Context(), h.Pool, unit.ID, m.ID)
		if err != nil {
			log.Printf("web: loading scout account for %s: %v", m.ID, err)
			continue
		}
		if !has {
			continue
		}
		section, err := h.buildLedgerStatementSection(r.Context(), account.ID, account.Name, from, to)
		if err != nil {
			log.Printf("web: building statement for account %s: %v", account.ID, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		sections = append(sections, section)
	}

	if len(sections) == 0 {
		http.Error(w, "no matching Scout account(s) to print", http.StatusBadRequest)
		return
	}

	data, err := ledgerStatementPDF(unit.Name+" — Scout Account Statement", sections)
	if err != nil {
		log.Printf("web: rendering accounts statement PDF: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writePDF(w, "scout-accounts.pdf", data)
}
