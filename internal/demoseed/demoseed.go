// Package demoseed creates a full set of test data — one login per
// system-defined role, plus realistic calendar, ledger, fundraiser, and approval
// activity — so a fresh deployment can be clicked through and tested
// without hand-creating a dozen accounts first. See cmd/server's
// -seed-demo flag and DEMO_DATA.md.
//
// This is meant for a fresh or staging database, not a live production
// site with real families already in it — every login here is obviously
// fake (example.com addresses, one shared password) but it's still real
// rows in the same tables real data lives in.
//
// Deliberately built on the same package functions the web handlers use
// (internal/roster, internal/calendar, internal/ledger, internal/content,
// internal/auth, internal/twofactor) rather than hand-written SQL,
// wherever those functions exist — that way seeding a demo dataset
// exercises the real business logic (double-entry balance checks, the
// approval workflow, audit logging, TOTP enrollment) instead of just
// inserting rows that happen to look right. The one exception is
// creating a family/login/first-member, which goes through raw SQL for
// the same reason internal/bootstrap.CreateAdmin does: there's no
// pre-existing login to hand a caller-supplied password to any other
// way, and (for the very first persona) no actor yet to attribute an
// audit log entry to.
package demoseed

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/ledger"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/twofactor"
	"github.com/47-yonkers/scout-site/internal/units"
)

// DemoPassword is shared by every seeded login, for convenience — this is
// test data, not a real credential, so there's no value in generating a
// dozen different random passwords nobody will remember.
const DemoPassword = "ScoutDemo2026!"

// markerEmail is checked at the start of Run to decide whether demo data
// already exists in this database.
const markerEmail = "alex.superadmin@example.com"

// Persona is one seeded login, reported back so the caller (cmd/server)
// can print exactly what was created and how to sign in as it.
type Persona struct {
	Label       string // e.g. "Super Admin"
	Unit        string // "Troop 47", "Pack 47", or "Troop 47 & Pack 47"
	Email       string
	Password    string
	TOTPSecret  string   // "" unless this login has confirmed TOTP enrollment
	BackupCodes []string // set only alongside TOTPSecret
	Note        string   // anything worth calling out
}

// Summary is what Run reports back.
type Summary struct {
	AlreadySeeded bool // true if Run found existing demo data and made no changes
	Personas      []Persona
}

// person is an internal record of one created member — enough to wire up
// roles, RSVPs, and ledger accounts against later in Run without
// re-querying.
type person struct {
	FamilyID string
	MemberID string
	UserID   string // "" for a member added to an existing family (only the family's first member has a login)
}

