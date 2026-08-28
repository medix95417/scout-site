package ledger

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// This file is the online storefront layer on top of the existing
// Fundraiser/FundraiserAllocation types in ledger.go: a per-fundraiser
// item catalog and homepage button image, which single fundraiser (if
// any) is currently shown as the public storefront for a unit, and the
// order queue itself. It deliberately never credits a Scout's ledger
// account the instant an order is placed — see creditOrderIfReady — since
// no online payment processor exists yet and crediting ahead of actually
// receiving the money would risk a phantom balance in the unit's general
// fund.

const (
	// MaxOrderItemQuantity bounds a single line on a storefront order.
	// Generous for popcorn or wreaths, small enough that price × quantity
	// can't come near overflowing int64 — see CreateFundraiserOrder.
	MaxOrderItemQuantity = 10_000

	// MaxOrderTotalCents bounds a whole order at $1,000,000.
	MaxOrderTotalCents = 100_000_000
)

// FundraiserItem is one sellable line in a fundraiser's catalog.
type FundraiserItem struct {
	ID           string
	FundraiserID string
	Name         string
	PriceCents   int64
	SortOrder    int
}

// AddFundraiserItem appends a new item to a fundraiser's catalog, after
// whatever's already there.
func AddFundraiserItem(ctx context.Context, pool *pgxpool.Pool, fundraiserID, name string, priceCents int64) (FundraiserItem, error) {
	name = strings.TrimSpace(name)
	if name == "" || priceCents <= 0 {
		return FundraiserItem{}, fmt.Errorf("ledger: item needs a name and a positive price")
	}

	var sortOrder int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(sort_order) + 1, 0) FROM fundraiser_items WHERE fundraiser_id = $1`, fundraiserID).Scan(&sortOrder); err != nil {
		return FundraiserItem{}, err
	}

	var it FundraiserItem
	err := pool.QueryRow(ctx, `
		INSERT INTO fundraiser_items (fundraiser_id, name, price_cents, sort_order)
		VALUES ($1, $2, $3, $4)
		RETURNING id, fundraiser_id, name, price_cents, sort_order
	`, fundraiserID, name, priceCents, sortOrder).Scan(&it.ID, &it.FundraiserID, &it.Name, &it.PriceCents, &it.SortOrder)
	return it, err
}

// UpdateFundraiserItem changes an existing item's name/price. Past orders
// keep whatever name/price they snapshotted at the time — see
// FundraiserOrderItem — so this never rewrites history.
func UpdateFundraiserItem(ctx context.Context, pool *pgxpool.Pool, itemID, name string, priceCents int64) error {
	name = strings.TrimSpace(name)
	if name == "" || priceCents <= 0 {
		return fmt.Errorf("ledger: item needs a name and a positive price")
	}
	_, err := pool.Exec(ctx, `UPDATE fundraiser_items SET name = $1, price_cents = $2 WHERE id = $3`, name, priceCents, itemID)
	return err
}

// DeleteFundraiserItem removes an item from the catalog. Orders that
// already sold it are unaffected, since their fundraiser_order_items rows
// are independent snapshots, not references to this row.
func DeleteFundraiserItem(ctx context.Context, pool *pgxpool.Pool, itemID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM fundraiser_items WHERE id = $1`, itemID)
	return err
}

// GetFundraiserItem looks up a single catalog item, e.g. so a caller can
// confirm it belongs to the fundraiser it claims to before editing it.
func GetFundraiserItem(ctx context.Context, pool *pgxpool.Pool, itemID string) (FundraiserItem, error) {
	var it FundraiserItem
	err := pool.QueryRow(ctx, `SELECT id, fundraiser_id, name, price_cents, sort_order FROM fundraiser_items WHERE id = $1`, itemID).
		Scan(&it.ID, &it.FundraiserID, &it.Name, &it.PriceCents, &it.SortOrder)
	return it, err
}

