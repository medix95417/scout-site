package prospect

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Campaign and consent behaviour against a real database.
//
// The rules that matter here are all SQL: who a campaign reaches, that an
// opted-out family is excluded, that one address doesn't get two copies,
// that a sent campaign is frozen, and that one unit can't touch another's.
// Every one of them fails silently — the send succeeds and the wrong
// people get email — so none of them can be left to a reading of the code.
//
// Skipped without TEST_DATABASE_URL, so the default test run stays a pure
// unit run.
type env struct {
	pool   *pgxpool.Pool
	unitID string
	actor  string
}

// newEnv reuses the package's existing TEST_DATABASE_URL harness (see
// prospect_test.go) rather than standing up a second one, and adds the
// member every audit entry needs as its actor.
func newEnv(t *testing.T, name string) env {
	t.Helper()
	ctx := context.Background()
	pool := testPool(t)
	unitID := newUnit(t, pool)

	var familyID, actor string
	if err := pool.QueryRow(ctx,
		`INSERT INTO families (name) VALUES ($1) RETURNING id::text`, name).Scan(&familyID); err != nil {
		t.Fatalf("creating test family: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO members (family_id, first_name, last_name, member_type)
		VALUES ($1, 'Test', 'Leader', 'adult') RETURNING id::text
	`, familyID).Scan(&actor); err != nil {
		t.Fatalf("creating test member: %v", err)
	}
	return env{pool: pool, unitID: unitID, actor: actor}
}

func (e env) prospect(t *testing.T, email, status string) Prospect {
	t.Helper()
	ctx := context.Background()
	p, err := Create(ctx, e.pool, New{
		UnitID: e.unitID, ParentName: "Parent", ParentEmail: email, ChildName: "Child",
	})
	if err != nil {
		t.Fatalf("creating prospect %s: %v", email, err)
	}
	if status != StatusNew {
		if _, err := UpdateStatus(ctx, e.pool, e.unitID, p.ID, status, "", e.actor); err != nil {
			t.Fatalf("setting status: %v", err)
		}
		p.Status = status
	}
	return p
}

func emailsOf(rs []Recipient) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, strings.ToLower(r.Email))
	}
	return out
}

func count(list []string, want string) int {
	n := 0
	for _, v := range list {
		if v == want {
			n++
		}
	}
	return n
}

func TestRecipientsExcludeOptOutsAndOtherStatuses(t *testing.T) {
	e := newEnv(t, "campaudience")
	ctx := context.Background()

	a := e.prospect(t, "a@example.com", StatusNew)
	e.prospect(t, "b@example.com", StatusContacted)
	e.prospect(t, "c@example.com", StatusJoined)
	// The same family enquiring twice, differing only in case.
	e.prospect(t, "A@Example.com", StatusNew)

	got, err := RecipientsForStatuses(ctx, e.pool, e.unitID, []string{StatusNew, StatusContacted})
	if err != nil {
		t.Fatal(err)
	}
	list := emailsOf(got)
	if count(list, "c@example.com") != 0 {
		t.Error("a family already marked as joined was included")
	}
	if count(list, "a@example.com") != 1 {
		t.Errorf("a@example.com appears %d times — that family would get %d copies", count(list, "a@example.com"), count(list, "a@example.com"))
	}
	if count(list, "b@example.com") != 1 {
		t.Errorf("b@example.com missing from %v", list)
	}

	// Opting out removes them from every future campaign.
	if _, err := SetEmailOptOut(ctx, e.pool, e.unitID, a.ID, true, e.actor); err != nil {
		t.Fatal(err)
	}
	got, _ = RecipientsForStatuses(ctx, e.pool, e.unitID, []string{StatusNew, StatusContacted})
	if count(emailsOf(got), "a@example.com") != 0 {
		t.Fatal("an opted-out family is still a campaign recipient")
	}

	// Putting them back on is possible, for a family who asks.
	if _, err := SetEmailOptOut(ctx, e.pool, e.unitID, a.ID, false, e.actor); err != nil {
		t.Fatal(err)
	}
	back, err := Get(ctx, e.pool, e.unitID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.EmailOptOut || back.OptOutAt != nil {
		t.Error("opting back in left the record saying otherwise")
	}

	// A status that isn't a status must not widen the audience.
	got, err = RecipientsForStatuses(ctx, e.pool, e.unitID, []string{"everyone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("an invalid status reached %d recipients", len(got))
	}
}

// One unit's campaign must never reach the other unit's prospects, even
// though a family can hold roles in both.
func TestRecipientsAreUnitScoped(t *testing.T) {
	a := newEnv(t, "campunita")
	b := newEnv(t, "campunitb")
	ctx := context.Background()

	a.prospect(t, "onlyfora@example.com", StatusNew)
	b.prospect(t, "onlyforb@example.com", StatusNew)

	got, err := RecipientsForStatuses(ctx, a.pool, a.unitID, []string{StatusNew})
	if err != nil {
		t.Fatal(err)
	}
	if count(emailsOf(got), "onlyforb@example.com") != 0 {
		t.Fatal("one unit's campaign would reach the other unit's prospects")
	}
}

func TestUnsubscribeEndToEnd(t *testing.T) {
	e := newEnv(t, "campunsub")
	ctx := context.Background()
	secret := []byte("a-test-signing-secret-at-least-32-bytes")

	p := e.prospect(t, "unsub@example.com", StatusContacted)

	if _, err := Unsubscribe(ctx, e.pool, secret, p.ID, "forged"); !errors.Is(err, ErrBadUnsubscribeToken) {
		t.Fatalf("a forged token was accepted: %v", err)
	}
	// A token signed with someone else's secret must not work either.
	if _, err := Unsubscribe(ctx, e.pool, secret, p.ID, UnsubscribeToken([]byte("another-secret-of-sufficient-len"), p.ID)); !errors.Is(err, ErrBadUnsubscribeToken) {
		t.Fatalf("a token from a different secret was accepted: %v", err)
	}
	// And one prospect's token must not unsubscribe another.
	other := e.prospect(t, "other@example.com", StatusContacted)
	if _, err := Unsubscribe(ctx, e.pool, secret, other.ID, UnsubscribeToken(secret, p.ID)); !errors.Is(err, ErrBadUnsubscribeToken) {
		t.Fatal("one family's link unsubscribed a different family")
	}

	after, err := Unsubscribe(ctx, e.pool, secret, p.ID, UnsubscribeToken(secret, p.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !after.EmailOptOut || after.OptOutAt == nil {
		t.Fatal("the link did not record the opt-out")
	}

	// Clicked twice — mail clients pre-fetch links.
	first := *after.OptOutAt
	again, err := Unsubscribe(ctx, e.pool, secret, p.ID, UnsubscribeToken(secret, p.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !again.OptOutAt.Equal(first) {
		t.Error("a second click moved the opt-out timestamp")
	}

	got, _ := RecipientsForStatuses(ctx, e.pool, e.unitID, []string{StatusContacted})
	if count(emailsOf(got), "unsub@example.com") != 0 {
		t.Fatal("unsubscribing did not take effect on future campaigns")
	}
}

func TestCampaignIsFrozenOnceSent(t *testing.T) {
	e := newEnv(t, "campfrozen")
	ctx := context.Background()

	c, err := CreateCampaign(ctx, e.pool, e.unitID, "Come and visit", "<p>Hello</p>",
		[]string{StatusNew, "not-a-status"}, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TargetStatuses) != 1 {
		t.Errorf("target statuses not filtered on the way in: %v", c.TargetStatuses)
	}

	if _, err := CreateCampaign(ctx, e.pool, e.unitID, "   ", "", nil, e.actor); err == nil {
		t.Error("a blank subject was accepted")
	}

	if _, err := UpdateCampaign(ctx, e.pool, c.ID, e.unitID, "Edited", "<p>v2</p>", []string{StatusNew}, e.actor); err != nil {
		t.Fatalf("editing a draft failed: %v", err)
	}

	if _, err := e.pool.Exec(ctx, `UPDATE prospect_campaigns SET status='sent' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateCampaign(ctx, e.pool, c.ID, e.unitID, "Sneaky", "", nil, e.actor); !errors.Is(err, ErrCampaignSent) {
		t.Errorf("editing a sent campaign returned %v, want ErrCampaignSent — the record would become a lie", err)
	}
	if err := DeleteCampaign(ctx, e.pool, c.ID, e.unitID, e.actor); !errors.Is(err, ErrCampaignSent) {
		t.Errorf("deleting a sent campaign returned %v, want ErrCampaignSent", err)
	}
}

func TestCampaignsAreUnitScoped(t *testing.T) {
	a := newEnv(t, "campscopea")
	b := newEnv(t, "campscopeb")
	ctx := context.Background()

	c, err := CreateCampaign(ctx, a.pool, a.unitID, "A's letter", "", []string{StatusNew}, a.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GetCampaign(ctx, a.pool, c.ID, b.unitID); !errors.Is(err, ErrCampaignNotFound) {
		t.Errorf("one unit read another's campaign: %v", err)
	}
	if _, err := UpdateCampaign(ctx, a.pool, c.ID, b.unitID, "Hijacked", "", nil, b.actor); !errors.Is(err, ErrCampaignNotFound) {
		t.Errorf("one unit edited another's campaign: %v", err)
	}
	if err := DeleteCampaign(ctx, a.pool, c.ID, b.unitID, b.actor); !errors.Is(err, ErrCampaignNotFound) {
		t.Errorf("one unit deleted another's campaign: %v", err)
	}
}

// A campaign with no unsubscribe function must send nothing at all, and
// must leave the campaign editable rather than stranded at "sending".
func TestSendRefusesWithoutAnUnsubscribeLink(t *testing.T) {
	e := newEnv(t, "campnounsub")
	ctx := context.Background()

	c, err := CreateCampaign(ctx, e.pool, e.unitID, "No way out", "<p>x</p>", []string{StatusNew}, e.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE prospect_campaigns SET status='sending' WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	c.Status = "sending"

	sent, failed := SendCampaign(ctx, e.pool, nil, c,
		[]Recipient{{ProspectID: "x", Email: "nobody@example.com"}}, nil, e.actor)
	if sent != 0 || failed != 0 {
		t.Fatalf("sent=%d failed=%d — it mailed the public with no unsubscribe link", sent, failed)
	}

	back, err := GetCampaign(ctx, e.pool, c.ID, e.unitID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Status != "draft" {
		t.Errorf("campaign left at %q rather than returned to draft", back.Status)
	}
}
