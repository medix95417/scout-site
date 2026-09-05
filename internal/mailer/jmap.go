package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"
)

// fastmailSessionURL is JMAP's well-known session discovery endpoint —
// fixed for every Fastmail account (see
// https://www.fastmail.com/dev/), unlike SMTP's per-deployment
// host/port. A session response gives everything else needed to talk
// to the API: the actual JMAP endpoint to POST method calls to, and
// which of the caller's accounts is the "primary" mail account. A var,
// not a const, so a test can point it at an httptest.Server instead of
// the real Fastmail API — same reasoning as dialTimeout/overallTimeout
// in mailer.go.
var fastmailSessionURL = "https://api.fastmail.com/jmap/session"

// jmapHTTPTimeout bounds each HTTP round trip a JMAP send makes —
// session discovery, then two JMAP API calls. Mirrors deliverSMTP's
// overallTimeout in spirit: a hung/unreachable API must not block the
// caller (a password-reset request, the reminders batch job) forever.
var jmapHTTPTimeout = 15 * time.Second

// jmapRoute is everything a send needs that isn't the message itself:
// where to POST, which account, which sending identity, which Drafts
// mailbox. Resolving it costs two HTTP round trips, and it is the same
// answer for every message in a batch.
type jmapRoute struct {
	apiURL          string
	accountID       string
	identityID      string
	draftsMailboxID string
}

// jmapRouteCache holds one resolved route, so a batch send resolves it
// once instead of once per recipient.
//
// This used to be resolved on every single call, deliberately, and the
// reasoning was sound at the time: the only senders were password resets
// and event reminders, so the extra round trips cost nothing, and never
// caching meant a rotated API token or a changed From address took effect
// on the very next send with no restart.
//
// Mass email changed the first half of that. A campaign to fifty
// prospects, or a newsletter to a whole roster, was three HTTP requests
// per recipient where it needed one — slower than necessary and closer to
// Fastmail's rate limits than necessary.
//
// The second half is kept rather than traded away: the cache is keyed on
// the token and From address it was resolved with, so changing either
// misses the cache and re-resolves immediately. Nothing needs restarting,
// and a stale route can never be used against new credentials.
type jmapRouteCache struct {
	mu sync.Mutex

	// The key: what this route was resolved for. A change in either
	// invalidates it.
	token string
	from  string

	cached     jmapRoute
	resolved   bool
	resolvedAt time.Time
}

// jmapRouteTTL bounds how long a resolved route is reused. Fastmail does
// not document a session lifetime, and the session object can change
// server-side (RFC 8620 §2 gives it a state string for exactly that
// reason), so the route is re-resolved periodically rather than held for
// the life of the process. Long enough that any realistic batch resolves
// once; short enough that a server-side change heals on its own without
// anybody noticing. A var so a test can shrink it.
var jmapRouteTTL = 15 * time.Minute

// route returns a usable jmapRoute, resolving it if the cache is empty,
// keyed for different credentials, expired, or explicitly refreshed.
//
// The lock is held across the network fetch on purpose. Two goroutines
// starting a batch at the same moment would otherwise both resolve;
// serializing means the second waits briefly and then reuses the first's
// answer, which is the whole point. Submits happen outside this lock, so
// the batches themselves still overlap freely.
func (c *jmapRouteCache) route(ctx context.Context, cfg Config, fromAddr string, refresh bool) (jmapRoute, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	fresh := c.resolved &&
		c.token == cfg.APIToken &&
		c.from == fromAddr &&
		time.Since(c.resolvedAt) < jmapRouteTTL
	if fresh && !refresh {
		return c.cached, nil
	}

	session, err := fetchJMAPSession(ctx, cfg.APIToken)
	if err != nil {
		return jmapRoute{}, fmt.Errorf("mailer: fetching JMAP session: %w", err)
	}
	accountID := session.PrimaryAccounts["urn:ietf:params:jmap:mail"]
	if accountID == "" {
		return jmapRoute{}, fmt.Errorf("mailer: JMAP session has no mail account for this API token")
	}

	identityID, draftsMailboxID, err := fetchIdentityAndDrafts(ctx, session.APIURL, cfg.APIToken, accountID, fromAddr)
	if err != nil {
		// Leave any previous entry invalidated rather than serving a
		// route the server has just disagreed with.
		c.resolved = false
		return jmapRoute{}, err
	}

	c.token, c.from = cfg.APIToken, fromAddr
	c.cached = jmapRoute{
		apiURL:          session.APIURL,
		accountID:       accountID,
		identityID:      identityID,
		draftsMailboxID: draftsMailboxID,
	}
	c.resolved, c.resolvedAt = true, time.Now()
	return c.cached, nil
}

