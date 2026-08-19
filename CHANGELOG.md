# Changelog

All notable changes to this project are documented here, newest first.
Versions follow [Semantic Versioning](https://semver.org/) loosely (MAJOR
for breaking/schema changes that need a manual step beyond the normal
automatic migration, MINOR for new features, PATCH for fixes to existing
behavior) and are tagged in git as `vX.Y.Z`.

**A note on the early history below:** this project didn't have
incremental commits until the `v1.4.0` catch-up commit — everything from
`v1.0.0`'s initial scaffold through `v1.3.0` was built and delivered
across several conversations as zip files, without ever being committed
to git in between. The entries for `v1.0.1` through `v1.3.0` are
reconstructed from the project's own docs (`SECURITY_AUDIT.md`,
`PHASE2_TREASURY.md`, `DEMO_DATA.md`) and are accurate about *what*
shipped, but don't have reliable individual dates — they're grouped under
the date range between the `v1.0.0` commit and the `v1.4.0` catch-up
commit. Every version from `v1.4.0` onward is a real, individually
tagged commit with an accurate date.

## [Unreleased]

Nothing yet.

## [1.5.0] — 2026-08-19

Six Phase 3 items from `README.md`'s "Not in Phase 1" list, plus a
production-outage fix.

**Trip-fund closeout.** A Treasurer can now close a trip fund once its
balance is exactly zero — they move any remainder out via the existing
transaction/transfer paths first, rather than this silently sweeping or
stranding money as a side effect of closing (`internal/ledger.CloseTripFund`).
Once closed, further postings against it are rejected the same as any
other closed account.

**Bulk fundraiser proceeds import.** A Treasurer can paste rows copied
from a vendor spreadsheet or `.csv` (name, gross amount, and quantity for
fixed-per-item fundraisers) into `/treasury/fundraisers/{id}` instead of
the one-Scout-at-a-time form. Rows are matched to the roster by name;
unmatched/ambiguous/invalid rows are skipped and reported with a reason
rather than blocking the rows that are fine.

**Newsletter email.** A leader can draft, edit, and send a plain-text
newsletter (`/admin/newsletters`) to every family currently on a unit's
roster, via the existing SMTP mailer. One-way draft→sent transition — no
re-editing or re-sending once it's gone out.

**Scoutbook/spreadsheet roster CSV import.** A leader pastes rows
exported from Scoutbook (or any spreadsheet) at `/admin/roster/import` to
bulk-add families/members instead of one at a time. Header-driven column
matching, groups rows into families by name, matches existing logins by
email, and de-duplicates by name within a resolved family so re-running
the same import is safe.

**Digital permission slips / consent forms.** A leader attaches a
consent form to a calendar event (`/calendar/{id}/permission-slip`); a
parent/guardian signs it once per Scout of theirs attending — per
participant, not per family, matching BSA consent norms — by typing
their name. Leaders get a live compliance roster; editing a slip's text
never invalidates signatures already collected.

**Rank/badge advancement tracking.** `/advancement` (members-only) shows
every family the unit's rank/badge history; `/admin/advancement` lets a
leader record one earned rank/badge at a time or bulk-paste many at
once. The `advancement_records` table has existed since the Phase 1
schema (`0001_init.sql`) — this is the first thing to actually populate
and display it.

