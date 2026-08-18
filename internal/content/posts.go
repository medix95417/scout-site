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
	Slug       string
	Title      string
	Body       string
	PageType   string // "post" or "gallery"
	Visibility string // "public" or "members"
	Status     string // "draft" or "published"
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// GalleryPhoto is one photo in a gallery, parsed from a Post's Body.
type GalleryPhoto struct {
	URL     string
	Caption string // "" if the leader didn't add one
}

// ParseGalleryPhotos reads a gallery's Body as one photo per line — the
// same "one item per line" convention HomepageSections already uses for
// home-program (see content.go). Each line is either a bare image URL or
// "URL | Caption" if the leader wants a caption under that photo. Blank
// lines are skipped so a stray empty line in the textarea doesn't
// produce an empty tile.
func ParseGalleryPhotos(body string) []GalleryPhoto {
	var photos []GalleryPhoto
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		url, caption, _ := strings.Cut(line, "|")
		photos = append(photos, GalleryPhoto{URL: strings.TrimSpace(url), Caption: strings.TrimSpace(caption)})
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

const postColumns = `id, unit_id, slug, title, body, page_type::text, visibility::text, status::text, created_at, updated_at`

func scanPost(row interface{ Scan(dest ...any) error }) (Post, error) {
	var p Post
	err := row.Scan(&p.ID, &p.UnitID, &p.Slug, &p.Title, &p.Body, &p.PageType, &p.Visibility, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// CreatePost creates a new post or gallery as a draft — a leader
// publishes it explicitly via SetPublished once it's ready, so a
// half-written announcement or a gallery mid-upload is never visible to
// families in between.
func CreatePost(ctx context.Context, pool *pgxpool.Pool, unitID, pageType, title, body, visibility, actorID string) (Post, error) {
	slug, err := randomSlug(pageType)
	if err != nil {
		return Post{}, fmt.Errorf("content: generating slug: %w", err)
	}

	p, err := scanPost(pool.QueryRow(ctx, `
		INSERT INTO content_pages (unit_id, slug, title, body, page_type, visibility, status, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, 'draft', $7)
		RETURNING `+postColumns,
		unitID, slug, title, body, pageType, visibility, actorID))
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
func UpdatePost(ctx context.Context, pool *pgxpool.Pool, id, unitID, title, body, visibility, actorID string) (Post, error) {
	before, _, err := GetPostAnyType(ctx, pool, id, unitID)
	if err != nil {
		return Post{}, err
	}

	p, err := scanPost(pool.QueryRow(ctx, `
		UPDATE content_pages SET title = $1, body = $2, visibility = $3, updated_at = now()
		WHERE id = $4 AND unit_id = $5
		RETURNING `+postColumns,
		title, body, visibility, id, unitID))
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

// ListAllForUnit returns every post/gallery of the given type (draft and
// published alike), newest first — what the admin list
// (/admin/news, /admin/gallery) shows.
func ListAllForUnit(ctx context.Context, pool *pgxpool.Pool, unitID, pageType string) ([]Post, error) {
	return queryPosts(ctx, pool, `
		SELECT `+postColumns+`
		FROM content_pages
		WHERE unit_id = $1 AND page_type = $2
		ORDER BY created_at DESC
	`, unitID, pageType)
}

// ListPublishedForUnit returns published posts/galleries of any
// visibility, newest first — what a logged-in family sees.
func ListPublishedForUnit(ctx context.Context, pool *pgxpool.Pool, unitID, pageType string) ([]Post, error) {
	return queryPosts(ctx, pool, `
		SELECT `+postColumns+`
		FROM content_pages
		WHERE unit_id = $1 AND page_type = $2 AND status = 'published'
		ORDER BY created_at DESC
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
		WHERE unit_id = $1 AND page_type = $2 AND status = 'published' AND visibility = 'public'
		ORDER BY created_at DESC
	`, unitID, pageType)
}
