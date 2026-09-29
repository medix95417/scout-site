package web

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/47-yonkers/scout-site/internal/units"
)

type navigationLink struct {
	Label   string
	URL     string
	Current bool
}

func publicNavigation(path string) []navigationLink {
	links := []navigationLink{
		{Label: "Home", URL: "/"},
		{Label: "News", URL: "/news"},
		{Label: "Photos", URL: "/gallery"},
		{Label: "Our Leaders", URL: "/leaders"},
		{Label: "Calendar", URL: "/calendar"},
		{Label: "Resources", URL: "/resources"},
	}
	for i := range links {
		links[i].Current = path == links[i].URL || (links[i].URL != "/" && strings.HasPrefix(path, links[i].URL+"/"))
	}
	return links
}

// Unit switches always land on the public homepage. Never carry an object ID,
// form, query string, or private-page path into a different unit.
func unitHomeURL(unit units.Unit, r *http.Request, secure bool) string {
	scheme := "http"
	if secure {
		scheme = "https"
	}
	host := unit.Hostname
	if !secure {
		if _, port, err := net.SplitHostPort(r.Host); err == nil && !strings.Contains(host, ":") {
			host = net.JoinHostPort(host, port)
		}
	}
	return (&url.URL{Scheme: scheme, Host: host, Path: "/"}).String()
}

func (h *Handlers) unitNavigation(r *http.Request, current units.Unit) []navigationLink {
	all, err := units.List(r.Context(), h.Pool)
	if err != nil {
		log.Printf("web: loading unit navigation: %v", err)
		return []navigationLink{{Label: current.Name, URL: "/", Current: true}}
	}
	links := make([]navigationLink, 0, len(all))
	for _, unit := range all {
		href := unitHomeURL(unit, r, h.SecureCookie)
		if unit.ID == current.ID {
			href = "/"
		}
		links = append(links, navigationLink{Label: unit.Name, URL: href, Current: unit.ID == current.ID})
	}
	return links
}
