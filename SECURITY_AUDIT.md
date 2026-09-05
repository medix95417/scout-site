# Security Audit — Scout Site (Phase 1)

**Date:** 2026-08-18 (initial audit); **updated 2026-08-18** with a follow-up pass implementing all three items that were previously left as recommendations (see "Follow-up: recommendations now implemented" below); **updated again 2026-08-18** to extend rate limiting to the "forgot password" flow, at the requester's explicit request given that some of this site's accounts belong to minors (see "New: password-reset rate limiting" below).
**Scope:** Full codebase as of this audit — auth, sessions, password reset, self-service roster management, calendar/RSVP, approval workflow, homepage content editing, activity log, email system, deployment configuration (Docker Compose, Caddy).
**Method:** Manual line-by-line review of every handler, every SQL query, every template, and every piece of deployment configuration, cross-referenced against the requirements doc's permission model. Where a finding involved a fix, the fix was implemented and re-verified (see "Verification" at the end) — this is not a report-only audit.

**A note on scope of the "re-audit":** everything below is a code-level review of what's in this repository — the same method as the original pass. It is not a live penetration test of the running VPS (this environment has no network path to `troop.47-yonkers.org` / `pack.47-yonkers.org`), and it doesn't replace a `docker compose up -d --build` confirming the code actually compiles and runs on your server, since this sandbox still can't fetch `golang.org/x/crypto` or run a real `go build`. See "Verification" for exactly what was and wasn't checked.

## Bottom line

Your two direct questions, answered first:

- **Are passwords stored as a salted hash?** Yes. Every password (login, temporary passwords, reset flow) is hashed with bcrypt (`bcrypt.DefaultCost`, cost factor 10) before storage — bcrypt salts every hash automatically and uniquely per password, so no two identical passwords ever produce the same stored hash. No password is ever written to disk, logged, or stored anywhere in plaintext. Confirmed by reading every code path that touches a password and by grepping the entire codebase for any log statement that could contain one — there are none.
- **Any data leaks?** One real one was found and fixed (see Finding 1 below) — a unit-wide leader (e.g., a Scoutmaster) could view, rename, or reset the password of a family with zero relationship to their own unit, by guessing/editing a member ID in the URL. That's now closed. Two related cross-tenant issues (Findings 2 and 3) were also found and fixed. Nothing else rose to the level of a data leak — see "What's already solid" below for what was checked and came back clean (SQL injection, XSS, PII exposure via the roster/audit pages, email header injection, secrets handling).

Three vulnerabilities were found and **fixed as part of this audit** (code changes described below, already applied to your codebase). Two lower-severity gaps were fixed as well. A handful of items are flagged as recommendations for follow-up, not fixed now, because they're feature-sized work (rate limiting) rather than one-line corrections.

---

## Findings fixed in this pass

### 1. CRITICAL — Unit-wide leaders could manage members outside their unit, including resetting an unrelated family's password

**File:** `internal/roster/roster.go`, `Scope.CanManageMember`

The function that gates every roster-admin action (view a member, rename them, grant them a role, reset their password) had this shortcut:

```go
func (s Scope) CanManageMember(...) (bool, error) {
    if s.UnitWide {
        return true, nil   // <-- true for ANY memberID, no unit check
    }
    ...
}
```

A Scoutmaster, Assistant Scoutmaster, Cubmaster, or super_admin is "unit-wide" *within their own unit* — but this code returned `true` for **any** member ID, including one belonging to a family in the other unit (or any family in the database) that had never had any relationship to the acting leader's unit. The scoped case (Den Leaders) correctly checked for an existing `role_assignments` row; the unit-wide case skipped that check entirely.

**Impact:** Reachable via four routes — `GET/POST /admin/roster/members/{id}`, `POST /admin/roster/members/{id}/roles`, and critically **`POST /admin/roster/members/{id}/reset-password`** — this let a leader of one unit, given any member's UUID, view that member's name and family name, rename them, grant them a role in their own unit, or **reset their password and lock the real family out of their own account.** The normal UI never surfaces such an ID (the roster list is correctly unit-filtered), so exploitation required direct URL manipulation — but the check itself was simply absent, not merely obscured.

**Fix:** `CanManageMember` now always requires at least one `role_assignments` row for `(memberID, unitID)` to exist, and only *then* checks whether the scope (unit-wide vs. a specific den/patrol) covers it. Every legitimate workflow already only ever links to members who already have a role in the current unit, so this closes the hole without changing any real behavior.

### 2. HIGH — A leader could approve or reject the other unit's pending event

**Files:** `internal/approval/approval.go` (`Decide`), `internal/web/web.go` (`ApprovalDecide`)

`Decide` updated an `approval_requests` row by ID and status only — it never checked that the request belonged to the unit the acting approver actually had `CanApprove` rights in:

```sql
UPDATE approval_requests SET status = $1, ... WHERE id = $3 AND status = 'pending'
```

**Impact:** A Troop Scoutmaster (who legitimately holds approve rights for the Troop) could approve or reject a **Pack** SPL/Patrol-Leader-submitted event by guessing its request ID, since the handler only checked "does this person have approve rights *somewhere*," not "does this specific request belong to their unit." Approving/rejecting also flips the underlying event's publish status, so this could publish or kill an event in a unit the actor has no legitimate authority over.

**Fix:** `Decide` now takes `unitID` and requires `unit_id = $unitID` in the same `UPDATE ... WHERE` clause (atomic, no separate check-then-act race), returning a distinct `approval.ErrNotFound` the handler maps to a 404. The handler now passes the current unit's ID.

