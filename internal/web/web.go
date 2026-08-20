// Package web holds the HTTP handlers and templates that render the site.
// Deliberately server-rendered (html/template + htmx) rather than a
// separate frontend — see scout-website-architecture-phase1.md Section 1
// for why.
package web

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/approval"
	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/csrf"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/mailer"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/storage"
	"github.com/47-yonkers/scout-site/internal/units"
	"github.com/47-yonkers/scout-site/internal/version"
)

//go:embed all:templates
var templatesFS embed.FS

// errMemberNotFound backs actingMember's individual-login path — should
// only ever happen if a member row was deleted out from under a still-live
// login, which ON DELETE CASCADE on users.member_id (see migration 0009)
// actually prevents (deleting the member deletes the login's session-worthy
// row too), so this is more a defensive backstop than an expected path.
var errMemberNotFound = errors.New("web: member not found")

// Handlers holds shared dependencies for every HTTP handler.
type Handlers struct {
	Pool         *pgxpool.Pool
	CookieDomain string
	SecureCookie bool // set true when serving over HTTPS (production); false for local http://
	Mailer       *mailer.Mailer
	Storage      *storage.Store

	home                 *template.Template
	login                *template.Template
	roster               *template.Template
	calendar             *template.Template
	audit                *template.Template
	contentAdmin         *template.Template
	rosterAdmin          *template.Template
	rosterMemberEdit     *template.Template
	rosterCredentials    *template.Template
	forgotPassword       *template.Template
	resetPassword        *template.Template
	loginTwoFactor       *template.Template
	twoFactorSettings    *template.Template
	twoFactorBackupCodes *template.Template
	treasury             *template.Template
	treasuryAccount      *template.Template
	treasuryFundraiser   *template.Template
	systemSettings       *template.Template
	accounts             *template.Template

	newsListTmpl         *template.Template
	newsDetailTmpl       *template.Template
	galleryListTmpl      *template.Template
	galleryDetailTmpl    *template.Template
	adminContentListTmpl *template.Template
	adminContentFormTmpl *template.Template

	newsletterList *template.Template
	newsletterForm *template.Template

	rosterImport        *template.Template
	rosterImportResults *template.Template

	permissionSlip *template.Template

	advancement      *template.Template
	advancementAdmin *template.Template

	fileLibrary *template.Template

	customRoles *template.Template

	myFamily        *template.Template
	familyDirectory *template.Template

	groupsList *template.Template
	groupView  *template.Template
	groupAdmin *template.Template
}

// templateFuncs are available to every page template. formatCents is the
// only one so far — Phase 2's treasury templates need to render signed
// cent amounts as "$12.34"/"-$12.34" and Go templates have no arithmetic
// or number-formatting of their own.
var templateFuncs = template.FuncMap{
	"formatCents": formatCents,
	"hasPrefix":   strings.HasPrefix,
}

// New parses templates and returns a ready-to-use Handlers.
func New(pool *pgxpool.Pool, cookieDomain string, secureCookie bool, mail *mailer.Mailer, store *storage.Store) (*Handlers, error) {
	parse := func(page string) (*template.Template, error) {
		return template.New("base.html").Funcs(templateFuncs).ParseFS(templatesFS, "templates/base.html", "templates/"+page)
	}

	h := &Handlers{Pool: pool, CookieDomain: cookieDomain, SecureCookie: secureCookie, Mailer: mail, Storage: store}

	var err error
	if h.home, err = parse("home.html"); err != nil {
		return nil, err
	}
	if h.login, err = parse("login.html"); err != nil {
		return nil, err
	}
	if h.roster, err = parse("roster.html"); err != nil {
		return nil, err
	}
	if h.calendar, err = parse("calendar.html"); err != nil {
		return nil, err
	}
	if h.audit, err = parse("audit.html"); err != nil {
		return nil, err
	}
	if h.contentAdmin, err = parse("content-admin.html"); err != nil {
		return nil, err
	}
	if h.rosterAdmin, err = parse("admin-roster.html"); err != nil {
		return nil, err
	}
	if h.rosterMemberEdit, err = parse("admin-roster-member.html"); err != nil {
		return nil, err
	}
	if h.rosterCredentials, err = parse("admin-roster-credentials.html"); err != nil {
		return nil, err
	}
	if h.forgotPassword, err = parse("forgot-password.html"); err != nil {
		return nil, err
	}
	if h.resetPassword, err = parse("reset-password.html"); err != nil {
		return nil, err
	}
	if h.loginTwoFactor, err = parse("login-two-factor.html"); err != nil {
		return nil, err
	}
	if h.twoFactorSettings, err = parse("two-factor-settings.html"); err != nil {
		return nil, err
	}
	if h.twoFactorBackupCodes, err = parse("two-factor-backup-codes.html"); err != nil {
		return nil, err
	}
	if h.treasury, err = parse("treasury.html"); err != nil {
		return nil, err
	}
	if h.treasuryAccount, err = parse("treasury-account.html"); err != nil {
		return nil, err
	}
	if h.treasuryFundraiser, err = parse("treasury-fundraiser.html"); err != nil {
		return nil, err
	}
	if h.systemSettings, err = parse("admin-settings.html"); err != nil {
		return nil, err
	}
	if h.accounts, err = parse("accounts.html"); err != nil {
		return nil, err
	}
	if h.newsListTmpl, err = parse("news.html"); err != nil {
		return nil, err
	}
	if h.newsDetailTmpl, err = parse("news-detail.html"); err != nil {
		return nil, err
	}
	if h.galleryListTmpl, err = parse("gallery.html"); err != nil {
		return nil, err
	}
	if h.galleryDetailTmpl, err = parse("gallery-detail.html"); err != nil {
		return nil, err
	}
	if h.adminContentListTmpl, err = parse("admin-content-list.html"); err != nil {
		return nil, err
	}
	if h.adminContentFormTmpl, err = parse("admin-content-form.html"); err != nil {
		return nil, err
	}
	if h.newsletterList, err = parse("admin-newsletter-list.html"); err != nil {
		return nil, err
	}
	if h.newsletterForm, err = parse("admin-newsletter-form.html"); err != nil {
		return nil, err
	}
	if h.rosterImport, err = parse("admin-roster-import.html"); err != nil {
		return nil, err
	}
	if h.rosterImportResults, err = parse("admin-roster-import-results.html"); err != nil {
		return nil, err
	}
	if h.permissionSlip, err = parse("permission-slip.html"); err != nil {
		return nil, err
	}
	if h.advancement, err = parse("advancement.html"); err != nil {
		return nil, err
	}
	if h.advancementAdmin, err = parse("admin-advancement.html"); err != nil {
		return nil, err
	}
	if h.fileLibrary, err = parse("files.html"); err != nil {
		return nil, err
	}
	if h.customRoles, err = parse("admin-custom-roles.html"); err != nil {
		return nil, err
	}
	if h.myFamily, err = parse("my-family.html"); err != nil {
		return nil, err
	}
	if h.familyDirectory, err = parse("family-directory.html"); err != nil {
		return nil, err
	}
	if h.groupsList, err = parse("groups-list.html"); err != nil {
		return nil, err
	}
	if h.groupView, err = parse("group-view.html"); err != nil {
		return nil, err
	}
	if h.groupAdmin, err = parse("admin-group.html"); err != nil {
		return nil, err
	}
	return h, nil
}

