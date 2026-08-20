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

**Fix — no way to give an existing person a role in a different unit.** A
leader could not add a Scout or parent already registered under the other
unit (e.g. a Pack Scout crossing over to a Troop position, or a parent
taking on a role in both) — the roster admin page's roster list only ever
shows members who already hold a role in the current unit, and both "add"
flows there only ever create brand-new member records, so an existing
person from the other unit was simply invisible. Added a new "Add an
Existing Person" section on `/admin/roster` (unit-wide leaders only, same
restriction as creating a new den/patrol) backed by a new
`roster.MembersNotInUnit` query — every member system-wide who doesn't yet
have a role here — letting a leader give that person their first role in
this unit without duplicating their member record. Once assigned, the
"also holds roles elsewhere" note on the member's profile page (added
earlier) now actually reaches someone in this situation, since previously
that page's own access check silently required the member to already have
a role in the current unit too.

**Print / Download PDF on My Family and the Family Directory.** Both pages
now have a "Print / Download PDF" link that generates a real, server-side
PDF (via the new `github.com/go-pdf/fpdf` dependency) — a simple,
one-page contact list, grouped by family, using the exact same
release-filtered contact info the on-screen Family Directory already shows
(and, for My Family, the caller's own unfiltered info, same as that page's
own display). The Family Directory's on-screen layout was already grouped
by family (one card per family) — this only adds the export, not a new
grouping. A real PDF was chosen deliberately over a print stylesheet, since
the My Family page itself is full of editable form fields that wouldn't
print cleanly.

**Resources page — public and members-only documents/links.** A new
`/resources` page lists leader-curated documents and links (handbooks,
forms, useful outside sites) — some marked public and visible to any
visitor, others members-only. One page serves both views: a logged-out
visitor sees only what's marked public, a logged-in one sees everything,
and an admin/cubmaster/pack master (`units.CanEditUnitContent`, the same
gate the homepage and news/gallery admin surfaces already use) additionally
gets an inline "Add a resource" form and per-resource delete/toggle-public
controls — the same "one page, admin controls appear inline" pattern
`/files` already uses, rather than a separate admin page. A resource is
either a document already in the unit's file library or an external link,
never both. Built on a new `resources` table (migration
`0019_resources.sql`) with its own `is_public` flag, independent of the
underlying file's own — a members-only-by-default library file can be
curated here as a public resource without changing its own public flag or
its raw `/files/{id}/download` URL.

**Per-page hero banners, admin-editable.** Calendar, News, Gallery, Roster,
Family Directory, Files, and the Patrols/Dens list can each now carry an
optional full-bleed hero banner image, shown just below the site header on
that page (and its sub-pages, e.g. an individual news article). Set from a
new "Page Hero Banners" section on `/admin/home`, right below the existing
homepage sections — same paste-a-URL-or-choose-from-your-public-library
picker the homepage's own photo fields already use, so no new admin UI
concept was needed. Leave one blank for no banner; News and Gallery banners
show to logged-out visitors too, since those pages are public. The
homepage keeps its own separate, richer hero (background photo + tagline +
call-to-action) — this is additive, not a replacement. Built on
`internal/content`'s existing `content_pages`-backed section mechanism,
with a new `pagehero-*` slug prefix alongside the existing `home-*` one.
Also fixed a pre-existing bug where the Pack unit's homepage hero-photo
field was missing the image preview/picker UI that the Troop's identical
field already had (a `SectionDef.Kind` of `"url"` instead of `"image"`).

**Patrols/Dens dropdown in the hamburger nav.** The nav's "Patrols"/"Dens"
entry now expands (a nested no-JS `<details>` disclosure, same pattern as
the outer hamburger) to list each patrol/den by name with a direct link to
its own page, plus a "View all" link to the full list — no more landing on
a list page just to pick one. Falls back to the plain link it replaces
when a unit has no patrols/dens set up yet.

