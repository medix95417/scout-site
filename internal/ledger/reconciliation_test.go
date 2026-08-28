package ledger

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Reconciliation tests use the same TEST_DATABASE_URL harness as the rest
// of this package (see ledger_test.go) — the rules worth protecting here
// are database rules: the partial unique index that allows only one open
// reconciliation per account, the ON DELETE SET NULL that releases ticked
// postings, and the status guards that make completion race-safe.

// recFixture is a unit with a general fund and an external account, which
// between them are enough to post ordinary money-in/money-out entries.
type recFixture struct {
	fixture
	general  Account
	external Account
}

func newRecFixture(t *testing.T) recFixture {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	general, err := EnsureUnitGeneralAccount(ctx, f.pool, f.unitID, f.memberID)
	if err != nil {
		t.Fatalf("EnsureUnitGeneralAccount: %v", err)
	}
	external, err := EnsureExternalAccount(ctx, f.pool, f.unitID, f.memberID)
	if err != nil {
		t.Fatalf("EnsureExternalAccount: %v", err)
	}
	return recFixture{fixture: f, general: general, external: external}
}

// post books an amount into (positive) or out of (negative) the general
// fund and returns the id of the general-fund posting.
func (f recFixture) post(t *testing.T, amountCents int64, description string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := PostTransaction(ctx, f.pool, f.unitID, "deposit", description, f.memberID, []Posting{
		{AccountID: f.external.ID, AmountCents: -amountCents},
		{AccountID: f.general.ID, AmountCents: amountCents},
	}); err != nil {
		t.Fatalf("posting %q: %v", description, err)
	}
	var postingID string
	if err := f.pool.QueryRow(ctx, `
		SELECT p.id::text FROM ledger_postings p
		JOIN ledger_transactions t ON t.id = p.transaction_id
		WHERE p.account_id = $1 AND t.description = $2
	`, f.general.ID, description).Scan(&postingID); err != nil {
		t.Fatalf("finding posting for %q: %v", description, err)
	}
	return postingID
}

func (f recFixture) start(t *testing.T, closingCents int64) Reconciliation {
	t.Helper()
	rec, err := StartReconciliation(context.Background(), f.pool, f.unitID, f.general.ID,
		time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), closingCents, f.memberID)
	if err != nil {
		t.Fatalf("StartReconciliation: %v", err)
	}
	return rec
}

func (f recFixture) summarize(t *testing.T, recID string) ReconciliationSummary {
	t.Helper()
	s, err := SummarizeReconciliation(context.Background(), f.pool, f.unitID, recID)
	if err != nil {
		t.Fatalf("SummarizeReconciliation: %v", err)
	}
	return s
}

// TestReconciliation_DifferenceMustBeZeroToComplete is the headline rule:
// a reconciliation that doesn't balance can't be signed off, and the
// difference is what tells the treasurer how far out they are.
func TestReconciliation_DifferenceMustBeZeroToComplete(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()

	deposit := f.post(t, 10000, "January dues")
	check := f.post(t, -2500, "Campsite deposit check")

	// The bank shows only the dues so far — the check hasn't cleared.
	rec := f.start(t, 10000)

	if s := f.summarize(t, rec.ID); s.DifferenceCents() != 10000 {
		t.Fatalf("with nothing ticked, difference should be the whole statement balance, got %d", s.DifferenceCents())
	}

	if _, err := CompleteReconciliation(ctx, f.pool, f.unitID, rec.ID, f.memberID); !errors.Is(err, ErrReconciliationNotBalanced) {
		t.Fatalf("completing an unbalanced reconciliation should fail with ErrReconciliationNotBalanced, got %v", err)
	}

	if err := SetPostingCleared(ctx, f.pool, f.unitID, rec.ID, deposit, true); err != nil {
		t.Fatalf("clearing the deposit: %v", err)
	}

	s := f.summarize(t, rec.ID)
	if s.DifferenceCents() != 0 || !s.Balanced() {
		t.Fatalf("difference should be zero once the deposit is ticked, got %d", s.DifferenceCents())
	}
	if s.UnclearedCount != 1 || s.UnclearedTotal != -2500 {
		t.Fatalf("the outstanding check should be the one uncleared item, got count=%d total=%d", s.UnclearedCount, s.UnclearedTotal)
	}

	// Ticking the outstanding check too would be wrong, and the arithmetic
	// says so rather than quietly accepting it.
	if err := SetPostingCleared(ctx, f.pool, f.unitID, rec.ID, check, true); err != nil {
		t.Fatalf("clearing the check: %v", err)
	}
	if d := f.summarize(t, rec.ID).DifferenceCents(); d != 2500 {
		t.Fatalf("over-ticking should push the difference off zero, got %d", d)
	}
	if err := SetPostingCleared(ctx, f.pool, f.unitID, rec.ID, check, false); err != nil {
		t.Fatalf("unticking the check: %v", err)
	}

	if _, err := CompleteReconciliation(ctx, f.pool, f.unitID, rec.ID, f.memberID); err != nil {
		t.Fatalf("a balanced reconciliation should complete: %v", err)
	}

	done, err := GetReconciliation(ctx, f.pool, f.unitID, rec.ID)
	if err != nil {
		t.Fatalf("GetReconciliation: %v", err)
	}
	if !done.Completed() || done.CompletedBy != f.memberID || done.CompletedAt == nil {
		t.Fatalf("a completed reconciliation should record who signed it off and when, got %+v", done)
	}
}

