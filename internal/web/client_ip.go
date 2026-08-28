package web

import (
	"net"
	"net/http"
	"strings"
)

// clientIP works out who to attribute a request to for rate-limiting
// purposes, and it is deliberately fussy about it, because getting this
// wrong is worse than not rate-limiting at all: if a visitor can choose
// their own key, they can both evade their own limit and exhaust someone
// else's.
//
// trustProxy says whether this app sits behind a reverse proxy that is
// the ONLY way in (see the Caddyfile, and TRUST_PROXY_HEADERS in
// .env.example). When it doesn't, X-Forwarded-For is just a header the
// client typed and is ignored entirely.
//
// When it does, the RIGHTMOST value in X-Forwarded-For is used, not the
// leftmost. This is the part that's easy to get backwards. A proxy
// APPENDS the address it actually saw, so in
//
//	X-Forwarded-For: 1.2.3.4, 5.6.7.8
//
// the last entry is what our proxy observed and the earlier ones are
// whatever the client sent — attacker-controlled, and the usual way
// header-based limits get bypassed. Reading the rightmost value means a
// forged prefix is ignored.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			candidate := strings.TrimSpace(parts[len(parts)-1])
			if ip := net.ParseIP(candidate); ip != nil {
				return ip.String()
			}
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// No port (some test servers, some proxies) — use it as-is.
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}
