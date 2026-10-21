package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/csp"
	"github.com/47-yonkers/scout-site/internal/mailer"
	"github.com/47-yonkers/scout-site/internal/mailer/mailertest"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/units"
)

// Emailing a news post, and emailing a den or patrol, through the real router,
// real sessions and a real database, with an SMTP sink standing in for the
// mail server. What these answer is the question a leader would ask — "who
// actually got it?" — rather than whether a function was called.
//
// Skipped without TEST_DATABASE_URL, like the other integration tests.

var siteCounter atomic.Int64

type emailSite struct {
	t      *testing.T
	pool   *pgxpool.Pool
	sink   *mailertest.Sink
	srv    *httptest.Server
	host   string
	unitID string
	name   string
	sfx    string

	eagle, wolf string // two patrols
}

// person is somebody signed in, plus the address their login receives mail at.
type person struct {
	token string
	email string
	// member is the person's own member id, family their household's, and
	// shared the household's shared login where it has one.
	member, family, shared string
}

func newEmailSite(t *testing.T, withMail bool) *emailSite {
	t.Helper()
	pool := uploadFolderPool(t)
	ctx := context.Background()
	s := &emailSite{t: t, pool: pool, sfx: fmt.Sprintf("%d-%d", time.Now().UnixNano(), siteCounter.Add(1))}
	s.name = "Troop " + s.sfx
	s.host = "pe-" + s.sfx + ".test.invalid"

	s.unitID = s.one(`INSERT INTO units (slug, name, unit_type, hostname) VALUES ($1, $2, 'troop', $3) RETURNING id::text`,
		"pe-"+s.sfx, s.name, s.host)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM units WHERE id = $1`, s.unitID) })

	s.eagle = s.one(`INSERT INTO sub_groups (unit_id, name, sub_group_type) VALUES ($1, 'Eagle Patrol', 'patrol') RETURNING id::text`, s.unitID)
	s.wolf = s.one(`INSERT INTO sub_groups (unit_id, name, sub_group_type) VALUES ($1, 'Wolf Patrol', 'patrol') RETURNING id::text`, s.unitID)

	var mail *mailer.Mailer
	if withMail {
		s.sink = mailertest.New(t)
		mail = mailer.New(s.sink.Config(), nil)
	}
	h, err := New(pool, "", false, mail, nil)
	if err != nil {
		t.Fatalf("building handlers: %v", err)
	}
	mux := http.NewServeMux()
	h.Routes(mux)
	var handler http.Handler = mux
	handler = auth.WithUser(pool)(handler)
	handler = units.Middleware(pool)(handler)
	handler = csp.Middleware(handler)
	s.srv = httptest.NewServer(handler)
	t.Cleanup(func() {
		s.settle()
		s.srv.Close()
	})
	_ = ctx
	return s
}

func (s *emailSite) exec(q string, args ...any) {
	s.t.Helper()
	if _, err := s.pool.Exec(context.Background(), q, args...); err != nil {
		s.t.Fatalf("%s: %v", q, err)
	}
}

func (s *emailSite) one(q string, args ...any) string {
	s.t.Helper()
	var v string
	if err := s.pool.QueryRow(context.Background(), q, args...).Scan(&v); err != nil {
		s.t.Fatalf("%s: %v", q, err)
	}
	return v
}

func (s *emailSite) count(q string, args ...any) int {
	s.t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		s.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// settle waits for background sends to finish, so a test ends with nothing
// still writing to a unit its cleanup is about to delete.
func (s *emailSite) settle() {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := s.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM newsletters WHERE unit_id = $1 AND status = 'sending'`, s.unitID).Scan(&n); err != nil || n == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// family makes a household and, when login is true, its shared login.
func (s *emailSite) family(label string, shared bool) (familyID, sharedEmail string) {
	familyID = s.one(`INSERT INTO families (name) VALUES ($1) RETURNING id::text`, label+" "+s.sfx)
	s.t.Cleanup(func() { _, _ = s.pool.Exec(context.Background(), `DELETE FROM families WHERE id = $1`, familyID) })
	if shared {
		sharedEmail = fmt.Sprintf("%s-shared-%s@test.invalid", strings.ToLower(label), s.sfx)
		s.exec(`INSERT INTO users (family_id, member_id, email, password_hash) VALUES ($1, NULL, $2, 'unused')`, familyID, sharedEmail)
	}
	return familyID, sharedEmail
}

