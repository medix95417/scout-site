// Package web holds the HTTP handlers and templates that render the site.
// Deliberately server-rendered (html/template + htmx) rather than a
// separate frontend — see scout-website-architecture-phase1.md Section 1
// for why.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/approval"
	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/csp"
	"github.com/47-yonkers/scout-site/internal/csrf"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/leaders"
	"github.com/47-yonkers/scout-site/internal/ledger"
	"github.com/47-yonkers/scout-site/internal/mailer"
	"github.com/47-yonkers/scout-site/internal/ratelimit"
	"github.com/47-yonkers/scout-site/internal/roster"
	"github.com/47-yonkers/scout-site/internal/settings"
	"github.com/47-yonkers/scout-site/internal/storage"
	"github.com/47-yonkers/scout-site/internal/units"
	"github.com/47-yonkers/scout-site/internal/version"
)

//go:embed all:templates
var templatesFS embed.FS

// staticFS holds small, built-in brand assets (currently just the official
// Cub Scouts/Scouts BSA trademark images a unit's logo_url may point at) —
// unlike uploaded files/photos, these ship with the binary rather than
// living in per-unit object storage, since they're the same fixed set of
// program trademarks for every install rather than unit-specific content.
//
//go:embed all:static
var staticFS embed.FS

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

	// TrustProxyHeaders — see internal/config.Config.TrustProxyHeaders and
	// clientIP.
	TrustProxyHeaders bool

	// UnsubscribeSecret signs the one-click unsubscribe links in prospect
	// campaign emails. Set from config.SessionSecret, whose doc comment
	// has always said it is the secret to use once something needed
	// signing — this is that something. Deriving the link means no
	// unsubscribe token is ever stored next to the address it protects.
	// See internal/prospect.UnsubscribeToken.
	UnsubscribeSecret []byte

	// orderLimiter bounds how many fundraiser orders one address can place.
	// The storefront order form is the only place an anonymous visitor can
	// write to the database, so it's the only one that needs this.
	orderLimiter *ratelimit.Limiter

	// loginLimiter and resetLimiter are per-address ceilings on the two
	// other endpoints an anonymous visitor can reach. They sit alongside
	// the existing per-ACCOUNT limits in internal/auth (8 failed logins
	// per email, 3 reset emails per email) rather than replacing them —
	// the two answer different questions. A per-account lock stops one
	// family's password being guessed however many addresses it comes
	// from; a per-address cap stops one machine working through many
	// accounts.
	loginLimiter *ratelimit.Limiter
	resetLimiter *ratelimit.Limiter

	// joinLimiter bounds the public "interested in joining" form. Like
	// the storefront it is an anonymous write, and unlike the storefront
	// it also sends mail, so an unbounded one would let a stranger use
	// this site to post arbitrary text into a leader's inbox as fast as
	// they can loop.
	joinLimiter *ratelimit.Limiter

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
	changePassword       *template.Template
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
	newsletterView *template.Template

	rosterImport        *template.Template
	rosterImportResults *template.Template

	calendarSubscribe *template.Template
	calendarFeeds     *template.Template

	advancement      *template.Template
	advancementAdmin *template.Template

	fileLibrary *template.Template

	customRoles *template.Template

	myFamily        *template.Template
	familyDirectory *template.Template

	groupsList *template.Template
	groupView  *template.Template
	groupAdmin *template.Template

	resourcesList *template.Template

	helpPage *template.Template

	joinPage      *template.Template
	prospectsPage *template.Template

	campaignForm *template.Template
	campaignView *template.Template
	unsubscribed *template.Template

	treasuryReports    *template.Template
	treasuryReportView *template.Template

	treasuryReconciliations *template.Template
	treasuryReconciliation  *template.Template

	leadersList          *template.Template
	adminLeadersList     *template.Template
	adminLeadersFormTmpl *template.Template

	fundraiserStorefront *template.Template
	expenseApprovals     *template.Template
	fundraiserThankYou   *template.Template
}

// templateFuncs are available to every page template. formatCents is the
// only one so far — Phase 2's treasury templates need to render signed
// cent amounts as "$12.34"/"-$12.34" and Go templates have no arithmetic
// or number-formatting of their own.
var templateFuncs = template.FuncMap{
	"formatCents":       formatCents,
	"hasPrefix":         strings.HasPrefix,
	"dict":              templateDict,
	"galleryPhotos":     templateGalleryPhotos,
	"chunkFiles":        chunkFiles,
	"chunkFileRows":     chunkFileRows,
	"heroSizeClass":     heroSizeClass,
	"homeHeroSizeClass": homeHeroSizeClass,
	"thumbURL":          thumbURL,
	"photoFocusClass":   photoFocusClass,
}

// thumbURL rewrites one of this app's own /files/{id}/download URLs to
// its small, resized preview counterpart (see Handlers.FileThumbnail) —
// used everywhere a photo displays at thumbnail/carousel size rather
// than full size, so a page with many photos doesn't make a visitor
// download every one at full camera resolution just to show it a few
// hundred pixels wide. A URL that isn't shaped like our own file
// download link (an external URL a leader pasted in directly — see
// "Image picker: allow web URL or own store everywhere") passes through
// unchanged, since there's nothing here to resize.
func thumbURL(url string) string {
	const prefix, suffix = "/files/", "/download"
	if !strings.HasPrefix(url, prefix) || !strings.HasSuffix(url, suffix) {
		return url
	}
	id := strings.TrimSuffix(strings.TrimPrefix(url, prefix), suffix)
	if id == "" || strings.ContainsRune(id, '/') {
		return url
	}
	return prefix + id + "/thumb"
}

// heroSizeClass maps a content.HeroSize preset to the Tailwind height
// classes for a plain background-image hero banner — base.html's
// per-page hero and group-view.html's den/patrol hero. Falls back to the
// original fixed size (content.HeroSizeMedium) for anything unrecognized,
// same as content.NormalizeHeroSize.
func heroSizeClass(size string) string {
	switch size {
	case content.HeroSizeShort:
		return "h-24 sm:h-32"
	case content.HeroSizeTall:
		return "h-64 sm:h-80"
	default:
		return "h-40 sm:h-56"
	}
}

// homeHeroSizeClass is heroSizeClass's sibling for the homepage's own
// hero (home.html) — a text+CTA overlay sized by vertical padding around
// its content rather than a fixed element height, since its content's
// own height needs to stay part of the layout.
func homeHeroSizeClass(size string) string {
	switch size {
	case content.HeroSizeShort:
		return "py-12 sm:py-16"
	case content.HeroSizeTall:
		return "py-40 sm:py-56"
	default:
		return "py-28 sm:py-36"
	}
}

// photoFocusClass maps a leaders.PhotoFocus preset to the Tailwind
// object-position class an object-cover leader photo uses to pick which
// part survives the crop when the photo's aspect ratio doesn't match
// its fixed-height card (see leaders.html/admin-leaders-list.html).
// Falls back to the original centered crop (leaders.PhotoFocusCenter)
// for anything unrecognized, same as leaders.NormalizePhotoFocus.
func photoFocusClass(focus string) string {
	switch focus {
	case leaders.PhotoFocusTop:
		return "object-top"
	case leaders.PhotoFocusBottom:
		return "object-bottom"
	default:
		return "object-center"
	}
}

// chunkFiles splits fs into pages of at most size files each — the "show
// 25 at a time" pagination behind eventAccordionPickerStrip and
// eventAccordionCheckboxGridRow (see _image-picker.html): an event with
// hundreds of photos only ever renders one page's worth of thumbnails
// until a leader clicks "Show more" for the next, keeping a single very
// full event from undoing the accordion's own lazy-loading benefit.
func chunkFiles(fs []files.File, size int) [][]files.File {
	if size <= 0 || len(fs) == 0 {
		return nil
	}
	var chunks [][]files.File
	for size < len(fs) {
		fs, chunks = fs[size:], append(chunks, fs[:size:size])
	}
	return append(chunks, fs)
}

// chunkFileRows is chunkFiles' sibling for []fileRow — the file
// library's own "show 25 at a time" pagination within each of its
// event-grouped accordions (see files.html's fileLibraryGroup), since a
// fileRow (a decorated files.File, with its own management controls) is
// a different type from the bare files.File the photo pickers page
// through.
func chunkFileRows(fs []fileRow, size int) [][]fileRow {
	if size <= 0 || len(fs) == 0 {
		return nil
	}
	var chunks [][]fileRow
	for size < len(fs) {
		fs, chunks = fs[size:], append(chunks, fs[:size:size])
	}
	return append(chunks, fs)
}

// templateGalleryPhotos parses a Kind:"images" section's saved body (or,
// if never saved, its placeholder — the stock default photos) into the
// list photoCarousel renders, so /admin/home shows a live preview of the
// same carousel the homepage will.
func templateGalleryPhotos(body, placeholder string) []content.GalleryPhoto {
	if body == "" {
		body = placeholder
	}
	return content.ParseGalleryPhotos(body)
}

// templateDict builds a map from alternating key/value arguments — the
// standard html/template idiom for passing several named values into a
// shared {{template}} block (see templates/content-admin.html's
// "imagePicker" block, reused across homepage sections, page hero
// banners, and the Gallery/homepage-gallery pickers), which otherwise only
// ever receives a single ".". Keys must be strings.
func templateDict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("dict: odd number of arguments")
	}
	m := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict: keys must be strings, got %T", pairs[i])
		}
		m[key] = pairs[i+1]
	}
	return m, nil
}

// New parses templates and returns a ready-to-use Handlers.
// redirectTo answers a GET on a POST-only form target by sending the
// browser to the page that form lives on.
func redirectTo(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, path, http.StatusSeeOther)
	}
}

