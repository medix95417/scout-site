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
