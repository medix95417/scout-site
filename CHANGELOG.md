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

**Fixed: submitting the "forgot password" form could show a generic
500 error instead of the confirmation page.** Unrelated to which mail
transport is configured — a pre-existing bug where the success page's
data didn't declare a field the template needs unconditionally, which
made template rendering fail every time that specific page actually
got shown (evidently never previously exercised in production). The
three places this page renders now share one struct type instead of
each building its own ad hoc one, closing off this whole class of bug;
added a test that renders the actual page data through the actual
template to catch a recurrence.

**Added an alternative outbound-email transport for hosts that block
SMTP.** Some ISPs/hosting providers block outbound SMTP entirely, even
on 465/587, which previously meant no way to send password-reset or
event-reminder email at all. Setting `MAIL_PROVIDER=fastmail-jmap` (with
`FASTMAIL_API_TOKEN`) now sends mail over Fastmail's JMAP HTTPS API
instead of SMTP, so no SMTP port needs to be reachable — see
DEPLOY.md/.env.example. `SMTP_FROM` still applies (must be one of that
Fastmail account's own addresses/aliases); the SMTP-specific settings
are ignored and unaffected when this is set.

**The Files page now groups by event in a paginated accordion, same as
the photo pickers.** It used to render every file in the library flat
and all at once (or, with an event filter checked, grouped but still
unpaginated) — slow to open once a unit had built up a large library.
Now it's always grouped by event (files with no event link land in
their own "Not linked to an event" group), each group collapsed by
default and showing at most 25 files at a time with a "Show 25 more" to
reveal the next batch, mirroring the accordion picker's own lazy-loading
behavior. Checking specific events in the existing filter still narrows
which groups show.

**Thumbnails are now generated at upload time, not on first view.**
Previously a photo's thumbnail was generated the first time anyone
requested it — usually fine, but it meant the very first page load to
show a freshly-uploaded batch of photos (or the Files page immediately
after enabling this feature) could trigger a burst of real-time image
resizing all at once. Uploading an image now generates and caches its
thumbnail immediately, while the leader is already waiting on the
upload to finish, so viewing it later never has to. Photos already
uploaded before this existed get theirs generated automatically too —
the server runs a backfill pass in the background on every normal
startup (safe to leave running forever; an already-cached thumbnail is
left alone), so deploying this update is the only step needed, no
manual command to remember. A `-backfill-thumbnails` flag runs the same
pass on demand if you'd rather see the result immediately instead of
checking logs — see DEPLOY.md's "Ongoing operations." The on-demand
per-photo fallback in FileThumbnail still exists as a safety net for
anything a backfill pass hasn't gotten to yet.

**Leader photos can now have their crop position adjusted.** The Our
Leaders page fills a fixed-size card with each photo (`object-cover`),
cropping whatever doesn't fit — a portrait headshot in a wide card could
lose the top of someone's head or their chin to the default center crop
with no way to fix it. The leader edit form now has a Top/Center/Bottom
"Photo position" control with a live preview (updates instantly as you
change the setting or pick a different photo), and the public page and
admin list both honor the chosen position. Existing leader profiles keep
today's centered crop unchanged.

**Fixed: generated photo thumbnails could show sideways.** Go's image
decoder ignores EXIF orientation — the tag a phone camera uses to record
"held rotated, display upright" separately from the stored pixel grid —
so a thumbnail built from a photo taken sideways came out sideways too,
even though every browser had always shown the original file itself
correctly. Thumbnail generation now reads that tag and rotates/flips
the image to match before resizing. Any thumbnail already cached with
the bug is automatically bypassed (not served) the next time it's
requested, so no manual fix-up is needed for photos already uploaded.

**Photos now load a resized preview instead of the full original —
much faster over a slow connection.** Every thumbnail-sized photo on the
site (a gallery carousel, the homepage's Recent Activities, the file
library, event photo attachments, den/patrol pages, the leaders page,
and every admin photo picker) was serving the visitor's full original
camera photo — often several megabytes — just to shrink it down with
CSS. That's what made the homepage's auto-advancing carousel visibly
outrun a slow connection: it moves to the next photo on a fixed timer
regardless of whether the current one finished downloading, and a
multi-megabyte photo often couldn't. A new `/files/{id}/thumb` endpoint
generates a resized JPEG (longest side capped at 640px) the first time
a photo's thumbnail is requested and caches it in storage for every
later request, so existing photos pick one up automatically with no
migration step. Clicking through to the full-size lightbox view still
serves the original, unresized photo.

**Fixed: the full-size photo viewer (lightbox) could clip the top or
bottom of a photo on mobile.** It capped the image's height at `85vh`,
which browsers compute against the viewport with the address bar
hidden — taller than what's actually on screen while the address bar is
showing, so part of the photo could render behind it. Now it caps
against `85svh` (the guaranteed-visible "small viewport height") where
supported, with the old `85vh` kept only as a fallback for browsers that
don't understand `svh`, and the viewer can scroll as a safety net for
any photo that still doesn't fit. Applies everywhere the lightbox is
used — Photos, an individual gallery, and the homepage's Recent
Activities.