// parsePageTemplate parses one page template together with the shared
// base layout and partials — the single definition of how a page is
// assembled, so that TestEveryTemplateParses exercises the same path New
// does rather than a copy of it that could drift.
func parsePageTemplate(page string) (*template.Template, error) {
	return template.New("base.html").Funcs(templateFuncs).ParseFS(templatesFS, "templates/base.html", "templates/_image-picker.html", "templates/"+page)
}

func New(pool *pgxpool.Pool, cookieDomain string, secureCookie bool, mail *mailer.Mailer, store *storage.Store) (*Handlers, error) {
	parse := parsePageTemplate

	h := &Handlers{
		Pool:         pool,
		CookieDomain: cookieDomain,
		SecureCookie: secureCookie,
		Mailer:       mail,
		Storage:      store,
		// 10 orders an hour from one address. A real family ordering
		// popcorn places one, occasionally a second for a neighbour;
		// anything past ten in an hour from the same place is a script.
		orderLimiter: ratelimit.New(10, time.Hour),
		// Counts FAILED logins only (see LoginSubmit), which is what
		// makes 15 a safe number: a Scout meeting is a whole troop behind
		// one router, and successful logins cost nothing. Fifteen wrong
		// passwords from one address in fifteen minutes is not that.
		loginLimiter: ratelimit.New(15, 15*time.Minute),
		// Generous enough for a leader helping several families reset in
		// one sitting; far below what a script would want.
		resetLimiter: ratelimit.New(10, time.Hour),

		// 5 enquiries an hour from one address. A real family sends one;
		// a second is a correction. Anything past that is either a mistake
		// worth slowing down or somebody using the form as a mail relay.
		joinLimiter: ratelimit.New(5, time.Hour),
	}

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
	if h.changePassword, err = parse("change-password.html"); err != nil {
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
	if h.newsletterView, err = parse("admin-newsletter-view.html"); err != nil {
		return nil, err
	}
	if h.rosterImport, err = parse("admin-roster-import.html"); err != nil {
		return nil, err
	}
	if h.rosterImportResults, err = parse("admin-roster-import-results.html"); err != nil {
		return nil, err
	}
	if h.calendarSubscribe, err = parse("calendar-subscribe.html"); err != nil {
		return nil, err
	}
	if h.calendarFeeds, err = parse("admin-calendar-feeds.html"); err != nil {
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
	if h.resourcesList, err = parse("resources.html"); err != nil {
		return nil, err
	}
	if h.helpPage, err = parse("help.html"); err != nil {
		return nil, err
	}
	if h.joinPage, err = parse("join.html"); err != nil {
		return nil, err
	}
	if h.campaignForm, err = parse("admin-prospect-campaign-form.html"); err != nil {
		return nil, err
	}
	if h.campaignView, err = parse("admin-prospect-campaign-view.html"); err != nil {
		return nil, err
	}
	if h.unsubscribed, err = parse("unsubscribed.html"); err != nil {
		return nil, err
	}
	if h.prospectsPage, err = parse("admin-prospects.html"); err != nil {
		return nil, err
	}
	if h.treasuryReports, err = parse("treasury-reports.html"); err != nil {
		return nil, err
	}
	if h.treasuryReportView, err = parse("treasury-report-view.html"); err != nil {
		return nil, err
	}
	if h.treasuryReconciliations, err = parse("treasury-reconciliations.html"); err != nil {
		return nil, err
	}
	if h.treasuryReconciliation, err = parse("treasury-reconciliation.html"); err != nil {
		return nil, err
	}
	if h.leadersList, err = parse("leaders.html"); err != nil {
		return nil, err
	}
	if h.adminLeadersList, err = parse("admin-leaders-list.html"); err != nil {
		return nil, err
	}
	if h.adminLeadersFormTmpl, err = parse("admin-leaders-form.html"); err != nil {
		return nil, err
	}
	if h.expenseApprovals, err = parse("expense-approvals.html"); err != nil {
		return nil, err
	}
	if h.fundraiserStorefront, err = parse("fundraiser-storefront.html"); err != nil {
		return nil, err
	}
	if h.fundraiserThankYou, err = parse("fundraiser-thank-you.html"); err != nil {
		return nil, err
	}
	return h, nil
}

// Routes registers every Phase 1 route. Mounted by cmd/server after the
// unit-resolution and auth middleware.
func (h *Handlers) Routes(mux *http.ServeMux) {
	// Built-in brand assets (trademark logos) — see staticFS's doc comment.
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("web: static assets: " + err.Error()) // can't happen — "static" is embedded above
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))

	mux.HandleFunc("GET /{$}", h.Home)
	mux.HandleFunc("GET /fundraiser", h.FundraiserStorefront)
	mux.HandleFunc("POST /fundraiser/order", h.FundraiserPlaceOrder)
	mux.HandleFunc("GET /login", h.LoginForm)
	mux.HandleFunc("POST /login", h.LoginSubmit)
	mux.HandleFunc("POST /logout", h.Logout)
	mux.HandleFunc("GET /forgot-password", h.ForgotPasswordForm)
	mux.HandleFunc("POST /forgot-password", h.ForgotPasswordSubmit)
	mux.HandleFunc("GET /reset-password", h.ResetPasswordForm)
	mux.HandleFunc("POST /reset-password", h.ResetPasswordSubmit)
	mux.HandleFunc("GET /login/change-password", h.ChangePasswordForm)
	mux.HandleFunc("POST /login/change-password", h.ChangePasswordSubmit)
	mux.HandleFunc("GET /roster", h.Roster)
	mux.HandleFunc("GET /roster/export.pdf", h.RosterExportPDF)
	mux.HandleFunc("GET /advancement", h.Advancement)
	mux.HandleFunc("GET /admin/advancement", h.AdminAdvancementList)
	mux.HandleFunc("POST /admin/advancement", h.AdminAdvancementCreate)
	mux.HandleFunc("POST /admin/advancement/bulk", h.AdminAdvancementBulkImport)
	mux.HandleFunc("POST /admin/advancement/{id}/delete", h.AdminAdvancementDelete)
	mux.HandleFunc("GET /calendar", h.Calendar)
	mux.HandleFunc("GET /calendar/export.pdf", h.CalendarExportPDF)
	mux.HandleFunc("POST /calendar", h.CalendarCreate)
	mux.HandleFunc("POST /calendar/{id}", h.CalendarUpdate)
	mux.HandleFunc("POST /calendar/{id}/delete", h.CalendarDelete)
	mux.HandleFunc("POST /calendar/{id}/rsvp", h.CalendarRSVP)
	mux.HandleFunc("GET /calendar/{id}/attendees.pdf", h.CalendarEventAttendeesExportPDF)
	mux.HandleFunc("POST /calendar/approvals/{id}/decide", h.ApprovalDecide)
	mux.HandleFunc("GET /audit", h.AuditView)
	mux.HandleFunc("GET /audit/export.csv", h.AuditExport)
	mux.HandleFunc("GET /accounts", h.AccountsView)
	mux.HandleFunc("GET /accounts/export.pdf", h.AccountsExportPDF)
	mux.HandleFunc("GET /admin/home", h.HomeContentList)
	mux.HandleFunc("POST /admin/home/{slug}", h.HomeContentSave)
	mux.HandleFunc("GET /admin/roster", h.AdminRosterList)
	mux.HandleFunc("POST /admin/roster/families", h.AdminRosterCreateFamily)
	mux.HandleFunc("POST /admin/roster/members", h.AdminRosterAddMember)
	// A form target that only answers POST returns 405 Method Not Allowed
	// the moment someone reloads or goes back onto it — which is exactly
	// what happens after an error, and reads as the site being broken
	// rather than as "that didn't work". Send a bare GET to the page the
	// form lives on instead.
	mux.HandleFunc("GET /admin/roster/members", redirectTo("/admin/roster"))
	mux.HandleFunc("GET /admin/roster/families", redirectTo("/admin/roster"))
	mux.HandleFunc("GET /admin/roster/members/{id}", h.AdminRosterMemberEdit)
	mux.HandleFunc("POST /admin/roster/members/{id}", h.AdminRosterMemberUpdate)
	mux.HandleFunc("POST /admin/roster/members/{id}/roles", h.AdminRosterAssignRole)
	mux.HandleFunc("POST /admin/roster/members/{id}/reset-password", h.AdminRosterResetPassword)
	mux.HandleFunc("POST /admin/roster/members/{id}/deactivate", h.AdminRosterMemberDeactivate)
	mux.HandleFunc("POST /admin/roster/members/{id}/reactivate", h.AdminRosterMemberReactivate)
	mux.HandleFunc("POST /admin/roster/members/{id}/login", h.AdminRosterCreateMemberLogin)
	mux.HandleFunc("POST /admin/roster/members/{id}/login/reset-password", h.AdminRosterResetMemberLoginPassword)
	mux.HandleFunc("POST /admin/roster/roles/{id}/delete", h.AdminRosterRemoveRole)
	mux.HandleFunc("POST /admin/roster/sub-groups", h.AdminRosterCreateSubGroup)
	mux.HandleFunc("POST /admin/roster/sub-groups/{id}/{action}", h.AdminRosterSetSubGroupActive)
	mux.HandleFunc("POST /admin/roster/existing-member", h.AdminRosterAssignExistingMember)
	mux.HandleFunc("POST /admin/roster/members/{id}/delete", h.AdminRosterMemberDelete)
	mux.HandleFunc("GET /admin/roster/import", h.AdminRosterImportForm)
	mux.HandleFunc("GET /admin/roster/import/template.csv", h.AdminRosterImportTemplate)
	mux.HandleFunc("POST /admin/roster/import", h.AdminRosterImportApply)

	// Phase 2: two-factor login (Treasurer/super_admin) and self-service enrollment.
	mux.HandleFunc("GET /login/2fa", h.LoginTwoFactorForm)
	mux.HandleFunc("POST /login/2fa", h.LoginTwoFactorSubmit)
	mux.HandleFunc("GET /join", h.JoinForm)
	mux.HandleFunc("POST /join", h.JoinSubmit)
	mux.HandleFunc("GET /admin/prospects", h.ProspectsList)
	mux.HandleFunc("POST /admin/prospects/{id}", h.ProspectUpdate)
	mux.HandleFunc("POST /admin/prospects/{id}/delete", h.ProspectDelete)
	mux.HandleFunc("POST /admin/prospects/{id}/opt-out", h.ProspectOptOut)

	// Mass email to prospects.
	//
	// On their own path rather than under /admin/prospects/, because
	// "/admin/prospects/campaigns/{id}" and "/admin/prospects/{id}/delete"
	// are ambiguous to Go's ServeMux — both match
	// "/admin/prospects/campaigns/delete" and neither is more specific —
	// and an ambiguous pair is a panic at registration, i.e. a server
	// that won't boot. See TestRoutesRegisterWithoutPanic.
	mux.HandleFunc("GET /admin/prospect-campaigns/new", h.AdminCampaignNew)
	mux.HandleFunc("POST /admin/prospect-campaigns", h.AdminCampaignCreate)
	mux.HandleFunc("GET /admin/prospect-campaigns/{id}", h.AdminCampaignView)
	mux.HandleFunc("GET /admin/prospect-campaigns/{id}/edit", h.AdminCampaignEdit)
	mux.HandleFunc("POST /admin/prospect-campaigns/{id}", h.AdminCampaignUpdate)
	mux.HandleFunc("POST /admin/prospect-campaigns/{id}/send", h.AdminCampaignSend)
	mux.HandleFunc("POST /admin/prospect-campaigns/{id}/delete", h.AdminCampaignDelete)
	mux.HandleFunc("GET /admin/prospect-templates/{id}", h.AdminCampaignTemplate)
	mux.HandleFunc("POST /admin/prospect-templates/{id}/delete", h.AdminCampaignTemplateDelete)

	// Public, and deliberately outside every auth check: the people who
	// need it are members of the public with no login, and the HMAC in
	// the link is the whole of the authorization. See
	// internal/web/prospect_unsubscribe.go.
	mux.HandleFunc("GET /unsubscribe", h.ProspectUnsubscribe)
	mux.HandleFunc("GET /help", h.Help)
	mux.HandleFunc("GET /admin/calendar-feeds", h.AdminCalendarFeeds)
	mux.HandleFunc("POST /admin/calendar-feeds", h.AdminCalendarFeedAdd)
	mux.HandleFunc("POST /admin/calendar-feeds/{id}/refresh", h.AdminCalendarFeedRefresh)
	mux.HandleFunc("POST /admin/calendar-feeds/{id}/toggle", h.AdminCalendarFeedToggle)
	mux.HandleFunc("POST /admin/calendar-feeds/{id}/delete", h.AdminCalendarFeedDelete)
	// Same ambiguity trap as the campaign routes above: under
	// /admin/calendar-feeds/, "conflicts/{id}" and "{id}/refresh" both
	// match "/admin/calendar-feeds/conflicts/refresh" and neither wins,
	// which is a registration panic rather than a 404.
	mux.HandleFunc("POST /admin/calendar-conflicts/{id}", h.AdminCalendarConflictResolve)
	mux.HandleFunc("GET /settings/calendar", h.CalendarSubscribe)
	mux.HandleFunc("POST /settings/calendar/regenerate", h.CalendarSubscribeRegenerate)
	mux.HandleFunc("POST /settings/calendar/remove", h.CalendarSubscribeRemove)
	// The feed itself. Public in the routing sense — a calendar app can't
	// log in — with the token in the path doing the authenticating. See
	// calendar_feed.go.
	//
	// Deliberately NOT under /calendar/: that subtree already has
	// /calendar/{id}/... routes, and a {token} wildcard beside an {id}
	// one makes "/calendar/feed/attendees.pdf" ambiguous — which Go's
	// ServeMux rejects at startup rather than resolving arbitrarily.
	mux.HandleFunc("GET /feed/{token}", h.CalendarFeed)
	mux.HandleFunc("GET /settings/2fa", h.TwoFactorSettings)
	mux.HandleFunc("POST /settings/2fa/enroll", h.TwoFactorEnroll)
	mux.HandleFunc("POST /settings/2fa/confirm", h.TwoFactorConfirm)
	mux.HandleFunc("POST /settings/2fa/disable", h.TwoFactorDisable)
	mux.HandleFunc("POST /settings/password", h.AccountChangePassword)

	// Phase 2: fund accounting — Treasurer-only unless noted.
	mux.HandleFunc("GET /treasury", h.TreasuryDashboard)
	mux.HandleFunc("GET /treasury/reports", h.TreasuryReportsList)
	mux.HandleFunc("GET /treasury/reports/view", h.TreasuryReportView)
	mux.HandleFunc("GET /treasury/reports/export.pdf", h.TreasuryReportExportPDF)
	mux.HandleFunc("POST /treasury/reports/save", h.TreasuryReportSave)
	mux.HandleFunc("GET /treasury/reports/saved/{id}/run", h.TreasuryReportRunSaved)
	mux.HandleFunc("POST /treasury/reports/saved/{id}/delete", h.TreasuryReportDeleteSaved)
	mux.HandleFunc("GET /treasury/reconciliations", h.TreasuryReconciliations)
	mux.HandleFunc("POST /treasury/reconciliations", h.TreasuryStartReconciliation)
	mux.HandleFunc("GET /treasury/reconciliations/{id}", h.TreasuryReconciliationView)
	mux.HandleFunc("POST /treasury/reconciliations/{id}/items/{postingID}", h.TreasuryReconciliationToggleItem)
	mux.HandleFunc("POST /treasury/reconciliations/{id}/complete", h.TreasuryCompleteReconciliation)
	mux.HandleFunc("POST /treasury/reconciliations/{id}/delete", h.TreasuryDeleteReconciliation)
	mux.HandleFunc("GET /expense-approvals", h.ExpenseApprovalsList)
	mux.HandleFunc("POST /expense-approvals/{id}/decide", h.ExpenseApprovalDecide)
	mux.HandleFunc("POST /treasury/transactions", h.TreasuryPostTransaction)
	mux.HandleFunc("POST /treasury/transactions/{id}/reverse", h.TreasuryReverseTransaction)
	mux.HandleFunc("POST /treasury/trip-funds", h.TreasuryCreateTripFund)
	mux.HandleFunc("POST /treasury/accounts/{id}/close", h.TreasuryCloseTripFund)
	mux.HandleFunc("POST /treasury/transfers/{id}/decide", h.TreasuryDecideTransfer)
	mux.HandleFunc("GET /treasury/accounts/{id}", h.TreasuryAccountView)
	mux.HandleFunc("GET /treasury/accounts/{id}/export.pdf", h.TreasuryAccountExportPDF)
	mux.HandleFunc("POST /treasury/accounts/{id}/transfer", h.TreasuryRequestTransfer)
	mux.HandleFunc("GET /treasury/fundraisers/{id}", h.TreasuryFundraiserView)
	mux.HandleFunc("POST /treasury/fundraisers", h.TreasuryCreateFundraiser)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/allocate", h.TreasuryAllocateFundraiser)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/allocate-bulk", h.TreasuryAllocateFundraiserBulk)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/confirm-cap", h.TreasuryConfirmFundraiserCap)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/items", h.TreasuryFundraiserAddItem)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/items/{itemID}", h.TreasuryFundraiserUpdateItem)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/items/{itemID}/delete", h.TreasuryFundraiserDeleteItem)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/button-image", h.TreasuryFundraiserSetButtonImage)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/orders/{orderID}/mark-paid", h.TreasuryOrderMarkPaid)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/orders/{orderID}/resolve-scout", h.TreasuryOrderResolveScout)
	mux.HandleFunc("POST /treasury/fundraisers/{id}/orders/{orderID}/cancel", h.TreasuryOrderCancel)

	// Site-wide settings — super_admin only.
	mux.HandleFunc("GET /admin/settings", h.SystemSettingsView)
	mux.HandleFunc("POST /admin/settings/{key}/toggle", h.SystemSettingsToggle)
	mux.HandleFunc("POST /admin/settings/text", h.SystemSettingsUpdateText)
	mux.HandleFunc("POST /admin/settings/unit/{key}/toggle", h.UnitSettingsToggle)
	mux.HandleFunc("POST /admin/settings/unit/text", h.UnitSettingsUpdateText)
	mux.HandleFunc("POST /admin/settings/unit/social", h.SocialSettingsUpdateText)
	mux.HandleFunc("POST /admin/settings/unit/welcome-email", h.WelcomeEmailSettingsUpdateText)
	// Same reasoning as the roster form targets above: a POST-only URL
	// answers 405 to the reload that inevitably follows an error page.
	mux.HandleFunc("GET /admin/settings/unit/welcome-email", redirectTo("/admin/settings"))
	mux.HandleFunc("GET /admin/settings/unit/prospects", redirectTo("/admin/settings"))
	mux.HandleFunc("POST /admin/settings/unit/prospects", h.ProspectSettingsUpdateText)
	mux.HandleFunc("POST /admin/settings/unit/treasury-controls", h.TreasuryControlsUpdateText)
	mux.HandleFunc("POST /admin/settings/unit/fundraiser-storefront", h.FundraiserStorefrontSettingsUpdate)

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

	// Public "Our Leaders" page and its admin CRUD (internal/web/leaders.go).
	mux.HandleFunc("GET /leaders", h.LeadersList)
	mux.HandleFunc("GET /admin/leaders", h.AdminLeadersList)
	mux.HandleFunc("GET /admin/leaders/new", h.AdminLeadersNew)
	mux.HandleFunc("POST /admin/leaders", h.AdminLeadersCreate)
	mux.HandleFunc("GET /admin/leaders/{id}/edit", h.AdminLeadersEdit)
	mux.HandleFunc("POST /admin/leaders/{id}", h.AdminLeadersUpdate)
	mux.HandleFunc("POST /admin/leaders/{id}/publish", h.AdminLeadersPublishToggle)
	mux.HandleFunc("POST /admin/leaders/{id}/delete", h.AdminLeadersDelete)

	mux.HandleFunc("GET /admin/newsletters", h.AdminNewsletterList)
	mux.HandleFunc("GET /admin/newsletters/new", h.AdminNewsletterNew)
	mux.HandleFunc("POST /admin/newsletters", h.AdminNewsletterCreate)
	mux.HandleFunc("GET /admin/newsletters/{id}", h.AdminNewsletterView)
	mux.HandleFunc("GET /admin/newsletters/{id}/edit", h.AdminNewsletterEdit)
	mux.HandleFunc("POST /admin/newsletters/{id}", h.AdminNewsletterUpdate)
	mux.HandleFunc("POST /admin/newsletters/{id}/send", h.AdminNewsletterSend)

	// File library and event photos (internal/web/files.go).
	mux.HandleFunc("GET /files", h.FileLibrary)
	mux.HandleFunc("POST /files/upload", h.FileUpload)
	mux.HandleFunc("GET /files/{id}/download", h.FileDownload)
	mux.HandleFunc("GET /files/{id}/thumb", h.FileThumbnail)
	mux.HandleFunc("POST /files/{id}/delete", h.FileDelete)
	mux.HandleFunc("POST /files/{id}/link", h.FileSetEventLinks)
	mux.HandleFunc("POST /files/{id}/public", h.FileSetPublic)
	mux.HandleFunc("POST /files/{id}/rename", h.FileSetDisplayName)
	mux.HandleFunc("POST /files/{id}/category", h.FileSetCategory)

	// Resources page — curated documents/links, public or members-only
	// (internal/web/resources.go).
	mux.HandleFunc("GET /resources", h.ResourcesList)
	mux.HandleFunc("POST /resources", h.ResourceCreate)
	mux.HandleFunc("GET /resources/{id}/download", h.ResourceDownload)
	mux.HandleFunc("POST /resources/{id}/hide-file", h.ResourceHideUnderlyingFile)
	mux.HandleFunc("POST /resources/{id}/delete", h.ResourceDelete)
	mux.HandleFunc("POST /resources/{id}/public", h.ResourceSetPublic)

	// Custom roles — super_admin only (internal/web/admin_roles.go).
	mux.HandleFunc("GET /admin/custom-roles", h.AdminCustomRolesList)
	mux.HandleFunc("POST /admin/custom-roles", h.AdminCustomRolesCreate)
	mux.HandleFunc("POST /admin/custom-roles/{id}", h.AdminCustomRolesUpdate)
	mux.HandleFunc("POST /admin/custom-roles/{id}/delete", h.AdminCustomRolesDelete)
	// Built-in roles live on the same page but a different path: they have
	// no id of their own, only a slug, and they can never be created or
	// deleted — only re-pointed at a different capability set.
	mux.HandleFunc("POST /admin/system-roles/{slug}", h.AdminSystemRoleUpdate)

	// Self-service contact info and the family directory it feeds (internal/web/my_family.go).
	mux.HandleFunc("GET /my-family", h.MyFamily)
	mux.HandleFunc("POST /my-family/members/{id}", h.MyFamilyUpdateMember)
	mux.HandleFunc("POST /my-family/address", h.MyFamilyUpdateAddress)
	mux.HandleFunc("GET /directory", h.FamilyDirectory)
	mux.HandleFunc("GET /directory/export.pdf", h.DirectoryExportPDF)

	// Members-only patrol/den pages (internal/web/groups.go).
	mux.HandleFunc("GET /groups", h.GroupsList)
	mux.HandleFunc("GET /groups/{id}", h.GroupView)
	mux.HandleFunc("GET /admin/groups/{id}", h.AdminGroupEdit)
	mux.HandleFunc("POST /admin/groups/{id}", h.AdminGroupUpdate)
	mux.HandleFunc("POST /admin/groups/{id}/photos", h.AdminGroupSetPhotos)
	mux.HandleFunc("POST /admin/groups/{id}/news", h.AdminGroupCreateNews)
	mux.HandleFunc("POST /admin/groups/{id}/news/{postID}/delete", h.AdminGroupDeleteNews)
}