// Routes registers every Phase 1 route. Mounted by cmd/server after the
// unit-resolution and auth middleware.
func (h *Handlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", h.Home)
	mux.HandleFunc("GET /login", h.LoginForm)
	mux.HandleFunc("POST /login", h.LoginSubmit)
	mux.HandleFunc("POST /logout", h.Logout)
	mux.HandleFunc("GET /forgot-password", h.ForgotPasswordForm)
	mux.HandleFunc("POST /forgot-password", h.ForgotPasswordSubmit)
	mux.HandleFunc("GET /reset-password", h.ResetPasswordForm)
	mux.HandleFunc("POST /reset-password", h.ResetPasswordSubmit)
	mux.HandleFunc("GET /roster", h.Roster)
	mux.HandleFunc("GET /advancement", h.Advancement)
	mux.HandleFunc("GET /admin/advancement", h.AdminAdvancementList)
	mux.HandleFunc("POST /admin/advancement", h.AdminAdvancementCreate)
	mux.HandleFunc("POST /admin/advancement/bulk", h.AdminAdvancementBulkImport)
	mux.HandleFunc("POST /admin/advancement/{id}/delete", h.AdminAdvancementDelete)
	mux.HandleFunc("GET /calendar", h.Calendar)
	mux.HandleFunc("POST /calendar", h.CalendarCreate)
	mux.HandleFunc("POST /calendar/{id}/rsvp", h.CalendarRSVP)
	mux.HandleFunc("POST /calendar/approvals/{id}/decide", h.ApprovalDecide)
	mux.HandleFunc("GET /calendar/{id}/permission-slip", h.PermissionSlipView)
	mux.HandleFunc("POST /calendar/{id}/permission-slip", h.PermissionSlipSave)
	mux.HandleFunc("POST /calendar/{id}/permission-slip/sign", h.PermissionSlipSign)
	mux.HandleFunc("GET /audit", h.AuditView)
	mux.HandleFunc("GET /audit/export.csv", h.AuditExport)
	mux.HandleFunc("GET /accounts", h.AccountsView)
	mux.HandleFunc("GET /admin/home", h.HomeContentList)
	mux.HandleFunc("POST /admin/home/{slug}", h.HomeContentSave)
	mux.HandleFunc("GET /admin/roster", h.AdminRosterList)
	mux.HandleFunc("POST /admin/roster/families", h.AdminRosterCreateFamily)
	mux.HandleFunc("POST /admin/roster/members", h.AdminRosterAddMember)
	mux.HandleFunc("GET /admin/roster/members/{id}", h.AdminRosterMemberEdit)
	mux.HandleFunc("POST /admin/roster/members/{id}", h.AdminRosterMemberUpdate)
	mux.HandleFunc("POST /admin/roster/members/{id}/roles", h.AdminRosterAssignRole)
	mux.HandleFunc("POST /admin/roster/members/{id}/reset-password", h.AdminRosterResetPassword)
	mux.HandleFunc("POST /admin/roster/members/{id}/login", h.AdminRosterCreateMemberLogin)
	mux.HandleFunc("POST /admin/roster/members/{id}/login/reset-password", h.AdminRosterResetMemberLoginPassword)
	mux.HandleFunc("POST /admin/roster/roles/{id}/delete", h.AdminRosterRemoveRole)
	mux.HandleFunc("POST /admin/roster/sub-groups", h.AdminRosterCreateSubGroup)
	mux.HandleFunc("GET /admin/roster/import", h.AdminRosterImportForm)
	mux.HandleFunc("POST /admin/roster/import", h.AdminRosterImportApply)

	// Phase 2: two-factor login (Treasurer/super_admin) and self-service enrollment.
	mux.HandleFunc("GET /login/2fa", h.LoginTwoFactorForm)
	mux.HandleFunc("POST /login/2fa", h.LoginTwoFactorSubmit)
	mux.HandleFunc("GET /settings/2fa", h.TwoFactorSettings)
	mux.HandleFunc("POST /settings/2fa/enroll", h.TwoFactorEnroll)
	mux.HandleFunc("POST /settings/2fa/confirm", h.TwoFactorConfirm)
	mux.HandleFunc("POST /settings/2fa/disable", h.TwoFactorDisable)

	// Phase 2: fund accounting — Treasurer-only unless noted.
	mux.HandleFunc("GET /treasury", h.TreasuryDashboard)
	mux.HandleFunc("POST /treasury/transactions", h.TreasuryPostTransaction)
	mux.HandleFunc("POST /treasury/trip-funds", h.TreasuryCreateTripFund)
	mux.HandleFunc("POST /treasury/accounts/{id}/close", h.TreasuryCloseTripFund)
	mux.HandleFunc("POST /treasury/transfers/{id}/decide", h.TreasuryDecideTransfer)
	mux.HandleFunc("GET /treasury/accounts/{id}", h.TreasuryAccountView)
	mux.HandleFunc("POST /treasury/accounts/{id}/transfer", h.TreasuryRequestTransfer)
	mux.HandleFunc("GET /treasury/fundraisers/{id}", h.TreasuryFundraiserView)
	mux.HandleFunc("POST /treasury/fundraisers", h.TreasuryCreateFundraiser)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/allocate", h.TreasuryAllocateFundraiser)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/allocate-bulk", h.TreasuryAllocateFundraiserBulk)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/confirm-cap", h.TreasuryConfirmFundraiserCap)

	// Site-wide settings — super_admin only.
	mux.HandleFunc("GET /admin/settings", h.SystemSettingsView)
	mux.HandleFunc("POST /admin/settings/{key}/toggle", h.SystemSettingsToggle)
	mux.HandleFunc("POST /admin/settings/text", h.SystemSettingsUpdateText)
	mux.HandleFunc("POST /admin/settings/unit/{key}/toggle", h.UnitSettingsToggle)

	// News/announcements and photo galleries (internal/web/content_posts.go).
	mux.HandleFunc("GET /news", h.NewsList)
	mux.HandleFunc("GET /news/{id}", h.NewsView)
	mux.HandleFunc("GET /gallery", h.GalleryList)
	mux.HandleFunc("GET /gallery/{id}", h.GalleryView)

	mux.HandleFunc("GET /admin/news", h.AdminNewsList)
	mux.HandleFunc("GET /admin/news/new", h.AdminNewsNew)
	mux.HandleFunc("POST /admin/news", h.AdminNewsCreate)
	mux.HandleFunc("GET /admin/news/{id}/edit", h.AdminNewsEdit)
	mux.HandleFunc("POST /admin/news/{id}", h.AdminNewsUpdate)
	mux.HandleFunc("POST /admin/news/{id}/publish", h.AdminNewsPublishToggle)

	mux.HandleFunc("GET /admin/gallery", h.AdminGalleryList)
	mux.HandleFunc("GET /admin/gallery/new", h.AdminGalleryNew)
	mux.HandleFunc("POST /admin/gallery", h.AdminGalleryCreate)
	mux.HandleFunc("GET /admin/gallery/{id}/edit", h.AdminGalleryEdit)
	mux.HandleFunc("POST /admin/gallery/{id}", h.AdminGalleryUpdate)
	mux.HandleFunc("POST /admin/gallery/{id}/publish", h.AdminGalleryPublishToggle)

	mux.HandleFunc("GET /admin/newsletters", h.AdminNewsletterList)
	mux.HandleFunc("GET /admin/newsletters/new", h.AdminNewsletterNew)
	mux.HandleFunc("POST /admin/newsletters", h.AdminNewsletterCreate)
	mux.HandleFunc("GET /admin/newsletters/{id}/edit", h.AdminNewsletterEdit)
	mux.HandleFunc("POST /admin/newsletters/{id}", h.AdminNewsletterUpdate)
	mux.HandleFunc("POST /admin/newsletters/{id}/send", h.AdminNewsletterSend)

	// File library and event photos (internal/web/files.go).
	mux.HandleFunc("GET /files", h.FileLibrary)
	mux.HandleFunc("POST /files/upload", h.FileUpload)
	mux.HandleFunc("GET /files/{id}/download", h.FileDownload)
	mux.HandleFunc("POST /files/{id}/delete", h.FileDelete)
	mux.HandleFunc("POST /files/{id}/link", h.FileSetEventLinks)
	mux.HandleFunc("POST /files/{id}/public", h.FileSetPublic)

	// Custom roles — super_admin only (internal/web/admin_roles.go).
	mux.HandleFunc("GET /admin/custom-roles", h.AdminCustomRolesList)
	mux.HandleFunc("POST /admin/custom-roles", h.AdminCustomRolesCreate)
	mux.HandleFunc("POST /admin/custom-roles/{id}/delete", h.AdminCustomRolesDelete)

	// Self-service contact info and the family directory it feeds (internal/web/my_family.go).
	mux.HandleFunc("GET /my-family", h.MyFamily)
	mux.HandleFunc("POST /my-family/members/{id}", h.MyFamilyUpdateMember)
	mux.HandleFunc("POST /my-family/address", h.MyFamilyUpdateAddress)
	mux.HandleFunc("GET /directory", h.FamilyDirectory)

	// Members-only patrol/den pages (internal/web/groups.go).
	mux.HandleFunc("GET /groups", h.GroupsList)
	mux.HandleFunc("GET /groups/{id}", h.GroupView)
	mux.HandleFunc("GET /admin/groups/{id}", h.AdminGroupEdit)
	mux.HandleFunc("POST /admin/groups/{id}", h.AdminGroupUpdate)
	mux.HandleFunc("POST /admin/groups/{id}/photos", h.AdminGroupSetPhotos)
}

