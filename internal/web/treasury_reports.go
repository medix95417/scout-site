package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/ledger"
)

// This file is /treasury/reports — the Treasurer-only accounting reports
// area (item 4 of the printing/reporting request): a separate page from
// the treasury dashboard, with report-specific filter forms, an on-screen
// view, a PDF export, and named report presets any Treasurer/super_admin
// in the unit can save/re-run (see migration 0024 and
// internal/ledger/reports.go for why those presets are unit-wide, not
// per-person). Four report types are supported for now — the ones that
// mapped cleanly onto data this ledger already tracks:
//   - Income & Expense Summary: derived from postings to the unit's
//     external contra-account (see ledger.PeriodTotals) — the exact
//     definition of "real money crossed the ledger's boundary" in this
//     double-entry design, with This Period/Prior Month/Year-to-Date
//     columns.
//   - Account Balances: every account and its current balance — this
//     ledger's closest equivalent to a nonprofit's statement of
//     financial position, since there's no accrual accounting here.
//   - Transaction Detail (General Ledger): every posting in range, one
//     row per account touched, optionally narrowed to specific
//     account(s) and/or transaction type(s) — the "all items in the
//     ledger should be selectable and narrowed" requirement.
//   - Fundraiser Proceeds Summary: gross vs. credited proceeds per
//     fundraiser in range.
//
// Deliberately not built yet: per-account/per-ledger budgets and a
// budget-vs-actual column — flagged by the request itself as future
// work, so nothing here assumes a particular budget shape ahead of that
// conversation.

// reportTypeLabels is every report type /treasury/reports supports, in
// display order — also doubles as the "is this a real report type" set.
var reportTypeLabels = []struct{ Type, Label string }{
	{"income_expense", "Income & Expense Summary"},
	{"account_balances", "Account Balances"},
	{"scout_accounts", "Scout Accounts"},
	{"transaction_detail", "Transaction Detail (General Ledger)"},
	{"fundraiser_proceeds", "Fundraiser Proceeds Summary"},
}

func reportLabel(reportType string) string {
	for _, t := range reportTypeLabels {
		if t.Type == reportType {
			return t.Label
		}
	}
	return ""
}

// ledgerTransactionTypes are the conventional transaction_type values
// used everywhere else in this codebase (see internal/ledger's
// Transaction.TransactionType doc comment) — transaction_type is free
// text, not a database enum, so this is the fixed set the Transaction
// Detail report's filter checkboxes offer rather than trying to
// discover distinct values live.
var ledgerTransactionTypes = []string{"deposit", "expense", "manual_adjustment", "fundraiser_allocation", "trip_fund_transfer"}

// reportRequest is one report's filter selections, parsed straight from
// the query string — the same shape whether it came from a form
// submission or a saved-report redirect.
type reportRequest struct {
	Type             string
	From, To         time.Time
	AccountIDs       []string
	TransactionTypes []string
	FundraiserIDs    []string
}

func parseReportRequest(r *http.Request) reportRequest {
	from, to := parseDateRangeParam(r)
	return reportRequest{
		Type:             r.URL.Query().Get("type"),
		From:             from,
		To:               to,
		AccountIDs:       r.URL.Query()["account_id"],
		TransactionTypes: r.URL.Query()["transaction_type"],
		FundraiserIDs:    r.URL.Query()["fundraiser_id"],
	}
}

// periodColumn is one column of the Income & Expense Summary report.
type periodColumn struct {
	Label   string
	Income  string
	Expense string
	Net     string
}

func toPeriodColumn(label string, t ledger.PeriodTotals) periodColumn {
	return periodColumn{Label: label, Income: formatCents(t.IncomeCents), Expense: formatCents(t.ExpenseCents), Net: formatCents(t.NetCents())}
}

// priorCalendarMonth returns the full calendar month immediately before
// t's own month — the Income & Expense Summary's "Prior Month" column.
func priorCalendarMonth(t time.Time) (start, end time.Time) {
	firstOfThisMonth := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	start = firstOfThisMonth.AddDate(0, -1, 0)
	end = firstOfThisMonth.Add(-time.Nanosecond)
	return start, end
}

// yearToDate returns January 1st of t's year through t itself — the
// Income & Expense Summary's "Year to Date" column.
func yearToDate(t time.Time) (start, end time.Time) {
	return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location()), t
}

// accountBalanceRow decorates an account with its display-formatted
// balance, for the Account Balances report.
type accountBalanceRow struct {
	ledger.AccountWithBalance
	BalanceDisplay string
}

// ledgerDetailRow is one posting on the Transaction Detail (General
// Ledger) report — one row per account a transaction touched, in
// standard debit/credit form, rather than one row per transaction (a
// transaction can touch several accounts with different signs, so
// there's no single "the amount" to show for it as a whole).
type ledgerDetailRow struct {
	Date        string
	AccountName string
	Description string
	Type        string
	Debit       string // "" if this posting is a credit
	Credit      string // "" if this posting is a debit
}

