package web

import (
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/emailtemplate"
	"github.com/47-yonkers/scout-site/internal/newsletter"
	"github.com/47-yonkers/scout-site/internal/prospect"
	"github.com/47-yonkers/scout-site/internal/units"
)

// TestEveryTemplateParses proves a page compiles; it cannot prove a page
// renders, because a field that doesn't exist on the data struct is an
// execution-time error. These pages are new and each is fed by a struct
// declared inline in its handler, which is exactly where the two drift.

func renderPage(t *testing.T, page string, data any) string {
	t.Helper()
	tmpl, err := parsePageTemplate(page)
	if err != nil {
		t.Fatalf("parsing %s: %v", page, err)
	}
	var sb strings.Builder
	if err := tmpl.ExecuteTemplate(&sb, "base", data); err != nil {
		t.Fatalf("executing %s: %v", page, err)
	}
	return sb.String()
}

func testBase(title string) baseData {
	return baseData{
		PageTitle: title,
		Unit:      units.Unit{Name: "Pack 47", UnitType: "pack", Slug: "pack47"},
		CSRFToken: "tok",
		LoggedIn:  true,
	}
}

func TestCampaignFormRenders(t *testing.T) {
	out := renderPage(t, "admin-prospect-campaign-form.html", campaignFormData{
		baseData: testBase("Edit Message"),
		IsEdit:   true,
		Campaign: prospect.Campaign{
			ID: "camp-1", Subject: "Come and visit", Body: "<p>Hello</p>",
			Status: "draft", TargetStatuses: []string{prospect.StatusNew},
		},
		Statuses: []campaignStatusChoice{
			{Value: prospect.StatusNew, Label: "New enquiry", Selected: true, Count: 3},
			{Value: prospect.StatusJoined, Label: "Joined", Count: 0},
		},
		RecipientCount:   3,
		SavedTemplates:   []emailtemplate.Template{{ID: "tpl-1", Name: "Autumn letter", Subject: "Join us"}},
		StarterTemplates: template.JS("[]"),
		MailerReady:      true,
	})

	for _, want := range []string{
		"Come and visit",
		`action="/admin/prospect-campaigns/camp-1"`,
		`action="/admin/prospect-campaigns/camp-1/send"`,
		"Autumn letter",
		"Send to 3 families",
		"save_as_template",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("campaign form does not contain %q", want)
		}
	}
}

// A composer with email switched off must say so rather than offering a
// Send button that fails.
func TestCampaignFormWarnsWhenMailIsOff(t *testing.T) {
	out := renderPage(t, "admin-prospect-campaign-form.html", campaignFormData{
		baseData:         testBase("New Message"),
		Campaign:         prospect.Campaign{Status: "draft"},
		StarterTemplates: template.JS("[]"),
		MailerReady:      false,
	})
	if !strings.Contains(out, "Email isn't configured") {
		t.Error("composer does not warn that email is unconfigured")
	}
}

func TestCampaignViewRenders(t *testing.T) {
	sent := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	out := renderPage(t, "admin-prospect-campaign-view.html", struct {
		baseData
		Campaign   prospect.Campaign
		Body       template.HTML
		Recipients []campaignRecipientRow
		Delivered  int
		Failed     int
		SentOn     string
	}{
		baseData: testBase("Message to Prospects"),
		Campaign: prospect.Campaign{
			ID: "camp-1", Subject: "Come and visit", Status: "sent",
			TargetStatuses: []string{prospect.StatusNew}, SentAt: &sent, RecipientCount: 2,
		},
		Body: template.HTML("<p>Hello there</p>"),
		Recipients: []campaignRecipientRow{
			{Name: "Robin", Email: "robin@example.com", SentOn: "Wed Mar 4, 2026 10:00 AM"},
			{Name: "Sam", Email: "sam@example.com", Err: "mailbox full"},
		},
		Delivered: 1, Failed: 1, SentOn: "Wed Mar 4, 2026 10:00 AM",
	})

	for _, want := range []string{"Hello there", "robin@example.com", "mailbox full", "New enquiry"} {
		if !strings.Contains(out, want) {
			t.Errorf("campaign view does not contain %q", want)
		}
	}
}

