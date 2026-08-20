package web

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/47-yonkers/scout-site/internal/approval"
	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/ledger"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file is Phase 2's fund-accounting UI — internal/ledger's business
// logic wired up to HTTP. Most routes here are Treasurer/super_admin only
// (units.CanManageLedger); the two exceptions are TreasuryAccountView and
// TreasuryRequestTransfer, which the owning login can also reach for a
// Scout's individual account (see isAccountOwner in web.go, which checks
// exact-member ownership for an individual Scout login or broader family
// membership for the family-wide login) so they can check a balance and
// push money into a trip fund without needing Treasurer access
// themselves. See also /accounts (accounts.go) — a lighter-weight,
// non-Treasurer page listing every account a login can see, since this
// dashboard's own account table is Treasurer-only.

// transactionView decorates a ledger.TransactionDetail with the acting
// member's display name — display-only concern, kept in the web package
// rather than internal/ledger, same pattern Phase 1 used for
// pendingApprovalView.
type transactionView struct {
	ledger.TransactionDetail
	CreatedByName string
}

func (h *Handlers) decorateTransactions(ctx context.Context, txs []ledger.TransactionDetail) ([]transactionView, error) {
	ids := make([]string, 0, len(txs))
	seen := map[string]bool{}
	for _, t := range txs {
		if t.CreatedBy != "" && !seen[t.CreatedBy] {
			ids = append(ids, t.CreatedBy)
			seen[t.CreatedBy] = true
		}
	}
	names, err := h.memberNames(ctx, ids)
	if err != nil {
		return nil, err
	}
	views := make([]transactionView, 0, len(txs))
	for _, t := range txs {
		views = append(views, transactionView{TransactionDetail: t, CreatedByName: names[t.CreatedBy]})
	}
	return views, nil
}

func (h *Handlers) memberNames(ctx context.Context, memberIDs []string) (map[string]string, error) {
	names := map[string]string{}
	if len(memberIDs) == 0 {
		return names, nil
	}
	rows, err := h.Pool.Query(ctx, `SELECT id, first_name || ' ' || last_name FROM members WHERE id = ANY($1)`, memberIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

// parseDollarsToCents parses a user-typed dollar amount ("12.34") into
// integer cents. Money is never handled as a floating-point type once
// it's in the database (see internal/ledger's package comment) — this is
// the one, deliberate boundary where a float briefly exists, right at
// form-parsing, before immediately rounding to an exact integer.
func parseDollarsToCents(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("amount is required")
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid amount", raw)
	}
	return int64(math.Round(f * 100)), nil
}

// formatCents renders integer cents as a "$12.34"-style string for
// templates (Go templates can't do arithmetic/formatting like this
// inline, so it's precomputed here rather than in every template).
func formatCents(cents int64) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	s := fmt.Sprintf("$%d.%02d", cents/100, cents%100)
	if neg {
		return "-" + s
	}
	return s
}

// requireTreasurer resolves the current unit/user and checks
// units.CanManageLedger, writing an HTTP error/redirect and returning
// ok=false if the request should stop here. Shared preamble for every
// Treasurer-only handler below.
func (h *Handlers) requireTreasurer(w http.ResponseWriter, r *http.Request, redirectPath string) (unit units.Unit, actor family.Member, ok bool) {
	unit, _ = units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next="+redirectPath, http.StatusSeeOther)
		return unit, family.Member{}, false
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanManageLedger(caps) {
		http.Error(w, "you don't have permission to manage the treasury", http.StatusForbidden)
		return unit, family.Member{}, false
	}

	actor, err = h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return unit, family.Member{}, false
	}
	return unit, actor, true
}

// --- Dashboard -----------------------------------------------------------

