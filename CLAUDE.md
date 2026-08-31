# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go web app serving two Scouting units (a Troop and a Pack) from one
binary and one Postgres database, tenant-resolved per request by the
`Host` header (`internal/units.ByHostname`/`Middleware`). Server-rendered
`html/template` + htmx + Tailwind (via CDN) — no separate frontend build
or JS toolchain. See `README.md` for the full feature rundown,
`scout-website-architecture-phase1.md` for the original architecture
writeup, `PHASE2_TREASURY.md` for the fund-accounting design, and
`SECURITY_AUDIT.md` for security posture/decisions.

## Commands

You do not need Go installed to run the app (Docker Compose builds it in
a container), but Go 1.25+ is needed for local build/test/vet.

```bash
go build ./...              # compile everything
go vet ./...                # static checks
go test ./...                # run all tests (pure unit tests, no live DB required)
go test ./internal/calendar/...              # test a single package
go test ./internal/calendar/... -run TestName -v   # run a single test
gofmt -l .                   # list files needing formatting (this codebase is gofmt-clean)
go mod tidy                  # resolve/update dependency versions
```

Running the full app requires Postgres and is normally done via Docker
Compose (see README.md "Running with Docker Compose" for the full
sequence: `docker compose up -d db app`, then `-migrate`/`-seed`/
`-bootstrap-admin` as one-off `docker compose run --rm app <flag>`
invocations). Against a Postgres you're already running some other way:

```bash
go run ./cmd/server -migrate   # apply pending migrations, then exit
go run ./cmd/server            # run the server (reads config from env, see .env.example)
```

`cmd/server/main.go` documents every CLI flag (`-migrate`, `-seed`,
`-seed-demo`, `-bootstrap-admin`, `-grant-role`, `-send-event-reminders`,
`-backfill-thumbnails`) — read its top-of-file doc comment before adding
a new one.

## Architecture

**Package layering.** Business-logic packages (`internal/family`,
`internal/roster`, `internal/calendar`, `internal/content`,
`internal/approval`, `internal/audit`, `internal/ledger`,
`internal/twofactor`, `internal/settings`, `internal/permission`,
`internal/advancement`, `internal/leaders`, `internal/resources`,
`internal/help`, `internal/prospect`) contain
data model and business rules only — no HTTP or template code. All HTTP
handlers and `html/template` rendering live together in `internal/web`
(one package, many files split by feature area, e.g. `treasury.go`,
`twofactor.go`, `admin_roster.go`, `content_posts.go`, `audit.go`,
`settings_admin.go`). Templates are in `internal/web/templates/`.

**Request middleware order matters** (`cmd/server/main.go`): resolve the
tenant unit from `Host` first (`units.Middleware`, everything downstream
assumes it's in request context), then attach the logged-in user
(`auth.WithUser`), then CSRF, then security headers.

**Multi-tenancy + roles.** One family/login can exist across both units;
roles (Cubmaster, Scoutmaster, Den Leader, Treasurer, super_admin, etc.)
are granted per-unit, not globally — an account with a leader role on one
subdomain doesn't automatically get it on the other. As of individual
Scout logins, "the current login" no longer always means "the whole
family" — every permission check in `internal/web` goes through the
`rolesFor`/`actingMember`/`isAccountOwner`/`rosterScope` helpers rather
than assuming a family-wide login, so any new permission-sensitive
handler should use those too, not roll its own family/member resolution.

**Migrations.** Plain SQL files embedded from `internal/db/migrations/`
and applied in order by `internal/db.Migrate` — it runs automatically on
every server startup (and via `-migrate`) and needs no separate tool.
Add a new migration as the next-numbered `NNNN_description.sql` file.

**Optional integrations degrade gracefully, never fatally.** Email
(`internal/mailer`, SMTP or Fastmail JMAP) and file storage
(`internal/storage`, S3-compatible) are both optional: unconfigured, the
rest of the site still works and just reports a clear "not configured"
message at the point of use instead of failing to start or erroring
elsewhere. Follow this pattern for any new optional external dependency.

**Generic mechanisms reused across features, don't rebuild them:**
- `internal/approval` — generic approval-request workflow, used for both
  event-approval (SPL/Patrol Leader submitted events) and Phase 2
  trip-fund transfer approvals (`entity_type` distinguishes the cases).
- `internal/audit` — generic audit log with filter/export
  (`Filter`/`ForUnitFiltered`/`ActorsForUnit`/`EntityTypesForUnit`) that
  `/audit` and its CSV export build on. Any new mutating action should
  write an audit entry the same way existing handlers do, or it silently
  won't show up in the Activity Log.
- `internal/content` (`posts.go`) — the homepage's editable
  `content_pages` table also backs news posts and photo galleries via
  `page_type = 'post'`/`'gallery'`, rather than separate tables.
- `internal/settings` — small generic key/value store for site-wide
  on/off toggles (`system_settings` table); add a new toggle here rather
  than inventing a one-off settings mechanism.
- `internal/prospect` — enquiries from families interested in joining,
  captured by the public `/join` form and tracked by leaders on
  `/admin/prospects`. Deliberately not part of `internal/roster`: a
  prospect has no family, no login and no roles, and joining is a
  separate act a leader performs on the roster page.
- `internal/help` — the in-app help catalog. Each topic declares the
  capability it needs and the feature toggles it depends on, and
  `help.For(Viewer)` is the only place that decides visibility. When you
  add a feature, add its help topic here with its gates rather than
  writing prose into the template — `help_test.go` fails a topic that
  mentions a gated feature without declaring the gate, and that guard is
  what keeps help from describing a switched-off feature.

**Ledger (`internal/ledger`, Phase 2).** Double-entry accounting:
transactions and postings, unit general funds, per-Scout individual
accounts, per-event trip funds, and fundraiser tracking with a
proceeds-allocation rule. No HTTP/template code, same separation as other
business-logic packages.

**Two-factor auth (`internal/twofactor`).** Pure-stdlib TOTP (RFC 6238),
no database code — just the cryptographic primitive, called from
`internal/auth`. Mandatory for Treasurer/super_admin logins, opt-in for
everyone else.
