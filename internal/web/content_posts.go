package web

// This file is the news/announcements feed and photo galleries —
// scout-website-requirements.md Section 3.2 ("Communication": announcements/
// news feed, photo galleries), built on internal/content/posts.go, which
// in turn reuses the content_pages table (and its 'post'/'gallery'
// page_type values) content.go already established for homepage
// sections. Public routes (/news, /gallery, and their /{id} detail pages)
// are readable by anyone, filtered to what that visitor is allowed to
// see (see content.ListPublishedPublicForUnit vs ListPublishedForUnit);
// the /admin/news and /admin/gallery routes are gated the same way
// /admin/home already is — units.CanEditUnitContent, i.e. any adult
// leader role, not just super_admin.

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/content"
	"github.com/47-yonkers/scout-site/internal/family"
	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/units"
)

// galleryFileURLPattern matches this app's own file-download links (as
// opposed to an externally hosted photo URL) so filterViewableGalleryPhotos
// can look up whether the file behind one is public.
var galleryFileURLPattern = regexp.MustCompile(`^/files/([^/?#]+)/download$`)

// filterViewableGalleryPhotos drops any gallery photo/video that points at
// this unit's own file library and isn't marked public, whenever the
// current viewer isn't logged in — the same access FileDownload itself
// enforces (see requiresLoginToDownload), just applied before the page
// renders instead of after a blocked image request leaves a blank/broken
// tile in its place. A photo hosted elsewhere (not one of our own
// /files/{id}/download links) is always kept — this app has no access
// check to apply to it either way. Fails closed: if the visibility lookup
// itself errors, every one of this unit's own file links is dropped
// rather than risk showing a private photo.
func (h *Handlers) filterViewableGalleryPhotos(ctx context.Context, unitID string, photos []content.GalleryPhoto, loggedIn bool) []content.GalleryPhoto {
	if loggedIn || len(photos) == 0 {
		return photos
	}

	ids := galleryFileIDs(photos)
	if len(ids) == 0 {
		return photos
	}

	publicIDs, err := files.PublicFileIDSet(ctx, h.Pool, unitID, ids)
	if err != nil {
		log.Printf("web: checking gallery photo visibility: %v", err)
		publicIDs = map[string]bool{}
	}
	return keepViewableGalleryPhotos(photos, publicIDs)
}

