-- 0023_unit_payment_settings.sql
--
-- Lets unit_settings hold a text value alongside its existing boolean
-- one, the same relaxation migration 0009 made to system_settings for
-- exactly the same reason: Stripe/PayPal credentials (see
-- internal/settings' per-unit payment settings) need an actual value, not
-- on/off, and need it scoped per unit — Troop and Pack connect their own,
-- separate payment processor accounts, matching how the treasury already
-- keeps fully separate books per unit (see internal/ledger). value is
-- relaxed to nullable so a text-setting row can leave it NULL rather than
-- needing a meaningless boolean placeholder (and vice versa: a boolean
-- toggle's value_text stays NULL).
ALTER TABLE unit_settings ALTER COLUMN value DROP NOT NULL;
ALTER TABLE unit_settings ADD COLUMN value_text text;