// ItemsForFundraiser lists a fundraiser's catalog in display order.
func ItemsForFundraiser(ctx context.Context, pool *pgxpool.Pool, fundraiserID string) ([]FundraiserItem, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, fundraiser_id, name, price_cents, sort_order FROM fundraiser_items
		WHERE fundraiser_id = $1 ORDER BY sort_order, name
	`, fundraiserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FundraiserItem
	for rows.Next() {
		var it FundraiserItem
		if err := rows.Scan(&it.ID, &it.FundraiserID, &it.Name, &it.PriceCents, &it.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SetFundraiserButtonImage changes the graphic used for a fundraiser's
// homepage storefront button — swappable independently of everything
// else so the button's look can change as what's being sold changes.
func SetFundraiserButtonImage(ctx context.Context, pool *pgxpool.Pool, fundraiserID, imageURL string) error {
	_, err := pool.Exec(ctx, `UPDATE fundraisers SET button_image_url = $1 WHERE id = $2`, strings.TrimSpace(imageURL), fundraiserID)
	return err
}

// SetStorefrontFundraiser sets which single fundraiser (if any) is shown
// as the unit's public storefront/homepage button, atomically turning off
// any other fundraiser's flag first — "one at a time" is an application
// rule, not just the database's partial unique index backing it up. Pass
// fundraiserID = "" with enabled = false to turn the storefront off
// entirely without picking a replacement.
func SetStorefrontFundraiser(ctx context.Context, pool *pgxpool.Pool, unitID, fundraiserID string, enabled bool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE fundraisers SET storefront_enabled = false WHERE unit_id = $1 AND storefront_enabled`, unitID); err != nil {
		return err
	}

	if enabled {
		if fundraiserID == "" {
			return fmt.Errorf("ledger: choose a fundraiser to enable the storefront for")
		}
		ct, err := tx.Exec(ctx, `UPDATE fundraisers SET storefront_enabled = true WHERE id = $1 AND unit_id = $2`, fundraiserID, unitID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return fmt.Errorf("ledger: fundraiser not found in this unit")
		}
	}

	return tx.Commit(ctx)
}