// baseData is embedded in every page's template data.
type baseData struct {
	Unit                     units.Unit
	LoggedIn                 bool
	CanEditContent           bool // leader roles only — drives the "Edit Homepage" nav link and homepage edit affordances
	CanManageLedger          bool // Treasurer/super_admin in *this* unit — drives the "Treasury" nav link
	IsSuperAdmin             bool // super_admin in *this* unit — drives the "Site Settings" nav link (see internal/web/settings_admin.go)
	AdvancementEnabled       bool // this unit's settings.AdvancementEnabled toggle — drives the "Advancement"/"Manage Advancement" nav links, see internal/web/advancement.go
	ScoutAccountsSelfService bool // this unit's settings.ScoutAccountSelfService toggle — drives the "My Accounts" nav link and family self-service account access, see internal/web/accounts.go
	NeedsTwoFactorSetup      bool // this login holds a treasury role, or the require-two-factor-for-all setting is on, and hasn't confirmed TOTP enrollment yet — drives a persistent nudge banner, see base.html
	PageTitle                string
	Flash                    string
	CSRFToken                string // embedded as a hidden field in every <form method="post"> — see internal/csrf
	Version                  string // this build's release version — see internal/version, shown in base.html's footer
}

// rolesFor resolves the current login's roles in a unit. A family-wide
// login (user.MemberID == nil, still the default/common case) gets the
// union of every role anyone in the family holds — RolesForFamilyInUnit's
// original Phase 1 behavior. An individual member login (see
// internal/auth.User.MemberID — e.g. a Scout logging in as themselves
// rather than through their family's shared login) gets only that one
// member's own roles: per the site's "an individual login sees just their
// own stuff" design, a parent's Scoutmaster access should never leak into
// their Scout's own login just because they share a family.
func (h *Handlers) rolesFor(ctx context.Context, user auth.User, unitID string) ([]string, error) {
	if user.MemberID != nil {
		return units.RolesForMemberInUnit(ctx, h.Pool, *user.MemberID, unitID)
	}
	return units.RolesForFamilyInUnit(ctx, h.Pool, user.FamilyID, unitID)
}

