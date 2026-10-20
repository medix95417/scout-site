package files

// Posters: the still picture a video shows before it is played.
//
// A poster is made in the leader's browser — the page draws a frame of the
// video onto a canvas and sends it up as a picture — because the browser is
// the one place that can already decode whatever the leader's phone recorded.
// The server has no video decoder, and gaining one would mean either a much
// larger runtime image or parsing every upload with a native library on the
// machine that holds the database credentials.
//
// This file is the data side: where a poster is kept and whether a video has
// one. Nothing here knows about HTTP or about pictures.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoSuchFile is returned when a poster is recorded against a file that
// isn't in the unit.
var ErrNoSuchFile = errors.New("files: no such file in this unit")

// PosterKey is where a video's poster is kept in the bucket.
//
// Derived from the file's id rather than from its storage key, which is what
// the thumbnail does. A thumbnail can be rebuilt from the photo whenever it
// is missing, so it can afford to follow the file around; a poster can only
// be remade by a leader with a browser, so it must not depend on anything
// that moves. A file's storage key is rewritten by the rekey pass; its id
// never changes, and neither does the unit it belongs to.
//
// "<uuid>.jpg" cannot equal any file's own key, which always has a slug after
// the uuid ("<uuid>-<name>.<ext>"), so a poster never overwrites an upload.
func PosterKey(unitID, fileID string) string {
	return fmt.Sprintf("%s/posters/%s.jpg", unitID, fileID)
}

// SetPoster records whether a file has a poster. Scoped to the unit, like
// every other write on a file, so an id from the other unit changes nothing.
func SetPoster(ctx context.Context, pool *pgxpool.Pool, fileID, unitID string, has bool) error {
	tag, err := pool.Exec(ctx,
		`UPDATE files SET has_poster = $1 WHERE id = $2 AND unit_id = $3`, has, fileID, unitID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoSuchFile
	}
	return nil
}

// HasPoster reports whether a file has a poster. A file that isn't in the
// unit has none.
func HasPoster(ctx context.Context, pool *pgxpool.Pool, fileID, unitID string) (bool, error) {
	var has bool
	err := pool.QueryRow(ctx,
		`SELECT has_poster FROM files WHERE id = $1 AND unit_id = $2`, fileID, unitID).Scan(&has)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return has, err
}

// VideoPoster is one video in a unit and whether it has a poster yet.
type VideoPoster struct {
	ID    string
	Label string // what the library shows for it: its friendly name, else its filename
	Has   bool
}

// VideoPosters lists every video in a unit with whether it has a poster, in
// one query: the Files page needs both the set that have one (to show them)
// and the list that don't (to offer making them).
//
// "Video" is by the content type the server recorded from the file's bytes at
// upload — never by what the uploader claimed (see sniffContentType).
func VideoPosters(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]VideoPoster, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, COALESCE(NULLIF(display_name, ''), filename), has_poster
		FROM files
		WHERE unit_id = $1 AND content_type LIKE 'video/%'
		ORDER BY created_at DESC
	`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []VideoPoster
	for rows.Next() {
		var v VideoPoster
		if err := rows.Scan(&v.ID, &v.Label, &v.Has); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
