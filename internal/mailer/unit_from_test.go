package mailer_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/db"
	"github.com/47-yonkers/scout-site/internal/mailer"
	"github.com/47-yonkers/scout-site/internal/mailer/mailertest"
	"github.com/47-yonkers/scout-site/internal/settings"
)

// Which address a message goes out as is the whole of this feature, so these
// look at the From header a recipient would read, on mail that really went
// through an SMTP conversation, not at a config struct.

var (
	unitRun     = time.Now().UnixNano()
	unitCounter atomic.Int64
)

func unitTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping mailer integration tests")
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

func newUnit(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	sfx := fmt.Sprintf("%d-%d", unitRun, unitCounter.Add(1))
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO units (slug, name, unit_type, hostname) VALUES ($1, $2, 'troop', $3) RETURNING id::text`,
		"mf-"+sfx, name, "mf-"+sfx+".test.invalid").Scan(&id); err != nil {
		t.Fatalf("creating unit: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM units WHERE id = $1`, id) })
	return id
}

func setUnitFrom(t *testing.T, pool *pgxpool.Pool, unitID, from string) {
	t.Helper()
	var actor string
	// updated_by is a member id; any existing one will do for a test, and a
	// throwaway family keeps the test self-contained.
	var fam string
	if err := pool.QueryRow(context.Background(), `INSERT INTO families (name) VALUES ('mailfrom') RETURNING id::text`).Scan(&fam); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM families WHERE id = $1`, fam) })
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, 'A', 'B', 'adult') RETURNING id::text`, fam).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetUnitText(context.Background(), pool, unitID, settings.MailFrom, from, actor); err != nil {
		t.Fatalf("setting the unit's From: %v", err)
	}
}

func lastFrom(t *testing.T, sink *mailertest.Sink) (header, envelope string) {
	t.Helper()
	if !sink.WaitFor(1, 5*time.Second) {
		t.Fatal("no message arrived")
	}
	msgs := sink.Messages()
	m := msgs[len(msgs)-1]
	return m.HeaderFrom(), m.From
}

func TestAUnitSendsFromItsOwnAddress(t *testing.T) {
	pool := unitTestPool(t)
	troop := newUnit(t, pool, "Troop MF")
	pack := newUnit(t, pool, "Pack MF")
	setUnitFrom(t, pool, troop, "Troop 47 <troop@example.org>")
	setUnitFrom(t, pool, pack, "Pack 47 <pack@example.org>")

	sink := mailertest.New(t)
	m := mailer.New(sink.Config(), pool)
	ctx := context.Background()

	for _, c := range []struct {
		name     string
		ctx      context.Context
		wantHdr  string
		wantAddr string
	}{
		{"the Troop", mailer.WithUnit(ctx, troop), "Troop 47 <troop@example.org>", "troop@example.org"},
		{"the Pack", mailer.WithUnit(ctx, pack), "Pack 47 <pack@example.org>", "pack@example.org"},
	} {
		n := len(sink.Messages())
		if err := m.Send(c.ctx, "family@test.invalid", "Hello", "body"); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !sink.WaitFor(n+1, 5*time.Second) {
			t.Fatalf("%s: nothing arrived", c.name)
		}
		got := sink.Messages()[n]
		if got.HeaderFrom() != c.wantHdr {
			t.Errorf("%s: From header = %q, want %q", c.name, got.HeaderFrom(), c.wantHdr)
		}
		if got.From != c.wantAddr {
			t.Errorf("%s: envelope sender = %q, want %q", c.name, got.From, c.wantAddr)
		}
	}
}

