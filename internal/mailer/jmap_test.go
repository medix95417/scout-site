package mailer

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigEnabled_FastmailJMAP(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"token and from set", Config{Provider: ProviderFastmailJMAP, APIToken: "tok", From: "a@example.com"}, true},
		{"missing token", Config{Provider: ProviderFastmailJMAP, From: "a@example.com"}, false},
		{"missing from", Config{Provider: ProviderFastmailJMAP, APIToken: "tok"}, false},
		{"SMTP host is irrelevant for this provider", Config{Provider: ProviderFastmailJMAP, APIToken: "tok", From: "a@example.com", Host: ""}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.cfg.Enabled(); got != c.want {
				t.Errorf("Enabled() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestJMAPResponse_ResultFor(t *testing.T) {
	resp := jmapResponse{MethodResponses: [][]json.RawMessage{
		{jrm(`"Identity/get"`), jrm(`{"list":[]}`), jrm(`"id"`)},
		{jrm(`"error"`), jrm(`{"type":"unknownMethod"}`), jrm(`"mb"`)},
	}}

	t.Run("found", func(t *testing.T) {
		result, err := resp.resultFor("id")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(result) != `{"list":[]}` {
			t.Errorf("got %s", result)
		}
	})
	t.Run("method-level error surfaced", func(t *testing.T) {
		if _, err := resp.resultFor("mb"); err == nil || !strings.Contains(err.Error(), "unknownMethod") {
			t.Errorf("expected an error mentioning unknownMethod, got %v", err)
		}
	})
	t.Run("missing call id", func(t *testing.T) {
		if _, err := resp.resultFor("nope"); err == nil {
			t.Error("expected an error for a call id with no matching response")
		}
	})
}

func jrm(s string) json.RawMessage { return json.RawMessage(s) }

func TestCheckSetNotCreated(t *testing.T) {
	t.Run("created successfully", func(t *testing.T) {
		resp := jmapResponse{MethodResponses: [][]json.RawMessage{
			{jrm(`"Email/set"`), jrm(`{"created":{"draft":{"id":"e1"}}}`), jrm(`"e0"`)},
		}}
		if err := checkSetNotCreated(resp, "e0", "draft", "creating"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("notCreated surfaces the reason", func(t *testing.T) {
		resp := jmapResponse{MethodResponses: [][]json.RawMessage{
			{jrm(`"Email/set"`), jrm(`{"notCreated":{"draft":{"type":"invalidProperties","description":"bad subject"}}}`), jrm(`"e0"`)},
		}}
		err := checkSetNotCreated(resp, "e0", "draft", "creating")
		if err == nil || !strings.Contains(err.Error(), "bad subject") {
			t.Errorf("expected an error mentioning the notCreated reason, got %v", err)
		}
	})
}

// jmapTestServer builds a fake Fastmail: /session for session discovery
// and /api for JMAP method calls, routing by the first call's method
// name since that's what differs between fetchIdentityAndDrafts' batch
// and submitEmail's. Returns the server (caller must Close it) and a
// pointer to the last JMAP request body posted to /api, for assertions.
func jmapTestServer(t *testing.T, identityEmail string) (*httptest.Server, *[]byte) {
	t.Helper()
	var lastAPIBody []byte

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("session request Authorization = %q, want Bearer test-token", got)
		}
		fmt.Fprintf(w, `{"apiUrl":%q,"primaryAccounts":{"urn:ietf:params:jmap:mail":"acct1"}}`, srv.URL+"/api")
	})
	// requiredCapability mirrors a real JMAP server's own capability
	// gating (RFC 8620 §3.3, RFC 8621 §7) — a method call whose owning
	// capability isn't in the request's top-level "using" gets rejected
	// with "unknownMethod", even if every other call in the same batch
	// is fine. This is exactly the bug this fake server exists to catch:
	// an earlier version of fetchIdentityAndDrafts called Identity/get
	// (submission) while only declaring the mail capability, which a
	// less faithful fake (one that only checked the method name, not
	// "using") would never have caught.
	requiredCapability := map[string]string{
		"Identity/get":        "urn:ietf:params:jmap:submission",
		"Mailbox/query":       "urn:ietf:params:jmap:mail",
		"Email/set":           "urn:ietf:params:jmap:mail",
		"EmailSubmission/set": "urn:ietf:params:jmap:submission",
	}

	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lastAPIBody = body
		var req jmapRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("server: parsing request: %v", err)
		}
		if len(req.MethodCalls) == 0 {
			t.Fatal("server: empty methodCalls")
		}

		using := map[string]bool{}
		for _, capability := range req.Using {
			using[capability] = true
		}
		for _, call := range req.MethodCalls {
			method, _ := call[0].(string)
			id, _ := call[2].(string)
			need := requiredCapability[method]
			if need != "" && !using[need] {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"methodResponses":[["error", {"type":"unknownMethod","description":"method exists, but appropriate 'using' item not specified for object"}, %q]]}`, id)
				return
			}
		}

		firstMethod, _ := req.MethodCalls[0][0].(string)

		w.Header().Set("Content-Type", "application/json")
		switch firstMethod {
		case "Identity/get":
			fmt.Fprintf(w, `{"methodResponses":[
				["Identity/get", {"list":[{"id":"ident1","email":%q}]}, "id"],
				["Mailbox/query", {"ids":["drafts1"]}, "mb"]
			]}`, identityEmail)
		case "Email/set":
			fmt.Fprint(w, `{"methodResponses":[
				["Email/set", {"created":{"draft":{"id":"email1"}}}, "e0"],
				["EmailSubmission/set", {"created":{"submission":{"id":"sub1"}}}, "s0"]
			]}`)
		default:
			t.Fatalf("server: unexpected first method %q", firstMethod)
		}
	})
	srv = httptest.NewServer(mux)
	return srv, &lastAPIBody
}

func TestSendViaFastmailJMAP_Success(t *testing.T) {
	srv, lastBody := jmapTestServer(t, "sender@example.com")
	defer srv.Close()

	origSession := fastmailSessionURL
	fastmailSessionURL = srv.URL + "/session"
	defer func() { fastmailSessionURL = origSession }()

	cfg := Config{
		Provider: ProviderFastmailJMAP,
		APIToken: "test-token",
		From:     "Troop 47 <sender@example.com>",
	}
	m := New(cfg, nil)
	if err := m.sendViaFastmailJMAP(t.Context(), cfg, "family@example.com", "Subject", "Hello", "text/plain"); err != nil {
		t.Fatalf("sendViaFastmailJMAP: %v", err)
	}

	if !strings.Contains(string(*lastBody), `"drafts1":true`) {
		t.Errorf("expected the create call to target the resolved Drafts mailbox, body: %s", *lastBody)
	}
	if !strings.Contains(string(*lastBody), `"identityId":"ident1"`) {
		t.Errorf("expected the submission to use the resolved identity id, body: %s", *lastBody)
	}
	if !strings.Contains(string(*lastBody), `"onSuccessDestroyEmail":["#submission"]`) {
		t.Errorf("expected the draft to be destroyed on success, body: %s", *lastBody)
	}
}

func TestSendViaFastmailJMAP_IdentityMismatch(t *testing.T) {
	// The account's only identity is a different address than From —
	// this must fail loudly (send from the wrong address unnoticed
	// would be worse) rather than silently substituting it.
	srv, _ := jmapTestServer(t, "someone-else@example.com")
	defer srv.Close()

	origSession := fastmailSessionURL
	fastmailSessionURL = srv.URL + "/session"
	defer func() { fastmailSessionURL = origSession }()

	cfg := Config{
		Provider: ProviderFastmailJMAP,
		APIToken: "test-token",
		From:     "sender@example.com",
	}
	err := New(cfg, nil).sendViaFastmailJMAP(t.Context(), cfg, "family@example.com", "Subject", "Hello", "text/plain")
	if err == nil || !strings.Contains(err.Error(), "no Fastmail identity matches") {
		t.Errorf("expected an identity-mismatch error, got %v", err)
	}
}
