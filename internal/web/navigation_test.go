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

func TestFamilyShortcutsRespectMembershipAndCapabilities(t *testing.T) {
	data := homePage()
	data.LoggedIn = true
	data.IsUnitMember = false
	out := renderPage(t, "home.html", data)
	if strings.Contains(out, `id="family-home-heading"`) {
		t.Fatal("other-unit visitor sees member shortcuts")
	}
	data.IsUnitMember = true
	out = renderPage(t, "home.html", data)
	if !strings.Contains(out, `id="family-home-heading"`) {
		t.Fatal("member has no family home")
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
