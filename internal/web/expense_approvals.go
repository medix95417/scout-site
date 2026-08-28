package web

// This file is the segregation-of-duties half of the Treasury: the page
// where a unit's top leader authorizes spending a Treasurer has entered.
//
// It deliberately lives outside /treasury, which is gated on
// units.CanManageLedger. A Cubmaster or Scoutmaster doesn't hold that —
// and shouldn't, since the whole point of the control is that the person
// who can spend the money isn't the person who signs it off. So this is
// its own route with its own gate (units.CanApproveExpenses), reachable
// by someone who can't see the rest of the Treasury at all.

import (
	"errors"
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/approval"
	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/ledger"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// requireExpenseApprover is the shared preamble for the two handlers
// below: logged in, Treasury on for this unit, and holding
// units.CapApproveExpenses.
func (h *Handlers) requireExpenseApprover(w http.ResponseWriter, r *http.Request) (unit units.Unit, actor family.Member, ok bool) {
	unit, _ = units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/expense-approvals", http.StatusSeeOther)
		return unit, family.Member{}, false
	}
	if !h.requireTreasuryEnabled(w, r, unit.ID) {
		return unit, family.Member{}, false
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanApproveExpenses(caps) {
		http.Error(w, "only the "+topLeaderTitle(unit.UnitType)+" (or an Admin) can authorize spending", http.StatusForbidden)
		return unit, family.Member{}, false
	}

	actor, err = h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return unit, family.Member{}, false
	}
	return unit, actor, true
}

// ExpenseApprovalsList shows every expense waiting on this leader.
func (h *Handlers) ExpenseApprovalsList(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireExpenseApprover(w, r)
	if !ok {
		return
	}

	pending, err := ledger.PendingExpensesForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading pending expenses: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views, err := h.decorateTransactions(r.Context(), pending)
	if err != nil {
		log.Printf("web: decorating pending expenses: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	threshold, err := settings.ExpenseApprovalThresholdCents(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: reading expense approval threshold: %v", err)
	}

	// An expense is shown as decidable only to someone who didn't enter
	// it. The real check is in ExpenseApprovalDecide — this just avoids
	// offering a button that would be refused.
	type pendingExpenseView struct {
		transactionView
		AmountDisplay string
		OwnEntry      bool
	}
	rows := make([]pendingExpenseView, 0, len(views))
	for _, v := range views {
		// The expense's own debit is the negative posting; show it as a
		// positive amount, which is how a person thinks about a bill.
		var amount int64
		for _, p := range v.Postings {
			if p.AmountCents < 0 {
				amount += -p.AmountCents
			}
		}
		rows = append(rows, pendingExpenseView{
			transactionView: v,
			AmountDisplay:   formatCents(amount),
			OwnEntry:        v.CreatedBy == actor.ID,
		})
	}

	data := struct {
		baseData
		Pending          []pendingExpenseView
		ThresholdDisplay string
		LeaderTitle      string
	}{
		baseData:         h.base(r, "Authorize Spending"),
		Pending:          rows,
		ThresholdDisplay: formatCents(threshold),
		LeaderTitle:      topLeaderTitle(unit.UnitType),
	}
	h.render(w, h.expenseApprovals, data)
}

// ExpenseApprovalDecide authorizes or declines one pending expense.
func (h *Handlers) ExpenseApprovalDecide(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireExpenseApprover(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	requestID := r.PathValue("id")

	// The control only means something if the approver isn't the person
	// who entered it. Checked here, against the stored submitter, rather
	// than relying on the list page having hidden the button.
	var submittedBy string
	if err := h.Pool.QueryRow(r.Context(),
		`SELECT submitted_by::text FROM approval_requests WHERE id = $1 AND unit_id = $2 AND status = 'pending'`,
		requestID, unit.ID,
	).Scan(&submittedBy); err != nil {
		http.Error(w, "that request doesn't exist, or was already decided", http.StatusNotFound)
		return
	}
	if submittedBy == actor.ID {
		http.Error(w, "you entered this expense, so someone else has to authorize it — that separation is the point of the check", http.StatusForbidden)
		return
	}

	approve := r.FormValue("decision") == "approve"
	if err := approval.Decide(r.Context(), h.Pool, requestID, unit.ID, actor.ID, approve); err != nil {
		switch {
		case errors.Is(err, approval.ErrNotFound):
			http.Error(w, "that request doesn't exist, or was already decided", http.StatusNotFound)
		case errors.Is(err, approval.ErrInsufficientFunds):
			http.Error(w, "that account doesn't have enough to cover this expense any more — decline it, or ask the Treasurer to re-enter it once the money is in", http.StatusBadRequest)
		case errors.Is(err, approval.ErrAccountClosed):
			http.Error(w, "an account this expense uses has since been closed", http.StatusBadRequest)
		default:
			log.Printf("web: deciding expense approval: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/expense-approvals", http.StatusSeeOther)
}
