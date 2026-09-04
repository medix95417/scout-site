package csrf

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func handlerUnderTest() http.Handler {
	return handlerUnderTestAs(false)
}

// handlerUnderTestAs builds the middleware with a fixed answer to "is this
// request authenticated", which is what decides the request-body limit.
func handlerUnderTestAs(authed bool) http.Handler {
	return Middleware(false, func(*http.Request) bool { return authed })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
}

func TestMiddleware_GETIssuesCookie(t *testing.T) {
	h := handlerUnderTest()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET request should always pass, got status %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != CookieName {
		t.Fatalf("expected exactly one %s cookie to be set, got %+v", CookieName, cookies)
	}
	if cookies[0].Value == "" {
		t.Error("issued CSRF cookie has an empty value")
	}
	if !cookies[0].HttpOnly {
		t.Error("CSRF cookie should be HttpOnly")
	}
}

func TestMiddleware_POSTWithoutTokenRejected(t *testing.T) {
	h := handlerUnderTest()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST without a csrf_token should be rejected, got status %d", rec.Code)
	}
}

func TestMiddleware_POSTWithMismatchedTokenRejected(t *testing.T) {
	h := handlerUnderTest()

	// First, GET to obtain a cookie.
	getReq := httptest.NewRequest(http.MethodGet, "/", nil)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	cookie := getRec.Result().Cookies()[0]

	form := url.Values{"csrf_token": {"not-the-real-token"}}
	postReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusForbidden {
		t.Errorf("POST with a mismatched csrf_token should be rejected, got status %d", postRec.Code)
	}
}

func TestMiddleware_POSTWithMatchingTokenAccepted(t *testing.T) {
	h := handlerUnderTest()

	getReq := httptest.NewRequest(http.MethodGet, "/", nil)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	cookie := getRec.Result().Cookies()[0]

	form := url.Values{"csrf_token": {cookie.Value}}
	postReq := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusOK {
		t.Errorf("POST with a matching csrf_token should be accepted, got status %d", postRec.Code)
	}
}

func TestMiddleware_MultipartPOSTWithMatchingTokenAccepted(t *testing.T) {
	h := handlerUnderTest()

	getReq := httptest.NewRequest(http.MethodGet, "/", nil)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	cookie := getRec.Result().Cookies()[0]

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("csrf_token", cookie.Value); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/", &body)
	postReq.Header.Set("Content-Type", mw.FormDataContentType())
	postReq.AddCookie(cookie)
	postRec := httptest.NewRecorder()
	h.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusOK {
		t.Errorf("multipart POST with a matching csrf_token should be accepted, got status %d", postRec.Code)
	}
}

func TestMiddleware_MultipartPOSTWithoutTokenRejected(t *testing.T) {
	h := handlerUnderTest()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/", &body)
	postReq.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq)

	if rec.Code != http.StatusForbidden {
		t.Errorf("multipart POST without a csrf_token should be rejected, got status %d", rec.Code)
	}
}

func TestMiddleware_ReusesExistingCookie(t *testing.T) {
	h := handlerUnderTest()

	getReq := httptest.NewRequest(http.MethodGet, "/", nil)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	cookie := getRec.Result().Cookies()[0]

	// A second GET carrying that cookie should not issue a new one.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	if len(rec2.Result().Cookies()) != 0 {
		t.Error("Middleware re-issued a CSRF cookie when a valid one was already present")
	}
}

// An anonymous POST must not be able to make the server buffer an
// upload-sized body.
//
// The token can only be checked after the body is parsed — for multipart
// it is a field inside the body — so a request with a junk token still
// costs a full parse first. At the signed-in cap that is 250 MB of
// buffering and disk spill per request, from anyone, repeatable: a cheap
// way to fill a small VPS's disk. Signed-out traffic on this site is
// logins, password resets and join enquiries, all a few kilobytes.
func TestAnonymousPostIsCappedFarBelowTheUploadLimit(t *testing.T) {
	if maxAnonymousRequestBodySize >= maxRequestBodySize {
		t.Fatalf("anonymous cap (%d) must be well below the signed-in cap (%d)",
			maxAnonymousRequestBodySize, maxRequestBodySize)
	}

	// A body comfortably over the anonymous cap but under the signed-in one.
	big := strings.Repeat("x", maxAnonymousRequestBodySize+(1<<20))

	for _, tc := range []struct {
		name     string
		authed   bool
		wantCode int
	}{
		{"anonymous is refused as too large", false, http.StatusRequestEntityTooLarge},
		// Signed in, the same body is allowed through the size check and
		// then fails on the token instead — proving the cap, not the body,
		// is what changed.
		{"signed in gets the full cap", true, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := handlerUnderTestAs(tc.authed)

			form := url.Values{"csrf_token": {"definitely-not-valid"}, "padding": {big}}
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: CookieName, Value: "some-token-value"})

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Errorf("got %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}
