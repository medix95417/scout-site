package web

import (
	"net/http/httptest"
	"testing"
)

// TestForgotPasswordTemplate_RendersEveryDataShape guards against exactly
// the bug this once had: before forgotPasswordData existed as one shared
// named type, ForgotPasswordSubmit's success path built its own ad hoc
// anonymous struct that omitted a Disabled field the template evaluates
// unconditionally ({{if .Disabled}}...{{else if .Submitted}}...{{end}}).
// That made html/template fail at *execute* time (not parse time — see
// New's own template-parse check elsewhere, which this class of bug slips
// past entirely) for that one render only. render then logged "web:
// template execution error" and served a generic 500 "internal error" —
// exactly what a family saw trying to reset their password, with no
// indication anywhere that the actual cause was a template/data mismatch
// rather than anything to do with mail delivery.
//
// Uses forgotPasswordData itself (the real type every call site now
// shares), not a hand-copied struct shape — so this test actually
// exercises what the handlers build, rather than just proving the
// template can work with some well-formed struct.
func TestForgotPasswordTemplate_RendersEveryDataShape(t *testing.T) {
	h, err := New(nil, "", false, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := []struct {
		name string
		data forgotPasswordData
	}{
		{"initial form", forgotPasswordData{baseData: baseData{PageTitle: "Forgot Password"}}},
		{"submitted", forgotPasswordData{baseData: baseData{PageTitle: "Forgot Password"}, Submitted: true}},
		{"disabled", forgotPasswordData{baseData: baseData{PageTitle: "Forgot Password"}, Disabled: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.render(rec, h.forgotPassword, c.data)
			if rec.Code != 200 {
				t.Errorf("render produced status %d (a template execution error becomes a 500 — see render's own log line), body: %s", rec.Code, rec.Body.String())
			}
		})
	}
}
