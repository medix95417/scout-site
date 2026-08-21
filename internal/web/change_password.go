package web

import (
	"errors"
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/auth"
)

// This file is the forced "you're using a temporary password — set a real
// one" step that follows a successful password check for any login whose
// users.must_change_password flag is set (see internal/roster's
// CreateFamilyWithMember/CreateMemberLogin/ResetFamilyPassword/
// ResetMemberLoginPassword). Mirrors the pending-two-factor-login mechanism
// in internal/auth/totp.go and internal/web/twofactor.go — just for "must
// set a new password" instead of "must enter a TOTP code" — and runs
// before it: a temporary credential must be replaced before a second
// factor even comes into play (see LoginSubmit/completeLogin in web.go).

func (h *Handlers) ChangePasswordForm(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(auth.PendingPasswordChangeCookieName)
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	exists, err := auth.PendingPasswordChangeExists(r.Context(), h.Pool, cookie.Value)
	if err != nil {
		log.Printf("web: checking pending password change: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !exists {
		h.expirePendingPasswordChange(w, r)
		return
	}

	data := struct{ baseData }{baseData: h.base(r, "Set a New Password")}
	h.render(w, h.changePassword, data)
}

func (h *Handlers) ChangePasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cookie, err := r.Cookie(auth.PendingPasswordChangeCookieName)
	if err != nil || cookie.Value == "" {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	password := r.FormValue("password")
	showError := func(flash string) {
		data := struct{ baseData }{baseData: h.base(r, "Set a New Password")}
		data.Flash = flash
		h.render(w, h.changePassword, data)
	}

	if err := auth.ValidatePasswordComplexity(password); err != nil {
		showError(err.Error())
		return
	}
	if password != r.FormValue("password_confirm") {
		showError("Passwords don't match.")
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Printf("web: hashing new password: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	userID, next, err := auth.ConsumePendingPasswordChange(r.Context(), h.Pool, cookie.Value, hash)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidPendingPasswordChange) {
			h.expirePendingPasswordChange(w, r)
			return
		}
		log.Printf("web: consuming pending password change: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	auth.ClearPendingPasswordChangeCookie(w, h.CookieDomain, h.SecureCookie)
	h.completeLogin(w, r, userID, sanitizeNextPath(next))
}

// expirePendingPasswordChange clears a stale/invalid pending-password-
// change cookie and bounces the visitor back to a fresh /login with an
// explanatory flash — shared by both the GET (page load) and POST (form
// submission) paths, same shape as twofactor.go's
// expirePendingTwoFactorLogin.
func (h *Handlers) expirePendingPasswordChange(w http.ResponseWriter, r *http.Request) {
	auth.ClearPendingPasswordChangeCookie(w, h.CookieDomain, h.SecureCookie)
	data := struct {
		baseData
		Next string
	}{baseData: h.base(r, "Log in"), Next: "/"}
	data.Flash = "Your login session expired — please sign in again."
	h.render(w, h.login, data)
}