// capabilitiesFor resolves the current login's roles in a unit straight
// into the capabilities they grant (see units.CapabilitiesForRoles) — the
// one call every permission check (CanEditUnitContent, CanManageLedger,
// IsSuperAdmin, etc.) should go through, so a custom role's capabilities
// count exactly the same as an equivalent fixed role's everywhere in the
// app.
func (h *Handlers) capabilitiesFor(ctx context.Context, user auth.User, unitID string) (units.Capabilities, error) {
	roles, err := h.rolesFor(ctx, user, unitID)
	if err != nil {
		return nil, err
	}
	return units.CapabilitiesForRoles(ctx, h.Pool, unitID, roles)
}

// actingMember resolves which family.Member the current login's actions
// (event creation, RSVPs, ledger entries, etc.) should be attributed to.
// For an individual member login this is unambiguous — the member
// themselves, no heuristic needed. For a family-wide login it falls back
// to family.ActingMemberForFamilyInUnit's existing heuristic (prefer
// whoever holds a role in the unit, then adults, then alphabetical).
func (h *Handlers) actingMember(ctx context.Context, user auth.User, unitID string) (family.Member, error) {
	if user.MemberID != nil {
		m, found, err := family.GetMember(ctx, h.Pool, *user.MemberID)
		if err != nil {
			return family.Member{}, err
		}
		if !found {
			return family.Member{}, errMemberNotFound
		}
		return m, nil
	}
	return family.ActingMemberForFamilyInUnit(ctx, h.Pool, user.FamilyID, unitID)
}

// rosterScope resolves the current login's roster-management scope in a
// unit — roster.ScopeForMember for an individual member login, or
// roster.ScopeForFamily's original family-wide computation otherwise. See
// rolesFor's comment for why this split matters: without it, an
// individual login belonging to a leader (e.g. an Assistant Scoutmaster
// with their own login) would have its roster scope silently broadened by
// whatever roles OTHER members of their family happen to hold, which
// breaks the "just their own stuff" guarantee just as surely as leaking
// permissions would.
func (h *Handlers) rosterScope(ctx context.Context, user auth.User, unitID string) (roster.Scope, error) {
	if user.MemberID != nil {
		return roster.ScopeForMember(ctx, h.Pool, *user.MemberID, unitID)
	}
	return roster.ScopeForFamily(ctx, h.Pool, user.FamilyID, unitID)
}

// isAccountOwner reports whether the current login "owns" a ledger
// account belonging to memberID — for an individual member login, exact
// match only (per the "just their own stuff" design: a Scout's own login
// sees their own account, not their siblings'); for a family-wide login,
// broader family membership (a parent should still be able to see/manage
// any of their children's accounts through the shared login).
func isAccountOwner(ctx context.Context, pool *pgxpool.Pool, user auth.User, memberID string) (bool, error) {
	if user.MemberID != nil {
		return *user.MemberID == memberID, nil
	}
	return family.MemberBelongsToFamily(ctx, pool, memberID, user.FamilyID)
}

// scoutAccountSelfServiceEnabled reports whether families/individuals may
// reach their own Scout ledger account in this unit (see
// settings.ScoutAccountSelfService). When false, account ownership stops
// granting access — only a Treasurer/super_admin can view or move money on
// an account — so the account handlers gate their "owner" path on this.
func (h *Handlers) scoutAccountSelfServiceEnabled(ctx context.Context, unitID string) (bool, error) {
	return settings.GetForUnit(ctx, h.Pool, unitID, settings.ScoutAccountSelfService)
}

