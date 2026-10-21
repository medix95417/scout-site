package newsletter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
	"github.com/47-yonkers/scout-site/internal/mailer"
	"github.com/47-yonkers/scout-site/internal/mailer/mailertest"
)

// Emailing a post, or an update to a den or patrol, against a real database:
// who a message reaches, that every refusal happens before anything is
// written, and that what is sent is what the record says.

var (
	runID   = time.Now().UnixNano()
	counter atomic.Int64
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping newsletter integration tests")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrating test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type world struct {
	t    *testing.T
	pool *pgxpool.Pool
	unit string
	sfx  string

	eagle, wolf string // two patrols of this unit
	actor       string // an adult who sends things

	famA, famB, famC string
	// member ids
	a1, y1, y2 string // family A: an adult, a Scout in Eagle, a Scout in Wolf
	bScout     string // family B: a Scout in Wolf
}

func (w *world) exec(q string, args ...any) {
	w.t.Helper()
	if _, err := w.pool.Exec(context.Background(), q, args...); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
}

func (w *world) one(q string, args ...any) string {
	w.t.Helper()
	var s string
	if err := w.pool.QueryRow(context.Background(), q, args...).Scan(&s); err != nil {
		w.t.Fatalf("%s: %v", q, err)
	}
	return s
}

func (w *world) email(local string) string { return fmt.Sprintf("%s-%s@test.invalid", local, w.sfx) }

// newWorld builds one troop with two patrols and three families:
//
//	family A: an adult, a Scout in Eagle, a Scout in Wolf — each with their
//	          own login as well as the family's shared one
//	family B: a Scout in Wolf, shared login only
//	family C: on the roster but in no patrol, shared login only
func newWorld(t *testing.T) *world {
	t.Helper()
	pool := testPool(t)
	w := &world{t: t, pool: pool, sfx: fmt.Sprintf("%d-%d", runID, counter.Add(1))}

	w.unit = w.one(`INSERT INTO units (slug, name, unit_type, hostname) VALUES ($1, $2, 'troop', $3) RETURNING id::text`,
		"nl-"+w.sfx, "Troop "+w.sfx, "nl-"+w.sfx+".test.invalid")
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM units WHERE id = $1`, w.unit) })

	w.eagle = w.one(`INSERT INTO sub_groups (unit_id, name, sub_group_type) VALUES ($1, 'Eagle Patrol', 'patrol') RETURNING id::text`, w.unit)
	w.wolf = w.one(`INSERT INTO sub_groups (unit_id, name, sub_group_type) VALUES ($1, 'Wolf Patrol', 'patrol') RETURNING id::text`, w.unit)

	family := func(name string) string {
		id := w.one(`INSERT INTO families (name) VALUES ($1) RETURNING id::text`, name+" "+w.sfx)
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM families WHERE id = $1`, id) })
		return id
	}
	member := func(fam, first, kind string) string {
		return w.one(`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, $2, 'Test', $3) RETURNING id::text`, fam, first, kind)
	}
	login := func(fam string, memberID *string, local string) {
		w.exec(`INSERT INTO users (family_id, member_id, email, password_hash) VALUES ($1, $2, $3, 'unused')`, fam, memberID, w.email(local))
	}
	role := func(memberID string, group *string, role string) {
		w.exec(`INSERT INTO role_assignments (member_id, unit_id, sub_group_id, role) VALUES ($1, $2, $3, $4)`, memberID, w.unit, group, role)
	}

	w.famA, w.famB, w.famC = family("A"), family("B"), family("C")

	w.a1 = member(w.famA, "Adult", "adult")
	w.y1 = member(w.famA, "Eaglet", "youth")
	w.y2 = member(w.famA, "Wolfling", "youth")
	w.actor = w.a1
	login(w.famA, nil, "a-shared")
	login(w.famA, &w.a1, "a-adult")
	login(w.famA, &w.y1, "a-eagle-scout")
	login(w.famA, &w.y2, "a-wolf-scout")
	role(w.a1, nil, "parent")
	role(w.y1, &w.eagle, "scout")
	role(w.y2, &w.wolf, "scout")

	w.bScout = member(w.famB, "Bee", "youth")
	login(w.famB, nil, "b-shared")
	role(w.bScout, &w.wolf, "scout")

	cMember := member(w.famC, "Cee", "youth")
	login(w.famC, nil, "c-shared")
	role(cMember, nil, "scout")
	return w
}

