-- 0006_ledger.sql
-- Phase 2: the fund-accounting core — internal/ledger. Per
-- scout-website-requirements.md Section 3.4: separate books per unit, a
-- Treasurer role with full access to those books, fundraiser tracking with
-- a configurable (not hardcoded) per-scout allocation cap, "push" transfers
-- from a Scout's individual account into a trip-specific fund tied to a
-- calendar event, and robust reconciliation (every dollar accounted for).
--
-- Design: a real double-entry ledger, not a single running-balance column
-- per account. Every transaction is a set of postings that must sum to
-- zero (enforced below by a deferred constraint trigger, not just
-- application code) — money can move between accounts but can never simply
-- appear or disappear, which is the actual meaning of "every dollar
-- accounted for." Amounts are stored as bigint cents throughout, never a
-- floating-point type, to avoid rounding error in money math.

-- ---------------------------------------------------------------------------
-- Accounts — one row per "book" a unit's money can sit in.
-- ---------------------------------------------------------------------------

-- 'external' is the contra-account for real money crossing the ledger's
-- boundary — a deposit credits some account and debits 'external'; an
-- expense debits some account and credits 'external' — so every deposit
-- and expense is still a proper, balanced double-entry transaction
-- without needing an actual bank-integration account. One consequence
-- worth knowing: 'external's balance is always exactly the negative of
-- the sum of every other account's balance in the unit (the whole ledger
-- always sums to zero, by construction, via the balance-check trigger
-- below), so -1 * the external account's balance is the unit's true total
-- holdings across every book combined — a built-in reconciliation figure,
-- not something that needs separate computing. See internal/ledger's
-- PostTransaction/EnsureExternalAccount.
CREATE TYPE ledger_account_type AS ENUM ('unit_general', 'scout_individual', 'trip_fund', 'external');
CREATE TYPE ledger_account_status AS ENUM ('open', 'closed');

CREATE TABLE ledger_accounts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id      uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    account_type ledger_account_type NOT NULL,
    member_id    uuid REFERENCES members(id) ON DELETE CASCADE, -- set only for scout_individual
    event_id     uuid REFERENCES events(id) ON DELETE SET NULL, -- set only for trip_fund; kept even if the calendar event is later deleted, so the fund's own history survives — hence SET NULL, not CASCADE
    name         text NOT NULL,
    status       ledger_account_status NOT NULL DEFAULT 'open',
    created_by   uuid NOT NULL REFERENCES members(id),
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT ledger_accounts_member_id_shape CHECK (
        (account_type = 'scout_individual' AND member_id IS NOT NULL) OR
        (account_type != 'scout_individual' AND member_id IS NULL)
    ),
    CONSTRAINT ledger_accounts_event_id_shape CHECK (
        (account_type = 'trip_fund') OR (event_id IS NULL)
    )
);
CREATE INDEX idx_ledger_accounts_unit_id ON ledger_accounts(unit_id);

-- One general fund per unit.
CREATE UNIQUE INDEX idx_ledger_accounts_one_general_per_unit
    ON ledger_accounts(unit_id) WHERE account_type = 'unit_general';
-- One external (deposits/withdrawals) contra-account per unit.
CREATE UNIQUE INDEX idx_ledger_accounts_one_external_per_unit
    ON ledger_accounts(unit_id) WHERE account_type = 'external';
-- One individual account per Scout per unit (a Scout who somehow has roles
-- in both units — unusual but not impossible for an older Scout who's also
-- registered with the Pack as a den chief helper, say — gets one per unit,
-- matching "separate books per unit").
CREATE UNIQUE INDEX idx_ledger_accounts_one_per_scout_per_unit
    ON ledger_accounts(unit_id, member_id) WHERE account_type = 'scout_individual';
-- One trip fund per calendar event.
CREATE UNIQUE INDEX idx_ledger_accounts_one_per_event
    ON ledger_accounts(unit_id, event_id) WHERE account_type = 'trip_fund';

-- ---------------------------------------------------------------------------
-- Transactions & postings — the double-entry core.
-- ---------------------------------------------------------------------------

-- 'pending_approval' is for a trip-fund push transfer awaiting Treasurer
-- sign-off (see internal/approval — reused unchanged from Phase 1, per
-- scout-website-architecture-phase1.md Section 5). Its postings exist from
-- the moment the transaction is created (so the balance-check trigger below
-- can validate them), but every balance/statement query filters to
-- status = 'posted', so a pending transfer has zero effect on anyone's
-- balance until a Treasurer approves it.
CREATE TYPE ledger_transaction_status AS ENUM ('posted', 'pending_approval', 'rejected');