func (s *emailSite) member(familyID, first, kind string) string {
	return s.one(`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, $2, 'Test', $3) RETURNING id::text`,
		familyID, first, kind)
}

func (s *emailSite) role(member string, group *string, role string) {
	s.t.Helper()
	if err := roster.AssignRole(context.Background(), s.pool, member, s.unitID, group, role, member); err != nil {
		s.t.Fatalf("assigning %s: %v", role, err)
	}
}

// signIn issues a session for the login at this address.
func (s *emailSite) signIn(email string) string {
	s.t.Helper()
	id := s.one(`SELECT id::text FROM users WHERE email = $1`, email)
	token, _, err := auth.CreateSession(context.Background(), s.pool, id)
	if err != nil {
		s.t.Fatalf("creating a session: %v", err)
	}
	return token
}

// leader is an adult with their own login and a role. group may be nil for a
// unit-wide role.
func (s *emailSite) leader(label, role string, group *string) person {
	s.t.Helper()
	fam, _ := s.family(label, false)
	m := s.member(fam, label, "adult")
	email := fmt.Sprintf("%s-own-%s@test.invalid", strings.ToLower(label), s.sfx)
	s.exec(`INSERT INTO users (family_id, member_id, email, password_hash) VALUES ($1, $2, $3, 'unused')`, fam, m, email)
	s.role(m, group, role)
	return person{token: s.signIn(email), email: email, member: m, family: fam}
}

// scout is a youth with their own login, in a group with a role.
func (s *emailSite) scout(label, role string, group *string) person {
	s.t.Helper()
	fam, shared := s.family(label, true)
	m := s.member(fam, label, "youth")
	email := fmt.Sprintf("%s-own-%s@test.invalid", strings.ToLower(label), s.sfx)
	s.exec(`INSERT INTO users (family_id, member_id, email, password_hash) VALUES ($1, $2, $3, 'unused')`, fam, m, email)
	s.role(m, group, role)
	return person{token: s.signIn(email), email: email, member: m, family: fam, shared: shared}
}

// household is a family with a shared login and one Scout in a group, with an
// adult parent. It returns the shared login's address.
func (s *emailSite) household(label string, group *string) string {
	s.t.Helper()
	fam, shared := s.family(label, true)
	parent := s.member(fam, label+"Parent", "adult")
	kid := s.member(fam, label+"Kid", "youth")
	s.role(parent, nil, "parent")
	s.role(kid, group, "scout")
	return shared
}

func (s *emailSite) anonymous() person { return person{} }

type reply struct {
	status   int
	body     string
	location string
}

func (s *emailSite) do(p person, method, path string, form url.Values) reply {
	s.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, s.srv.URL+path, body)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Host = s.host
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if p.token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: p.token})
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: string(b), location: resp.Header.Get("Location")}
}

func (s *emailSite) get(p person, path string) reply { return s.do(p, "GET", path, nil) }
func (s *emailSite) post(p person, path string, form url.Values) reply {
	return s.do(p, "POST", path, form)
}

func (s *emailSite) newsPost(title, body string, published bool) string {
	s.t.Helper()
	actor := s.one(`SELECT m.id::text FROM members m JOIN role_assignments ra ON ra.member_id = m.id WHERE ra.unit_id = $1 LIMIT 1`, s.unitID)
	p, err := content.CreatePost(context.Background(), s.pool, s.unitID, "post", title, body, "members", "", actor)
	if err != nil {
		s.t.Fatalf("creating a news post: %v", err)
	}
	if published {
		if err := content.SetPublished(context.Background(), s.pool, p.ID, s.unitID, true, actor); err != nil {
			s.t.Fatalf("publishing it: %v", err)
		}
	}
	return p.ID
}

// delivered waits for n messages and returns who they went to.
func (s *emailSite) delivered(n int) []string {
	s.t.Helper()
	if !s.sink.WaitFor(n, 10*time.Second) {
		s.t.Fatalf("expected %d messages, the mail server saw %d: %v", n, len(s.sink.Messages()), s.sink.Recipients())
	}
	s.settle()
	return s.sink.Recipients()
}