// TestReconciliation_OpeningBalanceCarriesForward checks the month-to-
// month chain: the second statement starts where the first one ended, and
// last month's outstanding check clears against this month's statement.
func TestReconciliation_OpeningBalanceCarriesForward(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()

	deposit := f.post(t, 10000, "January dues")
	check := f.post(t, -2500, "Campsite deposit check")

	jan := f.start(t, 10000)
	if err := SetPostingCleared(ctx, f.pool, f.unitID, jan.ID, deposit, true); err != nil {
		t.Fatalf("clearing the deposit: %v", err)
	}
	if _, err := CompleteReconciliation(ctx, f.pool, f.unitID, jan.ID, f.memberID); err != nil {
		t.Fatalf("completing January: %v", err)
	}

	// February: the check finally cleared, so the bank is now at $75.00.
	feb, err := StartReconciliation(ctx, f.pool, f.unitID, f.general.ID,
		time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), 7500, f.memberID)
	if err != nil {
		t.Fatalf("StartReconciliation (February): %v", err)
	}
	if feb.OpeningBalanceCents != 10000 {
		t.Fatalf("February should open at January's closing balance, got %d", feb.OpeningBalanceCents)
	}

	// January's cleared deposit must not be tickable again.
	items, err := ReconciliationItems(ctx, f.pool, f.unitID, feb.ID)
	if err != nil {
		t.Fatalf("ReconciliationItems: %v", err)
	}
	if len(items) != 1 || items[0].PostingID != check {
		t.Fatalf("February should offer only the still-outstanding check, got %+v", items)
	}

	if err := SetPostingCleared(ctx, f.pool, f.unitID, feb.ID, check, true); err != nil {
		t.Fatalf("clearing the check in February: %v", err)
	}
	if s := f.summarize(t, feb.ID); !s.Balanced() {
		t.Fatalf("February should balance at 75.00, difference %d", s.DifferenceCents())
	}
	if _, err := CompleteReconciliation(ctx, f.pool, f.unitID, feb.ID, f.memberID); err != nil {
		t.Fatalf("completing February: %v", err)
	}
}

