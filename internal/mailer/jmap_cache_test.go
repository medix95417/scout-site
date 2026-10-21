package mailer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingJMAPServer is jmapTestServer with the request counts exposed,
// because the whole point of the cache is how many requests happen — a
// test that only checks the mail "sent" would pass just as well with no
// cache at all.
type countingJMAPServer struct {
	srv         *httptest.Server
	sessions    atomic.Int32 // GET /session
	identityGet atomic.Int32 // Identity/get + Mailbox/query (route resolution)
	submits     atomic.Int32 // Email/set + EmailSubmission/set (the actual send)

	// submitted is each send's request as the server saw it, so a test can say
	// which identity a message went out under, not just that one did.
	mu        sync.Mutex
	submitted []string

	// rejectSubmits, while true, makes every submit fail with 401 — the
	// "credentials rejected at the door" case.
	rejectSubmits atomic.Bool
	// rateLimitAlways / rateLimitOnce make submits fail with 429, either
	// forever or just for the first attempt.
	rateLimitAlways atomic.Bool
	rateLimitOnce   atomic.Bool
}

// newCountingJMAPServer's account has one sending identity per address given:
// ident0, ident1, ... in the order passed.
func newCountingJMAPServer(t *testing.T, identityEmails ...string) *countingJMAPServer {
	t.Helper()
	c := &countingJMAPServer{}

	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		c.sessions.Add(1)
		fmt.Fprintf(w, `{"apiUrl":%q,"primaryAccounts":{"urn:ietf:params:jmap:mail":"acct1"}}`, c.srv.URL+"/api")
	})
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		var req jmapRequest
		if err := readJSON(r, &req); err != nil {
			t.Errorf("server: parsing request: %v", err)
			return
		}
		if len(req.MethodCalls) == 0 {
			t.Error("server: empty methodCalls")
			return
		}
		firstMethod, _ := req.MethodCalls[0][0].(string)

		w.Header().Set("Content-Type", "application/json")
		switch firstMethod {
		case "Identity/get":
			c.identityGet.Add(1)
			// Call ids match what fetchIdentityAndDrafts asks for; a
			// response keyed differently is a response it can't read.
			var identities []string
			for i, email := range identityEmails {
				identities = append(identities, fmt.Sprintf(`{"id":"ident%d","email":%q}`, i, email))
			}
			fmt.Fprintf(w, `{"methodResponses":[
				["Identity/get", {"list":[%s]}, "id"],
				["Mailbox/query", {"ids":["drafts1"]}, "mb"]
			]}`, strings.Join(identities, ","))
		case "Email/set":
			c.submits.Add(1)
			if raw, err := json.Marshal(req); err == nil {
				c.mu.Lock()
				c.submitted = append(c.submitted, string(raw))
				c.mu.Unlock()
			}
			if c.rateLimitAlways.Load() || c.rateLimitOnce.CompareAndSwap(true, false) {
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"type":"about:blank","status":429,"detail":"slow down"}`)
				return
			}
			if c.rejectSubmits.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"type":"about:blank","status":401,"detail":"token rejected"}`)
				return
			}
			fmt.Fprint(w, `{"methodResponses":[
				["Email/set", {"created":{"draft":{"id":"email1"}}}, "e0"],
				["EmailSubmission/set", {"created":{"submission":{"id":"sub1"}}}, "s0"]
			]}`)
		default:
			t.Errorf("server: unexpected first method %q", firstMethod)
		}
	})

	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)

	orig := fastmailSessionURL
	fastmailSessionURL = c.srv.URL + "/session"
	t.Cleanup(func() { fastmailSessionURL = orig })
	return c
}

func testJMAPConfig() Config {
	return Config{
		Provider: ProviderFastmailJMAP,
		APIToken: "test-token",
		From:     "Troop 47 <sender@example.com>",
	}
}

