package web

import (
	"strings"
	"testing"
)

// The hamburger's admin links live in three collapsible groups. Two
// things have to hold for that to be an improvement rather than a place
// to lose a link: a group appears only when this login can reach
// something inside it, and the group holding the current page is already
// open.

func TestNavGroupForPath(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/admin/roster", navGroupManage},
		{"/admin/news", navGroupManage},
		{"/admin/news/123/edit", navGroupManage}, // a detail page counts as its section
		{"/admin/home", navGroupManage},
		{"/admin/newsletters", navGroupManage},

		{"/treasury", navGroupMoney},
		{"/treasury/reports", navGroupMoney},
		{"/treasury/reconciliations", navGroupMoney},
		{"/expense-approvals", navGroupMoney},

		// These three sit under /admin/ but belong to the site, not its
		// content — so they must beat the /admin prefix, not fall into
		// "Manage" with it.
		{"/audit", navGroupSite},
		{"/admin/settings", navGroupSite},
		{"/admin/custom-roles", navGroupSite},
		{"/admin/custom-roles/abc", navGroupSite},

		// Outside the admin half entirely: nothing opens.
		{"/", ""},
		{"/news", ""},
		{"/roster", ""},
		{"/files", ""},
		{"/my-family", ""},
		// A path that merely starts with the same letters is not inside.
		{"/administrators", ""},
		{"/treasury-notes", ""},
	}
	for _, c := range cases {
		if got := navGroupForPath(c.path); got != c.want {
			t.Errorf("navGroupForPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// navMenu renders a page for one set of permissions and returns its
// markup, menu included. Built on the existing homepage fixture rather
// than an ad-hoc struct, so it satisfies everything home.html reads and
// only the nav-relevant fields are overridden here.
func navMenu(t *testing.T, b baseData) string {
	t.Helper()
	page := homePage()
	unit := page.Unit // keep the fixture's unit; the caller is setting permissions
	b.Unit = unit
	page.baseData = b
	return renderPage(t, "home.html", page)
}

func TestAdminGroupsAppearOnlyWhenTheyHaveSomethingInThem(t *testing.T) {
	// A signed-in family with no leader role: no admin groups at all.
	plain := navMenu(t, baseData{LoggedIn: true, IsUnitMember: true})
	for _, label := range []string{">Manage<", ">Money<", ">Site<"} {
		if strings.Contains(plain, label) {
			t.Errorf("an ordinary member was offered the %s group", label)
		}
	}
	// They still get their own things.
	for _, want := range []string{"/settings/2fa", "/help", "/logout"} {
		if !strings.Contains(plain, want) {
			t.Errorf("a signed-in member lost %s from the menu", want)
		}
	}

	// A content editor: Manage and Site (for the Activity Log), but no
	// Money — they cannot reach anything in it.
	editor := navMenu(t, baseData{LoggedIn: true, IsUnitMember: true, CanEditContent: true})
	if !strings.Contains(editor, ">Manage<") {
		t.Error("a content editor has no Manage group")
	}
	if !strings.Contains(editor, ">Site<") {
		t.Error("a content editor has no Site group, so no way to the Activity Log")
	}
	if strings.Contains(editor, ">Money<") {
		t.Error("a content editor was offered a Money group that would open onto nothing")
	}

	// A treasurer with the treasury switched on: Money, and Site for the
	// log, but no Manage.
	treasurer := navMenu(t, baseData{LoggedIn: true, IsUnitMember: true, CanManageLedger: true, TreasuryEnabled: true})
	if !strings.Contains(treasurer, ">Money<") {
		t.Error("a treasurer has no Money group")
	}
	if strings.Contains(treasurer, ">Manage<") {
		t.Error("a treasurer was offered a Manage group that would open onto nothing")
	}
	if !strings.Contains(treasurer, "/treasury/reconciliations") {
		t.Error("the treasurer's own links aren't in the group")
	}

	// Treasury switched off: no Money group even for a treasurer, since
	// every link in it is behind that toggle.
	off := navMenu(t, baseData{LoggedIn: true, IsUnitMember: true, CanManageLedger: true})
	if strings.Contains(off, ">Money<") {
		t.Error("the Money group showed with the treasury feature switched off")
	}
}

// A login that can approve spending but not edit content used to lose the
// admin section entirely — the whole block was gated on content/ledger/
// super_admin, and approval was not among them. Capabilities are
// per-unit overridable, so that combination is reachable.
func TestApproverWithoutContentRightsStillGetsTheirLink(t *testing.T) {
	out := navMenu(t, baseData{LoggedIn: true, IsUnitMember: true, CanApproveExpenses: true, TreasuryEnabled: true})
	if !strings.Contains(out, ">Money<") {
		t.Fatal("an approver has no Money group")
	}
	if !strings.Contains(out, "/expense-approvals") {
		t.Error("an approver can't reach Authorize Spending from the menu")
	}
	// And nothing they cannot use.
	if strings.Contains(out, "/treasury/reports") {
		t.Error("an approver was shown ledger-only links")
	}
}

func TestTheGroupHoldingThisPageIsOpen(t *testing.T) {
	admin := baseData{LoggedIn: true, IsUnitMember: true, CanEditContent: true, CanManageLedger: true, IsSuperAdmin: true, TreasuryEnabled: true}

	// Nothing open anywhere else on the site.
	elsewhere := navMenu(t, admin)
	if strings.Contains(elsewhere, "<details class=\"group\" open>") {
		t.Error("a group was open on a page outside all of them")
	}

	admin.NavOpenGroup = navGroupMoney
	onTreasury := navMenu(t, admin)
	if strings.Count(onTreasury, "<details class=\"group\" open>") != 1 {
		t.Errorf("want exactly one open group on a treasury page, markup: %q",
			htmlBetween(t, onTreasury, "Manage", "Log out"))
	}
	// The open one is Money, not another.
	opened := onTreasury[strings.Index(onTreasury, "<details class=\"group\" open>"):]
	if !strings.HasPrefix(opened[:200], "<details class=\"group\" open>") || !strings.Contains(opened[:200], "Money") {
		t.Errorf("the wrong group is open: %q", opened[:200])
	}
}

// Every link the old flat menu offered has to still be reachable, or the
// tidy-up quietly lost a page.
func TestNoAdminLinkWasLostInTheRegrouping(t *testing.T) {
	out := navMenu(t, baseData{
		LoggedIn: true, IsUnitMember: true,
		CanEditContent: true, CanManageLedger: true, CanApproveExpenses: true, IsSuperAdmin: true,
		AdvancementEnabled: true, TreasuryEnabled: true, NewsletterEnabled: true,
		ManagesFamilyContacts: true,
	})
	for _, href := range []string{
		"/admin/roster", "/admin/advancement", "/admin/home", "/admin/news",
		"/admin/gallery", "/admin/leaders", "/admin/prospects", "/admin/newsletters",
		"/expense-approvals", "/treasury", "/treasury/reports", "/treasury/reconciliations",
		"/audit", "/admin/custom-roles", "/admin/settings",
		"/roster", "/directory", "/groups", "/advancement", "/files", "/accounts",
		"/my-family", "/settings/2fa", "/help", "/logout",
		"/news", "/gallery", "/leaders", "/calendar", "/resources",
	} {
		if !strings.Contains(out, `"`+href+`"`) {
			t.Errorf("%s is no longer reachable from the menu", href)
		}
	}
}