// baseData is embedded in every page's template data.
type baseData struct {
	Unit                     units.Unit
	LoggedIn                 bool
	CanEditContent           bool // leader roles only — drives the "Edit Homepage" nav link and homepage edit affordances
	CanManageLedger          bool // Treasurer/super_admin in *this* unit — drives the "Treasury" nav link
	CanApproveExpenses       bool // Cubmaster/Scoutmaster/super_admin in *this* unit — drives the "Authorize Spending" nav link, see internal/web/expense_approvals.go
	IsSuperAdmin             bool // super_admin in *this* unit — drives the "Site Settings" nav link (see internal/web/settings_admin.go)
	AdvancementEnabled       bool // this unit's settings.AdvancementEnabled toggle — drives the "Advancement"/"Manage Advancement" nav links, see internal/web/advancement.go
	TreasuryEnabled          bool // this unit's settings.TreasuryEnabled toggle — drives the "My Accounts"/"Treasury"/"Reports" nav links, see internal/web/treasury.go's requireTreasuryEnabled
	NewsletterEnabled        bool // this unit's settings.NewsletterEnabled toggle — drives the "Newsletters" nav link, see internal/web/newsletter.go
	ScoutAccountsSelfService bool // this unit's settings.ScoutAccountSelfService toggle — drives the "My Accounts" nav link and family self-service account access, see internal/web/accounts.go
	PasswordResetEnabled     bool // site-wide settings.PasswordResetEnabled toggle — drives whether the login page's "Forgot your password?" link points to the reset form or an explanation, see internal/web/password_reset.go. Computed for every request (not just logged-in ones), since /login and /forgot-password are reached signed out
	NeedsTwoFactorSetup      bool // this login holds a treasury role, or the require-two-factor-for-all setting is on, and hasn't confirmed TOTP enrollment yet — drives a persistent nudge banner, see base.html
	// IsUnitMember drives which nav links are shown: the unit-scoped ones
	// (roster, directory, patrols/dens, advancement, files) are pointless
	// for a signed-in family from the OTHER unit, who would only get a
	// "this page is for X families" refusal on clicking them. Self-scoped
	// links (My Family, Help, Security) stay under LoggedIn, since they
	// work on either site. See internal/web/unit_membership.go.
	IsUnitMember       bool
	NavSubGroups       []roster.SubGroup // every patrol/den in this unit, for the hamburger nav's Patrols/Dens submenu (see base.html) — named distinctly from any page's own "Groups" field (e.g. internal/web/groups.go's GroupsList) so embedding baseData never shadows a page's own data
	PageHeroImageURL   string            // this request's page hero banner image, if the current path is one of content.HeroPages and a leader has set one — see heroKeyForPath and base.html. Named distinctly from the Home handler's own "HeroImageURL" field (for the homepage's separate, richer hero mechanism) so embedding baseData never lets one shadow the other
	PageHeroSize       string            // content.HeroSize{Short,Medium,Tall} for PageHeroImageURL, already normalized — see base.html's heroSizeClass
	MainWidthClass     string            // overrides <main>'s default max-w-4xl (see base.html) for pages that need extra width — currently just the homepage, whose Recent Activities gallery grid needs the room; empty means "use the default" for every other page
	FooterFacebookURL  string            // site-wide footer's social icons — see h.socialLinks. Named distinctly from the Home handler's own FacebookURL/InstagramURL/TikTokURL fields so embedding baseData never lets one shadow the other
	FooterInstagramURL string
	FooterTikTokURL    string
	FooterYear         int // current year, for the footer's copyright line
	PageTitle          string
	Flash              string
	CSPNonce           string // per-request Content-Security-Policy nonce — every inline <script> in a template must carry it or the browser won't run it (see internal/csp)
	CSRFToken          string // embedded as a hidden field in every <form method="post"> — see internal/csrf
	Version            string // this build's release version — see internal/version, shown in base.html's footer
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

// heroKeyForPath maps a request path to the content.HeroPage.Key whose
// banner (if any) that page should show, or "" if the path isn't one of
// content.HeroPages. Matches both a page's list route and its sub-routes
// (e.g. "/news/123") so an article/detail page still shows the same
// section-wide banner as its listing page.
func heroKeyForPath(path string) string {
	switch {
	case path == "/calendar" || strings.HasPrefix(path, "/calendar/"):
		return "calendar"
	case path == "/news" || strings.HasPrefix(path, "/news/"):
		return "news"
	case path == "/gallery" || strings.HasPrefix(path, "/gallery/"):
		return "gallery"
	case path == "/roster" || strings.HasPrefix(path, "/roster/"):
		return "roster"
	case path == "/directory" || strings.HasPrefix(path, "/directory/"):
		return "directory"
	case path == "/files" || strings.HasPrefix(path, "/files/"):
		return "files"
	case path == "/groups" || strings.HasPrefix(path, "/groups/"):
		return "groups"
	default:
		return ""
	}
}

// socialLinks resolves the three optional social-profile URLs for a unit
// (Facebook/Instagram/TikTok), each gated by its own on/off toggle — see
// internal/settings' SocialFacebookURL/SocialFacebookEnabled and its
// Instagram/TikTok siblings, set from /admin/settings. This is the shared
// logic behind both the site-wide footer (base, below) and the homepage's
// own social icon row (Home), so a leader's /admin/settings choices apply
// identically in both places. A unit that configured one of these through
// the old /admin/home content editor (before this moved to
// /admin/settings) still sees it, via content.LegacySocialURL, until it's
// re-saved through the new form.
func (h *Handlers) socialLinks(ctx context.Context, unitID string) (facebook, instagram, tiktok string, err error) {
	resolve := func(enabledKey, urlKey, legacySlug string) (string, error) {
		enabled, err := settings.GetForUnit(ctx, h.Pool, unitID, enabledKey)
		if err != nil || !enabled {
			return "", err
		}
		v, err := settings.GetUnitText(ctx, h.Pool, unitID, urlKey)
		if err != nil || v != "" {
			return v, err
		}
		return content.LegacySocialURL(ctx, h.Pool, unitID, legacySlug)
	}

	if facebook, err = resolve(settings.SocialFacebookEnabled, settings.SocialFacebookURL, "home-facebook"); err != nil {
		return "", "", "", err
	}
	if instagram, err = resolve(settings.SocialInstagramEnabled, settings.SocialInstagramURL, "home-instagram"); err != nil {
		return "", "", "", err
	}
	if tiktok, err = resolve(settings.SocialTikTokEnabled, settings.SocialTikTokURL, "home-tiktok"); err != nil {
		return "", "", "", err
	}
	return facebook, instagram, tiktok, nil
}

// base assembles the data every page needs: the unit, the logged-in user,
// the nav and footer, and the feature toggles that decide which nav items
// exist at all.
//
// It does that with about a dozen separate round-trips to Postgres — each
// settings.Get/GetForUnit is its own uncached SELECT, plus the social
// links, the page's hero, the sub-groups for the nav, and (for a logged-in
// viewer) the two-factor nudge. A whole page render measures 39 queries
// for a signed-out visitor and around 57 for a signed-in one, and that is
// deliberately left alone: with Postgres in a container beside the app
// they add up to roughly 1.3ms in total, which is far below the cost of
// the caching layer that would replace them.
//
// That trade depends entirely on the database being local. If it ever
// moves off-box — a managed Postgres, a database on another host, anything
// reached over a real network — the arithmetic inverts: those queries
// become that many sequential round-trips, so a 1ms network hop puts a
// 40-60ms floor under every page on the site before a byte of HTML is
// rendered, and a 5ms hop makes it 200-300ms. Nothing about the code
// changes; only the distance to the database does.
//
// If that move ever happens, cache the feature toggles rather than
// unpicking the handlers. They are a handful of booleans per unit that
// change only when a leader edits the settings page, so a small in-process
// cache invalidated on write (settings.Set and its unit equivalent are the
// only writers) removes most of the round-trips without touching any of
// the call sites here.
func (h *Handlers) base(r *http.Request, pageTitle string) baseData {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	data := baseData{Unit: unit, LoggedIn: loggedIn, PageTitle: pageTitle, CSRFToken: csrf.TokenFromContext(r.Context()), CSPNonce: csp.NonceFromContext(r.Context()), Version: version.Version, FooterYear: time.Now().Year()}

	if facebook, instagram, tiktok, err := h.socialLinks(r.Context(), unit.ID); err != nil {
		log.Printf("web: loading social links for footer: %v", err)
	} else {
		data.FooterFacebookURL = facebook
		data.FooterInstagramURL = instagram
		data.FooterTikTokURL = tiktok
	}

	if heroKey := heroKeyForPath(r.URL.Path); heroKey != "" {
		if heroURL, err := content.HeroURLForPage(r.Context(), h.Pool, unit.ID, heroKey); err != nil {
			log.Printf("web: loading page hero banner: %v", err)
		} else {
			data.PageHeroImageURL = heroURL
		}
		if heroSize, err := content.HeroSizeForPage(r.Context(), h.Pool, unit.ID, heroKey); err != nil {
			log.Printf("web: loading page hero banner size: %v", err)
		} else {
			data.PageHeroSize = heroSize
		}
	}

	if enabled, err := settings.Get(r.Context(), h.Pool, settings.PasswordResetEnabled); err != nil {
		log.Printf("web: checking password-reset-enabled setting: %v", err)
	} else {
		data.PasswordResetEnabled = enabled
	}

	if loggedIn {
		caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
		if err != nil {
			log.Printf("web: loading capabilities: %v", err)
		}
		// Any role in this unit is membership; capabilities say what that
		// membership may do. Derived from the roles already loaded above
		// rather than a second query.
		if roles, err := h.rolesFor(r.Context(), user, unit.ID); err != nil {
			log.Printf("web: loading roles for nav: %v", err)
		} else {
			data.IsUnitMember = len(roles) > 0
		}
		data.CanEditContent = units.CanEditUnitContent(caps)
		data.CanManageLedger = units.CanManageLedger(caps)
		data.CanApproveExpenses = units.CanApproveExpenses(caps)
		data.IsSuperAdmin = units.IsSuperAdmin(caps)

		if enabled, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, settings.AdvancementEnabled); err != nil {
			log.Printf("web: checking advancement-enabled setting: %v", err)
		} else {
			data.AdvancementEnabled = enabled
		}

		if enabled, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, settings.TreasuryEnabled); err != nil {
			log.Printf("web: checking treasury-enabled setting: %v", err)
		} else {
			data.TreasuryEnabled = enabled
		}

		if enabled, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, settings.NewsletterEnabled); err != nil {
			log.Printf("web: checking newsletter-enabled setting: %v", err)
		} else {
			data.NewsletterEnabled = enabled
		}

		if enabled, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, settings.ScoutAccountSelfService); err != nil {
			log.Printf("web: checking scout-account-self-service setting: %v", err)
		} else {
			// The "My Accounts" nav link shows when families may see their
			// own balances, or always for a treasurer (who reaches accounts
			// through the treasury area regardless of the family-facing toggle).
			data.ScoutAccountsSelfService = enabled
		}

		if groups, err := roster.SubGroupsForUnit(r.Context(), h.Pool, unit.ID); err != nil {
			log.Printf("web: loading sub-groups for nav: %v", err)
		} else {
			data.NavSubGroups = groups
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

// homeActivity is one recent, published, public Photo Album shown in the
// homepage's left-column preview — see maxHomeActivities below.
type homeActivity struct {
	Title  string
	URL    string
	Photos []content.GalleryPhoto
}

// homeNewsItem is one news post as the homepage shows it: title, link,
// date, and a short plain-text lead-in pulled out of the post body.
type homeNewsItem struct {
	Title       string
	URL         string
	DateDisplay string
	Excerpt     string
	MembersOnly bool
}

// maxHomeActivities caps how many Photo Album posts the homepage
// previews at once — ListPublishedPublicForUnit returns every published
// public one, but the homepage only has room for a couple rows before it
// should send visitors to the full /gallery listing instead. Set to fill
// exactly two rows of the Recent Activities grid (see home.html): 2
// columns on desktop (sm: and up) × 2 rows = 4, 1 column on mobile × 2
// rows shown = 2 (mobile hides the 3rd/4th via home.html's own
// responsive classes, rather than fetching a different count per
// viewport). When there are more eligible galleries than this, which
// ones show is picked at random on every page load (see
// randomHomeActivities) instead of freezing on whichever are newest, so
// a unit with a deep gallery history isn't stuck always featuring the
// same handful.
const maxHomeActivities = 4

// maxHomeNews caps the homepage's news list. Three is a headline block,
// not a feed — the full list is one click away at /news, and the point of
// the homepage section is "something happened recently", not archive
// browsing.
const maxHomeNews = 3

// randomHomeActivities picks up to max activities at random out of all —
// see maxHomeActivities — so a visitor sees a different rotation on each
// page load once a unit has published more galleries than fit on the
// homepage. Selected activities keep their original (newest-first)
// relative order; if there are max or fewer already, all of them are
// returned unchanged (no randomization to do).
func randomHomeActivities(all []homeActivity, max int) []homeActivity {
	if len(all) <= max {
		return all
	}
	indices := rand.Perm(len(all))[:max]
	sort.Ints(indices)
	picked := make([]homeActivity, len(indices))
	for i, idx := range indices {
		picked[i] = all[idx]
	}
	return picked
}

// maxHomeEvents caps how many upcoming events the homepage's "Upcoming
// Events" list shows — ListUpcomingPublicForUnit returns every one from
// today forward, but the homepage is a preview, not the full calendar;
// send visitors to /calendar itself for anything beyond the next few.
const maxHomeEvents = 5

func (h *Handlers) Home(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	_, loggedIn := auth.UserFromContext(r.Context())
	events, err := calendar.ListUpcomingPublicForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: listing public events: %v", err)
	}
	if len(events) > maxHomeEvents {
		events = events[:maxHomeEvents]
	}

	saved, err := content.SectionsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading homepage content: %v", err)
	}

	// Fail closed: if this can't be read, the homepage shows no join
	// link rather than one that might 404.
	joinFormOpen, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, settings.ProspectFormEnabled)
	if err != nil {
		log.Printf("web: loading prospect-form-enabled setting: %v", err)
		joinFormOpen = false
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

	// The left-column preview shows recent Photo Album activities, newest
	// first, each with its own auto-rotating carousel — replacing the old
	// single leader-curated home-gallery strip with something that updates
	// itself as new albums get published.
	galleryPosts, err := content.ListPublishedPublicForUnit(r.Context(), h.Pool, unit.ID, "gallery")
	if err != nil {
		log.Printf("web: listing public photo albums: %v", err)
	}
	var activities []homeActivity
	for _, p := range galleryPosts {
		photos := h.filterViewableGalleryPhotos(r.Context(), unit.ID, content.ParseGalleryPhotos(p.Body), loggedIn)
		if len(photos) == 0 {
			continue
		}
		activities = append(activities, homeActivity{Title: p.Title, URL: "/gallery/" + p.ID, Photos: photos})
	}
	activities = randomHomeActivities(activities, maxHomeActivities)

	// News for the homepage block. Which list is used is the whole
	// privacy question here: a signed-out visitor gets only posts
	// published as public, while a signed-in one also sees members-only
	// posts. Two different queries rather than one query plus filtering
	// in Go, so a members-only post can never reach an anonymous
	// response even if the rendering below changes later.
	var newsPosts []content.Post
	if loggedIn {
		newsPosts, err = content.ListPublishedForUnit(r.Context(), h.Pool, unit.ID, "post")
	} else {
		newsPosts, err = content.ListPublishedPublicForUnit(r.Context(), h.Pool, unit.ID, "post")
	}
	if err != nil {
		log.Printf("web: listing news for homepage: %v", err)
	}
	if len(newsPosts) > maxHomeNews {
		newsPosts = newsPosts[:maxHomeNews]
	}
	news := make([]homeNewsItem, 0, len(newsPosts))
	for _, p := range newsPosts {
		news = append(news, homeNewsItem{
			Title:       p.Title,
			URL:         "/news/" + p.ID,
			DateDisplay: postedOn(p.CreatedAt),
			Excerpt:     excerpt(p.Body, 160),
			MembersOnly: p.Visibility != "public",
		})
	}

	// The storefront button never shows while Treasury itself is off for
	// this unit — see settings.TreasuryEnabled — since it exists to feed
	// the same ledger that toggle gates.
	var storefront ledger.Fundraiser
	var storefrontActive bool
	if treasuryEnabled, err := settings.GetForUnit(r.Context(), h.Pool, unit.ID, settings.TreasuryEnabled); err != nil {
		log.Printf("web: checking treasury-enabled setting for homepage: %v", err)
	} else if treasuryEnabled {
		storefront, storefrontActive, err = ledger.ActiveStorefrontFundraiser(r.Context(), h.Pool, unit.ID)
		if err != nil {
			log.Printf("web: loading active storefront fundraiser for homepage: %v", err)
		}
	}

	heroSize, err := content.HeroSizeForSlug(r.Context(), h.Pool, unit.ID, "home-hero-image")
	if err != nil {
		log.Printf("web: loading homepage hero size: %v", err)
	}

	// Wider than every other page's default max-w-4xl: Recent Activities'
	// grid (up to 4 gallery previews, 2 per row) needs the extra room.
	bd := h.base(r, "")
	bd.MainWidthClass = "max-w-6xl"

	data := struct {
		baseData
		Events              []calendar.Event
		News                []homeNewsItem
		Activities          []homeActivity
		Hero                string
		HeroImageURL        string
		HeroSize            string
		ProgramItems        []string
		ProgramImageURL     string
		Meeting             string
		Leadership          string
		SocialURL           string
		StorefrontActive    bool
		StorefrontName      string
		StorefrontButtonURL string
		// JoinFormOpen gates the homepage's "interested in joining"
		// call-to-action on the same setting /join itself checks
		// (settings.ProspectFormEnabled). Linking to the form while the
		// unit has it switched off would send a prospective family to a
		// "not found" page, which is a worse first impression than no
		// link at all.
		JoinFormOpen bool
	}{
		baseData:            bd,
		Events:              events,
		News:                news,
		Activities:          activities,
		Hero:                text["home-hero"],
		HeroImageURL:        text["home-hero-image"],
		HeroSize:            heroSize,
		ProgramItems:        programItems,
		ProgramImageURL:     text["home-program-image"],
		Meeting:             text["home-meeting"],
		Leadership:          text["home-leadership"],
		SocialURL:           text["home-social"],
		StorefrontActive:    storefrontActive,
		StorefrontName:      storefront.Name,
		StorefrontButtonURL: storefront.ButtonImageURL,
		JoinFormOpen:        joinFormOpen,
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
	Saved       bool   // false if a leader hasn't ever saved this section — the placeholder is what's showing live
	IsHeroImage bool   // true for the homepage hero photo and every page hero banner — these get a Size selector alongside the URL field (see content.HeroSizeSlug); "Our Program"'s photo is "image"-kind too but isn't a hero, so stays plain
	Size        string // content.HeroSize{Short,Medium,Tall}, already normalized; only meaningful when IsHeroImage
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
		if def.Slug == "home-hero-image" {
			row.IsHeroImage = true
			if size, err := content.HeroSizeForSlug(r.Context(), h.Pool, unit.ID, def.Slug); err != nil {
				log.Printf("web: loading homepage hero size: %v", err)
			} else {
				row.Size = size
			}
		}
		rows = append(rows, row)
	}

	savedHero, err := content.HeroSectionsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading page hero banners: %v", err)
	}
	heroDefs := content.HeroSections()
	heroRows := make([]homeAdminRow, 0, len(heroDefs))
	for _, def := range heroDefs {
		row := homeAdminRow{Slug: def.Slug, Label: def.Label, Placeholder: def.Placeholder, Help: def.Help, Kind: def.Kind, IsHeroImage: true}
		if s, ok := savedHero[def.Slug]; ok {
			row.Body = s.Body
			row.Saved = true
		}
		if size, err := content.HeroSizeForSlug(r.Context(), h.Pool, unit.ID, def.Slug); err != nil {
			log.Printf("web: loading page hero banner size for %s: %v", def.Slug, err)
		} else {
			row.Size = size
		}
		heroRows = append(heroRows, row)
	}

	// Two separate lists: the single-photo hero/banner slots only ever
	// take a plain <img>, so their picker stays image-only; the "images"
	// kind (gallery-strip sections) renders through the same
	// content.GalleryPhoto/photoCarousel machinery a Gallery album does,
	// so it can offer video too.
	publicImageGroups, publicImagesUngrouped, err := files.ListImageFilesGroupedByEvent(r.Context(), h.Pool, unit.ID, true)
	if err != nil {
		log.Printf("web: loading public images: %v", err)
	}
	publicMediaGroups, publicMediaUngrouped, err := files.ListMediaFilesGroupedByEvent(r.Context(), h.Pool, unit.ID, true)
	if err != nil {
		log.Printf("web: loading public media: %v", err)
	}

	data := struct {
		baseData
		Sections              []homeAdminRow
		HeroSections          []homeAdminRow
		PublicImageGroups     []files.EventFileGroup
		PublicImagesUngrouped []files.File
		PublicMediaGroups     []files.EventFileGroup
		PublicMediaUngrouped  []files.File
	}{
		baseData:              h.base(r, "Edit Homepage"),
		Sections:              rows,
		HeroSections:          heroRows,
		PublicImageGroups:     publicImageGroups,
		PublicImagesUngrouped: publicImagesUngrouped,
		PublicMediaGroups:     publicMediaGroups,
		PublicMediaUngrouped:  publicMediaUngrouped,
	}
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
	var isHeroImage bool
	valid := false
	for _, def := range content.HomepageSections(unit.UnitType) {
		if def.Slug == slug {
			valid = true
			label = def.Label
			isHeroImage = slug == "home-hero-image"
			break
		}
	}
	if !valid {
		for _, def := range content.HeroSections() {
			if def.Slug == slug {
				valid = true
				label = def.Label
				isHeroImage = true
				break
			}
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

	// A hero image's size preset saves alongside its URL, as a sibling
	// content_pages row — see content.HeroSizeSlug.
	if isHeroImage {
		size := content.NormalizeHeroSize(r.FormValue("size"))
		if _, err := content.UpsertSection(r.Context(), h.Pool, unit.ID, content.HeroSizeSlug(slug), label+" size", size, actor.ID); err != nil {
			log.Printf("web: saving hero size for %s: %v", slug, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
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
// sanitizeNextPath decides whether a ?next= value is a safe place to send
// somebody after login, and falls back to the homepage when it isn't.
//
// The value arrives in a URL anyone can craft and is followed immediately
// after a password is typed, which makes an off-site redirect here a
// credential-phishing step rather than a curiosity: land on the real
// login page, sign in, get bounced to a copy that asks you to "confirm"
// your password.
//
// Rejecting "//host" alone is not enough. Browsers normalise a backslash
// to a forward slash while resolving a URL, so "/\evil.com" is fetched as
// "//evil.com" — a protocol-relative URL to another origin — even though
// it passes a naive first-character check. Rather than enumerate the
// tricks, this parses the value and insists it names no scheme and no
// host, so anything that could leave this site is refused whatever
// spelling it arrives in.
func sanitizeNextPath(next string) string {
	if next == "" || next[0] != '/' {
		return "/"
	}
	// A backslash never legitimately appears in a path this app generates,
	// and browsers treat it as a slash — so refuse it outright rather than
	// reasoning about where it might end up.
	if strings.ContainsAny(next, `\`) {
		return "/"
	}
	// Control characters (including a stray newline or tab) can be dropped
	// or reinterpreted by a browser mid-parse, changing what the rest of
	// the string means.
	for _, r := range next {
		if r < 0x20 || r == 0x7f {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" {
		return "/"
	}
	// url.Parse leaves "//host/path" with an empty Scheme but a non-empty
	// Host, so the check above catches it; this is belt-and-braces for a
	// value that somehow parses with neither.
	if strings.HasPrefix(next, "//") {
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

	// Per-address ceiling, alongside internal/auth's per-account lockout.
	// Checked before the password is looked at, so a blocked address costs
	// nothing — no user lookup, no bcrypt.
	ip := clientIP(r, h.TrustProxyHeaders)
	if h.loginLimiter.Blocked(ip) {
		data := struct {
			baseData
			Next string
		}{baseData: h.base(r, "Log in"), Next: next}
		data.Flash = "Too many failed sign-in attempts from this connection. Please wait a few minutes and try again."
		h.render(w, h.login, data)
		return
	}

	user, err := auth.Authenticate(r.Context(), h.Pool, email, password)
	if err != nil {
		// Only failures are counted. A correct password costs nothing,
		// which is what keeps a whole troop on one meeting-hall router
		// from locking itself out.
		h.loginLimiter.Allow(ip)

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

	// A login whose current password is still a leader-issued temporary
	// one (users.must_change_password — see internal/roster's
	// CreateFamilyWithMember/CreateMemberLogin/ResetFamilyPassword/
	// ResetMemberLoginPassword) must replace it before going any further —
	// checked before the two-factor step below, so a temporary credential
	// can't be used to reach a real session (or even the TOTP prompt)
	// without first being changed.
	if user.MustChangePassword {
		pendingToken, pendingExpiresAt, err := auth.CreatePendingPasswordChange(r.Context(), h.Pool, user.ID, next)
		if err != nil {
			log.Printf("web: creating pending password change: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		auth.SetPendingPasswordChangeCookie(w, pendingToken, pendingExpiresAt, h.CookieDomain, h.SecureCookie)
		http.Redirect(w, r, "/login/change-password", http.StatusSeeOther)
		return
	}

	h.completeLogin(w, r, user.ID, next)
}

// completeLogin runs what happens once a login has fully authenticated —
// password correct, and any forced temporary-password change already done
// (see MustChangePassword above) — deciding whether a confirmed TOTP
// enrollment needs a second factor before a real session is issued, or
// whether to just issue one directly. Shared by LoginSubmit and
// ChangePasswordSubmit so the "temporary password → forced change →
// [TOTP] → session" chain has one place that decides the TOTP branch.
//
// Phase 2: a login with confirmed TOTP enrollment needs a second factor
// before a real session gets created — see internal/auth/totp.go. This
// applies to ANY confirmed enrollment, not just Treasurer/super_admin
// logins: two-factor is available to every user as an opt-in security
// measure (see /settings/2fa, reachable from every logged-in page's nav),
// and once someone has actually gone through enrollment, logging in should
// ask for their code regardless of why they set it up. What varies by
// role (and by the site-wide "require two-factor for everyone" setting)
// is only whether a login that HASN'T enrolled yet gets nudged to — see
// baseData.NeedsTwoFactorSetup in h.base, which never blocks a login
// outright, only this actually-enrolled check does.
func (h *Handlers) completeLogin(w http.ResponseWriter, r *http.Request, userID, next string) {
	_, confirmed, err := auth.TOTPStatus(r.Context(), h.Pool, userID)
	if err != nil {
		log.Printf("web: checking two-factor status: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if confirmed {
		pendingToken, pendingExpiresAt, err := auth.CreatePendingTwoFactorLogin(r.Context(), h.Pool, userID, next)
		if err != nil {
			log.Printf("web: creating pending two-factor login: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		auth.SetPendingTwoFactorCookie(w, pendingToken, pendingExpiresAt, h.CookieDomain, h.SecureCookie)
		http.Redirect(w, r, "/login/2fa", http.StatusSeeOther)
		return
	}

	token, expiresAt, err := auth.CreateSession(r.Context(), h.Pool, userID)
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

// rosterContactDisplay joins whichever of a roster entry's Email/HomePhone/
// CellPhone fields are actually set (i.e. released — see
// family.RosterEntry's doc comment) into one "· "-separated string for a
// single-line/single-cell display, used by both roster.html and the
// roster PDF export so the two stay in sync.
func rosterContactDisplay(e family.RosterEntry) string {
	var parts []string
	if e.Email != "" {
		parts = append(parts, e.Email)
	}
	if e.HomePhone != "" {
		parts = append(parts, "home "+e.HomePhone)
	}
	if e.CellPhone != "" {
		parts = append(parts, "cell "+e.CellPhone)
	}
	return strings.Join(parts, " · ")
}

func (h *Handlers) Roster(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if _, _, ok := h.requireUnitMember(w, r, "/roster"); !ok {
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
	}{baseData: h.base(r, "Roster"), Roster: h.labelRoster(r.Context(), unit.ID, roster)}
	h.render(w, h.roster, data)
}

// RosterExportPDF is Roster's printable sibling — same data, same "must
// be logged in" gate, rendered as a downloadable PDF.
func (h *Handlers) RosterExportPDF(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	if _, _, ok := h.requireUnitMember(w, r, "/roster"); !ok {
		return
	}

	roster, err := family.RosterForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading roster: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	roster = h.labelRoster(r.Context(), unit.ID, roster)

	rows := make([][]string, 0, len(roster))
	for _, e := range roster {
		subGroup := e.SubGroupName
		if subGroup == "" {
			subGroup = "—"
		}
		contact := rosterContactDisplay(e)
		if contact == "" {
			contact = "—"
		}
		address := e.Address
		if address == "" {
			address = "—"
		}
		rows = append(rows, []string{e.FirstName + " " + e.LastName, e.MemberType, subGroup, strings.Join(e.RoleLabels, ", "), contact, address})
	}

	data, err := simpleTablePDF(unit.Name+" — Roster", "",
		[]string{"Name", "Type", "Den/Patrol", "Roles", "Contact", "Address"},
		[]float64{40, 14, 22, 28, 38, 38}, []string{"L", "L", "L", "L", "L", "L"}, rows)
	if err != nil {
		log.Printf("web: rendering roster PDF: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writePDF(w, "roster.pdf", data)
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
	Files           []files.File
	SubGroupName    string // "" if unscoped — see calendar.Event.SubGroupID
	SeriesCount     int    // >1 if this event is one occurrence of a repeating series — see calendar.CountInSeries
	StartsAtInput   string // e.StartsAt formatted for an <input type="datetime-local">'s value — see calendar.go's datetimeLocalFormat
	EndsAtInput     string // "" if EndsAt is nil
	SubGroupIDValue string // e.SubGroupID dereferenced ("" if unscoped) — Go templates' eq can't compare a *string to a string, so the edit form's <select> needs this plain-string form to mark the current selection
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
	// Members-only events belong to the families of THIS unit. Being
	// signed in is not enough, because one login legitimately spans both
	// units — see internal/web/unit_membership.go. A signed-in visitor
	// from the other unit sees the same public calendar a stranger does.
	isMember := h.viewerIsUnitMember(r)

	// Unauthenticated visitors must only ever see public events — members-only
	// events (the default visibility for anything not explicitly marked
	// public) are for logged-in families only.
	var events []calendar.Event
	var err error
	if isMember {
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
	if isMember {
		monthEvents, err = calendar.ListForRangeForUnit(r.Context(), h.Pool, unit.ID, rangeStart, rangeEnd)
	} else {
		monthEvents, err = calendar.ListForRangePublicForUnit(r.Context(), h.Pool, unit.ID, rangeStart, rangeEnd)
	}
	if err != nil {
		log.Printf("web: listing events for month grid: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var canCreate, requiresApproval, canApprove, canManageFiles, canEditContent bool
	var pendingViews []pendingApprovalView
	var creatableSubGroups []roster.SubGroup

	if loggedIn {
		caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
		if err != nil {
			log.Printf("web: loading capabilities: %v", err)
		}
		canEditContent = units.CanEditUnitContent(caps)
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

	const datetimeLocalFormat = "2006-01-02T15:04"
	eventViews := make([]eventView, len(events))
	for i, e := range events {
		var subGroupName string
		if e.SubGroupID != nil {
			subGroupName = subGroupNameByID[*e.SubGroupID]
		}
		v := eventView{Event: e, Files: filesByEvent[e.ID], SubGroupName: subGroupName}
		if e.SubGroupID != nil {
			v.SubGroupIDValue = *e.SubGroupID
		}
		if canEditContent {
			v.StartsAtInput = e.StartsAt.Format(datetimeLocalFormat)
			if e.EndsAt != nil {
				v.EndsAtInput = e.EndsAt.Format(datetimeLocalFormat)
			}
			if e.SeriesID != nil {
				if n, err := calendar.CountInSeries(r.Context(), h.Pool, *e.SeriesID); err != nil {
					log.Printf("web: counting event series: %v", err)
				} else {
					v.SeriesCount = n
				}
			}
		}
		eventViews[i] = v
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
		AllSubGroups       []roster.SubGroup
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
		AllSubGroups:       subGroups,
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

	// "Repeats" creates several independent event rows up front (each with
	// its own RSVPs, approval routing, and later edit/delete — see
	// calendar.Update/Delete) rather than one row expanded at read time, so
	// every occurrence behaves exactly like an event created by hand. count
	// is clamped to maxRepeatOccurrences so a typo (or a hostile client)
	// can't be asked to insert an unbounded number of rows in one request.
	count := 1
	frequency := r.FormValue("repeat_frequency")
	if frequency != "" {
		if n, err := strconv.Atoi(r.FormValue("repeat_count")); err == nil && n > 1 {
			count = n
		} else {
			count = 2
		}
		if count > maxRepeatOccurrences {
			count = maxRepeatOccurrences
		}
	}

	var seriesID *string
	if count > 1 {
		id := uuid.NewString()
		seriesID = &id
	}

	for i := 0; i < count; i++ {
		occStart := addRepeatInterval(startsAt, frequency, i)
		var occEnd *time.Time
		if endsAt != nil {
			e := addRepeatInterval(*endsAt, frequency, i)
			occEnd = &e
		}
		_, err = calendar.Create(r.Context(), h.Pool, calendar.CreateInput{
			UnitID:      unit.ID,
			Title:       r.FormValue("title"),
			SubGroupID:  subGroupID,
			Description: r.FormValue("description"),
			Location:    r.FormValue("location"),
			StartsAt:    occStart,
			EndsAt:      occEnd,
			Visibility:  visibility,
			CreatedBy:   actor.ID,
			SeriesID:    seriesID,
		}, units.CanEditUnitContent(caps))
		if err != nil {
			log.Printf("web: creating event: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

// maxRepeatOccurrences caps how many occurrences one "repeats" submission
// may create at once — about a year of weekly meetings, generous for any
// real unit schedule while keeping a single request's work bounded.
const maxRepeatOccurrences = 52

// addRepeatInterval returns t shifted forward by n repetitions of
// frequency ("weekly", "biweekly", or "monthly"; any other value,
// including "", is treated as no shift). n=0 always returns t unchanged —
// the first occurrence in a series is never shifted.
func addRepeatInterval(t time.Time, frequency string, n int) time.Time {
	switch frequency {
	case "weekly":
		return t.AddDate(0, 0, 7*n)
	case "biweekly":
		return t.AddDate(0, 0, 14*n)
	case "monthly":
		return t.AddDate(0, n, 0)
	default:
		return t
	}
}

// CalendarUpdate edits an existing event's details. Restricted to
// CanEditUnitContent — the same broad-leader gate as publishing an event
// directly — rather than also opening it to a scoped submitter (SPL/Patrol
// Leader): editing doesn't re-run approval routing, so letting a
// not-yet-approved event's own submitter silently change it after
// submission would be a way around review, not a convenience.
func (h *Handlers) CalendarUpdate(w http.ResponseWriter, r *http.Request) {
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
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to edit events", http.StatusForbidden)
		return
	}

	eventID := r.PathValue("id")
	if _, found, err := calendar.GetEvent(r.Context(), h.Pool, eventID, unit.ID); err != nil {
		log.Printf("web: loading event: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}

	startsAt, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("starts_at"), time.Local)
	if err != nil {
		http.Error(w, "invalid start time", http.StatusBadRequest)
		return
	}
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
		subGroupID = &raw
		visibility = "members"
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return
	}

	if _, err := calendar.Update(r.Context(), h.Pool, eventID, unit.ID, calendar.UpdateInput{
		Title:       r.FormValue("title"),
		Description: r.FormValue("description"),
		Location:    r.FormValue("location"),
		StartsAt:    startsAt,
		EndsAt:      endsAt,
		Visibility:  visibility,
		SubGroupID:  subGroupID,
	}, actor.ID); err != nil {
		log.Printf("web: updating event: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

// CalendarDelete removes an event outright. Same CanEditUnitContent gate
// as CalendarUpdate.
func (h *Handlers) CalendarDelete(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to delete events", http.StatusForbidden)
		return
	}

	actor, err := h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return
	}

	eventID := r.PathValue("id")
	if err := calendar.Delete(r.Context(), h.Pool, eventID, unit.ID, actor.ID); err != nil {
		if errors.Is(err, calendar.ErrEventNotFound) {
			http.NotFound(w, r)
			return
		}
		log.Printf("web: deleting event: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

func (h *Handlers) CalendarRSVP(w http.ResponseWriter, r *http.Request) {
	// RSVPing is acting on this unit's event, so it needs membership here
	// and not merely a session — a family in the other unit has no
	// business adding themselves to this unit's attendee list.
	unit, user, ok := h.requireUnitMember(w, r, "/calendar")
	if !ok {
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

// CalendarExportPDF prints a date-ranged, optionally den/patrol-narrowed
// list of events. Same visibility rules as the on-screen calendar — an
// unauthenticated visitor only ever gets public events, and a logged-in
// viewer gets everything calendar.FilterVisibleToViewer would already
// show them on-screen (their own den/patrol's scoped events plus every
// whole-unit one, or everything if they can edit unit content) — printing
// can never surface an event the same viewer couldn't already see in the
// browser.
func (h *Handlers) CalendarExportPDF(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	// Same rule as the on-screen calendar: the printable copy must not
	// carry members-only events to a family from the other unit.
	isMember := h.viewerIsUnitMember(r)

	from, to := parseDateRangeParam(r)

	var events []calendar.Event
	var err error
	if isMember {
		events, err = calendar.ListForRangeForUnit(r.Context(), h.Pool, unit.ID, from, to)
	} else {
		events, err = calendar.ListForRangePublicForUnit(r.Context(), h.Pool, unit.ID, from, to)
	}
	if err != nil {
		log.Printf("web: loading events for calendar PDF: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if loggedIn {
		caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
		if err != nil {
			log.Printf("web: loading capabilities: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		canEditContent := units.CanEditUnitContent(caps)

		var viewerSubGroupIDs []string
		if user.MemberID != nil {
			viewerSubGroupIDs, err = roster.SubGroupIDsForMember(r.Context(), h.Pool, *user.MemberID, unit.ID)
		} else {
			viewerSubGroupIDs, err = roster.SubGroupIDsForFamily(r.Context(), h.Pool, user.FamilyID, unit.ID)
		}
		if err != nil {
			log.Printf("web: loading viewer's sub-group membership: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		viewerSubGroups := make(map[string]bool, len(viewerSubGroupIDs))
		for _, id := range viewerSubGroupIDs {
			viewerSubGroups[id] = true
		}
		events = calendar.FilterVisibleToViewer(events, viewerSubGroups, canEditContent)
	}

	// Narrow to specific den(s)/patrol(s) if requested — a whole-unit event
	// (SubGroupID nil) always stays in regardless, same as the on-screen
	// month grid showing it to everyone irrespective of den/patrol.
	if ids := r.URL.Query()["sub_group_id"]; len(ids) > 0 {
		wanted := make(map[string]bool, len(ids))
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" {
				wanted[id] = true
			}
		}
		filtered := events[:0]
		for _, e := range events {
			if e.SubGroupID == nil || wanted[*e.SubGroupID] {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}

	subGroups, err := roster.SubGroupsForUnit(r.Context(), h.Pool, unit.ID)
	if err != nil {
		log.Printf("web: loading sub-groups: %v", err)
	}
	subGroupNameByID := make(map[string]string, len(subGroups))
	for _, g := range subGroups {
		subGroupNameByID[g.ID] = g.Name
	}

	pdfEvents := make([]calendarPDFEvent, 0, len(events))
	for _, e := range events {
		var subGroup string
		if e.SubGroupID != nil {
			subGroup = subGroupNameByID[*e.SubGroupID]
		}
		pdfEvents = append(pdfEvents, calendarPDFEvent{
			DateRange: e.DateRangeDisplay(),
			Title:     e.Title,
			Location:  e.Location,
			SubGroup:  subGroup,
		})
	}

	data, err := calendarEventsPDF(unit.Name+" — Calendar", dateRangeLabel(from, to), pdfEvents)
	if err != nil {
		log.Printf("web: rendering calendar PDF: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writePDF(w, "calendar.pdf", data)
}

// CalendarEventAttendeesExportPDF prints who's attending one event — the
// first place RSVP identity is ever shown to anyone but the RSVP'ing
// family itself, so it's gated to unit-content editors rather than the
// calendar's usual read-your-own-den visibility.
func (h *Handlers) CalendarEventAttendeesExportPDF(w http.ResponseWriter, r *http.Request) {
	unit, _ := units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to print this event's attendee roster", http.StatusForbidden)
		return
	}

	eventID := r.PathValue("id")
	event, found, err := calendar.GetEvent(r.Context(), h.Pool, eventID, unit.ID)
	if err != nil {
		log.Printf("web: loading event for attendee PDF: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	attendees, err := calendar.AttendeesForEvent(r.Context(), h.Pool, eventID)
	if err != nil {
		log.Printf("web: loading attendees: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	responseLabels := map[string]string{"yes": "Yes", "maybe": "Maybe", "no": "No"}
	rows := make([][]string, 0, len(attendees))
	for _, a := range attendees {
		rows = append(rows, []string{a.FirstName + " " + a.LastName, a.MemberType, a.FamilyName, responseLabels[a.Response]})
	}

	data, err := simpleTablePDF(unit.Name+" — "+event.Title+" — Attendee Roster", event.DateRangeDisplay(),
		[]string{"Name", "Type", "Family", "RSVP"},
		[]float64{55, 25, 55, 25}, []string{"L", "L", "L", "L"}, rows)
	if err != nil {
		log.Printf("web: rendering attendee roster PDF: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writePDF(w, "attendees.pdf", data)
}

// --- Audit — see internal/web/audit.go for AuditView/AuditExport -----------
