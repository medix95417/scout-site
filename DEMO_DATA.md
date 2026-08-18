# Demo / test data

`-seed-demo` creates a full set of test logins — one per role — plus
realistic calendar, ledger, fundraiser, and approval activity, so you can
click through every permission tier and every Phase 1 + Phase 2 feature
without hand-creating a dozen accounts first.

**This is test data for a fresh or staging deployment, not something to
run against a live site with real families already in it.** Every login
here is obviously fake (an `@example.com` address — reserved by RFC 2606
for exactly this, guaranteed never to be a real, deliverable address —
and one shared password), but it's still real rows in the same
`families`/`users`/`members` tables real data lives in.

## Running it

Same shape as `-seed` and `-bootstrap-admin` — run once, it exits:

```bash
docker compose run --rm app -seed-demo
```

Prerequisites: `-migrate` must already have run (it runs automatically
on every normal server start, so this is almost never something you need
to do by hand — see README.md) and `-seed` must have created the
Troop/Pack unit rows first. If either hasn't happened, `-seed-demo` fails
immediately with a clear error rather than partially creating data.

It prints every login it created straight to the terminal — email,
password, and (for the three logins enrolled in two-factor) the TOTP
setup key and backup codes. That output is the only place those appear in
plaintext, same as a real temporary password from `/admin/roster` — copy
it somewhere before it scrolls off, or redirect the command's output to a
file:

```bash
docker compose run --rm app -seed-demo > demo-logins.txt
```

Safe to re-run: if it finds the Super Admin demo login already exists, it
assumes the rest of the dataset does too and exits without creating
anything else. It does **not** repair a partially-completed run (e.g. one
interrupted mid-way by a crash or a `Ctrl-C`) — see "Starting over" below
if that happens.

Every seeded login shares one password:

```
ScoutDemo2026!
```

## The logins

Every login below shares the password above, **except Riley Morgan's
individual Scout login** (see the last row) — that one gets its own
randomly-generated temporary password, printed to the terminal the same
way a leader-created login's temporary password would be, since that's
what actually creating an individual login looks like end to end. "Unit"
is which subdomain to log in at (`troop.` for Troop 47, `pack.` for Pack
47) — a family's role only applies to the unit(s) it was granted in, same
as any real account.

| Role | Unit | Email | Notes |
|---|---|---|---|
| Super Admin | Troop 47 & Pack 47 | `alex.superadmin@example.com` | Full access everywhere, including Treasury in both units. Two-factor already enrolled and confirmed. |
| Scoutmaster | Troop 47 | `sam.scoutmaster@example.com` | Can edit content/calendar directly and approve SPL/Patrol Leader submissions. |
| Assistant Scoutmaster | Troop 47 | `ashley.asm@example.com` | Same content/approval rights as Scoutmaster. |
| Senior Patrol Leader | Troop 47 | `sydney.spl@example.com` | Youth member. Can submit events for approval but not publish directly. |
| Patrol Leader | Troop 47 | `presley.patrolleader@example.com` | Youth member, scoped to Eagle Patrol. Same submit-for-approval rights as SPL. |
| Treasurer | Troop 47 | `terry.treasurer.troop@example.com` | Two-factor already enrolled and confirmed — logs straight in to `/treasury`. |
| Parent + Scout | Troop 47 | `pat.parent.troop@example.com` | One family login: Pat Morgan (parent) and Riley Morgan (scout) share it. Riley has an individual ledger account with fundraiser credits already on it (see below). Pat holds **no** treasury role but is enrolled in two-factor anyway (already confirmed) — demonstrates that 2FA is opt-in for everyone, not just Treasurer/Admin. |
| Cubmaster | Pack 47 | `casey.cubmaster@example.com` | Can edit content/calendar directly (Cub Scouts has no youth-submission-approval workflow, matching the real program). |
| Den Leader | Pack 47 | `dana.denleader@example.com` | Scoped to Bear Den 3. |
| Treasurer | Pack 47 | `taylor.treasurer.pack@example.com` | **Two-factor NOT enrolled, on purpose** — logging in shows the persistent "set up two-factor" banner, and `/settings/2fa` starts from scratch, so you can test first-time enrollment end to end. |
| Parent + Scout | Pack 47 | `jesse.parent.pack@example.com` | One family login: Jesse Nakamura (parent) and Morgan Nakamura (scout) share it. |
| Scout (individual login) | Troop 47 | `riley.scout@example.com` | **Different, randomly-generated password — see above.** Riley Morgan's own login, separate from the `pat.parent.troop@example.com` family login her parent uses. Logging in as Riley shows just her own stuff: her individual ledger account balance (via "My Accounts" in the nav), her own RSVPs, and the `scout` role — none of whatever Pat's shared family login can otherwise do. Demonstrates the individual-Scout-login feature end to end. |

