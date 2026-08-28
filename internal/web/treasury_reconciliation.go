package web

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/ledger"
)

// Bank reconciliation UI — internal/ledger's reconciliation logic wired up
// to HTTP. Treasurer/super_admin only, same as the rest of the treasury
// (see requireTreasurer), and same separation as everywhere else: the
// arithmetic and every rule about what may be ticked live in the ledger
// package, and this file only turns form values into calls and results
// into a page.

// reconciliationItemView decorates one tick-off row with the display
// strings a template can't compute for itself.
type reconciliationItemView struct {
	ledger.ReconciliationItem
	AmountDisplay string
	DateDisplay   string
}

// reconciliationView is one reconciliation's summary with its money
// figures pre-formatted, plus whether the difference runs in the
// treasurer's favor or against it — enough for the page to say what to do
// next rather than just showing a number.
type reconciliationView struct {
	ledger.ReconciliationSummary
	OpeningDisplay    string
	ClosingDisplay    string
	ClearedDisplay    string
	DifferenceDisplay string
	UnclearedDisplay  string
	StatementDisplay  string
	CompletedDisplay  string
}

func makeReconciliationView(s ledger.ReconciliationSummary) reconciliationView {
	v := reconciliationView{
		ReconciliationSummary: s,
		OpeningDisplay:        formatCents(s.OpeningBalanceCents),
		ClosingDisplay:        formatCents(s.ClosingBalanceCents),
		ClearedDisplay:        formatCents(s.ClearedBalanceCents()),
		DifferenceDisplay:     formatCents(s.DifferenceCents()),
		UnclearedDisplay:      formatCents(s.UnclearedTotal),
		StatementDisplay:      s.StatementDate.Format("January 2, 2006"),
	}
	if s.CompletedAt != nil {
		v.CompletedDisplay = s.CompletedAt.Format("January 2, 2006")
	}
	return v
}

