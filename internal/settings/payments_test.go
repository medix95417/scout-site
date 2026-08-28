package settings

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateUnitTextValue_RejectsLiveStripeSecret covers F6: this
// codebase has no checkout flow and no webhook handler, so a live secret
// key entered here would be read by nothing while sitting in the database
// and in every backup taken from then on. Until PaymentsIntegrationLive
// says otherwise, a live secret key is refused with an explanation.
func TestValidateUnitTextValue_RejectsLiveStripeSecret(t *testing.T) {
	if PaymentsIntegrationLive {
		t.Skip("payments integration is live — this guard is intentionally off")
	}

	if err := validateUnitTextValue(StripeSecretKey, "sk_live_51abcdefghijklmnop"); !errors.Is(err, ErrLiveKeyNotAccepted) {
		t.Errorf("a live secret key should be refused, got %v", err)
	}

	accepted := []struct{ key, value string }{
		// A test key is the whole point — a unit can configure everything
		// except the live credential ahead of the integration landing.
		{StripeSecretKey, "sk_test_51abcdefghijklmnop"},
		// A publishable key is designed to be visible in a browser, so
		// it's harmless at rest and staging it now is fine.
		{StripePublishableKey, "pk_live_51abcdefghijklmnop"},
		{StripeWebhookSigningSecret, "whsec_abcdefghijklmnop"},
		{PayPalClientSecret, "some-paypal-secret"},
		// Nothing to do with payments at all.
		{SocialFacebookURL, "https://facebook.com/troop47"},
		{WelcomeEmailSubject, "Welcome to {{unit_name}}!"},
		// Blank means "leave the stored value alone" upstream.
		{StripeSecretKey, ""},
	}
	for _, c := range accepted {
		if err := validateUnitTextValue(c.key, c.value); err != nil {
			t.Errorf("validateUnitTextValue(%q, %q) = %v, want nil", c.key, c.value, err)
		}
	}
}

// TestErrLiveKeyNotAccepted_ExplainsItself — this message is shown
// straight to an admin who just pasted a key, so it has to say what to do
// next rather than only what went wrong.
func TestErrLiveKeyNotAccepted_ExplainsItself(t *testing.T) {
	msg := ErrLiveKeyNotAccepted.Error()
	for _, want := range []string{"sk_test_", "backup"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error message should mention %q; it reads: %s", want, msg)
		}
	}
}