**Toggle: family access to Scout account balances.** A new per-unit
setting on `/admin/settings` ("Family access to Scout account balances")
lets an admin shut off the family-facing self-service view of Scout ledger
accounts while leaving the Treasury area fully intact. When off, only a
Treasurer/super_admin can open an account page — `/accounts` and
`/treasury/accounts/{id}` return a clear message to anyone else, the
self-service push-transfer is blocked, and the "My Accounts" nav link is
hidden. Defaults to on, so existing units are unchanged. Same per-unit
mechanism as the advancement toggle (`unit_settings`, no migration).

## [1.7.1] — 2026-08-20

**Fix — deploy-blocking migration error (`events.sub_group_id` already
exists).** `docker compose run --rm app -migrate` failed on
`0018_calendar_sub_groups.sql` with `column "sub_group_id" of relation
"events" already exists`. Root cause: that column has existed since
`0001_init.sql` (defined inline with `ON DELETE CASCADE`), but 0018
mistakenly tried to `ADD` it again — which fails on every real database,
since they're all created from 0001. Local testing had masked it by
dropping the column before re-running. 0018 no longer adds the column; it
now just corrects the foreign key from `ON DELETE CASCADE` to the intended
`ON DELETE SET NULL` (deleting a patrol/den widens its events back to
whole-unit scope instead of deleting them), written idempotently. Verified
by reproducing the exact production failure on a fresh database built from
the committed migrations, then confirming the corrected migration applies
cleanly both there and through a full 0001→0018 chain.

**Security — stored-XSS fix in the newsletter sanitizer.** A full security
audit found that the newsletter HTML sanitizer (`internal/newsletter.Sanitize`,
added in 1.7.0) blocked `javascript:` URLs by a prefix check but could be
bypassed two ways: embedding an ASCII control character inside the scheme
(`java&#9;script:` — browsers strip it before evaluating, so the link still
executes) and using other dangerous schemes like `vbscript:`. Since a
newsletter body is stored HTML that renders into other leaders' browsers
(re-opening a draft) and is emailed to families, this was an exploitable
stored-XSS vector. The URL check is now a positive scheme allowlist
(`http`/`https`/`mailto`/`tel` plus relative/anchor URLs) applied after
stripping the control characters and whitespace browsers ignore — anything
it doesn't positively recognize is dropped. Added regression tests covering
the obfuscation bypasses and confirming legitimate links still pass.

**Security — patched a SQL-injection CVE in the `pgx` database driver.**
The newly-added `govulncheck` CI job immediately surfaced GO-2026-5004: a
SQL-injection vulnerability in `github.com/jackc/pgx/v5` v5.6.0 (placeholder
confusion with dollar-quoted string literals), reachable from this app's
own queries. The application's own SQL is fully parameterized — this was a
flaw inside the driver's placeholder handling, not in app code — but it was
still exploitable through us, so the driver is upgraded to v5.9.2 (the
patched release). This is exactly the class of dependency-level issue a
manual code review can't catch, which is why the scan was added.

**Security — `govulncheck` in CI.** The GitHub Actions workflow now runs
`govulncheck` as its own job on every push and pull request, scanning the
code and its dependencies against the Go vulnerability database. It reports
only vulnerabilities actually reachable from this module, so a failure here
is a real, actionable finding.

