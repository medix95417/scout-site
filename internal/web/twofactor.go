package web

import (
	"errors"
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/twofactor"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file covers Phase 2's two-factor login: the /login/2fa code-entry
// step that follows a successful password check for a Treasurer/
// super_admin login (see internal/auth/totp.go's pending-login mechanism
// and LoginSubmit in web.go), and the self-service /settings/2fa
// enrollment flow.

func (h *Handlers) LoginTwoFactorForm(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(auth.PendingTwoFactorCookieName)
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	exists, err := auth.PendingTwoFactorLoginExists(r.Context(), h.Pool, cookie.Value)
	if err != nil {
		log.Printf("web: checking pending two-factor login: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !exists {
		h.expirePendingTwoFactorLogin(w, r)
		return
	}

	data := struct{ baseData }{baseData: h.base(r, "Two-Factor Verification")}
	h.render(w, h.loginTwoFactor, data)
}

func (h *Handlers) LoginTwoFactorSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cookie, err := r.Cookie(auth.PendingTwoFactorCookieName)
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	userID, next, err := auth.VerifyPendingTwoFactorLogin(r.Context(), h.Pool, cookie.Value, r.FormValue("code"))
	if err != nil {
		if errors.Is(err, auth.ErrInvalidPendingLogin) {
			h.expirePendingTwoFactorLogin(w, r)
			return
		}
		if errors.Is(err, auth.ErrInvalidTOTPCode) || errors.Is(err, auth.ErrTOTPNotEnrolled) {
			data := struct{ baseData }{baseData: h.base(r, "Two-Factor Verification")}
			data.Flash = "That code didn't match — check your authenticator app (or use a backup code) and try again."
			h.render(w, h.loginTwoFactor, data)
			return
		}
		log.Printf("web: verifying two-factor code: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	token, expiresAt, err := auth.CreateSession(r.Context(), h.Pool, userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	auth.ClearPendingTwoFactorCookie(w, h.CookieDomain, h.SecureCookie)
	auth.SetSessionCookie(w, token, expiresAt, h.CookieDomain, h.SecureCookie)
	http.Redirect(w, r, sanitizeNextPath(next), http.StatusSeeOther)
}

// expirePendingTwoFactorLogin clears a stale/invalid pending-login cookie
// and bounces the visitor back to a fresh /login with an explanatory
// flash — shared by both the GET (page load) and POST (code submission)
// paths for the same "your pending login is gone" outcome.
func (h *Handlers) expirePendingTwoFactorLogin(w http.ResponseWriter, r *http.Request) {
	auth.ClearPendingTwoFactorCookie(w, h.CookieDomain, h.SecureCookie)
	data := struct {
		baseData
		Next string
	}{baseData: h.base(r, "Log in"), Next: "/"}
	data.Flash = "Your login session expired — please sign in again."
	h.render(w, h.login, data)
}

// --- Settings: TOTP enrollment ------------------------------------------

func (h *Handlers) TwoFactorSettings(w http.ResponseWriter, r *http.Request) {
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/settings/2fa", http.StatusSeeOther)
		return
	}
	h.renderTwoFactorSettings(w, r, user.ID, "")
}

// renderTwoFactorSettings loads a user's current TOTP status (and, if an
// enrollment is in progress but not yet confirmed, the pending secret so
// it can be redisplayed after a page reload) and renders the settings
// page. Shared by the GET handler and TwoFactorConfirm's error path, so a
// wrong confirmation code re-renders the same setup screen with a flash
// instead of losing the in-progress secret.
func (h *Handlers) renderTwoFactorSettings(w http.ResponseWriter, r *http.Request, userID, flash string) {
	enrolled, confirmed, err := auth.TOTPStatus(r.Context(), h.Pool, userID)
	if err != nil {
		log.Printf("web: loading two-factor status: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	unit, _ := units.UnitFromContext(r.Context())
	user, _ := auth.UserFromContext(r.Context())

	data := struct {
		baseData
		Enrolled        bool
		Confirmed       bool
		FormattedSecret string
		ProvisioningURI string
	}{baseData: h.base(r, "Two-Factor Authentication"), Enrolled: enrolled, Confirmed: confirmed}
	data.Flash = flash

	if enrolled && !confirmed {
		secret, found, err := auth.PendingTOTPSecret(r.Context(), h.Pool, userID)
		if err != nil {
			log.Printf("web: loading pending two-factor secret: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if found {
			data.FormattedSecret = twofactor.FormatSecretForDisplay(secret)
			data.ProvisioningURI = twofactor.ProvisioningURI(unit.Name+" (Scout Site)", user.Email, secret)
		}
	}

	h.render(w, h.twoFactorSettings, data)
}

func (h *Handlers) TwoFactorEnroll(w http.ResponseWriter, r *http.Request) {
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if _, err := auth.BeginTOTPEnrollment(r.Context(), h.Pool, user.ID); err != nil {
		log.Printf("web: beginning two-factor enrollment: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings/2fa", http.StatusSeeOther)
}

func (h *Handlers) TwoFactorConfirm(w http.ResponseWriter, r *http.Request) {
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	backupCodes, err := auth.ConfirmTOTPEnrollment(r.Context(), h.Pool, user.ID, r.FormValue("code"))
	if err != nil {
		flash := "Something went wrong — please try again."
		switch {
		case errors.Is(err, auth.ErrInvalidTOTPCode):
			flash = "That code didn't match — check the time on your phone and try again."
		case errors.Is(err, auth.ErrTOTPNotEnrolled):
			flash = "Your enrollment session expired — start over below."
		default:
			log.Printf("web: confirming two-factor enrollment: %v", err)
		}
		h.renderTwoFactorSettings(w, r, user.ID, flash)
		return
	}

	data := struct {
		baseData
		BackupCodes []string
	}{baseData: h.base(r, "Two-Factor Authentication Enabled"), BackupCodes: backupCodes}
	h.render(w, h.twoFactorBackupCodes, data)
}

func (h *Handlers) TwoFactorDisable(w http.ResponseWriter, r *http.Request) {
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := auth.DisableTOTP(r.Context(), h.Pool, user.ID); err != nil {
		log.Printf("web: disabling two-factor: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings/2fa", http.StatusSeeOther)
}