// fundraiserProceedsRowView decorates ledger.FundraiserProceedsRow with
// display-formatted amounts.
type fundraiserProceedsRowView struct {
	Name       string
	ScoutCount int
	Gross      string
	Credited   string
}

// reportViewData is what both the on-screen report page and its PDF
// export render — exactly one of the type-specific fields is populated,
// matching Type.
type reportViewData struct {
	Type           string
	Label          string
	DateRangeLabel string

	Periods            []periodColumn              // income_expense
	Accounts           []accountBalanceRow         // account_balances
	ScoutAccounts      []accountBalanceRow         // scout_accounts
	ScoutAccountsTotal string                      // scout_accounts
	LedgerRows         []ledgerDetailRow           // transaction_detail
	Fundraisers        []fundraiserProceedsRowView // fundraiser_proceeds
}

// buildReport loads and formats one report — the shared logic behind
// TreasuryReportView (HTML) and TreasuryReportExportPDF (PDF), so the two
// can never show different numbers for the same filters.
func (h *Handlers) buildReport(ctx context.Context, unitID string, req reportRequest) (reportViewData, error) {
	view := reportViewData{
		Type:           req.Type,
		Label:          reportLabel(req.Type),
		DateRangeLabel: dateRangeLabel(req.From, req.To),
	}

	switch req.Type {
	case "income_expense":
		thisPeriod, err := ledger.IncomeExpenseTotals(ctx, h.Pool, unitID, req.From, req.To)
		if err != nil {
			return view, err
		}
		pmStart, pmEnd := priorCalendarMonth(req.To)
		priorMonth, err := ledger.IncomeExpenseTotals(ctx, h.Pool, unitID, pmStart, pmEnd)
		if err != nil {
			return view, err
		}
		ytdStart, ytdEnd := yearToDate(req.To)
		ytd, err := ledger.IncomeExpenseTotals(ctx, h.Pool, unitID, ytdStart, ytdEnd)
		if err != nil {
			return view, err
		}
		view.Periods = []periodColumn{
			toPeriodColumn("This Period ("+dateRangeLabel(req.From, req.To)+")", thisPeriod),
			toPeriodColumn("Prior Month ("+pmStart.Format("January 2006")+")", priorMonth),
			toPeriodColumn("Year to Date ("+ytdStart.Format("Jan 2, 2006")+" – "+ytdEnd.Format("Jan 2, 2006")+")", ytd),
		}

	case "account_balances":
		accounts, err := ledger.ListAccountsForUnit(ctx, h.Pool, unitID)
		if err != nil {
			return view, err
		}
		for _, a := range accounts {
			view.Accounts = append(view.Accounts, accountBalanceRow{AccountWithBalance: a, BalanceDisplay: formatCents(a.BalanceCents)})
		}

	case "scout_accounts":
		accounts, err := ledger.ListAccountsForUnit(ctx, h.Pool, unitID)
		if err != nil {
			return view, err
		}
		var total int64
		for _, a := range accounts {
			if a.AccountType != "scout_individual" {
				continue
			}
			view.ScoutAccounts = append(view.ScoutAccounts, accountBalanceRow{AccountWithBalance: a, BalanceDisplay: formatCents(a.BalanceCents)})
			total += a.BalanceCents
		}
		view.ScoutAccountsTotal = formatCents(total)

	case "transaction_detail":
		txs, err := ledger.TransactionsForUnitFiltered(ctx, h.Pool, unitID, req.From, req.To, req.AccountIDs, req.TransactionTypes)
		if err != nil {
			return view, err
		}
		wantAccounts := make(map[string]bool, len(req.AccountIDs))
		for _, id := range req.AccountIDs {
			wantAccounts[id] = true
		}
		for _, t := range txs {
			for _, p := range t.Postings {
				if len(wantAccounts) > 0 && !wantAccounts[p.AccountID] {
					continue
				}
				row := ledgerDetailRow{
					Date:        t.OccurredAt.Format("Jan 2, 2006"),
					AccountName: p.AccountName,
					Description: t.Description,
					Type:        t.TransactionType,
				}
				if p.AmountCents < 0 {
					row.Debit = formatCents(-p.AmountCents)
				} else {
					row.Credit = formatCents(p.AmountCents)
				}
				view.LedgerRows = append(view.LedgerRows, row)
			}
		}

	case "fundraiser_proceeds":
		rows, err := ledger.FundraiserProceedsForUnit(ctx, h.Pool, unitID, req.From, req.To)
		if err != nil {
			return view, err
		}
		wantFundraisers := make(map[string]bool, len(req.FundraiserIDs))
		for _, id := range req.FundraiserIDs {
			wantFundraisers[id] = true
		}
		for _, row := range rows {
			if len(wantFundraisers) > 0 && !wantFundraisers[row.FundraiserID] {
				continue
			}
			view.Fundraisers = append(view.Fundraisers, fundraiserProceedsRowView{
				Name: row.FundraiserName, ScoutCount: row.ScoutCount,
				Gross: formatCents(row.GrossCents), Credited: formatCents(row.CreditedCents),
			})
		}
	}
	return view, nil
}

