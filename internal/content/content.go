// Package content manages editable homepage sections (and, later, general
// announcements/pages) so leaders can update copy — meeting times, program
// blurbs, leadership contacts — without touching code or redeploying.
//
// It's built on the existing `content_pages` table rather than a new one:
// a homepage section is just a content_pages row with a well-known slug
// (see HomepageSections below) and page_type 'page'. That keeps this
// feature inside the generic content model the architecture doc already
// established instead of adding a parallel system.
package content

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// Section is one editable block of homepage copy.
type Section struct {
	ID        string
	UnitID    string
	Slug      string
	Title     string
	Body      string // leader-authored plain text; the web layer escapes and formats it per section (see internal/web)
	UpdatedAt time.Time
}

// SectionDef describes a homepage slot the site knows how to render, plus
// the placeholder shown before a leader has ever filled it in. Order here
// is display order on both the homepage and the admin editing list.
type SectionDef struct {
	Slug        string
	Label       string // shown in the admin "edit homepage" list
	Placeholder string // shown on the live site until a leader edits it
	Kind        string // "" (default) = multi-line text box; "url" = single-line link field; "image" = single-line image-URL field (gets a preview + "choose from library" picker on /admin/home — see content-admin.html)
	Help        string // optional short instructions shown under the field in the admin list
}

// Stock placeholder photos — freely-licensed Scouting/outdoor photos from
// Wikimedia Commons, used as defaults so a brand-new site doesn't launch
// with empty gray boxes. Linked via Commons' Special:FilePath, which is
// the stable, official way to hotlink a Commons file without knowing its
// internal storage path. These are meant to be swapped for the unit's own
// photos via /admin/home once they have some — hotlinking a third party
// indefinitely isn't ideal for a permanent production site, and real
// troop/pack photos will always beat stock ones.
const (
	stockPhotoCampfire = "https://commons.wikimedia.org/wiki/Special:FilePath/Cole_Canoe_Base_Boy_Scout_Campfire.JPG"
	stockPhotoHiking   = "https://commons.wikimedia.org/wiki/Special:FilePath/Children_hiking_in_the_forest.jpg"
	// Philmont Scout Ranch's "Tooth of Time" — a mountain landscape, not a
	// campfire — so the Troop hero reads distinctly older/more rugged
	// (high-adventure) rather than reusing the Pack's cozier campfire photo.
	stockPhotoPhilmont = "https://commons.wikimedia.org/wiki/Special:FilePath/Philmont_Scout_Ranch_Tooth_of_Time.jpg"
)