**Homepage: Recent Activities picks a random rotation once there are more
galleries than fit.** Previously it always showed the newest 4 (2 on
mobile); a unit with a deep gallery history would feature the same
handful forever. Now, whenever there are more eligible galleries than
the homepage has room for, which ones show is chosen at random on every
page load — for every visitor, logged in or not — so older galleries get
a turn too instead of scrolling off into obscurity. Within whichever set
gets picked, they still display newest-first.

**Homepage: Upcoming Events now caps at the next 5** instead of showing
every upcoming public event — the homepage is a preview, not the full
calendar; "View full calendar" already links to `/calendar` for anyone
who wants to see further out.

**Homepage: Recent Activities now shows up to 4 galleries side by side**
(2 columns × 2 rows on desktop, 1 column × 2 rows — so 2 total — on
mobile) instead of one stacked column, with the homepage's own content
column widened to give the wider grid room; Upcoming Events keeps
sitting beside it, just also a bit wider. Verified with a
headless-browser check at both a mobile and a desktop viewport width
that exactly 2 vs. 4 galleries are visible.

**Hero banners (homepage hero, per-page hero banners, and den/patrol
hero) now have a Short/Medium/Tall size selector** next to each image
field, so a leader can pick a banner height that suits the actual photo
instead of every hero being forced into the same fixed crop. Medium
matches the original size, so nothing changes until a leader picks
something else.

**Photo pickers now page a busy event's photos 25 at a time.** The
per-event accordion picker (below) already stopped loading every
event's photos at once — this closes the remaining gap where a single
event with hundreds of its own photos would still render (and try to
load) all of them the moment a leader opened it. Now only the first 25
render initially; a "Show more" expander reveals the next 25, and so on
until every photo's been shown. Verified with a headless-browser test
that opening a 60-photo event only ever triggers the 25 downloads
actually visible at each step. Applies everywhere the accordion picker
does — homepage sections, Gallery albums, leader photos, and the
den/patrol page's Hero/Photos pickers.

**Every "choose a photo from your library" picker is now a per-event
accordion instead of one giant eagerly-loaded thumbnail grid** — a
leader on a unit with hundreds of campout photos was hitting a very
slow page load (the edit den/patrol page especially) because every
single thumbnail loaded at once, whether needed or not. Photos now
group by the calendar event they're linked to (plus a "Not linked to an
event" bucket), each collapsed by default; a collapsed group's images
never even hit the network — closing a `<details>` removes its content
from the render tree entirely, and combined with native
`loading="lazy"` on every thumbnail, a browser never requests them
until a leader actually opens that event's group. This applies
everywhere a picker like this shows up: the homepage's hero/program/
gallery-strip editor, Gallery albums, leader photos, and the den/patrol
page's Hero and Photos pickers. The den/patrol page's Hero banner also
changed from a URL-only text field to the same photo picker every other
hero banner already has — paste a link still works, but clicking a
library photo no longer requires copying a download link by hand
first. `/files` and the den/patrol Photos grid also gained
`loading="lazy"` on their own thumbnails for the same reason. (If a
single event ends up with a very large photo count of its own, the
next lever — capping how many thumbnails render per group with a
"show more" expander — is a reasonable follow-up, not built here.)

**Clicking a photo now opens a full-size lightbox with Back/Forward
navigation**, replacing the previous "opens in a new tab, no way to see
the next one" behavior. The photo shows uncropped (no forced aspect
ratio, so nothing is cut off) with its own prev/next arrows, arrow-key
support, and Escape/click-outside to close — available everywhere a
photo carousel renders: Gallery albums, the Photos list, and the
homepage's recent-activity strip. Videos are unaffected; they already
play in place with their own controls.

**Clicking a photo now opens it at full size in a new tab**, wherever
photos render in a carousel (Gallery albums, the Photos list, the
homepage's recent-activity strip) — previously the inline framing (a
cropped square/strip) was all there was to see. Videos are unaffected;
they already play in place. Also added an "Add Photos" button to the
Manage Photos admin page, linking straight to the file library, so a
leader doesn't have to know to visit `/files` first before building an
album.

**Homepage: Recent Activities and Upcoming Events moved right under the
hero banner** (previously several sections down, after the quick-link
cards, "Our Program," and meeting/leadership info) since it's the
page's freshest, most-changing content. Recent-activity photos now
render as a large square tile instead of a short wide strip, so a
portrait phone photo shows top-to-bottom instead of losing most of its
height to cropping.

**Fixed: a private photo/video mixed into a public gallery album showed up
as a blank/broken tile for a signed-out visitor instead of simply not
showing at all.** The access check itself was already correct — a private
file was never actually served to a logged-out request — but the gallery
page still rendered an `<img>`/`<video>` tile for it, which then failed to
load. Public gallery pages (the `/gallery` list, a gallery's detail page,
and the homepage's recent-activity carousel) now drop any private photo or
video from the page entirely before rendering, for a visitor who isn't
logged in; a logged-in visitor still sees everything, matching the file
library's existing access rule.

**Videos are now first-class alongside photos.** A video already uploaded
fine, but couldn't be marked "Public," never showed up in any photo
picker, and had no player anywhere it was used — this closes all three
gaps. On `/files`, a video gets its own thumbnail preview, a "Video"
badge, and the same "Make public"/"Make private" toggle photos have. The
Gallery editor's "choose from your library" strip and its "add all of an
event's photos" section now include videos too (with a small play-icon
badge on their thumbnails), and picking one adds it to the album the same
way a photo does. Wherever a gallery's photos render — the gallery list
and detail pages, and the homepage's recent-activity carousel — a video
entry now plays with a real `<video>` control instead of showing as a
broken image. The single-photo pickers (homepage hero, page banners,
leader photos, den/patrol pages) are unchanged and stay photo-only, since
a full-bleed background video raises its own autoplay/bandwidth questions
outside this scope.

