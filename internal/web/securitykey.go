package web

// Security keys (WebAuthn) as a second factor: the HTTP half. The data
// and rules are in internal/auth/securitykey.go; the ceremony is
// go-webauthn's. What lives here is the wiring between a browser's
// navigator.credentials call and those two.
//
// Every ceremony is two requests. "Begin" returns the options the
// browser needs as JSON and stores the challenge server-side against the
// login; "finish" receives the authenticator's answer as a form field
// and checks it against that stored challenge. Both halves are ordinary
// POSTs carrying the CSRF token, so the existing middleware covers them
// with nothing special-cased. The finish step is a real form submission
// rather than a fetch, so the server can render the backup-codes page or
// redirect exactly as the TOTP flow does.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/47-yonkers/scout-site/internal/auth"
)

// rpIDFor decides the WebAuthn relying-party id — the domain a key is
// bound to. A credential registered under one RP id will not answer for
// another, so it has to be the same on both subdomains or a key set up
// on the Troop site would not work on the Pack's.
//
// The cookie domain is the right answer whenever it is set: it is
// already "the domain both subdomains share" for exactly the same
// single-sign-on reason. Without one (local development), the longest
// suffix every unit hostname shares does the same job — "localhost" for
// troop.localhost and pack.localhost. An explicit override wins over
// both, for a deployment whose units live on unrelated domains.
func rpIDFor(override, cookieDomain string, hostnames []string) string {
	if o := strings.TrimSpace(override); o != "" {
		return o
	}
	if d := strings.TrimPrefix(strings.TrimSpace(cookieDomain), "."); d != "" {
		return d
	}
	if suffix := commonDottedSuffix(hostnames); suffix != "" {
		return suffix
	}
	if len(hostnames) > 0 {
		return hostnames[0]
	}
	return "localhost"
}

// commonDottedSuffix returns the labels every hostname ends with, or ""
// when they share none.
func commonDottedSuffix(hostnames []string) string {
	if len(hostnames) == 0 {
		return ""
	}
	parts := make([][]string, len(hostnames))
	for i, h := range hostnames {
		parts[i] = strings.Split(strings.ToLower(strings.TrimSpace(h)), ".")
	}
	var common []string
	for depth := 1; ; depth++ {
		var label string
		for i, p := range parts {
			if depth > len(p) {
				return join(common)
			}
			l := p[len(p)-depth]
			if i == 0 {
				label = l
			} else if l != label {
				return join(common)
			}
		}
		common = append([]string{label}, common...)
	}
}

func join(labels []string) string { return strings.Join(labels, ".") }

