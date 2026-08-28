-- 0038_transaction_reversal.sql
--
-- A posted transaction is immutable, and should stay that way: no
-- application code updates or deletes a posting, which is exactly what
-- makes these books trustworthy. What was missing is the other half —
-- a sanctioned way to CORRECT one.
--
-- Until now a treasurer who mis-entered a deposit could only post an
-- unrelated manual_adjustment, leaving two entries with nothing tying
-- them together. A reversal instead posts the equal-and-opposite entry
-- and records which transaction it undoes, so both stay on the books
-- (what an auditor wants to see) and a statement can show them as a
-- matched pair rather than two unexplained movements.
ALTER TABLE ledger_transactions
    ADD COLUMN reverses_transaction_id uuid REFERENCES ledger_transactions(id);

-- One reversal per transaction. Without this, double-clicking "reverse"
-- would post the correction twice and leave the account off by the
-- original amount in the other direction.
CREATE UNIQUE INDEX idx_ledger_transactions_one_reversal_per_transaction
    ON ledger_transactions(reverses_transaction_id)
    WHERE reverses_transaction_id IS NOT NULL;