### 3. MEDIUM — A member of one unit could RSVP to the other unit's event

**Files:** `internal/calendar/calendar.go` (`SetRSVP`), `internal/web/web.go` (`CalendarRSVP`)

Same root cause as Finding 2, lower blast radius: `SetRSVP` inserted an RSVP row keyed only by event ID and member ID, with no check that the event belonged to the unit the member was actually logged into. A logged-in family in one unit could record an RSVP against the other unit's event by guessing its ID.

**Fix:** `SetRSVP` now takes `unitID` and only inserts if `EXISTS (SELECT 1 FROM events WHERE id = $eventID AND unit_id = $unitID)`, done as a single atomic `INSERT ... SELECT ... WHERE EXISTS`. Zero rows affected now returns a distinct `calendar.ErrEventNotFound`, mapped to a 404. Also added while touching this function: the RSVP response value (`yes`/`no`/`maybe`) is now validated in Go before hitting the database, rather than relying solely on the Postgres enum constraint to reject bad input.

### 4. MEDIUM — Password reset didn't invalidate existing sessions

**Files:** `internal/auth/auth.go` (`ConsumeResetToken`, new `DestroySessionsForUserTx`/`DestroySessionsForFamily`), `internal/roster/roster.go` (`ResetFamilyPassword`)

Neither the self-service "forgot password" flow nor the leader-triggered reset from `/admin/roster` invalidated the account's existing sessions. If a session cookie had leaked or been left signed in on a shared/stolen device, it would have stayed valid *even after* the password was changed specifically to shut that access out — defeating the point of a password reset as an incident-response tool.

**Fix:** `ConsumeResetToken` now deletes every session for that user inside the same transaction as the password change (atomic — if the transaction fails, neither the password change nor the session wipe takes effect). `ResetFamilyPassword` (the leader-triggered path) now deletes every session for every login on that family as a best-effort follow-up step (logged, not blocking, so a session-table hiccup never prevents a leader from handing off a working temporary password).

### 5. LOW — Misleading doc comment on `SESSION_SECRET`

**File:** `internal/config/config.go`

The comment claimed `SESSION_SECRET` "signs session cookies." It doesn't — sessions are opaque `crypto/rand` tokens validated against a server-side table (by design, per `internal/auth`'s own package comment), so there's nothing to sign yet. Not exploitable (the actual session mechanism doesn't depend on this value at all), but a misleading comment about what protects login sessions is exactly the kind of thing that causes a real mistake later. Corrected to explain why the variable is still required (forces every deploy to generate a real secret now, ready for Phase 2 when something *does* need signing) rather than claiming a protection that isn't there.

---

## What's already solid (checked, not just assumed)

- **SQL injection:** Every one of the 63 query call sites in the codebase uses parameterized placeholders (`$1, $2, ...`); grepped the entire repo for any query built via string concatenation or `fmt.Sprintf` with SQL keywords — none found.
- **XSS:** `html/template` (contextually auto-escaping) is used exclusively — grepped for `text/template` and for any use of the unsafe `template.HTML`/`template.JS`/`template.URL`/`template.CSS` escape-hatch casts; none exist anywhere in the codebase. User-supplied content (event titles, family names, homepage content, a leader-pasted image URL used inside a CSS `url()`) all flow through the escaper with no bypass.
- **Email header injection:** Verified specifically, not just assumed — a malicious event title containing embedded CRLF characters cannot smuggle extra SMTP headers into a reminder email, because `mime.QEncoding.Encode` (already used for the `Subject:` header) triggers full Q-encoding on any string containing a byte outside printable ASCII, which includes `\r`/`\n` — confirmed by reading the Go standard library's own `needsEncoding` implementation. Recipient addresses are validated with `net/mail.ParseAddress` before ever reaching the SMTP `RCPT TO` command, which rejects any embedded control characters.
- **CSRF:** Every state-changing route in the app is POST-only (verified against the full route table — zero GET routes with side effects), and the session cookie is `HttpOnly`, `Secure` (in production), and `SameSite=Lax`. Lax-mode cookies are not sent on cross-site POST requests at all, which is the primary CSRF vector for this app's request pattern. Not a dedicated CSRF token, so noted as a hardening recommendation below rather than a finding — the current setup meaningfully mitigates the risk, it just doesn't eliminate reliance on browser cookie-attribute behavior.
- **Secrets hygiene:** `.env` is git-ignored. Postgres and the app's own HTTP port are both bound to `127.0.0.1` only, even in the production `docker-compose.yml` — neither is reachable from the public internet; only Caddy's 80/443 are exposed, and Caddy issues real Let's Encrypt certificates and auto-redirects HTTP→HTTPS by default.
- **Host header trust:** Tenant routing (`troop.` vs `pack.`) resolves from the incoming `Host` header, which is safe here specifically because Caddy's site blocks only forward traffic that already matched one of the two real, DNS-verified hostnames — the app never receives a request with a spoofed Host header from the public internet.
- **Audit log:** Every `audit.Log` call site was checked — none ever logs a password hash or other secret; the family-creation entry explicitly logs a hand-built `{name, email}` map rather than the full user record, and the `/audit` page's own query doesn't even select the `before_state`/`after_state` columns, so nothing in there could reach the UI even if some future code accidentally over-logged.
- **Roster page PII:** The plain `/roster` view (visible to any logged-in unit member, not just leaders) only ever carries names, den/patrol, and role — no email address, phone number, or password hash is in the struct it's built from.
- **Reset token / session token entropy:** Both use 32 bytes from `crypto/rand` (256 bits), base64-encoded — well above any practical brute-force floor. Reset tokens are single-use, checked and consumed atomically via `SELECT ... FOR UPDATE` inside a transaction, preventing a race that could redeem the same token twice.

