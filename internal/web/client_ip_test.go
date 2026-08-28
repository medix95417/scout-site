package web

import (
	"net/http/httptest"
	"testing"
)

// TestClientIP_IgnoresForwardedHeaderWhenNotBehindAProxy — if the app is
// reachable directly, X-Forwarded-For is just a header the visitor typed.
// Believing it would let anyone pick their own rate-limit bucket, so it's
// ignored entirely unless a proxy is known to be in front.
func TestClientIP_IgnoresForwardedHeaderWhenNotBehindAProxy(t *testing.T) {
	r := httptest.NewRequest("POST", "/fundraiser/order", nil)
	r.RemoteAddr = "203.0.113.9:54321"
	r.Header.Set("X-Forwarded-For", "1.1.1.1")

	if got := clientIP(r, false); got != "203.0.113.9" {
		t.Errorf("clientIP = %q, want the real peer 203.0.113.9 — the header must not be trusted here", got)
	}
}

// TestClientIP_UsesRightmostForwardedValue is the subtle one, and the
// reason this has its own test. A proxy APPENDS the address it actually
// saw, so the rightmost entry is the trustworthy one; everything to its
// left is whatever the client sent. Reading the leftmost value — the
// common mistake — would let a visitor spoof a different address on every
// request and never hit a limit.
func TestClientIP_UsesRightmostForwardedValue(t *testing.T) {
	cases := []struct {
		name string
		xff  string
		want string
	}{
		{"single value from our proxy", "198.51.100.7", "198.51.100.7"},
		{"client forged a prefix", "1.1.1.1, 198.51.100.7", "198.51.100.7"},
		{"several forged entries", "9.9.9.9, 8.8.8.8, 7.7.7.7, 198.51.100.7", "198.51.100.7"},
		{"spacing is tolerated", "1.1.1.1,198.51.100.7", "198.51.100.7"},
		{"IPv6 from the proxy", "2001:db8::1", "2001:db8::1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/fundraiser/order", nil)
			r.RemoteAddr = "10.0.0.2:443" // the proxy's own address on the docker network
			r.Header.Set("X-Forwarded-For", c.xff)

			if got := clientIP(r, true); got != c.want {
				t.Errorf("clientIP(%q) = %q, want %q", c.xff, got, c.want)
			}
		})
	}
}

// TestClientIP_FallsBackWhenHeaderIsUnusable — a missing or malformed
// header falls back to the peer address rather than returning something
// empty, which would put every such request into one shared bucket.
func TestClientIP_FallsBackWhenHeaderIsUnusable(t *testing.T) {
	for _, xff := range []string{"", "not-an-ip", "1.1.1.1, garbage"} {
		r := httptest.NewRequest("POST", "/fundraiser/order", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if got := clientIP(r, true); got != "203.0.113.9" {
			t.Errorf("clientIP with XFF %q = %q, want the peer address", xff, got)
		}
	}
}
