package ledger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/approval"
	"github.com/47-yonkers/scout-site/internal/db"
)

// These are integration tests, not unit tests, and that's deliberate: the
// guarantees worth protecting in this package are guarantees about what
// the database ends up holding. The balance-to-zero rule in particular is
// enforced twice — once in Go and once by a deferred constraint trigger
// (0006_ledger.sql) — and a test with a fake pool would exercise neither
// the trigger nor the transaction boundaries that make the Go half safe.
//
// Set TEST_DATABASE_URL to run them, e.g.
//
//	createdb scout_ledger_test
//	TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/scout_ledger_test?sslmode=disable' go test ./internal/ledger/
//
// They skip when it isn't set, so `go test ./...` stays green on a
// machine (or a CI runner) with no Postgres.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping ledger integration tests")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrating test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// fixture is one isolated unit with one member, so each test works on its
// own books and can't be perturbed by another test's rows.
type fixture struct {
	pool     *pgxpool.Pool
	unitID   string
	memberID string
}

// runID keeps slugs and hostnames unique across runs — these tests leave
// their rows behind, so a fixed name would collide the second time the
// suite is run against the same database.
var (
	runID       = time.Now().UnixNano()
	unitCounter atomic.Int64
)

func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := testPool(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d-%d", runID, unitCounter.Add(1))

	var unitID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO units (slug, name, unit_type, hostname) VALUES ($1, $2, 'troop', $3) RETURNING id`,
		"t-"+suffix, "Troop "+suffix, "h-"+suffix+".example.test",
	).Scan(&unitID); err != nil {
		t.Fatalf("creating test unit: %v", err)
	}

	var familyID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO families (name) VALUES ($1) RETURNING id`, "Family "+suffix,
	).Scan(&familyID); err != nil {
		t.Fatalf("creating test family: %v", err)
	}

	var memberID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, 'Test', 'Scout', 'youth') RETURNING id`,
		familyID,
	).Scan(&memberID); err != nil {
		t.Fatalf("creating test member: %v", err)
	}

	return fixture{pool: pool, unitID: unitID, memberID: memberID}
}

// newEvent creates a calendar event to hang a trip fund off. CreateTripFund
// takes an event ID as a plain string and passes it straight through as a
// uuid parameter, so it needs a real one rather than "".
func (f fixture) newEvent(t *testing.T, title string) string {
	t.Helper()
	var eventID string
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO events (unit_id, title, starts_at, created_by) VALUES ($1, $2, now(), $3) RETURNING id`,
		f.unitID, title, f.memberID,
	).Scan(&eventID); err != nil {
		t.Fatalf("creating test event: %v", err)
	}
	return eventID
}

// TestPostTransaction_RejectsUnbalancedPostings is the headline invariant:
// money can move between accounts but can never appear or disappear.
func TestPostTransaction_RejectsUnbalancedPostings(t *testing.T) {
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

	// Both sides of the same amount — the shape every real entry has.
	if _, err := PostTransaction(ctx, f.pool, f.unitID, "deposit", "balanced", f.memberID, []Posting{
		{AccountID: external.ID, AmountCents: -5000},
		{AccountID: general.ID, AmountCents: 5000},
	}); err != nil {
		t.Fatalf("a balanced transaction should post: %v", err)
	}

	unbalanced := [][]Posting{
		{{AccountID: external.ID, AmountCents: -5000}, {AccountID: general.ID, AmountCents: 4999}},
		{{AccountID: external.ID, AmountCents: -1}, {AccountID: general.ID, AmountCents: 100}},
	}
	for i, postings := range unbalanced {
		if _, err := PostTransaction(ctx, f.pool, f.unitID, "deposit", "unbalanced", f.memberID, postings); !errors.Is(err, ErrUnbalanced) {
			t.Errorf("case %d: want ErrUnbalanced, got %v", i, err)
		}
	}

	// A zero-amount posting is meaningless and would also slip past a
	// naive sum check, so it's rejected outright.
	if _, err := PostTransaction(ctx, f.pool, f.unitID, "deposit", "zero", f.memberID, []Posting{
		{AccountID: external.ID, AmountCents: 0},
		{AccountID: general.ID, AmountCents: 0},
	}); err == nil {
		t.Error("a transaction of zero-amount postings should be rejected")
	}

	// Only the one balanced transaction above should have landed.
	balance, err := BalanceForAccount(ctx, f.pool, general.ID)
	if err != nil {
		t.Fatalf("BalanceForAccount: %v", err)
	}
	if balance != 5000 {
		t.Errorf("general fund balance = %d, want 5000 — a rejected transaction leaked into the books", balance)
	}
}

