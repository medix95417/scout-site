package web

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// One install, two units, each emailing as its own address. These run the way
// the real thing does — one web server, one database, one mail server, the
// Troop and the Pack told apart by the Host a request names — and look at the
// From header on mail that really went through SMTP.

const (
	troopFrom    = "Troop 47 <troop@example.org>"
	packFrom     = "Pack 47 <pack@example.org>"
	siteWideFrom = "Test Unit <noreply@test.invalid>" // what the sink's config sends as
)

type twoUnits struct {
	troop, pack           *emailSite
	troopAdmin, packAdmin person
}

func newTwoUnits(t *testing.T) twoUnits {
	t.Helper()
	troop := newEmailSite(t, true)
	pack := troop.sibling("pack", "den")
	return twoUnits{
		troop: troop, pack: pack,
		troopAdmin: troop.leader("Tina", "super_admin", nil),
		packAdmin:  pack.leader("Pam", "super_admin", nil),
	}
}

func (u twoUnits) setFrom(site *emailSite, admin person, from string) reply {
	return site.post(admin, "/admin/settings/unit/mail-from", form("mail_from", from))
}

// lastMessage waits for a message past n and returns it.
func lastMessage(t *testing.T, s *emailSite, n int) (header, envelope string) {
	t.Helper()
	if !s.sink.WaitFor(n+1, 10*time.Second) {
		t.Fatalf("expected a message after %d, the mail server saw %d: %v", n, len(s.sink.Messages()), s.sink.Recipients())
	}
	m := s.sink.Messages()[n]
	return m.HeaderFrom(), m.From
}

func TestTheSettingsPageHasAFromAddressForEachUnit(t *testing.T) {
	u := newTwoUnits(t)

	r := u.troop.get(u.troopAdmin, "/admin/settings")
	if r.status != http.StatusOK {
		t.Fatalf("settings: %d", r.status)
	}
	for _, want := range []string{
		`name="mail_from"`,
		"From address",
		`action="/admin/settings/unit/mail-from"`,
		// Not set: it says what happens instead, and what that address is.
		"Not set, so",
		"noreply@test.invalid",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("the settings page lacks %q", want)
		}
	}
	// It sits under the Email (SMTP) Server heading — the last section of the
	// page — and not under Payments, which is where an unrecognised section of
	// per-unit settings would otherwise land.
	smtp, from, payments := strings.Index(r.body, "Email (SMTP) Server"), strings.Index(r.body, `name="mail_from"`), strings.Index(r.body, "Payments")
	if smtp < 0 || from < smtp {
		t.Errorf("the From field isn't under Email (SMTP) Server (smtp %d, field %d)", smtp, from)
	}
	if payments >= 0 && payments < smtp {
		// Payments comes before the SMTP section, so the field being after
		// the SMTP heading already rules it out; this just keeps that true
		// if the sections are ever reordered.
		if from < payments {
			t.Errorf("the From field is above the Payments heading")
		}
	}

	// Saving shows it back, and says that is what this unit sends as.
	if r := u.setFrom(u.troop, u.troopAdmin, troopFrom); r.status != http.StatusSeeOther || strings.Contains(r.location, "save_error") {
		t.Fatalf("saving: %d %q", r.status, r.location)
	}
	r = u.troop.get(u.troopAdmin, "/admin/settings")
	if !strings.Contains(r.body, `value="Troop 47 &lt;troop@example.org&gt;"`) {
		t.Errorf("the saved address isn't in the field")
	}
	// "goes out as" appears in the fallback sentence too ("Not set, so ... goes
	// out as the site-wide address"), so look for it followed by the unit's own
	// address, and for the fallback sentence to be gone.
	if !strings.Contains(r.body, `goes out as <span class="font-mono">Troop 47 &lt;troop@example.org&gt;</span>`) {
		t.Errorf("the page doesn't say the unit now sends as its own address")
	}
	if strings.Contains(r.body, "Not set, so") {
		t.Errorf("the page still says the unit is using the site-wide address after it saved its own")
	}
}

func TestEachUnitSetsItsOwnAddressAndTheOtherIsUntouched(t *testing.T) {
	u := newTwoUnits(t)

	u.setFrom(u.troop, u.troopAdmin, troopFrom)
	if got := u.pack.get(u.packAdmin, "/admin/settings"); strings.Contains(got.body, "troop@example.org") {
		t.Errorf("the Pack's settings page shows the Troop's address")
	}
	u.setFrom(u.pack, u.packAdmin, packFrom)
	if got := u.troop.get(u.troopAdmin, "/admin/settings"); strings.Contains(got.body, "pack@example.org") {
		t.Errorf("the Troop's settings page shows the Pack's address")
	}
	// Both are still what each set.
	for _, c := range []struct {
		site *emailSite
		who  person
		want string
	}{
		{u.troop, u.troopAdmin, "troop@example.org"},
		{u.pack, u.packAdmin, "pack@example.org"},
	} {
		if r := c.site.get(c.who, "/admin/settings"); !strings.Contains(r.body, c.want) {
			t.Errorf("%s lost its address after the other unit saved", c.site.name)
		}
	}
}