// TestReconciliation_CannotStealAnotherPeriodsPosting guards the tick-off
// list against reaching into a signed-off period.
func TestReconciliation_CannotStealAnotherPeriodsPosting(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()

	deposit := f.post(t, 10000, "January dues")
	jan := f.start(t, 10000)
	if err := SetPostingCleared(ctx, f.pool, f.unitID, jan.ID, deposit, true); err != nil {
		t.Fatalf("clearing the deposit: %v", err)
	}
	if _, err := CompleteReconciliation(ctx, f.pool, f.unitID, jan.ID, f.memberID); err != nil {
		t.Fatalf("completing January: %v", err)
	}

	feb, err := StartReconciliation(ctx, f.pool, f.unitID, f.general.ID,
		time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), 10000, f.memberID)
	if err != nil {
		t.Fatalf("StartReconciliation (February): %v", err)
	}
	if err := SetPostingCleared(ctx, f.pool, f.unitID, feb.ID, deposit, true); !errors.Is(err, ErrPostingNotReconcilable) {
		t.Fatalf("February must not be able to re-tick January's cleared deposit, got %v", err)
	}
	// And it can't untick it out of January either.
	if err := SetPostingCleared(ctx, f.pool, f.unitID, feb.ID, deposit, false); !errors.Is(err, ErrPostingNotReconcilable) {
		t.Fatalf("February must not be able to release January's cleared deposit, got %v", err)
	}
	if s := f.summarize(t, jan.ID); s.ClearedTotalCents != 10000 {
		t.Fatalf("January's cleared total should be untouched, got %d", s.ClearedTotalCents)
	}
}

// TestReconciliation_OneOpenPerAccount checks the partial unique index.
func TestReconciliation_OneOpenPerAccount(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()

	first := f.start(t, 0)
	if _, err := StartReconciliation(ctx, f.pool, f.unitID, f.general.ID,
		time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), 0, f.memberID); !errors.Is(err, ErrReconciliationOpen) {
		t.Fatalf("a second open reconciliation should be refused, got %v", err)
	}

	// Completing the first frees the account up again.
	if _, err := CompleteReconciliation(ctx, f.pool, f.unitID, first.ID, f.memberID); err != nil {
		t.Fatalf("completing the first: %v", err)
	}
	if _, err := StartReconciliation(ctx, f.pool, f.unitID, f.general.ID,
		time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), 0, f.memberID); err != nil {
		t.Fatalf("a new reconciliation should be allowed once the previous is complete: %v", err)
	}
}

// TestReconciliation_CompletedIsImmutable checks that a signed-off
// reconciliation can't be re-ticked, re-completed, or deleted — it's the
// record that the books were checked, so it stays put.
func TestReconciliation_CompletedIsImmutable(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()

	deposit := f.post(t, 10000, "January dues")
	rec := f.start(t, 10000)
	if err := SetPostingCleared(ctx, f.pool, f.unitID, rec.ID, deposit, true); err != nil {
		t.Fatalf("clearing the deposit: %v", err)
	}
	if _, err := CompleteReconciliation(ctx, f.pool, f.unitID, rec.ID, f.memberID); err != nil {
		t.Fatalf("completing: %v", err)
	}

	if err := SetPostingCleared(ctx, f.pool, f.unitID, rec.ID, deposit, false); !errors.Is(err, ErrReconciliationClosed) {
		t.Fatalf("unticking a completed reconciliation should fail, got %v", err)
	}
	if _, err := CompleteReconciliation(ctx, f.pool, f.unitID, rec.ID, f.memberID); !errors.Is(err, ErrReconciliationClosed) {
		t.Fatalf("completing twice should fail, got %v", err)
	}
	if err := DeleteReconciliation(ctx, f.pool, f.unitID, rec.ID, f.memberID); !errors.Is(err, ErrReconciliationClosed) {
		t.Fatalf("deleting a completed reconciliation should fail, got %v", err)
	}
}

// TestReconciliation_DeleteReleasesTickedPostings checks that abandoning
// an unfinished reconciliation leaves its entries free for the next one,
// rather than stranding them as permanently cleared.
func TestReconciliation_DeleteReleasesTickedPostings(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()

	deposit := f.post(t, 10000, "January dues")
	rec := f.start(t, 10000)
	if err := SetPostingCleared(ctx, f.pool, f.unitID, rec.ID, deposit, true); err != nil {
		t.Fatalf("clearing the deposit: %v", err)
	}
	if err := DeleteReconciliation(ctx, f.pool, f.unitID, rec.ID, f.memberID); err != nil {
		t.Fatalf("DeleteReconciliation: %v", err)
	}

	var reconciled bool
	if err := f.pool.QueryRow(ctx,
		`SELECT reconciliation_id IS NOT NULL FROM ledger_postings WHERE id = $1`, deposit,
	).Scan(&reconciled); err != nil {
		t.Fatalf("re-reading the posting: %v", err)
	}
	if reconciled {
		t.Fatal("abandoning a reconciliation should leave its postings uncleared")
	}
	if _, err := GetReconciliation(ctx, f.pool, f.unitID, rec.ID); !errors.Is(err, ErrReconciliationNotFound) {
		t.Fatalf("the deleted reconciliation should be gone, got %v", err)
	}
}