func (h *Handlers) base(r *http.Request, pageTitle string) baseData {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	data := baseData{Unit: unit, LoggedIn: loggedIn, PageTitle: pageTitle, CSRFToken: csrf.TokenFromContext(r.Context()), Version: version.Version}
	if loggedIn {
		caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
		if err != nil {
			log.Printf("web: loading capabilities: %v", err)
		}
		data.CanEditContent = units.CanEditUnitContent(caps)
		data.CanManageLedger = units.CanManageLedger(caps)
		data.IsSuperAdmin = units.IsSuperAdmin(caps)

		if enabled, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, settings.AdvancementEnabled); err != nil {
			log.Printf("web: checking advancement-enabled setting: %v", err)
		} else {
			data.AdvancementEnabled = enabled
		}

		if enabled, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, settings.ScoutAccountSelfService); err != nil {
			log.Printf("web: checking scout-account-self-service setting: %v", err)
		} else {
			// The "My Accounts" nav link shows when families may see their
			// own balances, or always for a treasurer (who reaches accounts
			// through the treasury area regardless of the family-facing toggle).
			data.ScoutAccountsSelfService = enabled
		}

		// Cross-unit check (not the per-unit `caps` above) — a login that's
		// Treasurer on only one of the two units still needs 2FA nudged
		// everywhere, since single sign-on means one session already spans
		// both subdomains. Short-circuits past the settings.Get call for
		// the common case (a login with no treasury role anywhere and the
		// site-wide setting off).
		var needsNudge bool
		if user.MemberID != nil {
			needsNudge, err = units.MemberHasAnyTreasuryRole(r.Context(), h.Pool, *user.MemberID)
		} else {
			needsNudge, err = units.FamilyHasAnyTreasuryRole(r.Context(), h.Pool, user.FamilyID)
		}
		if err != nil {
			log.Printf("web: checking two-factor requirement: %v", err)
		}
		if !needsNudge {
			if requireForAll, err := settings.Get(r.Context(), h.Pool, settings.RequireTwoFactorForAll); err != nil {
				log.Printf("web: checking require-two-factor-for-all setting: %v", err)
			} else {
				needsNudge = requireForAll
			}
		}
		if needsNudge {
			if _, confirmed, err := auth.TOTPStatus(r.Context(), h.Pool, user.ID); err != nil {
				log.Printf("web: checking two-factor status: %v", err)
			} else {
				data.NeedsTwoFactorSetup = !confirmed
			}
		}
	}
	return data
}

func (h *Handlers) render(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("web: template execution error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// --- Home -------------------------------------------------------------

// sectionText returns a saved section's body, falling back to its
// placeholder if a leader hasn't saved anything yet (or saved blank) — the
// homepage should never show a raw empty slot. Most url-kind sections
// default to a stock photo placeholder; home-social has no placeholder and
// comes back empty so the template can skip rendering it.
func sectionText(saved map[string]content.Section, def content.SectionDef) string {
	if s, ok := saved[def.Slug]; ok && s.Body != "" {
		return s.Body
	}
	return def.Placeholder
}

func (h *Handlers) Home(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	events, err := calendar.ListUpcomingPublicForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing public events: %v", err)
	}

	saved, err := content.SectionsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading homepage content: %v", err)
	}

	text := make(map[string]string)
	for _, def := range content.HomepageSections(unit.UnitType) {
		text[def.Slug] = sectionText(saved, def)
	}

	// "Our program" is entered one activity per line and rendered as a
	// bulleted list, mirroring pack6crestwood.org's "OUR PROGRAM" section.
	var programItems []string
	for _, line := range strings.Split(text["home-program"], "\n") {
		if line = strings.TrimSpace(line); line != "" {
			programItems = append(programItems, line)
		}
	}

	// The gallery strip's second photo (and its caption) leans Cub-Scout- or
	// Troop-specific — Pinewood Derby is a Cub Scout program element, so it
	// doesn't make sense to caption it that way on the Troop site.
	gallery2Caption := "Pinewood Derby Fun"
	if unit.UnitType == "troop" {
		gallery2Caption = "Adventure Awaits"
	}

	data := struct {
		baseData
		Events          []calendar.Event
		Hero            string
		HeroImageURL    string
		ProgramItems    []string
		ProgramImageURL string
		Meeting         string
		Leadership      string
		Gallery1URL     string
		Gallery2URL     string
		Gallery2Caption string
		SocialURL       string
		FacebookURL     string
		InstagramURL    string
		TikTokURL       string
	}{
		baseData:        h.base(r, ""),
		Events:          events,
		Hero:            text["home-hero"],
		HeroImageURL:    text["home-hero-image"],
		ProgramItems:    programItems,
		ProgramImageURL: text["home-program-image"],
		Meeting:         text["home-meeting"],
		Leadership:      text["home-leadership"],
		Gallery1URL:     text["home-gallery-1"],
		Gallery2URL:     text["home-gallery-2"],
		Gallery2Caption: gallery2Caption,
		SocialURL:       text["home-social"],
		FacebookURL:     text["home-facebook"],
		InstagramURL:    text["home-instagram"],
		TikTokURL:       text["home-tiktok"],
	}

	h.render(w, h.home, data)
}

// --- Homepage content editing (admin) --------------------------------------

// homeAdminRow is one section's editing state for the /admin/home list.
type homeAdminRow struct {
	Slug        string
	Label       string
	Placeholder string
	Help        string
	Kind        string // "" = textarea, "url" = single-line link input
	Body        string
	Saved       bool // false if a leader hasn't ever saved this section — the placeholder is what's showing live
}

