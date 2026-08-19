// Package ledger implements Phase 2's fund accounting: separate
// double-entry books per unit, individual Scout accounts, trip funds tied
// to calendar events, and fundraiser-proceeds allocation — see
// scout-website-requirements.md Section 3.4 and
// internal/db/migrations/0006_ledger.sql for the schema this package
// drives, and the reasoning behind a real double-entry model (money can
// move between accounts but can never simply appear or disappear) rather
// than a single running-balance column per account.
//
// Every account is scoped to one unit (separate books per unit, per the
// requirements doc), and every write in this package that accepts
// caller-supplied account IDs re-validates that those accounts actually
// belong to the unit in question before touching them — the same
// cross-tenant class of bug the Phase 1 security audit found and fixed
// three times over (see SECURITY_AUDIT.md) matters even more here, since
// the blast radius of getting it wrong is someone else's money.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/approval"
	"github.com/47-yonkers/scout-site/internal/audit"
)

// Account is one "book" a unit's money can sit in: the unit's general
// fund, one Scout's individual account, or a trip fund tied to a
// calendar event.
type Account struct {
	ID          string
	UnitID      string
	AccountType string // "unit_general" | "scout_individual" | "trip_fund" | "external"
	MemberID    string // set only for scout_individual, else ""
	EventID     string // set only for trip_fund, else ""
	Name        string
	Status      string // "open" | "closed"
}

// AccountWithBalance decorates Account with its current balance — the sum
// of every posting on it from a transaction with status = 'posted'. A
// pending or rejected transfer never affects a balance.
type AccountWithBalance struct {
	Account
	BalanceCents int64
}

// Transaction is one ledger transaction: a set of Postings that must sum
// to exactly zero. That invariant is enforced twice — once here in Go
// before any database write, and independently by a deferred database
// constraint trigger (0006_ledger.sql) that would reject the write even
// if this package had a bug, since a financial ledger is exactly the kind
// of thing worth not trusting to application code alone.
type Transaction struct {
	ID                string
	UnitID            string
	TransactionType   string // e.g. "deposit", "expense", "manual_adjustment", "fundraiser_allocation", "trip_fund_transfer"
	Description       string
	Status            string // "posted" | "pending_approval" | "rejected"
	CreatedBy         string
	ApprovalRequestID string // set only for a transaction that went through Treasurer approval, else ""
}

// Posting is one line of a Transaction: a signed cents amount against one
// account. Positive credits (increases) the account; negative debits
// (decreases) it. Amounts are always integer cents — never a
// floating-point type — to avoid rounding error in money math.
type Posting struct {
	AccountID   string
	AmountCents int64
}

// PostingDetail decorates Posting with the account's display name, for
// rendering a transaction's line items without a second round trip per
// row.
type PostingDetail struct {
	AccountID   string
	AccountName string
	AmountCents int64
}

// TransactionDetail is a Transaction plus its postings and when it
// occurred — what a statement/activity-feed view actually needs.
type TransactionDetail struct {
	Transaction
	OccurredAt time.Time
	Postings   []PostingDetail
}

// Fundraiser is one tracked fundraiser and the rule for how much of each
// Scout's attributed proceeds gets credited to their individual account.
type Fundraiser struct {
	ID                       string
	UnitID                   string
	Name                     string
	AllocationMode           string // "percentage" | "fixed_per_item"
	AllocationPercent        string // decimal as a string, e.g. "10.00"; "" unless AllocationMode == "percentage"
	AllocationFixedCents     int64  // e.g. 200 = $2.00/item; 0 unless AllocationMode == "fixed_per_item"
	NeedsCouncilConfirmation bool
	CreatedBy                string
}

// FundraiserAllocation is one Scout's credited share from a Fundraiser,
// computed from that fundraiser's rule at the moment the allocation was
// recorded — editing a fundraiser's rule afterward never rewrites past
// allocations, same principle as an audit log never being rewritten.
type FundraiserAllocation struct {
	ID                  string
	FundraiserID        string
	MemberID            string
	GrossAmountCents    int64
	Quantity            string // decimal as a string; "" if not applicable (percentage mode)
	CreditedCents       int64
	LedgerTransactionID string
	CreatedBy           string
}

