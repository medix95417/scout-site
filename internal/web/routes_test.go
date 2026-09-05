package web

import (
	"net/http"
	"testing"
)

// Every route the site serves, registered exactly as New does it.
//
// Go's ServeMux panics at registration time when two patterns are
// ambiguous — when both match some path and neither is more specific.
// That is not a 404 or a misrouted request: it is a process that does not
// start, and until this test existed the only thing that discovered it
// was deploying.
//
// The trap is easy to walk into and invisible on inspection, because the
// two patterns look nothing alike. "/admin/prospects/campaigns/{id}" and
// "/admin/prospects/{id}/delete" collide on
// "/admin/prospects/campaigns/delete"; "/admin/calendar-feeds/conflicts/{id}"
// and "/admin/calendar-feeds/{id}/refresh" collide the same way. Both
// were written, and both were caught here rather than in production —
// which is the entire argument for the test.
func TestRoutesRegisterWithoutPanic(t *testing.T) {
	// A zero Handlers is enough: Routes only reads method values to
	// register them, and never calls one.
	h := &Handlers{}
	mux := http.NewServeMux()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registering routes panicked, so the server would not start:\n%v", r)
		}
	}()
	h.Routes(mux)
}
