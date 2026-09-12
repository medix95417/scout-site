package web

import (
	"strings"
	"testing"

	"github.com/47-yonkers/scout-site/internal/prospect"
)

func testEnquiry() prospect.Prospect {
	return prospect.Prospect{
		ID:          "p1",
		ParentName:  "Jo Brennan",
		ParentEmail: "jo@example.com",
		ChildName:   "Alex Brennan",
	}
}

func TestAutoReplyFillsInThePlaceholders(t *testing.T) {
	subject, body := renderProspectAutoReply(
		"About {{child_name}} joining {{unit_name}}",
		"Hi {{parent_name}}, we've got your note about {{child_name}}. Have a look at {{site_url}} meanwhile.",
		testEnquiry(), "Pack 47", "https://pack.example.org")

	if subject != "About Alex Brennan joining Pack 47" {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{"Jo Brennan", "Alex Brennan", "https://pack.example.org"} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "{{") {
		t.Errorf("a placeholder was left unsubstituted: %s", body)
	}
}

// The variation people actually type. A placeholder that doesn't match
// fails silently, in an email a real family reads.
func TestAutoReplyToleratesSpacesInsideTheBraces(t *testing.T) {
	_, body := renderProspectAutoReply("s", "Hello {{ parent_name }} about {{  child_name  }}.",
		testEnquiry(), "Pack 47", "https://pack.example.org")

	if !strings.Contains(body, "Hello Jo Brennan about Alex Brennan.") {
		t.Errorf("spaced placeholders were not substituted: %s", body)
	}
}