---

## Recommendations from the original pass — now implemented

All three items originally listed here as "not fixed in this pass" have since been implemented. They're kept below (struck through in spirit, kept in full for the record) with a note on how each was closed.

- ~~**No rate limiting / lockout on login or forgot-password.**~~ **Implemented:** account-level lockout on login. See "New: login lockout" below.
- ~~**No dedicated CSRF token.**~~ **Implemented:** double-submit-cookie CSRF protection on every state-changing route. See "New: CSRF protection" below.
- ~~**The `/audit` activity log is visible to every logged-in family member, not just leaders.**~~ **Implemented:** `/audit` now requires `CanEditUnitContent` (leader roles), same gate as `/admin/roster` and `/admin/home`. See "New: `/audit` restricted to leaders" below.

---

## Follow-up: recommendations now implemented

### New: login lockout

**Files:** `internal/db/migrations/0003_lockout.sql` (new), `internal/auth/auth.go` (`CheckLockout`, `RecordLoginFailure`, `ClearLoginFailures`, `ErrAccountLocked`, `Authenticate`), `internal/web/web.go` (`LoginSubmit`)

A new `login_failures` table tracks failed attempts keyed by normalized email address (not by IP — this app has no reverse proxy header it can trust as a real client IP, and an account-level lock is what actually protects one account regardless of how many source addresses an attack comes from). `Authenticate` now checks this **before** looking up the user or running any bcrypt comparison:

- 8 failures (`MaxLoginFailures`) within a 15-minute window (`LoginLockoutWindow`) locks the email out for the remainder of that window.
- A failure older than the window doesn't count toward a new lockout — the counter resets on the next failure rather than accumulating forever.
- Failures are recorded for **every** failed attempt, including against emails with no account at all, and lockout applies the same way either way — so lockout behavior itself can't be used as an oracle to test which emails have accounts (the same reasoning `ErrInvalidCredentials` already used for the error message).
- A successful login clears the account's failure record immediately, so a few mistyped passwords followed by the correct one don't force a wait.
- The login page shows a distinct message ("Too many failed login attempts...") when locked out, versus the generic "Incorrect email or password" for a simple wrong guess — this does confirm to an attacker that lockout kicked in, which is an accepted, standard trade-off (matches how most production login forms behave) rather than an oversight.

Originally not extended to `/forgot-password` in this pass on the reasoning that its abuse case (inbox-flooding, not credential guessing) was lower-severity — since revisited and implemented; see "New: password-reset rate limiting" below.

### New: password-reset rate limiting

**Files:** `internal/db/migrations/0004_password_reset_rate_limit.sql` (new), `internal/auth/auth.go` (`RecordAndCheckPasswordResetRequest`), `internal/web/password_reset.go` (`ForgotPasswordSubmit`)

A new `password_reset_requests` table caps how many "forgot password" submissions a single email address can trigger an actual outbound email for: 3 (`MaxPasswordResetRequests`) per rolling hour (`PasswordResetRequestWindow`, chosen to match `ResetTokenDuration` — by the time the window resets, any earlier token has expired anyway). Same shape as the login-lockout table and same reasoning: keyed by normalized email (not IP, for the same reverse-proxy-trust reason as login lockout), and the counter is recorded for **every** submission — including against an email with no account — so the rate-limit state can never be used to test which emails are registered.