// nothingSent asserts that nothing was recorded and nothing was mailed. The
// database half is what makes it decisive: a send that had started would have
// a row by now, whatever the mail server has seen so far.
func (s *emailSite) nothingSent(what string) {
	s.t.Helper()
	if n := s.count(`SELECT count(*) FROM newsletters WHERE unit_id = $1 AND kind <> 'newsletter'`, s.unitID); n != 0 {
		s.t.Errorf("%s: %d email record(s) were created, want none", what, n)
	}
	time.Sleep(150 * time.Millisecond)
	if s.sink != nil {
		if got := s.sink.Messages(); len(got) != 0 {
			s.t.Errorf("%s: mail went to %v, want none", what, s.sink.Recipients())
		}
	}
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for _, w := range want {
		seen[w]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func form(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return v
}

func (s *emailSite) setFeature(on bool) {
	s.t.Helper()
	actor := s.one(`SELECT m.id::text FROM members m JOIN role_assignments ra ON ra.member_id = m.id WHERE ra.unit_id = $1 LIMIT 1`, s.unitID)
	if err := settings.SetForUnit(context.Background(), s.pool, s.unitID, settings.NewsletterEnabled, on, actor); err != nil {
		s.t.Fatalf("setting the email switch: %v", err)
	}
}

// --- unit news ---------------------------------------------------------------

func TestPublishingAPostEmailsNobody(t *testing.T) {
	s := newEmailSite(t, true)
	editor := s.leader("Sam", "scoutmaster", nil)
	s.household("Fam", &s.eagle)

	r := s.post(editor, "/admin/news", form("title", "Campout news", "body", "Bring a jacket.", "visibility", "members"))
	if r.status != http.StatusSeeOther {
		t.Fatalf("creating the post: %d %s", r.status, r.body)
	}
	id := s.one(`SELECT id::text FROM content_pages WHERE unit_id = $1 AND title = 'Campout news'`, s.unitID)
	if r := s.post(editor, "/admin/news/"+id+"/publish", form()); r.status != http.StatusSeeOther {
		t.Fatalf("publishing: %d %s", r.status, r.body)
	}
	s.nothingSent("publishing a news post")
}

func TestAnEditorCanEmailAPublishedPostToEveryone(t *testing.T) {
	s := newEmailSite(t, true)
	editor := s.leader("Sam", "scoutmaster", nil)
	shared := s.household("Fam", &s.eagle)
	id := s.newsPost("Campout news", "Bring a jacket.\nhttps://example.org/list", true)

	r := s.post(editor, "/admin/news/"+id+"/email", form("audience", "unit"))
	if r.status != http.StatusSeeOther || !strings.Contains(r.location, "emailed=") {
		t.Fatalf("emailing: %d %q %s", r.status, r.location, r.body)
	}

	// Every login on the roster: the household's shared one and the editor's own.
	if got := s.delivered(2); !sameSet(got, shared, editor.email) {
		t.Errorf("reached %v, want %v and %v", got, shared, editor.email)
	}
	m := s.sink.Messages()[0]
	if m.Subject() != "Campout news" {
		t.Errorf("subject = %q", m.Subject())
	}
	body := m.Body()
	for _, want := range []string{"Bring a jacket.", "https://example.org/list", "http://" + s.host + "/news/" + id, "Sam"} {
		if !strings.Contains(body, want) {
			t.Errorf("the email lacks %q:\n%s", want, body)
		}
	}
}

func TestANewsEmailCanGoToSelectedPatrolsOnly(t *testing.T) {
	s := newEmailSite(t, true)
	editor := s.leader("Sam", "scoutmaster", nil)
	eagleFam := s.household("Eaglefam", &s.eagle)
	s.household("Wolffam", &s.wolf)
	id := s.newsPost("Eagle campout", "Eagles only.", true)

	r := s.post(editor, "/admin/news/"+id+"/email", form("audience", "groups", "group_ids", s.eagle))
	if r.status != http.StatusSeeOther {
		t.Fatalf("emailing: %d %s", r.status, r.body)
	}
	if got := s.delivered(1); !sameSet(got, eagleFam) {
		t.Errorf("an Eagle-only post reached %v, want only %v", got, eagleFam)
	}
}

func TestAGroupsAudienceWithNoGroupChosenSendsNothing(t *testing.T) {
	s := newEmailSite(t, true)
	editor := s.leader("Sam", "scoutmaster", nil)
	s.household("Fam", &s.eagle)
	id := s.newsPost("Campout news", "x", true)

	// "Only these patrols" with none ticked must not fall back to everyone.
	r := s.post(editor, "/admin/news/"+id+"/email", form("audience", "groups"))
	if r.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", r.status)
	}
	s.nothingSent("an audience of patrols with none chosen")
}

func TestAPostAlreadyEmailedIsNotSentAgainWithoutSayingSo(t *testing.T) {
	s := newEmailSite(t, true)
	editor := s.leader("Sam", "scoutmaster", nil)
	fam := s.household("Fam", &s.eagle)
	id := s.newsPost("Campout news", "x", true)

	if r := s.post(editor, "/admin/news/"+id+"/email", form("audience", "unit")); r.status != http.StatusSeeOther {
		t.Fatalf("first send: %d %s", r.status, r.body)
	}
	s.delivered(2)
	before := len(s.sink.Messages())

	r := s.post(editor, "/admin/news/"+id+"/email", form("audience", "unit"))
	if r.status != http.StatusSeeOther || !strings.Contains(r.location, "email=again") {
		t.Errorf("a second send without confirming: %d %q, want a redirect asking for confirmation", r.status, r.location)
	}
	time.Sleep(150 * time.Millisecond)
	if n := len(s.sink.Messages()); n != before {
		t.Errorf("a second send without confirming mailed %d more message(s)", n-before)
	}

	if r := s.post(editor, "/admin/news/"+id+"/email", form("audience", "unit", "again", "1")); r.status != http.StatusSeeOther || !strings.Contains(r.location, "emailed=") {
		t.Fatalf("a confirmed resend: %d %q %s", r.status, r.location, r.body)
	}
	if !s.sink.WaitFor(before+2, 10*time.Second) {
		t.Errorf("a confirmed resend did not go out: %v", s.sink.Recipients())
	}
	_ = fam
}

func TestOnlyAPublishedUnitPostCanBeEmailedFromTheNewsPage(t *testing.T) {
	s := newEmailSite(t, true)
	editor := s.leader("Sam", "scoutmaster", nil)
	s.household("Fam", &s.eagle)

	draft := s.newsPost("Not yet", "x", false)
	if r := s.post(editor, "/admin/news/"+draft+"/email", form("audience", "unit")); r.status != http.StatusBadRequest {
		t.Errorf("a draft: status %d, want 400", r.status)
	}

	// A den's or patrol's own update has type 'post' too, but it is not unit
	// news: emailing it from here would send one patrol's message to everyone.
	groupPost, err := content.CreatePost(context.Background(), s.pool, s.unitID, "post", "Patrol only", "x", "members", s.eagle, editor.member)
	if err != nil {
		t.Fatal(err)
	}
	if err := content.SetPublished(context.Background(), s.pool, groupPost.ID, s.unitID, true, editor.member); err != nil {
		t.Fatal(err)
	}
	if r := s.post(editor, "/admin/news/"+groupPost.ID+"/email", form("audience", "unit")); r.status != http.StatusNotFound {
		t.Errorf("a patrol's update: status %d, want 404", r.status)
	}
	s.nothingSent("a draft and a patrol's update")
}

func TestOnlyAContentEditorCanEmailNews(t *testing.T) {
	s := newEmailSite(t, true)
	s.leader("Sam", "scoutmaster", nil)
	fam, shared := s.family("Par", true)
	parent := s.member(fam, "Par", "adult")
	s.role(parent, nil, "parent")
	id := s.newsPost("Campout news", "x", true)

	for name, who := range map[string]person{
		"a parent":   {token: s.signIn(shared)},
		"signed out": s.anonymous(),
	} {
		r := s.post(who, "/admin/news/"+id+"/email", form("audience", "unit"))
		if r.status == http.StatusOK || (r.status == http.StatusSeeOther && strings.Contains(r.location, "emailed=")) {
			t.Errorf("%s was allowed to email a post: %d %q", name, r.status, r.location)
		}
	}
	s.nothingSent("people who may not edit news")
}

func TestNewsEmailFollowsTheEmailSwitchAndTheMailServer(t *testing.T) {
	// Switched off: refused, and the panel isn't offered.
	s := newEmailSite(t, true)
	editor := s.leader("Sam", "scoutmaster", nil)
	s.household("Fam", &s.eagle)
	id := s.newsPost("Campout news", "x", true)
	s.setFeature(false)
	if r := s.post(editor, "/admin/news/"+id+"/email", form("audience", "unit")); r.status != http.StatusForbidden {
		t.Errorf("with email switched off: status %d, want 403", r.status)
	}
	if r := s.get(editor, "/admin/news/"+id+"/edit"); strings.Contains(r.body, "Email this post") {
		t.Errorf("the panel is offered while email is switched off")
	}
	s.nothingSent("email switched off")

	// No mail server: refused with a reason, and the panel says so rather than
	// offering a button that cannot work.
	n := newEmailSite(t, false)
	editor = n.leader("Sam", "scoutmaster", nil)
	n.household("Fam", &n.eagle)
	id = n.newsPost("Campout news", "x", true)
	r := n.post(editor, "/admin/news/"+id+"/email", form("audience", "unit"))
	if r.status != http.StatusServiceUnavailable {
		t.Errorf("without a mail server: status %d, want 503", r.status)
	}
	page := n.get(editor, "/admin/news/"+id+"/edit")
	if !strings.Contains(page.body, "Email isn't set up for this site yet") {
		t.Errorf("the edit page does not say email isn't set up")
	}
	n.nothingSent("no mail server")
}

func TestTheEmailPanelIsOnlyOnAPublishedPost(t *testing.T) {
	s := newEmailSite(t, true)
	editor := s.leader("Sam", "scoutmaster", nil)
	s.household("Fam", &s.eagle)

	draft := s.newsPost("Draft", "x", false)
	if r := s.get(editor, "/admin/news/"+draft+"/edit"); r.status != http.StatusOK || strings.Contains(r.body, "Email this post") {
		t.Errorf("a draft offers the panel (status %d)", r.status)
	}
	live := s.newsPost("Live", "x", true)
	r := s.get(editor, "/admin/news/"+live+"/edit")
	for _, want := range []string{"Email this post", "Eagle Patrol", "Wolf Patrol", "Publishing a post doesn't email anyone"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("a published post's panel lacks %q", want)
		}
	}
	if strings.Contains(r.body, `name="again"`) {
		t.Errorf("a post never emailed asks whether to send it again")
	}
}