**Security — defense-in-depth response headers.** Every response now carries
`X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
`Referrer-Policy: strict-origin-when-cross-origin`, and a minimal
`Content-Security-Policy` (`frame-ancestors 'none'; object-src 'none';
base-uri 'self'`). The CSP deliberately doesn't restrict scripts — the app
loads htmx/Tailwind/Quill from CDNs and uses inline scripts — so it only
locks down framing/plugins/`<base>`, which is a pure win. HSTS stays with
Caddy, which adds it on TLS termination.

## [1.7.0] — 2026-08-20

**Calendar events scoped to a specific patrol/den.** The "Add an event"
form now offers an optional "Scope to a specific patrol/den" picker — a
unit-wide leader can schedule for any patrol/den, a scoped submitter
(e.g. a Patrol Leader with only submit-for-approval rights) only for
their own. A scoped event is visible only to that patrol/den's own
members (via their existing role assignment, not just its leaders) plus
any leader broad enough to hold full content-edit access, for
cross-den scheduling oversight — everyone else sees it exactly as if it
didn't exist. Scoping always overrides "visible to the public": a
patrol/den event is inherently members-only, since the unauthenticated
calendar path doesn't apply any den-filtering of its own.

- **Migration:** `0018_calendar_sub_groups.sql` — adds `events.sub_group_id`
  (nullable; existing and future unscoped events are unaffected).

**Members-only patrol/den pages, with photos.** Each patrol (Troop) or
den (Pack) now gets its own page at `/groups/{id}` — a short description
plus a photo grid, similar in spirit to the main homepage's "Our Program"
section and gallery strip, but never shown to a logged-out visitor: every
route here requires login, and none of it is reachable from the public
homepage. A new `/groups` page lists every patrol/den in the unit as the
members-only landing point, and pills on `/admin/roster`'s "Dens &
Patrols" section now link to a sub-group's edit page.

Editing a sub-group's own blurb/photos requires being able to manage
that specific sub-group — a unit-wide leader can edit any of them, a Den
Leader only their own den, matching the existing "Den Leader (their
den)" scoping already used everywhere else in the roster. Photos are
picked from the file library the same way event photos already are (no
new upload path needed), and don't need to be marked "Public" the way a
homepage photo does, since the page itself already requires login.

- **Migration:** `0017_sub_group_pages.sql` — adds `sub_groups.description`
  and a `sub_group_files` join table (mirrors the existing `event_files`).

**Home page photos, chosen from the site's own file storage.** The hero,
"Our Program," and gallery photo fields on `/admin/home` now offer a
"Choose from your file library" dropdown alongside the existing paste-a-
URL option — no more needing to host a photo somewhere else first.

The file library is members-only by design (every download normally
requires login), but the homepage is public, so a leader has to
explicitly mark a photo "Public" from `/files` (image files only) before
it shows up in the homepage picker — picking a photo never makes it
public by itself. `/files/{id}/download` skips the login requirement only
for a file marked this way; everything else in the library is unaffected.

- **Migration:** `0016_public_files.sql` — adds `files.is_public`
  (default false).

**Newsletter: WYSIWYG HTML editor, real HTML email, and starter
templates.** The newsletter body is now authored with a full rich-text
editor (Quill, loaded from a CDN — same pattern as htmx/Tailwind already
used elsewhere) instead of a plain-text box: bold/italic/underline,
headers, bullet/numbered lists, links, images, colors, and alignment.
Sending now delivers a real HTML email (`mailer.SendHTML`) rather than
plain text — every other email in the app (password reset, event
reminders) is unchanged and still plain-text via `mailer.Send`. A new
"Start from a template" picker on `/admin/newsletters/new` offers a
Troop- or Pack-appropriate "Monthly Update" and "Event Announcement"
starting point (`internal/newsletter.StarterTemplates`), editable before
saving.

Since this is the first place in the app that stores and renders raw HTML
from a form rather than escaping it as plain text, every write path
(`CreateDraft`, `UpdateDraft`) runs the body through a new allowlist-based
sanitizer (`internal/newsletter.Sanitize`, built on `golang.org/x/net/html`
— already an indirect dependency, so no new one added) before it's ever
stored: script tags, event-handler attributes (`onerror=`, etc.), and
`javascript:`/`data:` URLs are stripped; Quill's actual formatting output
(styles, classes, lists, links, images) survives untouched.

- **No migration needed** — `newsletters.body` already stored arbitrary
  text; it just holds HTML now instead of plain text with line breaks.

**Cross-unit role visibility on the member edit page.** A member/family
can hold roles in both the Troop and Pack at once (already true at the
data layer), but `/admin/roster/members/{id}` only ever showed roles in
whichever unit you were viewing it from — a Troop leader had no way to
know the same person is, say, also a Den Leader in the Pack. The page now
shows a small read-only "Also holds roles elsewhere" note listing any
other unit(s) and role(s) the member holds. Read-only by design — it
doesn't grant the viewing leader any ability to manage those other roles,
just visibility that they exist; managing them still requires being on
that other unit's own admin roster page.

**Social media links on the homepage.** A leader can now set a Facebook
page, Instagram profile, and/or TikTok profile URL for their unit from
the existing `/admin/home` homepage editor — each is its own optional
field, independent of the other, and only the ones actually filled in
show up as follow links on the public homepage. Built on the existing
generic homepage-section mechanism (`internal/content.HomepageSections`)
rather than a new table or admin page — three new section slugs
(`home-facebook`, `home-instagram`, `home-tiktok`) picked up the existing
`/admin/home` editing UI for free. No migration needed.

**Custom roles with admin-picked capabilities.** A super_admin can now
create a role on the fly (`/admin/custom-roles`, per unit) and choose
which capabilities it grants — edit content, approve submissions, submit
for approval, manage the ledger, or site settings — instead of every role
being one of the 9 fixed, code-defined ones. Under the hood, every
permission check in the app (`CanEditUnitContent`, `CanManageLedger`,
`CanApprove`, `CanSubmitForApproval`, `IsSuperAdmin`) now resolves a
member/family's roles into a capability set (`internal/units.Capabilities`)
rather than checking hardcoded role-name lists — the 9 existing roles'
exact behavior is preserved byte-for-byte (verified against every
`DEMO_DATA.md` persona, including the 2FA-enrolled ones), just re-expressed
as which capabilities they grant. A custom role with a given capability is
indistinguishable from a built-in role with that same capability to every
check in the codebase.

Multiple role assignments per member — including holding roles in both
the Troop and Pack simultaneously — already worked at the data layer and
in the existing "Add Role"/roster UI; nothing needed to change there.

- **Migration:** `0013_custom_roles.sql` — widens `role_assignments.role`
  from the fixed `member_role` enum to plain text (existing rows/values
  unchanged) and adds `custom_roles` (per-unit role definitions with a
  `capabilities` array, checked against the fixed capability set).

**Advancement on/off toggle.** A super_admin can now turn `/advancement`
and `/admin/advancement` on or off per unit, from a new "This Unit's
Settings" section on `/admin/settings` — Troop and Pack can answer this
independently. Defaults to on (no behavior change for existing units)
so it can be turned off where BSA national's own Scoutbook changes have
made a unit's own tracking redundant, with a one-click way to turn it
back on. Turning it off hides the nav links and blocks direct URL access
to every advancement route with a clear message — existing records
aren't touched, just hidden until re-enabled.

- **Migration:** `0014_unit_settings.sql` — adds `unit_settings`, a
  per-unit sibling to the existing site-wide `system_settings` table.

**Roster contact info, with family-controlled release to the rest of the
unit.** Each person can now have an email, home phone, and cell phone on
file, plus one shared address per household — editable either by a
leader from `/admin/roster/members/{id}` or by the family themselves from
a new self-service `/my-family` page (a family-wide login manages
everyone in the family; an individual member login manages only their
own contact fields, matching the existing "just their own stuff" rule
used elsewhere for individual logins). Nothing is visible to anyone else
by default — email, phone, and address each have their own release
toggle, opt-in and off until the family/member turns it on themselves.
A new `/directory` page shows every family on the unit's roster with only
the fields they've chosen to release; the underlying query never even
selects an unreleased field, rather than fetching everything and hiding
it in the template.

- **Migration:** `0015_roster_contact_info.sql` — adds `address`/
  `release_address` to `families`, and `email`/`home_phone`/`cell_phone`/
  `release_email`/`release_phone` to `members`, all nullable/default-false
  so existing rows are unaffected.

## [1.6.3] — 2026-08-20

**Fix — deploy build OOM-killed on a small VPS.** `docker compose up -d
--build` could fail with `signal: killed` and no other error message —
the Docker build's `go build` step getting silently OOM-killed partway
through. Root cause: `minio-go` (the S3 client added in 1.6.0) pulls in
`goccy/go-json`, which contains a single ~5,000-line generated encoder
file large enough that compiling it concurrently with everything else
in the build can exceed available memory on a 1GB VPS. The Dockerfile's
build step now sets `GOMAXPROCS=1 GOFLAGS=-p=1`, trading build speed for
peak memory by serializing compilation instead of parallelizing it.
DEPLOY.md also documents adding 2GB of swap on a 1GB-RAM VPS, for the
case where even a serialized build still needs more memory than the box
has to spare.

- **No deploy or `.env` changes needed** beyond the usual `git pull` +
  `docker compose up -d --build` — if the build still gets OOM-killed
  afterward, add swap per DEPLOY.md's updated step 2.

## [1.6.2] — 2026-08-19

**Fix — restart loop when `S3_ENDPOINT` is set to a full URL.** 1.6.1
made an *unconfigured* S3 endpoint safe (the app boots, `/files` just
shows a notice), but a *misconfigured* one still crashed the app on
every startup: `main.go` called `log.Fatalf` on any error from
`storage.New`, and pasting the endpoint value a provider's own
dashboard gives you — e.g. DigitalOcean Spaces' "Origin Endpoint,"
`https://nyc3.digitaloceanspaces.com` — produced exactly that error
(minio-go's `Endpoint` field wants a bare host, not a URL with a
scheme: `"Endpoint url cannot have fully qualified paths"`), so the
container restart-looped forever under `restart: unless-stopped`.