// Roles are per unit. Being Admin of the Troop does not let someone choose what
// address the Pack writes to its families from — even by naming the Pack's host.
func TestOnlyAUnitsOwnAdminSetsItsAddress(t *testing.T) {
	u := newTwoUnits(t)
	scoutmaster := u.pack.leader("Sam", "scoutmaster", nil)

	value := func() string {
		return u.pack.one(`SELECT COALESCE((SELECT value_text FROM unit_settings WHERE unit_id = $1 AND key = 'mail_from'), '')`, u.pack.unitID)
	}

	// The Troop's Admin, on the Pack's host.
	r := u.pack.doOn(u.pack.host, u.troopAdmin, "POST", "/admin/settings/unit/mail-from", form("mail_from", "evil@example.org"))
	if r.status == http.StatusSeeOther && !strings.Contains(r.location, "login") {
		t.Errorf("the Troop's Admin changed the Pack's address: %d %q", r.status, r.location)
	}
	// The Pack's own Scoutmaster: a leader, not an Admin.
	if r := u.pack.post(scoutmaster, "/admin/settings/unit/mail-from", form("mail_from", "sm@example.org")); r.status != http.StatusForbidden {
		t.Errorf("a Scoutmaster saving the unit's From: status %d, want 403", r.status)
	}
	// Signed out.
	if r := u.pack.post(u.pack.anonymous(), "/admin/settings/unit/mail-from", form("mail_from", "anon@example.org")); r.status == http.StatusOK {
		t.Errorf("signed out saved the From: %d", r.status)
	}
	if got := value(); got != "" {
		t.Errorf("the Pack's address was changed by someone who may not: %q", got)
	}

	// And it is the Pack's Admin who can.
	if r := u.setFrom(u.pack, u.packAdmin, packFrom); r.status != http.StatusSeeOther {
		t.Fatalf("the Pack's own Admin: %d", r.status)
	}
	if got := value(); got != packFrom {
		t.Errorf("stored %q, want %q", got, packFrom)
	}
}

func TestAnAddressThatIsNotOneIsRefusedWithAReason(t *testing.T) {
	u := newTwoUnits(t)
	for _, bad := range []string{"not an address", "troop@example.org\r\nBcc: x@example.org"} {
		r := u.setFrom(u.troop, u.troopAdmin, bad)
		if r.status != http.StatusSeeOther || !strings.Contains(r.location, "save_error=") {
			t.Errorf("%q: %d %q, want a redirect carrying the reason", bad, r.status, r.location)
			continue
		}
		page := u.troop.get(u.troopAdmin, r.location)
		if !strings.Contains(page.body, "email address the site can send from") {
			t.Errorf("%q: the page doesn't give the reason", bad)
		}
	}
	if got := u.troop.one(`SELECT COALESCE((SELECT value_text FROM unit_settings WHERE unit_id = $1 AND key = 'mail_from'), '')`, u.troop.unitID); got != "" {
		t.Errorf("a refused address was stored: %q", got)
	}
}

// The test button is how an Admin checks the setup, so it has to send as this
// unit and say which address that was — a test that worked from an address other
// than the one being checked would have told them little.
func TestTheTestEmailGoesOutAsTheUnitAndSaysSo(t *testing.T) {
	u := newTwoUnits(t)
	u.setFrom(u.troop, u.troopAdmin, troopFrom)
	u.setFrom(u.pack, u.packAdmin, packFrom)

	for _, c := range []struct {
		site *emailSite
		who  person
		from string
	}{
		{u.troop, u.troopAdmin, troopFrom},
		{u.pack, u.packAdmin, packFrom},
	} {
		n := len(c.site.sink.Messages())
		r := c.site.post(c.who, "/admin/settings/test-email", form())
		if r.status != http.StatusSeeOther || !strings.Contains(r.location, "mail_test_ok=") {
			t.Fatalf("%s test email: %d %q", c.site.name, r.status, r.location)
		}
		hdr, _ := lastMessage(t, c.site, n)
		if hdr != c.from {
			t.Errorf("%s test email went out as %q, want %q", c.site.name, hdr, c.from)
		}
		page := c.site.get(c.who, r.location)
		addr := c.from[strings.Index(c.from, "<")+1 : strings.Index(c.from, ">")]
		if !strings.Contains(page.body, "from") || !strings.Contains(page.body, addr) {
			t.Errorf("%s: the page doesn't say the test went out from %s", c.site.name, addr)
		}
	}
}