// --- dens and patrols -------------------------------------------------------

func TestAPatrolLeaderEmailsTheirOwnPatrolOnly(t *testing.T) {
	s := newEmailSite(t, true)
	pl := s.scout("Pat", "patrol_leader", &s.eagle)
	eagleFam := s.household("Eaglefam", &s.eagle)
	s.household("Wolffam", &s.wolf)

	// The composer shows how many it reaches and never who.
	page := s.get(pl, "/groups/"+s.eagle+"/email")
	if page.status != http.StatusOK {
		t.Fatalf("the composer: %d %s", page.status, page.body)
	}
	if strings.Contains(page.body, "@test.invalid") {
		t.Errorf("the composer shows email addresses to a Patrol Leader")
	}
	if strings.Contains(page.body, `name="post_to_page"`) {
		t.Errorf("a Patrol Leader is offered posting to the patrol page, which needs editing rights they lack")
	}

	r := s.post(pl, "/groups/"+s.eagle+"/email", form("subject", "Meet at 7", "body", "Bring a flashlight."))
	if r.status != http.StatusSeeOther || !strings.Contains(r.location, "sent=") {
		t.Fatalf("sending: %d %q %s", r.status, r.location, r.body)
	}
	// The Eagle household, and the Patrol Leader's own: their shared login and
	// their own, since they are in Eagle. The Wolf household is not reached.
	if got := s.delivered(3); !sameSet(got, eagleFam, pl.email, pl.shared) {
		t.Errorf("a message to Eagle reached %v, want %v, %v and %v", got, eagleFam, pl.email, pl.shared)
	}
	m := s.sink.Messages()[0]
	if m.Subject() != "Eagle Patrol: Meet at 7" {
		t.Errorf("subject = %q, want it named for the patrol", m.Subject())
	}

	// And not another patrol.
	if r := s.get(pl, "/groups/"+s.wolf+"/email"); r.status != http.StatusForbidden {
		t.Errorf("another patrol's composer: status %d, want 403", r.status)
	}
	before := len(s.sink.Messages())
	if r := s.post(pl, "/groups/"+s.wolf+"/email", form("subject", "Hi Wolves", "body", "x")); r.status != http.StatusForbidden {
		t.Errorf("sending to another patrol: status %d, want 403", r.status)
	}
	time.Sleep(150 * time.Millisecond)
	if n := len(s.sink.Messages()); n != before {
		t.Errorf("a Patrol Leader's message to another patrol was mailed to %d more", n-before)
	}
}

