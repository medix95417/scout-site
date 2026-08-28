-- Bank reconciliation: the monthly tick-off of the unit's own books
-- against what the bank says. A reconciliation names one account (in
-- practice the unit's general fund — the one the checking account
-- mirrors), a statement date, and the closing balance printed on that
-- statement. The Treasurer then ticks off the postings that actually
-- appear on the statement; whatever stays unticked is an outstanding
-- check or a deposit in transit and carries over to next month.
--
-- The reconciliation can only be marked done when the difference is
-- exactly zero, which is the whole point of the exercise: a non-zero
-- difference means the books and the bank disagree and somebody has to
-- find out why before signing off.

CREATE TYPE bank_reconciliation_status AS ENUM ('open', 'completed');

CREATE TABLE bank_reconciliations (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    unit_id               uuid NOT NULL REFERENCES units(id) ON DELETE CASCADE,
    account_id            uuid NOT NULL REFERENCES ledger_accounts(id) ON DELETE CASCADE,
    statement_date        date NOT NULL,
    -- Frozen at start time from the previous completed reconciliation's
    -- closing balance (0 for the very first one), so that editing an old
    -- reconciliation can never silently move this month's starting point.
    opening_balance_cents bigint NOT NULL,
    closing_balance_cents bigint NOT NULL,
    status                bank_reconciliation_status NOT NULL DEFAULT 'open',
    created_by            uuid NOT NULL REFERENCES members(id),
    created_at            timestamptz NOT NULL DEFAULT now(),
    completed_by          uuid REFERENCES members(id),
    completed_at          timestamptz,

    -- A completed reconciliation must say who signed it off and when; an
    -- open one must not claim either.
    CONSTRAINT bank_reconciliations_completion_consistent CHECK (
        (status = 'completed' AND completed_by IS NOT NULL AND completed_at IS NOT NULL)
        OR
        (status = 'open' AND completed_by IS NULL AND completed_at IS NULL)
    )
);

-- One open reconciliation per account at a time. Two half-finished
-- reconciliations of the same account would compete for the same
-- uncleared postings and neither would mean anything.
CREATE UNIQUE INDEX bank_reconciliations_one_open_per_account
    ON bank_reconciliations (account_id)
    WHERE status = 'open';

CREATE INDEX bank_reconciliations_unit_idx
    ON bank_reconciliations (unit_id, statement_date DESC);

-- A posting is "cleared" once it belongs to a reconciliation. Nullable:
-- everything already in the ledger starts out uncleared, which is the
-- correct starting state.
ALTER TABLE ledger_postings
    ADD COLUMN reconciliation_id uuid REFERENCES bank_reconciliations(id) ON DELETE SET NULL;

CREATE INDEX ledger_postings_reconciliation_idx
    ON ledger_postings (reconciliation_id)
    WHERE reconciliation_id IS NOT NULL;
