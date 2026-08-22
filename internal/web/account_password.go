package web

// This file is voluntary, logged-in password change — reachable from
// /settings/2fa (the "Security" page) alongside two-factor setup. This is
// deliberately a separate mechanism from internal/web/change_password.go
// (which handles a forced temporary-password replacement before a session
// even exists) and from /forgot-password (an unauthenticated recovery
// flow via emailed token): here the visitor is already logged in and
// simply proves they know their current password.

import (
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/auth"
)

// AccountChangePassword lets a logged-in login replace its own password,
// after confirming the current one. Same "invalidate every session"
// behavior as every other password change in this codebase (see
// auth.ChangePassword) — including this request's own — so completeLogin
// re-authenticates the visitor afterward: straight back in with a fresh
// session if this login has no confirmed two-factor device, or through
// /login/2fa first if it does, mirroring exactly how
// ChangePasswordSubmit's forced-change flow already re-authenticates
// after a password update.
func (h *Handlers) AccountChangePassword(w http.ResponseWriter, r *http.Request) {
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/settings/2fa", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if !auth.VerifyPassword(user, r.FormValue("current_password")) {
		h.renderTwoFactorSettings(w, r, user.ID, "Your current password didn't match — nothing was changed.")
		return
	}

	newPassword := r.FormValue("new_password")
	if err := auth.ValidatePasswordComplexity(newPassword); err != nil {
		h.renderTwoFactorSettings(w, r, user.ID, err.Error())
		return
	}
	if newPassword != r.FormValue("new_password_confirm") {
		h.renderTwoFactorSettings(w, r, user.ID, "New passwords don't match.")
		return
	}

	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		log.Printf("web: hashing new password: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := auth.ChangePassword(r.Context(), h.Pool, user.ID, hash); err != nil {
		log.Printf("web: changing password for user %s: %v", user.ID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.completeLogin(w, r, user.ID, "/settings/2fa")
}