func TestAPatrolLeaderCannotPostToThePatrolPageOrAnyoneElses(t *testing.T) {
	s := newEmailSite(t, true)
	pl := s.scout("Pat", "patrol_leader", &s.eagle)
	s.household("Eaglefam", &s.eagle)

	r := s.post(pl, "/groups/"+s.eagle+"/email", form("subject", "Hi", "body", "x", "post_to_page", "1"))
	if r.status != http.StatusForbidden {
		t.Errorf("posting to the page: status %d, want 403", r.status)
	}
	if n := s.count(`SELECT count(*) FROM content_pages WHERE unit_id = $1 AND sub_group_id IS NOT NULL`, s.unitID); n != 0 {
		t.Errorf("a Patrol Leader's refused request still posted %d update(s)", n)
	}
	s.nothingSent("a refused post-to-page")

	// The page's own form is the existing permission, unchanged.
	if r := s.post(pl, "/admin/groups/"+s.eagle+"/news", form("title", "T", "body", "x")); r.status != http.StatusForbidden {
		t.Errorf("a Patrol Leader posting an update: status %d, want 403", r.status)
	}
}

func TestADenOrPatrolLinkIsOnlyShownToThoseWhoCanEmailIt(t *testing.T) {
	s := newEmailSite(t, true)
	pl := s.scout("Pat", "patrol_leader", &s.eagle)
	s.household("Eaglefam", &s.eagle)
	s.household("Wolffam", &s.wolf)

	if r := s.get(pl, "/groups/"+s.eagle); !strings.Contains(r.body, "Email this patrol") {
		t.Errorf("a Patrol Leader isn't offered to email their own patrol")
	}
	if r := s.get(pl, "/groups/"+s.wolf); strings.Contains(r.body, "Email this patrol") {
		t.Errorf("a Patrol Leader is offered to email a patrol that isn't theirs")
	}
	s.setFeature(false)
	if r := s.get(pl, "/groups/"+s.eagle); strings.Contains(r.body, "Email this patrol") {
		t.Errorf("the link is shown while email is switched off")
	}
	if r := s.get(pl, "/groups/"+s.eagle+"/email"); r.status != http.StatusForbidden {
		t.Errorf("the composer while email is switched off: status %d, want 403", r.status)
	}
}

