package ledger

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// This file backs /treasury/reports (internal/web/treasury_reports.go) —
// Phase 2's requested accounting reports, plus a small per-unit "saved
// report" preset table so a Treasurer doesn't have to reselect the same
// filters every time.

// --- Income & Expense Summary -------------------------------------------

// PeriodTotals is a unit's income/expense totals for one date range,
// derived from postings to the unit's external contra-account (see
// migration 0006's package comment) — the exact, structural definition
// of "real money crossed the ledger's boundary" in this double-entry
// design, rather than trusting the free-text transaction_type column. A
// deposit debits external (a negative posting there), so its magnitude
// counts as income; an expense credits external (a positive posting),
// so its magnitude counts as an expense. A transfer between two accounts
// the unit already owns (a fundraiser allocation, a trip-fund push)
// never touches external at all, so it correctly never shows up here —
// money moved between books, none of it crossed in or out.
type PeriodTotals struct {
	IncomeCents  int64
	ExpenseCents int64
}

// NetCents is income minus expense for the period.
func (p PeriodTotals) NetCents() int64 { return p.IncomeCents - p.ExpenseCents }

// IncomeExpenseTotals sums a unit's posted income/expense activity (see
// PeriodTotals) for [from, to]. Returns a zero PeriodTotals, not an
// error, if the unit has no external account yet (true of a brand-new
// unit that's never recorded a deposit or expense).
func IncomeExpenseTotals(ctx context.Context, pool *pgxpool.Pool, unitID string, from, to time.Time) (PeriodTotals, error) {
	ext, err := getExternalAccount(ctx, pool, unitID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PeriodTotals{}, nil
		}
		return PeriodTotals{}, err
	}

	var income, expense int64
	err = pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN p.amount_cents < 0 THEN -p.amount_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN p.amount_cents > 0 THEN p.amount_cents ELSE 0 END), 0)
		FROM ledger_postings p
		JOIN ledger_transactions t ON t.id = p.transaction_id
		WHERE p.account_id = $1 AND t.status = 'posted' AND t.occurred_at >= $2 AND t.occurred_at <= $3
	`, ext.ID, from, to).Scan(&income, &expense)
	if err != nil {
		return PeriodTotals{}, err
	}
	return PeriodTotals{IncomeCents: income, ExpenseCents: expense}, nil
}

// --- Transaction Detail (General Ledger) --------------------------------

// TransactionsForUnitFiltered lists a unit's transactions narrowed by
// date range and, optionally, specific accounts and/or transaction
// types — the Transaction Detail / General Ledger report. A nil/empty
// accountIDs or transactionTypes means "don't filter on that dimension."
// This returns every posting of a matching transaction (both sides of a
// double-entry transaction, not just the requested account's leg) —
// the caller (internal/web/treasury_reports.go's buildReport) narrows
// the displayed rows down to just the requested account(s)' own
// postings, so account_id acts as "show me this account's own ledger
// detail," matching a bank statement, rather than "show me every
// transaction that happens to touch this account, in full."
func TransactionsForUnitFiltered(ctx context.Context, pool *pgxpool.Pool, unitID string, from, to time.Time, accountIDs, transactionTypes []string) ([]TransactionDetail, error) {
	where := `WHERE t.unit_id = $1 AND t.occurred_at >= $2 AND t.occurred_at <= $3`
	args := []any{unitID, from, to}
	if len(accountIDs) > 0 {
		args = append(args, accountIDs)
		where += ` AND t.id IN (SELECT transaction_id FROM ledger_postings WHERE account_id = ANY($4))`
	}
	if len(transactionTypes) > 0 {
		args = append(args, transactionTypes)
		where += ` AND t.transaction_type = ANY($` + strconv.Itoa(len(args)) + `)`
	}
	where += ` ORDER BY t.occurred_at, t.id`
	return transactionsQuery(ctx, pool, where, args...)
}

// --- Fundraiser Proceeds Summary -----------------------------------------

// FundraiserProceedsRow is one fundraiser's aggregated proceeds for the
// Fundraiser Proceeds Summary report — total gross proceeds attributed,
// total actually credited to Scout accounts, and how many Scouts were
// credited, within a date range (by allocation created_at).
type FundraiserProceedsRow struct {
	FundraiserID   string
	FundraiserName string
	ScoutCount     int
	GrossCents     int64
	CreditedCents  int64
}

// FundraiserProceedsForUnit aggregates every fundraiser's allocations
// within [from, to], one row per fundraiser (including one with zero
// allocations in range, so a report never silently omits a fundraiser
// that just had no activity that period).
func FundraiserProceedsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string, from, to time.Time) ([]FundraiserProceedsRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT f.id, f.name,
			COUNT(DISTINCT fa.member_id),
			COALESCE(SUM(fa.gross_amount_cents), 0),
			COALESCE(SUM(fa.credited_cents), 0)
		FROM fundraisers f
		LEFT JOIN fundraiser_allocations fa
			ON fa.fundraiser_id = f.id AND fa.created_at >= $2 AND fa.created_at <= $3
		WHERE f.unit_id = $1
		GROUP BY f.id, f.name
		ORDER BY f.name
	`, unitID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FundraiserProceedsRow
	for rows.Next() {
		var r FundraiserProceedsRow
		if err := rows.Scan(&r.FundraiserID, &r.FundraiserName, &r.ScoutCount, &r.GrossCents, &r.CreditedCents); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- Saved report presets ------------------------------------------------

// SavedReport is a named, unit-wide filter preset for /treasury/reports —
// see migration 0024's comment on why it's unit-wide rather than
// per-person, and why Filters is a raw JSON blob rather than individual
// columns (it's exactly the report page's own query-string parameters).
type SavedReport struct {
	ID         string
	UnitID     string
	Name       string
	ReportType string
	Filters    string // raw JSON
	CreatedBy  string
	CreatedAt  time.Time
}

// CreateSavedReport saves a new named filter preset.
func CreateSavedReport(ctx context.Context, pool *pgxpool.Pool, unitID, name, reportType, filtersJSON, createdBy string) (SavedReport, error) {
	var r SavedReport
	err := pool.QueryRow(ctx, `
		INSERT INTO saved_treasury_reports (unit_id, name, report_type, filters, created_by)
		VALUES ($1, $2, $3, $4::jsonb, $5)
		RETURNING id, unit_id, name, report_type, filters::text, created_by, created_at
	`, unitID, name, reportType, filtersJSON, createdBy).Scan(&r.ID, &r.UnitID, &r.Name, &r.ReportType, &r.Filters, &r.CreatedBy, &r.CreatedAt)
	if err != nil {
		return SavedReport{}, err
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "saved_treasury_report",
		EntityID:   r.ID,
		ActorID:    &createdBy,
		Action:     "create",
		After:      map[string]string{"name": name, "report_type": reportType},
	})
	return r, nil
}

