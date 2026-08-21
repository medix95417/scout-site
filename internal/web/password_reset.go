package web

import (
	"log"
	"net/http"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/units"
)

// This file is the self-service half of password recovery — a family
// resetting their own password by email, independent of (and in addition
// to) the leader-triggered reset in internal/web/admin_roster.go, which
// stays useful when a family's email is wrong/unreachable or a leader is
// handing over a new password in person.

// requestOrigin builds the scheme://host prefix for links embedded in
// outbound email (the password reset link), from the incoming request —
// so the link points back at whichever subdomain (troop./pack.) the
// request actually arrived on. Scheme mirrors h.SecureCookie, the same
// signal already used to decide whether session cookies get the Secure
// flag.
func (h *Handlers) requestOrigin(r *http.Request) string {
	scheme := "http"
	if h.SecureCookie {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (h *Handlers) ForgotPasswordForm(w http.ResponseWriter, r *http.Request) {
	data := struct {
		baseData
		Submitted bool
	}{baseData: h.base(r, "Forgot Password")}
	h.render(w, h.forgotPassword, data)
}

// ForgotPasswordSubmit always shows the same confirmation regardless of
// whether the email is registered — same "don't leak which one it was"
// reasoning as auth.ErrInvalidCredentials — and regardless of whether this
// submission was rate-limited (see below). If email isn't configured
// (SMTP_HOST empty), nothing gets sent, but the visitor sees the same
// message; the operator sees a clear warning in the server log instead,
// since that's the audience who can actually act on it.
func (h *Handlers) ForgotPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	email := r.FormValue("email")

	// Rate-limit how many reset emails one address can trigger — protects
	// that account's inbox (a family's, or a Scout's own login) from being
	// flooded by repeated submissions. Applied before the account lookup,
	// and applied the same way whether or not the email has an account, so
	// it can't be used to test which emails are registered. A tracking
	// error fails open (allowed=true) — a hiccup in this bookkeeping
	// should never be the reason a real reset request doesn't go out.
	allowed, err := auth.RecordAndCheckPasswordResetRequest(r.Context(), h.Pool, email)
	if err != nil {
		log.Printf("web: recording password reset request rate: %v", err)
		allowed = true
	}

	if allowed {
		user, found, err := auth.UserByEmail(r.Context(), h.Pool, email)
		if err != nil {
			log.Printf("web: looking up user by email for password reset: %v", err)
		} else if found {
			h.sendResetEmail(r, user)
		}
	} else {
		log.Printf("web: password reset request rate-limited for %s", auth.NormalizeEmail(email))
	}

	data := struct {
		baseData
		Submitted bool
	}{baseData: h.base(r, "Forgot Password"), Submitted: true}
	h.render(w, h.forgotPassword, data)
}

func (h *Handlers) sendResetEmail(r *http.Request, user auth.User) {
	token, _, err := auth.CreateResetToken(r.Context(), h.Pool, user.ID)
	if err != nil {
		log.Printf("web: creating reset token for %s: %v", user.Email, err)
		return
	}

	if !h.Mailer.Enabled(r.Context()) {
		log.Printf("web: password reset requested for %s but email isn't configured (SMTP_HOST is empty) — no email sent; a leader can reset this family's password from /admin/roster instead", user.Email)
		return
	}

	unit, _ := units.UnitFromContext(r.Context())
	link := h.requestOrigin(r) + "/reset-password?token=" + token
	body := "Someone (hopefully you) requested a password reset for " + unit.Name + ".\n\n" +
		"Reset your password: " + link + "\n\n" +
		"This link expires in about an hour. If you didn't request this, you can ignore this email — " +
		"your password won't change unless you click the link above and set a new one."

	if err := h.Mailer.Send(r.Context(), user.Email, "Reset your "+unit.Name+" password", body); err != nil {
		log.Printf("web: sending reset email to %s: %v", user.Email, err)
	}
}

func (h *Handlers) ResetPasswordForm(w http.ResponseWriter, r *http.Request) {
	data := struct {
		baseData
		Token string
	}{baseData: h.base(r, "Reset Password"), Token: r.URL.Query().Get("token")}
	h.render(w, h.resetPassword, data)
}

func (h *Handlers) ResetPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	password := r.FormValue("password")

	showError := func(flash string) {
		data := struct {
			baseData
			Token string
		}{baseData: h.base(r, "Reset Password"), Token: token}
		data.Flash = flash
		h.render(w, h.resetPassword, data)
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

	if _, err := auth.ConsumeResetToken(r.Context(), h.Pool, token, hash); err != nil {
		showError("This link is invalid or has expired — request a new one.")
		return
	}

	data := struct {
		baseData
		Next string
	}{baseData: h.base(r, "Log in")}
	data.Flash = "Your password has been updated. You can log in now."
	h.render(w, h.login, data)
}