var (
	// ErrUnbalanced is returned when a caller tries to post a transaction
	// whose postings don't sum to zero.
	ErrUnbalanced = errors.New("ledger: postings do not sum to zero")

	// ErrInsufficientFunds is returned when a transfer's source account
	// doesn't have enough available (posted) balance to cover it.
	ErrInsufficientFunds = errors.New("ledger: insufficient balance for this transfer")

	// ErrAccountNotFound guards against posting to an account ID that
	// doesn't exist, or exists but belongs to a different unit — see the
	// package comment on why every write re-validates this.
	ErrAccountNotFound = errors.New("ledger: account not found in this unit")

	// ErrAccountClosed guards against posting to an account that's been
	// closed out (e.g. a settled trip fund).
	ErrAccountClosed = errors.New("ledger: account is closed")

	// ErrTripFundExists is returned by CreateTripFund when the calendar
	// event already has a trip fund (one trip fund per event, enforced by
	// a unique index in 0006_ledger.sql).
	ErrTripFundExists = errors.New("ledger: a trip fund already exists for this event")

	// ErrAccountBalanceNotZero is returned by CloseTripFund when the fund
	// still has a nonzero balance — see that function's doc comment for
	// why closing requires zeroing it out first rather than sweeping or
	// stranding the remainder automatically.
	ErrAccountBalanceNotZero = errors.New("ledger: account balance must be zero before it can be closed")

	// ErrNotATripFund is returned by CloseTripFund when the given account
	// isn't a trip_fund account — closing is trip-fund-specific, unlike
	// ErrAccountClosed/ErrAccountNotFound which apply to any account type.
	ErrNotATripFund = errors.New("ledger: not a trip fund account")
)

// EnsureUnitGeneralAccount returns a unit's single general-fund account,
// creating it on first use. Safe to call concurrently — a lost race on
// the initial insert simply falls back to re-selecting the row the other
// caller created.
func EnsureUnitGeneralAccount(ctx context.Context, pool *pgxpool.Pool, unitID, createdBy string) (Account, error) {
	if a, err := getUnitGeneralAccount(ctx, pool, unitID); err == nil {
		return a, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Account{}, err
	}

	var a Account
	err := pool.QueryRow(ctx, `
		INSERT INTO ledger_accounts (unit_id, account_type, name, created_by)
		VALUES ($1, 'unit_general', 'General Fund', $2)
		ON CONFLICT (unit_id) WHERE account_type = 'unit_general' DO NOTHING
		RETURNING id, unit_id, account_type::text, COALESCE(member_id::text, ''), COALESCE(event_id::text, ''), name, status::text
	`, unitID, createdBy).Scan(&a.ID, &a.UnitID, &a.AccountType, &a.MemberID, &a.EventID, &a.Name, &a.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Lost the race — someone else's concurrent call created it first.
			return getUnitGeneralAccount(ctx, pool, unitID)
		}
		return Account{}, err
	}
	return a, nil
}

func getUnitGeneralAccount(ctx context.Context, pool *pgxpool.Pool, unitID string) (Account, error) {
	var a Account
	err := pool.QueryRow(ctx, `
		SELECT id, unit_id, account_type::text, COALESCE(member_id::text, ''), COALESCE(event_id::text, ''), name, status::text
		FROM ledger_accounts WHERE unit_id = $1 AND account_type = 'unit_general'
	`, unitID).Scan(&a.ID, &a.UnitID, &a.AccountType, &a.MemberID, &a.EventID, &a.Name, &a.Status)
	return a, err
}

// EnsureExternalAccount returns a unit's single "external" contra-account
// — the other side of every deposit and expense (see 0006_ledger.sql's
// comment on ledger_account_type) — creating it on first use. Same
// lost-the-race fallback as EnsureUnitGeneralAccount.
func EnsureExternalAccount(ctx context.Context, pool *pgxpool.Pool, unitID, createdBy string) (Account, error) {
	if a, err := getExternalAccount(ctx, pool, unitID); err == nil {
		return a, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Account{}, err
	}

	var a Account
	err := pool.QueryRow(ctx, `
		INSERT INTO ledger_accounts (unit_id, account_type, name, created_by)
		VALUES ($1, 'external', 'External (Deposits & Withdrawals)', $2)
		ON CONFLICT (unit_id) WHERE account_type = 'external' DO NOTHING
		RETURNING id, unit_id, account_type::text, COALESCE(member_id::text, ''), COALESCE(event_id::text, ''), name, status::text
	`, unitID, createdBy).Scan(&a.ID, &a.UnitID, &a.AccountType, &a.MemberID, &a.EventID, &a.Name, &a.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return getExternalAccount(ctx, pool, unitID)
		}
		return Account{}, err
	}
	return a, nil
}

func getExternalAccount(ctx context.Context, pool *pgxpool.Pool, unitID string) (Account, error) {
	var a Account
	err := pool.QueryRow(ctx, `
		SELECT id, unit_id, account_type::text, COALESCE(member_id::text, ''), COALESCE(event_id::text, ''), name, status::text
		FROM ledger_accounts WHERE unit_id = $1 AND account_type = 'external'
	`, unitID).Scan(&a.ID, &a.UnitID, &a.AccountType, &a.MemberID, &a.EventID, &a.Name, &a.Status)
	return a, err
}