// TreasuryReconciliations lists a unit's reconciliation history and the
// form to start the next one.
func (h *Handlers) TreasuryReconciliations(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireTreasurer(w, r, "/treasury/reconciliations")
	if !ok {
		return
	}

	history, err := ledger.ListReconciliations(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading reconciliations: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	accounts, err := ledger.ListAccountsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading accounts: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Only the unit's own funds are worth reconciling against a bank
	// statement. Individual Scout accounts and trip funds are subdivisions
	// of the same real-world money, and "external" is the offsetting side
	// of every deposit and expense rather than an account anyone banks.
	reconcilable := make([]ledger.AccountWithBalance, 0, len(accounts))
	for _, a := range accounts {
		if a.AccountType == "unit_general" && a.Status == "open" {
			reconcilable = append(reconcilable, a)
		}
	}

	type row struct {
		ledger.Reconciliation
		ClosingDisplay   string
		StatementDisplay string
		CompletedDisplay string
	}
	rows := make([]row, 0, len(history))
	openByAccount := map[string]bool{}
	for _, rec := range history {
		rw := row{
			Reconciliation:   rec,
			ClosingDisplay:   formatCents(rec.ClosingBalanceCents),
			StatementDisplay: rec.StatementDate.Format("January 2, 2006"),
		}
		if rec.CompletedAt != nil {
			rw.CompletedDisplay = rec.CompletedAt.Format("January 2, 2006")
		}
		if !rec.Completed() {
			openByAccount[rec.AccountID] = true
		}
		rows = append(rows, rw)
	}

	// An account that already has one in progress can't start another, so
	// it's dropped from the picker rather than offered and then refused.
	available := make([]ledger.AccountWithBalance, 0, len(reconcilable))
	for _, a := range reconcilable {
		if !openByAccount[a.ID] {
			available = append(available, a)
		}
	}

	data := struct {
		baseData
		Reconciliations []row
		Accounts        []ledger.AccountWithBalance
		HasOpen         bool
		Today           string
	}{
		baseData:        h.base(r, "Bank Reconciliation"),
		Reconciliations: rows,
		Accounts:        available,
		HasOpen:         len(openByAccount) > 0,
		Today:           time.Now().Format("2006-01-02"),
	}
	h.render(w, h.treasuryReconciliations, data)
}

// TreasuryStartReconciliation opens a reconciliation from the statement
// date and closing balance printed on the bank statement.
func (h *Handlers) TreasuryStartReconciliation(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, "/treasury/reconciliations")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	accountID := r.FormValue("account_id")
	if accountID == "" {
		http.Error(w, "choose an account to reconcile", http.StatusBadRequest)
		return
	}
	statementDate, err := time.Parse("2006-01-02", strings.TrimSpace(r.FormValue("statement_date")))
	if err != nil {
		http.Error(w, "enter the statement's closing date", http.StatusBadRequest)
		return
	}
	// The closing balance is the one number copied straight off the bank
	// statement, and it can legitimately be negative (an overdrawn
	// account), so it goes through the same parser as every other amount
	// rather than being restricted to positives.
	closingCents, err := parseDollarsToCents(r.FormValue("closing_balance"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rec, err := ledger.StartReconciliation(r.Context(), h.Pool, unit.ID, accountID, statementDate, closingCents, actor.ID)
	if err != nil {
		writeReconciliationError(w, err)
		return
	}
	http.Redirect(w, r, "/treasury/reconciliations/"+rec.ID, http.StatusSeeOther)
}

// TreasuryReconciliationView is the tick-off worksheet itself.
func (h *Handlers) TreasuryReconciliationView(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireTreasurer(w, r, "/treasury/reconciliations")
	if !ok {
		return
	}

	id := r.PathValue("id")
	summary, err := ledger.SummarizeReconciliation(r.Context(), h.Pool, unit.ID, id)
	if err != nil {
		writeReconciliationError(w, err)
		return
	}
	items, err := ledger.ReconciliationItems(r.Context(), h.Pool, unit.ID, id)
	if err != nil {
		writeReconciliationError(w, err)
		return
	}

	views := make([]reconciliationItemView, 0, len(items))
	for _, it := range items {
		views = append(views, reconciliationItemView{
			ReconciliationItem: it,
			AmountDisplay:      formatCents(it.AmountCents),
			DateDisplay:        it.OccurredAt.Format("Jan 2, 2006"),
		})
	}

	data := struct {
		baseData
		Rec   reconciliationView
		Items []reconciliationItemView
	}{
		baseData: h.base(r, "Bank Reconciliation"),
		Rec:      makeReconciliationView(summary),
		Items:    views,
	}
	h.render(w, h.treasuryReconciliation, data)
}

// TreasuryReconciliationToggleItem ticks or unticks one entry. The
// checkbox posts its own form (see the template) rather than one big
// save-everything form, so a half-finished worksheet is never lost and
// the running difference updates as the treasurer works down the
// statement.
func (h *Handlers) TreasuryReconciliationToggleItem(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireTreasurer(w, r, "/treasury/reconciliations")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	id := r.PathValue("id")
	cleared := r.FormValue("cleared") == "1"
	if err := ledger.SetPostingCleared(r.Context(), h.Pool, unit.ID, id, r.PathValue("postingID"), cleared); err != nil {
		writeReconciliationError(w, err)
		return
	}
	http.Redirect(w, r, "/treasury/reconciliations/"+id, http.StatusSeeOther)
}

// TreasuryCompleteReconciliation signs off a reconciliation whose
// difference has reached zero.
func (h *Handlers) TreasuryCompleteReconciliation(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, "/treasury/reconciliations")
	if !ok {
		return
	}

	id := r.PathValue("id")
	if _, err := ledger.CompleteReconciliation(r.Context(), h.Pool, unit.ID, id, actor.ID); err != nil {
		writeReconciliationError(w, err)
		return
	}
	http.Redirect(w, r, "/treasury/reconciliations/"+id, http.StatusSeeOther)
}

// TreasuryDeleteReconciliation abandons an unfinished reconciliation.
func (h *Handlers) TreasuryDeleteReconciliation(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, "/treasury/reconciliations")
	if !ok {
		return
	}

	if err := ledger.DeleteReconciliation(r.Context(), h.Pool, unit.ID, r.PathValue("id"), actor.ID); err != nil {
		writeReconciliationError(w, err)
		return
	}
	http.Redirect(w, r, "/treasury/reconciliations", http.StatusSeeOther)
}

// writeReconciliationError maps this feature's ledger errors onto status
// codes and wording a treasurer can act on, the same way writeLedgerError
// does for the rest of the treasury.
func writeReconciliationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ledger.ErrReconciliationNotFound):
		http.Error(w, "that reconciliation doesn't belong to this unit", http.StatusNotFound)
	case errors.Is(err, ledger.ErrAccountNotFound):
		http.Error(w, "that account doesn't belong to this unit", http.StatusBadRequest)
	case errors.Is(err, ledger.ErrReconciliationOpen):
		http.Error(w, "this account already has a reconciliation in progress — finish or discard that one first", http.StatusBadRequest)
	case errors.Is(err, ledger.ErrReconciliationClosed):
		http.Error(w, "this reconciliation has already been completed and can't be changed", http.StatusBadRequest)
	case errors.Is(err, ledger.ErrReconciliationNotBalanced):
		http.Error(w, "the difference isn't zero yet — the books and the statement still disagree, so this can't be signed off", http.StatusBadRequest)
	case errors.Is(err, ledger.ErrPostingNotReconcilable):
		http.Error(w, "that entry can't be cleared here — it may already have been cleared on an earlier statement, or not have posted yet", http.StatusBadRequest)
	default:
		log.Printf("web: reconciliation error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