// TreasuryReportsList is /treasury/reports — a card per report type plus
// every saved preset for this unit.
func (h *Handlers) TreasuryReportsList(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireTreasurer(w, r, "/treasury/reports")
	if !ok {
		return
	}

	saved, err := ledger.ListSavedReportsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading saved reports: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	accounts, err := ledger.ListAccountsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading accounts: %v", err)
	}
	fundraisers, err := ledger.ListFundraisersForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading fundraisers: %v", err)
	}

	data := struct {
		baseData
		ReportTypes      []struct{ Type, Label string }
		SavedReports     []ledger.SavedReport
		Accounts         []ledger.AccountWithBalance
		Fundraisers      []ledger.Fundraiser
		TransactionTypes []string
	}{
		baseData:         h.base(r, "Reports"),
		ReportTypes:      reportTypeLabels,
		SavedReports:     saved,
		Accounts:         accounts,
		Fundraisers:      fundraisers,
		TransactionTypes: ledgerTransactionTypes,
	}
	h.render(w, h.treasuryReports, data)
}

// TreasuryReportView renders one report on-screen for the given filters.
func (h *Handlers) TreasuryReportView(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireTreasurer(w, r, "/treasury/reports")
	if !ok {
		return
	}

	req := parseReportRequest(r)
	if reportLabel(req.Type) == "" {
		http.Error(w, "unknown report type", http.StatusBadRequest)
		return
	}

	view, err := h.buildReport(r.Context(), unit.ID, req)
	if err != nil {
		log.Printf("web: building report: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := struct {
		baseData
		Report      reportViewData
		QueryValues url.Values
		RawQuery    string
	}{
		baseData:    h.base(r, view.Label),
		Report:      view,
		QueryValues: r.URL.Query(),
		RawQuery:    r.URL.RawQuery,
	}
	h.render(w, h.treasuryReportView, data)
}

// TreasuryReportExportPDF is TreasuryReportView's downloadable sibling —
// identical filters, identical numbers, rendered as a PDF instead of an
// HTML page.
func (h *Handlers) TreasuryReportExportPDF(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireTreasurer(w, r, "/treasury/reports")
	if !ok {
		return
	}

	req := parseReportRequest(r)
	if reportLabel(req.Type) == "" {
		http.Error(w, "unknown report type", http.StatusBadRequest)
		return
	}

	view, err := h.buildReport(r.Context(), unit.ID, req)
	if err != nil {
		log.Printf("web: building report: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data, err := reportPDF(unit.Name+" — "+view.Label, view)
	if err != nil {
		log.Printf("web: rendering report PDF: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writePDF(w, req.Type+".pdf", data)
}

// TreasuryReportSave stores the exact filters of the report currently
// being viewed as a named, unit-wide preset (see ledger.SavedReport) —
// the "Save this report" form posts the same query string the view page
// itself was rendered with, plus a name.
func (h *Handlers) TreasuryReportSave(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, "/treasury/reports")
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "a name is required to save a report", http.StatusBadRequest)
		return
	}
	reportType := r.FormValue("type")
	if reportLabel(reportType) == "" {
		http.Error(w, "unknown report type", http.StatusBadRequest)
		return
	}

	filters := map[string][]string{}
	for key, values := range r.Form {
		if key == "csrf_token" || key == "name" {
			continue
		}
		filters[key] = values
	}
	filtersJSON, err := json.Marshal(filters)
	if err != nil {
		log.Printf("web: encoding saved report filters: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if _, err := ledger.CreateSavedReport(r.Context(), h.Pool, unit.ID, name, reportType, string(filtersJSON), actor.ID); err != nil {
		log.Printf("web: saving report: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/treasury/reports", http.StatusSeeOther)
}

// TreasuryReportRunSaved redirects to the report view page with a saved
// preset's exact filters restored. Running a saved report always
// recomputes against current data — nothing about the numbers themselves
// is stored, just which filters produced them (see migration 0024).
func (h *Handlers) TreasuryReportRunSaved(w http.ResponseWriter, r *http.Request) {
	unit, _, ok := h.requireTreasurer(w, r, "/treasury/reports")
	if !ok {
		return
	}

	id := r.PathValue("id")
	saved, found, err := ledger.GetSavedReport(r.Context(), h.Pool, id, unit.ID)
	if err != nil {
		log.Printf("web: loading saved report: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	var filters map[string][]string
	if err := json.Unmarshal([]byte(saved.Filters), &filters); err != nil {
		log.Printf("web: decoding saved report filters: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/treasury/reports/view?"+url.Values(filters).Encode(), http.StatusSeeOther)
}

// TreasuryReportDeleteSaved removes one saved report preset.
func (h *Handlers) TreasuryReportDeleteSaved(w http.ResponseWriter, r *http.Request) {
	unit, actor, ok := h.requireTreasurer(w, r, "/treasury/reports")
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := ledger.DeleteSavedReport(r.Context(), h.Pool, id, unit.ID, actor.ID); err != nil {
		log.Printf("web: deleting saved report: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/treasury/reports", http.StatusSeeOther)
}
