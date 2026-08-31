#!/usr/bin/env bash
#
# Rebuild this site from the files scripts/backup.sh produced.
#
#   scripts/restore.sh <db-backup.sql.gz.gpg> [photos-backup.tar.gpg]
#
# Restore is a command you run on the box, not a page in the admin UI.
# That's deliberate: an upload endpoint able to replace the database is
# the most powerful route a web application can expose, and anyone doing
# a real recovery already has shell access — the site is down. Keeping it
# here means a compromised admin account can't hand over or overwrite
# everything.
#
# By default it refuses to overwrite a database that already holds data,
# because the ordinary case is recovering onto a clean box or standing up
# a copy. Pass --force when you genuinely mean to replace a live site.
set -euo pipefail

cd "$(dirname "$0")/.."

PASSPHRASE_FILE="${BACKUP_PASSPHRASE_FILE:-./backup-passphrase}"
FORCE=0
ARGS=()
for arg in "$@"; do
  case "$arg" in
    --force) FORCE=1 ;;
    *) ARGS+=("$arg") ;;
  esac
done
set -- "${ARGS[@]:-}"

die() { echo "restore: $*" >&2; exit 1; }

DB_BACKUP="${1:-}"
FILES_BACKUP="${2:-}"

[ -n "$DB_BACKUP" ] || die "usage: scripts/restore.sh <db-backup.sql.gz.gpg> [photos-backup.tar.gpg] [--force]"
[ -f "$DB_BACKUP" ] || die "no such file: $DB_BACKUP"
[ -z "$FILES_BACKUP" ] || [ -f "$FILES_BACKUP" ] || die "no such file: $FILES_BACKUP"
[ -f "$PASSPHRASE_FILE" ] || die "no passphrase file at $PASSPHRASE_FILE — without it these backups cannot be decrypted"

command -v gpg >/dev/null || die "gpg isn't installed. On Debian/Ubuntu: apt-get install gnupg"

# --- Verify before decrypting ----------------------------------------
#
# Checked first so a truncated download fails as "this file is damaged"
# rather than as a decryption error, which reads like a wrong passphrase
# and sends you looking in the wrong place.
manifest="$(dirname "$DB_BACKUP")/$(basename "$DB_BACKUP" | sed -E 's/^scoutsite-db-(.*)\.sql\.gz\.gpg$/scoutsite-\1.sha256/')"
if [ -f "$manifest" ]; then
  echo "restore: checking $(basename "$manifest")…" >&2
  ( cd "$(dirname "$DB_BACKUP")" && sha256sum --quiet --check "$(basename "$manifest")" ) \
    || die "checksum mismatch — these files are damaged or incomplete. Fetch them again rather than restoring from them."
else
  echo "restore: no .sha256 manifest alongside the backup; skipping the integrity check." >&2
fi

decrypt() { gpg --batch --quiet --decrypt --passphrase-file "$PASSPHRASE_FILE" "$1"; }

# --- Refuse to clobber a live database unless told to -----------------
docker compose up -d db >/dev/null
for _ in $(seq 30); do
  docker compose exec -T db pg_isready -U scoutsite >/dev/null 2>&1 && break
  sleep 1
done

existing="$(docker compose exec -T db psql -U scoutsite -d scoutsite -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'" 2>/dev/null || echo 0)"
if [ "${existing:-0}" -gt 0 ] && [ "$FORCE" -ne 1 ]; then
  die "this database already has $existing tables.

Restoring would replace everything in it. If that's what you want, re-run with --force.
If you meant to recover onto a clean machine, you're pointed at the wrong one."
fi

# The app writes to the database on startup (migrations), so it must not
# be running while the dump is being replayed.
echo "restore: stopping the app…" >&2
docker compose stop app >/dev/null 2>&1 || true

echo "restore: restoring the database…" >&2
if ! decrypt "$DB_BACKUP" | gunzip | docker compose exec -T db psql \
      --username=scoutsite --dbname=scoutsite --quiet \
      --set ON_ERROR_STOP=on >/dev/null; then
  die "the database restore failed. The app is still stopped; the database may be half-restored.
Start again from a known-good backup, or drop and recreate the database first:
  docker compose exec -T db psql -U scoutsite -d postgres -c 'DROP DATABASE scoutsite' -c 'CREATE DATABASE scoutsite'"
fi

# --- Photos -----------------------------------------------------------
if [ -n "$FILES_BACKUP" ]; then
  echo "restore: restoring photos and documents…" >&2
  # Additive by design: this replaces objects at matching keys and never
  # deletes anything the archive doesn't mention.
  decrypt "$FILES_BACKUP" | docker compose run --rm --no-TTY -T app -restore-files \
    || die "the photo restore failed. The database is restored; you can re-run just this half."
else
  echo "restore: no photo archive given — database only." >&2
fi

echo "restore: starting the app…" >&2
docker compose up -d app >/dev/null

echo >&2
echo "restore: done. The app applies any newer migrations automatically on startup," >&2
echo "so a backup from an older version comes forward on its own." >&2
echo "Check it: docker compose logs -f app" >&2