func TestAScoutInAPatrolCannotEmailIt(t *testing.T) {
	s := newEmailSite(t, true)
	scout := s.scout("Sky", "scout", &s.eagle)
	s.household("Eaglefam", &s.eagle)

	if r := s.get(scout, "/groups/"+s.eagle+"/email"); r.status != http.StatusForbidden {
		t.Errorf("a Scout's composer: status %d, want 403", r.status)
	}
	if r := s.post(scout, "/groups/"+s.eagle+"/email", form("subject", "Hi", "body", "x")); r.status != http.StatusForbidden {
		t.Errorf("a Scout sending: status %d, want 403", r.status)
	}
	s.nothingSent("a Scout emailing their patrol")
}

func TestAUnitWideLeaderCanEmailAnyPatrolAndPostItToThePage(t *testing.T) {
	s := newEmailSite(t, true)
	sm := s.leader("Sam", "scoutmaster", nil)
	wolfFam := s.household("Wolffam", &s.wolf)
	s.household("Eaglefam", &s.eagle)

	r := s.post(sm, "/groups/"+s.wolf+"/email", form("subject", "Hike", "body", "Saturday.", "post_to_page", "1"))
	if r.status != http.StatusSeeOther {
		t.Fatalf("sending: %d %s", r.status, r.body)
	}
	if got := s.delivered(1); !sameSet(got, wolfFam) {
		t.Errorf("reached %v, want only the Wolf family %v", got, wolfFam)
	}
	if n := s.count(`SELECT count(*) FROM content_pages WHERE unit_id = $1 AND sub_group_id = $2 AND title = 'Hike' AND status = 'published'`, s.unitID, s.wolf); n != 1 {
		t.Errorf("the update was posted to the patrol's page %d times, want once", n)
	}
	// The record links the email to that post.
	if n := s.count(`SELECT count(*) FROM newsletters WHERE unit_id = $1 AND kind = 'group_update' AND post_id IS NOT NULL`, s.unitID); n != 1 {
		t.Errorf("the email record isn't linked to the post it was also published as (%d)", n)
	}
}

