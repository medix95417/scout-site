// Package help is the in-app help content: what each part of the site
// does, written for the person actually using it.
//
// The content is a declarative catalog rather than a hand-written page,
// because help has two properties that hand-written pages get wrong.
//
// First, it must never say more than the reader is allowed to know. A
// Scout with their own login and a Treasurer both open /help; the Scout
// should not be reading about recording expenses or reconciling a bank
// statement. That isn't only tidiness — help text describes where the
// money lives and who signs off on it, and a page that quietly explains
// the treasury to everyone undoes the point of scoping the treasury.
//
// Second, it must never describe a feature this unit has switched off.
// A unit with Advancement disabled has no /advancement page, and help
// that explains one is worse than no help at all: it sends a leader
// looking for a nav link that isn't there and reads as a bug in the site.
//
// Both are properties of *each topic*, so each topic declares them —
// RequiredCapability and RequiresSetting — and Visible() is the single
// place that decides. Adding a topic means answering both questions in
// the topic itself; there is no separate list of rules to keep in sync,
// and help_test.go fails a topic that talks about a gated feature
// without declaring the gate.
package help

import (
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// Audience is who a topic is written for — used only to group topics
// under headings on the page. It carries no access meaning on its own;
// RequiredCapability is what actually gates a topic.
type Audience string

const (
	// AudienceEveryone is for topics any signed-in person needs: the
	// calendar, their own family's details, signing in.
	AudienceEveryone Audience = "Everyone"
	// AudienceScout is for topics specific to a Scout's own login.
	AudienceScout Audience = "For Scouts"
	// AudienceLeader is for unit leadership — roster, content, calendar
	// management.
	AudienceLeader Audience = "For Leaders"
	// AudienceTreasurer is for the people who handle money.
	AudienceTreasurer Audience = "For Treasurers"
	// AudienceAdmin is for a super_admin configuring the site itself.
	AudienceAdmin Audience = "For Site Admins"
)

// audienceOrder is the order sections appear on the page — broadest
// first, so the topics that apply to the most people are at the top and
// nobody has to scroll past someone else's job to find theirs.
var audienceOrder = []Audience{AudienceEveryone, AudienceScout, AudienceLeader, AudienceTreasurer, AudienceAdmin}

// Viewer is everything Visible needs to decide what one person may read:
// the capabilities they hold in this unit, and which features this unit
// has switched on.
type Viewer struct {
	Capabilities units.Capabilities

	// IsIndividualScout is true for a Scout's own login, as opposed to
	// the shared family login. Only affects which of two topics about
	// "your account" is shown — not access.
	IsIndividualScout bool

	// Enabled maps a settings key to whether it's on for this unit.
	// A key that isn't present is treated as off, which is the safe
	// direction: help stays silent about a feature rather than
	// describing one that might not be there.
	Enabled map[string]bool
}

// featureOn reports whether a setting key is on for this viewer's unit.
func (v Viewer) featureOn(key string) bool { return v.Enabled[key] }

// Topic is one help entry.
type Topic struct {
	// ID is a stable anchor/slug, used for in-page links.
	ID string
	// Title is the question or task, phrased the way someone would ask it.
	Title string
	// Audience groups the topic under a heading.
	Audience Audience
	// Body is one or more paragraphs of plain text. Rendered escaped, so
	// it's prose, not markup.
	Body []string

	// RequiredCapability gates the topic on a capability the viewer holds
	// in this unit — one of units.Cap*. Empty means every signed-in
	// person may read it.
	RequiredCapability string

	// RequiresSettings gates the topic on unit feature toggles — any of
	// the settings.* keys. ALL of them must be on for the topic to show,
	// because a topic can sit behind more than one switch: a family's
	// Scout-account balance needs both the treasury itself and the
	// family-facing self-service view, and describing it with either one
	// off sends the reader to a page that isn't there. Empty means the
	// topic isn't tied to a feature that can be switched off.
	RequiresSettings []string

	// ScoutLoginOnly limits a topic to an individual Scout's login (as
	// opposed to the shared family login). Used only to show a Scout the
	// version of "your account" that matches what they actually see.
	ScoutLoginOnly bool

	// FamilyLoginOnly is its mirror, for topics that only make sense on a
	// shared family login — a parent-only action, for instance,
	// which an individual Scout login is refused.
	FamilyLoginOnly bool
}

// Visible reports whether this viewer may read this topic. Every gate is
// AND-ed: a topic about a feature that's off stays hidden even from a
// super_admin, because the page it describes isn't there for them either.
func (t Topic) Visible(v Viewer) bool {
	for _, key := range t.RequiresSettings {
		if !v.featureOn(key) {
			return false
		}
	}
	if t.RequiredCapability != "" && !v.Capabilities.Has(t.RequiredCapability) {
		return false
	}
	if t.ScoutLoginOnly && !v.IsIndividualScout {
		return false
	}
	if t.FamilyLoginOnly && v.IsIndividualScout {
		return false
	}
	return true
}

// Section is one audience's visible topics, for rendering.
type Section struct {
	Audience Audience
	Topics   []Topic
}

// For returns the topics this viewer may read, grouped into sections in
// audienceOrder, with empty sections dropped.
func For(v Viewer) []Section {
	byAudience := map[Audience][]Topic{}
	for _, t := range Topics {
		if t.Visible(v) {
			byAudience[t.Audience] = append(byAudience[t.Audience], t)
		}
	}
	var out []Section
	for _, a := range audienceOrder {
		if topics := byAudience[a]; len(topics) > 0 {
			out = append(out, Section{Audience: a, Topics: topics})
		}
	}
	return out
}

// SettingKeys is every feature toggle any topic depends on — what the web
// layer needs to look up to build a Viewer, so it fetches exactly the
// settings the catalog actually uses rather than a hardcoded list that
// could fall behind.
func SettingKeys() []string {
	seen := map[string]bool{}
	var keys []string
	for _, t := range Topics {
		for _, key := range t.RequiresSettings {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	return keys
}

// Topics is the catalog. Ordered within each audience from "what you'll
// do first" to "what you'll do rarely".
var Topics = []Topic{
	// --- Everyone ---------------------------------------------------
	{
		ID:       "signing-in",
		Title:    "Signing in, and what your login can see",
		Audience: AudienceEveryone,
		Body: []string{
			"Most families share one login. It covers everyone in the household: the calendar, your family's details, and anything the unit has marked members-only.",
			"A Scout can also be given their own separate login. That one sees only their own things — their own RSVPs and any role they personally hold. It deliberately can't see the rest of the family's information.",
			"If you're locked out, use \"Forgot your password?\" on the sign-in page. If that isn't offered, a leader can reset it for you from the roster.",
		},
	},
	{
		ID:       "calendar-rsvp",
		Title:    "Finding events and RSVPing",
		Audience: AudienceEveryone,
		Body: []string{
			"The Calendar lists everything coming up. Events marked for a single den or patrol only show for the families in it.",
			"Open an event to RSVP. Changing your mind later is fine — just RSVP again with the new answer.",
		},
	},
	{
		ID:       "calendar-on-your-phone",
		Title:    "Putting the calendar on your phone",
		Audience: AudienceEveryone,
		Body: []string{
			"On the Calendar page, \"Add to your phone\" creates a private link you can subscribe to from the calendar app you already use. Unit events then sit alongside everything else in your life and update on their own.",
			"The link is yours alone and shows exactly what you can see here, including your den or patrol's own events. Treat it like a password: anyone you send it to can see those events without signing in. If you lose a phone, create a new link — the old one stops working straight away.",
		},
	},
	{
		ID:       "my-family",
		Title:    "Keeping your family's details up to date",
		Audience: AudienceEveryone,
		Body: []string{
			"\"My Family\" shows what the unit has on file for you: names, contact details, and your household address.",
			"You control what the rest of the unit sees. Each of email, phone, and address has its own sharing switch, and they're all off until you turn them on. Leaders can always see your details regardless — the switches govern the family directory other members read.",
		},
	},
	{
		ID:       "photos-news",
		Title:    "News, photos, and resources",
		Audience: AudienceEveryone,
		Body: []string{
			"News and Photos carry what the unit has been up to. Some posts are public and some are members-only; signed in, you see both.",
			"Resources holds handbooks, forms, and useful links. The same split applies — some are public, some appear only once you're signed in.",
		},
	},
	{
		ID:               "my-accounts-family",
		Title:            "Checking your Scout's account balance",
		Audience:         AudienceEveryone,
		RequiresSettings: []string{settings.TreasuryEnabled, settings.ScoutAccountSelfService},
		// A Scout's own login gets "Your Scout account" instead, which
		// says the same thing in the second person — showing both meant a
		// Scout read about "your Scout's" balance as though they were the
		// parent.
		FamilyLoginOnly: true,
		Body: []string{
			"\"My Accounts\" shows the balance the unit is holding for your Scout, and every deposit and withdrawal that got it there — fundraiser earnings in, campout fees out.",
			"If the unit has a trip fund open for an event, you can move money from your Scout's account into it. That request goes to the Treasurer to approve; nothing moves until they do.",
		},
	},

	// --- For Scouts -------------------------------------------------
	{
		ID:             "scout-own-login",
		Title:          "What you can see with your own login",
		Audience:       AudienceScout,
		ScoutLoginOnly: true,
		Body: []string{
			"This login is yours. It shows your own RSVPs, the events for your den or patrol, and any role you hold in the unit.",
			"It doesn't show the rest of your family's information, and your parents' login doesn't show what you do here either. If you need something changed on your family's record, ask a parent or a leader.",
		},
	},
	{
		ID:               "scout-account-balance",
		Title:            "Your Scout account",
		Audience:         AudienceScout,
		ScoutLoginOnly:   true,
		RequiresSettings: []string{settings.TreasuryEnabled, settings.ScoutAccountSelfService},
		Body: []string{
			"If you've earned money through a fundraiser, it's held in your Scout account and shown under \"My Accounts\", along with everything that's been added or spent.",
			"The money is real but it isn't cash in hand — it's credit the unit holds for you, normally put toward camp fees and gear.",
		},
	},

	// --- For Leaders ------------------------------------------------
	{
		ID:                 "roster-basics",
		Title:              "Adding families and Scouts to the roster",
		Audience:           AudienceLeader,
		RequiredCapability: units.CapEditContent,
		Body: []string{
			"\"Manage Roster\" is where people are added. A new family gets one shared login; you can optionally email them their sign-in details as the account is created, which is the easiest way to get someone started.",
			"A Scout who wants their own separate login can be given one from their member page. A member who leaves can be deactivated rather than deleted — their history stays, and they can be brought back later.",
			"Dens and patrols work the same way: archiving one hides it everywhere without losing its members, photos, or history.",
		},
	},
	{
		ID:                 "roster-import",
		Title:              "Importing a roster from Scoutbook",
		Audience:           AudienceLeader,
		RequiredCapability: units.CapEditContent,
		Body: []string{
			"Rather than typing everyone in, export your roster and paste it into the import page. Download the sample CSV there first — it already has the right column headings and this unit's own role and den/patrol names filled in, so you can replace the example people and paste it straight back.",
			"Re-running the same import is safe. A person already on the roster in that family gets their role granted rather than being added twice.",
			"Each brand-new family's temporary password is shown once, on the results page. Copy them down before navigating away.",
		},
	},
	{
		ID:                 "content-editing",
		Title:              "Editing the homepage, news, and photos",
		Audience:           AudienceLeader,
		RequiredCapability: units.CapEditContent,
		Body: []string{
			"The homepage's text and photos are edited from \"Edit Homepage\" — the hero banner, what your program offers, meeting details, and contact information.",
			"News posts and photo galleries are each marked public or members-only when you write them. Public ones show to anyone; members-only ones only appear once someone signs in. The homepage follows the same rule, so a members-only post never shows to a visitor.",
		},
	},
	{
		ID:                 "calendar-manage",
		Title:              "Creating and managing events",
		Audience:           AudienceLeader,
		RequiredCapability: units.CapEditContent,
		Body: []string{
			"Events can be public (on the site for anyone) or members-only, and can be scoped to one den or patrol so only those families see them.",
			"A repeating event is created as a series; editing or deleting one later asks whether you mean that single date or the whole series.",
		},
	},
	{
		ID:                 "calendar-import",
		Title:              "Importing another calendar",
		Audience:           AudienceLeader,
		RequiredCapability: units.CapEditContent,
		Body: []string{
			"Imported calendars brings events in from somewhere else — the council's calendar, or one a leader keeps in Google — so they appear here without anybody retyping them.",
			"In Google Calendar, go to Settings, pick the calendar, open Integrate calendar, and copy the \"Secret address in iCal format\". Paste that here. The plain sharing page won't work; it has to be the .ics address.",
			"Events refresh on their own. Changes made in the source calendar follow through, and anything removed there is removed here too — so imported events can't be edited on this site. Removing a calendar removes the events that came from it, and leaves events created here untouched.",
		},
	},
	{
		ID:                 "advancement",
		Title:              "Recording advancement",
		Audience:           AudienceLeader,
		RequiredCapability: units.CapEditContent,
		RequiresSettings:   []string{settings.AdvancementEnabled},
		Body: []string{
			"Advancement records what each Scout has earned and when. Families see their own Scout's progress; leaders see the whole unit.",
			"This site records advancement for your own reference — it isn't connected to Scoutbook, so anything official still needs recording there too.",
		},
	},
	{
		ID:                 "prospects",
		Title:              "Families asking about joining",
		Audience:           AudienceLeader,
		RequiredCapability: units.CapEditContent,
		RequiresSettings:   []string{settings.ProspectFormEnabled},
		Body: []string{
			"A family who fills in the enquiry form on the public site lands on the Prospects page, with what they told us about themselves and their child.",
			"Move each one along as you go — contacted, visited a meeting, joined, or not joining — and note what was said. That's what stops an enquiry going cold because everyone assumed somebody else had called.",
			"An Admin sets who gets emailed about a new enquiry in Site Settings. Nobody has to be: the enquiry is recorded either way, so the list is the record rather than somebody's inbox.",
			"Joining is still a separate step. When a family signs up, add them on the Manage Roster page and mark the enquiry as joined — a prospect deliberately isn't a roster member until you make them one.",
		},
	},
	{
		ID:                 "newsletters",
		Title:              "Sending a newsletter",
		Audience:           AudienceLeader,
		RequiredCapability: units.CapEditContent,
		RequiresSettings:   []string{settings.NewsletterEnabled},
		Body: []string{
			"Newsletters are written in a formatting editor and sent by email to the unit. Save a draft and come back to it; nothing sends until you choose to send.",
			"If email hasn't been configured for the site, sending will tell you so plainly rather than silently failing — the draft is kept.",
		},
	},

	// --- For Treasurers ---------------------------------------------
	{
		ID:                 "treasury-basics",
		Title:              "Recording money in and out",
		Audience:           AudienceTreasurer,
		RequiredCapability: units.CapManageLedger,
		RequiresSettings:   []string{settings.TreasuryEnabled},
		Body: []string{
			"The Treasury records deposits, expenses, and transfers. Every entry moves money between two places, so the books always balance — money can't simply appear or disappear.",
			"Mistakes are corrected by reversing the entry, not by editing or deleting it. Both the original and the correction stay visible, which is what anyone reviewing the books expects to see.",
			"Money is held in a unit general fund, optionally a separate account per Scout, and optionally a trip fund tied to a particular event.",
		},
	},
	{
		ID:                 "treasury-approvals",
		Title:              "Expenses that need a second signature",
		Audience:           AudienceTreasurer,
		RequiredCapability: units.CapManageLedger,
		RequiresSettings:   []string{settings.TreasuryEnabled},
		Body: []string{
			"Above a set amount, an expense you record waits for the Cubmaster or Scoutmaster — or an Admin — to authorize before it hits the books, so no one person both spends the money and approves it. You can't authorize an expense you entered yourself.",
			"An Admin sets that amount in Site Settings; it starts at $100. There's no switch to turn the requirement off — to effectively disable it, set the threshold very high. Setting it to 0 does the opposite and sends every expense for authorization.",
			"If nobody else on the roster can authorize, an expense over the threshold is refused outright rather than left pending forever. The message says so, and the fix is to assign a Cubmaster or Scoutmaster on the Roster page, or raise the threshold.",
		},
	},
	{
		ID:                 "treasury-reconciliation",
		Title:              "Reconciling against the bank statement",
		Audience:           AudienceTreasurer,
		RequiredCapability: units.CapManageLedger,
		RequiresSettings:   []string{settings.TreasuryEnabled},
		Body: []string{
			"Once a month, when the statement arrives, enter its closing date and balance and tick off the entries that appear on it. Whatever stays unticked is a check nobody has cashed yet or a deposit still in transit — normal, and it carries to next month.",
			"Only the unit's general fund is offered, because that's the account the bank statement actually covers. Scout accounts and trip funds are subdivisions of the same real money, so they're already included in it.",
			"You can only sign off when the difference reaches zero. That's the point of the exercise: a difference that won't close means the books and the bank genuinely disagree, and the fix is a correcting entry in the Treasury, not a change made while reconciling.",
			"A completed reconciliation can't be edited afterwards. It's the record that the books were checked on that date.",
		},
	},
	{
		ID:                 "treasury-fundraisers",
		Title:              "Running a fundraiser",
		Audience:           AudienceTreasurer,
		RequiredCapability: units.CapManageLedger,
		RequiresSettings:   []string{settings.TreasuryEnabled},
		Body: []string{
			"A fundraiser tracks what was sold and splits the proceeds by a rule you set — a percentage, or a fixed amount per item — into the selling Scout's account.",
			"A new fundraiser starts flagged as needing council confirmation, as a reminder that the split has to match what your council actually allows before you rely on it.",
		},
	},
	{
		ID:                 "treasury-reports",
		Title:              "Reports and statements",
		Audience:           AudienceTreasurer,
		RequiredCapability: units.CapManageLedger,
		RequiresSettings:   []string{settings.TreasuryEnabled},
		Body: []string{
			"Reports cover income and expense for a period, current balances, per-Scout accounts, fundraiser proceeds, and a full transaction detail listing. Each can be exported as a PDF for a committee meeting or an annual review.",
			"A report you'll run repeatedly can be saved with its filters, so next month is one click.",
		},
	},

	// --- For Site Admins --------------------------------------------
	{
		ID:                 "settings-overview",
		Title:              "Turning features on and off",
		Audience:           AudienceAdmin,
		RequiredCapability: units.CapSuperAdmin,
		Body: []string{
			"Site Settings switches whole features on and off for a unit — the treasury, advancement, newsletters, family access to Scout account balances.",
			"Turning something off closes its pages and hides its links, but never deletes what's underneath. Turn it back on and the data is exactly as it was. This help page follows the same rule: it stops describing a feature that's switched off.",
			"The Troop and the Pack are configured separately, so turning something off on one doesn't affect the other.",
		},
	},
	{
		ID:                 "roles-capabilities",
		Title:              "Roles and what they can do",
		Audience:           AudienceAdmin,
		RequiredCapability: units.CapSuperAdmin,
		Body: []string{
			"Roles are granted per unit, not site-wide. Someone who leads in the Pack doesn't automatically lead in the Troop.",
			"Beyond the built-in roles you can create custom ones and choose exactly which abilities they carry — editing content, approving submissions, managing the treasury, authorizing expenses.",
			"Anyone who can move money is required to use two-factor authentication; everyone else can turn it on for themselves under Security.",
		},
	},
	{
		ID:                 "audit-log",
		Title:              "The activity log",
		Audience:           AudienceAdmin,
		RequiredCapability: units.CapSuperAdmin,
		Body: []string{
			"Every action of consequence is recorded: roster changes, content edits, approvals, and every ledger entry. The log can be filtered by date, by person, and by what was affected, and exported as a CSV.",
			"This is what makes an unexpected change answerable after the fact — who did it, when, and what it looked like before.",
		},
	},
}