**Raised file upload limits** — a single file can now be up to 50 MB
(was 20 MB), and one upload submission (a whole batch of files at once)
can now total up to 500 MB (was 250 MB), enough headroom for phone
photos and short videos from a full campout in one go.

**Files can now be filtered/grouped by event, and Gallery albums can pull
straight from an event's own photos.** On `/files`, checking one or more
events switches the page from its ordinary flat list to grouping by
event instead, so files from the same campout read together. On the
Gallery editor, a new "add all of an event's photos" section lists every
photo linked to a chosen event — public or private — letting a leader
build a members-only album straight from a private event's photos instead
of copying each download link by hand. Mixing public and private photos
from the same event is fine: a private one simply won't display to a
signed-out visitor even inside a "Public" album, since the existing
file-download access check still applies wherever its URL ends up
embedded — the picker just labels which is which.

**Calendar events can now be edited and deleted, and can repeat.** Any
leader who can publish an event directly can now also edit or delete it
afterward from the calendar page — editing never re-triggers the
SPL/Patrol-Leader approval workflow, and deleting cleans up its RSVPs,
permission slip, and any pending approval request with it (a linked trip
fund or fundraiser just loses the link, keeping its own history). The
"Add an event" form also gained a "Repeats" option — weekly, every two
weeks, or monthly, for up to 52 occurrences — which creates each
occurrence as its own fully independent event (own RSVPs, own approval
routing, individually editable/deletable), tagged only for display as
"part of a repeating series (N events)."

**Uploading several files at once with a given name now numbers them**
("Campout 2026 1", "Campout 2026 2", ...) instead of applying that name
to only one file and leaving the rest with their bare filenames.

**A file's category (General document vs. Event photo) can now be
changed after upload**, alongside the existing rename/make-public-or-
private/link-to-events/delete actions on `/files`.

**Added a "File Storage" section to Site Settings**, showing a unit's
stored files broken down by category — file count and space used per
category, plus a grand total — so a site admin can see what's using
storage without digging through the full file list. Read-only; manage
the files themselves from `/files`.

**Relabeled the existing "My Accounts" self-service toggle** on Settings
from "Family access to Scout account balances" to "My Accounts (family
access to Scout account balances)" — same setting, same behavior, just
named after the actual page it controls so it's easier to find.

**Added a "Treasury (fund accounting)" toggle to Settings**, letting a
super_admin turn the whole Treasury function off per unit — `/treasury`
and all its sub-pages, the family self-service "My Accounts" view, and
(since it exists to feed the same ledger) the fundraiser storefront's
admin pages, public order page, and homepage button all close, with a
clear message for anyone who still has a URL bookmarked, Treasurer
included — this isn't a per-role exception like the existing Scout-account
self-service toggle, turning the function off means off for everyone.
Defaults to on, so no existing unit sees any change; no ledger data is
touched by turning it off, every entry point just comes back exactly as
it was once it's turned back on.

**Added a fundraiser storefront: an item catalog, a homepage "Buy Now"
button, and an order queue — the first step toward online payments.**
A Treasurer can now add sellable items and a button image to any existing
Fundraiser from its Treasury page, then a super_admin picks which single
fundraiser (if any) is the active storefront from Settings — enabling one
automatically disables any other, so only one campaign is ever live at a
time. When on, the homepage shows a large "Buy Now" button just below the
hero banner, linking to a new public order page (`/fundraiser`) reachable
by anonymous visitors and logged-in leaders/parents alike; when off, the
button disappears entirely. An order asks for the buyer's info, which
items and quantities, and — in place of a payment method, since no
processor is wired up yet — the name of the Scout who should get credit.
That name is matched against the unit's youth roster automatically on an
exact, unambiguous match; an unmatched or ambiguous name is still recorded
and left for a leader to resolve by hand from the fundraiser's order
queue. Crucially, the existing Fundraiser ledger-credit mechanism
(`RecordFundraiserAllocation`) never fires the instant an order is
placed — only once a leader explicitly marks the order "paid" and its
Scout match is resolved, so the unit's books are never credited ahead of
money actually being received. Online payment via Stripe/PayPal is still
the next step; this phase is the order-taking half.

**Five fixes/additions to existing admin and account features.**
- **Fixed a missing "Manage Leaders" link** — the Our Leaders admin page
  (`/admin/leaders`) has existed since it shipped, but the hamburger
  menu's Admin section never linked to it, so there was no way to reach
  it short of typing the URL directly.
- **Calendar's "Add an event" is now a collapsible accordion**, matching
  the "Print Events" section right below it, instead of always being
  expanded and taking up space above the calendar itself.