// webAuthn returns the relying party, built from the units table the
// first time it is needed. Not in New: the hostnames are data, and a
// failure to read them should show up as "security keys are unavailable
// right now" on the page that needs them, not as a server that will not
// start.
//
// The library checks the origin the browser signed against a fixed
// allow list, and also insists the origin each ceremony is bound to is
// on that list. The list starts as every unit hostname over the schemes
// this deployment serves, and grows by one entry the first time a
// request arrives with an origin not yet on it — which in practice is a
// development origin carrying a port. That is safe to admit because it
// is not the browser's claim: units.Middleware has already refused any
// Host that is not one of ours, and the scheme is the server's own
// decision (see siteURL). So the list can only ever contain origins
// this server itself would serve.
func (h *Handlers) webAuthn(r *http.Request) (*webauthn.WebAuthn, error) {
	origin := h.siteURL(r)

	h.webauthnMu.Lock()
	defer h.webauthnMu.Unlock()
	if h.webauthnRP != nil {
		for _, o := range h.webauthnRP.Config.RPOrigins {
			if o == origin {
				return h.webauthnRP, nil
			}
		}
	}

	rows, err := h.Pool.Query(r.Context(), `SELECT hostname FROM units ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hosts []string
	for rows.Next() {
		var host string
		if err := rows.Scan(&host); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var origins []string
	add := func(o string) {
		if !seen[o] {
			seen[o] = true
			origins = append(origins, o)
		}
	}
	for _, host := range hosts {
		add("https://" + host)
		if !h.SecureCookie {
			add("http://" + host)
		}
	}
	if h.webauthnRP != nil {
		for _, o := range h.webauthnRP.Config.RPOrigins {
			add(o)
		}
	}
	add(origin)

	rp, err := webauthn.New(&webauthn.Config{
		RPID:          rpIDFor(h.WebAuthnRPID, h.CookieDomain, hosts),
		RPDisplayName: "Scout Site",
		RPOrigins:     origins,
	})
	if err != nil {
		return nil, err
	}
	h.webauthnRP = rp
	return rp, nil
}

// writeJSON sends a JSON body. The begin handlers speak JSON because the
// browser API wants an object, not a page.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// webauthnFailure turns a library error into a log line with the detail
// and a message without it. The detail names what was wrong with the
// signature or attestation, which is for the log; the person just needs
// to know it did not work.
func webauthnFailure(what string, err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) {
		log.Printf("web: %s: %s: %s (%s)", what, pe.Type, pe.Details, pe.DevInfo)
	} else {
		log.Printf("web: %s: %v", what, err)
	}
	return "That didn't work — the security key's answer couldn't be verified. Try again."
}

// --- Settings: registering and removing keys -------------------------

// securityKeyStepUp reports whether adding or removing a key needs the
// current password first, and checks it if so. Same rule as
// auth.BeginTOTPEnrollment: once a login has any second factor, a live
// session alone must not be able to change what that factor is.
func (h *Handlers) securityKeyStepUp(r *http.Request, user auth.User) (ok bool, err error) {
	has, err := auth.HasSecondFactor(r.Context(), h.Pool, user.ID)
	if err != nil {
		return false, err
	}
	if !has {
		return true, nil
	}
	return auth.VerifyPassword(user, r.FormValue("password")), nil
}

// SecurityKeyBegin starts registering a key: checks the password if the
// login already has a second factor, and returns the creation options.
func (h *Handlers) SecurityKeyBegin(w http.ResponseWriter, r *http.Request) {
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		writeJSONError(w, http.StatusUnauthorized, "please sign in again")
		return
	}
	ok, err := h.securityKeyStepUp(r, user)
	if err != nil {
		log.Printf("web: security key step-up: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusForbidden, "That password didn't match — re-enter your current password to add a key.")
		return
	}

	rp, err := h.webAuthn(r)
	if err != nil {
		log.Printf("web: building webauthn relying party: %v", err)
		writeJSONError(w, http.StatusServiceUnavailable, "security keys aren't available right now")
		return
	}
	wu, err := auth.LoadWebAuthnUser(r.Context(), h.Pool, user.ID)
	if err != nil {
		log.Printf("web: loading webauthn user: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	exclusions := make([]protocol.CredentialDescriptor, 0, len(wu.Keys))
	for _, c := range wu.WebAuthnCredentials() {
		exclusions = append(exclusions, c.Descriptor())
	}
	creation, session, err := rp.BeginRegistration(wu,
		// Every key this login already has is excluded, so the browser
		// refuses to register the same YubiKey twice.
		webauthn.WithExclusions(exclusions),
		// We do not need to know the make of the key, and asking for
		// attestation makes some browsers show a consent prompt about it.
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
		// A non-discoverable credential: the server names the key at
		// login, so the authenticator need not store anything per site —
		// a hardware key has a small number of resident slots and this
		// site should not spend one.
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementDiscouraged,
			UserVerification: protocol.VerificationPreferred,
		}),
		// Bound to the origin this request arrived on, which the units
		// middleware has already checked is one of ours.
		webauthn.WithRegistrationOrigin(h.siteURL(r)),
	)
	if err != nil {
		log.Printf("web: beginning security key registration: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := auth.SaveWebAuthnSession(r.Context(), h.Pool, user.ID, auth.WebAuthnRegister, session); err != nil {
		log.Printf("web: saving webauthn session: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, creation)
}

// SecurityKeyFinish receives the authenticator's answer, verifies it
// against the stored challenge, and stores the key. Backup codes are
// shown if this was the login's first second factor.
func (h *Handlers) SecurityKeyFinish(w http.ResponseWriter, r *http.Request) {
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/settings/2fa", http.StatusSeeOther)
		return
	}
	rp, err := h.webAuthn(r)
	if err != nil {
		log.Printf("web: building webauthn relying party: %v", err)
		h.renderTwoFactorSettings(w, r, user.ID, "Security keys aren't available right now.")
		return
	}
	session, err := auth.TakeWebAuthnSession(r.Context(), h.Pool, user.ID, auth.WebAuthnRegister)
	if err != nil {
		if !errors.Is(err, auth.ErrNoWebAuthnSession) {
			log.Printf("web: taking webauthn session: %v", err)
		}
		h.renderTwoFactorSettings(w, r, user.ID, "That took too long, or was already used — start adding the key again.")
		return
	}
	wu, err := auth.LoadWebAuthnUser(r.Context(), h.Pool, user.ID)
	if err != nil {
		log.Printf("web: loading webauthn user: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	parsed, err := protocol.ParseCredentialCreationResponseBytes([]byte(r.FormValue("credential")))
	if err != nil {
		h.renderTwoFactorSettings(w, r, user.ID, webauthnFailure("parsing security key registration", err))
		return
	}
	cred, err := rp.CreateCredential(wu, *session, parsed)
	if err != nil {
		h.renderTwoFactorSettings(w, r, user.ID, webauthnFailure("verifying security key registration", err))
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if len(name) > 60 {
		name = name[:60]
	}
	codes, err := auth.AddSecurityKey(r.Context(), h.Pool, user.ID, name, cred)
	if err != nil {
		if errors.Is(err, auth.ErrSecurityKeyAlreadyRegistered) {
			h.renderTwoFactorSettings(w, r, user.ID, "That security key is already registered.")
			return
		}
		log.Printf("web: storing security key: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if codes != nil {
		data := struct {
			baseData
			BackupCodes []string
		}{baseData: h.base(r, "Two-Factor Authentication Enabled"), BackupCodes: codes}
		h.render(w, h.twoFactorBackupCodes, data)
		return
	}
	h.renderTwoFactorSettings(w, r, user.ID, "Security key added.")
}

// SecurityKeyDelete removes one key, after the password — a key is a
// second factor, and a session alone must not be able to take one away
// (see TwoFactorDisable for the same rule on the app).
func (h *Handlers) SecurityKeyDelete(w http.ResponseWriter, r *http.Request) {
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/settings/2fa", http.StatusSeeOther)
		return
	}
	if !auth.VerifyPassword(user, r.FormValue("password")) {
		h.renderTwoFactorSettings(w, r, user.ID, "That password didn't match — the key is still registered. Re-enter your current password to remove it.")
		return
	}
	if err := auth.RemoveSecurityKey(r.Context(), h.Pool, user.ID, r.PathValue("id")); err != nil {
		if errors.Is(err, auth.ErrSecurityKeyNotFound) {
			http.Redirect(w, r, "/settings/2fa", http.StatusSeeOther)
			return
		}
		log.Printf("web: removing security key: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.renderTwoFactorSettings(w, r, user.ID, "Security key removed.")
}

// --- Login: answering with a key -------------------------------------

// pendingLoginFromCookie resolves the pending-login cookie the password
// step set, or writes the "sign in again" page.
func (h *Handlers) pendingLoginFromCookie(w http.ResponseWriter, r *http.Request, asJSON bool) (token, userID, next string, ok bool) {
	cookie, err := r.Cookie(auth.PendingTwoFactorCookieName)
	if err != nil || cookie.Value == "" {
		if asJSON {
			writeJSONError(w, http.StatusUnauthorized, "your login session expired — please sign in again")
		} else {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		}
		return "", "", "", false
	}
	userID, next, err = auth.PendingTwoFactorLoginOwner(r.Context(), h.Pool, cookie.Value)
	if err != nil {
		if !errors.Is(err, auth.ErrInvalidPendingLogin) {
			log.Printf("web: reading pending two-factor login: %v", err)
		}
		if asJSON {
			writeJSONError(w, http.StatusUnauthorized, "your login session expired — please sign in again")
		} else {
			h.expirePendingTwoFactorLogin(w, r)
		}
		return "", "", "", false
	}
	return cookie.Value, userID, next, true
}

// LoginSecurityKeyBegin returns the assertion options for the login the
// pending cookie identifies — the allow list is that login's keys, so
// the browser prompts for the right one.
func (h *Handlers) LoginSecurityKeyBegin(w http.ResponseWriter, r *http.Request) {
	_, userID, _, ok := h.pendingLoginFromCookie(w, r, true)
	if !ok {
		return
	}
	rp, err := h.webAuthn(r)
	if err != nil {
		log.Printf("web: building webauthn relying party: %v", err)
		writeJSONError(w, http.StatusServiceUnavailable, "security keys aren't available right now")
		return
	}
	wu, err := auth.LoadWebAuthnUser(r.Context(), h.Pool, userID)
	if err != nil {
		log.Printf("web: loading webauthn user: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(wu.Keys) == 0 {
		writeJSONError(w, http.StatusBadRequest, "this login has no security key — enter a code instead")
		return
	}
	assertion, session, err := rp.BeginLogin(wu,
		webauthn.WithUserVerification(protocol.VerificationPreferred),
		webauthn.WithLoginOrigin(h.siteURL(r)),
	)
	if err != nil {
		log.Printf("web: beginning security key login: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := auth.SaveWebAuthnSession(r.Context(), h.Pool, userID, auth.WebAuthnLogin, session); err != nil {
		log.Printf("web: saving webauthn session: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, assertion)
}

// LoginSecurityKeyFinish verifies the key's answer and, if it holds,
// consumes the pending login and issues the session — the same last
// step LoginTwoFactorSubmit takes after a correct code.
func (h *Handlers) LoginSecurityKeyFinish(w http.ResponseWriter, r *http.Request) {
	token, userID, next, ok := h.pendingLoginFromCookie(w, r, false)
	if !ok {
		return
	}
	retry := func(flash string) {
		h.renderLoginTwoFactor(w, r, userID, flash)
	}

	rp, err := h.webAuthn(r)
	if err != nil {
		log.Printf("web: building webauthn relying party: %v", err)
		retry("Security keys aren't available right now — enter a code instead.")
		return
	}
	session, err := auth.TakeWebAuthnSession(r.Context(), h.Pool, userID, auth.WebAuthnLogin)
	if err != nil {
		if !errors.Is(err, auth.ErrNoWebAuthnSession) {
			log.Printf("web: taking webauthn session: %v", err)
		}
		retry("That took too long, or was already used — try the key again.")
		return
	}
	wu, err := auth.LoadWebAuthnUser(r.Context(), h.Pool, userID)
	if err != nil {
		log.Printf("web: loading webauthn user: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	parsed, err := protocol.ParseCredentialRequestResponseBytes([]byte(r.FormValue("credential")))
	if err != nil {
		_ = auth.RecordPendingTwoFactorFailure(r.Context(), h.Pool, token)
		retry(webauthnFailure("parsing security key assertion", err))
		return
	}
	cred, err := rp.ValidateLogin(wu, *session, parsed)
	if err != nil {
		_ = auth.RecordPendingTwoFactorFailure(r.Context(), h.Pool, token)
		retry(webauthnFailure("verifying security key assertion", err))
		return
	}
	if cred.Authenticator.CloneWarning {
		log.Printf("web: security key %x for login %s: signature counter went backwards (possible clone)", cred.ID, userID)
	}
	if err := auth.RecordSecurityKeyUse(r.Context(), h.Pool, cred.ID, cred.Authenticator.SignCount, cred.Flags.BackupState); err != nil {
		log.Printf("web: recording security key use: %v", err)
	}

	// The pending login is spent before the session exists, so two
	// answers to one challenge cannot both become sessions.
	if err := auth.ConsumePendingTwoFactorLogin(r.Context(), h.Pool, token); err != nil {
		if errors.Is(err, auth.ErrInvalidPendingLogin) {
			h.expirePendingTwoFactorLogin(w, r)
			return
		}
		log.Printf("web: consuming pending two-factor login: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sessionToken, expiresAt, err := auth.CreateSession(r.Context(), h.Pool, userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	auth.ClearPendingTwoFactorCookie(w, h.CookieDomain, h.SecureCookie)
	auth.SetSessionCookie(w, sessionToken, expiresAt, h.CookieDomain, h.SecureCookie)
	http.Redirect(w, r, sanitizeNextPath(next), http.StatusSeeOther)
}