CREATE TABLE ledger_transactions (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id              uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    transaction_type     text NOT NULL, -- e.g. 'deposit', 'expense', 'manual_adjustment', 'fundraiser_allocation', 'trip_fund_transfer' — free text like approval_requests.entity_type, deliberately not an enum so a new transaction type never needs a migration
    description          text NOT NULL,
    status               ledger_transaction_status NOT NULL DEFAULT 'posted',
    occurred_at          timestamptz NOT NULL DEFAULT now(), -- when the money actually moved (or, for a pending transfer, was requested)
    created_by           uuid NOT NULL REFERENCES members(id),
    approval_request_id  uuid REFERENCES approval_requests(id),
    created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_ledger_transactions_unit_id ON ledger_transactions(unit_id, occurred_at);
CREATE INDEX idx_ledger_transactions_status ON ledger_transactions(unit_id, status);

CREATE TABLE ledger_postings (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id uuid NOT NULL REFERENCES ledger_transactions(id) ON DELETE CASCADE,
    account_id     uuid NOT NULL REFERENCES ledger_accounts(id),
    amount_cents   bigint NOT NULL, -- signed: positive credits (increases) the account, negative debits (decreases) it
    created_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT ledger_postings_amount_not_zero CHECK (amount_cents != 0)
);
CREATE INDEX idx_ledger_postings_transaction_id ON ledger_postings(transaction_id);
CREATE INDEX idx_ledger_postings_account_id ON ledger_postings(account_id);

-- The actual "every dollar accounted for" guarantee: every transaction's
-- postings must sum to exactly zero. Deferred to end-of-transaction so the
-- application can insert a transaction's postings one at a time (or update
-- them) within a single database transaction and only gets checked once,
-- at COMMIT — see internal/ledger.PostTransaction.
CREATE OR REPLACE FUNCTION ledger_check_transaction_balance() RETURNS trigger AS $$
DECLARE
    total bigint;
    affected_transaction_id uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN
        affected_transaction_id := OLD.transaction_id;
    ELSE
        affected_transaction_id := NEW.transaction_id;
    END IF;

    SELECT COALESCE(SUM(amount_cents), 0) INTO total
    FROM ledger_postings WHERE transaction_id = affected_transaction_id;

    IF total <> 0 THEN
        RAISE EXCEPTION 'ledger_postings for transaction % do not balance (sum = % cents)', affected_transaction_id, total;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER ledger_postings_balance
    AFTER INSERT OR UPDATE OR DELETE ON ledger_postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_check_transaction_balance();

-- ---------------------------------------------------------------------------
-- Fundraisers — proceeds tracked, with a configurable (percentage OR fixed
-- dollar amount per item — see the compliance flag in
-- scout-website-requirements.md Section 3.4) share credited to individual
-- Scout accounts.
-- ---------------------------------------------------------------------------

CREATE TYPE fundraiser_allocation_mode AS ENUM ('percentage', 'fixed_per_item');

CREATE TABLE fundraisers (
    id                        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id                   uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    name                      text NOT NULL,
    allocation_mode           fundraiser_allocation_mode NOT NULL,
    allocation_percent        numeric(5,2), -- e.g. 10.00 = 10%; set only when allocation_mode = 'percentage'
    allocation_fixed_cents    bigint,       -- e.g. 200 = $2.00/item; set only when allocation_mode = 'fixed_per_item'
    -- Defaults to true on every new fundraiser: this project's original
    -- requirements doc flags the actual allowed percentage as something
    -- that must be confirmed with your council/chartered organization
    -- (IRS "private benefit" rules for tax-exempt orgs), not something
    -- this codebase should assume. The admin UI surfaces this flag as a
    -- visible "needs council confirmation" banner until a leader
    -- explicitly clears it (see internal/ledger.ConfirmFundraiserCap).
    needs_council_confirmation boolean NOT NULL DEFAULT true,
    created_by                uuid NOT NULL REFERENCES members(id),
    created_at                timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fundraisers_allocation_shape CHECK (
        (allocation_mode = 'percentage' AND allocation_percent IS NOT NULL AND allocation_fixed_cents IS NULL) OR
        (allocation_mode = 'fixed_per_item' AND allocation_fixed_cents IS NOT NULL AND allocation_percent IS NULL)
    )
);
CREATE INDEX idx_fundraisers_unit_id ON fundraisers(unit_id);

-- One row per Scout credited from a given fundraiser. gross_amount_cents is
-- the total proceeds attributed to that Scout (e.g. their popcorn sales
-- total); credited_cents is what the fundraiser's rule actually allocates
-- to their individual account, computed once at creation time from the
-- fundraiser's rule as it existed then — a later edit to the fundraiser's
-- percentage does not retroactively change past allocations, same
-- principle as an audit log never being rewritten after the fact.
CREATE TABLE fundraiser_allocations (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    fundraiser_id          uuid NOT NULL REFERENCES fundraisers(id) ON DELETE CASCADE,
    member_id              uuid NOT NULL REFERENCES members(id),
    gross_amount_cents     bigint NOT NULL,
    quantity               numeric(10,2), -- item count, only meaningful in fixed_per_item mode
    credited_cents         bigint NOT NULL,
    ledger_transaction_id  uuid NOT NULL REFERENCES ledger_transactions(id),
    created_by             uuid NOT NULL REFERENCES members(id),
    created_at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_fundraiser_allocations_fundraiser_id ON fundraiser_allocations(fundraiser_id);
CREATE INDEX idx_fundraiser_allocations_member_id ON fundraiser_allocations(member_id);