// galleryFileIDs pulls out the file IDs of every photo/video in photos
// that points at this app's own file library, ignoring externally hosted
// URLs (which have no such ID to look up).
func galleryFileIDs(photos []content.GalleryPhoto) []string {
	var ids []string
	for _, p := range photos {
		if m := galleryFileURLPattern.FindStringSubmatch(p.URL); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// keepViewableGalleryPhotos is filterViewableGalleryPhotos' pure part,
// pulled out so the actual filtering logic is unit-testable without a
// real database — the same pattern requiresLoginToDownload (files.go)
// uses for FileDownload's access check.
func keepViewableGalleryPhotos(photos []content.GalleryPhoto, publicIDs map[string]bool) []content.GalleryPhoto {
	visible := make([]content.GalleryPhoto, 0, len(photos))
	for _, p := range photos {
		if m := galleryFileURLPattern.FindStringSubmatch(p.URL); m != nil && !publicIDs[m[1]] {
			continue
		}
		visible = append(visible, p)
	}
	return visible
}

// contentKind describes the fixed differences between "post" (news) and
// "gallery" handling — everything else is shared by the parameterized
// handlers below. Keeping this table-driven, rather than writing near-
// duplicate handlers for each type, is what keeps adding a third content
// type later (if it ever comes up) a data change, not a new set of
// handlers.
type contentKind struct {
	PageType        string // content_pages.page_type value
	Label           string // singular, for page titles/buttons — "News Post" / "Photo Album"
	LabelPlural     string
	BasePath        string // admin base path — "/admin/news" / "/admin/gallery"
	PublicPath      string // public base path — "/news" / "/gallery"
	BodyLabel       string
	BodyHelp        string
	BodyPlaceholder string
	HasImagePicker  bool // true for galleryKind — offers the "choose from library" thumbnail strip alongside the body textarea (see admin-content-form.html)
}

var (
	newsKind = contentKind{
		PageType: "post", Label: "News Post", LabelPlural: "News",
		BasePath: "/admin/news", PublicPath: "/news",
		BodyLabel:       "Announcement",
		BodyHelp:        "Plain text — line breaks are preserved, but no HTML.",
		BodyPlaceholder: "What's the news?",
	}
	// Label/LabelPlural are "Photo Album"/"Photos" rather than the
	// PageType/BasePath's own "gallery" — the public-facing name is
	// "Photos" (see base.html's nav), but the underlying route/DB
	// page_type stays "gallery" to avoid a URL migration for something
	// that's purely a display-name change.
	galleryKind = contentKind{
		PageType: "gallery", Label: "Photo Album", LabelPlural: "Photos",
		BasePath: "/admin/gallery", PublicPath: "/gallery",
		BodyLabel:       "Photos",
		BodyHelp:        "One photo per line: paste a link to an image hosted elsewhere (e.g. a photo shared publicly from Google Photos/Drive), or click one from your library below — either way, optionally followed by | and a caption, e.g. https://example.com/photo.jpg | Sam at the summit.",
		BodyPlaceholder: "https://example.com/photo1.jpg | Setting up camp\nhttps://example.com/photo2.jpg",
		HasImagePicker:  true,
	}
)

// --- Public: news -----------------------------------------------------------

func (h *Handlers) NewsList(w http.ResponseWriter, r *http.Request) {
	h.publicContentList(w, r, newsKind, h.newsList)
}

func (h *Handlers) NewsView(w http.ResponseWriter, r *http.Request) {
	h.publicContentView(w, r, newsKind, h.newsDetail)
}

// --- Public: gallery ---------------------------------------------------------

func (h *Handlers) GalleryList(w http.ResponseWriter, r *http.Request) {
	h.publicContentList(w, r, galleryKind, h.galleryList)
}

func (h *Handlers) GalleryView(w http.ResponseWriter, r *http.Request) {
	h.publicContentView(w, r, galleryKind, h.galleryDetail)
}

// publicPostView is one item on the public /news or /gallery listing
// page.
type publicPostView struct {
	ID       string
	Title    string
	PostedOn string
	Excerpt  string                 // news only; "" for galleries
	Photos   []content.GalleryPhoto // galleries only; nil for news
}

func postedOn(t time.Time) string {
	return t.In(time.Local).Format("Jan 2, 2006")
}

// excerpt trims a post's body to a preview length for the list view,
// breaking on a word boundary rather than mid-word, and only appending
// "…" if it actually had to cut something.
func excerpt(body string, maxLen int) string {
	body = strings.Join(strings.Fields(body), " ") // collapse newlines/extra whitespace for the one-line preview
	if len(body) <= maxLen {
		return body
	}
	cut := body[:maxLen]
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return cut + "…"
}

func (h *Handlers) publicContentList(w http.ResponseWriter, r *http.Request, kind contentKind, render func(http.ResponseWriter, *http.Request, []content.Post)) {
	unit, _ := units.UnitFromContext(r.Context())
	_, loggedIn := auth.UserFromContext(r.Context())

	var posts []content.Post
	var err error
	if loggedIn {
		posts, err = content.ListPublishedForUnit(r.Context(), h.Pool, unit.ID, kind.PageType)
	} else {
		posts, err = content.ListPublishedPublicForUnit(r.Context(), h.Pool, unit.ID, kind.PageType)
	}
	if err != nil {
		log.Printf("web: listing %s: %v", kind.LabelPlural, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	render(w, r, posts)
}

func (h *Handlers) publicContentView(w http.ResponseWriter, r *http.Request, kind contentKind, render func(http.ResponseWriter, *http.Request, content.Post)) {
	unit, _ := units.UnitFromContext(r.Context())
	_, loggedIn := auth.UserFromContext(r.Context())

	post, ok, err := content.GetPost(r.Context(), h.Pool, r.PathValue("id"), unit.ID, kind.PageType)
	if err != nil {
		log.Printf("web: loading %s: %v", kind.Label, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Not found, still a draft, or (for a signed-out visitor) members-only
	// all collapse to the same 404 — none of those distinctions should be
	// observable to someone who isn't allowed to see the item at all.
	if !ok || post.Status != "published" || (post.Visibility != "public" && !loggedIn) {
		http.NotFound(w, r)
		return
	}
	render(w, r, post)
}

func (h *Handlers) newsList(w http.ResponseWriter, r *http.Request, posts []content.Post) {
	items := make([]publicPostView, 0, len(posts))
	for _, p := range posts {
		items = append(items, publicPostView{ID: p.ID, Title: p.Title, PostedOn: postedOn(p.CreatedAt), Excerpt: excerpt(p.Body, 220)})
	}
	data := struct {
		baseData
		Items []publicPostView
	}{baseData: h.base(r, "News"), Items: items}
	h.render(w, h.newsListTmpl, data)
}

func (h *Handlers) newsDetail(w http.ResponseWriter, r *http.Request, p content.Post) {
	data := struct {
		baseData
		Title    string
		Body     string
		PostedOn string
	}{baseData: h.base(r, p.Title), Title: p.Title, Body: p.Body, PostedOn: postedOn(p.CreatedAt)}
	h.render(w, h.newsDetailTmpl, data)
}

func (h *Handlers) galleryList(w http.ResponseWriter, r *http.Request, posts []content.Post) {
	unit, _ := units.UnitFromContext(r.Context())
	_, loggedIn := auth.UserFromContext(r.Context())

	items := make([]publicPostView, 0, len(posts))
	for _, p := range posts {
		photos := h.filterViewableGalleryPhotos(r.Context(), unit.ID, content.ParseGalleryPhotos(p.Body), loggedIn)
		items = append(items, publicPostView{ID: p.ID, Title: p.Title, PostedOn: postedOn(p.CreatedAt), Photos: photos})
	}
	data := struct {
		baseData
		Items []publicPostView
	}{baseData: h.base(r, "Photos"), Items: items}
	h.render(w, h.galleryListTmpl, data)
}

func (h *Handlers) galleryDetail(w http.ResponseWriter, r *http.Request, p content.Post) {
	unit, _ := units.UnitFromContext(r.Context())
	_, loggedIn := auth.UserFromContext(r.Context())

	photos := h.filterViewableGalleryPhotos(r.Context(), unit.ID, content.ParseGalleryPhotos(p.Body), loggedIn)
	data := struct {
		baseData
		Title    string
		PostedOn string
		Photos   []content.GalleryPhoto
	}{baseData: h.base(r, p.Title), Title: p.Title, PostedOn: postedOn(p.CreatedAt), Photos: photos}
	h.render(w, h.galleryDetailTmpl, data)
}

// --- Admin: shared list/create/edit/publish, parameterized by kind ---------

// requireContentEditor mirrors requireTreasurer/requireSuperAdmin's shape
// (see treasury.go, settings_admin.go) — same non-lockout-friendly
// login-then-permission-then-acting-member resolution, just gated on
// units.CanEditUnitContent, the same permission /admin/home already uses.
func (h *Handlers) requireContentEditor(w http.ResponseWriter, r *http.Request, redirectPath string) (unit units.Unit, actor family.Member, ok bool) {
	unit, _ = units.UnitFromContext(r.Context())
	user, loggedIn := auth.UserFromContext(r.Context())
	if !loggedIn {
		http.Redirect(w, r, "/login?next="+redirectPath, http.StatusSeeOther)
		return unit, family.Member{}, false
	}

	caps, err := h.capabilitiesFor(r.Context(), user, unit.ID)
	if err != nil || !units.CanEditUnitContent(caps) {
		http.Error(w, "you don't have permission to manage this site's content", http.StatusForbidden)
		return unit, family.Member{}, false
	}

	actor, err = h.actingMember(r.Context(), user, unit.ID)
	if err != nil {
		http.Error(w, "could not determine acting member — has your family been added to the roster yet?", http.StatusBadRequest)
		return unit, family.Member{}, false
	}
	return unit, actor, true
}

func (h *Handlers) AdminNewsList(w http.ResponseWriter, r *http.Request) {
	h.adminContentList(w, r, newsKind)
}
func (h *Handlers) AdminNewsNew(w http.ResponseWriter, r *http.Request) {
	h.adminContentForm(w, r, newsKind, "")
}
func (h *Handlers) AdminNewsEdit(w http.ResponseWriter, r *http.Request) {
	h.adminContentForm(w, r, newsKind, r.PathValue("id"))
}
func (h *Handlers) AdminNewsCreate(w http.ResponseWriter, r *http.Request) {
	h.adminContentCreate(w, r, newsKind)
}
func (h *Handlers) AdminNewsUpdate(w http.ResponseWriter, r *http.Request) {
	h.adminContentUpdate(w, r, newsKind)
}
func (h *Handlers) AdminNewsPublishToggle(w http.ResponseWriter, r *http.Request) {
	h.adminContentPublishToggle(w, r, newsKind)
}

func (h *Handlers) AdminGalleryList(w http.ResponseWriter, r *http.Request) {
	h.adminContentList(w, r, galleryKind)
}
func (h *Handlers) AdminGalleryNew(w http.ResponseWriter, r *http.Request) {
	h.adminContentForm(w, r, galleryKind, "")
}
func (h *Handlers) AdminGalleryEdit(w http.ResponseWriter, r *http.Request) {
	h.adminContentForm(w, r, galleryKind, r.PathValue("id"))
}
func (h *Handlers) AdminGalleryCreate(w http.ResponseWriter, r *http.Request) {
	h.adminContentCreate(w, r, galleryKind)
}
func (h *Handlers) AdminGalleryUpdate(w http.ResponseWriter, r *http.Request) {
	h.adminContentUpdate(w, r, galleryKind)
}
func (h *Handlers) AdminGalleryPublishToggle(w http.ResponseWriter, r *http.Request) {
	h.adminContentPublishToggle(w, r, galleryKind)
}

// adminContentRow is one row on the admin list — a post/gallery plus its
// display-ready fields.
type adminContentRow struct {
	ID         string
	Title      string
	Status     string // "draft" or "published"
	Visibility string // "public" or "members"
	UpdatedOn  string
}

func (h *Handlers) adminContentList(w http.ResponseWriter, r *http.Request, kind contentKind) {
	unit, _, ok := h.requireContentEditor(w, r, kind.BasePath)
	if !ok {
		return
	}

	posts, err := content.ListAllForUnit(r.Context(), h.Pool, unit.ID, kind.PageType)
	if err != nil {
		log.Printf("web: listing %s for admin: %v", kind.LabelPlural, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rows := make([]adminContentRow, 0, len(posts))
	for _, p := range posts {
		rows = append(rows, adminContentRow{ID: p.ID, Title: p.Title, Status: p.Status, Visibility: p.Visibility, UpdatedOn: postedOn(p.UpdatedAt)})
	}

	data := struct {
		baseData
		Kind  contentKind
		Items []adminContentRow
	}{baseData: h.base(r, "Manage "+kind.LabelPlural), Kind: kind, Items: rows}
	h.render(w, h.adminContentListTmpl, data)
}

// adminContentForm renders the create form (id == "") or the edit form
// (id != "") — the same template handles both (see admin-content-form.html).
func (h *Handlers) adminContentForm(w http.ResponseWriter, r *http.Request, kind contentKind, id string) {
	unit, _, ok := h.requireContentEditor(w, r, kind.BasePath)
	if !ok {
		return
	}

	var post content.Post
	isEdit := id != ""
	if isEdit {
		var found bool
		var err error
		post, found, err = content.GetPost(r.Context(), h.Pool, id, unit.ID, kind.PageType)
		if err != nil {
			log.Printf("web: loading %s %s for edit: %v", kind.Label, id, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !found {
			http.NotFound(w, r)
			return
		}
	} else {
		post.Visibility = "members" // same default as calendar events — see events table's DEFAULT 'members'
	}

	var publicImages []files.PickerImage
	var eventPhotoGroups []files.EventFileGroup
	if kind.HasImagePicker {
		var err error
		publicImages, err = files.ListPublicMediaForUnit(r.Context(), h.Pool, unit.ID)
		if err != nil {
			log.Printf("web: loading public images: %v", err)
		}
		eventPhotoGroups, err = files.ListEventPhotoGroupsForUnit(r.Context(), h.Pool, unit.ID)
		if err != nil {
			log.Printf("web: loading event photo groups: %v", err)
		}
	}

	data := struct {
		baseData
		Kind             contentKind
		IsEdit           bool
		Post             content.Post
		PublicImages     []files.PickerImage
		EventPhotoGroups []files.EventFileGroup
	}{baseData: h.base(r, kind.Label), Kind: kind, IsEdit: isEdit, Post: post, PublicImages: publicImages, EventPhotoGroups: eventPhotoGroups}
	h.render(w, h.adminContentFormTmpl, data)
}

func (h *Handlers) adminContentCreate(w http.ResponseWriter, r *http.Request, kind contentKind) {
	unit, actor, ok := h.requireContentEditor(w, r, kind.BasePath)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.PostFormValue("title"))
	body := r.PostFormValue("body")
	visibility := r.PostFormValue("visibility")
	if visibility != "public" {
		visibility = "members"
	}
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}

	p, err := content.CreatePost(r.Context(), h.Pool, unit.ID, kind.PageType, title, body, visibility, "", actor.ID)
	if err != nil {
		log.Printf("web: creating %s: %v", kind.Label, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, kind.BasePath+"/"+p.ID+"/edit", http.StatusSeeOther)
}

func (h *Handlers) adminContentUpdate(w http.ResponseWriter, r *http.Request, kind contentKind) {
	unit, actor, ok := h.requireContentEditor(w, r, kind.BasePath)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	id := r.PathValue("id")
	// Confirm it exists, belongs to this unit, and is the expected page
	// type before writing — same "don't trust the path" posture as every
	// other admin edit handler (see admin_roster.go's Scope checks).
	if _, found, err := content.GetPost(r.Context(), h.Pool, id, unit.ID, kind.PageType); err != nil {
		log.Printf("web: loading %s %s for update: %v", kind.Label, id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}

	title := strings.TrimSpace(r.PostFormValue("title"))
	body := r.PostFormValue("body")
	visibility := r.PostFormValue("visibility")
	if visibility != "public" {
		visibility = "members"
	}
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}

	if _, err := content.UpdatePost(r.Context(), h.Pool, id, unit.ID, title, body, visibility, actor.ID); err != nil {
		log.Printf("web: updating %s %s: %v", kind.Label, id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, kind.BasePath+"/"+id+"/edit", http.StatusSeeOther)
}

func (h *Handlers) adminContentPublishToggle(w http.ResponseWriter, r *http.Request, kind contentKind) {
	unit, actor, ok := h.requireContentEditor(w, r, kind.BasePath)
	if !ok {
		return
	}

	id := r.PathValue("id")
	post, found, err := content.GetPost(r.Context(), h.Pool, id, unit.ID, kind.PageType)
	if err != nil {
		log.Printf("web: loading %s %s: %v", kind.Label, id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	if err := content.SetPublished(r.Context(), h.Pool, id, unit.ID, post.Status != "published", actor.ID); err != nil {
		log.Printf("web: toggling publish state of %s %s: %v", kind.Label, id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, kind.BasePath, http.StatusSeeOther)
}