The critical design point, and the reason this is a slightly different shape than login lockout: **the HTTP response the visitor sees never changes.** `/forgot-password` already showed the identical "if that email has an account, we've sent a link..." message regardless of whether the account existed (see `UserByEmail`'s doc comment); rate limiting had to preserve that exactly, or a distinguishable "you've been rate-limited" message would itself become a new way to test something (in this case, "has this address already had 3 reset requests recently," which is weak signal, but the whole point of the existing design was zero distinguishable signal). So a request beyond the cap silently skips sending the email — same page, same flash text, same everything — while an operator-facing log line records that it happened. This directly protects any account's inbox, including a Scout's own login, from being flooded by repeated submissions, whether from a prankster, a bug in some other system retrying the form, or genuine malice.

A failure to record a rate-limit attempt (a database hiccup) fails open — the reset still goes out — on the reasoning that an unrelated bookkeeping error should never be the reason a real, legitimate password reset silently fails to send, especially for a feature that exists to help someone regain account access.

### New: CSRF protection

**Files:** `internal/csrf/csrf.go` (new package), `cmd/server/main.go` (middleware wiring), `internal/web/web.go` (`baseData.CSRFToken`), every template with a `<form method="post">` (9 templates, 15 forms total)

Implemented via the double-submit-cookie pattern: every visitor — logged in or not — gets a random 256-bit token (`crypto/rand`, via the same `auth.RandomToken` sessions and reset tokens already use) in a new `scoutsite_csrf` cookie (`HttpOnly`, `Secure` in production, `SameSite=Lax`, ~180-day lifetime). Every page render embeds that same value as a hidden `csrf_token` field via `baseData.CSRFToken`. Every `POST` request is checked against the cookie with `crypto/subtle.ConstantTimeCompare` (constant-time, so response timing can't leak how much of the token matched) — a missing or mismatched token gets a 403 with a message asking the visitor to reload and retry.

Chosen over a session-tied synchronizer token specifically because it also protects the pre-login forms (`/login`, `/forgot-password`) that have no session cookie yet to tie a token to. This is genuine defense-in-depth on top of the `SameSite=Lax` + POST-only-mutations protection the original audit already found adequate — worth having now, and it'll matter more once Phase 2 adds a payment/financial feature where a forged request would carry real financial consequences.

All 15 forms across 9 templates were updated and each one was verified two ways: (1) `grep` confirmed every `<form method="post">` in the codebase now has a `csrf_token` hidden input, with no stragglers; (2) three of the templates were actually executed with `html/template` against mock data (not just visually inspected) — this matters because 4 of the 15 forms live inside a `{{range}}` loop, where Go's template scoping rebinds `.` to the loop item and referencing `.CSRFToken` there would silently render empty (or error, depending on the field) rather than the top-level token. Those four correctly use `{{$.CSRFToken}}` instead, and the executed-template test confirms the real value renders correctly inside the loop, not just that the template parses.

### New: `/audit` restricted to leaders

**File:** `internal/web/web.go` (`AuditView`), `internal/web/templates/base.html`

`AuditView` now computes the acting family's roles and requires `units.CanEditUnitContent(roles)` — the same gate `/admin/roster` and `/admin/home` already use — returning a 403 for anyone else, instead of just checking "logged in." The "Activity Log" nav link in `base.html` was moved inside the same `{{if .CanEditContent}}` block as "Manage Roster" and "Edit Homepage", so it no longer even appears for a Scout/Parent account that can't access the page anyway.

### Later finding (found while adding activity log filtering, not part of either audit pass above): roster/role changes were never actually visible on `/audit`

**File:** `internal/audit/audit.go`

The permission gate above was always correct, but the query behind it wasn't complete: `ForUnit`'s `entity_id IN (...)` scoping subquery only covered `events`, `content_pages`, and (from Phase 2) the four ledger entity types — it never included `family`, `member`, `role_assignment`, or `sub_group`, even though `internal/roster` has been calling `audit.Log` with those entity types since Phase 1. The rows were being written to `audit_log` correctly; they just never matched the subquery that decides what a given unit's `/audit` page shows, so they silently never appeared. This means the bottom-line claim above ("the activity log surfaces things like who reset whose password and every role change") was accurate about the *gate*, not the *data* — a leader looking at `/audit` before this fix would not actually have seen a password reset or a role grant, despite both being logged.

**Impact:** Not a data exposure (the opposite — less was shown than should have been) and not reachable by an attacker to hide their own actions differently than before, since the write path (`audit.Log`) was never affected, only the read path. Still a real accountability gap worth flagging here: anyone relying on `/audit` to answer "who changed this family's role/password and when" was getting an incomplete answer for roster-related changes specifically, for as long as this has existed.

**Fix:** `family`/`member`/`role_assignment`/`sub_group` were added to the shared entity-scoping SQL (now factored out as `entityScopeSQL`, reused by the view, the CSV export, and the new filter-dropdown queries) — scoped through `role_assignments` the same way `units.RolesForFamilyInUnit` already answers "is this member/family part of this unit," since neither `members` nor `families` has a `unit_id` column of its own. See `README.md`'s "Also added post-Phase-2" section for the full writeup of this change alongside the news/gallery/audit-filtering work it shipped with.

### New: individual Scout logins — permission/ownership resolution had to move from "family" to "the current login," everywhere, not just in the obvious places

**Files:** `internal/web/web.go` (`rolesFor`, `actingMember`, `isAccountOwner`, `rosterScope`), `internal/web/admin_roster.go`, `internal/web/treasury.go`, `internal/web/content_posts.go`, `internal/web/settings_admin.go`, `internal/web/audit.go`, `internal/roster/roster.go` (`ScopeForMember`)

Adding a second kind of login (an individual member login, alongside the original family-wide one — see `internal/db/migrations/0009_member_logins_and_text_settings.sql`) meant every place that previously assumed "resolve permissions/acting-identity from `user.FamilyID`" needed a member-scoped alternative, or an individual Scout's login would either (a) incorrectly inherit whatever roles/scope anyone else in their family holds, defeating the "just their own stuff" design, or (b) incorrectly get *narrower* access than intended by missing a case entirely. Both are real risks in a change like this: (a) is a privilege-leak in the wrong direction (a Scout's own login seeing a parent's Scoutmaster-only pages), (b) is just broken, not unsafe.

This pass audited every call site of `units.RolesForFamilyInUnit` and `family.ActingMemberForFamilyInUnit` across `internal/web` (there were nine) and replaced them with calls through two new resolver helpers (`h.rolesFor`, `h.actingMember`) that branch on `user.MemberID`. A third case — ledger account ownership (`family.MemberBelongsToFamily`, used by `TreasuryAccountView`/`TreasuryRequestTransfer` to let a family see/manage their own Scout's account) — got its own helper (`isAccountOwner`) since "owns" means something different for the two login kinds: exact member match for an individual login, broader family membership for a family-wide one. A fourth, found only during this same audit rather than requested directly: `roster.ScopeForFamily` (which computes a leader's den/patrol-management scope from role assignments) had the identical problem — an individual login belonging to, say, an Assistant Scoutmaster would have had its roster-admin scope silently broadened by whatever roles *other* members of their family happen to hold, purely because the lookup was keyed on `family_id` rather than the specific member. Added `roster.ScopeForMember` and a `rosterScope` resolver alongside the other two, same pattern.

**Impact:** none of this shipped in an exploitable state — it was caught and fixed in the same change that introduced individual logins, before any real individual login existed to be affected. Documented here because it's exactly the kind of gap that's easy to miss in a smaller review (nine call sites across six files, one of them a second-order case that only surfaces if the *acting* login also happens to hold a leadership role) and because the pattern — "grep every call site of the family-scoped function, not just the ones the feature request obviously touches" — is the actual takeaway for the next login-model change, not just this one.

**Design note — SMTP password stays out of the database:** alongside individual logins, this pass also made SMTP host/port/username/from editable from `/admin/settings` (`internal/settings.TextSettings`), but deliberately left `SMTP_PASSWORD` as environment-variable-only (see `internal/mailer.Mailer.effective`). This wasn't an oversight — the tradeoff was raised explicitly before implementing, and the choice to keep the one real secret in this configuration out of Postgres matches this codebase's existing posture everywhere else: every account password is bcrypt-hashed before storage, and until now there was no precedent at all for storing a plaintext, directly-usable credential in the database. A future change that wants a fully-database-configurable mail setup should treat that as a deliberate new category of risk to accept, not a natural extension of this one.

---

## Verification

- `gofmt -l .` clean across the entire repository after every change, including this follow-up pass.
- A custom Go-AST-based checker (no external dependencies, since this sandbox has no network access for `go mod`) swept the whole repo for unused imports after every change. It flags `github.com/jackc/pgx/v5` as an "unused import" in three files (`auth.go`, `approval.go`, `roster.go`) — this is a confirmed false positive specific to this checker: the import path ends in `v5` but the actual package name is `pgx`, and `grep "pgx\."` in each file confirms real usage (e.g. `pgx.ErrNoRows`, `pgx.Tx`).
- A duplicate-top-level-declaration checker swept the whole repo — clean, no accidental redeclarations (relevant here since `RandomToken` was renamed from the unexported `randomToken` in the prior pass, and this pass added several new exported functions to the same file).
- Every changed/new function signature (`CheckLockout`, `RecordLoginFailure`, `ClearLoginFailures`, the CSRF middleware, `AuditView`'s new role check) was manually cross-checked against every call site for type/argument-order correctness. This sandbox still cannot run `go build` end-to-end — `golang.org/x/crypto` and other transitive dependencies aren't cached locally and can't be fetched — so this manual cross-check plus the template-execution tests below are the strongest verification available here; your own `docker compose up -d --build` remains the real first compile, same caveat as every other deliverable in this project.
- **New this pass:** three of the nine touched templates (`calendar.html`, `admin-roster-member.html`, `content-admin.html`) were actually parsed and executed with Go's real `html/template` package against mock data structurally matching their handlers' real data shapes (not hand-inspected only) — specifically to catch a template-scoping mistake in the `{{$.CSRFToken}}`-inside-`{{range}}` forms before it could ship as a silent empty-token field or a hard template-execution error. All three executed with zero errors; the rendered output was grepped to confirm the token value actually appears in each of the range-nested forms (verified per-iteration, e.g. two separate `{{range .Roles}}` iterations each correctly picked up the same top-level token). The other six touched templates have no `{{range}}`-nested forms, so a plain `.CSRFToken` reference is unambiguous and was verified by direct read of the rendered template source.
- Both migrations (`0003_lockout.sql`, `0004_password_reset_rate_limit.sql`) follow the existing hand-rolled migration runner's convention (`internal/db/db.go`) exactly — numbered `NNNN_*.sql` files, applied automatically and idempotently (tracked in `schema_migrations`) the next time the app starts, with **no separate manual `-migrate` step required** (confirmed by reading `cmd/server/main.go`: `db.Migrate` runs unconditionally before the `-migrate`-only early-return, i.e. on every normal server start too). Kept as two separate files rather than editing `0003_lockout.sql` in place, since that file may already have been applied to your database by the time this update was requested — migrations already shipped are treated as immutable history here, same convention `0001_init.sql`/`0002_email.sql` already establish.
- `RecordAndCheckPasswordResetRequest`'s call site in `ForgotPasswordSubmit` was manually traced end to end to confirm the response really is identical in both the allowed and rate-limited branches — the same `Submitted: true` struct literal, the same template, the same log level for a person who can't see server logs (nothing at all is exposed to the visitor either way).
- The atomic fixes from the original pass (approval/RSVP unit checks, session invalidation on reset) and this pass's lockout counter logic were all written as single SQL statements (`INSERT ... ON CONFLICT ... DO UPDATE` for the failure counter) specifically to avoid a check-then-act race under concurrent requests.
- **New this pass (individual Scout logins, `/accounts`, SMTP settings):** `gofmt -l .` clean across the whole repository after every change. The unused-import checker's `github.com/jackc/pgx/v5` false positive (see above) now also appears in `internal/settings/settings.go` and `internal/ledger/ledger.go`, both re-confirmed as the same false positive via `grep "pgx\."` (real usage: `pgx.ErrNoRows`, `pgx.Row`). The duplicate-declaration checker is clean. Every new/changed exported function signature (`auth.User.MemberID`, `auth.DestroySessionsForMember`, `family.GetMember`, `roster.CreateMemberLogin`/`ResetMemberLoginPassword`/`MemberHasLogin`/`MemberLoginEmail`/`ScopeForMember`, `units.MemberHasAnyTreasuryRole`, `ledger.ScoutAccountForMember`, `settings.GetText`/`SetText`/`AllText`, `mailer.New`'s new `pool` parameter and `Mailer.Enabled`'s new `ctx` parameter) was manually cross-checked against every call site — including re-ordering `cmd/server/main.go` so the mailer is constructed after the database pool connects, since `mailer.New` now needs it. This sandbox still cannot run `go build` end-to-end (same transitive-dependency caveat as every prior pass), so this manual cross-check plus the template-execution harness below remain the strongest verification available here.
- The `/tmp/tmplcheck2` template-execution harness (persistent across this project's sessions) was extended with new scenarios for every new/changed template this pass touched: `accounts.html` (with accounts and empty), `admin-roster.html` (editable/non-editable rows, including the new "Reset Password" list link), `admin-roster-member.html` (an adult with no individual login yet, and a youth member who has one — covering both the family-password branch, which is adult-only, and the individual-login branch, which isn't), `admin-roster-credentials.html`, and `admin-settings.html`'s new text-settings section (including an adversarial `"><script>alert(1)</script>` value in a stored settings field, confirming contextual auto-escaping holds for admin-entered configuration text the same way it already did for leader-authored content and system-generated QR data). All render with zero errors; the adversarial value round-trips HTML-escaped, not executable, in the rendered output.

---

# Audit pass 3 — post-public-site changes (2026-08-22)

**Scope:** everything merged since the last pass — PRs #42–#55: settings-page
social media toggles, the accessibility pass, the family-directory fix,
per-event permission slips, the email-hang and background-newsletter fixes,
and the public-site batch (nav/footer, calendar end dates, homepage layout,
Photos redesign, the new Our Leaders page, accordion disclosures, and the
newsletter / password-reset kill switches).

**Method — and how this pass differs from the previous two:** every prior pass
carried the caveat that the sandbox could not run `go build`, reach a database,
or exercise the app. That is no longer true. This pass was run against a real
Postgres instance with the demo dataset loaded and the server actually running,
so every claim below is backed by an executed request rather than by reading
code alone. Findings were reproduced live before being fixed and re-verified
live afterwards; both fixes also ship with a regression test, and each test was
confirmed to actually fail when its fix is reverted (a passing test that cannot
fail proves nothing).

## Findings fixed in this pass

### 1. MEDIUM (accountability, not exposure) — eight audit entity types were written but could never be displayed

**File:** `internal/audit/audit.go` (`entityScopeSQL`)

`audit_log` rows are filtered for display by `entityScopeSQL`, a UNION that
answers "which entity IDs belong to this unit." Eight of the twenty-two
`EntityType` values the codebase logs had no branch in it, so their rows were
written correctly and then were unreachable from every read path — the activity
log page, its CSV export, and its filter dropdowns alike:

`advancement_record`, `custom_role`, `leader`, `newsletter`, `permission_slip`,
`permission_slip_signature`, `saved_treasury_report`, `unit_setting`.

Measured on the demo database, **48 real audit rows were affected**. The most
consequential are `custom_role` and `unit_setting`: creating a custom role is a
privilege-granting action (a role can carry `manage_ledger`), and per-unit
settings now include security-relevant kill switches — so "who granted treasury
access" and "who turned off self-service password reset" were both
unanswerable from the activity log. `leader` was introduced by PR #52 in this
very batch; the other seven predate it.

This is the *same defect the previous pass already found and fixed once* for
`family`/`member`/`role_assignment`/`sub_group`. It recurred because nothing
enforced the invariant — adding an `audit.Log` call with a new `EntityType`
requires a matching `entityScopeSQL` branch, and omitting the second half fails
silently and invisibly.

**Impact:** not a data exposure — strictly less was shown than should have
been, and the write path was never affected, so no attacker could use this to
hide their own actions differently than anyone else's. It is a real
accountability gap: a leader relying on `/audit` to answer "who changed this"
got a confidently incomplete answer for those eight categories.

**Fix:** all eight added to `entityScopeSQL`, with
`permission_slip_signatures` scoped through its parent slip (it has no
`unit_id` of its own). Verified live: the filter dropdown now offers every
type, and per-unit counts are strict subsets of the global totals — the Troop's
log shows 2 leader entries and the Pack's shows 1, exactly matching the
database — confirming the new branches did not introduce cross-unit leakage
into the activity log, which is the obvious way this fix could have gone wrong.

**Durable fix:** `internal/audit/audit_entity_scope_test.go` walks the whole
repository for `EntityType:` literals and fails the build if any is missing
from `entityScopeSQL`, plus a second test that catches stale mappings left
behind after a call site is removed. This is the third occurrence of this class
of bug; a comment asking future authors to remember was demonstrably not enough.

### 2. LOW–MEDIUM (availability) — one NULL event description took down the calendar and homepage for everyone

**File:** `internal/calendar/calendar.go` (`eventColumns`)

`events.description` and `events.location` are nullable in `0001_init.sql`, but
`eventColumns` selected them bare into plain `string` scan targets. Because
`queryEvents` scans rows in a loop, a single NULL row does not degrade that one
row — it aborts the entire query. Found accidentally while seeding test events
for the calendar end-date checks below: one hand-written `INSERT` that left
`description` NULL turned `/calendar` into a 500 and blanked the homepage's
upcoming-events list, for every visitor, anonymous and logged-in alike.

**Impact:** availability only; no confidentiality or integrity dimension. Not
reachable through the application's own write paths today — `calendar.Create`
takes Go `string`s and therefore always writes `''`, never NULL — so this is
latent rather than live. It becomes reachable through a hand-written `INSERT`,
a restored or migrated backup, or any future import path, and the blast radius
is a total outage of two of the site's most-visited pages rather than one
missing row. The rest of the codebase already defends against exactly this with
`COALESCE(col, '')`, including on `events.location` in `internal/files`' own
join against this same table, so this was an inconsistency rather than a
considered decision.

**Fix:** both columns `COALESCE`'d in `eventColumns`, matching the existing
convention.

**Durable fix:** `internal/calendar/event_columns_test.go` parses the
migrations for the `events` table's nullable columns and asserts every one that
is scanned into a plain string is `COALESCE`'d — so a nullable column added to
`events` by a future migration fails in CI rather than in production. It runs
without a database, since CI has no Postgres service.

**Related sweep, came back clean:** the whole schema was checked for the same
class. There are eight nullable `text` columns across all tables
(`events.description`, `events.location`, `families.address`,
`members.cell_phone`/`email`/`home_phone`, `resources.url`,
`sub_groups.description`, `system_settings.value_text`,
`unit_settings.value_text`, `units.logo_url`). Every one other than the two
above is already either `COALESCE`'d or scanned into a pointer, which is the
correct idiom. `sub_groups.description` was additionally verified empirically —
forced to NULL in the live database, after which `/groups`, the group detail
page, `/admin/groups/{id}`, `/admin/roster` and `/calendar` all still returned
200 with nothing in the error log.

## Checked and clean (executed, not assumed)

- **XSS in the new Leaders feature, across all three escaping contexts.** A
  leader profile was created through the real admin form with
  `X"><script>alert(1)</script> O'Brien');alert(2);//` as the name,
  `</p><svg onload=alert(3)>` as the role title, `<img src=x onerror=alert(4)>`
  as the bio, and `javascript:alert(5)` as the photo URL, then rendered on both
  the public page and the admin list. HTML context escaped
  (`&#34;&gt;&lt;script&gt;`); the JS-in-attribute context — the
  `onsubmit="return confirm('Remove {{.Name}}…')"` delete confirmation, the
  riskiest new markup in the batch — correctly JS-escaped to
  `"><script>` and `O'Brien'`, so the apostrophe
  cannot terminate the `confirm()` string; and the `javascript:` URL was
  neutralised by `html/template`'s URL filter to `#ZgotmplZ`. No payload
  survived in executable form anywhere.
- **Cross-tenant isolation on the new Leaders CRUD.** A Troop Scoutmaster
  holding no Pack role, given a Pack leader's UUID, got 404 on all four routes
  (`GET .../edit`, `POST` update, publish, delete); the row was confirmed
  unchanged afterwards, and the Pack leader never appeared on the Troop's
  public page. A Parent got 403 on the admin list. This is the same shape as
  pass 1's Finding 1, re-tested against new code.
- **Members-only content never leaks to anonymous visitors** — the highest-risk
  question for the redesigned homepage and Photos pages, since the Photos list
  now carries every photo of every album rather than just a cover image. A
  published members-only album was invisible on the anonymous homepage and
  `/gallery` (title and captions both), returned 404 on its direct URL, and was
  correctly visible to a logged-in family. The homepage's "Recent Activities"
  preview stayed public-only even when rendered for a logged-in user.
- **Draft leader profiles are never public** — verified through the full
  draft → publish → unpublish lifecycle.
- **Permission model, swept as a matrix rather than spot-checked.** 27 routes ×
  4 Troop personas (anonymous, parent, scoutmaster, super admin) and 10 routes ×
  3 Pack personas. Every cell matched intent: anonymous redirects on everything
  non-public, parents 403 on all admin surfaces, scoutmasters 403 on treasury /
  settings / custom roles, super admin through. No regressions from the
  accordion refactors, which touched many of these templates.
- **Den-leader scoping.** A Den Leader saw 3 editable members against a
  Cubmaster's 15, got the scoped-notice copy, and was 403'd on a member outside
  her den — the area pass 2 found a second-order bug in.
- **The two new kill switches are properly gated.** Neither a scoutmaster nor a
  parent can flip the site-wide password-reset switch or the per-unit
  newsletter switch (403 both); the switch's stored value was confirmed
  unchanged after the attempts. Both routes reject a missing *and* a forged
  CSRF token (403).
- **No `{{range}}`-nested bare `.CSRFToken`.** The accordion work moved many
  forms inside `<details>` blocks, which is exactly the situation that produces
  pass 2's silent-empty-token bug. A nesting-aware scan of all templates found
  none; every CSRF field inside a loop correctly uses `{{$.CSRFToken}}`.
- **Newsletter sending has not regressed to hanging** (PRs #46/#47). With SMTP
  unconfigured, "Send now" returned HTTP 400 with a clear message in 2ms and
  left the newsletter as a draft, rather than blocking the request.
- **Calendar end-date filtering** (PR #49) behaves correctly against a
  discriminating set: a multi-day event still running shows, one that finished
  three days ago does not, a future single-day event shows, a past one does
  not. The `/calendar` month grid still shows past events in the current month,
  which is correct for a grid and distinct from the "upcoming" list.
- **Permission-slip enforcement** (PR #45): with enforcement off both a
  slip-requiring event and a weekly meeting offer the link; with it on the
  weekly meeting drops to none. The documented leader escape hatch works (200
  for a leader on an unflagged event, 404 for a parent, 303 for anonymous, 404
  cross-unit).

## Verification

- `gofmt -l .` clean and stable across repeated runs; `go build ./...`,
  `go vet ./...`, and `go test ./...` all pass, including the two new test
  files. The template-parse smoke test passes over all templates.
- Both new tests were confirmed non-vacuous by reverting their fix and watching
  them fail with the intended message, then restoring it.
- One caveat worth stating plainly: `go vet` and the test suite run without a
  database, and CI has no Postgres service, so both regression tests added here
  are deliberately static (they parse migrations and SQL constants) rather than
  round-tripping real rows. They catch the specific defects found, not every
  possible NULL-handling or audit-scope mistake.

---

# Pass: Content-Security-Policy hardening (nonce-based `script-src`)

The global CSP had no `script-src` at all. `default-src 'self'` covered
scripts, so nothing external could load — except the policy also had to
permit the CDNs the site genuinely uses (Tailwind, htmx, Quill, QRious),
and permitting them via `'unsafe-inline'` meant the policy stopped
constraining scripts in any way that mattered: an injected `<script>`
would have run.

## What changed

`internal/csp` generates a fresh 128-bit random nonce per request, puts
it in the request context, and sets:

    script-src 'nonce-<per-request>' https://cdn.tailwindcss.com https://cdnjs.cloudflare.com

Every inline `<script>` in the templates carries `nonce="{{.CSPNonce}}"`.
The 19 inline event-handler attributes (`onsubmit="return confirm(…)"`
and friends) that a nonce cannot rescue — CSP blocks attribute handlers
regardless — were converted to data attributes handled by delegated
listeners in `base.html`: `data-confirm`, `data-submit-on-change`, and
`data-roster-filter`.

`style-src` deliberately keeps `'unsafe-inline'`. Tailwind's play CDN
injects `<style>` elements at runtime that cannot be nonced. That is a
real, accepted limitation, not an oversight: it means the policy
constrains scripts but not styles, which is the trade this build makes in
exchange for not having a frontend toolchain.

## Verification

The header alone proves nothing — a CSP that silently blocks the site's
own scripts is worse than no CSP — so this was checked in a real browser
(Chromium via Playwright), signed in as a super_admin through the
mandatory TOTP step, not just on the public pages:

- **22 authenticated pages** rendered with zero CSP violations and zero
  JavaScript errors, covering every admin, treasury, roster, content and
  settings page that exists.
- **An injected inline `<script>` did not execute** (`window.__pwned`
  stayed undefined) and produced a CSP refusal — which is the entire
  point of the change.
- **All four converted mechanisms were exercised end-to-end**, not just
  inspected: the delegated `data-confirm` showed its prompt and *blocked
  the POST* on Cancel while allowing it on OK; `data-submit-on-change`
  auto-submitted and the database showed the resulting change; the roster
  filter (formerly `oninput=`) filtered.
- **The CDN allowlist was proven to still apply alongside the nonce.**
  This matters because a nonce voids host allowlists when
  `'strict-dynamic'` is present — this policy deliberately omits it. The
  sandbox's proxy blocks the real CDNs outright
  (`ERR_TUNNEL_CONNECTION_FAILED`), so the check was done with two local
  origins instead: a script from an allowlisted host loaded and executed;
  one from a non-allowlisted host was refused. Both confirmed in the same
  browser under the real policy.

One limitation worth stating: because the sandbox cannot reach
cdnjs.cloudflare.com or cdn.tailwindcss.com, the *real* Tailwind, htmx,
Quill and QRious loads were never observed succeeding under the policy.
What was proven is the enforcement rule that governs them. A first deploy
should still be spot-checked with the browser console open on
`/admin/newsletters/new` (Quill) and `/settings/2fa` (QRious).

---

# Design note: automatically hosting images out of an email body
*(added alongside the change, not a finding)*

## What the code now does

When a leader saves a prospect campaign or a newsletter, any image the
body carries as a `data:` URI is decoded, stored in the unit's file
library, and the body is rewritten to point at
`/files/{id}/download`. The row is created with **`is_public = true`**.
See `internal/web/inline_images.go` and
`internal/newsletter/inline_images.go`.

The motive is not security: Gmail and Outlook refuse to render a `data:`
URI image, so a logo embedded in an exported template arrived as a blank
gap, and the whole body was carried once per recipient.

## Why a public file is the correct outcome here, and what it costs

A mail client fetching an image has no session — it is an anonymous
stranger with a URL. There is no version of "image in an email" that is
members-only, so marking these public is not a weakening of the model,
it is stating what the feature already requires. It is the same act a
leader performs by hand today when they pick a library photo for the
public homepage.

The cost is real and worth naming: **an image put in an email is public
to anyone holding its URL**, and the URL is held by every recipient and
their mail provider. The id is an unguessable UUID and nothing links to
it, so this is not enumeration-exposed, but it is not a secret either. A
photo that should not be on the public site should not be in an email.
This is stated in both composers and in the help catalog rather than
left for a leader to infer.

## The constraints around it

- **Only images the sanitizer already accepted.** Hosting runs inside the
  sanitizing pass, and `sanitizer.hosted` re-derives the check itself
  (`imageDataURI`), so a src that would be rejected cannot be laundered
  into a hosted URL regardless of ordering. `data:` on `href` is still
  refused outright.
- **The declared type is never trusted.** `sniffContentType` re-derives
  the type from the bytes, and the result must be a format
  `writeUserFileHeaders` renders in place. SVG cannot reach storage: it
  is excluded from the data-URI pattern *and* absent from
  `inlineRenderableTypes`.
- **Bounded.** 10 MB per image, 40 images per save.
- **Per unit.** The storage key is `<unit id>/email-images/<sha256>`, and
  `files.ByStorageKey` is scoped by unit, so the Troop and the Pack keep
  separate copies of an identical image rather than sharing one owned by
  whoever saved first.
- **Never fatal.** Every failure leaves the image embedded and the draft
  intact.

## Verification

Guards were mutation-tested: creating the file private, dropping either
half of the type check, trusting the declared type, removing the size or
count cap, dropping the unit from the storage key or from the lookup,
swallowing a storage or insert error, and skipping the safety check on
the URL a store returns — each fails a test. The two SQL changes
(`files.Create` honouring `Public`, and `files.ByStorageKey`) are covered
by integration tests against a real Postgres, including a cross-unit
lookup that must not resolve.
