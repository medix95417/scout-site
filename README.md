# Scout Site — Phase 1 & 2

This is the walking skeleton described in `scout-website-architecture-phase1.md`
(single sign-on across two subdomains, roster, calendar/RSVP with the
SPL/Patrol Leader approval workflow, and the system-wide audit log) plus
Phase 2's fund accounting: a double-entry ledger, a Treasurer role with
mandatory two-factor login, trip funds, and fundraiser tracking. See
`PHASE2_TREASURY.md` for the full design writeup of the Phase 2 additions
— real payment processing (Stripe) is deliberately not built yet, per
build-order.

**You do NOT need Go installed to run this.** The only prerequisite is
Docker (specifically Docker Compose, which ships with Docker Desktop and
most Docker installs). The `app` container installs Go *inside itself*
during the image build and compiles the server there — Go never has to
exist on your machine at all. If you don't have Docker yet, install
Docker Desktop (Mac/Windows) or Docker Engine (Linux) first, then come
back here.

One caveat worth knowing up front: this code was written and
formatting-checked (`gofmt`), and every database query was manually
cross-checked against the schema and struct field order, but it's never
been compiled — the environment that generated it had no internet access
to fetch Go's dependencies. The first `docker compose build` below is
where that gets tested for real, on a machine (yours) that has normal
internet access. If it fails, send me the exact error — likely a small,
quick fix, not a sign anything is fundamentally wrong.

## Running with Docker Compose (the only path you need)

```bash
cp .env.example .env
```

Open `.env` and set `SESSION_SECRET` to a random value — easiest is to
run `openssl rand -base64 32` in a terminal and paste the result in. If
you don't have `openssl` handy, any long random string works for local
testing. Leave `COOKIE_DOMAIN` blank for now.

```bash
docker compose up -d db app
```

This builds the `app` image (installing Go inside the container and
compiling the server — this step needs internet access, which your
machine has) and starts Postgres alongside it. First run will take a
minute or two while it downloads the Go toolchain and dependencies inside
the container; that's normal.

Once it's up, create the database tables, the Troop/Pack unit rows, and
your own login, each as a one-off run of the same container image:

```bash
docker compose run --rm app -migrate
docker compose run --rm app -seed
docker compose run --rm -e ADMIN_EMAIL=you@example.com -e ADMIN_PASSWORD=changeme \
  -e ADMIN_FIRST_NAME=Your -e ADMIN_LAST_NAME=Name app -bootstrap-admin
```

Want to click around with realistic data instead of (or in addition to)
your own account — a full roster of test logins covering every role,
sample events, and Phase 2 ledger/fundraiser activity? Run
`docker compose run --rm app -seed-demo` and see **`DEMO_DATA.md`** for
the full list of logins it creates and what each one is for. Skip it if
you'd rather start from a clean, empty site.

The app resolves which unit (Troop/Pack) to show based on the `Host`
header — locally there's no real DNS for `troop.47-yonkers.org`, so add
these two lines to your computer's hosts file (on Mac/Linux that's
`/etc/hosts`, edited with `sudo`; on Windows it's
`C:\Windows\System32\drivers\etc\hosts`, edited as Administrator — this
is a harmless local-only override, it doesn't touch anything outside your
machine):

```
127.0.0.1 troop.47-yonkers.org
127.0.0.1 pack.47-yonkers.org
```

Then visit `http://troop.47-yonkers.org:8080` and
`http://pack.47-yonkers.org:8080` in your browser — that's the app,
running entirely in Docker. Since `COOKIE_DOMAIN` is left blank for local
testing, the session cookie is host-only — logging into one subdomain
won't automatically log you into the other locally. That's expected; in
production, `COOKIE_DOMAIN=.47-yonkers.org` makes the cookie shared across
both, which is the whole point (see `scout-website-architecture-phase1.md`
Section 2). The `caddy` service in `docker-compose.yml` is also defined
but only does something useful once real DNS points at a VPS — see
"Moving to a VPS" below; ignore it for now.

To stop everything: `docker compose down` (add `-v` if you also want to
wipe the database and start completely fresh next time).

## Optional: local Go build (only if you already have Go installed)

Skip this whole section unless you specifically want a faster edit/test
loop outside Docker, or already have Go 1.25+ on your machine. It is not
required to run or test the site.

```bash
go mod tidy      # resolves dependency versions, writes go.sum
go build ./...   # compiles everything; fix anything it flags
go vet ./...     # catches a few more classes of mistakes
go run ./cmd/server -migrate   # against a Postgres you're running some other way
```

## Moving to a VPS

Same `docker-compose.yml`, just pointed at production settings via
`.env` — no second compose file. See **`DEPLOY.md`** for the full
cold-server-to-running walkthrough: DNS, firewall, installing Docker,
getting the code onto the box, and ongoing operations (updates, backups,
logs). No architecture change is needed for this move — see
`scout-website-architecture-phase1.md` Section 8.