func TestNewsletterViewRenders(t *testing.T) {
	sent := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	count := 2
	out := renderPage(t, "admin-newsletter-view.html", struct {
		baseData
		Newsletter     newsletter.Newsletter
		Body           template.HTML
		Recipients     []newsletterRecipientRow
		Delivered      int
		Failed         int
		SentOn         string
		RecipientCount int
		LegacySend     bool
	}{
		baseData: testBase("Newsletter"),
		Newsletter: newsletter.Newsletter{
			ID: "n-1", Subject: "March update", Status: "sent", SentAt: &sent, RecipientCount: &count,
		},
		Body:           template.HTML("<p>What we did</p>"),
		Recipients:     []newsletterRecipientRow{{Email: "a@example.com", SentOn: "Wed Mar 4"}},
		Delivered:      1,
		SentOn:         "Wed Mar 4, 2026 10:00 AM",
		RecipientCount: 2,
	})
	for _, want := range []string{"March update", "What we did", "a@example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("newsletter view does not contain %q", want)
		}
	}
}

// Newsletters sent before per-recipient recording existed have a count
// and no rows. That must read as "we didn't keep the list", not as "it
// reached nobody".
func TestNewsletterViewExplainsAnOlderSend(t *testing.T) {
	count := 11
	out := renderPage(t, "admin-newsletter-view.html", struct {
		baseData
		Newsletter     newsletter.Newsletter
		Body           template.HTML
		Recipients     []newsletterRecipientRow
		Delivered      int
		Failed         int
		SentOn         string
		RecipientCount int
		LegacySend     bool
	}{
		baseData:       testBase("Newsletter"),
		Newsletter:     newsletter.Newsletter{ID: "n-0", Subject: "Old one", Status: "sent", RecipientCount: &count},
		Body:           template.HTML("<p>x</p>"),
		RecipientCount: 11,
		LegacySend:     true,
	})
	if !strings.Contains(out, "before the site started keeping the list") {
		t.Error("an older send is not explained")
	}
}

func TestUnsubscribePageRenders(t *testing.T) {
	done := renderPage(t, "unsubscribed.html", unsubscribeData{
		baseData: testBase("Unsubscribe"), OK: true, Email: "robin@example.com",
	})
	if !strings.Contains(done, "robin@example.com") || !strings.Contains(done, "You've been unsubscribed") {
		t.Error("the success page does not confirm what happened")
	}

	again := renderPage(t, "unsubscribed.html", unsubscribeData{
		baseData: testBase("Unsubscribe"), OK: true, AlreadyDone: true, Email: "robin@example.com",
	})
	if !strings.Contains(again, "already unsubscribed") {
		t.Error("a second click does not read as already done")
	}

	// A bad token must not echo an address back — that would turn this
	// page into a way to test whether a given address ever enquired.
	// Email is deliberately populated here even though the handler leaves
	// it empty on failure: the property under test is the template's, so
	// the template has to be the thing that withholds it.
	bad := renderPage(t, "unsubscribed.html", unsubscribeData{
		baseData: testBase("Unsubscribe"), OK: false, Email: "robin@example.com",
	})
	if !strings.Contains(bad, "didn't work") {
		t.Error("a bad link does not say so")
	}
	if strings.Contains(bad, "robin@example.com") {
		t.Error("the failure page shows the address, confirming to a prober that it enquired")
	}
	if strings.Contains(bad, "unsubscribed") {
		t.Error("the failure page claims something happened")
	}
}

func TestCalendarConflictsRender(t *testing.T) {
	start := time.Date(2026, 5, 9, 9, 0, 0, 0, time.UTC)
	end := start.Add(6 * time.Hour)
	out := renderPage(t, "admin-calendar-feeds.html", calendarFeedsData{
		baseData: testBase("Imported calendars"),
		Conflicts: []conflictRow{{
			Conflict: calendar.Conflict{
				ID: "c-1", FeedName: "Council calendar",
				Title: "Spring Camporee", Location: "Camp Read",
				StartsAt: start, EndsAt: &end,
				ExistingID: "e-1", ExistingTitle: "Camporee (ours)", ExistingStartsAt: start,
			},
			When:         "Sat 9 May 2026, 09:00–15:00",
			ExistingWhen: "Sat 9 May 2026, 09:00",
		}},
	})

	for _, want := range []string{
		"1 event waiting on you",
		"Spring Camporee",
		"Camporee (ours)",
		"Council calendar",
		`action="/admin/calendar-conflicts/c-1"`,
		`value="import"`, `value="skip"`, `value="replace"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("conflict section does not contain %q", want)
		}
	}

	// All three decisions must be submittable, so all three forms need
	// the token.
	if got := strings.Count(out, `name="csrf_token" value="tok"`); got < 3 {
		t.Errorf("only %d of the three decision forms carry a CSRF token", got)
	}
}
