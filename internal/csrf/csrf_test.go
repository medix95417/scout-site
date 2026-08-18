package csrf

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func handlerUnderTest() http.Handler {
	return Middleware(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
