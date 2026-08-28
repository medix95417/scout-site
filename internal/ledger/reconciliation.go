package ledger

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// Bank reconciliation — the monthly tick-off of one account's postings
// against the bank statement for the same period. Everything else in this
// package is about money moving correctly *within* the books; this is the
// one place the books get checked against an outside source of truth,
// which is what actually catches a missed deposit, a duplicated entry, or
// a check nobody ever cashed.
//
// The model is deliberately the standard one a bookkeeper already knows:
//
//	opening balance   (last statement's closing balance, frozen at start)
//	+ cleared postings (the ones ticked off as appearing on this statement)
//	= cleared balance
//	statement closing balance − cleared balance = difference
//
// and the reconciliation cannot be marked complete until that difference
// is exactly zero. Postings left unticked are outstanding checks and
// deposits in transit; they simply stay uncleared and show up again next
// month, which is the correct treatment rather than something to correct.
//
// Nothing here ever writes a posting, changes an amount, or moves money.
// Reconciling is a review activity: its only effect on the ledger is
// stamping postings with the reconciliation they cleared in. If the books
// are wrong, the fix is an ordinary correcting transaction (see
// ReverseTransaction) — never an edit made in the course of reconciling.

var (
	// ErrReconciliationNotFound is returned when a reconciliation id
	// doesn't exist in the unit being asked about — the same
	// cross-tenant-safe "not found" this package returns for accounts.
	ErrReconciliationNotFound = errors.New("ledger: reconciliation not found in this unit")

	// ErrReconciliationOpen is returned when starting a reconciliation for
	// an account that already has an unfinished one.
	ErrReconciliationOpen = errors.New("ledger: this account already has a reconciliation in progress")

	// ErrReconciliationClosed is returned when trying to change a
	// reconciliation that has already been signed off.
	ErrReconciliationClosed = errors.New("ledger: this reconciliation is already completed")

	// ErrReconciliationNotBalanced is returned when completing a
	// reconciliation whose difference isn't zero — the entire point of the
	// exercise, so it's refused rather than warned about.
	ErrReconciliationNotBalanced = errors.New("ledger: the difference must be zero before a reconciliation can be completed")

	// ErrPostingNotReconcilable is returned when ticking a posting that
	// isn't on this reconciliation's account, isn't posted yet, or has
	// already been cleared by a different (completed) reconciliation.
	ErrPostingNotReconcilable = errors.New("ledger: that entry can't be cleared on this reconciliation")
)

// Reconciliation is one statement period's reconciliation of one account.
type Reconciliation struct {
	ID                  string
	UnitID              string
	AccountID           string
	AccountName         string
	StatementDate       time.Time
	OpeningBalanceCents int64
	ClosingBalanceCents int64
	Status              string // "open" | "completed"
	CreatedBy           string
	CreatedAt           time.Time
	CompletedBy         string // "" while open
	CompletedAt         *time.Time
}

// Completed reports whether this reconciliation has been signed off.
func (r Reconciliation) Completed() bool { return r.Status == "completed" }

// ReconciliationItem is one posting as it appears on the tick-off list:
// the posting itself plus enough of its transaction to recognize it.
type ReconciliationItem struct {
	PostingID       string
	TransactionID   string
	TransactionType string
	Description     string
	OccurredAt      time.Time
	AmountCents     int64
	Cleared         bool
}

// ReconciliationSummary is a Reconciliation plus the arithmetic that
// decides whether it can be completed. Computed on read rather than
// stored, so it can never drift from the postings it describes.
type ReconciliationSummary struct {
	Reconciliation
	ClearedTotalCents int64 // sum of the postings ticked off on this reconciliation
	ClearedCount      int
	UnclearedCount    int
	UnclearedTotal    int64
}

// ClearedBalanceCents is what the account's balance would be if only the
// ticked-off entries existed — the figure that has to match the bank.
func (s ReconciliationSummary) ClearedBalanceCents() int64 {
	return s.OpeningBalanceCents + s.ClearedTotalCents
}