- **Roster now shows each member's contact info and address** — email,
  home/cell phone, and family address — but only whatever that family
  has actually chosen to share (the same release toggles from My Family
  already used elsewhere); nothing nobody opted to release ever shows.
  Added to the printable roster PDF too.
- **Dens and Patrols can now have their own hero banner photo**, separate
  from the unit-wide per-page banners, editable by that den's/patrol's
  own leader (or any unit-wide leader) right alongside its description.
- **Added a "Change Password" section to the Security page** — previously
  the only way to change your password was the forced flow after a
  leader-issued temporary one; now any logged-in login can change its own
  password after confirming the current one. Same as every other password
  change in this app, it signs the login out everywhere (including the
  current session) as a security measure, so it's immediately followed by
  a fresh login (through two-factor first, if enabled).

**Footer now matches the header's color, instead of a fixed navy.**
Troop's footer previously stayed a fixed dark blue no matter what, which
clashed with its green header — it now matches the header's color for
every unit, so the two bars read as one consistent band top and bottom.

**Security/robustness audit pass — two defects found and fixed.** See
`SECURITY_AUDIT.md`'s "Audit pass 3" for the full write-up, including
everything that was checked and came back clean.
- **Fixed eight kinds of activity-log entry being recorded but never
  shown.** Advancement records, custom roles, leader profiles,
  newsletters, permission slips and their signatures, saved treasury
  reports, and per-unit setting changes were all written to the audit log
  correctly, but no read path could ever display them — so the activity
  log quietly gave an incomplete answer for those categories (48 real
  entries in the development database). Most consequential: creating a
  custom role can grant treasury access, and per-unit settings now
  include the newsletter and password-reset switches, so "who granted
  that" and "who turned that off" were both unanswerable. Nothing was
  exposed that shouldn't have been — the log showed less than it should,
  never more.
- **Fixed a single empty event description being able to take down the
  calendar for everyone.** An event whose description or location was
  genuinely empty at the database level (rather than blank text) made
  `/calendar` fail to load entirely and blanked the homepage's upcoming
  events list — for every visitor, not just on that one event. Events
  created through the site were never affected; this could only come from
  a hand-written database change, a restored backup, or a future import.
- Both fixes ship with a test that fails the build if the same mistake is
  reintroduced.

**Two new toggles: turn off a unit's newsletters, or turn off self-service
email password reset site-wide.**
- New per-unit "Newsletters" toggle in `/admin/settings` (default on):
  turns off `/admin/newsletters` for that unit, including sending new
  ones — already-sent newsletters and their history aren't deleted, just
  the admin UI for managing new ones is hidden. Same shape as the
  existing advancement-tracking toggle.
- New site-wide "Allow self-service email password reset" toggle in
  `/admin/settings` (default on): turns off the "Forgot your password?"
  email flow across both units — the page explains resets are off and
  points to a leader instead, who can still reset anyone's password
  directly from `/admin/roster` either way. A reset link already emailed
  before this is turned off keeps working; this only stops new ones from
  going out.

**Accordion-style collapsible sections on Roster and Advancement.**
- `/admin/roster`'s three "add" forms (Add an Existing Person, Add a New
  Family, Add a Member to an Existing Family) and its Dens & Patrols
  section now collapse into the same disclosure pattern used elsewhere —
  closed by default, so the actual roster table isn't pushed down the
  page by forms you use far less often than you look up a member.
- `/admin/advancement`'s Record One and Bulk Import cards get the same
  treatment. Bulk Import stays open automatically right after you submit
  it, so you see the "X recorded, Y skipped" result without an extra
  click.
- The Roster and Advancement records tables themselves are left as
  tables, not converted to accordion rows — collapsing every person's or
  record's row would hide exactly the side-by-side, scan-a-column view
  that makes a roster/records table useful in the first place.

**Accordion-style collapsible rows on the busiest admin list pages.**
- News/Photos, Our Leaders, and Resources' management view now show each
  item as a collapsible row: title, status badges, and (for Resources)
  description stay visible at a glance, while the action buttons
  (Edit/Publish/Unpublish/Delete/make-public) are tucked behind a click to
  expand — cleaning up what used to be a row of several buttons sitting
  next to every single item. Built on the same no-JS `<details>/<summary>`
  disclosure the hamburger menu and its Patrols/Dens submenu already use,
  so it needs no new script and works identically with JS disabled.

**Added a public "Our Leaders" page.**
- New `/leaders` public page lists a unit's adult leaders — name, role
  title, a brief bio, and an optional photo (choosable from the unit's
  file library, same picker used for hero images).
- New `/admin/leaders` management page for adding/editing/removing
  leaders, gated the same way `/admin/home`, `/admin/news`, and
  `/admin/gallery` already are. New leader profiles start as a draft, same
  draft/published lifecycle news posts and photo albums already have, so
  a half-written profile never shows on the public page.
- "Our Leaders" joins the top-nav quick-link row and hamburger menu
  alongside Resources/Photos/Calendar.

**Photos page redesign: auto-rotating album previews, and a thumbnail
strip on each album's detail page.**
- The `/gallery` ("Photos") listing now shows each album's photos as an
  auto-rotating carousel (advancing every few seconds) instead of a single
  static cover image — the same auto-rotate mechanism the homepage's
  recent-activities preview uses.