// TestPostTransaction_RejectsCrossUnitAccounts covers the separate-books
// rule. Two units share one install; an account ID from one must never be
// postable from the other, however it reached the caller.
func TestPostTransaction_RejectsCrossUnitAccounts(t *testing.T) {
	a := newFixture(t)
	b := newFixture(t)
	ctx := context.Background()

	aGeneral, err := EnsureUnitGeneralAccount(ctx, a.pool, a.unitID, a.memberID)
	if err != nil {
		t.Fatalf("EnsureUnitGeneralAccount(a): %v", err)
	}
	bGeneral, err := EnsureUnitGeneralAccount(ctx, b.pool, b.unitID, b.memberID)
	if err != nil {
		t.Fatalf("EnsureUnitGeneralAccount(b): %v", err)
	}

	// Balanced, but reaching across the boundary — the sum being zero is
	// exactly why this needs its own check.
	if _, err := PostTransaction(ctx, a.pool, a.unitID, "transfer", "cross-unit", a.memberID, []Posting{
		{AccountID: aGeneral.ID, AmountCents: -2500},
		{AccountID: bGeneral.ID, AmountCents: 2500},
	}); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("want ErrAccountNotFound for a cross-unit posting, got %v", err)
	}

	for _, acct := range []struct {
		name string
		id   string
	}{{"unit A", aGeneral.ID}, {"unit B", bGeneral.ID}} {
		bal, err := BalanceForAccount(ctx, a.pool, acct.id)
		if err != nil {
			t.Fatalf("BalanceForAccount(%s): %v", acct.name, err)
		}
		if bal != 0 {
			t.Errorf("%s balance = %d, want 0 — the rejected transaction still moved money", acct.name, bal)
		}
	}
}

// TestRequestTripFundTransfer_RejectsOverdraft covers the guard on the one
// money movement a family can start themselves.
func TestRequestTripFundTransfer_RejectsOverdraft(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	general, err := EnsureUnitGeneralAccount(ctx, f.pool, f.unitID, f.memberID)
	if err != nil {
		t.Fatalf("EnsureUnitGeneralAccount: %v", err)
	}
	scout, err := EnsureScoutAccount(ctx, f.pool, f.unitID, f.memberID, "Test Scout", f.memberID)
	if err != nil {
		t.Fatalf("EnsureScoutAccount: %v", err)
	}
	trip, err := CreateTripFund(ctx, f.pool, f.unitID, f.newEvent(t, "Summer Camp"), "Summer Camp", f.memberID)
	if err != nil {
		t.Fatalf("CreateTripFund: %v", err)
	}

	// Fund the Scout's account with $30.
	if _, err := PostTransaction(ctx, f.pool, f.unitID, "fundraiser_allocation", "seed", f.memberID, []Posting{
		{AccountID: general.ID, AmountCents: -3000},
		{AccountID: scout.ID, AmountCents: 3000},
	}); err != nil {
		t.Fatalf("seeding the scout account: %v", err)
	}

	if _, _, err := RequestTripFundTransfer(ctx, f.pool, f.unitID, scout.ID, trip.ID, 3001, "too much", f.memberID); !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("want ErrInsufficientFunds for $30.01 out of $30.00, got %v", err)
	}
	if _, _, err := RequestTripFundTransfer(ctx, f.pool, f.unitID, scout.ID, trip.ID, 0, "zero", f.memberID); err == nil {
		t.Error("a zero-amount transfer should be rejected")
	}
	if _, _, err := RequestTripFundTransfer(ctx, f.pool, f.unitID, scout.ID, trip.ID, -500, "negative", f.memberID); err == nil {
		t.Error("a negative transfer should be rejected — it would be a withdrawal in disguise")
	}
}

