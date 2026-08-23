package web

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/ledger"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file is the public/member-facing half of the fundraiser
// storefront — the homepage "Buy Now" button links here. Reachable by an
// anonymous public visitor and by a logged-in leader/parent alike (one
// shared form for both, per how this was scoped): a logged-in submitter
// is simply recorded as the order's CreatedBy, everything else about
// placing the order is identical either way. See
// internal/ledger/fundraiser_storefront.go for why the ledger credit
// itself never happens here — only once a leader later marks the order
// paid.

type fundraiserItemView struct {
	ledger.FundraiserItem
	PriceDisplay string
}

func (h *Handlers) FundraiserStorefront(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if !h.requireTreasuryEnabled(w, r, unit.ID) {
		return
	}

	f, active, err := ledger.ActiveStorefrontFundraiser(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading active storefront fundraiser: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := struct {
		baseData
		Active     bool
		Fundraiser ledger.Fundraiser
		Items      []fundraiserItemView
	}{
		baseData:   h.base(r, "Fundraiser"),
		Active:     active,
		Fundraiser: f,
	}

	if active {
		items, err := ledger.ItemsForFundraiser(r.Context(), h.Pool, f.ID)
		if err != nil {
			log.Printf("web: loading storefront items: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, it := range items {
			data.Items = append(data.Items, fundraiserItemView{FundraiserItem: it, PriceDisplay: formatCents(it.PriceCents)})
		}
	}

	h.render(w, h.fundraiserStorefront, data)
}

// matchScoutByName resolves a buyer-typed name to exactly one youth
// roster member by exact, case-insensitive first+last name match —
// deliberately conservative: a public buyer's typed name is never trusted
// enough to guess through an ambiguous or partial match, so zero or
// multiple matches both leave the order unresolved for a leader to sort
// out by hand (see ledger.ResolveFundraiserOrderScout).
func matchScoutByName(roster []family.RosterEntry, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	matchID, matches := "", 0
	for _, m := range roster {
		if m.MemberType != "youth" {
			continue
		}
		full := strings.ToLower(strings.TrimSpace(m.FirstName + " " + m.LastName))
		if full == name {
			matchID = m.ID
			matches++
		}
	}
	if matches == 1 {
		return matchID
	}
	return ""
}

func (h *Handlers) FundraiserPlaceOrder(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if !h.requireTreasuryEnabled(w, r, unit.ID) {
		return
	}

	f, active, err := ledger.ActiveStorefrontFundraiser(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading active storefront fundraiser: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !active {
		http.Error(w, "no fundraiser is currently running", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	catalog, err := ledger.ItemsForFundraiser(r.Context(), h.Pool, f.ID)
	if err != nil {
		log.Printf("web: loading storefront items: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var orderItems []ledger.FundraiserOrderItem
	for _, it := range catalog {
		qty, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("qty_" + it.ID)))
		if qty > 0 {
			orderItems = append(orderItems, ledger.FundraiserOrderItem{ItemName: it.Name, UnitPriceCents: it.PriceCents, Quantity: qty})
		}
	}
	if len(orderItems) == 0 {
		http.Error(w, "choose at least one item", http.StatusBadRequest)
		return
	}

	buyerName := r.FormValue("buyer_name")
	scoutName := r.FormValue("scout_name")
	if strings.TrimSpace(buyerName) == "" || strings.TrimSpace(scoutName) == "" {
		http.Error(w, "enter your name and the Scout who should get credit", http.StatusBadRequest)
		return
	}

	var createdBy string
	if user, loggedIn := auth.UserFromContext(r.Context()); loggedIn {
		if actor, err := h.actingMember(r.Context(), user, unit.ID); err == nil {
			createdBy = actor.ID
		}
	}

	var scoutMemberID string
	if roster, err := family.RosterForUnit(r.Context(), h.Pool, unit.ID); err != nil {
		log.Printf("web: loading roster for storefront scout match: %v", err)
	} else {
		scoutMemberID = matchScoutByName(roster, scoutName)
	}

	order, err := ledger.CreateFundraiserOrder(r.Context(), h.Pool, f.ID, unit.ID, buyerName, r.FormValue("buyer_email"), r.FormValue("buyer_phone"), scoutName, scoutMemberID, orderItems, createdBy)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	type orderItemView struct {
		ledger.FundraiserOrderItem
		LineTotalDisplay string
	}
	itemViews := make([]orderItemView, 0, len(order.Items))
	for _, it := range order.Items {
		itemViews = append(itemViews, orderItemView{FundraiserOrderItem: it, LineTotalDisplay: formatCents(it.UnitPriceCents * int64(it.Quantity))})
	}

	data := struct {
		baseData
		Fundraiser   ledger.Fundraiser
		Order        ledger.FundraiserOrder
		Items        []orderItemView
		TotalDisplay string
	}{
		baseData:     h.base(r, "Thank You"),
		Fundraiser:   f,
		Order:        order,
		Items:        itemViews,
		TotalDisplay: formatCents(order.TotalCents),
	}
	h.render(w, h.fundraiserThankYou, data)
}
