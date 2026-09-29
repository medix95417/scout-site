package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/47-yonkers/scout-site/internal/audit"
	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/bootstrap"
	"github.com/47-yonkers/scout-site/internal/units"
)

func TestSharedLoginHasNoCapabilitiesOrRosterScopeWithoutDatabase(t *testing.T) {
	h := &Handlers{} // A shared login must not even consult role overrides.
	user := auth.User{FamilyID: "household"}
	caps, err := h.capabilitiesFor(context.Background(), user, "unit")
	if err != nil || len(caps) != 0 {
		t.Fatalf("shared capabilities: %v, %v", caps, err)
	}
	scope, err := h.rosterScope(context.Background(), user, "unit")
	if err != nil || scope.UnitWide || len(scope.SubGroupIDs) != 0 {
		t.Fatalf("shared management scope: %+v, %v", scope, err)
	}
}

func TestScriptsAreLocalNonceProtectedEmbeddedAssets(t *testing.T) {
	entries, err := templatesFS.ReadDir("templates")
	if err != nil {
		t.Fatal(err)
	}
	script := regexp.MustCompile(`<script\b[^>]*\bsrc="([^"]+)"[^>]*>`)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := templatesFS.ReadFile("templates/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range script.FindAllStringSubmatch(string(data), -1) {
			if !strings.HasPrefix(match[1], "/static/vendor/") || !strings.Contains(match[0], `nonce="{{.CSPNonce}}"`) {
				t.Errorf("unsafe script tag in %s: %s", entry.Name(), match[0])
				continue
			}
			if _, err := staticFS.ReadFile(strings.TrimPrefix(match[1], "/")); err != nil {
				t.Errorf("missing local script %s: %v", match[1], err)
			}
		}
	}
	css, err := staticFS.ReadFile("static/vendor/tailwind.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{`.hidden`, `.grid-cols-7`, `.sm\:grid-cols-2`, `.sm\:py-56`} {
		// Include classes from templates and Go helpers, not just literal HTML.
		if !strings.Contains(string(css), selector) {
			t.Errorf("compiled CSS missing %s", selector)
		}
	}
}

func TestUserFilesNeverPermitBrowserCaching(t *testing.T) {
	for _, ct := range []string{"image/jpeg", "application/pdf", "text/html"} {
		w := httptest.NewRecorder()
		writeUserFileHeaders(w, ct, "file")
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Errorf("cache permitted for %s", ct)
		}
	}
}

// Exercises real sessions and unit resolution so household and individual
// authority cannot get mixed up by middleware or configurable role overrides.
func TestPersonalLeadershipAndSharedMembership(t *testing.T) {
	ctx := context.Background()
	f := newFolderFixture(t, fmt.Sprintf("personal-leader-%d", time.Now().UnixNano()))
	p := f.h.Pool
	var familyID, adultID, childID string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(p.QueryRow(ctx, `INSERT INTO families(name) VALUES ('Security test') RETURNING id`).Scan(&familyID))
	t.Cleanup(func() {
		_, _ = p.Exec(ctx, `DELETE FROM audit_log WHERE entity_id=$1`, adultID)
		_, _ = p.Exec(ctx, `DELETE FROM families WHERE id=$1`, familyID)
	})
	must(p.QueryRow(ctx, `INSERT INTO members(family_id,first_name,last_name,member_type) VALUES($1,'Adult','Leader','adult') RETURNING id`, familyID).Scan(&adultID))
	must(p.QueryRow(ctx, `INSERT INTO members(family_id,first_name,last_name,member_type) VALUES($1,'Child','Scout','youth') RETURNING id`, familyID).Scan(&childID))
	_, err := p.Exec(ctx, `INSERT INTO role_assignments(member_id,unit_id,role) VALUES($1,$3,'super_admin'),($2,$3,'scout')`, adultID, childID, f.unitID)
	must(err)
	// Even an override granting parent full administrative power must not
	// give that authority to the shared login's membership marker.
	_, err = p.Exec(ctx, `INSERT INTO role_capability_overrides(unit_id,role_slug,capabilities) VALUES($1,'parent',ARRAY['super_admin','manage_ledger'])`, f.unitID)
	must(err)
	var host string
	must(p.QueryRow(ctx, `SELECT hostname FROM units WHERE id=$1`, f.unitID).Scan(&host))
	tokens := map[string]string{}
	users := map[string]string{}
	for name, member := range map[string]any{"shared": nil, "adult": adultID, "child": childID} {
		var id string
		email := fmt.Sprintf("%s-%s@test.invalid", name, f.unitID)
		if name == "adult" {
			password, err := bootstrap.CreatePersonalLogin(ctx, p, adultID, email)
			must(err)
			u, found, err := auth.UserByEmail(ctx, p, email)
			must(err)
			if !found || u.MemberID == nil || *u.MemberID != adultID || !u.MustChangePassword || !auth.VerifyPassword(u, password) {
				t.Fatal("personal-login transition failed")
			}
			id = u.ID
			if _, err := bootstrap.CreatePersonalLogin(ctx, p, adultID, email); err == nil {
				t.Fatal("existing login was overwritten")
			}
		} else {
			must(p.QueryRow(ctx, `INSERT INTO users(family_id,member_id,email,password_hash) VALUES($1,$2,$3,'unused') RETURNING id`, familyID, member, email).Scan(&id))
		}

		token, _, err := auth.CreateSession(ctx, p, id)
		must(err)
		tokens[name] = token
		users[name] = id
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin-check", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := f.h.requireSuperAdmin(w, r, "/admin-check")
		if !ok {
			return
		}
		audit.Log(r.Context(), p, audit.Entry{EntityType: "member", EntityID: adultID, ActorID: &actor.ID, Action: "security_test"})
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /member-check", func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := f.h.requireUnitMember(w, r, "/member-check"); ok {
			w.WriteHeader(204)
		}
	})
	handler := units.Middleware(p)(auth.WithUser(p)(mux))
	request := func(name, path, hostname string) int {
		r := httptest.NewRequest("GET", "http://"+hostname+path, nil)
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tokens[name]})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	for _, tc := range []struct {
		name, path string
		want       int
	}{{"shared", "/admin-check", 403}, {"child", "/admin-check", 403}, {"adult", "/admin-check", 204}, {"shared", "/member-check", 204}, {"child", "/member-check", 204}} {
		if got := request(tc.name, tc.path, host); got != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.name, tc.path, got, tc.want)
		}
	}
	var loggedUser, loggedActor string
	must(p.QueryRow(ctx, `SELECT authenticated_user_id,actor_id FROM audit_log WHERE entity_id=$1 AND action='security_test'`, adultID).Scan(&loggedUser, &loggedActor))
	if loggedUser != users["adult"] || loggedActor != adultID {
		t.Fatal("audit lost login or acting member identity")
	}
	other := newFolderFixture(t, fmt.Sprintf("personal-other-%d", time.Now().UnixNano()))
	var otherHost string
	must(p.QueryRow(ctx, `SELECT hostname FROM units WHERE id=$1`, other.unitID).Scan(&otherHost))
	if got := request("shared", "/member-check", otherHost); got != 403 {
		t.Errorf("cross-unit membership = %d", got)
	}
	if got := request("adult", "/admin-check", otherHost); got != 403 {
		t.Errorf("cross-unit authority = %d", got)
	}
	_, err = p.Exec(ctx, `DELETE FROM role_assignments WHERE member_id=$1`, adultID)
	must(err)
	if got := request("adult", "/admin-check", host); got != 403 {
		t.Errorf("revoked role still authorized = %d", got)
	}
}
