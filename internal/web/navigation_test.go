package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/units"
)

func TestUnitSwitchDoesNotCarryPrivateDestination(t *testing.T) {
	u := units.Unit{Hostname: "troop.example.org"}
	r := httptest.NewRequest("GET", "http://pack.example.org:8080/feed/secret?token=private", nil)
	if got := unitHomeURL(u, r, true); got != "https://troop.example.org/" {
		t.Fatalf("secure switch = %q", got)
	}
	if got := unitHomeURL(u, r, false); got != "http://troop.example.org:8080/" {
		t.Fatalf("local switch = %q", got)
	}
}

func TestNavigationMatchesWholeSections(t *testing.T) {
	for _, path := range []string{"/news/item", "/newsletter", "/"} {
		active := ""
		for _, link := range publicNavigation(path) {
			if link.Current {
				active = link.URL
			}
		}
		want := map[string]string{"/news/item": "/news", "/newsletter": "", "/": "/"}[path]
		if active != want {
			t.Errorf("%s: active %q, want %q", path, active, want)
		}
	}
}

func TestHomeHasNoDuplicateShortcutPanel(t *testing.T) {
	data := homePage()
	data.LoggedIn = true
	data.IsUnitMember = false
	out := renderPage(t, "home.html", data)
	if strings.Contains(out, `id="family-home-heading"`) {
		t.Fatal("other-unit visitor sees member shortcuts")
	}
	data.IsUnitMember = true
	out = renderPage(t, "home.html", data)
	if strings.Contains(out, `id="family-home-heading"`) || strings.Contains(out, `class="family-shortcuts"`) {
		t.Fatal("member sees duplicate homepage shortcut panel")
	}
	if strings.Contains(out, `href="/my-family"`) || strings.Contains(out, `href="/accounts"`) {
		t.Fatal("individual Scout sees unavailable account/family actions")
	}
	data.ManagesFamilyContacts = true
	data.TreasuryEnabled = true
	data.ScoutAccountsSelfService = true
	out = renderPage(t, "home.html", data)
	if !strings.Contains(out, `href="/my-family"`) || !strings.Contains(out, `href="/accounts"`) {
		t.Fatal("enabled parent actions are missing")
	}
}

func TestMainNavigationGroupsRequireLoggedInUnitMembership(t *testing.T) {
	for _, unitType := range []string{"pack", "troop"} {
		for _, tc := range []struct {
			name             string
			loggedIn, member bool
		}{
			{"visitor", false, false},
			{"other-unit login", true, false},
			{"member", true, true},
			{"no session", false, true},
		} {
			t.Run(unitType+"/"+tc.name, func(t *testing.T) {
				data := homePage()
				data.Unit.UnitType = unitType
				data.LoggedIn, data.IsUnitMember = tc.loggedIn, tc.member
				data.PublicNavigation = publicNavigation("/")
				out := renderPage(t, "home.html", data)
				nav := htmlBetween(t, out, `<nav aria-label="Main navigation"`, "</nav>")
				want := tc.loggedIn && tc.member
				if strings.Contains(nav, `href="/groups"`) != want {
					t.Fatalf("group navigation visibility wrong: %s", nav)
				}
				if want {
					label := "Dens"
					if unitType == "troop" {
						label = "Patrols"
					}
					if !strings.Contains(nav, ">"+label+"</a>") {
						t.Errorf("missing %s label", label)
					}
				}
			})
		}
	}
}