That's all 10 values of the `member_role` database enum covered:
`super_admin`, `scoutmaster`, `assistant_scoutmaster`,
`senior_patrol_leader`, `patrol_leader`, `treasurer`, `parent`, `scout`,
`cubmaster`, `den_leader`.

### Two-factor setup keys

Printed once by `-seed-demo` for Alex (Super Admin), Terry (Troop
Treasurer), and Pat (Troop Parent+Scout, voluntary enrollment) — add any
of the three to an authenticator app (Google Authenticator, Authy,
1Password, etc.) if you want to log all the way in as one of them rather
than just exercising the "not enrolled yet" banner via Taylor. You can
either scan the QR code shown on `/settings/2fa` after logging in with
the printed password, or type the setup key in manually — both options
are on that page now (see `PHASE2_TREASURY.md`, "Two-factor
authentication").

## What activity data exists, and what it's for

### Roster & sub-groups

- **Eagle Patrol** (Troop 47) — Presley (Patrol Leader) is scoped to it.
- **Bear Den 3** (Pack 47) — Dana (Den Leader) is scoped to it.

### Individual Scout logins

Riley Morgan (see the table above) has her own individual login,
separate from the Pat Morgan family login — the one member in this
dataset with both kinds of login demonstrated at once (Pat's family-wide
login still also reaches Riley's stuff, since a family-wide login sees
every child's account; Riley's own login sees only her own).

To see how a leader creates one of these themselves: log in as Sam
(Troop Scoutmaster), open `/admin/roster`, click into any member (e.g.
Sydney Park, who doesn't have an individual login yet), and look at the
"Individual Login" section near the bottom of the page — same place the
family password reset lives. There's also a direct "Reset Password" link
next to "Edit" on the roster list itself now, so resetting a family's
password no longer requires first opening their detail page to find it.

### Calendar & approvals

Troop 47:
- *Fall Campout 2025* — a past, published event with RSVPs from four
  different members (a mix of yes/maybe), for testing how past events
  and RSVP tallies display.
- *Troop 47 Open House* — upcoming, `visibility: public`, so it's also
  visible to a signed-out visitor on the public calendar.
- *Weekly Troop Meeting* — upcoming, `visibility: members`, only visible
  once logged in.
- *Eagle Patrol Weekend Hike (proposed)* — submitted by Presley (Patrol
  Leader), **left pending approval** so Sam or Ashley can test
  approving/rejecting it from the approvals view.
- *Summer Camp 2026* — upcoming, published, and the event Troop 47's
  trip fund (below) is tied to.

Pack 47:
- *Pinewood Derby 2025* — past, published, with RSVPs.
- *Pack 47 Popcorn Kickoff* — upcoming, `visibility: public`.
- *Bear Den 3 Meeting* — upcoming, `visibility: members`, published
  directly by Dana (Den Leader roles publish directly — there's no
  youth-submission workflow on the Pack side).

### Homepage content

Both units have their `home-hero` and `home-meeting` sections filled in
with real copy (via `/admin/home`), so you can see a customized section
next to the rest, which are left as Phase 1's stock placeholders —
useful for confirming both states render correctly.

### Ledger (Phase 2)

Troop 47:
- General fund: +$2,500.00 dues deposit, −$180.00 campsite expense,
  −$500.00 manual contribution to the Summer Camp 2026 trip fund.
- *Summer Camp 2026* trip fund: holds the $500 contribution above.
- *Fall Popcorn Sale 2026* — percentage-mode fundraiser (10%), **left
  flagged "needs council confirmation"** so you can see that banner. Sold
  $60 attributed to Riley Morgan → $6.00 credited to Riley's individual
  account.
- *Mulch Sale 2026* — fixed-per-item fundraiser ($2.00/item), **already
  confirmed** (via "Confirm Council-Approved Rule"), so you can compare
  against the still-pending popcorn sale. 15 items ($90 gross) attributed
  to Riley → $30.00 credited.
- Riley's individual account balance: $36.00 ($6 + $30 from the two
  fundraisers above).
- A **pending trip-fund transfer request**: Riley's family requested
  pushing $20.00 from Riley's account into the Summer Camp 2026 trip
  fund — left undecided so Terry (Troop Treasurer) can test
  approving/rejecting a transfer from `/treasury`.

Pack 47:
- General fund: +$800.00 dues deposit, −$65.00 Pinewood Derby track
  rental expense.
- *Popcorn Fundraiser 2026* — percentage-mode (8%), also left flagged
  "needs council confirmation." $40 attributed to Morgan Nakamura → $3.20
  credited.

Between the two fundraiser modes (percentage vs. fixed-per-item), the
confirmed vs. unconfirmed states, a funded trip fund, and one pending
transfer awaiting a decision, every Treasury screen and both
`ledger.RecordFundraiserAllocation` code paths get exercised at least
once.

### Site settings

Not pre-seeded — `system_settings` starts empty, so every toggle reads as
its documented default (`require_two_factor_for_all` defaults to off,
same as a real fresh deploy would). To try it: log in as Alex
(`alex.superadmin@example.com`), click "Site Settings" in the nav, and
flip `require_two_factor_for_all` on. Then log in as a persona who hasn't
enrolled in two-factor (Taylor, the Pack Treasurer, or any of the
non-Treasury logins) and confirm the reminder banner now appears for
them too — then flip it back off and confirm it goes away for
non-Treasury logins (Taylor still sees it either way, since Pack
Treasurer is always required regardless of this setting). The toggle
itself, and who flipped it, shows up on both units' `/audit` Activity Log
pages either way.

Also not pre-seeded: the SMTP host/port/username/from fields further down
the same `/admin/settings` page. Those start blank (falling back to
whatever `SMTP_HOST`/etc. environment variables are set, if any — see
DEPLOY.md) and are meant for setting up outgoing email without touching
the CLI. The SMTP password itself is never entered here — it stays in the
`SMTP_PASSWORD` environment variable regardless of what's filled in on
this page.

### News & photo galleries

Each unit gets two news posts (one published, one still a draft) and one
published photo gallery, so `/news` and `/gallery` aren't empty on a
fresh seed, and `/admin/news` has an example of both states to compare:

- Troop 47: *Summer Camp 2026 Sign-Ups Are Open* (published, public) and
  *Eagle Project Fundraiser — Details Coming Soon* (draft, members-only —
  won't show on `/news` until a leader publishes it from `/admin/news`).
  A published *Fall Campout 2025* gallery with two stock photos.
- Pack 47: *Popcorn Kickoff This Weekend* (published, public) and *Den
  Leader Volunteers Needed* (draft, members-only). A published *Pinewood
  Derby 2025* gallery with one stock photo, set members-only rather than
  public — so between the two units you can see both visibility levels
  in the seeded data, not just both draft states.

Log in as Sam (Troop Scoutmaster) or Casey (Pack Cubmaster) — both have
`CanEditUnitContent` — to publish the draft post, edit an existing one,
or add a new gallery photo from `/admin/news` / `/admin/gallery`.

### Activity Log filtering & export

No separate seeding needed — every other action above (role grants,
homepage edits, fundraiser confirmations, the site setting toggle, these
news posts/galleries) already writes to the audit log, so `/audit` has a
realistic mix of "functions" and "people" to filter across as soon as
`-seed-demo` finishes. To try it: log in as Sam or Terry (Troop) and open
`/audit` — use the Function dropdown to isolate just Roles or just Site
settings, use the Person dropdown to see only what one persona did, and
combine both with a date range. "Export CSV" carries whatever filter is
currently applied, so filtering first and exporting second gives you
exactly the rows you were looking at, not the full log.

## Starting over

If a `-seed-demo` run gets interrupted partway through (a crash, a
`Ctrl-C`), the simplest fix is removing what it managed to create and
running it again — it doesn't have a partial-repair mode. From `psql`
(or `docker compose exec db psql -U <user> <database>`):

```sql
DELETE FROM families WHERE name IN (
  'Rivera Family (admin)', 'Kowalski Family', 'Bennett Family', 'Park Family',
  'Diaz Family', 'Osei Family', 'Morgan Family', 'Whitfield Family',
  'Alvarez Family', 'Brooks Family', 'Nakamura Family'
);
```

Deleting a `families` row cascades to its `users`/`members` rows (and
from there to role assignments, RSVPs, and ledger accounts tied to that
family) via the existing `ON DELETE CASCADE` foreign keys — see
`internal/db/migrations/0001_init.sql`. It does **not** clean up
unit-level rows the demo data touched that aren't owned by a specific
family (the demo calendar events, the two units' ledger general/external
accounts, the fundraisers, the sub-groups, the seeded news posts/
galleries) — those are harmless to leave in place, but if you want a
fully clean slate, the more thorough option is dropping and recreating
the whole database, then re-running `-migrate -seed`.