- An album's detail page now shows a row of clickable thumbnails below the
  main photo, one per photo — click one to jump straight to that photo,
  with the active thumbnail staying in sync as you browse with the
  existing prev/next arrows or by swiping.

**Homepage now previews recent Photo Album activities alongside upcoming
events, side by side.**
- Added a two-column section: upcoming public events (from the calendar) on
  the right, and a preview of recent, published, public Photo Album
  activities on the left — each activity showing its own auto-rotating
  photo carousel (advancing every few seconds) when it has more than one
  photo, so a visitor gets a glance at what the unit's been up to without
  clicking into `/gallery`.
- This replaces the old, leader-curated "Gallery photos" homepage field
  (a fixed, manually-maintained photo strip) — the homepage now pulls
  straight from whatever Photo Album posts are actually published, so it
  stays current automatically as new albums go up.

**Fixed multi-day events disappearing from "Upcoming Events" before they
actually ended.**
- The homepage and `/calendar`'s "Upcoming Events" list only checked an
  event's *start* date, so a multi-day event (a weekend campout, say
  Friday through Sunday) vanished from the list a day after it started —
  Saturday, Sunday, and the rest of the campout no longer showed as
  upcoming even though it was still happening. Now checks the event's
  actual end date (already settable as an optional field when creating an
  event) when it has one, falling back to the start date for a normal
  single-day event exactly as before.

**Nav/footer cleanup on the public site.**
- Added a top-of-page quick-link row (Resources / Photos / Calendar) visible
  on tablet/desktop, alongside the existing hamburger menu — no need to open
  the hamburger just to reach the site's main public pages. (Home page's own
  "Our Leaders" link joins this once that page ships.)
- **Renamed "Gallery" to "Photos"** everywhere it's user-facing (nav, page
  heading, admin "Manage Photos" / "New Photo Album") — the underlying
  `/gallery` route and database `page_type` are unchanged, so this is a
  display-only rename, not a URL migration.
- **Removed the duplicate social media links section from the homepage** —
  Facebook/Instagram/TikTok now show exactly once, in the site-wide footer,
  instead of also repeating at the bottom of the homepage.
- **Social links now show as recognizable icon glyphs** (a simple, generic
  monochrome icon per platform — the common "follow us" convention most
  sites use) instead of plain text buttons reading "Facebook"/"Instagram"/
  "TikTok".

**Newsletter sending now happens in the background, so a slow mail
server (or a closed browser tab) can no longer lose track of what
actually went out.**
- Clicking "Send Now" used to block the whole request until every single
  recipient's email finished sending — for a real unreachable/slow SMTP
  server, that's minutes, all inside one HTTP request. If the browser
  disconnected or a reverse proxy gave up first, the newsletter's final
  "sent" status, recipient count, and audit log entry never got written —
  it stayed stuck as a draft with no record anything was attempted, and a
  retry would resend to everyone (including anyone who *did* get it the
  first time).