func (w *world) emails(local ...string) []string {
	out := make([]string, len(local))
	for i, l := range local {
		out[i] = w.email(l)
	}
	sort.Strings(out)
	return out
}

func equalSets(a, b []string) bool {
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, "|") == strings.Join(b, "|")
}

func enabledMailer(sink *mailertest.Sink) *mailer.Mailer { return mailer.New(sink.Config(), nil) }

func TestAGroupEmailReachesTheFamiliesOfThatGroupOnly(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		groups []string
		want   []string
	}{
		// The shared login and the adult's own are the household's; the Scout in
		// Eagle gets their own. The Wolf-patrol sibling does NOT — a message to
		// the Eagle Patrol must not land in a younger brother's own inbox.
		{"Eagle", []string{w.eagle}, w.emails("a-shared", "a-adult", "a-eagle-scout")},
		{"Wolf", []string{w.wolf}, w.emails("a-shared", "a-adult", "a-wolf-scout", "b-shared")},
		{"both", []string{w.eagle, w.wolf}, w.emails("a-shared", "a-adult", "a-eagle-scout", "a-wolf-scout", "b-shared")},
		{"none", nil, nil},
	}
	for _, c := range cases {
		got, err := RecipientEmailsForGroups(ctx, w.pool, w.unit, c.groups)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !equalSets(got, c.want) {
			t.Errorf("%s: reached %v, want %v", c.name, got, c.want)
		}
	}

	// A family in no patrol is in nobody's patrol email — and in the whole
	// unit's, which is unchanged.
	unit, err := RecipientEmailsForUnit(ctx, w.pool, w.unit)
	if err != nil {
		t.Fatal(err)
	}
	if !equalSets(unit, w.emails("a-shared", "a-adult", "a-eagle-scout", "a-wolf-scout", "b-shared", "c-shared")) {
		t.Errorf("the whole-unit reach changed: %v", unit)
	}
}

func TestOnlyActiveMembersAndRunningGroupsAreReached(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	// The Eagle Scout leaves: nobody active is left in Eagle, so family A is
	// no longer in the Eagle Patrol's email at all.
	w.exec(`UPDATE members SET active = false WHERE id = $1`, w.y1)
	got, err := RecipientEmailsForGroups(ctx, w.pool, w.unit, []string{w.eagle})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a patrol whose only member left still reaches %v", got)
	}

	// A patrol that has been folded is not somewhere to send.
	w.exec(`UPDATE sub_groups SET active = false WHERE id = $1`, w.wolf)
	got, err = RecipientEmailsForGroups(ctx, w.pool, w.unit, []string{w.wolf})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a folded patrol still reaches %v", got)
	}
}

func TestAnotherUnitsGroupReachesNobody(t *testing.T) {
	a := newWorld(t)
	b := newWorld(t)
	got, err := RecipientEmailsForGroups(context.Background(), a.pool, a.unit, []string{b.eagle})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a group of another unit reached %v through this one", got)
	}
}

func (w *world) post(title string) string {
	return w.one(`INSERT INTO content_pages (unit_id, slug, title, body, page_type, visibility, status, created_by)
		VALUES ($1, $2, $3, 'body', 'post', 'members', 'published', $4) RETURNING id::text`,
		w.unit, fmt.Sprintf("p-%s-%d", w.sfx, counter.Add(1)), title, w.actor)
}