// invalidate drops the cached route, so the next send re-resolves.
func (c *jmapRouteCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resolved = false
}

// sendViaFastmailJMAP sends one email through Fastmail's JMAP API
// (https://www.fastmail.com/dev/) instead of SMTP — see Config.Provider's
// doc comment for why a site would want this.
//
// The final request creates the email as a draft, submits it for
// delivery, and destroys the draft again on success — so nothing lingers
// in the account beyond what actually got sent, mirroring SMTP's own
// "fire and forget, no copy kept" behavior.
//
// A rejected-outright submit is retried once against a freshly resolved
// route, for the case where the cached one went stale mid-batch. See
// rejectedBeforeDelivery for why only that narrow class of failure is
// retried: anything else might already have delivered, and a duplicate
// recruiting email is worse than a reported failure.
func (m *Mailer) sendViaFastmailJMAP(ctx context.Context, cfg Config, to, subject, body, contentType string) error {
	fromAddr, err := extractAddr(cfg.From)
	if err != nil {
		return fmt.Errorf("mailer: invalid From address %q: %w", cfg.From, err)
	}

	route, err := m.jmapRoutes.route(ctx, cfg, fromAddr, false)
	if err != nil {
		return err
	}

	err = submitEmail(ctx, route.apiURL, cfg.APIToken, route.accountID,
		route.identityID, route.draftsMailboxID, cfg.From, to, subject, body, contentType)
	if err == nil || !rejectedBeforeDelivery(err) {
		return err
	}

	route, refreshErr := m.jmapRoutes.route(ctx, cfg, fromAddr, true)
	if refreshErr != nil {
		// Report what actually went wrong with the send, not the
		// follow-up failure to re-resolve.
		return err
	}
	return submitEmail(ctx, route.apiURL, cfg.APIToken, route.accountID,
		route.identityID, route.draftsMailboxID, cfg.From, to, subject, body, contentType)
}

// rejectedBeforeDelivery reports whether the API refused the request
// outright, which is the only failure safe to retry.
//
// submitEmail creates, submits and cleans up the draft in ONE JMAP
// request. If that request fails at any point after the server began
// acting on it, whether the mail went out is unknowable from here — so
// retrying could send a second copy. A 401 or 403 is different in kind:
// the server rejected the request at the door and did nothing, so no
// message can have been delivered and a retry is free.
func rejectedBeforeDelivery(err error) bool {
	var se *jmapStatusError
	return errors.As(err, &se) && (se.StatusCode == http.StatusUnauthorized || se.StatusCode == http.StatusForbidden)
}

// jmapSession is the subset of JMAP's session object (RFC 8620 §2) this
// package needs.
type jmapSession struct {
	APIURL          string            `json:"apiUrl"`
	PrimaryAccounts map[string]string `json:"primaryAccounts"`
}