// The reason the cache exists: a batch must resolve the route once, not
// once per recipient. Before this, a newsletter to a whole roster made
// three HTTP requests per family.
func TestBatchResolvesTheRouteOnce(t *testing.T) {
	srv := newCountingJMAPServer(t, "sender@example.com")
	cfg := testJMAPConfig()
	m := New(cfg, nil)

	const recipients = 10
	for i := 0; i < recipients; i++ {
		to := fmt.Sprintf("family%d@example.com", i)
		if err := m.sendViaFastmailJMAP(t.Context(), cfg, to, "Subject", "Hello", "text/html"); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	if got := int(srv.submits.Load()); got != recipients {
		t.Errorf("submits = %d, want %d — every recipient must still get exactly one message", got, recipients)
	}
	if got := srv.sessions.Load(); got != 1 {
		t.Errorf("session fetches = %d, want 1", got)
	}
	if got := srv.identityGet.Load(); got != 1 {
		t.Errorf("identity lookups = %d, want 1", got)
	}
}

// Never caching used to be what made a rotated token take effect at once.
// That property is kept by keying the cache on the credentials, and this
// is what holds it: a changed token must not be sent against a route
// resolved for the old one.
func TestChangedTokenReResolves(t *testing.T) {
	srv := newCountingJMAPServer(t, "sender@example.com")
	cfg := testJMAPConfig()
	m := New(cfg, nil)

	if err := m.sendViaFastmailJMAP(t.Context(), cfg, "a@example.com", "S", "B", "text/html"); err != nil {
		t.Fatal(err)
	}
	if got := srv.sessions.Load(); got != 1 {
		t.Fatalf("session fetches = %d, want 1", got)
	}

	rotated := cfg
	rotated.APIToken = "rotated-token"
	if err := m.sendViaFastmailJMAP(t.Context(), rotated, "b@example.com", "S", "B", "text/html"); err != nil {
		t.Fatal(err)
	}
	if got := srv.sessions.Load(); got != 2 {
		t.Errorf("session fetches = %d after a token change, want 2 — a rotated token was used against a stale route", got)
	}
}

// Same for the From address, which decides the sending identity. Reusing
// a route resolved for a different From would send as the wrong address.
func TestChangedFromReResolves(t *testing.T) {
	srv := newCountingJMAPServer(t, "sender@example.com")
	cfg := testJMAPConfig()
	m := New(cfg, nil)

	if err := m.sendViaFastmailJMAP(t.Context(), cfg, "a@example.com", "S", "B", "text/html"); err != nil {
		t.Fatal(err)
	}

	// A different From with no matching identity must fail, not quietly
	// reuse the cached identity for the old address.
	changed := cfg
	changed.From = "someone-else@example.com"
	err := m.sendViaFastmailJMAP(t.Context(), changed, "b@example.com", "S", "B", "text/html")
	if err == nil {
		t.Fatal("a From with no matching identity was accepted — the cached identity was reused for a different address")
	}
	if !strings.Contains(err.Error(), "no Fastmail identity matches") {
		t.Errorf("got %v, want an identity-mismatch error", err)
	}
	if got := srv.identityGet.Load(); got != 2 {
		t.Errorf("identity lookups = %d, want 2 — the From change did not re-resolve", got)
	}
}

// An expired route heals on its own rather than needing a restart.
func TestRouteExpires(t *testing.T) {
	srv := newCountingJMAPServer(t, "sender@example.com")
	cfg := testJMAPConfig()
	m := New(cfg, nil)

	orig := jmapRouteTTL
	jmapRouteTTL = 1 * time.Millisecond
	defer func() { jmapRouteTTL = orig }()

	if err := m.sendViaFastmailJMAP(t.Context(), cfg, "a@example.com", "S", "B", "text/html"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := m.sendViaFastmailJMAP(t.Context(), cfg, "b@example.com", "S", "B", "text/html"); err != nil {
		t.Fatal(err)
	}

	if got := srv.sessions.Load(); got != 2 {
		t.Errorf("session fetches = %d, want 2 — an expired route was reused", got)
	}
}

// A route that goes stale mid-batch must recover: the submit is rejected
// outright, the route is re-resolved, and the message goes out.
func TestRejectedSubmitRetriesOnceWithAFreshRoute(t *testing.T) {
	srv := newCountingJMAPServer(t, "sender@example.com")
	cfg := testJMAPConfig()
	m := New(cfg, nil)

	// Warm the cache with a good send.
	if err := m.sendViaFastmailJMAP(t.Context(), cfg, "a@example.com", "S", "B", "text/html"); err != nil {
		t.Fatal(err)
	}
	sessionsAfterWarm := srv.sessions.Load()

	// Now every submit is rejected. The send should fail, but only after
	// re-resolving and trying once more.
	srv.rejectSubmits.Store(true)
	err := m.sendViaFastmailJMAP(t.Context(), cfg, "b@example.com", "S", "B", "text/html")
	if err == nil {
		t.Fatal("a rejected submit reported success")
	}
	if got := srv.sessions.Load(); got != sessionsAfterWarm+1 {
		t.Errorf("session fetches = %d, want %d — a 401 did not trigger exactly one re-resolve",
			got, sessionsAfterWarm+1)
	}

	// And the retry must be bounded at one, or a broken token would make
	// every recipient cost two full round trips.
	before := srv.submits.Load()
	_ = m.sendViaFastmailJMAP(t.Context(), cfg, "c@example.com", "S", "B", "text/html")
	if got := srv.submits.Load() - before; got != 2 {
		t.Errorf("submit attempts = %d for one send, want exactly 2 (original + one retry)", got)
	}
}

// The retry is deliberately narrow. Anything that isn't an outright
// rejection might already have delivered, and a second attempt would send
// a duplicate — worse than a reported failure, especially for a
// recruiting email to a member of the public.
func TestOnlyOutrightRejectionsAreRetried(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"401 unauthorized", &jmapStatusError{StatusCode: 401, Status: "401 Unauthorized"}, true},
		{"403 forbidden", &jmapStatusError{StatusCode: 403, Status: "403 Forbidden"}, true},
		{"500 server error", &jmapStatusError{StatusCode: 500, Status: "500 Internal Server Error"}, false},
		{"429 rate limited", &jmapStatusError{StatusCode: 429, Status: "429 Too Many Requests"}, false},
		{"network failure", errors.New("connection reset"), false},
		{"wrapped 401", fmt.Errorf("sending: %w", &jmapStatusError{StatusCode: 401}), true},
	} {
		if got := rejectedBeforeDelivery(tc.err); got != tc.want {
			t.Errorf("%s: rejectedBeforeDelivery = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Two batches starting at once must not each resolve their own route, and
// must not race. Run with -race.
func TestConcurrentSendsShareOneRoute(t *testing.T) {
	srv := newCountingJMAPServer(t, "sender@example.com")
	cfg := testJMAPConfig()
	m := New(cfg, nil)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			to := fmt.Sprintf("family%d@example.com", i)
			if err := m.sendViaFastmailJMAP(t.Context(), cfg, to, "S", "B", "text/html"); err != nil {
				t.Errorf("send %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if got := srv.submits.Load(); got != 8 {
		t.Errorf("submits = %d, want 8", got)
	}
	if got := srv.sessions.Load(); got != 1 {
		t.Errorf("session fetches = %d, want 1 — concurrent senders each resolved their own route", got)
	}
}

// readJSON decodes a request body into v.
func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// The From address is per unit, so two units alternate on one mailer: the
// Troop's newsletter going out while a Pack password reset arrives. Each From
// is its own cache key, so they coexist — resolved once each — rather than
// every switch throwing the other's route out. And a message goes out under the
// identity of the address it is from, never the other unit's.
func TestTwoUnitsFromAddressesKeepTheirOwnRoutes(t *testing.T) {
	srv := newCountingJMAPServer(t, "troop@example.com", "pack@example.com")
	troop := testJMAPConfig()
	troop.From = "Troop 47 <troop@example.com>"
	pack := testJMAPConfig()
	pack.From = "Pack 47 <pack@example.com>"
	m := New(troop, nil)

	const rounds = 4
	for i := 0; i < rounds; i++ {
		if err := m.sendViaFastmailJMAP(t.Context(), troop, "a@example.com", "S", "B", "text/html"); err != nil {
			t.Fatalf("troop send %d: %v", i, err)
		}
		if err := m.sendViaFastmailJMAP(t.Context(), pack, "b@example.com", "S", "B", "text/html"); err != nil {
			t.Fatalf("pack send %d: %v", i, err)
		}
	}

	if got := int(srv.submits.Load()); got != rounds*2 {
		t.Errorf("submits = %d, want %d", got, rounds*2)
	}
	if got := srv.identityGet.Load(); got != 2 {
		t.Errorf("identity lookups = %d, want 2 — one per unit's address, not one per switch between them", got)
	}
	if got := srv.sessions.Load(); got != 2 {
		t.Errorf("session fetches = %d, want 2", got)
	}

	// Each send used its own address's identity. troop@ is ident0, pack@ is ident1.
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for i, req := range srv.submitted {
		wantIdent, wantFrom, otherFrom := `"identityId":"ident0"`, "troop@example.com", "pack@example.com"
		if i%2 == 1 {
			wantIdent, wantFrom, otherFrom = `"identityId":"ident1"`, "pack@example.com", "troop@example.com"
		}
		if !strings.Contains(req, wantIdent) || !strings.Contains(req, wantFrom) || strings.Contains(req, otherFrom) {
			t.Errorf("send %d was not sent as %s under %s:\n%s", i, wantFrom, wantIdent, req)
		}
	}
}
