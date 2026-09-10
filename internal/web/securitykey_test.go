package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/47-yonkers/scout-site/internal/auth"
)

// rpIDFor decides which domain a key is bound to; get it wrong and a key
// registered on one subdomain silently fails on the other.
func TestRPIDFor(t *testing.T) {
	cases := []struct {
		name, override, cookie string
		hosts                  []string
		want                   string
	}{
		{"cookie domain wins", "", ".47-yonkers.org", []string{"troop.47-yonkers.org", "pack.47-yonkers.org"}, "47-yonkers.org"},
		{"override wins over cookie", "keys.example.org", ".47-yonkers.org", []string{"troop.47-yonkers.org"}, "keys.example.org"},
		{"local dev shares localhost", "", "", []string{"troop.localhost", "pack.localhost"}, "localhost"},
		{"shared parent without a cookie domain", "", "", []string{"troop.47-yonkers.org", "pack.47-yonkers.org"}, "47-yonkers.org"},
		{"single host", "", "", []string{"scouts.example.org"}, "scouts.example.org"},
		{"unrelated hosts fall back to the first", "", "", []string{"a.example.org", "b.example.net"}, "a.example.org"},
		{"nothing at all", "", "", nil, "localhost"},
		{"case and whitespace", "", " .Example.ORG ", []string{"X.EXAMPLE.ORG"}, "Example.ORG"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rpIDFor(c.override, c.cookie, c.hosts); got != c.want {
				t.Errorf("rpIDFor(%q, %q, %v) = %q, want %q", c.override, c.cookie, c.hosts, got, c.want)
			}
		})
	}
}

// The settings page must render with and without keys, and offer the
// password step-up once anything is set up.
func TestTwoFactorSettingsRendersWithKeys(t *testing.T) {
	used := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	out := renderPage(t, "two-factor-settings.html", twoFactorSettingsData{
		baseData:  testBase("Two-Factor Authentication"),
		Confirmed: false,
		Keys: []auth.SecurityKey{
			{ID: "k1", Name: "blue YubiKey", CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), LastUsedAt: &used},
			{ID: "k2", Name: "spare", CreatedAt: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)},
		},
		StepUp: true,
	})
	for _, want := range []string{
		"blue YubiKey", "spare", "not used yet", "last used 1 Sep 2026",
		`action="/settings/2fa/keys/k1/delete"`, `id="key-password"`, `id="enroll-password"`,
		"scoutWebAuthn", "/settings/2fa/keys/begin",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("settings page with keys is missing %q", want)
		}
	}

	bare := renderPage(t, "two-factor-settings.html", twoFactorSettingsData{baseData: testBase("Two-Factor Authentication")})
	if strings.Contains(bare, `id="key-password"`) || strings.Contains(bare, `id="enroll-password"`) {
		t.Error("a login with nothing set up must not be asked for a password to add its first factor")
	}
	if !strings.Contains(bare, "No security key registered yet") {
		t.Error("the empty state is missing")
	}
}

// The login page offers whichever second steps the login has.
func TestLoginTwoFactorOffersWhatIsEnrolled(t *testing.T) {
	keyOnly := renderPage(t, "login-two-factor.html", loginTwoFactorData{baseData: testBase("Two-Factor Verification"), HasKeys: true})
	if !strings.Contains(keyOnly, "Use your security key") || !strings.Contains(keyOnly, "/login/2fa/key/begin") {
		t.Error("a key-only login must be offered its key")
	}
	if !strings.Contains(keyOnly, "Backup code") || strings.Contains(keyOnly, "6-digit code from your authenticator app") {
		t.Error("a key-only login's code box is for backup codes, not app codes")
	}

	appOnly := renderPage(t, "login-two-factor.html", loginTwoFactorData{baseData: testBase("Two-Factor Verification"), HasTOTP: true})
	if strings.Contains(appOnly, "Use your security key") || strings.Contains(appOnly, "scoutWebAuthn") {
		t.Error("an app-only login must not be offered a key it does not have")
	}
	if !strings.Contains(appOnly, "6-digit code from your authenticator app") {
		t.Error("an app-only login should be asked for its app code")
	}

	both := renderPage(t, "login-two-factor.html", loginTwoFactorData{baseData: testBase("Two-Factor Verification"), HasTOTP: true, HasKeys: true})
	if !strings.Contains(both, "Use your security key") || !strings.Contains(both, "6-digit code from your authenticator app") {
		t.Error("a login with both should be offered both")
	}
}

// The gates that are one call each, and that everything else would pass
// without. Read from the source, as with the other guards in this
// package.
func TestSecurityKeyGatesAreCalled(t *testing.T) {
	calls := func(file, fn string) map[string]bool {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}
		var decl *ast.FuncDecl
		ast.Inspect(f, func(n ast.Node) bool {
			if d, ok := n.(*ast.FuncDecl); ok && d.Name.Name == fn {
				decl = d
				return false
			}
			return true
		})
		if decl == nil {
			t.Fatalf("%s is gone from %s; if it moved, move this guard with it", fn, file)
		}
		seen := map[string]bool{}
		ast.Inspect(decl, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					seen[fun.Sel.Name] = true
					if pkg, ok := fun.X.(*ast.Ident); ok {
						seen[pkg.Name+"."+fun.Sel.Name] = true
					}
				case *ast.Ident:
					seen[fun.Name] = true
				}
			}
			return true
		})
		return seen
	}

	// A key-only login must be sent through the second step: the login
	// flow asks HasSecondFactor, never TOTPStatus.
	c := calls("web.go", "completeLogin")
	if !c["auth.HasSecondFactor"] {
		t.Error("completeLogin must decide the second step with auth.HasSecondFactor")
	}
	if c["auth.TOTPStatus"] {
		t.Error("completeLogin must not decide the second step with auth.TOTPStatus — a key-only login would walk straight in")
	}

	// Finishing a key login: take the one-shot challenge, validate, and
	// spend the pending login before issuing a session.
	c = calls("securitykey.go", "LoginSecurityKeyFinish")
	for _, want := range []string{"auth.TakeWebAuthnSession", "ValidateLogin", "auth.ConsumePendingTwoFactorLogin", "auth.CreateSession"} {
		if !c[want] {
			t.Errorf("LoginSecurityKeyFinish no longer calls %s", want)
		}
	}
	c = calls("securitykey.go", "SecurityKeyFinish")
	for _, want := range []string{"auth.TakeWebAuthnSession", "CreateCredential", "auth.AddSecurityKey"} {
		if !c[want] {
			t.Errorf("SecurityKeyFinish no longer calls %s", want)
		}
	}

	// Adding or removing a key steps up to the password once a factor exists.
	if c := calls("securitykey.go", "SecurityKeyBegin"); !c["securityKeyStepUp"] {
		t.Error("SecurityKeyBegin must call securityKeyStepUp")
	}
	if c := calls("securitykey.go", "SecurityKeyDelete"); !c["auth.VerifyPassword"] {
		t.Error("SecurityKeyDelete must verify the password")
	}
	if c := calls("securitykey.go", "securityKeyStepUp"); !c["auth.HasSecondFactor"] || !c["auth.VerifyPassword"] {
		t.Error("securityKeyStepUp must ask HasSecondFactor and then VerifyPassword")
	}
}