// HomepageSections is unit-type-aware: Packs and Troops get slightly
// different default copy, but the same editing mechanism. The Pack layout
// mirrors pack6crestwood.org's structure — full-bleed hero banner with
// photo, a bulleted "Our Program" list with a program photo, meeting/
// leadership info, and an optional social link — adapted to only link to
// pages this site actually has. The homepage's photo preview is no longer
// a leader-curated field here — see HomeActivityPhotos, which pulls
// straight from recent Photo Album posts instead, "by activity" per the
// request that replaced the old single hand-picked strip.
func HomepageSections(unitType string) []SectionDef {
	imageHelp := "Paste a link to an image hosted elsewhere (e.g. a photo you've uploaded to Google Photos/Drive and shared publicly). Defaults to a stock Scouting photo until you swap it for your own."

	if unitType == "troop" {
		return []SectionDef{
			{Slug: "home-hero", Label: "Hero tagline", Placeholder: "Adventure, leadership, and lifelong friendships — join our Scouts BSA troop."},
			{Slug: "home-hero-image", Label: "Hero background photo URL", Kind: "image", Placeholder: stockPhotoPhilmont, Help: imageHelp},
			{Slug: "home-program", Label: "Our program (one activity per line)", Placeholder: "Weekly troop meetings\nMonthly campouts\nService projects\nMerit badge workshops\nHigh-adventure trips", Help: "Each line becomes one bullet point on the homepage."},
			{Slug: "home-program-image", Label: "\"Our Program\" photo URL", Kind: "image", Placeholder: stockPhotoHiking, Help: imageHelp},
			{Slug: "home-meeting", Label: "Meeting info", Placeholder: "Meetings are held weekly — contact us for the current time and location."},
			{Slug: "home-leadership", Label: "Leadership & contact", Placeholder: "Contact our Scoutmaster to learn more about joining."},
			{Slug: "home-social", Label: "Social media link (optional)", Kind: "url", Help: "e.g. your troop's Facebook or Instagram page."},
		}
	}
	return []SectionDef{
		{Slug: "home-hero", Label: "Hero tagline", Placeholder: "Adventure starts here — join our Cub Scout pack!"},
		{Slug: "home-hero-image", Label: "Hero background photo URL", Kind: "image", Placeholder: stockPhotoCampfire, Help: imageHelp},
		{Slug: "home-program", Label: "Our program (one activity per line)", Placeholder: "Monthly pack meetings\nDens meeting every other week\nCamping (tent and cabin)\nPinewood Derby\nCommunity service projects", Help: "Each line becomes one bullet point on the homepage, like pack6crestwood.org's \"Our Program\" list."},
		{Slug: "home-program-image", Label: "\"Our Program\" photo URL", Kind: "image", Placeholder: stockPhotoHiking, Help: imageHelp},
		{Slug: "home-meeting", Label: "Meeting info", Placeholder: "Contact us for our current meeting time and location."},
		{Slug: "home-leadership", Label: "Leadership & contact", Placeholder: "Contact our Cubmaster to learn more about joining."},
		{Slug: "home-social", Label: "Social media link (optional)", Kind: "url", Help: "e.g. your pack's Instagram or Facebook page."},
	}
}

// heroSlugPrefix is the well-known slug prefix for a page's hero banner
// image — see HeroPages/HeroSections below.
const heroSlugPrefix = "pagehero-"

// HeroPage describes one of the main member/public-facing pages (besides
// the homepage, which already has its own richer hero) that can carry an
// editable hero banner image.
type HeroPage struct {
	Key   string // slug suffix (heroSlugPrefix + Key is the content_pages slug) and the internal/web lookup key for the current request path
	Label string // shown in the admin "Page Hero Banners" list
}

// HeroPages is every page offering an editable hero banner, in admin-list
// display order. Deliberately a smaller set than "every page" — the
// pages a visitor actually lands on and browses, not admin/settings
// pages. The homepage isn't here: it already has its own hero (background
// photo + tagline + CTA), edited via HomepageSections, not this mechanism.
var HeroPages = []HeroPage{
	{Key: "calendar", Label: "Calendar"},
	{Key: "news", Label: "News"},
	{Key: "gallery", Label: "Gallery"},
	{Key: "roster", Label: "Roster"},
	{Key: "directory", Label: "Family Directory"},
	{Key: "files", Label: "Files"},
	{Key: "groups", Label: "Patrols/Dens list"},
}

// HeroSections is HeroPages expressed as SectionDefs (Kind "image", the
// same paste-a-URL-or-choose-from-your-public-library field HomepageSections
// already uses) so /admin/home's existing editing UI and picker can render
// these with zero new code — see internal/web/web.go's HomeContentList/
// HomeContentSave, which combine this list with HomepageSections.
func HeroSections() []SectionDef {
	defs := make([]SectionDef, len(HeroPages))
	for i, p := range HeroPages {
		defs[i] = SectionDef{
			Slug:  heroSlugPrefix + p.Key,
			Label: p.Label + " page hero banner (optional)",
			Kind:  "image",
			Help:  "Paste an image URL, or choose one already marked \"Public\" in your file library. Leave blank for no banner on this page.",
		}
	}
	return defs
}

// HeroSectionsForUnit fetches every hero banner already set for a unit,
// keyed by slug — the hero sibling of SectionsForUnit.
func HeroSectionsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) (map[string]Section, error) {
	return sectionsForUnitLike(ctx, pool, unitID, heroSlugPrefix+"%")
}

