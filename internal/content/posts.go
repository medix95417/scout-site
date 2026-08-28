package content

// This file adds two more uses of the same content_pages table
// content.go already established for homepage sections: news posts
// (page_type = 'post') and photo galleries (page_type = 'gallery'). Both
// reuse the schema's existing draft/published lifecycle
// (content_status already had 'draft' before this file existed — see
// internal/db/migrations/0001_init.sql — homepage sections just never
// used anything but 'published') so a leader can assemble an announcement
// or a trip's photo set before it's visible to families, then publish it
// with one click.
//
// Unlike homepage sections, posts and galleries are leader-authored but
// not youth-submission-approved (no SPL/Patrol Leader workflow here) —
// same reasoning content.go already documented for homepage copy: this
// is low-risk copy/link editing by trusted adult leaders, not something
// that needs the approval-request machinery calendar events use.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// Post is one news post or photo gallery. The same struct serves both —
// PageType ("post" or "gallery") is what tells a caller which; a
// gallery's Body is its photos, one per line (see ParseGalleryPhotos),
// not prose.
type Post struct {
	ID         string
	UnitID     string
	SubGroupID string // "" unless scoped to one den/patrol's own page
	Slug       string
	Title      string
	Body       string
	PageType   string // "post" or "gallery"
	Visibility string // "public" or "members"
	Status     string // "draft" or "published"
	CreatedAt  time.Time
	UpdatedAt  time.Time

	// PhotoDate is the date the album's photos were actually taken, when
	// a leader has set one — nil when they haven't. Distinct from
	// CreatedAt, which is when the album was typed into the site: an
	// album of last spring's campout uploaded today should file under
	// last spring, not above this month's. Use DisplayDate rather than
	// reading this directly.
	PhotoDate *time.Time
}

// DisplayDate is the date to show and sort by: the leader-set PhotoDate
// when there is one, otherwise the creation time. Every read path uses
// this, so an album with no date set behaves exactly as it did before
// the field existed.
func (p Post) DisplayDate() time.Time {
	if p.PhotoDate != nil {
		return *p.PhotoDate
	}
	return p.CreatedAt
}

// GalleryPhoto is one photo (or video) in a gallery, parsed from a Post's
// Body. The name predates video support — a gallery may hold a mix of
// both — but isn't worth renaming everywhere IsVideo already tells a
// caller which.
type GalleryPhoto struct {
	URL     string
	Caption string // "" if the leader didn't add one
	IsVideo bool
}

// videoExtensions catches a manually pasted video link (one not chosen
// via a picker, so ParseGalleryPhotos has no content-type to go on) —
// common direct-file video extensions, checked against the URL's path
// case-insensitively. Not exhaustive; a link that doesn't match still
// works as a photo tile with a plain (non-playing) <img>, same as before
// video support existed.
var videoExtensions = []string{".mp4", ".webm", ".mov", ".m4v", ".ogv", ".avi", ".mkv"}

// ParseGalleryPhotos reads a gallery's Body as one item per line — the
// same "one item per line" convention HomepageSections already uses for
// home-program (see content.go). Each line is a bare URL, "URL |
// Caption", or "URL | Caption | video" if the third field marks it as a
// video (the picker in _image-picker.html appends that marker
// automatically for a video it already knows the content type of;
// ParseGalleryPhotos falls back to sniffing the URL's file extension for
// a manually pasted link). Blank lines are skipped so a stray empty line
// in the textarea doesn't produce an empty tile.
func ParseGalleryPhotos(body string) []GalleryPhoto {
	var photos []GalleryPhoto
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		url := strings.TrimSpace(parts[0])
		var caption string
		if len(parts) > 1 {
			caption = strings.TrimSpace(parts[1])
		}
		isVideo := len(parts) > 2 && strings.EqualFold(strings.TrimSpace(parts[2]), "video")
		if !isVideo {
			lower := strings.ToLower(url)
			for _, ext := range videoExtensions {
				if strings.HasSuffix(lower, ext) {
					isVideo = true
					break
				}
			}
		}
		photos = append(photos, GalleryPhoto{URL: url, Caption: caption, IsVideo: isVideo})
	}
	return photos
}