// DifferenceCents is the gap between the bank's closing balance and the
// cleared balance. Zero means the books agree with the bank.
func (s ReconciliationSummary) DifferenceCents() int64 {
	return s.ClosingBalanceCents - s.ClearedBalanceCents()
}

// Balanced reports whether this reconciliation can be completed.
func (s ReconciliationSummary) Balanced() bool { return s.DifferenceCents() == 0 }

const reconciliationColumns = `
	br.id::text, br.unit_id::text, br.account_id::text, a.name,
	br.statement_date, br.opening_balance_cents, br.closing_balance_cents,
	br.status::text, br.created_by::text, br.created_at,
	COALESCE(br.completed_by::text, ''), br.completed_at
`

func scanReconciliation(row pgx.Row) (Reconciliation, error) {
	var r Reconciliation
	err := row.Scan(&r.ID, &r.UnitID, &r.AccountID, &r.AccountName,
		&r.StatementDate, &r.OpeningBalanceCents, &r.ClosingBalanceCents,
		&r.Status, &r.CreatedBy, &r.CreatedAt, &r.CompletedBy, &r.CompletedAt)
	return r, err
}

// StartReconciliation opens a reconciliation of one account against a
// bank statement. The opening balance is taken from the previous
// completed reconciliation of the same account (zero for the first one
// ever) and frozen here, so that a later correction to an old
// reconciliation can't silently shift this period's starting point.
//
// Refuses if the account isn't this unit's — the same cross-tenant check
// every other write in this package makes — or if a reconciliation of it
// is already in progress.
func StartReconciliation(ctx context.Context, pool *pgxpool.Pool, unitID, accountID string, statementDate time.Time, closingBalanceCents int64, createdBy string) (Reconciliation, error) {
	var ok bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM ledger_accounts WHERE id = $1 AND unit_id = $2)`,
		accountID, unitID).Scan(&ok); err != nil {
		return Reconciliation{}, err
	}
	if !ok {
		return Reconciliation{}, ErrAccountNotFound
	}

	var opening int64
	err := pool.QueryRow(ctx, `
		SELECT closing_balance_cents FROM bank_reconciliations
		WHERE account_id = $1 AND status = 'completed'
		ORDER BY statement_date DESC, completed_at DESC
		LIMIT 1
	`, accountID).Scan(&opening)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Reconciliation{}, err
	}

	row := pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO bank_reconciliations
				(unit_id, account_id, statement_date, opening_balance_cents, closing_balance_cents, created_by)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING *
		)
		SELECT `+reconciliationColumns+`
		FROM inserted br JOIN ledger_accounts a ON a.id = br.account_id
	`, unitID, accountID, statementDate, opening, closingBalanceCents, createdBy)

	rec, err := scanReconciliation(row)
	if err != nil {
		// The partial unique index is what actually enforces "one open
		// reconciliation per account" — checking first and inserting after
		// would race two Treasurers starting one at the same moment.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Reconciliation{}, ErrReconciliationOpen
		}
		return Reconciliation{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "bank_reconciliation",
		EntityID:   rec.ID,
		ActorID:    &createdBy,
		Action:     "create",
		After: map[string]any{
			"account_id":            rec.AccountID,
			"statement_date":        rec.StatementDate.Format("2006-01-02"),
			"opening_balance_cents": rec.OpeningBalanceCents,
			"closing_balance_cents": rec.ClosingBalanceCents,
		},
	})
	return rec, nil
}

// GetReconciliation loads one reconciliation, scoped to its unit.
func GetReconciliation(ctx context.Context, pool *pgxpool.Pool, unitID, reconciliationID string) (Reconciliation, error) {
	row := pool.QueryRow(ctx, `
		SELECT `+reconciliationColumns+`
		FROM bank_reconciliations br JOIN ledger_accounts a ON a.id = br.account_id
		WHERE br.id = $1 AND br.unit_id = $2
	`, reconciliationID, unitID)
	rec, err := scanReconciliation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reconciliation{}, ErrReconciliationNotFound
	}
	return rec, err
}