func TestAGroupEmailThatWouldReachNobodyIsRefused(t *testing.T) {
	s := newEmailSite(t, true)
	sm := s.leader("Sam", "scoutmaster", nil)
	r := s.post(sm, "/groups/"+s.eagle+"/email", form("subject", "Hi", "body", "x", "post_to_page", "1"))
	if r.status != http.StatusBadRequest {
		t.Errorf("an empty patrol: status %d, want 400", r.status)
	}
	if n := s.count(`SELECT count(*) FROM content_pages WHERE unit_id = $1 AND sub_group_id IS NOT NULL`, s.unitID); n != 0 {
		t.Errorf("a refused email still posted the update to the page")
	}
	s.nothingSent("an empty patrol")
}

func TestTheSameGroupEmailTwiceInAMinuteIsSentOnce(t *testing.T) {
	s := newEmailSite(t, true)
	sm := s.leader("Sam", "scoutmaster", nil)
	s.household("Eaglefam", &s.eagle)

	f := form("subject", "Meet at 7", "body", "x")
	if r := s.post(sm, "/groups/"+s.eagle+"/email", f); r.status != http.StatusSeeOther {
		t.Fatalf("first: %d %s", r.status, r.body)
	}
	s.delivered(1)
	before := len(s.sink.Messages())
	if r := s.post(sm, "/groups/"+s.eagle+"/email", f); r.status != http.StatusConflict {
		t.Errorf("a double submit: status %d, want 409", r.status)
	}
	time.Sleep(150 * time.Millisecond)
	if n := len(s.sink.Messages()); n != before {
		t.Errorf("a double submit mailed %d more message(s)", n-before)
	}
}

func TestSubjectAndBodyAreRequiredAndBounded(t *testing.T) {
	s := newEmailSite(t, true)
	sm := s.leader("Sam", "scoutmaster", nil)
	s.household("Eaglefam", &s.eagle)

	for name, f := range map[string]url.Values{
		"no subject":   form("subject", " ", "body", "x"),
		"no body":      form("subject", "Hi", "body", "  "),
		"long subject": form("subject", strings.Repeat("a", maxGroupEmailSubject+1), "body", "x"),
	} {
		if r := s.post(sm, "/groups/"+s.eagle+"/email", f); r.status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.status)
		}
	}
	s.nothingSent("invalid composer input")
}

func TestAnotherUnitsGroupIsNotThere(t *testing.T) {
	s := newEmailSite(t, true)
	other := newEmailSite(t, true)
	sm := s.leader("Sam", "scoutmaster", nil)

	if r := s.get(sm, "/groups/"+other.eagle+"/email"); r.status != http.StatusNotFound {
		t.Errorf("another unit's group composer: status %d, want 404", r.status)
	}
	if r := s.post(sm, "/groups/"+other.eagle+"/email", form("subject", "Hi", "body", "x")); r.status != http.StatusNotFound {
		t.Errorf("another unit's group send: status %d, want 404", r.status)
	}
	other.nothingSent("a stranger's request")
}

// --- posting an update to a group's page -----------------------------------

func TestPostingAGroupUpdateEmailsNobodyUnlessAsked(t *testing.T) {
	s := newEmailSite(t, true)
	sm := s.leader("Sam", "scoutmaster", nil)
	s.household("Eaglefam", &s.eagle)

	r := s.post(sm, "/admin/groups/"+s.eagle+"/news", form("title", "Quiet update", "body", "x"))
	if r.status != http.StatusSeeOther {
		t.Fatalf("posting: %d %s", r.status, r.body)
	}
	if n := s.count(`SELECT count(*) FROM content_pages WHERE unit_id = $1 AND sub_group_id = $2 AND title = 'Quiet update'`, s.unitID, s.eagle); n != 1 {
		t.Fatalf("the update wasn't posted")
	}
	s.nothingSent("posting a group update with the box unticked")
}

func TestPostingAGroupUpdateCanAlsoEmailIt(t *testing.T) {
	s := newEmailSite(t, true)
	sm := s.leader("Sam", "scoutmaster", nil)
	eagleFam := s.household("Eaglefam", &s.eagle)
	s.household("Wolffam", &s.wolf)

	r := s.post(sm, "/admin/groups/"+s.eagle+"/news", form("title", "Loud update", "body", "Hello.", "also_email", "1"))
	if r.status != http.StatusSeeOther || !strings.HasSuffix(r.location, "?emailed=1") {
		t.Fatalf("posting: %d %q %s", r.status, r.location, r.body)
	}
	if got := s.delivered(1); !sameSet(got, eagleFam) {
		t.Errorf("reached %v, want only %v", got, eagleFam)
	}
	if n := s.count(`SELECT count(*) FROM content_pages WHERE unit_id = $1 AND sub_group_id = $2 AND title = 'Loud update' AND status = 'published'`, s.unitID, s.eagle); n != 1 {
		t.Errorf("the update isn't on the page")
	}
	if page := s.get(sm, "/groups/"+s.eagle+"?emailed=1"); !strings.Contains(page.body, "Posted, and emailed") {
		t.Errorf("the group page doesn't say it was emailed")
	}
}

