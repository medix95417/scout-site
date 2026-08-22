-- 0032_fundraiser_storefront.sql
--
-- Lets a unit sell items for a fundraiser online: an admin-managed item
-- catalog and button image attached to an existing ledger.Fundraiser, a
-- public/member order-taking form, and an order queue that a leader
-- confirms as paid before it feeds the existing FundraiserAllocation
-- credit-to-Scout flow (internal/ledger.RecordFundraiserAllocation) —
-- crediting only once payment is actually confirmed, never the instant an
-- order is placed, since no online payment processor is wired up yet and
-- a unit's general fund must never be credited against money it hasn't
-- actually received.
--
-- storefront_enabled marks which single Fundraiser (if any) is the one
-- currently shown on the public storefront/homepage button for a unit —
-- enforced as "at most one at a time" by the partial unique index below,
-- with the application additionally doing the flip-others-off step in one
-- transaction (see ledger.SetStorefrontFundraiser) rather than relying on
-- the constraint alone to surface a friendly error.
ALTER TABLE fundraisers ADD COLUMN storefront_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE fundraisers ADD COLUMN button_image_url text NOT NULL DEFAULT '';

CREATE UNIQUE INDEX fundraisers_one_storefront_per_unit_uidx ON fundraisers(unit_id) WHERE storefront_enabled;

-- The sellable catalog for a fundraiser. Deliberately just name + price,
-- editable any time — see fundraiser_order_items below for why editing or
-- deleting an item here never touches past orders.
CREATE TABLE fundraiser_items (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    fundraiser_id uuid NOT NULL REFERENCES fundraisers(id) ON DELETE CASCADE,
    name         text NOT NULL,
    price_cents  bigint NOT NULL CHECK (price_cents > 0),
    sort_order   int NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX fundraiser_items_fundraiser_idx ON fundraiser_items(fundraiser_id, sort_order);

CREATE TYPE fundraiser_order_status AS ENUM ('pending', 'paid', 'credited', 'canceled');

-- One order = one buyer's purchase, taken either by the public (no
-- payment method yet — see README/roadmap) or by a logged-in
-- leader/parent on the buyer's behalf (created_by set in that case, null
-- for a genuinely anonymous public order). scout_name_entered is always
-- whatever the buyer typed in for "which Scout gets credit" — free text,
-- since a public visitor can't be trusted to pick a real roster row;
-- scout_member_id is filled in once that name is resolved to an actual
-- roster member (automatically on an unambiguous exact match, or by a
-- leader manually afterward), and is what RecordFundraiserAllocation
-- actually needs. status starts 'pending', moves to 'paid' once a leader
-- confirms payment was received, and only becomes 'credited' once both
-- paid and scout_member_id are set and the ledger credit has actually
-- posted — see ledger.MarkFundraiserOrderPaid / ResolveFundraiserOrderScout.
CREATE TABLE fundraiser_orders (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    fundraiser_id            uuid NOT NULL REFERENCES fundraisers(id) ON DELETE CASCADE,
    unit_id                  uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    buyer_name               text NOT NULL,
    buyer_email              text NOT NULL DEFAULT '',
    buyer_phone              text NOT NULL DEFAULT '',
    scout_name_entered       text NOT NULL,
    scout_member_id          uuid REFERENCES members(id) ON DELETE SET NULL,
    total_cents              bigint NOT NULL CHECK (total_cents > 0),
    status                   fundraiser_order_status NOT NULL DEFAULT 'pending',
    fundraiser_allocation_id uuid REFERENCES fundraiser_allocations(id) ON DELETE SET NULL,
    created_by               uuid REFERENCES members(id) ON DELETE SET NULL,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX fundraiser_orders_fundraiser_idx ON fundraiser_orders(fundraiser_id, created_at DESC);
CREATE INDEX fundraiser_orders_unit_idx ON fundraiser_orders(unit_id, created_at DESC);

-- A snapshot of each line item at order time (name + price copied, not
-- referenced live) so a later catalog price change or item deletion never
-- rewrites what a past buyer actually agreed to pay — the same
-- snapshot-at-time-of-record principle FundraiserAllocation already uses.
CREATE TABLE fundraiser_order_items (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id         uuid NOT NULL REFERENCES fundraiser_orders(id) ON DELETE CASCADE,
    item_name        text NOT NULL,
    unit_price_cents bigint NOT NULL CHECK (unit_price_cents > 0),
    quantity         int NOT NULL CHECK (quantity > 0)
);
CREATE INDEX fundraiser_order_items_order_idx ON fundraiser_order_items(order_id);