// randomSlug generates an internal, never-user-facing slug for a new post
// or gallery. Posts/galleries are addressed by ID in URLs (/news/{id},
// /gallery/{id}), not by slug — unlike homepage sections' well-known
// slugs (see HomepageSections), a post's slug only exists to satisfy
// content_pages' UNIQUE(unit_id, slug) constraint, so a short random
// suffix is enough; there's no need for it to be human-readable.
func randomSlug(pageType string) (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return pageType + "-" + hex.EncodeToString(b), nil
}

// displayDateOrder sorts by the leader-set photo date where there is one
// and the creation time otherwise — the SQL mirror of Post.DisplayDate,
// so what the page shows and what it sorts by can't disagree. created_at
// breaks ties so two albums given the same date keep a stable order.
const displayDateOrder = `COALESCE(photo_date::timestamptz, created_at) DESC, created_at DESC`

const postColumns = `id, unit_id, COALESCE(sub_group_id::text, ''), slug, title, body, page_type::text, visibility::text, status::text, created_at, updated_at, photo_date`

func scanPost(row interface{ Scan(dest ...any) error }) (Post, error) {
	var p Post
	err := row.Scan(&p.ID, &p.UnitID, &p.SubGroupID, &p.Slug, &p.Title, &p.Body, &p.PageType, &p.Visibility, &p.Status, &p.CreatedAt, &p.UpdatedAt, &p.PhotoDate)
	return p, err
}

// CreatePost creates a new post or gallery as a draft — a leader
// publishes it explicitly via SetPublished once it's ready, so a
// half-written announcement or a gallery mid-upload is never visible to
// families in between. subGroupID scopes it to one den/patrol's own page
// (see GroupView's "News" section) — pass "" for the unit-wide /news and
// /gallery feeds.
func CreatePost(ctx context.Context, pool *pgxpool.Pool, unitID, pageType, title, body, visibility, subGroupID, actorID string) (Post, error) {
	slug, err := randomSlug(pageType)
	if err != nil {
		return Post{}, fmt.Errorf("content: generating slug: %w", err)
	}

	var subGroupIDArg *string
	if subGroupID != "" {
		subGroupIDArg = &subGroupID
	}

	p, err := scanPost(pool.QueryRow(ctx, `
		INSERT INTO content_pages (unit_id, sub_group_id, slug, title, body, page_type, visibility, status, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'draft', $8)
		RETURNING `+postColumns,
		unitID, subGroupIDArg, slug, title, body, pageType, visibility, actorID))
	if err != nil {
		return Post{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "content_page",
		EntityID:   p.ID,
		ActorID:    &actorID,
		Action:     "create",
		After:      p,
	})
	return p, nil
}

// UpdatePost edits a post/gallery's title, body, and visibility. Status
// (draft/published) is untouched — editing a live announcement to fix a
// typo shouldn't silently pull it back to draft; use SetPublished for
// that.
// UpdatePost edits a post/gallery. photoDate is the leader-set date the
// content is about (nil clears it, falling display and ordering back to
// created_at — see Post.DisplayDate).
func UpdatePost(ctx context.Context, pool *pgxpool.Pool, id, unitID, title, body, visibility string, photoDate *time.Time, actorID string) (Post, error) {
	before, _, err := GetPostAnyType(ctx, pool, id, unitID)
	if err != nil {
		return Post{}, err
	}

	p, err := scanPost(pool.QueryRow(ctx, `
		UPDATE content_pages SET title = $1, body = $2, visibility = $3, photo_date = $6, updated_at = now()
		WHERE id = $4 AND unit_id = $5
		RETURNING `+postColumns,
		title, body, visibility, id, unitID, photoDate))
	if err != nil {
		return Post{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "content_page",
		EntityID:   p.ID,
		ActorID:    &actorID,
		Action:     "update",
		Before:     before,
		After:      p,
	})
	return p, nil
}

// SetPublished flips a post/gallery between draft and published.
func SetPublished(ctx context.Context, pool *pgxpool.Pool, id, unitID string, publish bool, actorID string) error {
	status, action := "draft", "unpublish"
	if publish {
		status, action = "published", "publish"
	}

	tag, err := pool.Exec(ctx, `
		UPDATE content_pages SET status = $1, updated_at = now() WHERE id = $2 AND unit_id = $3
	`, status, id, unitID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("content: item %s not found in unit %s", id, unitID)
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "content_page",
		EntityID:   id,
		ActorID:    &actorID,
		Action:     action,
	})
	return nil
}