func (h *Handlers) HomeContentList(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next=/admin/home", http.StatusSeeOther)
		return
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to edit this site's homepage", http.StatusForbidden)
		return
	}

	saved, err := content.SectionsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading homepage content: %v", err)
	}

	defs := content.HomepageSections(unit.UnitType)
	rows := make([]homeAdminRow, 0, len(defs))
	for _, def := range defs {
		row := homeAdminRow{Slug: def.Slug, Label: def.Label, Placeholder: def.Placeholder, Help: def.Help, Kind: def.Kind}
		if s, ok := saved[def.Slug]; ok {
			row.Body = s.Body
			row.Saved = true
		}
		rows = append(rows, row)
	}

	publicImages, err := files.ListPublicImagesForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading public images: %v", err)
	}

	data := struct {
		baseData
		Sections     []homeAdminRow
		PublicImages []files.File
	}{baseData: h.base(r, "Edit Homepage"), Sections: rows, PublicImages: publicImages}
	h.render(w, h.contentAdmin, data)
}

func (h *Handlers) HomeContentSave(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to edit this site's homepage", http.StatusForbidden)
		return
	}

	slug := r.PathValue("slug")
	var label string
	valid := false
	for _, def := range content.HomepageSections(unit.UnitType) {
		if def.Slug == slug {
			valid = true
			label = def.Label
			break
		}
	}
	if !valid {
		http.Error(w, "unknown homepage section", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return
	}

	if _, err := content.UpsertSection(r.Context(), h.Pool, unit.ID, slug, label, r.FormValue("body"), actor.ID); err != nil {
		log.Printf("web: saving homepage section: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/home", http.StatusSeeOther)
}

// --- Login / logout -----------------------------------------------------

// sanitizeNextPath restricts a post-login redirect target to a same-site
// relative path, never an absolute URL — otherwise a crafted
// /login?next=https://evil.example.com/... link would have a successful,
// legitimate login silently hand the visitor off to an attacker-controlled
// page (a phishing pivot that abuses trust in the real login flow). "//" is
// rejected too, since browsers treat a scheme-relative URL as absolute.
func sanitizeNextPath(next string) string {
	if next == "" || next[0] != '/' || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func (h *Handlers) LoginForm(w http.ResponseWriter, r *http.Request) {
	data := struct {
		baseData
		Next string
	}{baseData: h.base(r, "Log in"), Next: sanitizeNextPath(r.URL.Query().Get("next"))}
	h.render(w, h.login, data)
}

func (h *Handlers) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	email := r.FormValue("email")
	password := r.FormValue("password")
	next := sanitizeNextPath(r.FormValue("next"))

	user, err := auth.Authenticate(r.Context(), h.Pool, email, password)
	if err != nil {
		flash := "Incorrect email or password."
		if errors.Is(err, auth.ErrAccountLocked) {
			flash = "Too many failed login attempts. Please wait a few minutes and try again."
		}
		data := struct {
			baseData
			Next string
		}{baseData: h.base(r, "Log in"), Next: next}
		d := data
		d.Flash = flash
		h.render(w, h.login, d)
		return
	}

	// Phase 2: a login with confirmed TOTP enrollment needs a second
	// factor before a real session gets created — see internal/auth/totp.go.
	// This applies to ANY confirmed enrollment, not just Treasurer/
	// super_admin logins: two-factor is available to every user as an
	// opt-in security measure (see /settings/2fa, reachable from every
	// logged-in page's nav), and once someone has actually gone through
	// enrollment, logging in should ask for their code regardless of why
	// they set it up. What varies by role (and by the site-wide
	// "require two-factor for everyone" setting) is only whether a login
	// that HASN'T enrolled yet gets nudged to — see baseData
	// .NeedsTwoFactorSetup in h.base, which never blocks a login outright,
	// only this actually-enrolled check does.
	_, confirmed, err := auth.TOTPStatus(r.Context(), h.Pool, user.ID)
	if err != nil {
		log.Printf("web: checking two-factor status: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if confirmed {
		pendingToken, pendingExpiresAt, err := auth.CreatePendingTwoFactorLogin(r.Context(), h.Pool, user.ID, next)
		if err != nil {
			log.Printf("web: creating pending two-factor login: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		auth.SetPendingTwoFactorCookie(w, pendingToken, pendingExpiresAt, h.CookieDomain, h.SecureCookie)
		http.Redirect(w, r, "/login/2fa", http.StatusSeeOther)
		return
	}

	token, expiresAt, err := auth.CreateSession(r.Context(), h.Pool, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	auth.SetSessionCookie(w, token, expiresAt, h.CookieDomain, h.SecureCookie)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		_ = auth.DestroySession(r.Context(), h.Pool, cookie.Value)
	}
	auth.ClearSessionCookie(w, h.CookieDomain, h.SecureCookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- Roster ---------------------------------------------------------------

func (h *Handlers) Roster(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if _, loggedIn := auth.UserFromContext(r.Context()); !loggedIn {
		http.Redirect(w, r, "/login?next=/roster", http.StatusSeeOther)
		return
	}

	roster, err := family.RosterForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading roster: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	data := struct {
		baseData
		Roster []family.RosterEntry
	}{baseData: h.base(r, "Roster"), Roster: roster}
	h.render(w, h.roster, data)
}

// --- Calendar ---------------------------------------------------------------

type pendingApprovalView struct {
	approval.Request
	EventTitle      string
	SubmittedByName string
}

// eventView is a calendar.Event decorated with whatever files/photos are
// linked to it — the calendar page's event list shows both inline.
type eventView struct {
	calendar.Event
	Files        []files.File
	SubGroupName string // "" if unscoped — see calendar.Event.SubGroupID
}

// parseMonthParam resolves the year/month a calendar page should show from
// its ?month=YYYY-MM query param, defaulting to the current month when the
// param is absent or malformed — a bad/garbled query string should show
// today's calendar, not an error page.
func parseMonthParam(raw string, today time.Time) (int, time.Month) {
	if t, err := time.ParseInLocation("2006-01", raw, today.Location()); err == nil {
		return t.Year(), t.Month()
	}
	return today.Year(), today.Month()
}

func (h *Handlers) Calendar(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())

	// Unauthenticated visitors must only ever see public events — members-only
	// events (the default visibility for anything not explicitly marked
	// public) are for logged-in families only.
	var events []calendar.Event
	var err error
	if loggedIn {
		events, err = calendar.ListUpcomingForUnit(r.Context(), h.Pool, unit.ID)
	} else {
		events, err = calendar.ListUpcomingPublicForUnit(r.Context(), h.Pool, unit.ID)
	}
	if err != nil {
		log.Printf("web: listing events: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	today := time.Now().In(time.Local)
	year, month := parseMonthParam(r.URL.Query().Get("month"), today)
	rangeStart, rangeEnd := calendar.GridRange(year, month, today.Location())
	var monthEvents []calendar.Event
	if loggedIn {
		monthEvents, err = calendar.ListForRangeForUnit(r.Context(), h.Pool, unit.ID, rangeStart, rangeEnd)
	} else {
		monthEvents, err = calendar.ListForRangePublicForUnit(r.Context(), h.Pool, unit.ID, rangeStart, rangeEnd)
	}
	if err != nil {
		log.Printf("web: listing events for month grid: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var canCreate, requiresApproval, canApprove, canManageFiles bool
	var pendingViews []pendingApprovalView
	var creatableSubGroups []roster.SubGroup

	if loggedIn {
		caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
		if err != nil {
			log.Printf("web: loading capabilities: %v", err)
		}
		canEditContent := units.CanEditUnitContent(caps)
		canCreate = canEditContent || units.CanSubmitForApproval(caps)
		requiresApproval = !canEditContent && units.CanSubmitForApproval(caps)
		canApprove = units.CanApprove(caps)
		canManageFiles = canEditContent

		if canApprove {
			pending, err := approval.PendingForUnit(r.Context(), h.Pool, unit.ID)
			if err != nil {
				log.Printf("web: loading pending approvals: %v", err)
			}
			pendingViews = h.decoratePending(r, pending)
		}

		// A den-scoped event (see migration 0018) is only visible to
		// members of that den, plus any leader broad enough to hold
		// CanEditUnitContent (same "broad content access" den_leader
		// already gets elsewhere, needed here for cross-den scheduling
		// oversight) — everyone else only sees unscoped, whole-unit events
		// plus whichever den(s) their own family actually belongs to.
		var viewerSubGroupIDs []string
		if user.MemberID != nil {
			viewerSubGroupIDs, err = roster.SubGroupIDsForMember(r.Context(), h.Pool, *user.MemberID, unit.ID)
		} else {
			viewerSubGroupIDs, err = roster.SubGroupIDsForFamily(r.Context(), h.Pool, user.FamilyID, unit.ID)
		}
		if err != nil {
			log.Printf("web: loading viewer's sub-group membership: %v", err)
		}
		viewerSubGroups := make(map[string]bool, len(viewerSubGroupIDs))
		for _, id := range viewerSubGroupIDs {
			viewerSubGroups[id] = true
		}
		events = calendar.FilterVisibleToViewer(events, viewerSubGroups, canEditContent)
		monthEvents = calendar.FilterVisibleToViewer(monthEvents, viewerSubGroups, canEditContent)

		// The create form's "scope to a patrol/den" picker: a broad leader
		// may schedule for any sub-group, a scoped submitter (e.g. a
		// Patrol Leader with only CanSubmitForApproval) only for their own.
		if canCreate {
			if canEditContent {
				creatableSubGroups, err = roster.SubGroupsForUnit(r.Context(), h.Pool, unit.ID)
				if err != nil {
					log.Printf("web: loading sub-groups: %v", err)
				}
			} else {
				allGroups, err := roster.SubGroupsForUnit(r.Context(), h.Pool, unit.ID)
				if err != nil {
					log.Printf("web: loading sub-groups: %v", err)
				}
				for _, g := range allGroups {
					if viewerSubGroups[g.ID] {
						creatableSubGroups = append(creatableSubGroups, g)
					}
				}
			}
		}
	}

	monthGrid := calendar.BuildMonthGrid(monthEvents, year, month, today)

	eventIDs := make([]string, len(events))
	for i, e := range events {
		eventIDs[i] = e.ID
	}
	filesByEvent, err := files.ListForEvents(r.Context(), h.Pool, eventIDs)
	if err != nil {
		log.Printf("web: loading files linked to events: %v", err)
	}

	subGroups, err := roster.SubGroupsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading sub-groups: %v", err)
	}
	subGroupNameByID := make(map[string]string, len(subGroups))
	for _, g := range subGroups {
		subGroupNameByID[g.ID] = g.Name
	}

	eventViews := make([]eventView, len(events))
	for i, e := range events {
		var subGroupName string
		if e.SubGroupID != nil {
			subGroupName = subGroupNameByID[*e.SubGroupID]
		}
		eventViews[i] = eventView{Event: e, Files: filesByEvent[e.ID], SubGroupName: subGroupName}
	}

	data := struct {
		baseData
		Events             []eventView
		Month              calendar.MonthGrid
		CanCreate          bool
		RequiresApproval   bool
		PendingApprovals   []pendingApprovalView
		CanManageFiles     bool
		SubGroupNoun       string
		CreatableSubGroups []roster.SubGroup
	}{
		baseData:           h.base(r, "Calendar"),
		Events:             eventViews,
		Month:              monthGrid,
		CanManageFiles:     canManageFiles,
		CanCreate:          canCreate,
		RequiresApproval:   requiresApproval,
		PendingApprovals:   pendingViews,
		SubGroupNoun:       subGroupNoun(unit.UnitType),
		CreatableSubGroups: creatableSubGroups,
	}
	h.render(w, h.calendar, data)
}

// decoratePending fetches display-friendly fields (event title, submitter
// name) for a set of pending approval requests. Kept as a small N+1 query
// loop for now — the pending list is expected to be a handful of items at
// most; revisit with a join if that stops being true.
func (h *Handlers) decoratePending(r *http.Request, reqs []approval.Request) []pendingApprovalView {
	var views []pendingApprovalView
	for _, req := range reqs {
		var title, firstName, lastName string
		_ = h.Pool.QueryRow(r.Context(), `SELECT title FROM events WHERE id = $1`, req.EntityID).Scan(&title)
		_ = h.Pool.QueryRow(r.Context(), `SELECT first_name, last_name FROM members WHERE id = $1`, req.SubmittedBy).Scan(&firstName, &lastName)
		views = append(views, pendingApprovalView{
			Request:         req,
			EventTitle:      title,
			SubmittedByName: firstName + " " + lastName,
		})
	}
	return views
}

func (h *Handlers) CalendarCreate(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !units.CanEditUnitContent(caps) && !units.CanSubmitForApproval(caps) {
		http.Error(w, "you don't have permission to add events", http.StatusForbidden)
		return
	}

	startsAt, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("starts_at"), time.Local)
	if err != nil {
		http.Error(w, "invalid start time", http.StatusBadRequest)
		return
	}

	// End date/time is optional — most events are a single point in time
	// (or at least don't need a precise end recorded). When given, it's
	// what lets a multi-day event (a weekend campout, e.g.) show correctly
	// across every day it spans on the month grid, not just its start day.
	var endsAt *time.Time
	if raw := strings.TrimSpace(r.FormValue("ends_at")); raw != "" {
		parsed, err := time.ParseInLocation("2006-01-02T15:04", raw, time.Local)
		if err != nil {
			http.Error(w, "invalid end time", http.StatusBadRequest)
			return
		}
		if parsed.Before(startsAt) {
			http.Error(w, "end time can't be before the start time", http.StatusBadRequest)
			return
		}
		endsAt = &parsed
	}

	visibility := "members"
	if r.FormValue("public") == "1" {
		visibility = "public"
	}

	// A patrol/den-scoped event is inherently members-only — the
	// unauthenticated calendar path (ListUpcomingPublicForUnit et al.)
	// doesn't apply any sub-group filtering at all, so a "public" event
	// scoped to one den would otherwise leak straight past
	// FilterVisibleToViewer to every logged-out visitor. Scoping always
	// wins over the "visible to the public" checkbox.
	var subGroupID *string
	if raw := strings.TrimSpace(r.FormValue("sub_group_id")); raw != "" {
		subGroupUnitID, ok, err := roster.SubGroupUnitID(r.Context(), h.Pool, raw)
		if err != nil {
			log.Printf("web: looking up sub-group: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok || subGroupUnitID != unit.ID {
			http.Error(w, "that "+subGroupNoun(unit.UnitType)+" doesn't exist in this unit", http.StatusBadRequest)
			return
		}

		allowed := units.CanEditUnitContent(caps)
		if !allowed {
			var memberSubGroups []string
			if user.MemberID != nil {
				memberSubGroups, err = roster.SubGroupIDsForMember(r.Context(), h.Pool, *user.MemberID, unit.ID)
			} else {
				memberSubGroups, err = roster.SubGroupIDsForFamily(r.Context(), h.Pool, user.FamilyID, unit.ID)
			}
			if err != nil {
				log.Printf("web: checking sub-group membership: %v", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			for _, id := range memberSubGroups {
				if id == raw {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			http.Error(w, "you can only scope an event to your own "+subGroupNoun(unit.UnitType), http.StatusForbidden)
			return
		}

		subGroupID = &raw
		visibility = "members"
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return
	}

	_, err = calendar.Create(r.Context(), h.Pool, calendar.CreateInput{
		UnitID:      unit.ID,
		Title:       r.FormValue("title"),
		SubGroupID:  subGroupID,
		Description: r.FormValue("description"),
		Location:    r.FormValue("location"),
		StartsAt:    startsAt,
		EndsAt:      endsAt,
		Visibility:  visibility,
		CreatedBy:   actor.ID,
	}, units.CanEditUnitContent(caps))
	if err != nil {
		log.Printf("web: creating event: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

func (h *Handlers) CalendarRSVP(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	eventID := r.PathValue("id")
	response := r.FormValue("response")

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member", http.StatusBadRequest)
		return
	}
	if err := calendar.SetRSVP(r.Context(), h.Pool, eventID, unit.ID, actor.ID, response); err != nil {
		if errors.Is(err, calendar.ErrEventNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("web: setting rsvp: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

func (h *Handlers) ApprovalDecide(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanApprove(caps) {
		http.Error(w, "you don't have permission to approve items", http.StatusForbidden)
		return
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member", http.StatusBadRequest)
		return
	}

	requestID := r.PathValue("id")
	approve := r.FormValue("decision") == "approve"
	if err := approval.Decide(r.Context(), h.Pool, requestID, unit.ID, actor.ID, approve); err != nil {
		if errors.Is(err, approval.ErrNotFound) {
			http.Error(w, "that approval request doesn't exist (or was already decided)", http.StatusNotFound)
			return
		}
		log.Printf("web: deciding approval: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

// --- Audit — see internal/web/audit.go for AuditView/AuditExport -----------