// TestRequestTripFundTransfer_PendingDoesNotMoveMoney is the property the
// whole approval workflow rests on: the postings exist from the moment
// the request is made (so the database can balance-check them), but they
// must not count toward any balance until a Treasurer approves.
func TestRequestTripFundTransfer_PendingDoesNotMoveMoney(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	general, _ := EnsureUnitGeneralAccount(ctx, f.pool, f.unitID, f.memberID)
	scout, _ := EnsureScoutAccount(ctx, f.pool, f.unitID, f.memberID, "Test Scout", f.memberID)
	trip, err := CreateTripFund(ctx, f.pool, f.unitID, f.newEvent(t, "Klondike"), "Klondike", f.memberID)
	if err != nil {
		t.Fatalf("CreateTripFund: %v", err)
	}
	if _, err := PostTransaction(ctx, f.pool, f.unitID, "fundraiser_allocation", "seed", f.memberID, []Posting{
		{AccountID: general.ID, AmountCents: -10000},
		{AccountID: scout.ID, AmountCents: 10000},
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	_, req, err := RequestTripFundTransfer(ctx, f.pool, f.unitID, scout.ID, trip.ID, 2500, "camp fee", f.memberID)
	if err != nil {
		t.Fatalf("RequestTripFundTransfer: %v", err)
	}

	assertBalance(t, ctx, f.pool, scout.ID, 10000, "scout account before approval")
	assertBalance(t, ctx, f.pool, trip.ID, 0, "trip fund before approval")

	if err := approval.Decide(ctx, f.pool, req.ID, f.unitID, f.memberID, true); err != nil {
		t.Fatalf("approving: %v", err)
	}

	assertBalance(t, ctx, f.pool, scout.ID, 7500, "scout account after approval")
	assertBalance(t, ctx, f.pool, trip.ID, 2500, "trip fund after approval")
}

// TestApproval_RejectedTransferNeverMovesMoney is the mirror image.
func TestApproval_RejectedTransferNeverMovesMoney(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	general, _ := EnsureUnitGeneralAccount(ctx, f.pool, f.unitID, f.memberID)
	scout, _ := EnsureScoutAccount(ctx, f.pool, f.unitID, f.memberID, "Test Scout", f.memberID)
	trip, err := CreateTripFund(ctx, f.pool, f.unitID, f.newEvent(t, "Camporee"), "Camporee", f.memberID)
	if err != nil {
		t.Fatalf("CreateTripFund: %v", err)
	}
	if _, err := PostTransaction(ctx, f.pool, f.unitID, "fundraiser_allocation", "seed", f.memberID, []Posting{
		{AccountID: general.ID, AmountCents: -4000},
		{AccountID: scout.ID, AmountCents: 4000},
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	_, req, err := RequestTripFundTransfer(ctx, f.pool, f.unitID, scout.ID, trip.ID, 1000, "fee", f.memberID)
	if err != nil {
		t.Fatalf("RequestTripFundTransfer: %v", err)
	}
	if err := approval.Decide(ctx, f.pool, req.ID, f.unitID, f.memberID, false); err != nil {
		t.Fatalf("rejecting: %v", err)
	}

	assertBalance(t, ctx, f.pool, scout.ID, 4000, "scout account after rejection")
	assertBalance(t, ctx, f.pool, trip.ID, 0, "trip fund after rejection")
}

// TestRecordFundraiserAllocation_CannotCreditMoreThanGross covers the cap
// that keeps a mis-entered allocation rule from crediting a Scout more
// than the unit actually took in on their behalf.
func TestRecordFundraiserAllocation_CannotCreditMoreThanGross(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	fr, err := CreateFundraiser(ctx, f.pool, f.unitID, "Popcorn", "percentage", "150.00", 0, "", f.memberID)
	if err != nil {
		t.Fatalf("CreateFundraiser: %v", err)
	}
	if _, err := RecordFundraiserAllocation(ctx, f.pool, fr.ID, f.memberID, "Test Scout", 10000, "", f.memberID); err == nil {
		t.Error("a 150% allocation rule should be refused, not credited")
	}

	ok, err := CreateFundraiser(ctx, f.pool, f.unitID, "Wreaths", "percentage", "10.00", 0, "", f.memberID)
	if err != nil {
		t.Fatalf("CreateFundraiser: %v", err)
	}
	alloc, err := RecordFundraiserAllocation(ctx, f.pool, ok.ID, f.memberID, "Test Scout", 10000, "", f.memberID)
	if err != nil {
		t.Fatalf("a 10%% allocation should be credited: %v", err)
	}
	if alloc.CreditedCents != 1000 {
		t.Errorf("credited %d cents on $100 gross at 10%%, want 1000", alloc.CreditedCents)
	}

	// The unit's total didn't change — the money moved from the general
	// fund into the Scout's account, it wasn't created.
	general, _ := EnsureUnitGeneralAccount(ctx, f.pool, f.unitID, f.memberID)
	assertBalance(t, ctx, f.pool, general.ID, -1000, "general fund after allocation")
	scout, _ := EnsureScoutAccount(ctx, f.pool, f.unitID, f.memberID, "Test Scout", f.memberID)
	assertBalance(t, ctx, f.pool, scout.ID, 1000, "scout account after allocation")
}

// TestCreateFundraiserOrder_BoundsQuantityAndTotal covers the public,
// unauthenticated order endpoint. Without a bound, price × quantity
// overflows int64 and can wrap back to a positive number: a $1.00 item at
// quantity 2e17 produced a "$15,532,559,262,904,483" order that passed a
// plain total > 0 check.
func TestCreateFundraiserOrder_BoundsQuantityAndTotal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	fr, err := CreateFundraiser(ctx, f.pool, f.unitID, "Popcorn", "percentage", "10.00", 0, "", f.memberID)
	if err != nil {
		t.Fatalf("CreateFundraiser: %v", err)
	}

	// The case that was actually reachable before the bound existed: a
	// quantity that fits Postgres's int quantity column, so nothing
	// downstream rejected it, at a total that fits bigint and passes
	// total_cents > 0. This produced a real $2,000,000,000 order row from
	// an anonymous POST.
	absurd := []FundraiserOrderItem{{ItemName: "Popcorn", UnitPriceCents: 100, Quantity: 2000000000}}
	if _, err := CreateFundraiserOrder(ctx, f.pool, fr.ID, f.unitID, "Buyer", "", "", "Test Scout", "", absurd, ""); err == nil {
		t.Error("a $2bn order should be rejected outright")
	}

	// Large enough that price × quantity wraps int64 back to a positive
	// number, defeating a plain total > 0 check in Go.
	overflowing := []FundraiserOrderItem{{ItemName: "Popcorn", UnitPriceCents: 100, Quantity: 200000000000000000}}
	if _, err := CreateFundraiserOrder(ctx, f.pool, fr.ID, f.unitID, "Buyer", "", "", "Test Scout", "", overflowing, ""); err == nil {
		t.Error("an overflowing quantity should be rejected outright")
	}

	tooMany := []FundraiserOrderItem{{ItemName: "Popcorn", UnitPriceCents: 100, Quantity: MaxOrderItemQuantity + 1}}
	if _, err := CreateFundraiserOrder(ctx, f.pool, fr.ID, f.unitID, "Buyer", "", "", "Test Scout", "", tooMany, ""); err == nil {
		t.Error("a quantity past MaxOrderItemQuantity should be rejected")
	}

	negative := []FundraiserOrderItem{{ItemName: "Popcorn", UnitPriceCents: 100, Quantity: -5}}
	if _, err := CreateFundraiserOrder(ctx, f.pool, fr.ID, f.unitID, "Buyer", "", "", "Test Scout", "", negative, ""); err == nil {
		t.Error("a negative quantity should be rejected")
	}

	good := []FundraiserOrderItem{{ItemName: "Popcorn", UnitPriceCents: 1200, Quantity: 3}}
	order, err := CreateFundraiserOrder(ctx, f.pool, fr.ID, f.unitID, "Buyer", "", "", "Test Scout", "", good, "")
	if err != nil {
		t.Fatalf("an ordinary order should be accepted: %v", err)
	}
	if order.TotalCents != 3600 {
		t.Errorf("order total = %d, want 3600", order.TotalCents)
	}
}

func assertBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID string, want int64, what string) {
	t.Helper()
	got, err := BalanceForAccount(ctx, pool, accountID)
	if err != nil {
		t.Fatalf("BalanceForAccount(%s): %v", what, err)
	}
	if got != want {
		t.Errorf("%s = %d cents, want %d", what, got, want)
	}
}

// TestReverseTransaction covers the correction path (F8): a posted entry
// can be undone by an equal-and-opposite entry that stays linked to it,
// rather than by editing or deleting anything.
func TestReverseTransaction(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	general, _ := EnsureUnitGeneralAccount(ctx, f.pool, f.unitID, f.memberID)
	external, _ := EnsureExternalAccount(ctx, f.pool, f.unitID, f.memberID)

	orig, err := PostTransaction(ctx, f.pool, f.unitID, "deposit", "Dues — typo, $500 not $50", f.memberID, []Posting{
		{AccountID: external.ID, AmountCents: -50000},
		{AccountID: general.ID, AmountCents: 50000},
	})
	if err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}
	assertBalance(t, ctx, f.pool, general.ID, 50000, "general fund after the mistaken deposit")

	rev, err := ReverseTransaction(ctx, f.pool, f.unitID, orig.ID, f.memberID)
	if err != nil {
		t.Fatalf("ReverseTransaction: %v", err)
	}
	if rev.ReversesTransactionID != orig.ID {
		t.Errorf("reversal links to %q, want %q", rev.ReversesTransactionID, orig.ID)
	}
	assertBalance(t, ctx, f.pool, general.ID, 0, "general fund after the reversal")

	// The original is still on the books — a correction shows, it doesn't hide.
	var stillThere bool
	if err := f.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM ledger_transactions WHERE id = $1 AND status = 'posted')`, orig.ID,
	).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if !stillThere {
		t.Error("the original transaction was removed — a reversal must leave it in place")
	}

	// Reversing twice would double-count the correction.
	if _, err := ReverseTransaction(ctx, f.pool, f.unitID, orig.ID, f.memberID); !errors.Is(err, ErrNotReversible) {
		t.Errorf("second reversal: want ErrNotReversible, got %v", err)
	}
	// Reversing a reversal is refused — re-post the original instead.
	if _, err := ReverseTransaction(ctx, f.pool, f.unitID, rev.ID, f.memberID); !errors.Is(err, ErrNotReversible) {
		t.Errorf("reversing a reversal: want ErrNotReversible, got %v", err)
	}
	// And it can't reach across units.
	other := newFixture(t)
	if _, err := ReverseTransaction(ctx, f.pool, other.unitID, orig.ID, other.memberID); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("cross-unit reversal: want ErrAccountNotFound, got %v", err)
	}
}

// TestReverseTransaction_RefusesPending — a pending transfer never moved
// money, so there is nothing to undo; it should be rejected instead.
func TestReverseTransaction_RefusesPending(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	general, _ := EnsureUnitGeneralAccount(ctx, f.pool, f.unitID, f.memberID)
	scout, _ := EnsureScoutAccount(ctx, f.pool, f.unitID, f.memberID, "Test Scout", f.memberID)
	trip, err := CreateTripFund(ctx, f.pool, f.unitID, f.newEvent(t, "Trip"), "Trip", f.memberID)
	if err != nil {
		t.Fatalf("CreateTripFund: %v", err)
	}
	if _, err := PostTransaction(ctx, f.pool, f.unitID, "fundraiser_allocation", "seed", f.memberID, []Posting{
		{AccountID: general.ID, AmountCents: -5000},
		{AccountID: scout.ID, AmountCents: 5000},
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	pending, _, err := RequestTripFundTransfer(ctx, f.pool, f.unitID, scout.ID, trip.ID, 1000, "fee", f.memberID)
	if err != nil {
		t.Fatalf("RequestTripFundTransfer: %v", err)
	}

	if _, err := ReverseTransaction(ctx, f.pool, f.unitID, pending.ID, f.memberID); !errors.Is(err, ErrNotReversible) {
		t.Errorf("reversing a pending transfer: want ErrNotReversible, got %v", err)
	}
}

// TestApproval_SequentialTransfersCannotOverdraw locks in the property
// that two separately-approved transfers can't together overdraw an
// account — the balance is re-read at each decision, not at request time.
// (This passed before the F7 fix too; it's here to keep it passing.)
func TestApproval_SequentialTransfersCannotOverdraw(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	general, _ := EnsureUnitGeneralAccount(ctx, f.pool, f.unitID, f.memberID)
	scout, _ := EnsureScoutAccount(ctx, f.pool, f.unitID, f.memberID, "Test Scout", f.memberID)
	trip, err := CreateTripFund(ctx, f.pool, f.unitID, f.newEvent(t, "Camp"), "Camp", f.memberID)
	if err != nil {
		t.Fatalf("CreateTripFund: %v", err)
	}
	if _, err := PostTransaction(ctx, f.pool, f.unitID, "fundraiser_allocation", "seed", f.memberID, []Posting{
		{AccountID: general.ID, AmountCents: -2000},
		{AccountID: scout.ID, AmountCents: 2000},
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// Two pending transfers, each affordable alone ($15 of a $20 balance)
	// but not together. Approving both must not overdraw the account.
	_, reqA, err := RequestTripFundTransfer(ctx, f.pool, f.unitID, scout.ID, trip.ID, 1500, "first", f.memberID)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	_, reqB, err := RequestTripFundTransfer(ctx, f.pool, f.unitID, scout.ID, trip.ID, 1500, "second", f.memberID)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}

	if err := approval.Decide(ctx, f.pool, reqA.ID, f.unitID, f.memberID, true); err != nil {
		t.Fatalf("approving the first: %v", err)
	}
	if err := approval.Decide(ctx, f.pool, reqB.ID, f.unitID, f.memberID, true); err == nil {
		t.Error("approving the second should have been refused — together they overdraw the account")
	}

	assertBalance(t, ctx, f.pool, scout.ID, 500, "scout account after one of two transfers")
	if bal, _ := BalanceForAccount(ctx, f.pool, scout.ID); bal < 0 {
		t.Errorf("scout account went negative (%d) — the overdraft guard failed", bal)
	}
}

// TestApproval_RechecksEveryDebitedAccount is the real regression test for
// F7's aggregation half. The old re-check ran a query returning one row
// per debited account and read it with QueryRow — so it validated only the
// FIRST debit and approved any others unchecked.
//
// Every trip-fund transfer built by RequestTripFundTransfer has exactly
// one debit, which is why nothing was wrong in practice. This constructs
// the two-debit case directly to prove the guard now covers all of them.
func TestApproval_RechecksEveryDebitedAccount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	general, _ := EnsureUnitGeneralAccount(ctx, f.pool, f.unitID, f.memberID)
	funded, _ := EnsureScoutAccount(ctx, f.pool, f.unitID, f.memberID, "Funded Scout", f.memberID)

	// A second Scout account, left at a zero balance.
	var emptyMemberID string
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO members (family_id, first_name, last_name, member_type)
		 SELECT family_id, 'Empty', 'Scout', 'youth' FROM members WHERE id = $1 RETURNING id`,
		f.memberID,
	).Scan(&emptyMemberID); err != nil {
		t.Fatalf("creating second member: %v", err)
	}
	empty, err := EnsureScoutAccount(ctx, f.pool, f.unitID, emptyMemberID, "Empty Scout", f.memberID)
	if err != nil {
		t.Fatalf("EnsureScoutAccount: %v", err)
	}
	trip, err := CreateTripFund(ctx, f.pool, f.unitID, f.newEvent(t, "Camp"), "Camp", f.memberID)
	if err != nil {
		t.Fatalf("CreateTripFund: %v", err)
	}

	if _, err := PostTransaction(ctx, f.pool, f.unitID, "fundraiser_allocation", "seed", f.memberID, []Posting{
		{AccountID: general.ID, AmountCents: -2000},
		{AccountID: funded.ID, AmountCents: 2000},
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	// Two debits: the first affordable, the second not. The affordable one
	// is listed first, which is exactly what the old single-row read saw.
	pending, err := insertTransaction(ctx, f.pool, f.unitID, "trip_fund_transfer", "two debits", f.memberID, "pending_approval", "", []Posting{
		{AccountID: funded.ID, AmountCents: -1000},
		{AccountID: empty.ID, AmountCents: -1000},
		{AccountID: trip.ID, AmountCents: 2000},
	})
	if err != nil {
		t.Fatalf("insertTransaction: %v", err)
	}
	req, err := approval.Submit(ctx, f.pool, "ledger_transaction", pending.ID, f.unitID, f.memberID)
	if err != nil {
		t.Fatalf("approval.Submit: %v", err)
	}

	if err := approval.Decide(ctx, f.pool, req.ID, f.unitID, f.memberID, true); err == nil {
		t.Error("approval succeeded — the second debit overdraws an empty account and must be caught")
	}

	assertBalance(t, ctx, f.pool, empty.ID, 0, "empty scout account")
	assertBalance(t, ctx, f.pool, funded.ID, 2000, "funded scout account")
	assertBalance(t, ctx, f.pool, trip.ID, 0, "trip fund")
}

// TestSubmitExpenseForApproval_MovesNothingUntilAuthorized is the
// segregation-of-duties control: an expense over the unit's threshold is
// recorded but doesn't count until somebody other than the Treasurer
// signs it off.
func TestSubmitExpenseForApproval_MovesNothingUntilAuthorized(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	general, _ := EnsureUnitGeneralAccount(ctx, f.pool, f.unitID, f.memberID)
	external, _ := EnsureExternalAccount(ctx, f.pool, f.unitID, f.memberID)

	// Fund the general account so the expense is affordable.
	if _, err := PostTransaction(ctx, f.pool, f.unitID, "deposit", "seed", f.memberID, []Posting{
		{AccountID: external.ID, AmountCents: -100000},
		{AccountID: general.ID, AmountCents: 100000},
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	expense := []Posting{
		{AccountID: general.ID, AmountCents: -25000},
		{AccountID: external.ID, AmountCents: 25000},
	}
	txn, req, err := SubmitExpenseForApproval(ctx, f.pool, f.unitID, "New tents", f.memberID, expense)
	if err != nil {
		t.Fatalf("SubmitExpenseForApproval: %v", err)
	}
	if txn.Status != "pending_approval" {
		t.Errorf("status = %q, want pending_approval", txn.Status)
	}
	if txn.ApprovalRequestID == "" {
		t.Error("the transaction should carry its approval request id")
	}

	assertBalance(t, ctx, f.pool, general.ID, 100000, "general fund while the expense waits")

	// It shows up for the leader, and is kept off the Treasurer's
	// trip-fund transfer queue.
	pendingExpenses, err := PendingExpensesForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatalf("PendingExpensesForUnit: %v", err)
	}
	if len(pendingExpenses) != 1 || pendingExpenses[0].ID != txn.ID {
		t.Fatalf("PendingExpensesForUnit returned %d rows, want the one submitted expense", len(pendingExpenses))
	}
	transfers, err := PendingTransfersForUnit(ctx, f.pool, f.unitID)
	if err != nil {
		t.Fatalf("PendingTransfersForUnit: %v", err)
	}
	for _, tr := range transfers {
		if tr.ID == txn.ID {
			t.Error("a pending expense should not appear in the Treasurer's transfer queue")
		}
	}

	if err := approval.Decide(ctx, f.pool, req.ID, f.unitID, f.memberID, true); err != nil {
		t.Fatalf("authorizing: %v", err)
	}
	assertBalance(t, ctx, f.pool, general.ID, 75000, "general fund once the expense is authorized")
}

// TestSubmitExpenseForApproval_DeclinedNeverCounts is the mirror image.
func TestSubmitExpenseForApproval_DeclinedNeverCounts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	general, _ := EnsureUnitGeneralAccount(ctx, f.pool, f.unitID, f.memberID)
	external, _ := EnsureExternalAccount(ctx, f.pool, f.unitID, f.memberID)
	if _, err := PostTransaction(ctx, f.pool, f.unitID, "deposit", "seed", f.memberID, []Posting{
		{AccountID: external.ID, AmountCents: -50000},
		{AccountID: general.ID, AmountCents: 50000},
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	_, req, err := SubmitExpenseForApproval(ctx, f.pool, f.unitID, "Not approved", f.memberID, []Posting{
		{AccountID: general.ID, AmountCents: -20000},
		{AccountID: external.ID, AmountCents: 20000},
	})
	if err != nil {
		t.Fatalf("SubmitExpenseForApproval: %v", err)
	}
	if err := approval.Decide(ctx, f.pool, req.ID, f.unitID, f.memberID, false); err != nil {
		t.Fatalf("declining: %v", err)
	}
	assertBalance(t, ctx, f.pool, general.ID, 50000, "general fund after the expense was declined")
}