// EnsureScoutAccount returns a Scout's individual ledger account within a
// unit, creating it (named from memberName) on first use. Same
// lost-the-race fallback as EnsureUnitGeneralAccount.
func EnsureScoutAccount(ctx context.Context, pool *pgxpool.Pool, unitID, memberID, memberName, createdBy string) (Account, error) {
	if a, err := getScoutAccount(ctx, pool, unitID, memberID); err == nil {
		return a, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Account{}, err
	}

	name := memberName + " — Individual Account"
	var a Account
	err := pool.QueryRow(ctx, `
		INSERT INTO ledger_accounts (unit_id, account_type, member_id, name, created_by)
		VALUES ($1, 'scout_individual', $2, $3, $4)
		ON CONFLICT (unit_id, member_id) WHERE account_type = 'scout_individual' DO NOTHING
		RETURNING id, unit_id, account_type::text, COALESCE(member_id::text, ''), COALESCE(event_id::text, ''), name, status::text
	`, unitID, memberID, name, createdBy).Scan(&a.ID, &a.UnitID, &a.AccountType, &a.MemberID, &a.EventID, &a.Name, &a.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return getScoutAccount(ctx, pool, unitID, memberID)
		}
		return Account{}, err
	}
	return a, nil
}

func getScoutAccount(ctx context.Context, pool *pgxpool.Pool, unitID, memberID string) (Account, error) {
	var a Account
	err := pool.QueryRow(ctx, `
		SELECT id, unit_id, account_type::text, COALESCE(member_id::text, ''), COALESCE(event_id::text, ''), name, status::text
		FROM ledger_accounts WHERE unit_id = $1 AND account_type = 'scout_individual' AND member_id = $2
	`, unitID, memberID).Scan(&a.ID, &a.UnitID, &a.AccountType, &a.MemberID, &a.EventID, &a.Name, &a.Status)
	return a, err
}

// ScoutAccountForMember looks up a member's individual ledger account in a
// unit, if one exists, with its current balance. Deliberately read-only —
// unlike EnsureScoutAccount, this must NOT create an account as a side
// effect of a family or Scout just checking their balance (see /accounts
// in internal/web): most Scouts have no individual account at all until
// their first fundraiser credit, and viewing a balance page shouldn't be
// what conjures a $0.00 account into existence.
func ScoutAccountForMember(ctx context.Context, pool *pgxpool.Pool, unitID, memberID string) (AccountWithBalance, bool, error) {
	var a AccountWithBalance
	err := pool.QueryRow(ctx, `
		SELECT
			a.id, a.unit_id, a.account_type::text, COALESCE(a.member_id::text, ''), COALESCE(a.event_id::text, ''), a.name, a.status::text,
			COALESCE((
				SELECT SUM(p.amount_cents) FROM ledger_postings p
				JOIN ledger_transactions t ON t.id = p.transaction_id
				WHERE p.account_id = a.id AND t.status = 'posted'
			), 0) AS balance_cents
		FROM ledger_accounts a
		WHERE a.unit_id = $1 AND a.account_type = 'scout_individual' AND a.member_id = $2
	`, unitID, memberID).Scan(&a.ID, &a.UnitID, &a.AccountType, &a.MemberID, &a.EventID, &a.Name, &a.Status, &a.BalanceCents)
	if err != nil {
		return AccountWithBalance{}, false, nil //nolint:nilerr // "no account yet" is a normal, expected outcome
	}
	return a, true, nil
}

// CreateTripFund creates a new trip-fund account tied to a calendar
// event — Treasurer-only, per units.CanManageLedger.
func CreateTripFund(ctx context.Context, pool *pgxpool.Pool, unitID, eventID, name, createdBy string) (Account, error) {
	var a Account
	err := pool.QueryRow(ctx, `
		INSERT INTO ledger_accounts (unit_id, account_type, event_id, name, created_by)
		VALUES ($1, 'trip_fund', $2, $3, $4)
		RETURNING id, unit_id, account_type::text, COALESCE(member_id::text, ''), COALESCE(event_id::text, ''), name, status::text
	`, unitID, eventID, name, createdBy).Scan(&a.ID, &a.UnitID, &a.AccountType, &a.MemberID, &a.EventID, &a.Name, &a.Status)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Account{}, ErrTripFundExists
		}
		return Account{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "ledger_account",
		EntityID:   a.ID,
		ActorID:    &createdBy,
		Action:     "create_trip_fund",
		After:      a,
	})
	return a, nil
}