// ActiveStorefrontFundraiser returns the unit's current storefront
// fundraiser, if one is enabled — what the homepage button and the
// public order page both look up.
func ActiveStorefrontFundraiser(ctx context.Context, pool *pgxpool.Pool, unitID string) (Fundraiser, bool, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id FROM fundraisers WHERE unit_id = $1 AND storefront_enabled LIMIT 1`, unitID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Fundraiser{}, false, nil
	}
	if err != nil {
		return Fundraiser{}, false, err
	}
	f, err := GetFundraiser(ctx, pool, id)
	if err != nil {
		return Fundraiser{}, false, err
	}
	return f, true, nil
}

// FundraiserOrderItem is one snapshotted line of a FundraiserOrder — the
// item's name and price as they were at order time, copied rather than
// referenced, so a later catalog edit or deletion never changes what a
// past buyer actually agreed to pay.
type FundraiserOrderItem struct {
	ItemName       string
	UnitPriceCents int64
	Quantity       int
}

// FundraiserOrder is one buyer's order against a fundraiser's storefront —
// taken either directly from an anonymous public visitor (CreatedBy = "")
// or entered by a logged-in leader/parent on the buyer's behalf.
// ScoutNameEntered is always the free-text name the buyer gave for "which
// Scout gets credit"; ScoutMemberID is that name resolved to an actual
// roster member, filled in automatically on an unambiguous match or later
// by a leader, and is what actually drives a ledger credit. Status moves
// pending -> paid (a leader confirmed payment was received) -> credited
// (RecordFundraiserAllocation has posted) or -> canceled.
type FundraiserOrder struct {
	ID                     string
	FundraiserID           string
	UnitID                 string
	BuyerName              string
	BuyerEmail             string
	BuyerPhone             string
	ScoutNameEntered       string
	ScoutMemberID          string // "" until resolved
	TotalCents             int64
	Status                 string // "pending" | "paid" | "credited" | "canceled"
	FundraiserAllocationID string // "" until credited
	CreatedBy              string // "" for an anonymous public order
	CreatedAt              time.Time
	Items                  []FundraiserOrderItem
}

// CreateFundraiserOrder records a new order and its line items in one
// transaction. scoutMemberID may be "" when the buyer's typed name
// couldn't be confidently matched to exactly one roster member — the
// order is still recorded, just left for a leader to resolve later via
// ResolveFundraiserOrderScout.
func CreateFundraiserOrder(ctx context.Context, pool *pgxpool.Pool, fundraiserID, unitID, buyerName, buyerEmail, buyerPhone, scoutNameEntered, scoutMemberID string, items []FundraiserOrderItem, createdBy string) (FundraiserOrder, error) {
	buyerName = strings.TrimSpace(buyerName)
	scoutNameEntered = strings.TrimSpace(scoutNameEntered)
	if buyerName == "" || scoutNameEntered == "" {
		return FundraiserOrder{}, fmt.Errorf("ledger: order needs a buyer name and a Scout name")
	}
	if len(items) == 0 {
		return FundraiserOrder{}, fmt.Errorf("ledger: order needs at least one item")
	}

	// Quantities arrive from a public, unauthenticated form (see
	// web.FundraiserPlaceOrder), so they're bounded here rather than
	// trusted. Without an upper bound, price × quantity silently overflows
	// int64 and can wrap back to a positive number — a $1.00 item at
	// quantity 2e17 produces a "$15,532,559,262,904,483" order that sails
	// past a total > 0 check. Rejecting is the point: an order this size
	// is never real, and quietly clamping it would put a number nobody
	// typed in front of a treasurer.
	var total int64
	for _, it := range items {
		if it.UnitPriceCents <= 0 || it.UnitPriceCents > MaxOrderTotalCents {
			return FundraiserOrder{}, fmt.Errorf("ledger: every order item needs a positive, sensible price")
		}
		if it.Quantity <= 0 || it.Quantity > MaxOrderItemQuantity {
			return FundraiserOrder{}, fmt.Errorf("ledger: quantity for %q must be between 1 and %d", it.ItemName, MaxOrderItemQuantity)
		}
		total += it.UnitPriceCents * int64(it.Quantity)
		if total > MaxOrderTotalCents {
			return FundraiserOrder{}, fmt.Errorf("ledger: order total is larger than this system accepts")
		}
	}
	if total <= 0 {
		return FundraiserOrder{}, fmt.Errorf("ledger: order total must be positive")
	}

	var scoutMemberIDArg, createdByArg *string
	if scoutMemberID != "" {
		scoutMemberIDArg = &scoutMemberID
	}
	if createdBy != "" {
		createdByArg = &createdBy
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return FundraiserOrder{}, err
	}
	defer tx.Rollback(ctx)

	var o FundraiserOrder
	err = tx.QueryRow(ctx, `
		INSERT INTO fundraiser_orders (fundraiser_id, unit_id, buyer_name, buyer_email, buyer_phone, scout_name_entered, scout_member_id, total_cents, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, fundraiser_id, unit_id, buyer_name, buyer_email, buyer_phone, scout_name_entered, COALESCE(scout_member_id::text, ''), total_cents, status::text, COALESCE(fundraiser_allocation_id::text, ''), COALESCE(created_by::text, ''), created_at
	`, fundraiserID, unitID, buyerName, strings.TrimSpace(buyerEmail), strings.TrimSpace(buyerPhone), scoutNameEntered, scoutMemberIDArg, total, createdByArg).Scan(
		&o.ID, &o.FundraiserID, &o.UnitID, &o.BuyerName, &o.BuyerEmail, &o.BuyerPhone, &o.ScoutNameEntered, &o.ScoutMemberID, &o.TotalCents, &o.Status, &o.FundraiserAllocationID, &o.CreatedBy, &o.CreatedAt,
	)
	if err != nil {
		return FundraiserOrder{}, err
	}

	for _, it := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fundraiser_order_items (order_id, item_name, unit_price_cents, quantity)
			VALUES ($1, $2, $3, $4)
		`, o.ID, it.ItemName, it.UnitPriceCents, it.Quantity); err != nil {
			return FundraiserOrder{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return FundraiserOrder{}, err
	}
	o.Items = items

	audit.Log(ctx, pool, audit.Entry{EntityType: "fundraiser_order", EntityID: o.ID, ActorID: nonEmptyPtr(createdBy), Action: "create", After: o})
	return o, nil
}

// nonEmptyPtr returns nil for "" and &s otherwise — audit.Entry.ActorID
// is a *string so an anonymous public order can log with no actor.
func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func scanFundraiserOrder(row pgx.Row) (FundraiserOrder, error) {
	var o FundraiserOrder
	err := row.Scan(&o.ID, &o.FundraiserID, &o.UnitID, &o.BuyerName, &o.BuyerEmail, &o.BuyerPhone, &o.ScoutNameEntered, &o.ScoutMemberID, &o.TotalCents, &o.Status, &o.FundraiserAllocationID, &o.CreatedBy, &o.CreatedAt)
	return o, err
}

const fundraiserOrderColumns = `id, fundraiser_id, unit_id, buyer_name, buyer_email, buyer_phone, scout_name_entered, COALESCE(scout_member_id::text, ''), total_cents, status::text, COALESCE(fundraiser_allocation_id::text, ''), COALESCE(created_by::text, ''), created_at`

// GetFundraiserOrder loads one order with its line items.
func GetFundraiserOrder(ctx context.Context, pool *pgxpool.Pool, orderID string) (FundraiserOrder, error) {
	o, err := scanFundraiserOrder(pool.QueryRow(ctx, `SELECT `+fundraiserOrderColumns+` FROM fundraiser_orders WHERE id = $1`, orderID))
	if err != nil {
		return FundraiserOrder{}, err
	}
	items, err := orderItemsFor(ctx, pool, o.ID)
	if err != nil {
		return FundraiserOrder{}, err
	}
	o.Items = items
	return o, nil
}

// OrdersForFundraiser lists a fundraiser's orders newest-first, each with
// its line items loaded — an admin reconciliation/order-queue view, not a
// hot path, so the per-order items lookup isn't worth optimizing away.
func OrdersForFundraiser(ctx context.Context, pool *pgxpool.Pool, fundraiserID string) ([]FundraiserOrder, error) {
	rows, err := pool.Query(ctx, `SELECT `+fundraiserOrderColumns+` FROM fundraiser_orders WHERE fundraiser_id = $1 ORDER BY created_at DESC`, fundraiserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FundraiserOrder
	for rows.Next() {
		o, err := scanFundraiserOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		items, err := orderItemsFor(ctx, pool, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Items = items
	}
	return out, nil
}

func orderItemsFor(ctx context.Context, pool *pgxpool.Pool, orderID string) ([]FundraiserOrderItem, error) {
	rows, err := pool.Query(ctx, `SELECT item_name, unit_price_cents, quantity FROM fundraiser_order_items WHERE order_id = $1 ORDER BY item_name`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []FundraiserOrderItem
	for rows.Next() {
		var it FundraiserOrderItem
		if err := rows.Scan(&it.ItemName, &it.UnitPriceCents, &it.Quantity); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// memberDisplayName is a small local lookup rather than a dependency on
// internal/family — EnsureScoutAccount/RecordFundraiserAllocation already
// take a plain memberName string, so this just resolves one when the
// caller (a "mark paid" or "resolve scout match" action) only has a
// member ID.
func memberDisplayName(ctx context.Context, pool *pgxpool.Pool, memberID string) (string, error) {
	var first, last string
	if err := pool.QueryRow(ctx, `SELECT first_name, last_name FROM members WHERE id = $1`, memberID).Scan(&first, &last); err != nil {
		return "", err
	}
	return strings.TrimSpace(first + " " + last), nil
}

// creditOrderIfReady posts the ledger credit for an order via
// RecordFundraiserAllocation and moves it to 'credited', but only once
// both conditions this whole feature was built around are true: a leader
// has confirmed the order is actually paid, and the order's Scout has
// been resolved to a real roster member. Called after either half of that
// changes (MarkFundraiserOrderPaid, ResolveFundraiserOrderScout) so
// whichever happens second is the one that triggers the credit. A no-op,
// returning the order unchanged, if it isn't ready yet or was already
// credited.
func creditOrderIfReady(ctx context.Context, pool *pgxpool.Pool, orderID, actorID string) (FundraiserOrder, error) {
	o, err := GetFundraiserOrder(ctx, pool, orderID)
	if err != nil {
		return FundraiserOrder{}, err
	}
	if o.Status != "paid" || o.ScoutMemberID == "" || o.FundraiserAllocationID != "" {
		return o, nil
	}

	name, err := memberDisplayName(ctx, pool, o.ScoutMemberID)
	if err != nil {
		return FundraiserOrder{}, err
	}

	qty := 0
	for _, it := range o.Items {
		qty += it.Quantity
	}

	alloc, err := RecordFundraiserAllocation(ctx, pool, o.FundraiserID, o.ScoutMemberID, name, o.TotalCents, strconv.Itoa(qty), actorID)
	if err != nil {
		return FundraiserOrder{}, err
	}

	if _, err := pool.Exec(ctx, `UPDATE fundraiser_orders SET status = 'credited', fundraiser_allocation_id = $1, updated_at = now() WHERE id = $2`, alloc.ID, orderID); err != nil {
		return FundraiserOrder{}, err
	}

	o.Status = "credited"
	o.FundraiserAllocationID = alloc.ID
	audit.Log(ctx, pool, audit.Entry{EntityType: "fundraiser_order", EntityID: o.ID, ActorID: &actorID, Action: "credit", After: o})
	return o, nil
}

// MarkFundraiserOrderPaid confirms a leader/Treasurer actually received
// payment for an order. If the order's Scout match is already resolved
// this immediately posts the ledger credit (via creditOrderIfReady);
// otherwise it stops at 'paid' until ResolveFundraiserOrderScout supplies
// the match. A no-op if the order isn't currently 'pending'.
func MarkFundraiserOrderPaid(ctx context.Context, pool *pgxpool.Pool, orderID, actorID string) (FundraiserOrder, error) {
	if _, err := pool.Exec(ctx, `UPDATE fundraiser_orders SET status = 'paid', updated_at = now() WHERE id = $1 AND status = 'pending'`, orderID); err != nil {
		return FundraiserOrder{}, err
	}
	audit.Log(ctx, pool, audit.Entry{EntityType: "fundraiser_order", EntityID: orderID, ActorID: &actorID, Action: "mark_paid"})
	return creditOrderIfReady(ctx, pool, orderID, actorID)
}

// ResolveFundraiserOrderScout assigns (or corrects) which roster member
// gets credit for an order's free-text Scout name. If the order is
// already marked paid, this immediately posts the ledger credit.
func ResolveFundraiserOrderScout(ctx context.Context, pool *pgxpool.Pool, orderID, scoutMemberID, actorID string) (FundraiserOrder, error) {
	if scoutMemberID == "" {
		return FundraiserOrder{}, fmt.Errorf("ledger: choose a Scout")
	}
	if _, err := pool.Exec(ctx, `UPDATE fundraiser_orders SET scout_member_id = $1, updated_at = now() WHERE id = $2`, scoutMemberID, orderID); err != nil {
		return FundraiserOrder{}, err
	}
	audit.Log(ctx, pool, audit.Entry{EntityType: "fundraiser_order", EntityID: orderID, ActorID: &actorID, Action: "resolve_scout"})
	return creditOrderIfReady(ctx, pool, orderID, actorID)
}

// CancelFundraiserOrder withdraws an order that was placed by mistake or
// never actually paid for. Refuses once an order has been credited — at
// that point a real ledger transaction already moved money into a Scout's
// account, and undoing that is a manual Treasury adjustment, not a plain
// cancel.
func CancelFundraiserOrder(ctx context.Context, pool *pgxpool.Pool, orderID, actorID string) error {
	ct, err := pool.Exec(ctx, `UPDATE fundraiser_orders SET status = 'canceled', updated_at = now() WHERE id = $1 AND status IN ('pending', 'paid')`, orderID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("ledger: order is already credited or canceled")
	}
	audit.Log(ctx, pool, audit.Entry{EntityType: "fundraiser_order", EntityID: orderID, ActorID: &actorID, Action: "cancel"})
	return nil
}