// Run seeds the full demo dataset. Idempotent at an all-or-nothing level:
// if the super_admin demo login already exists, Run assumes the rest of
// the dataset does too and returns immediately without creating anything
// — it does not attempt to repair a partially-completed prior run. If a
// prior run was interrupted partway through, the simplest fix is
// dropping the demo families it created (see DEMO_DATA.md, "Starting
// over") and running -seed-demo again.
func Run(ctx context.Context, pool *pgxpool.Pool) (Summary, error) {
	var existing string
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, markerEmail).Scan(&existing); err == nil {
		return Summary{AlreadySeeded: true}, nil
	}

	troop, found, err := units.BySlug(ctx, pool, "troop-47")
	if err != nil || !found {
		return Summary{}, fmt.Errorf("demoseed: unit \"troop-47\" not found — run -seed before -seed-demo")
	}
	pack, found, err := units.BySlug(ctx, pool, "pack-47")
	if err != nil || !found {
		return Summary{}, fmt.Errorf("demoseed: unit \"pack-47\" not found — run -seed before -seed-demo")
	}

	passwordHash, err := auth.HashPassword(DemoPassword)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: hashing shared demo password: %w", err)
	}

	var s Summary
	now := time.Now()

	// -- Super Admin: bootstrapped with raw SQL, same reasoning as
	// internal/bootstrap.CreateAdmin — no actor exists yet to attribute
	// an audit log entry to, and roster.AssignRole's audit call needs
	// one. Every persona created after this uses Alex's member ID as the
	// acting leader, which gives the audit log (see /audit) a realistic
	// trail instead of a wall of unattributed rows. --
	alex, err := createLogin(ctx, pool, "Rivera Family (admin)", markerEmail, passwordHash, "Alex", "Rivera", "adult")
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating super admin: %w", err)
	}
	for _, unitID := range []string{troop.ID, pack.ID} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO role_assignments (member_id, unit_id, role) VALUES ($1, $2, 'super_admin')`,
			alex.MemberID, unitID,
		); err != nil {
			return Summary{}, fmt.Errorf("demoseed: granting super_admin: %w", err)
		}
	}
	actor := alex.MemberID // acting leader for every roster/content/ledger call below

	alexSecret, alexCodes, err := enrollTOTP(ctx, pool, alex.UserID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: enrolling super admin's two-factor: %w", err)
	}
	s.Personas = append(s.Personas, Persona{
		Label: "Super Admin", Unit: "Troop 47 & Pack 47", Email: markerEmail, Password: DemoPassword,
		TOTPSecret: alexSecret, BackupCodes: alexCodes,
		Note: "Holds super_admin (and therefore treasury access) in both units, so two-factor is required — already enrolled and confirmed.",
	})

	// -- Sub-groups, for testing Den-Leader/Patrol-Leader scoping --
	eaglePatrol, err := roster.CreateSubGroup(ctx, pool, troop.ID, "Eagle Patrol", "patrol", actor)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Eagle Patrol: %w", err)
	}
	bearDen, err := roster.CreateSubGroup(ctx, pool, pack.ID, "Bear Den 3", "den", actor)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Bear Den 3: %w", err)
	}

	// -- Troop 47 leadership & program personas --
	sam, err := addPersona(ctx, pool, passwordHash, actor, "Kowalski Family", "sam.scoutmaster@example.com", "Sam", "Kowalski", "adult", troop.ID, nil, "scoutmaster")
	if err != nil {
		return Summary{}, err
	}
	s.Personas = append(s.Personas, Persona{Label: "Scoutmaster", Unit: "Troop 47", Email: "sam.scoutmaster@example.com", Password: DemoPassword})

	ashley, err := addPersona(ctx, pool, passwordHash, actor, "Bennett Family", "ashley.asm@example.com", "Ashley", "Bennett", "adult", troop.ID, nil, "assistant_scoutmaster")
	if err != nil {
		return Summary{}, err
	}
	s.Personas = append(s.Personas, Persona{Label: "Assistant Scoutmaster", Unit: "Troop 47", Email: "ashley.asm@example.com", Password: DemoPassword})

	sydney, err := addPersona(ctx, pool, passwordHash, actor, "Park Family", "sydney.spl@example.com", "Sydney", "Park", "youth", troop.ID, nil, "senior_patrol_leader")
	if err != nil {
		return Summary{}, err
	}
	s.Personas = append(s.Personas, Persona{Label: "Senior Patrol Leader", Unit: "Troop 47", Email: "sydney.spl@example.com", Password: DemoPassword, Note: "Youth member — can submit events for approval but not publish directly."})

	presley, err := addPersona(ctx, pool, passwordHash, actor, "Diaz Family", "presley.patrolleader@example.com", "Presley", "Diaz", "youth", troop.ID, &eaglePatrol.ID, "patrol_leader")
	if err != nil {
		return Summary{}, err
	}
	s.Personas = append(s.Personas, Persona{Label: "Patrol Leader (Eagle Patrol)", Unit: "Troop 47", Email: "presley.patrolleader@example.com", Password: DemoPassword, Note: "Youth member, scoped to Eagle Patrol — can submit events for approval but not publish directly."})

	terry, err := addPersona(ctx, pool, passwordHash, actor, "Osei Family", "terry.treasurer.troop@example.com", "Terry", "Osei", "adult", troop.ID, nil, "treasurer")
	if err != nil {
		return Summary{}, err
	}
	terrySecret, terryCodes, err := enrollTOTP(ctx, pool, terry.UserID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: enrolling Troop treasurer's two-factor: %w", err)
	}
	s.Personas = append(s.Personas, Persona{
		Label: "Treasurer", Unit: "Troop 47", Email: "terry.treasurer.troop@example.com", Password: DemoPassword,
		TOTPSecret: terrySecret, BackupCodes: terryCodes,
		Note: "Two-factor already enrolled and confirmed — logs straight in.",
	})

	pat, err := addPersona(ctx, pool, passwordHash, actor, "Morgan Family", "pat.parent.troop@example.com", "Pat", "Morgan", "adult", troop.ID, nil, "parent")
	if err != nil {
		return Summary{}, err
	}
	riley, err := addFamilyMember(ctx, pool, pat.FamilyID, "Riley", "Morgan", "youth", troop.ID, nil, "scout", actor)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: adding Riley Morgan: %w", err)
	}
	// Give Riley her own individual login, separate from Pat's family-wide
	// one — the one persona in this dataset that demonstrates the
	// individual-Scout-login feature end to end (see internal/auth.User
	// .MemberID). Riley can log in on her own and see just her own stuff:
	// her own ledger account balance (via /accounts), her own RSVPs, and
	// the "scout" role she personally holds — not whatever Pat's shared
	// family login can otherwise do.
	rileyPassword, err := roster.CreateMemberLogin(ctx, pool, riley.MemberID, pat.FamilyID, "riley.scout@example.com", actor)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Riley's individual login: %w", err)
	}
	s.Personas = append(s.Personas, Persona{
		Label: "Scout (individual login)", Unit: "Troop 47", Email: "riley.scout@example.com", Password: rileyPassword,
		Note: "Riley Morgan's own login, separate from the Pat Morgan family login above — logs in seeing just her own stuff (her individual ledger account balance via My Accounts, her own RSVPs, and the scout role) rather than whatever Pat's shared login can do. Demonstrates the individual-Scout-login feature.",
	})
	// Pat holds no treasury role — this is a voluntary enrollment, to
	// demonstrate that two-factor is genuinely opt-in for everyone now
	// (see the "Security" nav link), not just nudged for Treasurer/
	// super_admin logins.
	patSecret, patCodes, err := enrollTOTP(ctx, pool, pat.UserID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: enrolling Pat's voluntary two-factor: %w", err)
	}
	s.Personas = append(s.Personas, Persona{
		Label: "Parent + Scout", Unit: "Troop 47", Email: "pat.parent.troop@example.com", Password: DemoPassword,
		TOTPSecret: patSecret, BackupCodes: patCodes,
		Note: "Family login: Pat Morgan (parent) and Riley Morgan (scout) share this account — see DEMO_DATA.md for what's already on Riley's individual ledger account. Pat holds no treasury role but enrolled in two-factor anyway (already confirmed) — demonstrates that it's available to anyone, not just Treasurer/Admin.",
	})

	// -- Pack 47 leadership & program personas --
	casey, err := addPersona(ctx, pool, passwordHash, actor, "Whitfield Family", "casey.cubmaster@example.com", "Casey", "Whitfield", "adult", pack.ID, nil, "cubmaster")
	if err != nil {
		return Summary{}, err
	}
	s.Personas = append(s.Personas, Persona{Label: "Cubmaster", Unit: "Pack 47", Email: "casey.cubmaster@example.com", Password: DemoPassword})

	dana, err := addPersona(ctx, pool, passwordHash, actor, "Alvarez Family", "dana.denleader@example.com", "Dana", "Alvarez", "adult", pack.ID, &bearDen.ID, "den_leader")
	if err != nil {
		return Summary{}, err
	}
	s.Personas = append(s.Personas, Persona{Label: "Den Leader (Bear Den 3)", Unit: "Pack 47", Email: "dana.denleader@example.com", Password: DemoPassword})

	taylor, err := addPersona(ctx, pool, passwordHash, actor, "Brooks Family", "taylor.treasurer.pack@example.com", "Taylor", "Brooks", "adult", pack.ID, nil, "treasurer")
	if err != nil {
		return Summary{}, err
	}
	s.Personas = append(s.Personas, Persona{
		Label: "Treasurer", Unit: "Pack 47", Email: "taylor.treasurer.pack@example.com", Password: DemoPassword,
		Note: "Two-factor NOT yet enrolled, on purpose — logging in shows the persistent \"set up two-factor\" banner, and /settings/2fa is untouched, so first-time enrollment can be tested end to end.",
	})

	jesse, err := addPersona(ctx, pool, passwordHash, actor, "Nakamura Family", "jesse.parent.pack@example.com", "Jesse", "Nakamura", "adult", pack.ID, nil, "parent")
	if err != nil {
		return Summary{}, err
	}
	morgan, err := addFamilyMember(ctx, pool, jesse.FamilyID, "Morgan", "Nakamura", "youth", pack.ID, nil, "scout", actor)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: adding Morgan Nakamura: %w", err)
	}
	s.Personas = append(s.Personas, Persona{Label: "Parent + Scout", Unit: "Pack 47", Email: "jesse.parent.pack@example.com", Password: DemoPassword, Note: "Family login: Jesse Nakamura (parent) and Morgan Nakamura (scout) share this account."})

	// -- Homepage content: a couple of real overrides per unit, the rest
	// left as Phase 1's stock placeholders so both states are visible. --
	if _, err := content.UpsertSection(ctx, pool, troop.ID, "home-hero", "Hero tagline", "Adventure, leadership, and lifelong friendships — Troop 47 of Yonkers.", sam.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: setting Troop homepage hero: %w", err)
	}
	if _, err := content.UpsertSection(ctx, pool, troop.ID, "home-meeting", "Meeting info", "We meet Tuesdays at 7:00 PM at the Yonkers Scout Hall, 123 Main St.", sam.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: setting Troop homepage meeting info: %w", err)
	}
	if _, err := content.UpsertSection(ctx, pool, pack.ID, "home-hero", "Hero tagline", "Adventure starts here — join Pack 47!", casey.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: setting Pack homepage hero: %w", err)
	}
	if _, err := content.UpsertSection(ctx, pool, pack.ID, "home-meeting", "Meeting info", "Pack meetings are the 2nd Friday of each month, 6:30 PM, at Yonkers Elementary.", casey.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: setting Pack homepage meeting info: %w", err)
	}

	// -- News & photo galleries: one published post, one still-draft post,
	// and one published gallery per unit — so /news, /gallery, and
	// /admin/news' draft-vs-published toggle all have something to look
	// at immediately, without every seeded item already being live. --
	troopNews, err := content.CreatePost(ctx, pool, troop.ID, "post",
		"Summer Camp 2026 Sign-Ups Are Open",
		"Registration for Summer Camp 2026 is open now through the end of the month. See Sam or Ashley at the next meeting to sign up, or check the Summer Camp trip fund on the Treasury page to start saving.",
		"public", "", sam.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Troop news post: %w", err)
	}
	if err := content.SetPublished(ctx, pool, troopNews.ID, troop.ID, true, sam.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: publishing Troop news post: %w", err)
	}
	if _, err := content.CreatePost(ctx, pool, troop.ID, "post",
		"Eagle Project Fundraiser — Details Coming Soon",
		"Presley is planning an Eagle Scout service project fundraiser this fall — more details once the plan is finalized.",
		"members", "", sam.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Troop draft news post: %w", err)
	}
	troopGallery, err := content.CreatePost(ctx, pool, troop.ID, "gallery", "Fall Campout 2025",
		"https://commons.wikimedia.org/wiki/Special:FilePath/Cole_Canoe_Base_Boy_Scout_Campfire.JPG | Campfire on Saturday night\n"+
			"https://commons.wikimedia.org/wiki/Special:FilePath/Campsite_at_NoBeBoSco_07152018.jpg | Eagle Patrol's campsite",
		"public", "", sam.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Troop gallery: %w", err)
	}
	if err := content.SetPublished(ctx, pool, troopGallery.ID, troop.ID, true, sam.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: publishing Troop gallery: %w", err)
	}

	packNews, err := content.CreatePost(ctx, pool, pack.ID, "post",
		"Popcorn Kickoff This Weekend",
		"Pack 47's popcorn fundraiser kicks off this Saturday at the Popcorn Kickoff event — see the calendar for details.",
		"public", "", casey.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Pack news post: %w", err)
	}
	if err := content.SetPublished(ctx, pool, packNews.ID, pack.ID, true, casey.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: publishing Pack news post: %w", err)
	}
	if _, err := content.CreatePost(ctx, pool, pack.ID, "post",
		"Den Leader Volunteers Needed",
		"Bear Den 3 is looking for a second adult volunteer for the spring semester — talk to Dana if you're interested.",
		"members", "", casey.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Pack draft news post: %w", err)
	}
	packGallery, err := content.CreatePost(ctx, pool, pack.ID, "gallery", "Pinewood Derby 2025",
		"https://commons.wikimedia.org/wiki/Special:FilePath/Pinewood_derby_cars_02.jpg | Cars on the track",
		"members", "", casey.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Pack gallery: %w", err)
	}
	if err := content.SetPublished(ctx, pool, packGallery.ID, pack.ID, true, casey.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: publishing Pack gallery: %w", err)
	}

	// One den-scoped news post — shows up only on Bear Den 3's own page
	// (see internal/web/groups.go's GroupView "News" section), not on the
	// unit-wide /news feed.
	bearDenNews, err := content.CreatePost(ctx, pool, pack.ID, "post",
		"Great job at the Pinewood Derby!",
		"Bear Den 3 took second place as a den at this year's derby — thanks to everyone who came out to cheer on our racers.",
		"members", bearDen.ID, casey.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Bear Den 3 news post: %w", err)
	}
	if err := content.SetPublished(ctx, pool, bearDenNews.ID, pack.ID, true, casey.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: publishing Bear Den 3 news post: %w", err)
	}

	// -- Calendar: past / upcoming-public / upcoming-members / pending-approval / trip-fund-linked --
	fallCampout, err := calendar.Create(ctx, pool, calendar.CreateInput{
		UnitID: troop.ID, Title: "Fall Campout 2025", Description: "A weekend campout at Harriman State Park.",
		Location: "Harriman State Park", StartsAt: now.AddDate(0, 0, -120), EndsAt: timePtr(now.AddDate(0, 0, -118)),
		Visibility: "members", CreatedBy: sam.MemberID,
	}, true)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Fall Campout 2025: %w", err)
	}
	mustRSVP(ctx, pool, fallCampout.ID, troop.ID, pat.MemberID, "yes")
	mustRSVP(ctx, pool, fallCampout.ID, troop.ID, riley.MemberID, "yes")
	mustRSVP(ctx, pool, fallCampout.ID, troop.ID, sydney.MemberID, "yes")
	mustRSVP(ctx, pool, fallCampout.ID, troop.ID, presley.MemberID, "maybe")

	openHouse, err := calendar.Create(ctx, pool, calendar.CreateInput{
		UnitID: troop.ID, Title: "Troop 47 Open House", Description: "Come learn about Scouts BSA — all are welcome, no registration required.",
		Location: "Yonkers Scout Hall", StartsAt: now.AddDate(0, 0, 10), EndsAt: timePtr(now.AddDate(0, 0, 10).Add(2 * time.Hour)),
		Visibility: "public", CreatedBy: sam.MemberID,
	}, true)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Troop 47 Open House: %w", err)
	}
	mustRSVP(ctx, pool, openHouse.ID, troop.ID, pat.MemberID, "yes")

	weeklyMeeting, err := calendar.Create(ctx, pool, calendar.CreateInput{
		UnitID: troop.ID, Title: "Weekly Troop Meeting", Description: "Regular weekly meeting.",
		Location: "Yonkers Scout Hall", StartsAt: now.AddDate(0, 0, 4), EndsAt: timePtr(now.AddDate(0, 0, 4).Add(90 * time.Minute)),
		Visibility: "members", CreatedBy: ashley.MemberID,
	}, true)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Weekly Troop Meeting: %w", err)
	}
	mustRSVP(ctx, pool, weeklyMeeting.ID, troop.ID, riley.MemberID, "yes")
	mustRSVP(ctx, pool, weeklyMeeting.ID, troop.ID, sydney.MemberID, "maybe")

	// Submitted by the Patrol Leader — a role that requires approval, so
	// this lands as status=pending_approval with a matching
	// approval_requests row (see calendar.Create), left undecided so
	// Sam/Ashley can test approving or rejecting it on /approvals.
	_, err = calendar.Create(ctx, pool, calendar.CreateInput{
		UnitID: troop.ID, Title: "Eagle Patrol Weekend Hike (proposed)", Description: "Proposed day hike along the Hudson — awaiting Scoutmaster approval.",
		Location: "Untermyer Park", StartsAt: now.AddDate(0, 0, 25), EndsAt: timePtr(now.AddDate(0, 0, 25).Add(5 * time.Hour)),
		Visibility: "members", CreatedBy: presley.MemberID,
	}, false)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating the pending-approval hike: %w", err)
	}

	summerCamp, err := calendar.Create(ctx, pool, calendar.CreateInput{
		UnitID: troop.ID, Title: "Summer Camp 2026", Description: "Week-long summer camp — the Troop's main trip fund for the year.",
		Location: "Ten Mile River Scout Camps", StartsAt: now.AddDate(0, 0, 70), EndsAt: timePtr(now.AddDate(0, 0, 77)),
		Visibility: "members", CreatedBy: sam.MemberID,
	}, true)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Summer Camp 2026: %w", err)
	}
	mustRSVP(ctx, pool, summerCamp.ID, troop.ID, pat.MemberID, "yes")
	mustRSVP(ctx, pool, summerCamp.ID, troop.ID, riley.MemberID, "yes")

	derby, err := calendar.Create(ctx, pool, calendar.CreateInput{
		UnitID: pack.ID, Title: "Pinewood Derby 2025", Description: "Annual Pinewood Derby race.",
		Location: "Yonkers Elementary Gym", StartsAt: now.AddDate(0, 0, -90), EndsAt: timePtr(now.AddDate(0, 0, -90).Add(3 * time.Hour)),
		Visibility: "members", CreatedBy: casey.MemberID,
	}, true)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Pinewood Derby 2025: %w", err)
	}
	mustRSVP(ctx, pool, derby.ID, pack.ID, jesse.MemberID, "yes")
	mustRSVP(ctx, pool, derby.ID, pack.ID, morgan.MemberID, "yes")

	popcornKickoff, err := calendar.Create(ctx, pool, calendar.CreateInput{
		UnitID: pack.ID, Title: "Pack 47 Popcorn Kickoff", Description: "Kickoff event for the fall popcorn fundraiser — open to prospective families.",
		Location: "Yonkers Elementary Gym", StartsAt: now.AddDate(0, 0, 12), EndsAt: timePtr(now.AddDate(0, 0, 12).Add(2 * time.Hour)),
		Visibility: "public", CreatedBy: casey.MemberID,
	}, true)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Pack 47 Popcorn Kickoff: %w", err)
	}
	mustRSVP(ctx, pool, popcornKickoff.ID, pack.ID, jesse.MemberID, "yes")

	denMeeting, err := calendar.Create(ctx, pool, calendar.CreateInput{
		UnitID: pack.ID, Title: "Bear Den 3 Meeting", Description: "Regular den meeting.",
		Location: "Yonkers Elementary, Room 12", StartsAt: now.AddDate(0, 0, 6), EndsAt: timePtr(now.AddDate(0, 0, 6).Add(time.Hour)),
		Visibility: "members", CreatedBy: dana.MemberID,
	}, true)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Bear Den 3 Meeting: %w", err)
	}
	mustRSVP(ctx, pool, denMeeting.ID, pack.ID, morgan.MemberID, "maybe")

	// -- Ledger: Troop 47 --
	troopGeneral, err := ledger.EnsureUnitGeneralAccount(ctx, pool, troop.ID, terry.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: ensuring Troop general fund: %w", err)
	}
	troopExternal, err := ledger.EnsureExternalAccount(ctx, pool, troop.ID, terry.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: ensuring Troop external account: %w", err)
	}
	if _, err := ledger.PostTransaction(ctx, pool, troop.ID, "deposit", "Fall 2025 dues collected", terry.MemberID, []ledger.Posting{
		{AccountID: troopGeneral.ID, AmountCents: 250000},
		{AccountID: troopExternal.ID, AmountCents: -250000},
	}); err != nil {
		return Summary{}, fmt.Errorf("demoseed: posting Troop dues deposit: %w", err)
	}
	if _, err := ledger.PostTransaction(ctx, pool, troop.ID, "expense", "Campsite reservation deposit", terry.MemberID, []ledger.Posting{
		{AccountID: troopGeneral.ID, AmountCents: -18000},
		{AccountID: troopExternal.ID, AmountCents: 18000},
	}); err != nil {
		return Summary{}, fmt.Errorf("demoseed: posting Troop campsite expense: %w", err)
	}

	tripFund, err := ledger.CreateTripFund(ctx, pool, troop.ID, summerCamp.ID, "Summer Camp 2026", terry.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Summer Camp 2026 trip fund: %w", err)
	}
	if _, err := ledger.PostTransaction(ctx, pool, troop.ID, "manual_adjustment", "Troop contribution toward Summer Camp 2026", terry.MemberID, []ledger.Posting{
		{AccountID: troopGeneral.ID, AmountCents: -50000},
		{AccountID: tripFund.ID, AmountCents: 50000},
	}); err != nil {
		return Summary{}, fmt.Errorf("demoseed: posting Troop trip fund contribution: %w", err)
	}

	popcornFundraiser, err := ledger.CreateFundraiser(ctx, pool, troop.ID, "Fall Popcorn Sale 2026", "percentage", "10.00", 0, "", terry.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Fall Popcorn Sale 2026: %w", err)
	}
	if _, err := ledger.RecordFundraiserAllocation(ctx, pool, popcornFundraiser.ID, riley.MemberID, "Riley Morgan", 6000, "", terry.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: crediting Riley from the popcorn sale: %w", err)
	}

	mulchFundraiser, err := ledger.CreateFundraiser(ctx, pool, troop.ID, "Mulch Sale 2026", "fixed_per_item", "", 200, "", terry.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Mulch Sale 2026: %w", err)
	}
	if err := ledger.ConfirmFundraiserCap(ctx, pool, mulchFundraiser.ID, "fixed_per_item", "", 200, terry.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: confirming Mulch Sale 2026's council-approved rate: %w", err)
	}
	if _, err := ledger.RecordFundraiserAllocation(ctx, pool, mulchFundraiser.ID, riley.MemberID, "Riley Morgan", 9000, "15", terry.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: crediting Riley from the mulch sale: %w", err)
	}

	// Already exists from RecordFundraiserAllocation above — EnsureScoutAccount
	// just fetches it, same idempotent shape as EnsureUnitGeneralAccount.
	rileyAccount, err := ledger.EnsureScoutAccount(ctx, pool, troop.ID, riley.MemberID, "Riley Morgan", terry.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: loading Riley's individual account: %w", err)
	}
	// Left pending on purpose, so Terry can test approving/rejecting a
	// trip-fund transfer request on /treasury.
	if _, _, err := ledger.RequestTripFundTransfer(ctx, pool, troop.ID, rileyAccount.ID, tripFund.ID, 2000, "Riley's contribution to Summer Camp 2026", pat.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: requesting Riley's trip fund transfer: %w", err)
	}

	// -- Ledger: Pack 47 --
	packGeneral, err := ledger.EnsureUnitGeneralAccount(ctx, pool, pack.ID, taylor.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: ensuring Pack general fund: %w", err)
	}
	packExternal, err := ledger.EnsureExternalAccount(ctx, pool, pack.ID, taylor.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: ensuring Pack external account: %w", err)
	}
	if _, err := ledger.PostTransaction(ctx, pool, pack.ID, "deposit", "Pack dues & registration", taylor.MemberID, []ledger.Posting{
		{AccountID: packGeneral.ID, AmountCents: 80000},
		{AccountID: packExternal.ID, AmountCents: -80000},
	}); err != nil {
		return Summary{}, fmt.Errorf("demoseed: posting Pack dues deposit: %w", err)
	}
	if _, err := ledger.PostTransaction(ctx, pool, pack.ID, "expense", "Pinewood Derby track rental", taylor.MemberID, []ledger.Posting{
		{AccountID: packGeneral.ID, AmountCents: -6500},
		{AccountID: packExternal.ID, AmountCents: 6500},
	}); err != nil {
		return Summary{}, fmt.Errorf("demoseed: posting Pack derby expense: %w", err)
	}

	packFundraiser, err := ledger.CreateFundraiser(ctx, pool, pack.ID, "Popcorn Fundraiser 2026", "percentage", "8.00", 0, popcornKickoff.ID, taylor.MemberID)
	if err != nil {
		return Summary{}, fmt.Errorf("demoseed: creating Pack popcorn fundraiser: %w", err)
	}
	if _, err := ledger.RecordFundraiserAllocation(ctx, pool, packFundraiser.ID, morgan.MemberID, "Morgan Nakamura", 4000, "", taylor.MemberID); err != nil {
		return Summary{}, fmt.Errorf("demoseed: crediting Morgan from the Pack popcorn fundraiser: %w", err)
	}

	return s, nil
}

// createLogin creates a family, its login, and its first member, in one
// transaction — the same shape as internal/bootstrap.CreateAdmin, except
// the password hash is supplied by the caller (every demo login shares
// one bcrypt hash of DemoPassword rather than each generating its own
// random one) and the member type is a parameter, since some personas
// (the Senior Patrol Leader, the Patrol Leader) are youth.
func createLogin(ctx context.Context, pool *pgxpool.Pool, familyName, email, passwordHash, firstName, lastName, memberType string) (person, error) {
	email = auth.NormalizeEmail(email)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return person{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op if already committed

	var p person
	if err := tx.QueryRow(ctx, `INSERT INTO families (name) VALUES ($1) RETURNING id`, familyName).Scan(&p.FamilyID); err != nil {
		return person{}, fmt.Errorf("creating family %q: %w", familyName, err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO users (family_id, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		p.FamilyID, email, passwordHash,
	).Scan(&p.UserID); err != nil {
		return person{}, fmt.Errorf("creating login %s: %w", email, err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, $2, $3, $4) RETURNING id`,
		p.FamilyID, firstName, lastName, memberType,
	).Scan(&p.MemberID); err != nil {
		return person{}, fmt.Errorf("creating member %s %s: %w", firstName, lastName, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return person{}, err
	}
	return p, nil
}