func (h *Handlers) TreasuryDashboard(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, "/treasury")
	if !ok {
		return
	}

	// Ensure the two "always exist" accounts are there so a brand-new
	// unit's dashboard has something to show and something to post
	// deposits/expenses against right away.
	if _, err := ledger.EnsureUnitGeneralAccount(r.Context(), h.Pool, unit.ID, actor.ID); err != nil {
		log.Printf("web: ensuring unit general account: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := ledger.EnsureExternalAccount(r.Context(), h.Pool, unit.ID, actor.ID); err != nil {
		log.Printf("web: ensuring unit external account: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	accounts, err := ledger.ListAccountsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing treasury accounts: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	pendingRaw, err := ledger.PendingTransfersForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing pending transfers: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	pending, err := h.decorateTransactions(r.Context(), pendingRaw)
	if err != nil {
		log.Printf("web: decorating pending transfers: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	recentRaw, err := ledger.TransactionsForUnit(r.Context(), h.Pool, unit.ID, 30)
	if err != nil {
		log.Printf("web: listing recent transactions: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	recent, err := h.decorateTransactions(r.Context(), recentRaw)
	if err != nil {
		log.Printf("web: decorating recent transactions: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	fundraisers, err := ledger.ListFundraisersForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing fundraisers: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// For the "create a trip fund" form's event picker. Doesn't filter out
	// events that already have a trip fund — CreateTripFund's unique
	// constraint (and ledger.ErrTripFundExists) catches that if someone
	// picks one anyway, which is simpler than cross-referencing here too.
	upcomingEvents, err := calendar.ListUpcomingForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading upcoming events for treasury: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type accountView struct {
		ledger.AccountWithBalance
		BalanceDisplay string
	}
	var accountViews []accountView
	var totalHeld int64
	for _, a := range accounts {
		if a.AccountType != "external" {
			totalHeld += a.BalanceCents
		}
		accountViews = append(accountViews, accountView{AccountWithBalance: a, BalanceDisplay: formatCents(a.BalanceCents)})
	}

	data := struct {
		baseData
		Accounts         []accountView
		Pending          []transactionView
		Recent           []transactionView
		Fundraisers      []ledger.Fundraiser
		UpcomingEvents   []calendar.Event
		TotalHeldCents   int64
		TotalHeldDisplay string
	}{
		baseData:         h.base(r, "Treasury"),
		Accounts:         accountViews,
		Pending:          pending,
		Recent:           recent,
		Fundraisers:      fundraisers,
		UpcomingEvents:   upcomingEvents,
		TotalHeldCents:   totalHeld,
		TotalHeldDisplay: formatCents(totalHeld),
	}
	h.render(w, h.treasury, data)
}

// --- Manual entries: deposit / expense / internal transfer ----------------

func (h *Handlers) TreasuryPostTransaction(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, "/treasury")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	amountCents, err := parseDollarsToCents(r.FormValue("amount"))
	if err != nil || amountCents <= 0 {
		http.Error(w, "enter a valid, positive dollar amount", http.StatusBadRequest)
		return
	}
	description := strings.TrimSpace(r.FormValue("description"))
	if description == "" {
		http.Error(w, "a description is required", http.StatusBadRequest)
		return
	}

	var postings []ledger.Posting
	var transactionType string

	switch entryType := r.FormValue("entry_type"); entryType {
	case "deposit":
		external, err := ledger.EnsureExternalAccount(r.Context(), h.Pool, unit.ID, actor.ID)
		if err != nil {
			log.Printf("web: ensuring external account: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		postings = []ledger.Posting{
			{AccountID: external.ID, AmountCents: -amountCents},
			{AccountID: r.FormValue("account_id"), AmountCents: amountCents},
		}
		transactionType = "deposit"

	case "expense":
		external, err := ledger.EnsureExternalAccount(r.Context(), h.Pool, unit.ID, actor.ID)
		if err != nil {
			log.Printf("web: ensuring external account: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		postings = []ledger.Posting{
			{AccountID: r.FormValue("account_id"), AmountCents: -amountCents},
			{AccountID: external.ID, AmountCents: amountCents},
		}
		transactionType = "expense"

	case "transfer":
		fromID, toID := r.FormValue("from_account_id"), r.FormValue("to_account_id")
		if fromID == "" || toID == "" {
			http.Error(w, "choose both a from and a to account", http.StatusBadRequest)
			return
		}
		if fromID == toID {
			http.Error(w, "the from and to accounts must be different", http.StatusBadRequest)
			return
		}
		postings = []ledger.Posting{
			{AccountID: fromID, AmountCents: -amountCents},
			{AccountID: toID, AmountCents: amountCents},
		}
		transactionType = "manual_adjustment"

	default:
		http.Error(w, "unknown entry type", http.StatusBadRequest)
		return
	}

	if _, err := ledger.PostTransaction(r.Context(), h.Pool, unit.ID, transactionType, description, actor.ID, postings); err != nil {
		writeLedgerError(w, err)
		return
	}

	http.Redirect(w, r, "/treasury", http.StatusSeeOther)
}

// writeLedgerError maps internal/ledger's sentinel errors to a reasonable
// HTTP status/message; anything else is logged and reported generically.
func writeLedgerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ledger.ErrAccountNotFound):
		http.Error(w, "one of the selected accounts doesn't belong to this unit", http.StatusBadRequest)
	case errors.Is(err, ledger.ErrAccountClosed):
		http.Error(w, "one of the selected accounts is closed", http.StatusBadRequest)
	case errors.Is(err, ledger.ErrUnbalanced):
		http.Error(w, "internal error: transaction did not balance", http.StatusInternalServerError)
	case errors.Is(err, ledger.ErrInsufficientFunds):
		http.Error(w, "insufficient balance for this transfer", http.StatusBadRequest)
	case errors.Is(err, ledger.ErrTripFundExists):
		http.Error(w, "a trip fund already exists for this event", http.StatusBadRequest)
	case errors.Is(err, ledger.ErrAccountBalanceNotZero):
		http.Error(w, "this trip fund still has a balance — move it out (refund or transfer) before closing", http.StatusBadRequest)
	case errors.Is(err, ledger.ErrNotATripFund):
		http.Error(w, "only trip funds can be closed this way", http.StatusBadRequest)
	default:
		log.Printf("web: ledger error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// --- Trip funds ------------------------------------------------------------

func (h *Handlers) TreasuryCreateTripFund(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, "/treasury")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	eventID := r.FormValue("event_id")
	name := strings.TrimSpace(r.FormValue("name"))
	if eventID == "" || name == "" {
		http.Error(w, "choose an event and enter a fund name", http.StatusBadRequest)
		return
	}

	if _, err := ledger.CreateTripFund(r.Context(), h.Pool, unit.ID, eventID, name, actor.ID); err != nil {
		writeLedgerError(w, err)
		return
	}

	http.Redirect(w, r, "/treasury", http.StatusSeeOther)
}

// TreasuryCloseTripFund closes a trip fund whose balance has already been
// zeroed out — see internal/ledger.CloseTripFund's doc comment for why
// closing requires that rather than sweeping or stranding a remainder.
func (h *Handlers) TreasuryCloseTripFund(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, "/treasury")
	if !ok {
		return
	}

	accountID := r.PathValue("id")
	if _, err := ledger.CloseTripFund(r.Context(), h.Pool, unit.ID, accountID, actor.ID); err != nil {
		writeLedgerError(w, err)
		return
	}

	http.Redirect(w, r, "/treasury/accounts/"+accountID, http.StatusSeeOther)
}

// TreasuryDecideTransfer approves or rejects a pending trip-fund push
// transfer — Treasurer-only, reusing internal/approval.Decide exactly as
// the calendar approval flow does.
func (h *Handlers) TreasuryDecideTransfer(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, "/treasury")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	requestID := r.PathValue("id")
	approve := r.FormValue("decision") == "approve"
	if err := approval.Decide(r.Context(), h.Pool, requestID, unit.ID, actor.ID, approve); err != nil {
		if errors.Is(err, approval.ErrNotFound) {
			http.Error(w, "that transfer request doesn't exist (or was already decided)", http.StatusNotFound)
			return
		}
		if errors.Is(err, approval.ErrInsufficientFunds) {
			http.Error(w, "that Scout's account no longer has enough balance to cover this transfer — reject it or ask the family to resubmit for a smaller amount", http.StatusBadRequest)
			return
		}
		log.Printf("web: deciding treasury transfer: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/treasury", http.StatusSeeOther)
}

// --- Individual accounts (Treasurer, or the owning family) ----------------

func (h *Handlers) TreasuryAccountView(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
		return
	}

	accountID := r.PathValue("id")
	account, err := ledger.GetAccount(r.Context(), h.Pool, accountID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if account.UnitID != unit.ID {
		http.NotFound(w, r)
		return
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: loading capabilities: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	canManage := units.CanManageLedger(caps)

	isOwner := false
	if account.AccountType == "scout_individual" {
		owns, err := isAccountOwner(r.Context(), h.Pool, user, account.MemberID)
		if err != nil {
			log.Printf("web: checking account ownership: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Ownership only grants access while family self-service is on for
		// this unit — otherwise accounts are treasurer-only (see
		// settings.ScoutAccountSelfService).
		if owns {
			selfService, err := h.scoutAccountSelfServiceEnabled(r.Context(), unit.ID)
			if err != nil {
				log.Printf("web: checking scout-account-self-service setting: %v", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			isOwner = selfService
		}
	}

	if !canManage && !isOwner {
		http.Error(w, "you don't have permission to view this account", http.StatusForbidden)
		return
	}

	balance, err := ledger.BalanceForAccount(r.Context(), h.Pool, accountID)
	if err != nil {
		log.Printf("web: loading account balance: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	txRaw, err := ledger.TransactionsForAccount(r.Context(), h.Pool, accountID, 50)
	if err != nil {
		log.Printf("web: loading account statement: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	transactions, err := h.decorateTransactions(r.Context(), txRaw)
	if err != nil {
		log.Printf("web: decorating account statement: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var openTripFunds []ledger.AccountWithBalance
	if account.AccountType == "scout_individual" {
		all, err := ledger.ListAccountsForUnit(r.Context(), h.Pool, unit.ID)
		if err != nil {
			log.Printf("web: listing accounts: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, a := range all {
			if a.AccountType == "trip_fund" && a.Status == "open" {
				openTripFunds = append(openTripFunds, a)
			}
		}
	}

	data := struct {
		baseData
		Account            ledger.Account
		BalanceDisplay     string
		Transactions       []transactionView
		CanRequestTransfer bool
		OpenTripFunds      []ledger.AccountWithBalance
		CanCloseTripFund   bool
		TripFundHasBalance bool
	}{
		baseData:           h.base(r, account.Name),
		Account:            account,
		BalanceDisplay:     formatCents(balance),
		Transactions:       transactions,
		CanRequestTransfer: account.AccountType == "scout_individual" && (isOwner || canManage),
		OpenTripFunds:      openTripFunds,
		CanCloseTripFund:   canManage && account.AccountType == "trip_fund" && account.Status == "open" && balance == 0,
		TripFundHasBalance: canManage && account.AccountType == "trip_fund" && account.Status == "open" && balance != 0,
	}
	h.render(w, h.treasuryAccount, data)
}

// TreasuryRequestTransfer submits a "push" transfer from a Scout's
// individual account into an open trip fund for Treasurer sign-off — see
// internal/ledger.RequestTripFundTransfer.
func (h *Handlers) TreasuryRequestTransfer(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	accountID := r.PathValue("id")
	account, err := ledger.GetAccount(r.Context(), h.Pool, accountID)
	if err != nil || account.UnitID != unit.ID || account.AccountType != "scout_individual" {
		http.NotFound(w, r)
		return
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil {
		log.Printf("web: loading capabilities: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	owns, err := isAccountOwner(r.Context(), h.Pool, user, account.MemberID)
	if err != nil {
		log.Printf("web: checking account ownership: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Ownership only grants a self-service transfer while family
	// self-service is on for this unit (see settings.ScoutAccountSelfService);
	// otherwise only a treasurer can move money from an account.
	if owns {
		selfService, err := h.scoutAccountSelfServiceEnabled(r.Context(), unit.ID)
		if err != nil {
			log.Printf("web: checking scout-account-self-service setting: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		owns = selfService
	}
	if !owns && !units.CanManageLedger(caps) {
		http.Error(w, "you don't have permission to move money from this account", http.StatusForbidden)
		return
	}

	tripFundID := r.FormValue("trip_fund_id")
	tripFund, err := ledger.GetAccount(r.Context(), h.Pool, tripFundID)
	if err != nil || tripFund.UnitID != unit.ID || tripFund.AccountType != "trip_fund" || tripFund.Status != "open" {
		http.Error(w, "choose a valid, open trip fund", http.StatusBadRequest)
		return
	}

	amountCents, err := parseDollarsToCents(r.FormValue("amount"))
	if err != nil || amountCents <= 0 {
		http.Error(w, "enter a valid, positive dollar amount", http.StatusBadRequest)
		return
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member", http.StatusBadRequest)
		return
	}

	description := fmt.Sprintf("Push transfer to %s", tripFund.Name)
	if _, _, err := ledger.RequestTripFundTransfer(r.Context(), h.Pool, unit.ID, accountID, tripFundID, amountCents, description, actor.ID); err != nil {
		writeLedgerError(w, err)
		return
	}

	http.Redirect(w, r, "/treasury/accounts/"+accountID, http.StatusSeeOther)
}

// --- Fundraisers -----------------------------------------------------------

func (h *Handlers) TreasuryCreateFundraiser(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, "/treasury")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	mode := r.FormValue("allocation_mode")
	if name == "" || (mode != "percentage" && mode != "fixed_per_item") {
		http.Error(w, "enter a name and choose an allocation mode", http.StatusBadRequest)
		return
	}

	var percent string
	var fixedCents int64
	if mode == "percentage" {
		percent = strings.TrimSpace(r.FormValue("allocation_percent"))
		if _, err := strconv.ParseFloat(percent, 64); err != nil {
			http.Error(w, "enter a valid percentage", http.StatusBadRequest)
			return
		}
	} else {
		cents, err := parseDollarsToCents(r.FormValue("allocation_fixed_amount"))
		if err != nil || cents <= 0 {
			http.Error(w, "enter a valid, positive per-item amount", http.StatusBadRequest)
			return
		}
		fixedCents = cents
	}

	f, err := ledger.CreateFundraiser(r.Context(), h.Pool, unit.ID, name, mode, percent, fixedCents, actor.ID)
	if err != nil {
		log.Printf("web: creating fundraiser: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/treasury/fundraisers/"+f.ID, http.StatusSeeOther)
}

func (h *Handlers) TreasuryFundraiserView(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireTreasurer(w, r, r.URL.Path)
	if !ok {
		return
	}
	h.renderFundraiser(w, r, unit, r.PathValue("id"), nil)
}

// renderFundraiser loads a fundraiser's full view (allocations, roster
// Scouts for the single-row form) and renders it — shared by the plain GET
// view and TreasuryAllocateFundraiserBulk, so a bulk paste's results page
// is just this same view with a BulkImportResult attached, already
// reflecting whatever rows the import actually credited.
func (h *Handlers) renderFundraiser(w http.ResponseWriter, r *http.Request, unit units.Unit, fundraiserID string, bulkResult *bulkImportResult) {
	f, err := ledger.GetFundraiser(r.Context(), h.Pool, fundraiserID)
	if err != nil || f.UnitID != unit.ID {
		http.NotFound(w, r)
		return
	}

	allocations, err := ledger.AllocationsForFundraiser(r.Context(), h.Pool, fundraiserID)
	if err != nil {
		log.Printf("web: loading fundraiser allocations: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	memberIDs := make([]string, 0, len(allocations))
	for _, a := range allocations {
		memberIDs = append(memberIDs, a.MemberID)
	}
	names, err := h.memberNames(r.Context(), memberIDs)
	if err != nil {
		log.Printf("web: loading member names: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	type allocationView struct {
		ledger.FundraiserAllocation
		MemberName         string
		GrossAmountDisplay string
		CreditedDisplay    string
	}
	var allocationViews []allocationView
	for _, a := range allocations {
		allocationViews = append(allocationViews, allocationView{
			FundraiserAllocation: a,
			MemberName:           names[a.MemberID],
			GrossAmountDisplay:   formatCents(a.GrossAmountCents),
			CreditedDisplay:      formatCents(a.CreditedCents),
		})
	}

	roster, err := family.RosterForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading roster: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var scouts []family.RosterEntry
	for _, m := range roster {
		if m.MemberType == "youth" {
			scouts = append(scouts, m)
		}
	}

	data := struct {
		baseData
		Fundraiser  ledger.Fundraiser
		Allocations []allocationView
		Scouts      []family.RosterEntry
		BulkResult  *bulkImportResult
	}{
		baseData:    h.base(r, f.Name),
		Fundraiser:  f,
		Allocations: allocationViews,
		Scouts:      scouts,
		BulkResult:  bulkResult,
	}
	h.render(w, h.treasuryFundraiser, data)
}

func (h *Handlers) TreasuryAllocateFundraiser(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, r.URL.Path)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	fundraiserID := r.PathValue("id")
	f, err := ledger.GetFundraiser(r.Context(), h.Pool, fundraiserID)
	if err != nil || f.UnitID != unit.ID {
		http.NotFound(w, r)
		return
	}

	memberID := r.FormValue("member_id")
	grossCents, err := parseDollarsToCents(r.FormValue("gross_amount"))
	if err != nil || grossCents <= 0 || memberID == "" {
		http.Error(w, "choose a Scout and enter a valid, positive gross amount", http.StatusBadRequest)
		return
	}
	quantity := strings.TrimSpace(r.FormValue("quantity"))

	names, err := h.memberNames(r.Context(), []string{memberID})
	if err != nil {
		log.Printf("web: loading member name: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	memberName, ok2 := names[memberID]
	if !ok2 {
		http.Error(w, "unknown Scout", http.StatusBadRequest)
		return
	}

	if _, err := ledger.RecordFundraiserAllocation(r.Context(), h.Pool, fundraiserID, memberID, memberName, grossCents, quantity, actor.ID); err != nil {
		if errors.Is(err, ledger.ErrAccountNotFound) || errors.Is(err, ledger.ErrAccountClosed) || errors.Is(err, ledger.ErrUnbalanced) {
			writeLedgerError(w, err)
			return
		}
		log.Printf("web: recording fundraiser allocation: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/treasury/fundraisers/"+fundraiserID, http.StatusSeeOther)
}

// bulkImportRow is one line of a pasted bulk-allocation import and its
// outcome, for the results list rendered alongside the fundraiser page —
// see TreasuryAllocateFundraiserBulk.
type bulkImportRow struct {
	Line            int
	Input           string
	Credited        bool
	MemberName      string
	CreditedDisplay string
	Reason          string // populated when Credited is false
}

type bulkImportResult struct {
	Rows      []bulkImportRow
	Succeeded int
	Skipped   int
}

// splitBulkRow splits one pasted row into fields. Pasting from a
// spreadsheet (Excel, Google Sheets) produces tab-separated rows; typing
// or pasting from an actual .csv file produces comma-separated ones — a
// row is treated as whichever it contains, tab taking precedence since a
// Scout's name is vanishingly unlikely to contain a literal tab character
// but could in principle contain a comma (e.g. a suffix like "Jr.,").
func splitBulkRow(line string) []string {
	if strings.Contains(line, "\t") {
		return strings.Split(line, "\t")
	}
	return strings.Split(line, ",")
}

// TreasuryAllocateFundraiserBulk credits many Scouts' fundraiser proceeds
// from one pasted block of rows (first name, last name, gross amount, and
// — for a fixed_per_item fundraiser — a quantity), instead of the
// one-Scout-at-a-time form above. Each row is matched against the unit's
// youth roster by exact (case-insensitive) first+last name; a row with no
// match, an ambiguous match (two Scouts share a name — resolved manually
// via the single-row form instead of guessing), or any other problem is
// skipped and reported rather than blocking the rows that are fine. An
// optional header row (its amount column isn't a valid number) is
// detected and skipped silently.
func (h *Handlers) TreasuryAllocateFundraiserBulk(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, r.URL.Path)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	fundraiserID := r.PathValue("id")
	f, err := ledger.GetFundraiser(r.Context(), h.Pool, fundraiserID)
	if err != nil || f.UnitID != unit.ID {
		http.NotFound(w, r)
		return
	}

	roster, err := family.RosterForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading roster: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	byName := map[string][]family.RosterEntry{}
	for _, m := range roster {
		if m.MemberType != "youth" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(m.FirstName) + " " + strings.TrimSpace(m.LastName))
		byName[key] = append(byName[key], m)
	}

	result := &bulkImportResult{}
	sawFirstLine := false
	lineNum := 0
	for _, rawLine := range strings.Split(r.FormValue("bulk_rows"), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" {
			continue
		}

		fields := splitBulkRow(line)
		var firstName, lastName, grossRaw, quantityRaw string
		if len(fields) > 0 {
			firstName = strings.TrimSpace(fields[0])
		}
		if len(fields) > 1 {
			lastName = strings.TrimSpace(fields[1])
		}
		if len(fields) > 2 {
			grossRaw = strings.TrimSpace(fields[2])
		}
		if len(fields) > 3 {
			quantityRaw = strings.TrimSpace(fields[3])
		}

		if !sawFirstLine {
			sawFirstLine = true
			if _, err := parseDollarsToCents(grossRaw); err != nil {
				continue // looks like a header row — skip it silently
			}
		}
		lineNum++

		if len(fields) < 3 {
			result.Rows = append(result.Rows, bulkImportRow{Line: lineNum, Input: line, Reason: "expected at least first name, last name, and gross amount"})
			result.Skipped++
			continue
		}

		grossCents, err := parseDollarsToCents(grossRaw)
		if err != nil || grossCents <= 0 {
			result.Rows = append(result.Rows, bulkImportRow{Line: lineNum, Input: line, Reason: fmt.Sprintf("%q isn't a valid, positive dollar amount", grossRaw)})
			result.Skipped++
			continue
		}

		matches := byName[strings.ToLower(firstName+" "+lastName)]
		if len(matches) == 0 {
			result.Rows = append(result.Rows, bulkImportRow{Line: lineNum, Input: line, Reason: fmt.Sprintf("no Scout on the roster named %q %q", firstName, lastName)})
			result.Skipped++
			continue
		}
		if len(matches) > 1 {
			result.Rows = append(result.Rows, bulkImportRow{Line: lineNum, Input: line, Reason: fmt.Sprintf("%d Scouts on the roster are named %q %q — enter this one manually above", len(matches), firstName, lastName)})
			result.Skipped++
			continue
		}
		member := matches[0]

		if f.AllocationMode == "fixed_per_item" && quantityRaw == "" {
			result.Rows = append(result.Rows, bulkImportRow{Line: lineNum, Input: line, Reason: "this fundraiser is fixed-per-item — a quantity column is required"})
			result.Skipped++
			continue
		}

		memberName := member.FirstName + " " + member.LastName
		alloc, err := ledger.RecordFundraiserAllocation(r.Context(), h.Pool, fundraiserID, member.ID, memberName, grossCents, quantityRaw, actor.ID)
		if err != nil {
			result.Rows = append(result.Rows, bulkImportRow{Line: lineNum, Input: line, Reason: err.Error()})
			result.Skipped++
			continue
		}

		result.Rows = append(result.Rows, bulkImportRow{
			Line: lineNum, Input: line, Credited: true,
			MemberName: memberName, CreditedDisplay: formatCents(alloc.CreditedCents),
		})
		result.Succeeded++
	}

	h.renderFundraiser(w, r, unit, fundraiserID, result)
}

func (h *Handlers) TreasuryConfirmFundraiserCap(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, r.URL.Path)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	fundraiserID := r.PathValue("id")
	f, err := ledger.GetFundraiser(r.Context(), h.Pool, fundraiserID)
	if err != nil || f.UnitID != unit.ID {
		http.NotFound(w, r)
		return
	}

	mode := r.FormValue("allocation_mode")
	var percent string
	var fixedCents int64
	if mode == "percentage" {
		percent = strings.TrimSpace(r.FormValue("allocation_percent"))
		if _, err := strconv.ParseFloat(percent, 64); err != nil {
			http.Error(w, "enter a valid percentage", http.StatusBadRequest)
			return
		}
	} else if mode == "fixed_per_item" {
		cents, err := parseDollarsToCents(r.FormValue("allocation_fixed_amount"))
		if err != nil || cents <= 0 {
			http.Error(w, "enter a valid, positive per-item amount", http.StatusBadRequest)
			return
		}
		fixedCents = cents
	} else {
		http.Error(w, "unknown allocation mode", http.StatusBadRequest)
		return
	}

	if err := ledger.ConfirmFundraiserCap(r.Context(), h.Pool, fundraiserID, mode, percent, fixedCents, actor.ID); err != nil {
		log.Printf("web: confirming fundraiser cap: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/treasury/fundraisers/"+fundraiserID, http.StatusSeeOther)
}