// HeroURLForPage returns the hero banner image URL for one page (see
// HeroPage.Key), or "" if that page has no hero set — what every hero-
// capable page's own handler calls to decorate its baseData.
func HeroURLForPage(ctx context.Context, pool *pgxpool.Pool, unitID, pageKey string) (string, error) {
	s, found, err := GetSection(ctx, pool, unitID, heroSlugPrefix+pageKey)
	if err != nil || !found {
		return "", err
	}
	return s.Body, nil
}

// GetSection fetches one homepage section for a unit. Returns
// (Section{}, false, nil) if it hasn't been created yet — callers should
// fall back to the SectionDef placeholder, not treat this as an error.
func GetSection(ctx context.Context, pool *pgxpool.Pool, unitID, slug string) (Section, bool, error) {
	var s Section
	err := pool.QueryRow(ctx, `
		SELECT id, unit_id, slug, title, body, updated_at
		FROM content_pages
		WHERE unit_id = $1 AND slug = $2
	`, unitID, slug).Scan(&s.ID, &s.UnitID, &s.Slug, &s.Title, &s.Body, &s.UpdatedAt)
	if err != nil {
		return Section{}, false, nil //nolint:nilerr // "not created yet" is a normal, expected outcome
	}
	return s, true, nil
}

// SectionsForUnit fetches every homepage section already created for a
// unit, keyed by slug, in one query — what the Home handler uses so
// rendering the page doesn't cost one query per section.
func SectionsForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) (map[string]Section, error) {
	return sectionsForUnitLike(ctx, pool, unitID, "home-%")
}

// LegacySocialURL returns a Facebook/Instagram/TikTok URL saved through
// the old /admin/home content editor under one of its former slugs
// ("home-facebook", "home-instagram", "home-tiktok" — see
// internal/settings, which now owns these under /admin/settings), or ""
// if never set there. Purely a one-time fallback so a unit that already
// configured one of these before the move doesn't see it go blank the
// first time /admin/settings loads; once re-saved through the new form
// the settings value takes over and this is no longer consulted.
func LegacySocialURL(ctx context.Context, pool *pgxpool.Pool, unitID, slug string) (string, error) {
	s, found, err := GetSection(ctx, pool, unitID, slug)
	if err != nil || !found {
		return "", err
	}
	return s.Body, nil
}

// sectionsForUnitLike is the shared query behind SectionsForUnit and
// HeroSectionsForUnit — both just want every content_pages row for a unit
// whose slug matches a known prefix, keyed by slug.
func sectionsForUnitLike(ctx context.Context, pool *pgxpool.Pool, unitID, slugLike string) (map[string]Section, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, unit_id, slug, title, body, updated_at
		FROM content_pages
		WHERE unit_id = $1 AND slug LIKE $2
	`, unitID, slugLike)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sections := make(map[string]Section)
	for rows.Next() {
		var s Section
		if err := rows.Scan(&s.ID, &s.UnitID, &s.Slug, &s.Title, &s.Body, &s.UpdatedAt); err != nil {
			return nil, err
		}
		sections[s.Slug] = s
	}
	return sections, rows.Err()
}

// UpsertSection creates or updates a homepage section and logs the change
// to the audit trail. Homepage sections are always public/published —
// there's no draft state for them in Phase 1, since they're small,
// low-risk copy edits by trusted leaders, not youth submissions requiring
// approval (unlike calendar events).
func UpsertSection(ctx context.Context, pool *pgxpool.Pool, unitID, slug, title, body, actorID string) (Section, error) {
	var s Section
	err := pool.QueryRow(ctx, `
		INSERT INTO content_pages (unit_id, slug, title, body, page_type, visibility, status, created_by)
		VALUES ($1, $2, $3, $4, 'page', 'public', 'published', $5)
		ON CONFLICT (unit_id, slug) DO UPDATE
			SET title = EXCLUDED.title, body = EXCLUDED.body, updated_at = now()
		RETURNING id, unit_id, slug, title, body, updated_at
	`, unitID, slug, title, body, actorID).Scan(&s.ID, &s.UnitID, &s.Slug, &s.Title, &s.Body, &s.UpdatedAt)
	if err != nil {
		return Section{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "content_page",
		EntityID:   s.ID,
		ActorID:    &actorID,
		Action:     "update",
		After:      s,
	})

	return s, nil
}
