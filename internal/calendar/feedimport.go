package calendar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
	"github.com/47-yonkers/scout-site/internal/icalendar"
)

// The inbound half of calendar sharing: an external calendar (a Google
// secret address, most often) whose events are copied onto this unit's
// calendar and refreshed on a schedule.
//
// Imported events are ordinary rows in `events` with feed_id set, rather
// than a separate table, so every existing reader — the calendar page,
// the month grid, the personal .ics feed, event reminders — picks them up
// with no changes. What feed_id buys is knowing which events the importer
// owns, so a refresh can update and prune them without touching anything
// a leader typed by hand.

// ImportWindowPast bounds how much history an import brings in. A council
// calendar may hold years of it; none of it is interesting here.
const ImportWindowPast = 30 * 24 * time.Hour

// ImportWindowFuture bounds the other end, and with it how far a
// recurring event is expanded.
const ImportWindowFuture = 400 * 24 * time.Hour

// fetchTimeout caps a single feed fetch. Refreshes run in the background,
// but a hung fetch should not hold a connection open indefinitely.
const fetchTimeout = 30 * time.Second

// Feed is one subscribed external calendar.
type Feed struct {
	ID             string
	UnitID         string
	Name           string
	URL            string
	Visibility     string
	Enabled        bool
	LastFetchedAt  *time.Time
	LastStatus     string
	LastEventCount *int
	CreatedBy      string
	CreatedAt      time.Time
}

const feedColumns = `id, unit_id, name, url, visibility::text, enabled,
	last_fetched_at, COALESCE(last_status, ''), last_event_count, created_by, created_at`

func scanFeed(row interface{ Scan(...any) error }) (Feed, error) {
	var f Feed
	err := row.Scan(&f.ID, &f.UnitID, &f.Name, &f.URL, &f.Visibility, &f.Enabled,
		&f.LastFetchedAt, &f.LastStatus, &f.LastEventCount, &f.CreatedBy, &f.CreatedAt)
	return f, err
}

// ErrFeedURLNotAllowed rejects a URL before it is ever fetched. See
// validateFeedURL for why each case is refused.
var ErrFeedURLNotAllowed = errors.New("calendar: that address can't be used as a calendar feed")

