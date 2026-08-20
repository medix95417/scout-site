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
	stockPhotoCampfire  = "https://commons.wikimedia.org/wiki/Special:FilePath/Cole_Canoe_Base_Boy_Scout_Campfire.JPG"
	stockPhotoHiking    = "https://commons.wikimedia.org/wiki/Special:FilePath/Children_hiking_in_the_forest.jpg"
	stockPhotoCampsite  = "https://commons.wikimedia.org/wiki/Special:FilePath/Campsite_at_NoBeBoSco_07152018.jpg"
	stockPhotoDerby     = "https://commons.wikimedia.org/wiki/Special:FilePath/Pinewood_derby_cars_02.jpg"
	stockPhotoTroopCamp = "https://commons.wikimedia.org/wiki/Special:FilePath/Boy_Scouts_~_Camp_Pioneer_(7839747376).jpg"
	// Philmont Scout Ranch's "Tooth of Time" — a mountain landscape, not a
	// campfire — so the Troop hero reads distinctly older/more rugged
	// (high-adventure) rather than reusing the Pack's cozier campfire photo.
	stockPhotoPhilmont = "https://commons.wikimedia.org/wiki/Special:FilePath/Philmont_Scout_Ranch_Tooth_of_Time.jpg"
)

// HomepageSections is unit-type-aware: Packs and Troops get slightly
// different default copy (and, for the second gallery photo, a slightly
// different stock image), but the same editing mechanism. The Pack layout
// mirrors pack6crestwood.org's structure — full-bleed hero banner with
// photo, a bulleted "Our Program" list with a program photo, meeting/
// leadership info, a two-photo gallery strip, and an optional social
// link — adapted to only link to pages this site actually has.
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
			{Slug: "home-gallery-1", Label: "Gallery photo 1 URL", Kind: "image", Placeholder: stockPhotoCampsite, Help: imageHelp},
			{Slug: "home-gallery-2", Label: "Gallery photo 2 URL", Kind: "image", Placeholder: stockPhotoTroopCamp, Help: imageHelp},
			{Slug: "home-social", Label: "Social media link (optional)", Kind: "url", Help: "e.g. your troop's Facebook or Instagram page."},
			{Slug: "home-facebook", Label: "Facebook page URL (optional)", Kind: "url", Help: "Shows a Facebook icon/link on the homepage if set."},
			{Slug: "home-instagram", Label: "Instagram profile URL (optional)", Kind: "url", Help: "Shows an Instagram icon/link on the homepage if set."},
			{Slug: "home-tiktok", Label: "TikTok profile URL (optional)", Kind: "url", Help: "Shows a TikTok icon/link on the homepage if set."},
		}
	}
	return []SectionDef{
		{Slug: "home-hero", Label: "Hero tagline", Placeholder: "Adventure starts here — join our Cub Scout pack!"},
		{Slug: "home-hero-image", Label: "Hero background photo URL", Kind: "url", Placeholder: stockPhotoCampfire, Help: imageHelp},
		{Slug: "home-program", Label: "Our program (one activity per line)", Placeholder: "Monthly pack meetings\nDens meeting every other week\nCamping (tent and cabin)\nPinewood Derby\nCommunity service projects", Help: "Each line becomes one bullet point on the homepage, like pack6crestwood.org's \"Our Program\" list."},
		{Slug: "home-program-image", Label: "\"Our Program\" photo URL", Kind: "image", Placeholder: stockPhotoHiking, Help: imageHelp},
		{Slug: "home-meeting", Label: "Meeting info", Placeholder: "Contact us for our current meeting time and location."},
		{Slug: "home-leadership", Label: "Leadership & contact", Placeholder: "Contact our Cubmaster to learn more about joining."},
		{Slug: "home-gallery-1", Label: "Gallery photo 1 URL", Kind: "image", Placeholder: stockPhotoCampsite, Help: imageHelp},
		{Slug: "home-gallery-2", Label: "Gallery photo 2 URL", Kind: "image", Placeholder: stockPhotoDerby, Help: imageHelp},
		{Slug: "home-social", Label: "Social media link (optional)", Kind: "url", Help: "e.g. your pack's Instagram or Facebook page."},
		{Slug: "home-facebook", Label: "Facebook page URL (optional)", Kind: "url", Help: "Shows a Facebook icon/link on the homepage if set."},
		{Slug: "home-instagram", Label: "Instagram profile URL (optional)", Kind: "url", Help: "Shows an Instagram icon/link on the homepage if set."},
		{Slug: "home-tiktok", Label: "TikTok profile URL (optional)", Kind: "url", Help: "Shows a TikTok icon/link on the homepage if set."},
	}
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
	rows, err := pool.Query(ctx, `
		SELECT id, unit_id, slug, title, body, updated_at
		FROM content_pages
		WHERE unit_id = $1 AND slug LIKE 'home-%'
	`, unitID)
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