// TestReconciliation_IgnoresUnpostedTransactions checks that an expense
// still waiting on authorization can't be ticked off — it hasn't hit the
// books, so it can't have hit the bank.
func TestReconciliation_IgnoresUnpostedTransactions(t *testing.T) {
	f := newRecFixture(t)
	ctx := context.Background()

	if _, _, err := SubmitExpenseForApproval(ctx, f.pool, f.unitID, "Unauthorized camp gear", f.memberID, []Posting{
		{AccountID: f.general.ID, AmountCents: -9900},
		{AccountID: f.external.ID, AmountCents: 9900},
	}); err != nil {
		t.Fatalf("SubmitExpenseForApproval: %v", err)
	}

	var pendingPosting string
	if err := f.pool.QueryRow(ctx, `
		SELECT p.id::text FROM ledger_postings p
		JOIN ledger_transactions t ON t.id = p.transaction_id
		WHERE p.account_id = $1 AND t.status = 'pending_approval'
	`, f.general.ID).Scan(&pendingPosting); err != nil {
		t.Fatalf("finding the pending posting: %v", err)
	}

	rec := f.start(t, 0)
	items, err := ReconciliationItems(ctx, f.pool, f.unitID, rec.ID)
	if err != nil {
		t.Fatalf("ReconciliationItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("a pending expense should not appear on the tick-off list, got %+v", items)
	}
	if err := SetPostingCleared(ctx, f.pool, f.unitID, rec.ID, pendingPosting, true); !errors.Is(err, ErrPostingNotReconcilable) {
		t.Fatalf("a pending expense should not be tickable, got %v", err)
	}
	if s := f.summarize(t, rec.ID); s.UnclearedCount != 0 || s.UnclearedTotal != 0 {
		t.Fatalf("a pending expense should not count as an uncleared item, got count=%d total=%d", s.UnclearedCount, s.UnclearedTotal)
	}
}

// TestReconciliation_IsScopedToItsUnit is this package's standing
// cross-tenant check, applied to the new surface.
func TestReconciliation_IsScopedToItsUnit(t *testing.T) {
	f := newRecFixture(t)
	other := newRecFixture(t)
	ctx := context.Background()

	rec := f.start(t, 0)

	if _, err := GetReconciliation(ctx, f.pool, other.unitID, rec.ID); !errors.Is(err, ErrReconciliationNotFound) {
		t.Fatalf("another unit should not be able to read this reconciliation, got %v", err)
	}
	if _, err := SummarizeReconciliation(ctx, f.pool, other.unitID, rec.ID); !errors.Is(err, ErrReconciliationNotFound) {
		t.Fatalf("another unit should not be able to summarize this reconciliation, got %v", err)
	}
	if _, err := CompleteReconciliation(ctx, f.pool, other.unitID, rec.ID, other.memberID); !errors.Is(err, ErrReconciliationNotFound) {
		t.Fatalf("another unit should not be able to complete this reconciliation, got %v", err)
	}
	if err := DeleteReconciliation(ctx, f.pool, other.unitID, rec.ID, other.memberID); !errors.Is(err, ErrReconciliationNotFound) {
		t.Fatalf("another unit should not be able to delete this reconciliation, got %v", err)
	}
	if _, err := StartReconciliation(ctx, f.pool, other.unitID, f.general.ID,
		time.Now(), 0, other.memberID); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("another unit should not be able to reconcile this unit's account, got %v", err)
	}

	list, err := ListReconciliations(ctx, f.pool, other.unitID)
	if err != nil {
		t.Fatalf("ListReconciliations: %v", err)
	}
	for _, r := range list {
		if r.ID == rec.ID {
			t.Fatal("another unit's reconciliation list should not include this one")
		}
	}
}
