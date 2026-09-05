package web

// The way out of a recruiting email.
//
// The join form promises the address will only be used to talk about
// membership and that the promise can be withdrawn at any time. A
// promise a family can only act on by asking the unit nicely is not one
// they control, so every campaign message carries a link that does it
// directly, and the link works without a login because the people
// holding it don't have one.

import (
	"errors"
	"html"
	"log"
	"net/http"
	"strings"

	"github.com/47-yonkers/scout-site/internal/prospect"
)

// unsubscribeFooterMarker keeps the footer from being appended twice if a
// body is ever re-personalized.
const unsubscribeFooterMarker = "<!--unsubscribe-footer-->"

// appendUnsubscribeFooter puts the opt-out link at the bottom of one
// recipient's copy of a campaign.
//
// Appended per recipient at send time rather than stored in the body,
// because the link is specific to the person: a shared one would let
// anybody unsubscribe anybody, and a stored one would put a live opt-out
// token in the drafts table.
//
// The text says what the address was used for as well as offering the
// link, because "unsubscribe" on its own doesn't remind anybody why they
// are hearing from a Scouting unit they contacted eight months ago.
func appendUnsubscribeFooter(body, siteURL string, r prospect.Recipient, secret []byte) string {
	if strings.Contains(body, unsubscribeFooterMarker) {
		return body
	}
	token := prospect.UnsubscribeToken(secret, r.ProspectID)
	link := siteURL + "/unsubscribe?p=" + html.EscapeString(r.ProspectID) + "&t=" + html.EscapeString(token)

	return body + unsubscribeFooterMarker + `
<hr style="margin-top:2em;border:none;border-top:1px solid #ddd">
<p style="font-size:12px;color:#666">
You're receiving this because you asked us about joining. We only use your address to talk to you about
membership, and you can stop it at any time —
<a href="` + link + `">unsubscribe from these emails</a>.
</p>`
}

// unsubscribeData is the page a family lands on after clicking the link.
type unsubscribeData struct {
	baseData
	OK bool
	// Email is shown back so somebody who manages several addresses can
	// see which one they just took off the list. Never a different
	// prospect's — it comes from the row the token authorized.
	Email string
	// AlreadyDone distinguishes "we've just done it" from "this was
	// already done", which matters to anyone who clicks twice or whose
	// mail client pre-fetched the link.
	AlreadyDone bool
}

// ProspectUnsubscribe honours the link.
//
// Deliberately a GET that changes state, which is normally the wrong
// thing: a POST here would need a form and a click, and a one-click
// unsubscribe that actually works in one click is worth more than the
// convention. The HMAC is what makes it safe to act on — there is no
// ambient authority to abuse, since the visitor is signed out and the
// only thing the link can do is stop email to the one address it names.
//
// Not behind CSRF for the same reason: there is no session to ride.
func (h *Handlers) ProspectUnsubscribe(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("p")
	token := r.URL.Query().Get("t")

	data := unsubscribeData{baseData: h.base(r, "Unsubscribe")}
	if id == "" || token == "" {
		h.renderUnsubscribe(w, data)
		return
	}

	// Read the current state first, so a second click can say "already
	// done" rather than claiming to have done something.
	before, err := prospect.GetAnyUnit(r.Context(), h.Pool, id)
	if err == nil && before.EmailOptOut {
		data.OK = true
		data.AlreadyDone = true
		data.Email = before.ParentEmail
		// Still verify the token before echoing the address back —
		// otherwise this page would confirm whether a guessed id belongs
		// to a real prospect, and hand out their email with it.
		if prospect.UnsubscribeToken(h.UnsubscribeSecret, id) != token {
			data.OK = false
			data.Email = ""
			data.AlreadyDone = false
		}
		h.renderUnsubscribe(w, data)
		return
	}

	p, err := prospect.Unsubscribe(r.Context(), h.Pool, h.UnsubscribeSecret, id, token)
	switch {
	case errors.Is(err, prospect.ErrBadUnsubscribeToken):
		// Nothing more specific on purpose: a bad token and a
		// non-existent prospect must look identical, or this page becomes
		// a way to test whether an address enquired.
		h.renderUnsubscribe(w, data)
		return
	case err != nil:
		log.Printf("web: unsubscribing prospect: %v", err)
		h.renderUnsubscribe(w, data)
		return
	}

	data.OK = true
	data.Email = p.ParentEmail
	h.renderUnsubscribe(w, data)
}

func (h *Handlers) renderUnsubscribe(w http.ResponseWriter, data unsubscribeData) {
	// Never indexed and never cached: the URL carries a token, and a
	// shared cache holding the page would hold the address with it.
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	h.render(w, h.unsubscribed, data)
}