// ListReconciliations returns a unit's reconciliations, newest statement
// first — the history a treasurer hands to a reviewer at audit time.
func ListReconciliations(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Reconciliation, error) {
	rows, err := pool.Query(ctx, `
		SELECT `+reconciliationColumns+`
		FROM bank_reconciliations br JOIN ledger_accounts a ON a.id = br.account_id
		WHERE br.unit_id = $1
		ORDER BY br.statement_date DESC, br.created_at DESC
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Reconciliation
	for rows.Next() {
		rec, err := scanReconciliation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// SummarizeReconciliation loads a reconciliation together with the
// arithmetic that decides whether it balances.
func SummarizeReconciliation(ctx context.Context, pool *pgxpool.Pool, unitID, reconciliationID string) (ReconciliationSummary, error) {
	rec, err := GetReconciliation(ctx, pool, unitID, reconciliationID)
	if err != nil {
		return ReconciliationSummary{}, err
	}
	s := ReconciliationSummary{Reconciliation: rec}
	err = pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(p.amount_cents) FILTER (WHERE p.reconciliation_id = $1), 0),
			COUNT(*)                     FILTER (WHERE p.reconciliation_id = $1),
			COUNT(*)                     FILTER (WHERE p.reconciliation_id IS NULL),
			COALESCE(SUM(p.amount_cents) FILTER (WHERE p.reconciliation_id IS NULL), 0)
		FROM ledger_postings p
		JOIN ledger_transactions t ON t.id = p.transaction_id
		WHERE p.account_id = $2 AND t.status = 'posted'
	`, reconciliationID, rec.AccountID).Scan(&s.ClearedTotalCents, &s.ClearedCount, &s.UnclearedCount, &s.UnclearedTotal)
	if err != nil {
		return ReconciliationSummary{}, err
	}
	return s, nil
}