// GetPostAnyType fetches one post/gallery by ID, scoped to a unit, without
// checking PageType — used internally (e.g. by UpdatePost, which already
// knows the ID came from a same-page-type edit form) and by admin
// handlers that don't need the type-mismatch guard GetPost below adds.
func GetPostAnyType(ctx context.Context, pool *pgxpool.Pool, id, unitID string) (Post, bool, error) {
	p, err := scanPost(pool.QueryRow(ctx, `SELECT `+postColumns+` FROM content_pages WHERE id = $1 AND unit_id = $2`, id, unitID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Post{}, false, nil
		}
		return Post{}, false, err
	}
	return p, true, nil
}

// GetPost fetches one post/gallery by ID, scoped to both a unit and the
// expected PageType — so visiting /news/{id} with a gallery's ID (or vice
// versa) comes back "not found" instead of rendering a gallery's
// line-per-photo Body as if it were prose, or an announcement's prose as
// a broken photo grid.
func GetPost(ctx context.Context, pool *pgxpool.Pool, id, unitID, pageType string) (Post, bool, error) {
	p, ok, err := GetPostAnyType(ctx, pool, id, unitID)
	if err != nil {
		return Post{}, false, err
	}
	if !ok || p.PageType != pageType {
		return Post{}, false, nil
	}
	return p, true, nil
}

func queryPosts(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) ([]Post, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var posts []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

// ListAllForUnit returns every unit-wide post/gallery of the given type
// (draft and published alike), newest first — what the admin list
// (/admin/news, /admin/gallery) shows. Excludes sub_group_id-scoped
// posts: those are a den's/patrol's own news, managed and shown only on
// that sub-group's page (see GroupView/ListPublishedForSubGroup), not
// mixed into the whole-unit feed.
func ListAllForUnit(ctx context.Context, pool *pgxpool.Pool, unitID, pageType string) ([]Post, error) {
	return queryPosts(ctx, pool, `
		SELECT `+postColumns+`
		FROM content_pages
		WHERE unit_id = $1 AND page_type = $2 AND sub_group_id IS NULL
		ORDER BY `+displayDateOrder+`
	`, unitID, pageType)
}

// ListPublishedForUnit returns published, unit-wide posts/galleries of
// any visibility, newest first — what a logged-in family sees. See
// ListAllForUnit on why sub_group_id-scoped posts are excluded.
func ListPublishedForUnit(ctx context.Context, pool *pgxpool.Pool, unitID, pageType string) ([]Post, error) {
	return queryPosts(ctx, pool, `
		SELECT `+postColumns+`
		FROM content_pages
		WHERE unit_id = $1 AND page_type = $2 AND status = 'published' AND sub_group_id IS NULL
		ORDER BY `+displayDateOrder+`
	`, unitID, pageType)
}

// ListPublishedPublicForUnit is ListPublishedForUnit restricted to
// visibility = 'public' — what an unauthenticated visitor sees. Mirrors
// calendar.ListUpcomingPublicForUnit's split for the same reason: an
// unauthenticated request must never be able to construct a query that
// returns a members-only item.
func ListPublishedPublicForUnit(ctx context.Context, pool *pgxpool.Pool, unitID, pageType string) ([]Post, error) {
	return queryPosts(ctx, pool, `
		SELECT `+postColumns+`
		FROM content_pages
		WHERE unit_id = $1 AND page_type = $2 AND status = 'published' AND visibility = 'public' AND sub_group_id IS NULL
		ORDER BY `+displayDateOrder+`
	`, unitID, pageType)
}

// ListPublishedForSubGroup returns a den's/patrol's own published news
// posts, newest first — the "News" section on that sub-group's members-
// only page (see internal/web/groups.go's GroupView). Always page_type =
// 'post': a sub-group's photos are handled separately, by linking
// existing file-library images (files.ListForSubGroup), not by a
// sub-group-scoped gallery post.
func ListPublishedForSubGroup(ctx context.Context, pool *pgxpool.Pool, subGroupID, unitID string) ([]Post, error) {
	return queryPosts(ctx, pool, `
		SELECT `+postColumns+`
		FROM content_pages
		WHERE unit_id = $1 AND sub_group_id = $2 AND page_type = 'post' AND status = 'published'
		ORDER BY created_at DESC
	`, unitID, subGroupID)
}