- Sending is now: an immediate, atomic "start sending" step (checks mail is
  configured and there are recipients, then marks the newsletter
  "sending" so a second click can't start an overlapping send) followed by
  the actual per-recipient sending running in the background — detached
  from the request, so a browser disconnect can't cut it short or lose
  the final bookkeeping. The Newsletters list and the newsletter's own
  page show a "Sending" badge while this is in progress, and the page
  auto-refreshes every few seconds until it's done.
- A newsletter that somehow got stuck at "sending" (e.g. the server
  restarted mid-send) becomes sendable again after 40 minutes, rather
  than being stuck there forever with no way to retry.

**Fixed emails (password reset, newsletters) sometimes hanging until the
browser gives up with "site can't be reached."**
- A slow or unresponsive SMTP server used to be able to block a send
  indefinitely — only the very first step (the TCP connect) had a timeout;
  everything after that (the SMTP handshake, login, and actually
  transmitting the message) had none, since the standard library's SMTP
  client has no concept of a context deadline on its own. Fixed with a
  30-second deadline set directly on the connection, covering the whole
  conversation.
- That alone wasn't quite enough: the server's own HTTP timeout was
  shorter (10s) than even the original connection timeout (15s), so the
  connection could get forcibly dropped by the server itself before a
  slow send ever got the chance to fail gracefully and show its own error
  message — which is what actually produced the "site can't be reached"
  browser error, rather than a normal "couldn't send that email" page.
  Raised to 60 seconds, comfortably above the mailer's own worst case.
- Added a regression test (`TestSend_HungServerDoesNotBlockForever`)
  simulating exactly this — a server that accepts the connection and then
  never responds — confirming the send now fails within a bounded time
  instead of hanging.

**Permission slips are now a per-event choice, with a unit-wide option to
only show them where they're actually needed.**
- **"Requires a permission slip" checkbox** when creating a calendar event —
  most events (a weekly meeting) don't need one; a trip or campout does.
  Existing events that already had a permission slip attached were
  backfilled to "required," so nothing already in use changed.
- **New `/admin/settings` toggle: "Only show permission slips on events
  that need one."** Off by default — every event keeps showing the
  "Permission slip" link, exactly as before. Turned on, the link (and the
  page itself, even by direct URL) only shows for events marked as
  requiring one — a weekly meeting won't display it at all. A leader can
  always reach an event's permission slip page either way, since there's
  no way yet to edit an event's "requires a permission slip" flag after
  creation. An event a leader already attached a real slip to also always
  stays reachable to families, regardless of the checkbox, so a slip
  never becomes undiscoverable once it exists.

**Fixed the Family Directory showing admin/technical accounts as if they
were families.**
- A login whose only role in a unit is `super_admin` (e.g. a bootstrap
  admin account, or the demo data's "Rivera Family (admin)" persona) no
  longer appears in `/directory` or its PDF export. `super_admin` is
  deliberately a "site operator" capability, not a community leadership
  role like Scoutmaster or Treasurer — those still show normally, since
  they're real people families would want contact info for. A member
  holding `super_admin` alongside a real role is unaffected either way.

**Accessibility pass: button contrast, keyboard focus, reduced motion, and
form labels.**
- **Fixed a real WCAG contrast failure** — every "primary action" button
  (Save, Log in, Record Deposit, etc.) used white text on the unit's accent
  color. That's fine for Scouting Red, but Cub Scouts' mandated accent,
  Cub Scout Yellow (`#FDC116`), is a light color — white text on it was
  badly unreadable (~1.6:1 contrast, nowhere near the 3:1 minimum). Buttons
  now use a shared `.btn-accent` style whose text color
  (`Unit.AccentTextColor`) is computed from the accent color's actual WCAG
  luminance, so it stays correct automatically if a unit's colors ever
  change, rather than being hardcoded per unit type.
- **A single, consistent keyboard-focus style** site-wide (`:focus-visible`,
  so nothing changes for mouse users) — every link, button, and form field
  now gets the same visible outline when tabbed to, instead of relying on
  inconsistent browser defaults.
- **Respect "reduce motion"** — the handful of hover/hover-open transitions
  site-wide now collapse to instant for anyone whose OS is set to reduce
  motion.
- **Added `aria-label`s to form fields that only had placeholder text** —
  amount/description fields on Treasury, permission-slip signatures, the
  roster search box, and a few others now have a real accessible name, not
  just a placeholder that disappears once you start typing.
- Checked (no changes needed): heading hierarchy per page, and alt text on
  images — both were already in good shape (images sitting next to their
  own visible caption/label correctly use empty alt text, rather than
  needlessly repeating it for screen readers).

**Social media links moved to `/admin/settings`, each with its own on/off
switch.**
- **Facebook/Instagram/TikTok URLs are now set from the new "Social Media"
  section of `/admin/settings`**, alongside a per-platform toggle — a leader
  can enter a link ahead of time and turn it on later, or hide one
  temporarily (an inactive account, a platform the unit is stepping back
  from) without losing the URL underneath. Applies to both the site footer
  and the homepage's social icon row.
- These three fields moved off `/admin/home` (the general homepage-copy
  editor) — that's still where the single generic "Social media link" field
  lives, but Facebook/Instagram/TikTok specifically now live under Settings
  with everything else that's a site-wide on/off switch. A unit that had
  already set one of these through the old editor sees it carried over
  automatically the first time `/admin/settings` loads; saving the new form
  once switches that unit over for good.

**Brand conformance pass: fonts, footer, and a color fix, against the BSA
Digital Brand Guidelines.**
- **Roboto + Roboto Slab site-wide** — the two official BSA typefaces are
  now loaded (Google Fonts) and applied by default (Roboto for body text,
  Roboto Slab for every heading), site-wide, on both the public pages and
  every logged-in page. Purely additive — no markup changed, so nothing
  that depended on the previous OS-default font is affected.
- **Fixed a one-digit color typo** — Troop 47's theme color was `#243E2C`;
  the guideline's actual "Scouts Olive" is `#243E26`. Pack 47's colors were
  already byte-exact.
- **Rebuilt the site footer** to match the guideline's dark-blue footer bar
  (social icons + copyright), reusing the Facebook/Instagram/TikTok links a
  leader already set on `/admin/home` — no new data entry needed, and the
  icons only show up once a leader has actually set a link. Deliberately
  left out the guideline's national-property link row (About, Careers,
  Terms, Privacy Policy, Donor Privacy, Connect, Contact): this unit has no
  jobs page or national donor-privacy policy for "Careers"/"Donor Privacy"
  to point at, and dead links would read as broken, not polished.
- **Added a "Skip to main content" link** and an anchor on `<main>` — a
  standard, low-risk accessibility improvement that fits naturally
  alongside this pass; keyboard and screen-reader users can jump straight
  past the header/nav on every page.
- Bumped the footer's text opacity slightly (70%→80% for the copyright
  line) for a bit more contrast against the now-dark background.

**Forced password change on first use of a temporary password, and password
complexity requirements.**
- **A leader-issued temporary password must be replaced before it can be
  used to do anything else.** Whenever a leader creates a new family, adds
  an individual Scout login, or resets a family's/Scout's password from
  `/admin/roster`, the next login with that temporary password is
  interrupted by a "Set a New Password" step — no real session (and no
  two-factor prompt, for accounts with that enrolled) is issued until a new
  password is set. A self-service password reset via "Forgot your
  password?" also clears this requirement, since choosing a new password
  yourself already accomplishes the same thing.