// If the email can't be sent, the update must not be posted either: a refusal
// that left the update on the page and nobody emailed would look, to the person
// who ticked the box, exactly like it worked.
func TestAnUpdateThatCannotBeEmailedIsNotPostedEither(t *testing.T) {
	s := newEmailSite(t, true)
	// A den leader-style editor whose role has had email taken away: they can
	// still post updates, but may not email.
	sm := s.leader("Sam", "scoutmaster", nil)
	s.exec(`INSERT INTO role_capability_overrides (unit_id, role_slug, capabilities) VALUES ($1, 'scoutmaster', '{edit_content}')`, s.unitID)
	s.household("Eaglefam", &s.eagle)

	r := s.post(sm, "/admin/groups/"+s.eagle+"/news", form("title", "Nope", "body", "x", "also_email", "1"))
	if r.status != http.StatusForbidden {
		t.Errorf("status %d, want 403", r.status)
	}
	if n := s.count(`SELECT count(*) FROM content_pages WHERE unit_id = $1 AND sub_group_id IS NOT NULL`, s.unitID); n != 0 {
		t.Errorf("a refused 'also email' still posted the update (%d)", n)
	}
	s.nothingSent("a role without the email capability")

	// Without the box ticked the same person posts as before.
	if r := s.post(sm, "/admin/groups/"+s.eagle+"/news", form("title", "Fine", "body", "x")); r.status != http.StatusSeeOther {
		t.Errorf("posting without emailing: status %d, want 303", r.status)
	}
}

func TestAnUpdateToAnEmptyGroupIsNotPostedWhenEmailWasAsked(t *testing.T) {
	s := newEmailSite(t, true)
	sm := s.leader("Sam", "scoutmaster", nil)

	r := s.post(sm, "/admin/groups/"+s.eagle+"/news", form("title", "To nobody", "body", "x", "also_email", "1"))
	if r.status != http.StatusBadRequest {
		t.Errorf("status %d, want 400", r.status)
	}
	if n := s.count(`SELECT count(*) FROM content_pages WHERE unit_id = $1 AND sub_group_id IS NOT NULL`, s.unitID); n != 0 {
		t.Errorf("the update was posted although the email it was posted with would reach nobody")
	}
}

// --- the history ------------------------------------------------------------

func TestEmailsAreListedWithTheNewslettersAndOnTheGroupPage(t *testing.T) {
	s := newEmailSite(t, true)
	sm := s.leader("Sam", "scoutmaster", nil)
	s.household("Eaglefam", &s.eagle)
	id := s.newsPost("Campout news", "x", true)

	if r := s.post(sm, "/admin/news/"+id+"/email", form("audience", "unit")); r.status != http.StatusSeeOther {
		t.Fatalf("news: %d %s", r.status, r.body)
	}
	if r := s.post(sm, "/groups/"+s.eagle+"/email", form("subject", "Meet at 7", "body", "x")); r.status != http.StatusSeeOther {
		t.Fatalf("group: %d %s", r.status, r.body)
	}
	s.delivered(3)

	list := s.get(sm, "/admin/newsletters")
	for _, want := range []string{"Campout news", "News post emailed to", "Eagle Patrol: Meet at 7", "Update to Eagle Patrol"} {
		if !strings.Contains(list.body, want) {
			t.Errorf("the newsletters list lacks %q", want)
		}
	}
	if page := s.get(sm, "/groups/"+s.eagle+"/email"); !strings.Contains(page.body, "Eagle Patrol: Meet at 7") {
		t.Errorf("the composer's history doesn't list what was sent")
	}
	if page := s.get(sm, "/admin/news/"+id+"/edit"); !strings.Contains(page.body, "Already emailed") || !strings.Contains(page.body, `name="again"`) {
		t.Errorf("the post's page doesn't show it was emailed or offer a confirmed resend")
	}
}
