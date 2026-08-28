// Package advancement records rank and merit badge history — see
// internal/db/migrations/0001_init.sql's advancement_records table
// comment: Scoutbook is the eventual system of record for this, and a
// bulk import (BulkImport below) mirrors what its CSV exports would
// produce, but this package also supports a leader recording one earned
// rank/badge directly (CreateRecord, source = "manual") for units that
// don't export from Scoutbook regularly — a Scout who just earned
// Tenderfoot at tonight's Court of Honor shouldn't have to wait for the
// next Scoutbook export to show up on the site.
package advancement

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/47-yonkers/scout-site/internal/audit"
)

// Record is one earned rank or merit badge.
type Record struct {
	ID         string
	MemberID   string
	UnitID     string
	RecordType string // "rank" | "badge"
	Name       string
	EarnedDate *time.Time
	Source     string // "manual" | "scoutbook_csv"
	ImportedAt time.Time
}

// ErrNotFound covers "no such record" and "belongs to a different unit"
// alike — same posture every other unit-scoped lookup in this codebase
// takes.
var ErrNotFound = errors.New("advancement: record not found in this unit")

func validRecordType(t string) bool {
	return t == "rank" || t == "badge"
}

const columns = `id, member_id, unit_id, record_type, name, earned_date, source, imported_at`

func scan(row interface{ Scan(dest ...any) error }) (Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.MemberID, &r.UnitID, &r.RecordType, &r.Name, &r.EarnedDate, &r.Source, &r.ImportedAt)
	return r, err
}

// CreateRecord adds one rank/badge record for a member.
func CreateRecord(ctx context.Context, pool *pgxpool.Pool, memberID, unitID, recordType, name string, earnedDate *time.Time, source, actorID string) (Record, error) {
	if !validRecordType(recordType) {
		return Record{}, fmt.Errorf("advancement: %q is not a valid record type (must be \"rank\" or \"badge\")", recordType)
	}
	if name == "" {
		return Record{}, fmt.Errorf("advancement: name is required")
	}

	r, err := scan(pool.QueryRow(ctx, `
		INSERT INTO advancement_records (member_id, unit_id, record_type, name, earned_date, source)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+columns,
		memberID, unitID, recordType, name, earnedDate, source))
	if err != nil {
		return Record{}, err
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "advancement_record",
		EntityID:   r.ID,
		ActorID:    &actorID,
		Action:     "create",
		After:      r,
	})
	return r, nil
}

// Deliberately absent: a ListForMember(memberID) with no unit argument.
// It existed, had no callers, and was the one query in this package that
// would happily return records for a member of a different unit — a trap
// for whoever eventually wired it to a handler taking a member id from a
// request. Anything needing one member's history should filter
// ListForUnit's result, so the unit boundary is never optional.

// ListForUnit lists every advancement record in a unit, most recently
// earned first — the leader management view and the members-only
// /advancement page both start from this.
func ListForUnit(ctx context.Context, pool *pgxpool.Pool, unitID string) ([]Record, error) {
	return queryRecords(ctx, pool, `WHERE unit_id = $1 ORDER BY earned_date DESC NULLS LAST, imported_at DESC`, unitID)
}

func queryRecords(ctx context.Context, pool *pgxpool.Pool, whereAndOrder string, args ...any) ([]Record, error) {
	rows, err := pool.Query(ctx, `SELECT `+columns+` FROM advancement_records `+whereAndOrder, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []Record
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// DeleteRecord removes one record — for correcting a manual entry typo
// or a bad import row. Scoped to a unit the same way every other
// unit-scoped write in this codebase re-validates ownership before
// touching a row.
func DeleteRecord(ctx context.Context, pool *pgxpool.Pool, id, unitID, actorID string) error {
	var before Record
	before, err := scan(pool.QueryRow(ctx, `SELECT `+columns+` FROM advancement_records WHERE id = $1 AND unit_id = $2`, id, unitID))
	if err != nil {
		return ErrNotFound
	}

	tag, err := pool.Exec(ctx, `DELETE FROM advancement_records WHERE id = $1 AND unit_id = $2`, id, unitID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	audit.Log(ctx, pool, audit.Entry{
		EntityType: "advancement_record",
		EntityID:   id,
		ActorID:    &actorID,
		Action:     "delete",
		Before:     before,
	})
	return nil
}