- **Password complexity is now enforced everywhere a person chooses their
  own password** (the forced-change step above, and the existing
  self-service reset-password flow): at least 8 characters, with a mix of
  at least 3 of lowercase letters, uppercase letters, numbers, and symbols —
  enough to rule out something like "password" or "12345678" without being
  needlessly picky. System-generated temporary passwords are unaffected —
  they're already random with plenty of entropy.

**My Family page cleanup, roster search, and single-page den/patrol sites
with events, news, and photos.**
- **Removed the "Print / Download PDF" link from `/my-family`** — it
  duplicated the Family Directory's own PDF export for little benefit on a
  page that's about editing your own info, not printing it.
- **`/my-family` now shows what's on file before you edit it** — each
  member's card leads with their member type, den/patrol, and roles (all
  read-only; those are managed by a leader), then the existing editable
  contact-info fields below.
- **Roster search.** `/admin/roster`'s "Current Roster" table gained a
  live, client-side search box — type a name to filter the table down as
  you type. Each row's name is now also a link straight to that member's
  detail/edit page (previously only the separate "Edit" action linked
  there).
- **Den/patrol pages are now a fuller single-page hub.** Each sub-group's
  members-only page (`/groups/{id}`) gained:
  - An **Upcoming Events** section — that sub-group's own scoped events
    plus every whole-unit event (a Troop event on a patrol's page, a Pack
    event on a den's page), tagged so it's clear which is which.
  - A **News** section — short updates a Den Leader/Patrol Leader (or any
    unit-wide leader) can post directly from the group's own page, visible
    only there, not mixed into the unit-wide `/news` feed. Built on
    `content_pages.sub_group_id`, a column that's existed since the
    original schema but was never used until now.
  - The existing member list and linked photo gallery stay, just
    reorganized alongside the new sections on the same page.

**Scout Accounts report, fundraiser-to-event linkage, roster and event
attendee PDFs, and clearer council-confirmation wording.**
- **Scout Accounts report** — a 5th report type under `/treasury/reports`
  listing every Scout's individual account balance plus a grand total held
  for Scouts, right now (not a date range). Has its own PDF export like the
  other four report types.
- **Fundraisers can optionally be tied to a specific calendar event** —
  useful since the council-approved rate is frequently set per-fundraiser
  (a car wash vs. a popcorn sale vs. a campership drive), and a unit
  commonly runs a fundraiser as one specific event. Both fundraiser-creation
  forms on `/treasury` gained an optional "tie to event" picker (mirroring
  the existing trip-fund-to-event picker); the linked event, if any, shows
  on the fundraiser's own page and in the Treasury fundraisers list. Not
  every fundraiser maps to one discrete event (an ongoing donation drive,
  e.g.), so linkage is optional, not required.
- **Print a roster.** `/roster` gained a "Download PDF" link — the same
  member list shown on-screen (name, type, den/patrol, roles), available to
  any logged-in family, not just leaders.
- **Print an event's attendee roster.** Each event on `/calendar` gained a
  "Print attendee roster" link (visible to unit-content editors, same as
  "Attach a file") producing a PDF of who RSVP'd yes/maybe, alongside their
  family — the first place attendee identity is shown to anyone but the
  RSVP'ing family itself.
- **Cleaned up the council-confirmation wording** on `/treasury` and a
  fundraiser's own page — shorter, less repetitive copy for the
  "you'll need to confirm the actual rate with your council" messaging.

**Treasury Reports — a dedicated reports page, four report types, and named
saved presets.** A new "Reports" page under Treasury (`/treasury/reports`)
adds:
- **Income & Expense Summary**, with This Period / Prior Month / Year-to-Date
  columns, computed from real deposits and expenses only — an internal
  transfer (a fundraiser allocation, a trip-fund push) never counts as
  income or an expense, only money that actually crossed in or out of the
  unit's books.
- **Account Balances** — every account and its current balance.
- **Transaction Detail (General Ledger)** — every posting in a date range,
  narrowable to specific account(s) and/or transaction type(s).
- **Fundraiser Proceeds Summary** — gross vs. credited proceeds per
  fundraiser, narrowable to specific fundraiser(s).

Every report has a "Download PDF" link and a "Save this report" action —
saved reports are named and shared across the whole unit (not tied to
whoever saved them), so any Treasurer/Admin can re-run one with one click
instead of reselecting the same filters each time. Not built yet, flagged
for later: per-ledger budgets and a budget-vs-actual column on these
reports.

**Print a calendar date range, optionally narrowed to specific dens/patrols.**
`/calendar` gained a "Print Events" section: pick a date range (either end
optional) and, if the unit has any dens/patrols, choose specific ones to
include — leave none checked to print every event. A whole-unit event
always stays in regardless of which den/patrol is checked, matching how
the on-screen month grid already shows it to everyone. Printing respects
the exact same visibility rules as the browser: a logged-out visitor only
ever gets public events, and a logged-in family gets everything they could
already see on-screen — never more.

**Print a Scout ledger statement — one Scout, or a whole family, for any
date range.** A Scout's account page (`/treasury/accounts/{id}`, reachable
by the Scout/family themselves, not just the Treasurer) now has a "Print
Statement" form: pick a date range (or leave it blank for everything) and
download a PDF with a starting balance, every transaction with a running
balance, and an ending balance. "My Accounts" (`/accounts`) gained the same
date-range print, plus — for a family with more than one Scout — checkboxes
to choose which Scout(s) to include; choosing more than one prints each
Scout as their own section (sorted the same order they're already listed
in), never combined into one total.