// Everything substituted here was typed by a stranger into a public
// form, and the result is sent as HTML.
func TestAutoReplyEscapesWhatTheStrangerTyped(t *testing.T) {
	p := testEnquiry()
	p.ParentName = `Jo <script>alert('x')</script> & Sam`
	p.ChildName = `"Alex"`

	subject, body := renderProspectAutoReply("Hi {{parent_name}}", "<p>Hello {{parent_name}}, about {{child_name}}.</p>",
		p, "Pack 47", "https://pack.example.org")

	if strings.Contains(body, "<script>") {
		t.Errorf("a script tag survived into the email body: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("the name was not escaped: %s", body)
	}
	if !strings.Contains(body, "&amp;") {
		t.Errorf("an ampersand in a name was not escaped: %s", body)
	}
	// The leader's own markup is untouched — that's the point of
	// allowing HTML at all.
	if !strings.Contains(body, "<p>Hello") {
		t.Errorf("the template's own markup was escaped: %s", body)
	}
	// A subject is never rendered as markup, so it isn't escaped — but
	// it must not be able to start a new header line.
	if strings.ContainsAny(subject, "\r\n") {
		t.Errorf("the subject carries a newline: %q", subject)
	}
}

func TestAutoReplyHeaderInjectionInTheSubject(t *testing.T) {
	p := testEnquiry()
	p.ParentName = "Jo\r\nBcc: someone@example.com"

	subject, _ := renderProspectAutoReply("Hi {{parent_name}}", "body", p, "Pack 47", "https://x.test")
	if strings.ContainsAny(subject, "\r\n") {
		t.Fatalf("a name with a newline reached the subject line intact: %q", subject)
	}
	if !strings.Contains(subject, "Bcc") {
		// The content is fine; it's the line break that mattered.
		t.Logf("subject = %q", subject)
	}
}

// A plainly-typed message must not arrive as one run-on line; one with
// markup in it must arrive as written.
func TestAutoReplyPlainTextBecomesParagraphs(t *testing.T) {
	_, body := renderProspectAutoReply("s", "First line.\n\nSecond paragraph.", testEnquiry(), "Pack 47", "https://x.test")
	if !strings.Contains(body, "<p>First line.</p>") || !strings.Contains(body, "<p>Second paragraph.</p>") {
		t.Errorf("plain text was not turned into paragraphs: %s", body)
	}

	_, htmlBody := renderProspectAutoReply("s", `<div class="x">Written as HTML</div>`, testEnquiry(), "Pack 47", "https://x.test")
	if !strings.Contains(htmlBody, `<div class="x">Written as HTML</div>`) {
		t.Errorf("a leader's own HTML was mangled: %s", htmlBody)
	}
}

func TestAutoReplyFallsBackToTheDefaults(t *testing.T) {
	subject, body := renderProspectAutoReply("", "   ", testEnquiry(), "Pack 47", "https://pack.example.org")

	if !strings.Contains(subject, "Pack 47") {
		t.Errorf("the default subject didn't render: %q", subject)
	}
	if !strings.Contains(body, "Jo Brennan") {
		t.Errorf("the default body didn't render: %s", body)
	}
	if strings.Contains(body, "{{") {
		t.Errorf("the default body left a placeholder behind: %s", body)
	}
	// The default must not promise anything a unit would have to correct.
	for _, overclaim := range []string{"within 24", "tomorrow", "call you today"} {
		if strings.Contains(strings.ToLower(body), overclaim) {
			t.Errorf("the default body promises %q on a unit's behalf", overclaim)
		}
	}
}

func TestProspectsPageRendersTheAutoEmailAccordionClosed(t *testing.T) {
	data := prospectsPageData{
		baseData:  testBase("Prospects"),
		Statuses:  prospect.Statuses,
		AutoReply: autoReplyView{Subject: "Thanks!", Body: "Hello", MailerReady: true},
	}
	out := renderPage(t, "admin-prospects.html", data)

	if !strings.Contains(out, "Automatic email to prospects") {
		t.Fatal("the automatic email section is missing from the Prospects page")
	}
	// The section is a <details> with no open attribute — it is set once
	// and then left alone, and it must not push the enquiries down.
	i := strings.Index(out, `id="auto-email"`)
	if i == -1 {
		t.Fatal("the section has no anchor to come back to after saving")
	}
	rest := out[i:]
	openAt := strings.Index(rest, "<details open")
	summaryAt := strings.Index(rest, "<summary")
	if openAt != -1 && openAt < summaryAt {
		t.Error("the automatic email section starts open")
	}
	if !strings.Contains(out, `action="/admin/prospects/auto-email"`) {
		t.Error("the form doesn't post anywhere")
	}
	for _, field := range []string{"prospect_auto_reply_subject", "prospect_auto_reply_body", `name="enabled"`} {
		if !strings.Contains(out, field) {
			t.Errorf("the form is missing %s", field)
		}
	}
	// The placeholders have to reach the page as text a leader can copy,
	// not be eaten by the template engine.
	if !strings.Contains(out, "{{parent_name}}") {
		t.Error("the placeholder list didn't render")
	}
}

func TestProspectsPageSaysWhenEmailIsntConfigured(t *testing.T) {
	data := prospectsPageData{
		baseData:  testBase("Prospects"),
		Statuses:  prospect.Statuses,
		AutoReply: autoReplyView{Subject: "s", Body: "b", Enabled: true, MailerReady: false},
	}
	out := renderPage(t, "admin-prospects.html", data)

	if !strings.Contains(out, "No email is configured for this site yet") {
		t.Error("with no mailer configured the section should say so rather than quietly never sending")
	}
}

// The reply is only worth anything if it is actually sent, and the one
// place that can send it is the form handler. It is a best-effort call
// with no return value, so nothing else fails if it goes missing.
func TestJoinSubmitSendsTheAutoReply(t *testing.T) {
	src, err := readSource("prospects.go")
	if err != nil {
		t.Fatalf("reading prospects.go: %v", err)
	}
	joinSubmit, ok := functionBody(src, "JoinSubmit")
	if !ok {
		t.Fatal("JoinSubmit is gone from prospects.go; if it moved, move this guard with it")
	}
	if !strings.Contains(joinSubmit, "sendProspectAutoReply") {
		t.Error("a new enquiry no longer triggers the automatic reply")
	}
	// After the record is stored, not before: a mail failure must never
	// be the reason an enquiry is lost.
	if strings.Index(joinSubmit, "sendProspectAutoReply") < strings.Index(joinSubmit, "prospect.Create") {
		t.Error("the automatic reply is attempted before the enquiry is stored")
	}
}