// addPersona creates a family/login/member (via createLogin) and assigns
// it one role in one unit, via roster.AssignRole so the assignment goes
// through the same audit-logged path a real leader's role-assignment
// form does.
func addPersona(ctx context.Context, pool *pgxpool.Pool, passwordHash, actor, familyName, email, firstName, lastName, memberType, unitID string, subGroupID *string, role string) (person, error) {
	p, err := createLogin(ctx, pool, familyName, email, passwordHash, firstName, lastName, memberType)
	if err != nil {
		return person{}, fmt.Errorf("demoseed: creating %s %s (%s): %w", firstName, lastName, role, err)
	}
	if err := roster.AssignRole(ctx, pool, p.MemberID, unitID, subGroupID, role, actor); err != nil {
		return person{}, fmt.Errorf("demoseed: assigning %s to %s %s: %w", role, firstName, lastName, err)
	}
	return p, nil
}

// addFamilyMember adds a new member to an already-created family (e.g.
// the Scout child in a Parent+Scout demo login) and assigns it a role,
// the same two-step "add member, then assign role" flow /admin/roster
// uses.
func addFamilyMember(ctx context.Context, pool *pgxpool.Pool, familyID, firstName, lastName, memberType, unitID string, subGroupID *string, role, actor string) (person, error) {
	memberID, err := roster.AddMember(ctx, pool, familyID, firstName, lastName, memberType, "", "", actor)
	if err != nil {
		return person{}, fmt.Errorf("adding member: %w", err)
	}
	if err := roster.AssignRole(ctx, pool, memberID, unitID, subGroupID, role, actor); err != nil {
		return person{}, fmt.Errorf("assigning %s: %w", role, err)
	}
	return person{FamilyID: familyID, MemberID: memberID}, nil
}

