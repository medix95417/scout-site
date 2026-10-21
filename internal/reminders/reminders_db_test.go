package reminders

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
	"github.com/47-yonkers/scout-site/internal/mailer"
	"github.com/47-yonkers/scout-site/internal/mailer/mailertest"
	"github.com/47-yonkers/scout-site/internal/settings"
)

// A reminder is sent as the unit its event belongs to. Each unit's reminders go
// out from that unit's address, so a Pack family is not reminded by the Troop.

var (
	runID   = time.Now().UnixNano()
	counter atomic.Int64
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping reminders integration tests")
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

type unitFixture struct {
	unitID string
	email  string // the family's login, which the reminder goes to
	actor  string
}

func (f unitFixture) setFrom(t *testing.T, pool *pgxpool.Pool, from string) {
	t.Helper()
	if err := settings.SetUnitText(context.Background(), pool, f.unitID, settings.MailFrom, from, f.actor); err != nil {
		t.Fatalf("setting From: %v", err)
	}
}

// newUnitWithAReminderDue makes a unit with one published event starting in two
// hours and a family that has RSVP'd yes to it — so a reminder is due.
func newUnitWithAReminderDue(t *testing.T, pool *pgxpool.Pool, label string) unitFixture {
	t.Helper()
	ctx := context.Background()
	sfx := fmt.Sprintf("%d-%d", runID, counter.Add(1))
	exec := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, q, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return id
	}
	f := unitFixture{email: fmt.Sprintf("%s-%s@test.invalid", strings.ToLower(label), sfx)}
	f.unitID = exec(`INSERT INTO units (slug, name, unit_type, hostname) VALUES ($1, $2, 'troop', $3) RETURNING id::text`,
		"rm-"+sfx, label+" "+sfx, "rm-"+sfx+".test.invalid")
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM units WHERE id = $1`, f.unitID) })

	fam := exec(`INSERT INTO families (name) VALUES ($1) RETURNING id::text`, label+" fam "+sfx)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM families WHERE id = $1`, fam) })
	f.actor = exec(`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, $2, 'Test', 'adult') RETURNING id::text`, fam, label)
	if _, err := pool.Exec(ctx, `INSERT INTO users (family_id, member_id, email, password_hash) VALUES ($1, NULL, $2, 'unused')`, fam, f.email); err != nil {
		t.Fatalf("creating login: %v", err)
	}
	start := time.Now().Add(2 * time.Hour)
	event := exec(`INSERT INTO events (unit_id, title, starts_at, ends_at, visibility, status, created_by)
		VALUES ($1, $2, $3, $4, 'members', 'published', $5) RETURNING id::text`,
		f.unitID, label+" campout", start, start.Add(time.Hour), f.actor)
	if _, err := pool.Exec(ctx, `INSERT INTO rsvps (event_id, member_id, response) VALUES ($1, $2, 'yes')`, event, f.actor); err != nil {
		t.Fatalf("creating RSVP: %v", err)
	}
	return f
}

func fromFor(t *testing.T, sink *mailertest.Sink, to string) string {
	t.Helper()
	for _, m := range sink.Messages() {
		for _, r := range m.To {
			if r == to {
				return m.HeaderFrom()
			}
		}
	}
	t.Fatalf("no reminder reached %s (the server saw %v)", to, sink.Recipients())
	return ""
}

func TestEachUnitsRemindersGoOutFromItsOwnAddress(t *testing.T) {
	pool := testPool(t)
	troop := newUnitWithAReminderDue(t, pool, "Troop")
	pack := newUnitWithAReminderDue(t, pool, "Pack")
	troop.setFrom(t, pool, "Troop 47 <troop@example.org>")
	pack.setFrom(t, pool, "Pack 47 <pack@example.org>")

	sink := mailertest.New(t)
	m := mailer.New(sink.Config(), pool)
	res, err := Send(context.Background(), pool, m, 24*time.Hour)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Sent < 2 {
		t.Fatalf("sent %d reminders, want at least the two due here (failed %d)", res.Sent, res.Failed)
	}
	if got := fromFor(t, sink, troop.email); got != "Troop 47 <troop@example.org>" {
		t.Errorf("the Troop's reminder went out as %q", got)
	}
	if got := fromFor(t, sink, pack.email); got != "Pack 47 <pack@example.org>" {
		t.Errorf("the Pack's reminder went out as %q", got)
	}
}

func TestAUnitWithNoAddressRemindsFromTheSiteWideOne(t *testing.T) {
	pool := testPool(t)
	plain := newUnitWithAReminderDue(t, pool, "Plain")

	sink := mailertest.New(t)
	m := mailer.New(sink.Config(), pool)
	if _, err := Send(context.Background(), pool, m, 24*time.Hour); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := fromFor(t, sink, plain.email); got != "Test Unit <noreply@test.invalid>" {
		t.Errorf("a unit with no address of its own reminded as %q, want the site-wide one", got)
	}
}

// With no mail configured at all, reminders say so and send nothing — and are
// not recorded as sent, so they go out on the first run after it is set up. The
// check is made per unit now (a unit's own From address can be what makes it
// configured), and this is the case where that finds the answer is no.
func TestUnconfiguredMailSendsNothingAndSaysSo(t *testing.T) {
	pool := testPool(t)
	due := newUnitWithAReminderDue(t, pool, "Unconfigured")

	m := mailer.New(mailer.Config{}, pool) // nothing configured anywhere
	res, err := Send(context.Background(), pool, m, 24*time.Hour)
	if err == nil {
		t.Fatal("Send with no mail configured returned no error")
	}
	if res.Sent != 0 {
		t.Errorf("sent %d reminders with no mail configured", res.Sent)
	}
	// Not recorded as sent, so it goes out once mail is set up.
	var recorded int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM event_reminders_sent
		WHERE event_id IN (SELECT id FROM events WHERE unit_id = $1)`, due.unitID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Errorf("a reminder that was never sent was recorded as sent (%d)", recorded)
	}
}