func (w *world) newsletterCount() int {
	var n int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM newsletters WHERE unit_id = $1`, w.unit).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// Every way a start can be refused is refused before anything is written, so
// a refusal never leaves behind a record that looks like unfinished business.
func TestARefusedStartLeavesNoRecord(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	sink := mailertest.New(t)
	m := enabledMailer(sink)
	empty := w.one(`INSERT INTO sub_groups (unit_id, name, sub_group_type) VALUES ($1, 'Empty Patrol', 'patrol') RETURNING id::text`, w.unit)
	other := newWorld(t)

	cases := []struct {
		name string
		m    *mailer.Mailer
		in   PostEmail
		want error // nil means "any error"
	}{
		{"mail is not configured", mailer.New(mailer.Config{}, nil),
			PostEmail{UnitID: w.unit, Kind: KindNews, Subject: "s", Body: "b", ActorID: w.actor}, nil},
		{"a group update to no group", m,
			PostEmail{UnitID: w.unit, Kind: KindGroupUpdate, Subject: "s", Body: "b", ActorID: w.actor}, ErrNoAudience},
		{"a group nobody is in", m,
			PostEmail{UnitID: w.unit, Kind: KindGroupUpdate, Subject: "s", Body: "b", ActorID: w.actor, Groups: []string{empty}}, ErrNoRecipients},
		{"another unit's group", m,
			PostEmail{UnitID: w.unit, Kind: KindGroupUpdate, Subject: "s", Body: "b", ActorID: w.actor, Groups: []string{other.eagle}}, ErrBadAudience},
		{"a group that is not a group", m,
			PostEmail{UnitID: w.unit, Kind: KindNews, Subject: "s", Body: "b", ActorID: w.actor, Groups: []string{w.eagle, "00000000-0000-0000-0000-000000000000"}}, ErrBadAudience},
		{"a kind that is not made from a post", m,
			PostEmail{UnitID: w.unit, Kind: KindNewsletter, Subject: "s", Body: "b", ActorID: w.actor}, nil},
	}
	for _, c := range cases {
		_, err := StartPostEmail(ctx, w.pool, c.m, c.in)
		if err == nil {
			t.Errorf("%s: was started", c.name)
			continue
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
		}
	}
	if n := w.newsletterCount(); n != 0 {
		t.Errorf("refusals left %d records behind", n)
	}
}

func TestStartingAGroupUpdateRecordsWhatWasSentAndToWhom(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	sink := mailertest.New(t)
	postID := w.post("Campout")

	n, err := StartPostEmail(ctx, w.pool, enabledMailer(sink), PostEmail{
		UnitID: w.unit, Kind: KindGroupUpdate, PostID: &postID, Subject: "Eagle Patrol: Campout",
		Body:    `<p>Bring a <strong>tent</strong>.</p><script>alert(1)</script>`,
		ActorID: w.actor,
		// Named twice: the audience is the set of groups, not the list typed.
		Groups: []string{w.eagle, w.eagle, w.wolf},
	})
	if err != nil {
		t.Fatalf("StartPostEmail: %v", err)
	}

	if n.Status != "sending" || n.Kind != KindGroupUpdate || n.Audience != AudienceGroups {
		t.Errorf("record = status %q kind %q audience %q", n.Status, n.Kind, n.Audience)
	}
	if n.PostID == nil || *n.PostID != postID {
		t.Errorf("post id = %v, want %s", n.PostID, postID)
	}
	if strings.Contains(n.Body, "script") || !strings.Contains(n.Body, "<strong>tent</strong>") {
		t.Errorf("body was not sanitized and kept: %q", n.Body)
	}
	ids, err := GroupIDsFor(ctx, w.pool, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !equalSets(ids, []string{w.eagle, w.wolf}) {
		t.Errorf("audience groups = %v, want Eagle and Wolf once each", ids)
	}
	names, _ := GroupNamesFor(ctx, w.pool, n.ID)
	if strings.Join(names, ",") != "Eagle Patrol,Wolf Patrol" {
		t.Errorf("audience names = %v", names)
	}
}

func TestStartingANewsEmailToTheWholeUnitNamesNoGroups(t *testing.T) {
	w := newWorld(t)
	sink := mailertest.New(t)
	n, err := StartPostEmail(context.Background(), w.pool, enabledMailer(sink), PostEmail{
		UnitID: w.unit, Kind: KindNews, Subject: "Summer camp", Body: "<p>Sign up</p>", ActorID: w.actor,
	})
	if err != nil {
		t.Fatalf("StartPostEmail: %v", err)
	}
	if n.Audience != AudienceUnit {
		t.Errorf("audience = %q, want the whole unit", n.Audience)
	}
	if ids, _ := GroupIDsFor(context.Background(), w.pool, n.ID); len(ids) != 0 {
		t.Errorf("a whole-unit email lists groups %v", ids)
	}
	got, err := RecipientEmailsFor(context.Background(), w.pool, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 {
		t.Errorf("a whole-unit email reaches %d addresses, want all 6: %v", len(got), got)
	}
}

// If the groups a record was addressed to vanish between its being started
// and its being sent, it reaches nobody. It must never fall back to the whole
// unit, which is what "no audience rows" would otherwise mean.
func TestAGroupEmailWhoseGroupsVanishedReachesNobodyNotEverybody(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	sink := mailertest.New(t)
	n, err := StartPostEmail(ctx, w.pool, enabledMailer(sink), PostEmail{
		UnitID: w.unit, Kind: KindGroupUpdate, Subject: "s", Body: "b", ActorID: w.actor, Groups: []string{w.eagle},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.exec(`DELETE FROM sub_groups WHERE id = $1`, w.eagle) // cascades the audience row away

	got, err := RecipientEmailsFor(ctx, w.pool, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a record whose group was deleted reached %v — it fell back to a wider audience", got)
	}
}

// End to end: start it, send it, and see who actually got what.
func TestAGroupUpdateIsDeliveredToExactlyItsAudience(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	sink := mailertest.New(t)
	m := enabledMailer(sink)

	n, err := StartPostEmail(ctx, w.pool, m, PostEmail{
		UnitID: w.unit, Kind: KindGroupUpdate, Subject: "Eagle Patrol: Campout",
		Body: "<p>Bring a tent.</p>", ActorID: w.actor, Groups: []string{w.eagle},
	})
	if err != nil {
		t.Fatalf("StartPostEmail: %v", err)
	}
	res := SendNow(ctx, w.pool, m, n, w.actor)

	want := w.emails("a-shared", "a-adult", "a-eagle-scout")
	if res.Sent != len(want) || res.Failed != 0 {
		t.Errorf("sent %d failed %d, want %d and none", res.Sent, res.Failed, len(want))
	}
	if got := sink.Recipients(); !equalSets(got, want) {
		t.Errorf("the mail server received mail for %v, want %v", got, want)
	}
	for _, msg := range sink.Messages() {
		if msg.Subject() != "Eagle Patrol: Campout" || !strings.Contains(msg.Body(), "Bring a tent.") {
			t.Errorf("a message was %q / %q", msg.Subject(), msg.Body())
		}
		if len(msg.To) != 1 {
			t.Errorf("a message was addressed to %v — each family must get its own, not see the others", msg.To)
		}
	}

	var status string
	var count int
	if err := w.pool.QueryRow(ctx, `SELECT status::text, recipient_count FROM newsletters WHERE id = $1`, n.ID).Scan(&status, &count); err != nil {
		t.Fatal(err)
	}
	if status != "sent" || count != len(want) {
		t.Errorf("record says %q to %d, want sent to %d", status, count, len(want))
	}
	recs, err := RecipientsFor(ctx, w.pool, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != len(want) {
		t.Errorf("%d recipients recorded, want %d", len(recs), len(want))
	}
}

func TestHistoryByPostAndByGroup(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	sink := mailertest.New(t)
	m := enabledMailer(sink)
	postID := w.post("Campout")

	toEagle, err := StartPostEmail(ctx, w.pool, m, PostEmail{UnitID: w.unit, Kind: KindGroupUpdate, PostID: &postID, Subject: "e", Body: "b", ActorID: w.actor, Groups: []string{w.eagle}})
	if err != nil {
		t.Fatal(err)
	}
	toWolf, err := StartPostEmail(ctx, w.pool, m, PostEmail{UnitID: w.unit, Kind: KindGroupUpdate, Subject: "w", Body: "b", ActorID: w.actor, Groups: []string{w.wolf}})
	if err != nil {
		t.Fatal(err)
	}
	toAll, err := StartPostEmail(ctx, w.pool, m, PostEmail{UnitID: w.unit, Kind: KindNews, PostID: &postID, Subject: "a", Body: "b", ActorID: w.actor})
	if err != nil {
		t.Fatal(err)
	}

	byPost, err := ListForPost(ctx, w.pool, w.unit, postID)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(ns []Newsletter) []string {
		var out []string
		for _, n := range ns {
			out = append(out, n.ID)
		}
		return out
	}
	if !equalSets(ids(byPost), []string{toEagle.ID, toAll.ID}) {
		t.Errorf("emails made from the post = %v, want the Eagle one and the whole-unit one", ids(byPost))
	}

	eagle, err := ListForGroup(ctx, w.pool, w.unit, w.eagle, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !equalSets(ids(eagle), []string{toEagle.ID}) {
		t.Errorf("emails to Eagle = %v, want only the one addressed to it (not Wolf's, not the whole-unit news)", ids(eagle))
	}
	wolf, _ := ListForGroup(ctx, w.pool, w.unit, w.wolf, 10)
	if !equalSets(ids(wolf), []string{toWolf.ID}) {
		t.Errorf("emails to Wolf = %v", ids(wolf))
	}
	// Another unit sees none of it.
	other := newWorld(t)
	if got, _ := ListForGroup(ctx, w.pool, other.unit, w.eagle, 10); len(got) != 0 {
		t.Errorf("another unit's lookup returned %d records", len(got))
	}
}
