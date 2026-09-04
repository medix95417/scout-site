package calendar

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/icalendar"
)

// The feed URL is typed by a leader into an admin form and then fetched
// by the server, which sits on a Docker network alongside the database.
// That makes this validation the boundary between "subscribe to a
// calendar" and "make the server fetch anything reachable from inside the
// network", so it gets tested as the security control it is.
func TestValidateFeedURLRejectsInternalTargets(t *testing.T) {
	rejected := []struct {
		name, url string
	}{
		{"the database container by name", "http://db:5432/"},
		{"loopback", "http://127.0.0.1:8080/admin"},
		{"loopback by name", "http://localhost/admin/settings"},
		{"IPv6 loopback", "http://[::1]/"},
		{"private 10/8", "http://10.0.0.5/cal.ics"},
		{"private 192.168/16", "https://192.168.1.1/cal.ics"},
		{"private 172.16/12", "http://172.16.0.1/cal.ics"},
		{"cloud metadata service", "http://169.254.169.254/latest/meta-data/"},
		{"unspecified address", "http://0.0.0.0/"},
		{"file scheme", "file:///etc/passwd"},
		{"gopher scheme", "gopher://internal/"},
		{"no scheme at all", "just-some-text"},
		{"empty host", "http:///cal.ics"},
	}
	for _, c := range rejected {
		t.Run(c.name, func(t *testing.T) {
			if _, err := validateFeedURL(c.url); !errors.Is(err, ErrFeedURLNotAllowed) {
				t.Errorf("validateFeedURL(%q) allowed it (err=%v) — this is an SSRF hole", c.url, err)
			}
		})
	}
}

func TestValidateFeedURLAcceptsRealCalendars(t *testing.T) {
	allowed := []string{
		"https://calendar.google.com/calendar/ical/abc123%40group.calendar.google.com/private-deadbeef/basic.ics",
		"http://example.com/events.ics",
		"https://p01-calendars.icloud.com/published/2/some-token",
	}
	for _, u := range allowed {
		if _, err := validateFeedURL(u); err != nil {
			t.Errorf("validateFeedURL(%q) rejected a legitimate calendar: %v", u, err)
		}
	}
}

// Calendar apps hand out webcal:// links and pasting one is the obvious
// thing for a leader to do, so it must be accepted and normalised rather
// than rejected as an unknown scheme.
func TestValidateFeedURLNormalisesWebcal(t *testing.T) {
	got, err := validateFeedURL("webcal://example.com/events.ics")
	if err != nil {
		t.Fatalf("webcal:// rejected: %v", err)
	}
	if !strings.HasPrefix(got, "https://") {
		t.Errorf("webcal:// normalised to %q, want an https:// URL", got)
	}
}

// A webcal:// address pointing somewhere internal must not slip past the
// address checks just because it took the rewrite path.
func TestWebcalIsStillAddressChecked(t *testing.T) {
	for _, u := range []string{"webcal://127.0.0.1/x.ics", "webcal://10.0.0.1/x.ics"} {
		if _, err := validateFeedURL(u); !errors.Is(err, ErrFeedURLNotAllowed) {
			t.Errorf("validateFeedURL(%q) allowed an internal target via webcal rewriting", u)
		}
	}
}

func TestStatusMessageExplainsTheCommonMistake(t *testing.T) {
	// Pasting the calendar's HTML sharing page instead of its .ics
	// address is the mistake people actually make, so the admin page has
	// to say what to do about it rather than showing a parse error.
	msg := statusMessage(icalendar.ErrNotCalendar)
	for _, want := range []string{"Secret address", "iCal"} {
		if !strings.Contains(msg, want) {
			t.Errorf("status message %q does not mention %q", msg, want)
		}
	}
}

// The static checks above are the first line only. This is the one that
// matters: the guard that runs at dial time, with the concrete IP the
// socket is about to connect to.
//
// httptest listens on loopback, so fetching it is exactly the situation a
// rebinding attack engineers — a URL that looks fine until it resolves.
// If this ever passes, the server can be made to fetch anything reachable
// from inside its own network.
func TestDialGuardRefusesLoopbackAtConnectTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = w.Write([]byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"))
	}))
	defer srv.Close()

	if _, err := fetchFeed(t.Context(), NewFeedClient(), srv.URL); err == nil {
		t.Fatal("fetch succeeded against a loopback server — the SSRF guard is not working")
	} else if !strings.Contains(err.Error(), "refusing to connect") {
		t.Fatalf("blocked, but not by the dial guard: %v", err)
	}

	// The same server through an unguarded client must work, proving the
	// refusal above is the guard and not a broken fixture.
	if _, err := fetchFeed(t.Context(), &http.Client{}, srv.URL); err != nil {
		t.Fatalf("unguarded fetch of the same server failed, so the test proves nothing: %v", err)
	}
}

func TestSafeDialControlAllowsPublicAddresses(t *testing.T) {
	for _, addr := range []string{"93.184.216.34:443", "[2606:2800:220:1:248:1893:25c8:1946]:443"} {
		if err := safeDialControl("tcp", addr, nil); err != nil {
			t.Errorf("safeDialControl refused public address %s: %v", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:80", "10.1.2.3:443", "169.254.169.254:80", "[::1]:80"} {
		if err := safeDialControl("tcp", addr, nil); err == nil {
			t.Errorf("safeDialControl ALLOWED internal address %s", addr)
		}
	}
}