// enrollTOTP runs a login through the exact same enrollment steps
// /settings/2fa does — generate a secret, compute the code that secret
// produces right now, confirm with that code — so a demo Treasurer login
// is left in the same state a real completed enrollment would leave it
// in (confirmed, with a fresh batch of backup codes), rather than one
// hand-assembled to look that way.
func enrollTOTP(ctx context.Context, pool *pgxpool.Pool, userID string) (secret string, backupCodes []string, err error) {
	// A fresh demo login has no prior confirmed enrollment, so
	// BeginTOTPEnrollment's step-up password check never triggers here —
	// an empty currentPassword and a User stub carrying just the ID is enough.
	secret, err = auth.BeginTOTPEnrollment(ctx, pool, auth.User{ID: userID}, "")
	if err != nil {
		return "", nil, fmt.Errorf("beginning enrollment: %w", err)
	}
	code, err := twofactor.CurrentCode(secret)
	if err != nil {
		return "", nil, fmt.Errorf("computing current code: %w", err)
	}
	backupCodes, err = auth.ConfirmTOTPEnrollment(ctx, pool, userID, code)
	if err != nil {
		return "", nil, fmt.Errorf("confirming enrollment: %w", err)
	}
	return secret, backupCodes, nil
}

// mustRSVP sets an RSVP and swallows a failure into a log line rather
// than aborting the whole seed run — a missing RSVP is cosmetic (less
// demo data to look at), unlike a failed ledger posting or role grant,
// so it's not worth losing everything already seeded over.
func mustRSVP(ctx context.Context, pool *pgxpool.Pool, eventID, unitID, memberID, response string) {
	if err := calendar.SetRSVP(ctx, pool, eventID, unitID, memberID, response); err != nil {
		fmt.Printf("demoseed: warning: setting RSVP for event %s: %v\n", eventID, err)
	}
}

func timePtr(t time.Time) *time.Time { return &t }