// CloseTripFund marks a trip-fund account closed — Treasurer-only, per
// units.CanManageLedger. Requires the account's current balance to
// already be exactly zero: the Treasurer must move out (refund,
// transfer, or otherwise post) any remaining money themselves first,
// using the ordinary transaction/transfer paths, rather than this
// function silently sweeping or stranding a remainder as a side effect
// of closing. Once closed, insertTransaction's status check rejects any
// further posting to the account (see ErrAccountClosed).
func CloseTripFund(ctx context.Context, pool *pgxpool.Pool, unitID, accountID, closedBy string) (Account, error) {
	a, err := GetAccount(ctx, pool, accountID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Account{}, ErrAccountNotFound
		}
		return Account{}, err
	}
	if a.UnitID != unitID {
		return Account{}, ErrAccountNotFound
	}
	if a.AccountType != "trip_fund" {
		return Account{}, ErrNotATripFund
	}
	if a.Status != "open" {
		return Account{}, ErrAccountClosed
	}

	balance, err := BalanceForAccount(ctx, pool, accountID)
	if err != nil {
		return Account{}, err
	}
	if balance != 0 {
		return Account{}, ErrAccountBalanceNotZero
	}

	if _, err := pool.Exec(ctx, `UPDATE ledger_accounts SET status = 'closed' WHERE id = $1`, accountID); err != nil {
		return Account{}, err
	}
	a.Status = "closed"

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "ledger_account",
		EntityID:   a.ID,
		ActorID:    &closedBy,
		Action:     "close_trip_fund",
		After:      a,
	})
	return a, nil
}

// GetAccount looks up a single account by ID, regardless of unit — callers
// that need to enforce "this account belongs to the current unit" should
// check the returned Account.UnitID themselves (see, e.g., the web
// handlers in internal/web/treasury.go).
func GetAccount(ctx context.Context, pool *pgxpool.Pool, accountID string) (Account, error) {
	var a Account
	err := pool.QueryRow(ctx, `
		SELECT id, unit_id, account_type::text, COALESCE(member_id::text, ''), COALESCE(event_id::text, ''), name, status::text
		FROM ledger_accounts WHERE id = $1
	`, accountID).Scan(&a.ID, &a.UnitID, &a.AccountType, &a.MemberID, &a.EventID, &a.Name, &a.Status)
	return a, err
}