func fetchJMAPSession(ctx context.Context, token string) (jmapSession, error) {
	reqCtx, cancel := context.WithTimeout(ctx, jmapHTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, fastmailSessionURL, nil)
	if err != nil {
		return jmapSession{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return jmapSession{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return jmapSession{}, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return jmapSession{}, fmt.Errorf("session endpoint returned %s: %s", resp.Status, truncate(string(body), 500))
	}

	var session jmapSession
	if err := json.Unmarshal(body, &session); err != nil {
		return jmapSession{}, fmt.Errorf("parsing session response: %w", err)
	}
	if session.APIURL == "" {
		return jmapSession{}, fmt.Errorf("session response has no apiUrl — is the API token valid?")
	}
	return session, nil
}

// jmapRequest/jmapResponse model just enough of JMAP's request/response
// envelope (RFC 8620 §3.3) to issue a batch of method calls and read
// their results back by call ID. A method call is the 3-element tuple
// [name, arguments, id]; Go has no fixed-size heterogeneous tuple type,
// so both are represented as []any / []json.RawMessage and indexed by
// position.
type jmapRequest struct {
	Using       []string `json:"using"`
	MethodCalls [][]any  `json:"methodCalls"`
}

type jmapResponse struct {
	MethodResponses [][]json.RawMessage `json:"methodResponses"`
}

// resultFor returns the raw JSON result object for the method call
// whose call ID matches wantID, or an error if the response never
// carries one — e.g. because a method call earlier in the batch failed
// outright and JMAP's error-propagation to a resultReference'd later
// call omitted it, or the server returned fewer responses than calls.
func (r jmapResponse) resultFor(wantID string) (json.RawMessage, error) {
	for _, mr := range r.MethodResponses {
		if len(mr) != 3 {
			continue
		}
		var name, id string
		if err := json.Unmarshal(mr[0], &name); err != nil {
			continue
		}
		if err := json.Unmarshal(mr[2], &id); err != nil {
			continue
		}
		if id != wantID {
			continue
		}
		if name == "error" {
			return nil, fmt.Errorf("JMAP method %q failed: %s", wantID, truncate(string(mr[1]), 500))
		}
		return mr[1], nil
	}
	return nil, fmt.Errorf("JMAP response has no result for call %q", wantID)
}

// jmapStatusError is a non-200 from the JMAP API, carrying the status
// code so a caller can tell an outright rejection (401/403 — the server
// did nothing) from a failure that may have had side effects. See
// rejectedBeforeDelivery.
type jmapStatusError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *jmapStatusError) Error() string {
	return fmt.Sprintf("API returned %s: %s", e.Status, e.Body)
}

func postJMAP(ctx context.Context, apiURL, token string, req jmapRequest) (jmapResponse, error) {
	reqCtx, cancel := context.WithTimeout(ctx, jmapHTTPTimeout)
	defer cancel()

	payload, err := json.Marshal(req)
	if err != nil {
		return jmapResponse{}, fmt.Errorf("encoding request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return jmapResponse{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return jmapResponse{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return jmapResponse{}, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return jmapResponse{}, &jmapStatusError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       truncate(string(body), 500),
		}
	}

	var jr jmapResponse
	if err := json.Unmarshal(body, &jr); err != nil {
		return jmapResponse{}, fmt.Errorf("parsing response: %w", err)
	}
	return jr, nil
}

// fetchIdentityAndDrafts looks up the Identity whose email matches
// fromAddr (case-insensitively — JMAP/email addresses are conventionally
// treated case-insensitively on the domain and typically on the local
// part too) and the account's Drafts mailbox, in one JMAP request.
// Erroring out on a mismatched From rather than silently substituting
// the account's default identity — sending from the wrong address
// unnoticed is worse than a clear startup-time-ish configuration error.
func fetchIdentityAndDrafts(ctx context.Context, apiURL, token, accountID, fromAddr string) (identityID, draftsMailboxID string, err error) {
	resp, err := postJMAP(ctx, apiURL, token, jmapRequest{
		// Identity lives under the submission capability (RFC 8621 §7),
		// not mail (§1.4) — Mailbox/query alone would work with just
		// "mail" declared, but Identity/get in the same request needs
		// "submission" too, or the server rejects that one call with
		// "unknownMethod: appropriate 'using' item not specified".
		Using: []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"},
		MethodCalls: [][]any{
			{"Identity/get", map[string]any{
				"accountId":  accountID,
				"properties": []string{"id", "email"},
			}, "id"},
			{"Mailbox/query", map[string]any{
				"accountId": accountID,
				"filter":    map[string]any{"role": "drafts"},
			}, "mb"},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("mailer: looking up JMAP identity/mailbox: %w", err)
	}

	idResult, err := resp.resultFor("id")
	if err != nil {
		return "", "", fmt.Errorf("mailer: %w", err)
	}
	var identities struct {
		List []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"list"`
	}
	if err := json.Unmarshal(idResult, &identities); err != nil {
		return "", "", fmt.Errorf("mailer: parsing Identity/get result: %w", err)
	}
	var available []string
	for _, ident := range identities.List {
		available = append(available, ident.Email)
		if strings.EqualFold(ident.Email, fromAddr) {
			identityID = ident.ID
			break
		}
	}
	if identityID == "" {
		return "", "", fmt.Errorf("mailer: no Fastmail identity matches SMTP_FROM address %q (this account's identities: %s) — SMTP_FROM must be one of this token's own addresses/aliases", fromAddr, strings.Join(available, ", "))
	}

	mbResult, err := resp.resultFor("mb")
	if err != nil {
		return "", "", fmt.Errorf("mailer: %w", err)
	}
	var mailboxes struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(mbResult, &mailboxes); err != nil {
		return "", "", fmt.Errorf("mailer: parsing Mailbox/query result: %w", err)
	}
	if len(mailboxes.IDs) == 0 {
		return "", "", fmt.Errorf("mailer: this account has no Drafts mailbox")
	}

	return identityID, mailboxes.IDs[0], nil
}

// submitEmail creates the message as a draft, submits it for delivery,
// and destroys the draft again on success — see sendViaFastmailJMAP's
// doc comment for why. "#draft"/"#submission" are JMAP creation-id
// back-references (RFC 8620 §3.6.1): resolved server-side to the ids
// the earlier Email/set and EmailSubmission/set creations are assigned,
// without a round trip in between.
func submitEmail(ctx context.Context, apiURL, token, accountID, identityID, draftsMailboxID, from, to, subject, body, contentType string) error {
	fromName, fromAddr := "", from
	if addr, err := mail.ParseAddress(from); err == nil {
		fromName, fromAddr = addr.Name, addr.Address
	}

	fromField := []map[string]string{{"email": fromAddr}}
	if fromName != "" {
		fromField[0]["name"] = fromName
	}

	emailObj := map[string]any{
		"mailboxIds": map[string]bool{draftsMailboxID: true},
		"keywords":   map[string]bool{"$draft": true},
		"from":       fromField,
		"to":         []map[string]string{{"email": to}},
		"subject":    subject,
		"bodyValues": map[string]any{
			"body": map[string]any{"value": body, "charset": "utf-8"},
		},
	}
	bodyPart := []map[string]string{{"partId": "body", "type": contentType}}
	if contentType == "text/html" {
		emailObj["htmlBody"] = bodyPart
	} else {
		emailObj["textBody"] = bodyPart
	}

	resp, err := postJMAP(ctx, apiURL, token, jmapRequest{
		Using: []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"},
		MethodCalls: [][]any{
			{"Email/set", map[string]any{
				"accountId": accountID,
				"create":    map[string]any{"draft": emailObj},
			}, "e0"},
			{"EmailSubmission/set", map[string]any{
				"accountId": accountID,
				"create": map[string]any{
					"submission": map[string]any{
						"emailId":    "#draft",
						"identityId": identityID,
					},
				},
				"onSuccessDestroyEmail": []string{"#submission"},
			}, "s0"},
		},
	})
	if err != nil {
		return fmt.Errorf("mailer: sending via JMAP: %w", err)
	}

	if err := checkSetNotCreated(resp, "e0", "draft", "creating the email"); err != nil {
		return err
	}
	if err := checkSetNotCreated(resp, "s0", "submission", "submitting the email"); err != nil {
		return err
	}
	return nil
}

// checkSetNotCreated surfaces a clear error if the *Set/set call
// identified by callID reports the creationID as failed via
// "notCreated" — e.g. a validation error JMAP caught (a malformed
// address, an over-length subject) rather than an HTTP/transport
// failure, which postJMAP would already have caught.
func checkSetNotCreated(resp jmapResponse, callID, creationID, what string) error {
	result, err := resp.resultFor(callID)
	if err != nil {
		return fmt.Errorf("mailer: %s: %w", what, err)
	}
	var setResult struct {
		NotCreated map[string]json.RawMessage `json:"notCreated"`
	}
	if err := json.Unmarshal(result, &setResult); err != nil {
		return fmt.Errorf("mailer: %s: parsing result: %w", what, err)
	}
	if reason, failed := setResult.NotCreated[creationID]; failed {
		return fmt.Errorf("mailer: %s: %s", what, truncate(string(reason), 500))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