**Payments settings — Stripe and PayPal, configured entirely from the admin
page.** A new "Payments" section on `/admin/settings` lets each unit turn
Stripe and PayPal on or off independently and enter their credentials
(publishable/secret keys, webhook signing secret; client ID/secret, plus a
live-vs-sandbox toggle) directly from the web page — no environment
variables or CLI access needed. Troop and Pack each connect their own
account, matching how the treasury already keeps fully separate books per
unit: nothing entered for one unit is visible to, or usable by, the other.
Secret fields (the API secret keys) are never redisplayed once saved —
the form always shows them blank with an "already set" placeholder, and
resubmitting the page without retyping one leaves it untouched rather than
wiping it. This is the configuration layer only — actually accepting a
payment (a "Pay Now" button, checkout, and webhook-confirmed ledger
deposit) is a separate, larger follow-up once these credentials are in
place. Also fixed a latent bug this uncovered: loading the site-wide or
per-unit toggle list crashed if any text setting (SMTP fields, or now
these payment credentials) had ever been saved.

**Deactivate/reactivate a member, and admins no longer clutter the roster.**
Roster admins can now take a member off the roster without losing anything —
"Deactivate" on a member's edit page hides them from `/roster` and
`/admin/roster` while keeping their record, contact info, and every role
assignment completely intact; "Reactivate" (from the new "Inactive members"
section at the bottom of `/admin/roster`) instantly restores them exactly as
they were, with nothing to reassign. Deactivating a member with an open Scout
account is blocked until that account's balance is exactly $0.00 — the admin
sees the current balance and which unit it's in, so it can be resolved
(spent down, refunded, or transferred out) first. Separately, a member whose
only role in a unit is `super_admin` (a site-wide configuration grant, not a
real membership) no longer shows up on that unit's roster at all — anyone
who also holds a real membership/leadership role still appears normally.

**On-brand colors and official trademarks for both sites.** Troop 47 and
Pack 47 now match the Scouting America Brand Guidelines instead of an
arbitrary green/blue: Troop 47 uses Scouts BSA's olive + red, Pack 47 uses
Cub Scouts' blue + gold, and each site's header shows the corresponding
official program trademark. Added a second per-unit color
(`accent_color`, alongside the existing `theme_color`) so a unit can pair
a neutral/structural color (header, hero banner, calendar highlights) with
a separate, punchier color for buttons and other calls-to-action, matching
how the guidelines actually use each unit's colors — every button, "Save",
and "Join" link site-wide now renders in the accent color. No template
structure or page layout changed, just the color/branding layer.

**Thumbnails in the file library and patrol/den photo picker.** The main
file library list (`/files`) and the patrol/den page's photo checkbox list
now show a small thumbnail preview next to each image file's name, instead
of a filename with no visual — makes it much faster to spot the right
photo by eye rather than reading names one at a time.

**Fix — multi-file uploads failing.** Uploading more than a couple of
photos at once (e.g. a whole camera roll from a campout) could fail
outright: every POST request, uploads included, was capped at 25 MB
*total*, not per file — easy to exceed with just two or three modern phone
photos. Raised to 250 MB total per submission (each file individually
still capped at 20 MB), and reworded the error so it's clear it's the
whole batch's size, not one bad file. Also: if one file in a large batch
is still too big, the rest of the batch now uploads successfully instead
of the whole submission aborting — `/files` shows which file(s) were
skipped and why.

**Homepage gallery strip: pick multiple photos, with a real carousel.**
The homepage's two fixed "Gallery photo 1"/"Gallery photo 2" slots are now
one combined "Gallery photos" field — click as many thumbnails as you
want from your file library (or paste external URLs), each with an
optional caption. One photo shows plain; more than one becomes a
swipeable/scrollable carousel, same as a Gallery entry. A unit that had
already configured the old two-photo layout keeps seeing those exact
photos on the live homepage until a leader saves the new field — nothing
is silently lost during the upgrade.

**Gallery: pick photos from your library, with a real carousel.** Adding
photos to a Gallery entry no longer means hand-typing image URLs — click a
thumbnail from your file library (sorted by linked event, same picker as
homepage sections/hero banners) and it's added to the list; pasting an
external URL still works exactly as before. A gallery with more than one
photo now renders as an actual swipeable/scrollable carousel with prev/next
buttons, rather than every photo shown at once in a static grid.

**Friendly names for uploaded files.** A file in the file library can now
be given a friendlier display name (e.g. "Summer Camp Group Photo" instead
of `IMG_6440.jpeg`) — set at upload time, or any time after via a new
"Rename" action on `/files`. Shown everywhere a file is listed or picked
(the file library, every "choose from your library" image picker, the
resources page), falling back to the original filename until a leader sets
one. The original filename is unchanged and still what a downloaded copy
is saved as.

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