// ReconciliationItems returns everything a treasurer can tick on this
// reconciliation: the postings already cleared on it, plus every posting
// on the same account still uncleared by anyone.
//
// Uncleared postings are listed regardless of date. A check written in
// March that the bank cashes in May belongs on May's statement, so date-
// filtering the list would hide exactly the entries reconciliation exists
// to surface. Only postings from transactions that have actually posted
// appear — an expense still waiting on the Cubmaster's authorization
// hasn't hit the books, so it can't have hit the bank either.
func ReconciliationItems(ctx context.Context, pool *pgxpool.Pool, unitID, reconciliationID string) ([]ReconciliationItem, error) {
	rec, err := GetReconciliation(ctx, pool, unitID, reconciliationID)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `
		SELECT p.id::text, t.id::text, t.transaction_type, t.description, t.occurred_at,
		       p.amount_cents, (p.reconciliation_id IS NOT NULL)
		FROM ledger_postings p
		JOIN ledger_transactions t ON t.id = p.transaction_id
		WHERE p.account_id = $1
		  AND t.status = 'posted'
		  AND (p.reconciliation_id IS NULL OR p.reconciliation_id = $2)
		ORDER BY t.occurred_at, t.created_at, p.id
	`, rec.AccountID, reconciliationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ReconciliationItem
	for rows.Next() {
		var it ReconciliationItem
		if err := rows.Scan(&it.PostingID, &it.TransactionID, &it.TransactionType,
			&it.Description, &it.OccurredAt, &it.AmountCents, &it.Cleared); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SetPostingCleared ticks or unticks one posting on an open
// reconciliation. Both directions are guarded by the same WHERE clause,
// so a posting can only ever be moved between "uncleared" and "cleared by
// this reconciliation" — never stolen from a completed one.
func SetPostingCleared(ctx context.Context, pool *pgxpool.Pool, unitID, reconciliationID, postingID string, cleared bool) error {
	rec, err := GetReconciliation(ctx, pool, unitID, reconciliationID)
	if err != nil {
		return err
	}
	if rec.Completed() {
		return ErrReconciliationClosed
	}

	var target any
	if cleared {
		target = reconciliationID
	} else {
		target = nil
	}
	tag, err := pool.Exec(ctx, `
		UPDATE ledger_postings p
		SET reconciliation_id = $1
		FROM ledger_transactions t
		WHERE p.id = $2
		  AND t.id = p.transaction_id
		  AND t.status = 'posted'
		  AND p.account_id = $3
		  AND (p.reconciliation_id IS NULL OR p.reconciliation_id = $4)
	`, target, postingID, rec.AccountID, reconciliationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPostingNotReconcilable
	}
	return nil
}

// CompleteReconciliation signs off a reconciliation, refusing unless the
// difference is exactly zero. Once complete, its cleared postings stay
// stamped with it and drop out of the next reconciliation's list.
func CompleteReconciliation(ctx context.Context, pool *pgxpool.Pool, unitID, reconciliationID, actorID string) (ReconciliationSummary, error) {
	s, err := SummarizeReconciliation(ctx, pool, unitID, reconciliationID)
	if err != nil {
		return ReconciliationSummary{}, err
	}
	if s.Completed() {
		return ReconciliationSummary{}, ErrReconciliationClosed
	}
	if !s.Balanced() {
		return ReconciliationSummary{}, ErrReconciliationNotBalanced
	}

	// The status guard in the WHERE clause makes this safe against two
	// treasurers clicking Complete at the same moment: the second one
	// updates no rows and is told the reconciliation is already closed.
	tag, err := pool.Exec(ctx, `
		UPDATE bank_reconciliations
		SET status = 'completed', completed_by = $1, completed_at = now()
		WHERE id = $2 AND unit_id = $3 AND status = 'open'
	`, actorID, reconciliationID, unitID)
	if err != nil {
		return ReconciliationSummary{}, err
	}
	if tag.RowsAffected() == 0 {
		return ReconciliationSummary{}, ErrReconciliationClosed
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "bank_reconciliation",
		EntityID:   reconciliationID,
		ActorID:    &actorID,
		Action:     "approve",
		After: map[string]any{
			"statement_date":        s.StatementDate.Format("2006-01-02"),
			"closing_balance_cents": s.ClosingBalanceCents,
			"cleared_count":         s.ClearedCount,
			"uncleared_count":       s.UnclearedCount,
		},
	})

	s.Status = "completed"
	s.CompletedBy = actorID
	now := time.Now()
	s.CompletedAt = &now
	return s, nil
}

// DeleteReconciliation abandons an unfinished reconciliation, releasing
// every posting it had ticked back to uncleared. Completed
// reconciliations are not deletable: they're the record that the books
// were checked against the bank on a given date, which is exactly the
// thing an audit asks to see.
func DeleteReconciliation(ctx context.Context, pool *pgxpool.Pool, unitID, reconciliationID, actorID string) error {
	rec, err := GetReconciliation(ctx, pool, unitID, reconciliationID)
	if err != nil {
		return err
	}
	if rec.Completed() {
		return ErrReconciliationClosed
	}

	// ON DELETE SET NULL on the posting FK is what releases the ticked
	// postings; the delete is guarded on status so it can't race a
	// concurrent completion.
	tag, err := pool.Exec(ctx,
		`DELETE FROM bank_reconciliations WHERE id = $1 AND unit_id = $2 AND status = 'open'`,
		reconciliationID, unitID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrReconciliationClosed
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "bank_reconciliation",
		EntityID:   reconciliationID,
		ActorID:    &actorID,
		Action:     "delete",
		Before: map[string]any{
			"account_id":            rec.AccountID,
			"statement_date":        rec.StatementDate.Format("2006-01-02"),
			"closing_balance_cents": rec.ClosingBalanceCents,
		},
	})
	return nil
}