// FeedsForUnit lists a unit's subscriptions for the admin page.
func FeedsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Feed, error) {
	rows, err := pool.Query(ctx, `SELECT `+feedColumns+` FROM calendar_feeds WHERE unit_id = $1 ORDER BY name`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Feed
	for rows.Next() {
		f, err := scanFeed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetFeed loads one feed, scoped to its unit so a feed id from one
// subdomain can't be operated on from the other.
func GetFeed(ctx context.Context, pool *pgxpool.Pool, id, unitID string) (Feed, error) {
	return scanFeed(pool.QueryRow(ctx,
		`SELECT `+feedColumns+` FROM calendar_feeds WHERE id = $1 AND unit_id = $2`, id, unitID))
}

// AddFeed subscribes a unit to an external calendar. The URL is validated
// but not fetched here — the first refresh does that, and reports its
// outcome on the admin page.
func AddFeed(ctx context.Context, pool *pgxpool.Pool, unitID, name, rawURL, visibility, actorID string) (Feed, error) {
	clean, err := validateFeedURL(rawURL)
	if err != nil {
		return Feed{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = "Imported calendar"
	}
	if visibility != "public" && visibility != "members" {
		visibility = "members"
	}

	f, err := scanFeed(pool.QueryRow(ctx, `
		INSERT INTO calendar_feeds (unit_id, name, url, visibility, created_by)
		VALUES ($1, $2, $3, $4::visibility, $5)
		RETURNING `+feedColumns,
		unitID, strings.TrimSpace(name), clean, visibility, actorID))
	if err != nil {
		return Feed{}, err
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "calendar_feed", EntityID: f.ID, ActorID: &actorID, Action: "create",
		After: map[string]any{"name": f.Name, "url": f.URL, "visibility": f.Visibility},
	})
	return f, nil
}

// SetFeedEnabled pauses or resumes a feed without losing it or its events.
func SetFeedEnabled(ctx context.Context, pool *pgxpool.Pool, id, unitID string, enabled bool, actorID string) error {
	_, err := pool.Exec(ctx,
		`UPDATE calendar_feeds SET enabled = $1 WHERE id = $2 AND unit_id = $3`, enabled, id, unitID)
	if err != nil {
		return err
	}
	action := "disable"
	if enabled {
		action = "enable"
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "calendar_feed", EntityID: id, ActorID: &actorID, Action: action,
	})
	return nil
}

// DeleteFeed removes a subscription. Its imported events go with it, via
// the ON DELETE CASCADE on events.feed_id — which is what a leader means
// by "remove this calendar", and the reason imported events are marked
// rather than merged into the unit's own.
func DeleteFeed(ctx context.Context, pool *pgxpool.Pool, id, unitID, actorID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM calendar_feeds WHERE id = $1 AND unit_id = $2`, id, unitID)
	if err != nil {
		return err
	}
	audit.Log(ctx, pool, audit.Entry{
		EntityType: "calendar_feed", EntityID: id, ActorID: &actorID, Action: "delete",
	})
	return nil
}

// ImportResult reports what one refresh did, for the admin page and the
// command-line refresher.
type ImportResult struct {
	FeedID   string
	FeedName string
	Fetched  int // events the source offered inside the window
	Created  int
	Updated  int
	Removed  int // previously imported events no longer in the source
	// Conflicts is how many incoming events were held back because they
	// clash with something already on the calendar. They are not counted
	// as Created — nothing was written to the calendar for them; they are
	// waiting on a leader. See feedconflict.go.
	Conflicts int
	// Ignored is how many the feed offered that a leader has previously
	// told it to stop bringing in.
	Ignored int
	Err     error
}

// RefreshFeed fetches one feed and reconciles this unit's copy of it.
//
// The reconciliation is a three-way match on (feed_id, external_uid):
// events present in both are updated in place, ones only in the source
// are created, and ones only here are deleted because they have been
// cancelled or moved out of the window upstream. Matching on the source's
// own UID is what makes a re-import an update rather than a pile of
// duplicates — and why icalendar.Parse folds a recurrence's date into the
// UID it returns.
func RefreshFeed(ctx context.Context, pool *pgxpool.Pool, client *http.Client, f Feed) ImportResult {
	events, err := fetchFeed(ctx, client, f.URL)
	if err != nil {
		recordFeedStatus(ctx, pool, f.ID, statusMessage(err), nil)
		return ImportResult{FeedID: f.ID, FeedName: f.Name, Err: err}
	}
	return reconcile(ctx, pool, f, events)
}

// reconcile is RefreshFeed with the network already done — everything
// from "here is what the source offers" onwards.
//
// Split out so the interesting half can be exercised against a real
// database with a handwritten event list, rather than only through an
// HTTP round trip. The conflict rules in particular are the kind of thing
// that reads correctly and behaves otherwise, and they are unreachable
// from a unit test that can't reach Postgres.
func reconcile(ctx context.Context, pool *pgxpool.Pool, f Feed, events []icalendar.Event) ImportResult {
	res := ImportResult{FeedID: f.ID, FeedName: f.Name}
	res.Fetched = len(events)

	// What we already hold for this feed.
	existing := map[string]string{} // external_uid -> event id
	rows, err := pool.Query(ctx,
		`SELECT id, external_uid FROM events WHERE feed_id = $1 AND external_uid IS NOT NULL`, f.ID)
	if err != nil {
		res.Err = err
		return res
	}
	for rows.Next() {
		var id, uid string
		if err := rows.Scan(&id, &uid); err != nil {
			rows.Close()
			res.Err = err
			return res
		}
		existing[uid] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		res.Err = err
		return res
	}

	// Decisions a leader has already made about this feed. Loaded once
	// rather than per event: a council calendar the unit has pruned may
	// have dozens.
	ignored, err := ignoredUIDs(ctx, pool, f.ID)
	if err != nil {
		res.Err = err
		return res
	}

	seen := make(map[string]bool, len(events))
	for _, ev := range events {
		if ev.Cancelled {
			// The source kept the entry but called the event off. Not
			// importing it means the delete pass below removes any copy
			// we already had, which is the behaviour a family wants.
			continue
		}
		seen[ev.UID] = true

		if ignored[ev.UID] {
			// A leader has ruled on this one already. Marked seen above
			// so the delete pass doesn't treat it as withdrawn, which
			// would be harmless but would churn the log every refresh.
			res.Ignored++
			continue
		}

		var endsAt *time.Time
		if !ev.End.IsZero() {
			e := ev.End
			endsAt = &e
		}
		title := ev.Summary
		if strings.TrimSpace(title) == "" {
			title = "(untitled)"
		}

		if id, ok := existing[ev.UID]; ok {
			// Only the fields the source owns are updated. Notably
			// visibility is not: a leader may have made one imported
			// event public, and a refresh should not undo that.
			_, err = pool.Exec(ctx, `
				UPDATE events SET title = $1, description = $2, location = $3,
				                  starts_at = $4, ends_at = $5
				WHERE id = $6
			`, title, ev.Description, ev.Location, ev.Start, endsAt, id)
			if err != nil {
				res.Err = err
				return res
			}
			res.Updated++
			continue
		}

		// Only a brand-new import is checked for clashes. One already
		// imported is matched by external_uid and updated in place — it
		// is on the calendar because somebody agreed to it, and asking
		// again every time it moves by ten minutes would be noise.
		clashesWith, err := findConflict(ctx, pool, f.UnitID, f.ID, ev)
		if err != nil {
			res.Err = err
			return res
		}
		if clashesWith != "" {
			if err := recordConflict(ctx, pool, f.UnitID, f.ID, clashesWith, ev, title); err != nil {
				res.Err = err
				return res
			}
			res.Conflicts++
			continue
		}

		_, err = pool.Exec(ctx, `
			INSERT INTO events (unit_id, title, description, location, starts_at, ends_at,
			                    visibility, status, created_by, feed_id, external_uid)
			VALUES ($1, $2, $3, $4, $5, $6, $7::visibility, 'published', $8, $9, $10)
			ON CONFLICT (feed_id, external_uid) WHERE feed_id IS NOT NULL DO NOTHING
		`, f.UnitID, title, ev.Description, ev.Location, ev.Start, endsAt,
			f.Visibility, f.CreatedBy, f.ID, ev.UID)
		if err != nil {
			res.Err = err
			return res
		}
		res.Created++
	}

	// A conflict the source has stopped offering is no longer a decision
	// anybody needs to make.
	if _, err := pool.Exec(ctx, `
		DELETE FROM calendar_feed_conflicts
		WHERE feed_id = $1 AND NOT (external_uid = ANY($2))
	`, f.ID, seenUIDs(seen)); err != nil {
		log.Printf("calendar: clearing stale conflicts for feed %s: %v", f.ID, err)
	}

	// Anything we hold that the source no longer offers.
	for uid, id := range existing {
		if seen[uid] {
			continue
		}
		if _, err := pool.Exec(ctx, `DELETE FROM events WHERE id = $1 AND feed_id = $2`, id, f.ID); err != nil {
			res.Err = err
			return res
		}
		res.Removed++
	}

	count := res.Created + res.Updated
	status := "ok"
	if res.Conflicts > 0 {
		// Surfaced in the feed's own status line, because a conflict that
		// is only visible on a page nobody has a reason to open is a
		// conflict nobody resolves — and until it is resolved the event
		// is simply missing from the calendar.
		status = fmt.Sprintf("ok — %d event(s) held for review", res.Conflicts)
	}
	recordFeedStatus(ctx, pool, f.ID, status, &count)
	return res
}

// seenUIDs flattens the seen set for the stale-conflict delete above.
// Postgres has no set type to pass, and an empty slice is correct: a feed
// that offered nothing this time has no live conflicts either.
func seenUIDs(seen map[string]bool) []string {
	out := make([]string, 0, len(seen))
	for uid := range seen {
		out = append(out, uid)
	}
	return out
}

// RefreshAllFeeds refreshes every enabled feed across every unit. Called
// by the -refresh-calendar-feeds flag on a schedule.
func RefreshAllFeeds(ctx context.Context, pool *pgxpool.Pool) ([]ImportResult, error) {
	rows, err := pool.Query(ctx, `SELECT `+feedColumns+` FROM calendar_feeds WHERE enabled ORDER BY unit_id, name`)
	if err != nil {
		return nil, err
	}
	var feeds []Feed
	for rows.Next() {
		f, err := scanFeed(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		feeds = append(feeds, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	client := NewFeedClient()
	out := make([]ImportResult, 0, len(feeds))
	for _, f := range feeds {
		out = append(out, RefreshFeed(ctx, pool, client, f))
	}
	return out, nil
}

func recordFeedStatus(ctx context.Context, pool *pgxpool.Pool, feedID, status string, count *int) {
	_, _ = pool.Exec(ctx, `
		UPDATE calendar_feeds
		SET last_fetched_at = now(), last_status = $1, last_event_count = COALESCE($2, last_event_count)
		WHERE id = $3
	`, status, count, feedID)
}

// statusMessage turns a fetch failure into something a leader can act on,
// rather than a Go error string.
func statusMessage(err error) string {
	switch {
	case errors.Is(err, icalendar.ErrNotCalendar):
		return "That address returned a web page, not a calendar. In Google Calendar use Settings → your calendar → Integrate calendar → Secret address in iCal format."
	case errors.Is(err, context.DeadlineExceeded):
		return "The calendar did not respond in time. It may be temporarily unavailable."
	default:
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return msg
	}
}

func fetchFeed(ctx context.Context, client *http.Client, rawURL string) ([]icalendar.Event, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "scout-site calendar import")
	req.Header.Set("Accept", "text/calendar, */*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the calendar returned %s", resp.Status)
	}

	now := time.Now()
	return icalendar.Parse(io.LimitReader(resp.Body, icalendar.MaxFeedBytes),
		now.Add(-ImportWindowPast), now.Add(ImportWindowFuture))
}

// validateFeedURL is the first of two checks on a pasted address, and the
// weaker one: it rejects anything that isn't plain HTTP(S) and any
// literal internal IP, so a leader gets an immediate "no" in the form.
//
// It deliberately does NOT decide safety on its own. Resolving a hostname
// here and trusting the answer later is a time-of-check/time-of-use gap:
// the name may not resolve yet, may resolve differently when the fetch
// runs, or may be rebound between the two on purpose. The real
// enforcement is safeDialControl below, which inspects the address the
// connection is actually being made to.
func validateFeedURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	// Calendar apps hand out webcal:// links; it is plain HTTPS
	// underneath, and pasting one is the obvious thing to do.
	if strings.HasPrefix(strings.ToLower(raw), "webcal://") {
		raw = "https://" + raw[len("webcal://"):]
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrFeedURLNotAllowed
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrFeedURLNotAllowed
	}
	host := u.Hostname()
	if host == "" {
		return "", ErrFeedURLNotAllowed
	}
	// A bare name with no dot cannot be a public host, but is exactly
	// what a container on the app's own Docker network is called ("db").
	// Rejected here so the leader is told at once rather than after a
	// failed fetch. A literal IP has no dot requirement to meet.
	if net.ParseIP(host) == nil && !strings.Contains(host, ".") {
		return "", ErrFeedURLNotAllowed
	}
	if ip := net.ParseIP(host); ip != nil && !publicIP(ip) {
		return "", ErrFeedURLNotAllowed
	}
	return u.String(), nil
}

// publicIP reports whether an address is one the internet can route to —
// i.e. not the loopback, private, link-local, or unspecified ranges. The
// cloud metadata service at 169.254.169.254 is covered by link-local.
func publicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast())
}

// safeDialControl is the actual SSRF boundary.
//
// It runs after DNS resolution, with the concrete IP the socket is about
// to connect to, and refuses anything not publicly routable. Because it
// sits at the dial, it covers every path to a connection — the initial
// fetch, any HTTP redirect the server follows, a hostname that resolves
// to something different than it did a moment ago, and a name that could
// not be resolved at validation time at all.
func safeDialControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrFeedURLNotAllowed
	}
	ip := net.ParseIP(host)
	if ip == nil || !publicIP(ip) {
		return fmt.Errorf("%w: refusing to connect to %s", ErrFeedURLNotAllowed, host)
	}
	return nil
}

// NewFeedClient builds the HTTP client feed fetches must use. Anything
// fetching a leader-supplied URL without this dialer is unsafe.
func NewFeedClient() *http.Client {
	return &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
				Control: safeDialControl,
			}).DialContext,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Redirects are followed (calendar providers use them), but
			// each new hop dials through the same guarded dialer, and the
			// chain is bounded.
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return ErrFeedURLNotAllowed
			}
			return nil
		},
	}
}

// ErrNoSuchFeed is returned by GetFeed's callers when a feed id doesn't
// belong to the unit asking.
var ErrNoSuchFeed = pgx.ErrNoRows