// A unit that sets nothing sends exactly as it always did: the site-wide From.
// This is what makes the setting safe to add to a running site — nobody who
// does not touch it sees any change.
func TestAUnitWithNoAddressOfItsOwnSendsAsTheSiteDoes(t *testing.T) {
	pool := unitTestPool(t)
	plain := newUnit(t, pool, "Plain Troop")
	sink := mailertest.New(t)
	cfg := sink.Config()
	m := mailer.New(cfg, pool)

	for name, ctx := range map[string]context.Context{
		"a unit with no setting": mailer.WithUnit(context.Background(), plain),
		"no unit at all":         context.Background(),
	} {
		n := len(sink.Messages())
		if err := m.Send(ctx, "family@test.invalid", "Hello", "body"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !sink.WaitFor(n+1, 5*time.Second) {
			t.Fatalf("%s: nothing arrived", name)
		}
		if got := sink.Messages()[n].HeaderFrom(); got != "Test Unit <noreply@test.invalid>" {
			t.Errorf("%s: From = %q, want the site-wide address", name, got)
		}
	}
}

// Three places can name a From: the environment, the site-wide field on the
// Settings page, and now the unit's own. The nearer to the unit wins.
func TestTheUnitsAddressBeatsTheSiteWideOneWhichBeatsTheEnvironment(t *testing.T) {
	pool := unitTestPool(t)
	unit := newUnit(t, pool, "Precedence Troop")
	sink := mailertest.New(t)
	m := mailer.New(sink.Config(), pool) // environment: Test Unit <noreply@test.invalid>
	ctx := mailer.WithUnit(context.Background(), unit)

	if got := m.From(ctx); got != "Test Unit <noreply@test.invalid>" {
		t.Errorf("environment only: From = %q", got)
	}

	// Site-wide override, saved on the Settings page.
	var fam, actor string
	if err := pool.QueryRow(context.Background(), `INSERT INTO families (name) VALUES ('prec') RETURNING id::text`).Scan(&fam); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM families WHERE id = $1`, fam) })
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, 'A', 'B', 'adult') RETURNING id::text`, fam).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetText(context.Background(), pool, settings.SMTPFrom, "Site <site@example.org>", actor); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = settings.SetText(context.Background(), pool, settings.SMTPFrom, "", actor) })
	if got := m.From(ctx); got != "Site <site@example.org>" {
		t.Errorf("site-wide override: From = %q", got)
	}

	setUnitFrom(t, pool, unit, "Unit <unit@example.org>")
	if got := m.From(ctx); got != "Unit <unit@example.org>" {
		t.Errorf("unit's own: From = %q — the unit's address must win", got)
	}
	// Without the unit on the context, the unit's address is not used at all.
	if got := m.From(context.Background()); got != "Site <site@example.org>" {
		t.Errorf("no unit: From = %q, want the site-wide one", got)
	}

	// Clearing it puts the unit back on the site-wide address.
	setUnitFrom(t, pool, unit, "")
	if got := m.From(ctx); got != "Site <site@example.org>" {
		t.Errorf("after clearing: From = %q", got)
	}
}

// With Fastmail's JMAP API, whether mail can be sent at all depends on there
// being a From. If the only From a site has is a unit's own, that unit can send
// and a unit-less question about the site cannot — which is why every "can mail
// go out?" check names its unit.
func TestAUnitsOwnAddressIsEnoughToSendWithJMAP(t *testing.T) {
	pool := unitTestPool(t)
	unit := newUnit(t, pool, "JMAP Troop")
	m := mailer.New(mailer.Config{Provider: mailer.ProviderFastmailJMAP, APIToken: "tok"}, pool) // no site-wide From

	ctx := mailer.WithUnit(context.Background(), unit)
	if m.Enabled(ctx) || m.Enabled(context.Background()) {
		t.Fatal("JMAP with no From anywhere reports it can send")
	}
	setUnitFrom(t, pool, unit, "JMAP Troop <troop@example.org>")
	if !m.Enabled(ctx) {
		t.Error("a unit with its own From can't send, though that is all JMAP needs")
	}
	if m.Enabled(context.Background()) {
		t.Error("a send that names no unit reports it can send, using another unit's address")
	}
}

// Whatever the admin typed becomes a header in every message a unit sends, so
// nothing that could add another header may be stored.
func TestAUnitAddressThatIsNotOneIsNotStored(t *testing.T) {
	pool := unitTestPool(t)
	unit := newUnit(t, pool, "Validation Troop")
	var fam, actor string
	if err := pool.QueryRow(context.Background(), `INSERT INTO families (name) VALUES ('val') RETURNING id::text`).Scan(&fam); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM families WHERE id = $1`, fam) })
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO members (family_id, first_name, last_name, member_type) VALUES ($1, 'A', 'B', 'adult') RETURNING id::text`, fam).Scan(&actor); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		"not an address",
		"troop@",
		"@example.org",
		"troop@example.org\r\nBcc: someone@else.test",
		"Troop <troop@example.org>, Pack <pack@example.org>",
		"<>",
	} {
		err := settings.SetUnitText(context.Background(), pool, unit, settings.MailFrom, bad, actor)
		if err != settings.ErrInvalidMailFrom {
			t.Errorf("%q: error = %v, want ErrInvalidMailFrom", bad, err)
		}
	}
	if got, _ := settings.GetUnitText(context.Background(), pool, unit, settings.MailFrom); got != "" {
		t.Errorf("a refused address was stored anyway: %q", got)
	}

	for _, good := range []string{
		"troop@example.org",
		"Troop 47 <troop@example.org>",
		`"Troop 47, Yonkers" <troop@example.org>`,
	} {
		if err := settings.SetUnitText(context.Background(), pool, unit, settings.MailFrom, good, actor); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
	// Blank clears it, rather than being refused.
	if err := settings.SetUnitText(context.Background(), pool, unit, settings.MailFrom, "   ", actor); err != nil {
		t.Errorf("blank refused: %v", err)
	}
	if got, _ := settings.GetUnitText(context.Background(), pool, unit, settings.MailFrom); got != "" {
		t.Errorf("blank didn't clear it: %q", got)
	}
}
