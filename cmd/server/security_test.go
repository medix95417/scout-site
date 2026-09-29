package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLoggerNeverIncludesCredentials(t *testing.T) {
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(old) })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /feed/{token}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("GET /reset-password", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := requestLogger(mux, mux)
	for _, target := range []string{"/feed/secret-calendar.ics?key=secret-query", "/reset-password?token=secret-reset", "/missing/secret-unmatched", "/feed//secret-redirect"} {
		output.Reset()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", target, nil))
		if strings.Contains(output.String(), "secret-") {
			t.Fatalf("credential leaked: %s", output.String())
		}
	}
	output.Reset()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/feed/secret-calendar.ics", nil))
	if !strings.Contains(output.String(), "GET /feed/{token}") {
		t.Fatalf("missing useful route name: %s", output.String())
	}
}

func TestSecurityHeadersDefaultToNoStoreIncludingErrors(t *testing.T) {
	for _, status := range []int{200, 302, 403, 404, 500} {
		h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/reset-password?token=private", nil))
		if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("status %d cache policy %q", status, got)
		}
		if w.Header().Get("Strict-Transport-Security") != "" {
			t.Error("local HTTP must not set HSTS")
		}
	}
}