// ListSavedReportsForUnit lists every saved report preset for a unit,
// alphabetically — every Treasurer/super_admin in the unit sees the same
// list, since these presets aren't tied to whoever saved them.
func ListSavedReportsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]SavedReport, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, unit_id, name, report_type, filters::text, created_by, created_at
		FROM saved_treasury_reports WHERE unit_id = $1 ORDER BY name
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SavedReport
	for rows.Next() {
		var r SavedReport
		if err := rows.Scan(&r.ID, &r.UnitID, &r.Name, &r.ReportType, &r.Filters, &r.CreatedBy, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetSavedReport looks up one saved report, scoped to a unit.
func GetSavedReport(ctx context.Context, pool *pgxpool.Pool, id, unitID string) (SavedReport, bool, error) {
	var r SavedReport
	err := pool.QueryRow(ctx, `
		SELECT id, unit_id, name, report_type, filters::text, created_by, created_at
		FROM saved_treasury_reports WHERE id = $1 AND unit_id = $2
	`, id, unitID).Scan(&r.ID, &r.UnitID, &r.Name, &r.ReportType, &r.Filters, &r.CreatedBy, &r.CreatedAt)
	if err != nil {
		return SavedReport{}, false, nil //nolint:nilerr // "not found in this unit" is a normal, expected outcome
	}
	return r, true, nil
}

// DeleteSavedReport removes a saved report preset, scoped to a unit so
// one unit can't delete another's. A no-op (not an error) if it's already
// gone.
func DeleteSavedReport(ctx context.Context, pool *pgxpool.Pool, id, unitID, actorID string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM saved_treasury_reports WHERE id = $1 AND unit_id = $2`, id, unitID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "saved_treasury_report",
		EntityID:   id,
		ActorID:    &actorID,
		Action:     "delete",
	})
	return nil
}