**Fix — production outage:** a `POSTGRES_PASSWORD` containing a `/` (a
character `openssl rand -base64 N` can and does produce) broke the
database connection entirely — `docker-compose.yml` spliced the raw
password into a `postgres://` URL with no escaping, which pgx's URL
parser then rejected (`invalid port ... after host`). The `app`
container crash-looped on startup as a result, which is also why
Caddy's reverse proxy logged `dial tcp: lookup app ... server
misbehaving` and returned 502s to every visitor — Docker's embedded DNS
had nothing to resolve `app` to. The database connection string is now
built by the app itself (`internal/config.resolveDatabaseURL`) from
separate `DB_HOST`/`DB_PORT`/`DB_USER`/`DB_PASSWORD`/`DB_NAME`/
`DB_SSLMODE` values via `net/url`, which escapes correctly regardless of
what characters end up in the password. An explicit `DATABASE_URL`
environment variable still overrides this entirely, for anyone pointing
at an external managed Postgres.

- **Migration:** `0010_newsletters.sql`, `0011_permission_slips.sql` —
  apply automatically on next server start, no manual step needed.
  Advancement tracking needed no migration; `advancement_records` has
  existed since `0001_init.sql`.
- **Deploy note:** the `app` service in `docker-compose.yml` now expects
  `DB_HOST`/`DB_PORT`/`DB_USER`/`DB_PASSWORD`/`DB_NAME`/`DB_SSLMODE`
  instead of a pre-built `DATABASE_URL` — already updated if you're
  pulling this compose file fresh; no `.env` changes needed since
  `POSTGRES_PASSWORD` still flows through the same way.

## [1.4.0] — 2026-08-18

**Individual Scout logins.** A login used to always represent a whole
family (one shared email/password for everyone in it). `users` gained a
nullable `member_id` so one specific member — most usefully a Scout
registered in either unit, or both — can now have their own login,
separate from their family's shared one. An individual login sees just
its own stuff: its own roles, not the family's union of everyone's roles;
itself as the acting identity for RSVPs/content edits, not a guessed
"most likely" family member; its own ledger account, not every child's.
Created and reset from `/admin/roster` → a member's detail page →
"Individual Login" (works for any member type, not just adults).

**Account visibility.** New `/accounts` page ("My Accounts" in the nav,
any logged-in login) shows Scout ledger balances without needing the
Treasurer role — previously the only place an account balance rendered
was the Treasurer-only `/treasury` dashboard.

**Easier-to-find password resets.** Self-service "forgot password" and a
leader-triggered reset both already existed but were hard to find — added
a direct "Reset Password" link on the roster list itself (previously
buried on one member's detail sub-page), and extended the reset flow to
cover an individual login's password too, not just the family-wide one.

**SMTP settings in Site Settings.** Host/port/username/from are now
editable from `/admin/settings` instead of only via environment variables
and a restart — resolved at send time with the `SMTP_*` environment
variables as fallback. `SMTP_PASSWORD` deliberately stays
environment-variable-only; see `SECURITY_AUDIT.md` for why.

- **Migration:** `0009_member_logins_and_text_settings.sql` (nullable
  `member_id` on `users`; `system_settings` gains `value_text` and
  `value` becomes nullable) — applies automatically on next server start,
  no manual step needed.
- **Docs:** `README.md`, `DEPLOY.md`, `DEMO_DATA.md`, `SECURITY_AUDIT.md`
  updated.

## [1.3.0] — between 2026-08-15 and 2026-08-18

**News feed & photo galleries.** Public `/news` and `/gallery` pages,
built on the same `content_pages` table the homepage sections already
used. Editable from `/admin/news` / `/admin/gallery` by the same leader
roles that can edit the homepage — draft → published workflow, public or
members-only visibility.

**Activity Log filtering & CSV export.** `/audit` can now be filtered by
date range, "function" (which part of the site), and person, and exported
as CSV with the same filter applied (`/audit/export.csv`).

**Bugfix:** building the filter surfaced a real gap — roster/role changes
(adding a family, adding a member, granting or removing a role) were
being logged to the audit table since Phase 1 but were never included in
what `/audit` actually queried for, so they silently never appeared. Now
fixed; see `SECURITY_AUDIT.md`'s "Later finding" entry.

## [1.2.0] — between 2026-08-15 and 2026-08-18

**Phase 2: fund accounting** (see `PHASE2_TREASURY.md` for the full
writeup):

- A double-entry ledger (`internal/ledger`) — unit general funds, per-Scout
  individual accounts, per-event trip funds.
- A `treasurer` role and a `/treasury` dashboard: deposits, expenses,
  transfers, trip funds, and approving trip-fund push-transfer requests.
- A per-account statement page a family can also reach for their own
  Scout's account, without needing Treasurer access.
- Fundraiser tracking with a configurable proceeds-allocation rule
  (percentage or fixed-per-item), flagged "needs council confirmation"
  until a Treasurer sets the real, council-approved number.
- Mandatory TOTP two-factor login for Treasurer/super_admin logins, QR
  code enrollment (plus manual setup-key entry), and **voluntary**
  two-factor available to every other login via a "Security" nav link.
- `/admin/settings` (super_admin only) for site-wide configuration
  toggles, backed by a small generic `internal/settings` package.
- `-seed-demo`: a full set of test logins (one per role) plus realistic
  calendar/ledger/fundraiser/approval activity, for clicking through every
  permission tier without hand-creating accounts (see `DEMO_DATA.md`).
- **Fix:** `bootstrap.go`'s role validation was missing `treasurer` from
  its allowed-roles list.

## [1.0.1] — between 2026-08-15 and 2026-08-18

**Phase 1 security hardening** (see `SECURITY_AUDIT.md` for the full
report): fixed a critical cross-unit authorization bug (a unit-wide leader
could manage members, and reset passwords, outside their own unit), an
approval-decision cross-unit bug, an RSVP cross-unit bug, and password
reset not invalidating existing sessions. Added login lockout after
repeated failures, password-reset request rate limiting, CSRF protection
on every form, and restricted `/audit` to leaders only (previously
reachable by anyone logged in).

## [1.0.0] — 2026-08-15

Initial Phase 1 scaffold: single sign-on across both subdomains,
self-service roster management, calendar with an SPL/Patrol-Leader
submit-for-approval workflow, an editable homepage, and an activity log.
Commit `09a776f`.