Two fixes: `internal/storage.New` now accepts either form —
`S3_ENDPOINT` can be a bare host (`nyc3.digitaloceanspaces.com`) or a
full URL with scheme (`https://nyc3.digitaloceanspaces.com`), stripping
the scheme/path and inferring `S3_USE_SSL` from it when present. And
regardless of that, storage errors are no longer fatal at all —
`cmd/server/main.go` now treats a storage configuration error the same
as an unconfigured one: logged, not fatal, with the file library/event
photos degrading instead of the whole site going down.

- **No deploy or `.env` changes needed** to pick up this fix — just
  redeploy. If you'd previously worked around the crash by removing
  `S3_ENDPOINT`, you can now set it back to the exact value your
  provider gave you.

## [1.6.1] — 2026-08-19

Two fixes to the file storage work in 1.6.0, found from a real deploy attempt.

**Fix — deploy build failure.** `go.mod`'s `go` directive moved to
`1.25.0` in 1.6.0 (a transitive dependency of the new S3 client
requires it), and CI's Go install was updated to match — but the
`Dockerfile`'s build stage was still `FROM golang:1.24-bookworm`, so
`docker compose up -d --build` failed on every fresh deploy with `go.mod
requires go >= 1.25.0`. CI didn't catch this since it only runs `go
build ./...` against its own Go 1.25 install, not the Docker image.
Dockerfile now builds `FROM golang:1.25-bookworm`, and switched from
`go mod tidy` (re-resolves dependencies over the network on every
build) to `go mod download` against the committed `go.sum` — reproducible,
and a cacheable layer.

**Fix — file storage no longer blocks the whole site from starting.**
1.6.0 bundled a MinIO service in `docker-compose.yml` as the default
object store, and the app required it to be reachable at startup
(`log.Fatalf` on any connection error) — so if that dependency wasn't
healthy yet (or, after removing the bundled service, wasn't configured
at all), the `app` container never started and Caddy had nothing to
proxy to: the site showed a blank page instead of a clear error. The
bundled MinIO service is removed — `S3_ENDPOINT`/`S3_ACCESS_KEY`/
`S3_SECRET_KEY` now point at a bucket you already run or manage (a
self-hosted MinIO, AWS S3, Cloudflare R2, etc.), same as any other
external S3-compatible store. More importantly, file storage now
degrades gracefully exactly like `SMTP_HOST` already does: an empty
`S3_ENDPOINT` (or one that's temporarily unreachable) no longer crashes
the app at startup — the rest of the site works normally, and `/files`
shows a clear "file storage isn't configured yet" notice instead of an
error. `internal/storage.New` also no longer auto-creates the bucket at
startup (that's no longer appropriate for a bucket this app doesn't
own, and often isn't even permitted by a real cloud provider's IAM
policy) — create it yourself before expecting uploads to work.

- **Deploy note:** `docker-compose.yml` no longer has a `minio` service
  or `minio_data` volume. If you were relying on the bundled MinIO,
  stand up your own S3-compatible store and point `S3_ENDPOINT` at it
  before `docker compose up -d --build` — see DEPLOY.md's "Configure the
  environment."

## [1.6.0] — 2026-08-19

**Hamburger nav.** The header's nav had grown to too many always-visible
links. It's now a single dropdown (`<details>`/`<summary>`, no JS
framework needed), with admin-only links grouped under their own
"Admin" section — same permission gates as before, just organized
instead of sprawled across the top of every page.

**Version number in the footer.** The footer used to read "part of the
47-Yonkers Scouting sites (Phase 1 & 2)," which stopped being accurate
after Phase 3. It now shows the actual build version (`internal/version`,
kept in sync with this changelog by hand — see that package's doc
comment for why it's a plain constant rather than something injected at
build time).

**File library and event photos.** A new `/files` page (any logged-in
member can view; uploading/deleting/linking requires the same
`CanEditUnitContent` role that gates `/admin/home`) stores general
documents (packing lists, forms, handbooks) and event photos, and lets a
file be linked to one or more calendar events — the same permission slip
or packing list can be attached to several events, e.g. a recurring
campout, instead of re-uploading it each time. `/calendar` shows each
event's linked photos/documents inline, with photos rendered as
thumbnails.

Actual file bytes live in S3-compatible object storage
(`internal/storage`, via `minio-go`), not the database — Postgres only
stores metadata and a storage key (`0012_files.sql`'s `files` and
`event_files` tables). `docker-compose.yml` now bundles a MinIO service
as the default backing store, so file uploads work out of the box with
no extra setup; pointing at a real cloud bucket (AWS S3, Cloudflare R2,
etc.) instead is just a matter of overriding the `S3_*` environment
variables (see `.env.example`).

Getting real file uploads working also required fixing a CSRF
middleware gap: `r.ParseForm()` (what the middleware used to validate
`csrf_token`) never parses `multipart/form-data` bodies, so every file
upload would have failed CSRF validation regardless of how correct its
token was. The middleware now calls `r.ParseMultipartForm` instead,
which handles both body types, plus a `http.MaxBytesReader` cap (25 MB
per request) applied before parsing.

- **Migration:** `0012_files.sql` — applies automatically on next server
  start.
- **Deploy note:** `docker-compose.yml`'s `app` service now depends on a
  new `minio` service and expects `S3_ENDPOINT`/`S3_ACCESS_KEY`/
  `S3_SECRET_KEY`/`S3_BUCKET`/`S3_USE_SSL` — already updated if you're
  pulling this compose file fresh, with working local-dev defaults. On a
  VPS, set real `S3_ACCESS_KEY`/`S3_SECRET_KEY` values in `.env` (see
  DEPLOY.md's "Configure the environment" and "Security checklist").
  Back up the new `minio_data` volume alongside the database (see
  DEPLOY.md "Ongoing operations").
- **CI note:** `go.mod`'s `go` directive moved to `1.25.0` (a transitive
  dependency of the new S3 client requires it) — `.github/workflows/ci.yml`
  now installs Go 1.25 instead of 1.24.

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
