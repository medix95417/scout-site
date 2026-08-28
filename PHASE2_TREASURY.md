# Phase 2: Fund Accounting & Treasurer Role

This document explains what Phase 2 adds, the design decisions behind it,
and what's deliberately left for later. It assumes you've read
`README.md` and `scout-website-architecture-phase1.md` already.

Build order for this phase (your call): ledger + Treasurer role first,
payments last. TOTP two-factor for Treasurer/super_admin: yes. Fundraiser
allocation cap: no council number yet, so it ships as a clearly-flagged
placeholder. This document reflects those choices.

## What's new

- A double-entry ledger (`internal/ledger`) — every unit gets a general
  fund account; every Scout can get an individual account; every
  calendar event can get a tied trip fund.
- A `treasurer` role, addable via the existing roster admin UI (or the
  `-grant-role` CLI flag for a first foothold — see "Getting someone into
  the Treasurer role" below).
- A `/treasury` dashboard for recording deposits/expenses/transfers,
  creating trip funds, approving trip-fund transfer requests, and
  tracking fundraisers.
- A per-account statement page (`/treasury/accounts/{id}`) that a family
  can also reach for their own Scout's account, to check a balance and
  request a push transfer into an open trip fund — without needing
  Treasurer access themselves.
- Fundraiser tracking with a configurable proceeds-allocation rule
  (percentage or fixed-per-item), flagged "needs council confirmation"
  until a Treasurer sets the real number.
- Mandatory TOTP two-factor login (`internal/twofactor`, `/settings/2fa`)
  for any login that holds the Treasurer or super_admin role — and, as of
  this update, **optional** two-factor for every other login too, via a
  "Security" link in the nav everyone sees now (see "Two-factor
  authentication (TOTP)" below).
- QR code enrollment for TOTP, alongside the original manual setup-key
  entry — see "Two-factor authentication (TOTP)" below.
- Monthly bank reconciliation (`/treasury/reconciliations`) — tick the
  unit's own entries off against the bank statement, with sign-off gated
  on the difference reaching zero. See "Bank reconciliation" below.
- A `/admin/settings` page (super_admin only) for site-wide toggles —
  configuration that affects the whole install rather than one unit's
  content or books, editable without touching code. See "Site settings
  (`/admin/settings`)" below.

Not built yet, on purpose: real payment processing. See "What's
deliberately not here yet" below.

## The ledger: double-entry, integer cents, two independent balance checks

Every `ledger_transactions` row has two or more `ledger_postings` rows
(signed integer cents — positive credits an account, negative debits it)
that must sum to exactly zero. That's enforced twice, independently:

1. In Go, before any database write (`internal/ledger.insertTransaction`
   rejects an unbalanced posting set immediately).
2. In Postgres itself, by a deferred constraint trigger
   (`ledger_postings_balance`, see `internal/db/migrations/0006_ledger.sql`)
   that fires when the surrounding transaction commits. Even if the Go
   code had a bug, the database would still refuse to record money that
   doesn't balance.

Money is never a floating-point type once it's in the database — always
`bigint` cents. The one deliberate exception is `parseDollarsToCents` in
`internal/web/treasury.go`, where a user's typed dollar amount ("25.00")
is parsed as a float for one line before being rounded to an exact
integer — that's the single, intentional boundary where a float exists
at all.

### Account types

- **`unit_general`** — one per unit, the main fund.
- **`scout_individual`** — one per Scout per unit, tied to that Scout's
  `member_id`. Used for fundraiser credits and family-initiated trip-fund
  pushes.
- **`trip_fund`** — one per calendar event, tied to that event's `event_id`.
  Closed out once the trip is over (a manual admin step, not automated
  yet).
- **`external`** — a contra-account, one per unit, that never appears in
  the deposit/expense/transfer dropdowns on `/treasury`. This is the
  piece that makes deposits and expenses proper double-entry: a deposit
  credits the target account and debits `external`; an expense debits
  the source account and credits `external`. Real money entering or
  leaving the unit's bank account doesn't have a natural second
  *internal* account to balance against, so `external` stands in for
  "the outside world."

  This gives you a free reconciliation check: `-1 * external.balance`
  always equals the sum of every other account's balance, because the
  whole ledger sums to zero by construction. If those two numbers ever
  disagree, something is wrong at the database level, not just in the
  UI.

### Transaction statuses

- **`posted`** — immediately effective; counts toward every balance and
  statement.
- **`pending_approval`** — used only for trip-fund push transfers a
  family requests from their Scout's account. The postings exist from
  the moment the request is created, but every balance/statement query
  filters on `status = 'posted'`, so nothing counts until a Treasurer
  approves it. This mirrors how Phase 1's event RSVP approvals work —
  nothing is "real" until approved.
- **`rejected`** — a Treasurer declined the transfer; the postings stay
  in the table (for the audit trail) but never count toward anything.

## Trip-fund transfers reuse Phase 1's approval workflow

`scout-website-architecture-phase1.md` called this out explicitly: Phase
2 should reuse the generic approval-request mechanism (`internal/approval`)
unchanged, just with a new `entity_type` value. That's what happened —
`entity_type = "ledger_transaction"` sits alongside the existing
`entity_type = "event"` case Phase 1 already used for SPL/Patrol Leader
event approvals.

One change was needed to `approval.Decide` itself: it used to run the
approval-request update and the entity-specific side effect (flip an
event's status, or now, flip a ledger transaction's status) as two
separate, non-atomic database calls. Now that one of the entity types is
real money, that got wrapped in a single database transaction — either
both happen or neither does.

`approval.Decide` also re-checks the debiting account's balance at
decision time, not just at request time, since the balance could have
dropped in between (another transfer could have gone through first). If
the balance is no longer sufficient, approval fails with a new
`approval.ErrInsufficientFunds` and the request stays pending rather than
silently overdrawing an account.

Because Treasurer isn't part of Phase 1's calendar-approver role set, the
approve/reject buttons on `/treasury` post to a dedicated route
(`/treasury/transfers/{id}/decide`, gated on the Treasurer permission)
rather than reusing Phase 1's `/approvals/{id}/decide` route — both
routes end up calling the same underlying `approval.Decide`, just gated
by different roles.

## Fundraisers and the allocation-cap placeholder

BSA/IRS rules limit how much of a fundraiser's proceeds can be credited
to an individual Scout's account before it risks a unit's tax-exempt
"private benefit" standing — and that limit isn't something this
codebase should guess at. You told me you don't have that number from
your council yet, so every new fundraiser is created with
`needs_council_confirmation = true` by default, and both the fundraiser
list and the fundraiser detail page show a clearly-marked amber banner
until a Treasurer explicitly sets the real, council-confirmed rule via
the "Confirm Council-Approved Rule" form.

**Before crediting real fundraiser proceeds to Scout accounts, confirm
the actual allowed percentage or per-item amount with your council or
chartered organization, and set it on each fundraiser.** Nothing in this
codebase enforces a specific number — it only makes sure you can't miss
that a number needs setting.

Editing a fundraiser's rule after some proceeds have already been
credited doesn't rewrite those past allocations — each
`fundraiser_allocations` row records the amount that was actually
credited at the time, same principle as the audit log never being
rewritten.

## Two-factor authentication (TOTP)

Any login that holds the Treasurer or super_admin role — in *either* unit,
since single sign-on means one session already spans both
troop.47-yonkers.org and pack.47-yonkers.org — is required to set up
TOTP two-factor authentication (`internal/twofactor`, RFC 6238/RFC 4226,
pure Go standard library — `crypto/hmac`, `crypto/sha1`,
`encoding/base32`, no external dependency).

**As of this update, two-factor is also available to every other login,
as an opt-in.** It is *not* required for anyone outside Treasurer/
super_admin — you were clear you don't want it mandatory yet, just
discoverable, so a family that wants the extra security can turn it on
themselves. Every logged-in user now sees a "Security" link in the nav
(`base.html`, next to "Log out") that goes straight to `/settings/2fa`;
before this update that page existed but nothing pointed a non-Treasury
login to it.

**Enrollment now shows a scannable QR code, with manual entry kept as a
fallback.** `/settings/2fa` renders a QR code client-side using
[QRious](https://github.com/neocotic/qrious) (a small, dependency-free
canvas library), loaded from cdnjs — `qrious.min.js` v4.0.2 — the same
CDN-script pattern this project already uses for htmx and Tailwind, no
build step or bundler involved. The QR code encodes the same
`otpauth://` URI as before; scanning it with Google Authenticator, Authy,
1Password, Microsoft Authenticator, etc. sets everything up in one step.
Directly below the QR code, the page still shows the secret as a grouped
string (`ABCD EFGH IJKL MNOP`) and the raw `otpauth://` URI, for anyone
whose app can't scan (or who's setting this up on the same device the
authenticator app lives on) — "allow them to enter the seed manually if
needed" is preserved exactly, just no longer the only option.

Confirming enrollment generates 10 single-use backup codes (bcrypt-hashed
at rest, shown once, format `XXXXX-XXXXX`), for recovering access if the
device with the authenticator app is lost.

**A login with a treasury role but not yet enrolled is not locked out.**
Blocking login entirely until 2FA is set up creates a chicken-and-egg
problem (you can't log in to set up 2FA if login itself requires 2FA).
Instead, that login gets a persistent red banner on every page
("Your login can access unit funds — set up two-factor authentication to
keep it secure") linking to `/settings/2fa`, until they enroll. This is a
deliberate, documented trade-off for v1 — worth knowing about, not a bug.
A login without a treasury role that also hasn't enrolled sees no banner
by default — unless the new `require_two_factor_for_all` site setting is
turned on (see "Site settings" below), in which case everyone gets the
same nudge banner, worded generically rather than mentioning fund access.

Once enrolled and confirmed, login becomes two-step: password, then a
`/login/2fa` code-entry page — **for any enrolled login, not just
Treasurer/super_admin.** (Fixed as part of this update: the login-time
check used to only look at treasury roles, so a non-Treasury login that
voluntarily enrolled in TOTP was never actually asked for its code —
enrollment worked, but it was silently unenforced. Now, whether a code is
required at login depends solely on "is this login enrolled and
confirmed," not on role or on the site setting — the site setting only
ever affects who sees the *reminder banner* to enroll, never who's asked
for a code once they have.) That pending state is capped at 5 wrong
attempts within a 5-minute window before the pending login is discarded
and the visitor has to start over from `/login` — specifically to
prevent brute-forcing a 6-digit code.

## Site settings (`/admin/settings`)

A new page, visible only to logins with the `super_admin` role, for
configuration that applies across the whole site rather than one unit's
content or books — the kind of thing that used to mean asking me to
change code and redeploy. It's deliberately generic: a small
`internal/settings` package holds a list of toggles (currently one — see
below), each backed by a row in a new `system_settings` table
(`internal/db/migrations/0008_system_settings.sql`). A toggle a visitor
hasn't touched yet defaults to that toggle's documented default (`false`
for the one shipped here) rather than needing a row to exist first.
Adding a future toggle is one Go struct literal in
`internal/settings/settings.go` — no new page, no migration required
unless the toggle needs its own supporting data.

The one toggle shipped in this update:

- **`require_two_factor_for_all`** (default: off) — when on, every
  logged-in user who hasn't enrolled in two-factor sees the reminder
  banner that used to be Treasurer/super_admin-only. It does **not**
  make 2FA mandatory to log in — nobody gets locked out, per the
  non-lockout design above — it only widens who gets nudged toward
  enrolling. This matches what you asked for: "I do not want to make it
  mandatory, but having it as an option will show that we are looking
  for security."

Every toggle flip is recorded in the audit log (`entity_type =
"system_setting"`) and shows up on **both** units' `/audit` Activity Log
pages, since a site-wide change is relevant to Troop and Pack alike —
`system_settings` has no `unit_id` to scope it to one or the other.

To try it: log in as the seeded Super Admin (Alex — see `DEMO_DATA.md`),
click "Site Settings" in the nav, and flip `require_two_factor_for_all`.

## Bank reconciliation

Everything else in the treasury is about money moving correctly *within*
the books. Reconciliation is the one place the books get checked against
an outside source of truth, which is what actually catches a deposit that
was never recorded, an entry keyed twice, or a check nobody ever cashed.
For a small non-profit it's also the single most useful control available,
because it needs no extra staff — just someone doing it every month when
the statement arrives.

The model is the standard one a bookkeeper already knows:

    opening balance      (last statement's closing balance)
    + cleared postings   (the entries ticked off as on this statement)
    = cleared balance

    statement closing balance − cleared balance = difference

and a reconciliation **cannot be marked complete until that difference is
exactly zero**. That refusal is the entire point: a non-zero difference
means the books and the bank disagree and somebody has to find out why
before signing off. Entries left unticked are outstanding checks and
deposits in transit — normal, not errors — and they carry over to next
month automatically.

A few decisions worth knowing about:

- **Reconciling never changes the books.** The only effect on the ledger
  is stamping postings with the reconciliation that cleared them. If a
  reconciliation won't balance because the books are actually wrong, the
  fix is an ordinary correcting transaction on `/treasury`, which leaves
  both the error and the correction visible — never an edit made quietly
  in the course of reconciling.
- **A completed reconciliation is immutable and undeletable.** It's the
  record that the books were checked against the bank on a given date,
  which is exactly what a reviewer asks to see. An unfinished one can be
  discarded, which releases everything it had ticked.
- **The opening balance is frozen when the reconciliation starts**, taken
  from the previous completed one, so a later change elsewhere can't
  silently shift this period's starting point.
- **One open reconciliation per account at a time**, enforced by a partial
  unique index rather than a check-then-insert, so two treasurers starting
  one at the same moment can't both succeed.
- **Only posted transactions can be ticked.** An expense still waiting on
  the Cubmaster's authorization hasn't hit the books, so it can't have hit
  the bank either.
- **Only the unit's general fund is offered.** Individual Scout accounts
  and trip funds are subdivisions of the same real-world money, and
  `external` is the offsetting side of every deposit rather than an
  account anyone banks.

The rules live in `internal/ledger/reconciliation.go` with no HTTP code,
same separation as the rest of the package; `internal/web/treasury_reconciliation.go`
is only the UI. Both are Treasurer/super_admin only.

## What's deliberately not here yet

**Real payment processing (Stripe).** You picked ledger-and-Treasurer-role
first, payments last, so nothing in this delivery talks to Stripe or any
other processor. Every dollar in the ledger right now gets there by a
Treasurer manually recording a deposit or expense against real-world
activity (cash, check, a transfer they made themselves) — there's no
"pay online" flow yet for families. When you're ready for that piece,
the natural next step is a Stripe Checkout or Payment Intents integration
that, on a successful webhook-confirmed payment, calls the same
`internal/ledger.PostTransaction` deposit path the manual form uses today
— the ledger itself doesn't need to change, just a new way of arriving at
it.

**Automatic trip-fund closeout.** Marking a trip fund's account `closed`
once a trip is over is a manual step today (there's no UI for it yet,
though the schema already supports the `closed` status) — not built
because it wasn't clear from the requirements whether "closed" should
mean anything more than a display label yet (e.g., should it block new
postings? refund a remaining balance somewhere?). Worth a quick
conversation before building it.

**Fundraiser proceeds import.** Recording a fundraiser allocation is a
one-Scout-at-a-time manual form right now. If your fundraisers produce a
per-Scout spreadsheet/CSV from the vendor, a bulk-import path would save
real time — flag it if that's how yours work and I'll add it.

## Getting someone into the Treasurer role

Same mechanism as any other role in this codebase:

- If they already have a login and some other role in the unit, a
  unit-wide admin (or another Treasurer/super_admin) can assign them the
  Treasurer role from `/admin/roster` — `treasurer` was added to the
  assignable role list for both Troop and Pack unit-wide scopes (Den
  Leaders scoped to a single den still only see parent/scout roles, as
  before).
- For a first foothold — nobody with admin access yet, or you're setting
  this up on a fresh unit — use the existing `-grant-role` CLI flag (see
  `DEPLOY.md` "Adding a unit later" for the full explanation — this flag
  already existed in Phase 1, `treasurer` is just a new valid value for
  `GRANT_ROLE`):
  ```bash
  docker compose run --rm \
    -e GRANT_EMAIL=you@example.com \
    -e GRANT_UNIT_SLUG=troop-47 \
    -e GRANT_ROLE=treasurer \
    app -grant-role
  ```

Once granted, that person will see a "Treasury" link in the nav and, the
first time they log in after being granted the role, the two-factor
setup banner until they visit `/settings/2fa`.

## Deploying this

Same deployment path as every prior update to this project — no separate
migration step:

1. Pull or unzip the updated code onto the VPS
   (`/usr/website/scout-site`).
2. `docker compose up -d --build`

Four migrations ship in this delivery
(`internal/db/migrations/0005_treasurer_role.sql`,
`0006_ledger.sql`, `0007_totp.sql`, `0008_system_settings.sql`) and apply
automatically on server start, same as every migration before them —
`cmd/server/main.go` runs `db.Migrate` unconditionally every time the
server boots, `-migrate` is only needed if you want to apply migrations
*without* starting the server. `0008_system_settings.sql` is new as of
this update — the QR code and nav changes for two-factor need no
migration at all, only the settings page does.

`0005_treasurer_role.sql` is deliberately its own migration file
containing nothing but `ALTER TYPE member_role ADD VALUE 'treasurer';` —
Postgres won't let a transaction reference an enum value it just added
in that same transaction, and this project's migration runner wraps each
numbered file in its own transaction, so the new role value needed a
migration of its own before `0006_ledger.sql` and later code could use
it.
