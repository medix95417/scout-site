package settings

import (
	"errors"
	"testing"
)

// TestWelcomeEmailBody_RequiresThePasswordPlaceholder pins the refusal.
// Without it a leader saves a nice-looking template, ticks "email login
// details", and a family receives an email with no password and no way to
// know why they can't sign in — with nothing erroring anywhere.
func TestWelcomeEmailBody_RequiresThePasswordPlaceholder(t *testing.T) {
	for body, wantErr := range map[string]bool{
		"Hi {{name}}, your password is {{password}}":        false,
		"<p>Hi {{name}}</p><p>Password: {{ password }}</p>": false,
		"<table><tr><td>{{password}}</td></tr></table>":     false,
		"Hi {{name}}, welcome to the pack!":                 true,
		"<p>Your login is {{email}}</p>":                    true,
		"<p>{{<b>password</b>}}</p>":                        true,
		"":                                                  false, // blank means "use the default", which has one
	} {
		err := validateUnitTextValue(WelcomeEmailBody, body)
		if wantErr && !errors.Is(err, ErrWelcomeEmailNeedsPassword) {
			t.Errorf("body %q should have been refused, got %v", body, err)
		}
		if !wantErr && err != nil {
			t.Errorf("body %q should have been accepted, got %v", body, err)
		}
	}
}

// The subject has no such requirement — a password in a subject line is a
// bad idea, and the check must not leak onto other fields.
func TestWelcomeEmailSubject_HasNoPasswordRequirement(t *testing.T) {
	if err := validateUnitTextValue(WelcomeEmailSubject, "Welcome to {{unit_name}}!"); err != nil {
		t.Errorf("a subject without {{password}} should be fine, got %v", err)
	}
}
