package web

// Moving already-stored files into the folders they would be filed under
// today (see files.NewStorageKey and uploadFolder).
//
// This is an operator-run, one-shot maintenance pass behind cmd/server's
// -rekey-files, not anything reachable over HTTP. Nothing in the app
// needs it: a key is an opaque address, the database is the authority on
// where each file lives, and an old flat key resolves exactly as well as
// a new foldered one. It exists so a bucket a person has to look at —
// while restoring a backup, or auditing what is stored — reads the same
// way throughout instead of being half tidy and half a heap of UUIDs.
//
// Three properties make it safe to run against a live site.
//
// NOTHING 404s, EVER. Object storage has no atomic rename, so a move is
// copy, repoint, delete. The original stays readable until the database
// points at the copy, and the copy is verified present before that
// happens — so at every instant, the key the database holds is a key
// that exists.
//
// A CRASH LOSES NOTHING. The target key is derived from the file's own
// row id (files.StorageKeyFor), so it is the same on every run. Die
// between copy and repoint and the row still names the original, which
// is still there; the next run recomputes the identical target and
// overwrites its own half-finished copy. Die between repoint and delete
// and the row names the copy, which exists; the original is left behind
// as an orphan, which is untidy and harmless. There is no ordering here
// where the row names something that is not there.
//
// IT REFUSES TO GUESS. A file whose object is missing from the bucket
// altogether is counted and reported, never repointed — a row pointing
// at a key that was never written is a pre-existing problem, and quietly
// giving it a new key would erase the evidence.

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/files"
	"github.com/47-yonkers/scout-site/internal/storage"
)

// RekeyMove is one file's move, as the dry run reports it.
type RekeyMove struct {
	FileID   string
	Filename string
	From     string
	To       string
}

// RekeyResult is what a pass did, or would do.
type RekeyResult struct {
	// Moves is every file that is not already where it belongs. On a dry
	// run it is the whole plan; on a real run it is what was moved.
	Moves []RekeyMove
	// Skipped is files already filed under the right folder — the count
	// a second run should report for everything.
	Skipped int
	// Missing is rows whose object is not in the bucket. Left untouched.
	Missing []RekeyMove
	// Failed is files whose move errored partway. Reported per file and
	// then skipped: one unreadable object must not abandon the rest.
	Failed int
}

// rekeyStore is the slice of storage.Store the pass uses, named so tests
// can substitute a bucket that lives in a map instead of over a network.
type rekeyStore interface {
	Copy(ctx context.Context, src, dst string) error
	Exists(ctx context.Context, key string) bool
	Delete(ctx context.Context, key string) error
}

// RekeyFiles moves every file into the folder an upload of it would land
// in today.
//
// With apply false it changes nothing and returns the plan, which is the
// default and the way to look before leaping. With apply true it does
// the work.
func RekeyFiles(ctx context.Context, pool *pgxpool.Pool, store *storage.Store, apply bool) (RekeyResult, error) {
	return rekeyFiles(ctx, pool, store, apply)
}

func rekeyFiles(ctx context.Context, pool *pgxpool.Pool, store rekeyStore, apply bool) (RekeyResult, error) {
	var result RekeyResult

	all, err := files.ListAll(ctx, pool)
	if err != nil {
		return result, err
	}

	for _, f := range all {
		eventIDs, err := files.EventIDsForFile(ctx, pool, f.ID)
		if err != nil {
			log.Printf("rekey: reading event links for %s (%s): %v", f.ID, f.Filename, err)
			result.Failed++
			continue
		}

		folder := folderFor(ctx, pool, f.UnitID, f.Category, eventIDs)
		want := files.StorageKeyFor(f.UnitID, folder, f.ID, f.Filename)
		if f.StorageKey == want {
			result.Skipped++
			continue
		}

		move := RekeyMove{FileID: f.ID, Filename: f.Filename, From: f.StorageKey, To: want}

		// A row whose object was never written is not this pass's problem
		// to solve, and repointing it would hide it.
		if !store.Exists(ctx, f.StorageKey) {
			result.Missing = append(result.Missing, move)
			continue
		}
		if !apply {
			result.Moves = append(result.Moves, move)
			continue
		}
		if err := moveOne(ctx, pool, store, f, want); err != nil {
			log.Printf("rekey: %s (%s): %v", f.ID, f.Filename, err)
			result.Failed++
			continue
		}
		result.Moves = append(result.Moves, move)
	}
	return result, nil
}

// moveOne performs the copy-verify-repoint-delete for one file. The
// order is the whole safety argument — see this file's header.
func moveOne(ctx context.Context, pool *pgxpool.Pool, store rekeyStore, f files.File, want string) error {
	if err := store.Copy(ctx, f.StorageKey, want); err != nil {
		return fmt.Errorf("copying: %w", err)
	}
	// Verified rather than assumed: a copy that silently wrote nothing,
	// followed by a repoint, is exactly how a file gets lost.
	if !store.Exists(ctx, want) {
		return fmt.Errorf("copy to %q reported success but nothing is there", want)
	}

	// The thumbnail is cached under a derived key (see
	// thumbStorageSuffix). Moved on a best-effort basis: FileThumbnail
	// regenerates a missing one on demand, so failing here costs one
	// resize later, not a broken image — and it must not abort a move
	// whose real work already succeeded.
	oldThumb, newThumb := f.StorageKey+thumbStorageSuffix, want+thumbStorageSuffix
	thumbMoved := false
	if store.Exists(ctx, oldThumb) {
		if err := store.Copy(ctx, oldThumb, newThumb); err != nil {
			log.Printf("rekey: moving thumbnail for %s: %v (it will be regenerated on demand)", f.ID, err)
		} else {
			thumbMoved = true
		}
	}

	// The point of no return, and the point after which nothing is
	// broken either way: the row now names an object that exists.
	if err := files.SetStorageKey(ctx, pool, f.ID, want); err != nil {
		return fmt.Errorf("repointing the database: %w", err)
	}

	// Only now is the original safe to remove. A failure here leaves an
	// orphan, which wastes space and breaks nothing.
	if err := store.Delete(ctx, f.StorageKey); err != nil {
		log.Printf("rekey: removing the old object %q: %v (it is now an orphan)", f.StorageKey, err)
	}
	if thumbMoved {
		if err := store.Delete(ctx, oldThumb); err != nil {
			log.Printf("rekey: removing the old thumbnail %q: %v", oldThumb, err)
		}
	}
	return nil
}