// ListAccountsForUnit lists every account in a unit's books with its
// current balance — general fund first, then trip funds, then Scout
// individual accounts, alphabetically within each group.
func ListAccountsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]AccountWithBalance, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			a.id, a.unit_id, a.account_type::text, COALESCE(a.member_id::text, ''), COALESCE(a.event_id::text, ''), a.name, a.status::text,
			COALESCE((
				SELECT SUM(p.amount_cents) FROM ledger_postings p
				JOIN ledger_transactions t ON t.id = p.transaction_id
				WHERE p.account_id = a.id AND t.status = 'posted'
			), 0) AS balance_cents
		FROM ledger_accounts a
		WHERE a.unit_id = $1
		ORDER BY CASE a.account_type WHEN 'unit_general' THEN 0 WHEN 'external' THEN 1 WHEN 'trip_fund' THEN 2 ELSE 3 END, a.name
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []AccountWithBalance
	for rows.Next() {
		var a AccountWithBalance
		if err := rows.Scan(&a.ID, &a.UnitID, &a.AccountType, &a.MemberID, &a.EventID, &a.Name, &a.Status, &a.BalanceCents); err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// BalanceForAccount returns an account's current balance — the sum of
// every posting on it from a posted transaction.
func BalanceForAccount(ctx context.Context, pool *pgxpool.Pool, accountID string) (int64, error) {
	var balance int64
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(p.amount_cents), 0)
		FROM ledger_postings p
		JOIN ledger_transactions t ON t.id = p.transaction_id
		WHERE p.account_id = $1 AND t.status = 'posted'
	`, accountID).Scan(&balance)
	return balance, err
}

// PostTransaction records a new, immediately-effective ledger
// transaction — the everyday path for a Treasurer entering a deposit,
// expense, or manual adjustment, and also used internally for
// fundraiser-allocation transfers. Every account referenced in postings
// must already belong to unitID and be open, or this fails with
// ErrAccountNotFound / ErrAccountClosed rather than silently posting
// across unit boundaries.
func PostTransaction(ctx context.Context, pool *pgxpool.Pool, unitID, transactionType, description, createdBy string, postings []Posting) (Transaction, error) {
	return insertTransaction(ctx, pool, unitID, transactionType, description, createdBy, "posted", postings)
}

func insertTransaction(ctx context.Context, pool *pgxpool.Pool, unitID, transactionType, description, createdBy, status string, postings []Posting) (Transaction, error) {
	if len(postings) < 2 {
		return Transaction{}, fmt.Errorf("ledger: a transaction needs at least two postings, got %d", len(postings))
	}

	var sum int64
	accountIDs := make([]string, 0, len(postings))
	seen := map[string]bool{}
	for _, p := range postings {
		if p.AmountCents == 0 {
			return Transaction{}, fmt.Errorf("ledger: posting amount cannot be zero")
		}
		sum += p.AmountCents
		if !seen[p.AccountID] {
			accountIDs = append(accountIDs, p.AccountID)
			seen[p.AccountID] = true
		}
	}
	if sum != 0 {
		return Transaction{}, ErrUnbalanced
	}

	// Re-validate every referenced account belongs to this unit and is
	// open, before writing anything — see the package comment.
	rows, err := pool.Query(ctx, `SELECT id, status::text FROM ledger_accounts WHERE id = ANY($1) AND unit_id = $2`, accountIDs, unitID)
	if err != nil {
		return Transaction{}, err
	}
	statusByID := map[string]string{}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			rows.Close()
			return Transaction{}, err
		}
		statusByID[id] = st
	}
	if err := rows.Err(); err != nil {
		return Transaction{}, err
	}
	for _, id := range accountIDs {
		st, ok := statusByID[id]
		if !ok {
			return Transaction{}, ErrAccountNotFound
		}
		if st != "open" {
			return Transaction{}, ErrAccountClosed
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Transaction{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	var t Transaction
	err = tx.QueryRow(ctx, `
		INSERT INTO ledger_transactions (unit_id, transaction_type, description, status, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, unit_id, transaction_type, description, status::text, created_by, COALESCE(approval_request_id::text, '')
	`, unitID, transactionType, description, status, createdBy).Scan(
		&t.ID, &t.UnitID, &t.TransactionType, &t.Description, &t.Status, &t.CreatedBy, &t.ApprovalRequestID,
	)
	if err != nil {
		return Transaction{}, err
	}

	for _, p := range postings {
		if _, err := tx.Exec(ctx,
			`INSERT INTO ledger_postings (transaction_id, account_id, amount_cents) VALUES ($1, $2, $3)`,
			t.ID, p.AccountID, p.AmountCents,
		); err != nil {
			return Transaction{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Transaction{}, fmt.Errorf("ledger: committing transaction (a failure here may mean the postings didn't balance at the database level): %w", err)
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "ledger_transaction",
		EntityID:   t.ID,
		ActorID:    &createdBy,
		Action:     "create_" + status,
		After:      t,
	})

	return t, nil
}

// RequestTripFundTransfer creates a pending "push" transfer — a Scout's
// family moving money from the Scout's own individual account into a
// trip-specific fund — and submits it for Treasurer sign-off via the same
// generic approval workflow Phase 1 built for calendar submissions (see
// internal/approval and scout-website-architecture-phase1.md Section 5).
// Nothing affects any balance until a Treasurer approves it: the postings
// exist from creation (so the database's balance-check trigger can
// validate them) but every balance/statement query filters to
// status = 'posted'.
//
// The source account's available balance is checked here at request time,
// and re-checked at decision time inside internal/approval.Decide before
// a Treasurer's approval is allowed to take effect — a family's balance
// could otherwise drop between the request and the decision (e.g. another
// transfer clearing first).
func RequestTripFundTransfer(ctx context.Context, pool *pgxpool.Pool, unitID, scoutAccountID, tripFundAccountID string, amountCents int64, description, requestedBy string) (Transaction, approval.Request, error) {
	if amountCents <= 0 {
		return Transaction{}, approval.Request{}, fmt.Errorf("ledger: transfer amount must be positive")
	}

	balance, err := BalanceForAccount(ctx, pool, scoutAccountID)
	if err != nil {
		return Transaction{}, approval.Request{}, err
	}
	if balance < amountCents {
		return Transaction{}, approval.Request{}, ErrInsufficientFunds
	}

	t, err := insertTransaction(ctx, pool, unitID, "trip_fund_transfer", description, requestedBy, "pending_approval", []Posting{
		{AccountID: scoutAccountID, AmountCents: -amountCents},
		{AccountID: tripFundAccountID, AmountCents: amountCents},
	})
	if err != nil {
		return Transaction{}, approval.Request{}, err
	}

	req, err := approval.Submit(ctx, pool, "ledger_transaction", t.ID, unitID, requestedBy)
	if err != nil {
		return t, approval.Request{}, err
	}

	if _, err := pool.Exec(ctx,
		`UPDATE ledger_transactions SET approval_request_id = $1 WHERE id = $2`, req.ID, t.ID,
	); err != nil {
		return t, req, err
	}
	t.ApprovalRequestID = req.ID

	return t, req, nil
}

// transactionsQuery is the shared query behind TransactionsForUnit,
// TransactionsForAccount, and PendingTransfersForUnit — a single
// JOIN across transactions/postings/accounts, grouped into
// TransactionDetail values in Go (avoids an N+1 query per transaction).
func transactionsQuery(ctx context.Context, pool *pgxpool.Pool, whereAndOrder string, args ...any) ([]TransactionDetail, error) {
	rows, err := pool.Query(ctx, `
		SELECT t.id, t.unit_id, t.transaction_type, t.description, t.status::text, t.created_by, COALESCE(t.approval_request_id::text, ''), t.occurred_at,
		       p.account_id, a.name, p.amount_cents
		FROM ledger_transactions t
		JOIN ledger_postings p ON p.transaction_id = t.id
		JOIN ledger_accounts a ON a.id = p.account_id
		`+whereAndOrder, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var order []string
	byID := map[string]*TransactionDetail{}
	for rows.Next() {
		var id, unitID, txType, description, status, createdBy, approvalReqID string
		var occurredAt time.Time
		var pd PostingDetail
		if err := rows.Scan(&id, &unitID, &txType, &description, &status, &createdBy, &approvalReqID, &occurredAt, &pd.AccountID, &pd.AccountName, &pd.AmountCents); err != nil {
			return nil, err
		}
		td, ok := byID[id]
		if !ok {
			td = &TransactionDetail{
				Transaction: Transaction{ID: id, UnitID: unitID, TransactionType: txType, Description: description, Status: status, CreatedBy: createdBy, ApprovalRequestID: approvalReqID},
				OccurredAt:  occurredAt,
			}
			byID[id] = td
			order = append(order, id)
		}
		td.Postings = append(td.Postings, pd)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]TransactionDetail, 0, len(order))
	for _, id := range order {
		result = append(result, *byID[id])
	}
	return result, nil
}

// TransactionsForUnit lists a unit's ledger transactions (every status —
// posted, pending, and rejected, so a Treasurer's activity feed shows the
// full picture), most recent first. Pass limit <= 0 for no limit.
//
// This pulls the unit's full transaction history and limits in Go rather
// than in SQL — fine at the scale a single troop/pack's books run at, and
// simple; worth revisiting with a proper paginated query if that ever
// stops being true.
func TransactionsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string, limit int) ([]TransactionDetail, error) {
	all, err := transactionsQuery(ctx, pool, `WHERE t.unit_id = $1 ORDER BY t.occurred_at DESC, t.id`, unitID)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// TransactionsForAccount lists every transaction touching a single
// account — a family's "my Scout's account" statement, or a trip fund's
// own history — most recent first, including pending transfers so a
// family can see a request's status while it's awaiting approval.
func TransactionsForAccount(ctx context.Context, pool *pgxpool.Pool, accountID string, limit int) ([]TransactionDetail, error) {
	all, err := transactionsQuery(ctx, pool, `
		WHERE t.id IN (SELECT transaction_id FROM ledger_postings WHERE account_id = $1)
		ORDER BY t.occurred_at DESC, t.id
	`, accountID)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// PendingTransfersForUnit lists trip-fund transfers awaiting Treasurer
// approval — what the Treasurer dashboard shows as "needs your decision".
func PendingTransfersForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]TransactionDetail, error) {
	return transactionsQuery(ctx, pool, `WHERE t.unit_id = $1 AND t.status = 'pending_approval' ORDER BY t.occurred_at`, unitID)
}

// --- Fundraisers -------------------------------------------------------

// CreateFundraiser registers a new fundraiser and its per-Scout
// allocation rule (percentage of proceeds, or a fixed amount per item
// sold — see scout-website-requirements.md Section 3.4's compliance
// flag). needs_council_confirmation always starts true — see
// 0006_ledger.sql's column comment and ConfirmFundraiserCap below.
func CreateFundraiser(ctx context.Context, pool *pgxpool.Pool, unitID, name, allocationMode, allocationPercent string, allocationFixedCents int64, createdBy string) (Fundraiser, error) {
	var row pgx.Row
	switch allocationMode {
	case "percentage":
		row = pool.QueryRow(ctx, `
			INSERT INTO fundraisers (unit_id, name, allocation_mode, allocation_percent, created_by)
			VALUES ($1, $2, 'percentage', $3, $4)
			RETURNING id, unit_id, name, allocation_mode::text, COALESCE(allocation_percent::text, ''), COALESCE(allocation_fixed_cents, 0), needs_council_confirmation, created_by
		`, unitID, name, allocationPercent, createdBy)
	case "fixed_per_item":
		row = pool.QueryRow(ctx, `
			INSERT INTO fundraisers (unit_id, name, allocation_mode, allocation_fixed_cents, created_by)
			VALUES ($1, $2, 'fixed_per_item', $3, $4)
			RETURNING id, unit_id, name, allocation_mode::text, COALESCE(allocation_percent::text, ''), COALESCE(allocation_fixed_cents, 0), needs_council_confirmation, created_by
		`, unitID, name, allocationFixedCents, createdBy)
	default:
		return Fundraiser{}, fmt.Errorf("ledger: unknown allocation mode %q", allocationMode)
	}

	var f Fundraiser
	if err := row.Scan(&f.ID, &f.UnitID, &f.Name, &f.AllocationMode, &f.AllocationPercent, &f.AllocationFixedCents, &f.NeedsCouncilConfirmation, &f.CreatedBy); err != nil {
		return Fundraiser{}, err
	}

	audit.Log(ctx, pool, audit.Entry{EntityType: "fundraiser", EntityID: f.ID, ActorID: &createdBy, Action: "create", After: f})
	return f, nil
}

// ConfirmFundraiserCap updates a fundraiser's allocation rule to a
// council-confirmed value and clears needs_council_confirmation. Also
// usable to just clear the flag if the placeholder value already matches
// what the council confirmed — pass the same values back.
func ConfirmFundraiserCap(ctx context.Context, pool *pgxpool.Pool, fundraiserID, allocationMode, allocationPercent string, allocationFixedCents int64, confirmedBy string) error {
	var err error
	switch allocationMode {
	case "percentage":
		_, err = pool.Exec(ctx, `
			UPDATE fundraisers SET allocation_mode = 'percentage', allocation_percent = $1, allocation_fixed_cents = NULL, needs_council_confirmation = false
			WHERE id = $2
		`, allocationPercent, fundraiserID)
	case "fixed_per_item":
		_, err = pool.Exec(ctx, `
			UPDATE fundraisers SET allocation_mode = 'fixed_per_item', allocation_fixed_cents = $1, allocation_percent = NULL, needs_council_confirmation = false
			WHERE id = $2
		`, allocationFixedCents, fundraiserID)
	default:
		return fmt.Errorf("ledger: unknown allocation mode %q", allocationMode)
	}
	if err != nil {
		return err
	}

	audit.Log(ctx, pool, audit.Entry{EntityType: "fundraiser", EntityID: fundraiserID, ActorID: &confirmedBy, Action: "confirm_council_cap"})
	return nil
}

// ListFundraisersForUnit lists a unit's fundraisers, most recently
// created first.
func ListFundraisersForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Fundraiser, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, unit_id, name, allocation_mode::text, COALESCE(allocation_percent::text, ''), COALESCE(allocation_fixed_cents, 0), needs_council_confirmation, created_by
		FROM fundraisers WHERE unit_id = $1 ORDER BY created_at DESC
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Fundraiser
	for rows.Next() {
		var f Fundraiser
		if err := rows.Scan(&f.ID, &f.UnitID, &f.Name, &f.AllocationMode, &f.AllocationPercent, &f.AllocationFixedCents, &f.NeedsCouncilConfirmation, &f.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFundraiser looks up a single fundraiser by ID.
func GetFundraiser(ctx context.Context, pool *pgxpool.Pool, fundraiserID string) (Fundraiser, error) {
	var f Fundraiser
	err := pool.QueryRow(ctx, `
		SELECT id, unit_id, name, allocation_mode::text, COALESCE(allocation_percent::text, ''), COALESCE(allocation_fixed_cents, 0), needs_council_confirmation, created_by
		FROM fundraisers WHERE id = $1
	`, fundraiserID).Scan(&f.ID, &f.UnitID, &f.Name, &f.AllocationMode, &f.AllocationPercent, &f.AllocationFixedCents, &f.NeedsCouncilConfirmation, &f.CreatedBy)
	return f, err
}

// AllocationsForFundraiser lists every Scout credited from a fundraiser
// so far.
func AllocationsForFundraiser(ctx context.Context, pool *pgxpool.Pool, fundraiserID string) ([]FundraiserAllocation, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, fundraiser_id, member_id, gross_amount_cents, COALESCE(quantity::text, ''), credited_cents, ledger_transaction_id, created_by
		FROM fundraiser_allocations WHERE fundraiser_id = $1 ORDER BY created_at DESC
	`, fundraiserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FundraiserAllocation
	for rows.Next() {
		var fa FundraiserAllocation
		if err := rows.Scan(&fa.ID, &fa.FundraiserID, &fa.MemberID, &fa.GrossAmountCents, &fa.Quantity, &fa.CreditedCents, &fa.LedgerTransactionID, &fa.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, fa)
	}
	return out, rows.Err()
}

// RecordFundraiserAllocation credits one Scout's share of a fundraiser's
// proceeds to their individual account, computed from the fundraiser's
// allocation rule as it stands right now (editing the rule later never
// rewrites past allocations). Moves the credited amount out of the
// unit's general fund and into the Scout's individual account as an
// ordinary posted transaction — the unit's total balance never changes
// just because proceeds get reattributed from the general fund to a
// Scout's account, it only moves between books.
//
// quantity is only meaningful (and required, as a positive number string)
// in fixed_per_item mode; pass "" in percentage mode.
func RecordFundraiserAllocation(ctx context.Context, pool *pgxpool.Pool, fundraiserID, memberID, memberName string, grossAmountCents int64, quantity, createdBy string) (FundraiserAllocation, error) {
	if grossAmountCents <= 0 {
		return FundraiserAllocation{}, fmt.Errorf("ledger: gross amount must be positive")
	}

	f, err := GetFundraiser(ctx, pool, fundraiserID)
	if err != nil {
		return FundraiserAllocation{}, err
	}

	var creditedCents int64
	switch f.AllocationMode {
	case "percentage":
		pct, perr := strconv.ParseFloat(f.AllocationPercent, 64)
		if perr != nil {
			return FundraiserAllocation{}, fmt.Errorf("ledger: parsing allocation percent: %w", perr)
		}
		creditedCents = int64(math.Round(float64(grossAmountCents) * pct / 100))
	case "fixed_per_item":
		qty, qerr := strconv.ParseFloat(quantity, 64)
		if qerr != nil || qty <= 0 {
			return FundraiserAllocation{}, fmt.Errorf("ledger: quantity must be a positive number for a fixed-per-item fundraiser")
		}
		creditedCents = int64(math.Round(qty * float64(f.AllocationFixedCents)))
	default:
		return FundraiserAllocation{}, fmt.Errorf("ledger: unknown allocation mode %q", f.AllocationMode)
	}
	if creditedCents <= 0 {
		return FundraiserAllocation{}, fmt.Errorf("ledger: computed credit is zero or negative — check the fundraiser's allocation rule")
	}
	if creditedCents > grossAmountCents {
		return FundraiserAllocation{}, fmt.Errorf("ledger: computed credit (%d cents) exceeds the gross amount attributed to this scout (%d cents) — check the allocation rule", creditedCents, grossAmountCents)
	}

	generalAccount, err := EnsureUnitGeneralAccount(ctx, pool, f.UnitID, createdBy)
	if err != nil {
		return FundraiserAllocation{}, err
	}
	scoutAccount, err := EnsureScoutAccount(ctx, pool, f.UnitID, memberID, memberName, createdBy)
	if err != nil {
		return FundraiserAllocation{}, err
	}

	txn, err := PostTransaction(ctx, pool, f.UnitID, "fundraiser_allocation",
		fmt.Sprintf("%s — share for %s", f.Name, memberName), createdBy, []Posting{
			{AccountID: generalAccount.ID, AmountCents: -creditedCents},
			{AccountID: scoutAccount.ID, AmountCents: creditedCents},
		})
	if err != nil {
		return FundraiserAllocation{}, err
	}

	var qtyArg any
	if quantity != "" {
		qtyArg = quantity
	}

	var fa FundraiserAllocation
	err = pool.QueryRow(ctx, `
		INSERT INTO fundraiser_allocations (fundraiser_id, member_id, gross_amount_cents, quantity, credited_cents, ledger_transaction_id, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, fundraiser_id, member_id, gross_amount_cents, COALESCE(quantity::text, ''), credited_cents, ledger_transaction_id, created_by
	`, fundraiserID, memberID, grossAmountCents, qtyArg, creditedCents, txn.ID, createdBy).Scan(
		&fa.ID, &fa.FundraiserID, &fa.MemberID, &fa.GrossAmountCents, &fa.Quantity, &fa.CreditedCents, &fa.LedgerTransactionID, &fa.CreatedBy,
	)
	if err != nil {
		return FundraiserAllocation{}, err
	}

	audit.Log(ctx, pool, audit.Entry{EntityType: "fundraiser_allocation", EntityID: fa.ID, ActorID: &createdBy, Action: "create", After: fa})
	return fa, nil
}