// The point of it all: every kind of mail a unit sends goes out as that unit,
// and the other unit's families never see the first one's address.
func TestEveryKindOfMailGoesOutAsItsOwnUnit(t *testing.T) {
	u := newTwoUnits(t)
	u.setFrom(u.troop, u.troopAdmin, troopFrom)
	u.setFrom(u.pack, u.packAdmin, packFrom)
	u.troop.household("Troopfam", &u.troop.eagle)
	u.pack.household("Packfam", &u.pack.eagle)

	type send struct {
		name string
		do   func() reply
	}
	checks := []struct {
		site  *emailSite
		want  string
		sends []send
	}{
		{u.troop, troopFrom, []send{
			{"a news post", func() reply {
				id := u.troop.newsPost("Troop news", "x", true)
				return u.troop.post(u.troopAdmin, "/admin/news/"+id+"/email", form("audience", "unit"))
			}},
			{"a patrol email", func() reply {
				return u.troop.post(u.troopAdmin, "/groups/"+u.troop.eagle+"/email", form("subject", "Meet", "body", "x"))
			}},
			{"a newsletter", func() reply {
				u.troop.post(u.troopAdmin, "/admin/newsletters", form("subject", "Troop letter", "body", "<p>hi</p>"))
				id := u.troop.one(`SELECT id::text FROM newsletters WHERE unit_id = $1 AND subject = 'Troop letter'`, u.troop.unitID)
				return u.troop.post(u.troopAdmin, "/admin/newsletters/"+id+"/send", form())
			}},
			{"a password reset", func() reply {
				return u.troop.post(u.troop.anonymous(), "/forgot-password", form("email", u.troopAdmin.email))
			}},
		}},
		{u.pack, packFrom, []send{
			{"a news post", func() reply {
				id := u.pack.newsPost("Pack news", "x", true)
				return u.pack.post(u.packAdmin, "/admin/news/"+id+"/email", form("audience", "unit"))
			}},
			{"a den email", func() reply {
				return u.pack.post(u.packAdmin, "/groups/"+u.pack.eagle+"/email", form("subject", "Meet", "body", "x"))
			}},
			{"a newsletter", func() reply {
				u.pack.post(u.packAdmin, "/admin/newsletters", form("subject", "Pack letter", "body", "<p>hi</p>"))
				id := u.pack.one(`SELECT id::text FROM newsletters WHERE unit_id = $1 AND subject = 'Pack letter'`, u.pack.unitID)
				return u.pack.post(u.packAdmin, "/admin/newsletters/"+id+"/send", form())
			}},
			{"a password reset", func() reply {
				return u.pack.post(u.pack.anonymous(), "/forgot-password", form("email", u.packAdmin.email))
			}},
		}},
	}

	for _, c := range checks {
		for _, snd := range c.sends {
			before := len(c.site.sink.Messages())
			r := snd.do()
			if r.status >= 400 {
				t.Fatalf("%s %s: status %d: %s", c.site.name, snd.name, r.status, r.body)
			}
			// Every recipient of one send arrives; they all carry the one From.
			if !c.site.sink.WaitFor(before+1, 10*time.Second) {
				t.Fatalf("%s %s: nothing was mailed", c.site.name, snd.name)
			}
			c.site.settle()
			after := c.site.sink.Messages()[before:]
			for _, m := range after {
				if m.HeaderFrom() != c.want {
					t.Errorf("%s %s went out as %q, want %q", c.site.name, snd.name, m.HeaderFrom(), c.want)
				}
			}
		}
	}
	for _, m := range u.troop.sink.Messages() {
		if strings.Contains(m.HeaderFrom(), "noreply@test.invalid") {
			t.Errorf("a message went out from the site-wide address although both units set their own: %q", m.HeaderFrom())
		}
	}
}

// A site that adds this and changes nothing sees no change: a unit that sets no
// address sends as the site-wide one, even while the other unit sends as its own.
func TestAUnitThatSetsNothingStillSendsAsTheSiteWideAddress(t *testing.T) {
	u := newTwoUnits(t)
	u.setFrom(u.troop, u.troopAdmin, troopFrom) // only the Troop sets one
	u.pack.household("Packfam", &u.pack.eagle)

	n := len(u.pack.sink.Messages())
	id := u.pack.newsPost("Pack news", "x", true)
	if r := u.pack.post(u.packAdmin, "/admin/news/"+id+"/email", form("audience", "unit")); r.status != http.StatusSeeOther {
		t.Fatalf("emailing: %d %s", r.status, r.body)
	}
	hdr, _ := lastMessage(t, u.pack, n)
	if hdr != siteWideFrom {
		t.Errorf("the Pack, with no address of its own, sent as %q, want the site-wide %q", hdr, siteWideFrom)
	}
	u.pack.settle()
	if page := u.pack.get(u.packAdmin, "/admin/settings"); !strings.Contains(page.body, "Not set, so") {
		t.Errorf("the Pack's settings page doesn't say it is using the site-wide address")
	}
}
