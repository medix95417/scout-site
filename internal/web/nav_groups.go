package web

import "strings"

// The hamburger's collapsible admin groups.
//
// The menu had grown to fourteen admin links in one undifferentiated run
// under a single "ADMIN" heading — everything from editing the homepage
// to reconciling a bank statement, in the order the features happened to
// be built. They are now three named groups that open on demand, so the
// menu opens at a readable length and the thing you want is under a word
// that describes it.
//
// Collapsed by default, with one exception: the group holding the page
// you are already on opens with the menu, so navigating within a section
// doesn't cost a tap every time. That is what navGroupForPath decides.
const (
	navGroupManage = "manage" // the unit's own content and people
	navGroupMoney  = "money"  // treasury, spending, reconciliation
	navGroupSite   = "site"   // the site itself: log, roles, settings
)

// navGroupForPath returns the group whose submenu should already be open
// for a request path, or "" when the path isn't inside one.
//
// Prefix-matched the same way heroKeyForPath is, so a detail page under
// a section (/admin/news/123/edit) counts as being in that section.
func navGroupForPath(path string) string {
	in := func(prefixes ...string) bool {
		for _, p := range prefixes {
			if path == p || strings.HasPrefix(path, p+"/") {
				return true
			}
		}
		return false
	}

	switch {
	// Checked before the /admin/ group below, because these three live
	// under /admin/ but belong to the site rather than its content.
	case in("/audit", "/admin/custom-roles", "/admin/settings"):
		return navGroupSite
	case in("/treasury", "/expense-approvals"):
		return navGroupMoney
	case in("/admin"):
		return navGroupManage
	default:
		return ""
	}
}