## What's actually in Phase 1

- Single sign-on: one family account, one login, works across both
  subdomains once `COOKIE_DOMAIN` is set.
- Roster: view members and their roles per unit (`/roster`).
- Self-service roster management (`/admin/roster`, for leaders): add a
  brand-new family (creates their login and generates a one-time temporary
  password to hand them), add a member to a family that's already in the
  system, create dens/patrols, and assign or remove roles — no more direct
  SQL inserts. Scoped to match the requirements doc's permission table: a
  Cubmaster/Scoutmaster/Assistant Scoutmaster/super_admin can manage the
  whole unit, while a Den Leader can only manage members in their own den
  (they can't promote anyone to a leadership role, and can't touch other
  dens' rosters). Every add/edit/role change is recorded in the activity
  log. Locked-out families can either use the self-service "forgot
  password" email flow (see "Email" below) or have a leader generate a new
  temporary password from their roster entry — useful when a family's
  email on file is wrong or unreachable, or a leader is just handing over
  a new password in person.

  Role assignments are per-unit even though login is shared (single sign-on
  logs you in everywhere, but it doesn't hand out roles) — so an account
  that's a leader on the Troop site won't automatically see "Manage
  Roster" on the Pack site too, and vice versa. Once an account has *some*
  unit-wide leader role in a unit, granting itself or others more roles in
  that unit is entirely self-service from `/admin/roster`. Getting that
  first foothold in a unit is what `-bootstrap-admin` is for (grants
  `super_admin` in every unit that exists at the time it runs); if a unit
  gets added later, or an account otherwise needs its first role in a unit
  it doesn't have one in yet, see DEPLOY.md "Adding a unit later" for the
  one-off `-grant-role` command that covers it.
- Calendar: create events, RSVP, and — if you're logged in as an SPL or
  Patrol Leader — submit events that need an SM/ASM's approval before they
  publish (`/calendar`). Public events also show up on the homepage
  automatically. Alongside the agenda-style upcoming events list,
  `/calendar` shows a graphical month grid (prev/back/today navigation via
  `?month=YYYY-MM`) with each day's events as colored chips — clicking one
  jumps down to its full entry (details, RSVP buttons) in the list below.
  Events can optionally have an end date/time, not just a start — a
  weekend campout, for example, shows as a continuous bar across Friday
  through Sunday on the month grid instead of only marking its first day,
  and the list/homepage both show the full range ("Fri Jul 3, 2026 6:00 PM
  – Sun Jul 5, 2026 12:00 PM") instead of just a start time. Leaving the
  end date blank keeps the old single-point-in-time behavior.
- Editable homepage (styled after pack6crestwood.org's layout — a
  full-bleed photo hero, bulleted "Our Program" list with a photo, meeting/
  leadership info, and a two-photo gallery strip): any leader (Cubmaster/
  Den Leader for the Pack, Scoutmaster/ASM for the Troop) can update the
  hero tagline, hero/program/gallery photos (paste a link to an image
  hosted elsewhere — there's no photo upload feature yet), the program
  bullet list (one activity per line), meeting info, leadership/contact
  blurb, and an optional social media link, all from `/admin/home` — no
  code changes or redeploy needed.

  **Out of the box, the photo slots default to stock Scouting photos from
  Wikimedia Commons** (freely licensed, hotlinked via Commons'
  `Special:FilePath`) so a brand-new site doesn't launch with empty gray
  boxes. These are meant as placeholders, not a permanent choice — swap
  them for your own troop/pack photos via `/admin/home` as soon as you
  have some. Real photos will always look better than stock ones, and
  relying on a third party to keep hosting an image indefinitely isn't
  something you want on a site you're running long-term.
- Files (`/files`): a general document library (packing lists, forms,
  handbooks) plus event photos, stored in S3-compatible object storage
  you bring yourself — a self-hosted MinIO, AWS S3, Cloudflare R2, etc.
  (`internal/storage`; set `S3_ENDPOINT`/`S3_ACCESS_KEY`/`S3_SECRET_KEY`/
  `S3_BUCKET`, see `.env.example`). Left unconfigured, the rest of the
  site works fine — the file library and event photos just show a clear
  "not configured yet" notice instead of an error. Any logged-in member
  can view and download; uploading, deleting, and linking a file to one or
  more calendar events requires the same leader role that edits the
  homepage. A file can be linked to several events at once (the same
  packing list attached to a recurring campout, say), and `/calendar`
  shows each
  event's linked photos/documents inline.
- Activity log: every create/approve/reject/content-edit is recorded
  (`/audit`, leaders only).
- Email (optional — see `.env.example` and DEPLOY.md "Configure the
  environment"): if `SMTP_HOST` is set, two things work automatically —
    - **Self-service password reset.** "Forgot your password?" on the
      login page (`/forgot-password`) emails a one-time link, valid for
      about an hour, that lets a family set a new password themselves
      (`/reset-password`). The confirmation message is identical whether
      or not the email is actually registered, so the flow can't be used
      to find out who has an account. The link is single-use and is
      invalidated the moment it's redeemed (or expires).
    - **Event reminder emails.** `docker compose run --rm app
      -send-event-reminders` emails everyone who's RSVP'd yes or maybe to
      a published event starting within `REMINDER_WINDOW_HOURS` hours
      (default 24). It's meant to run periodically via cron (see
      DEPLOY.md "Ongoing operations") — each member is only ever emailed
      once per event no matter how many times the command runs, so
      running it hourly (or even more often) is safe.

  If `SMTP_HOST` is left blank, the site works exactly as before: "forgot
  password" quietly no-ops (with a note in the server log pointing at the
  `/admin/roster` fallback) and `-send-event-reminders` logs a warning and
  sends nothing. Nothing else depends on email being configured.

**Not in Phase 1** (see `scout-website-requirements.md` Section 6): any
payment processing, fund ledgers, individual Scout accounts, or trip-fund
transfers — Phase 2 (below) adds the ledger, accounts, and trip funds, but
still not real payment processing. Also not yet built, deliberately left
for a later round: a newsletter email feature (see
`scout-website-requirements.md`'s Phase 3 candidates), the Scoutbook CSV
import job, digital permission slips/consent forms, and rank/badge
advancement tracking (the `advancement_records` table exists but nothing
populates or displays it yet — it's meant to be filled by the not-yet-built
Scoutbook import).

**Also added post-Phase-2 — news, photo galleries, and a filterable/
exportable activity log:** a public news feed (`/news`) and photo
galleries (`/gallery`), both built on the same `content_pages` table
homepage sections already used (`page_type = 'post'`/`'gallery'` —
see `internal/content/posts.go`), editable from `/admin/news` and
`/admin/gallery` by the same leader roles that can edit the homepage.
Each post/gallery starts as a draft and is published with one click, and
each can be marked public or members-only, same visibility model the
calendar already uses. The Activity Log (`/audit`) can now be filtered by
date range, "function" (which part of the site — roster, calendar,
ledger, content, site settings), and person, and exported as CSV with
the same filters applied (`/audit/export.csv`) — see
`internal/audit/audit.go`'s `Filter`/`ForUnitFiltered`. Building the
filter also surfaced (and fixed) a real gap: roster changes — adding a
family, adding a member, granting or removing a role — were being logged
to the audit table since Phase 1 but were never actually included in
what `/audit` queried for, so they silently never appeared on the
Activity Log page despite `SECURITY_AUDIT.md` assuming they did. They do
now.

**Phase 2 — fund accounting (see `PHASE2_TREASURY.md` for the full
writeup):** a double-entry ledger (`internal/ledger`) with unit general
funds, per-Scout individual accounts, and per-event trip funds; a
Treasurer role (`/treasury`) that can record deposits/expenses/transfers
and approve trip-fund push transfers families request from their Scout's
account; fundraiser tracking with a configurable proceeds-allocation rule
that starts out flagged "needs council confirmation" until a Treasurer
sets the real, council-approved rule; and mandatory TOTP two-factor login
(`internal/twofactor`, `/settings/2fa`, QR code enrollment via a
CDN-loaded canvas library plus manual setup-key entry) for any login that
holds the Treasurer or super_admin role, since those can move real money
and some of the family accounts on this site belong to minors — two-factor
is also available as an **opt-in** for every other login via a "Security"
nav link, not required, just discoverable. Real payment processing
(Stripe) is intentionally not wired up yet — see `PHASE2_TREASURY.md` for
why and what that'll take.

**Also added post-Phase-2:** a `/admin/settings` page, visible only to
`super_admin` logins, for site-wide configuration toggles — starting with
whether the two-factor reminder banner should nudge every login or just
Treasurer/super_admin (still opt-in either way; see `PHASE2_TREASURY.md`,
"Site settings"). Backed by a small, deliberately generic
`internal/settings` package, so adding a future toggle doesn't need a new
page.

**Also added post-Phase-2 — individual Scout logins, account visibility,
and easier-to-find password resets:** until now every login was a whole
family's shared account — there was no way for one specific Scout,
registered in either unit (or both), to log in as themselves rather than
through their family's shared login. `/admin/roster` → a member's detail
page now has an "Individual Login" section (any member type, not just
adults) for creating one, alongside the existing family password reset.
An individual login sees *just its own stuff*: its own roles (resolved via
`units.RolesForMemberInUnit`, not the family-wide union), its own acting
identity for RSVPs/content edits (no more of `family
.ActingMemberForFamilyInUnit`'s "guess which family member" heuristic —
the member is already known), and its own ledger account ownership (an
individual login can see/manage only its own Scout account; a family-wide
login can still see/manage every child's, same as before) — see
`internal/web`'s `rolesFor`/`actingMember`/`isAccountOwner`/`rosterScope`
helpers, which every permission check in the codebase now goes through
instead of assuming "the current login" always means "the whole family."

A new `/accounts` page ("My Accounts" in the nav, visible to any logged-in
login) shows individual Scout ledger account balances without needing the
Treasurer role — a family-wide login sees every one of its children's
accounts in the current unit, an individual Scout login sees just its own.
Previously the only place an account balance/link rendered was the
Treasurer-only `/treasury` dashboard, so a family with no treasury role had
no discoverable way to check their child's balance even though the
underlying ownership check would have allowed it.

Password reset was never actually missing — both a self-service
"forgot password" email flow and a leader-triggered reset already existed
— it just wasn't easy to find, with the reset button buried on one
member's individual roster detail page. The roster list (`/admin/roster`)
now has a direct "Reset Password" link next to "Edit" on every row, and
the member detail page's password section now also covers resetting an
individual login's password (`roster.ResetMemberLoginPassword`), not just
the family-wide one.

Finally, SMTP host/port/username/from are now editable from
`/admin/settings` (`internal/settings`'s `TextSettings`, resolved at send
time by `internal/mailer.Mailer.effective` with the `SMTP_*` environment
variables as fallback) instead of only via environment variables and a
restart. `SMTP_PASSWORD` deliberately stays environment-variable-only —
this codebase has no other precedent for storing a plaintext secret in
Postgres, and there's no reason to start with the one real credential in
this configuration; see `SECURITY_AUDIT.md`.

## Versioning

Changes are tracked in `CHANGELOG.md` and tagged in git as `vX.Y.Z`
(current version: **v1.6.2**). Each delivery from here forward gets its
own commit and tag — see `CHANGELOG.md` for the full history and how
version numbers are chosen.

## Repo layout

See `scout-website-architecture-phase1.md` Section 6 for the reasoning.
Quick map:

- `cmd/server` — entrypoint, flags, HTTP server wiring.
- `internal/config` — environment variable loading.
- `internal/db` — Postgres connection + migration runner (embeds
  `internal/db/migrations/*.sql`).
- `internal/auth` — sessions, password hashing, login middleware.
- `internal/units` — resolves Troop vs. Pack by hostname, role/permission
  checks.
- `internal/family` — families, members, roster queries.
- `internal/roster` — self-service roster management (add families/members,
  assign roles, manage dens/patrols), including the Den-Leader-vs-unit-wide
  scoping rules.
- `internal/calendar` — events, RSVPs.
- `internal/content` — editable homepage sections (hero, program, meeting,
  leadership, photos), plus (`posts.go`) news posts and photo galleries —
  same `content_pages` table, `page_type = 'post'`/`'gallery'`.
- `internal/approval` — the generic approval-request workflow.
- `internal/audit` — the generic audit log, plus (as of the news/galleries
  update) filterable/exportable queries (`Filter`, `ForUnitFiltered`,
  `ActorsForUnit`, `EntityTypesForUnit`) the Activity Log and its CSV
  export both build on.
- `internal/bootstrap` — one-time creation of the first admin login.
- `internal/web` — HTTP handlers + `html/template` templates (htmx +
  Tailwind via CDN, no separate frontend build). News/galleries live in
  `content_posts.go`, the Activity Log and its CSV export in `audit.go`.
  Phase 2's treasury handlers (`treasury.go`), two-factor-login handlers
  (`twofactor.go`),
  and the site-wide settings admin page (`settings_admin.go`) live here
  alongside the Phase 1 handlers, same package.

Phase 2 additions:

- `internal/ledger` — the double-entry ledger: accounts, transactions,
  postings, fundraisers. No HTTP or template code — just data model and
  business rules, same separation Phase 1 used for `internal/calendar`
  etc.
- `internal/twofactor` — pure-stdlib TOTP (RFC 6238) generation and
  verification. No database code — just the cryptographic primitive,
  callable from `internal/auth`.
- `internal/approval` (existing Phase 1 package, extended) — the generic
  approval-request workflow gained an `entity_type = "ledger_transaction"`
  case alongside the existing `"event"` case, so trip-fund transfers reuse
  the same approve/reject mechanism SPL/Patrol Leader event approvals
  already used, unchanged in spirit.
- `internal/settings` — a small generic key/value store for site-wide
  on/off toggles (backed by the `system_settings` table), read by
  `settings_admin.go` and by `internal/web`'s login/nav logic. No HTTP
  code — same data-model-only separation as `internal/ledger` and
  `internal/twofactor`.
