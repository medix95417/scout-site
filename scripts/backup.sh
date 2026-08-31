#!/usr/bin/env bash
#
# Back up this site: the database and the photos, as two separate
# encrypted files.
#
#   scripts/backup.sh [output-directory]
#
# Two artifacts rather than one, deliberately. They have different sizes,
# different change rates and different restore paths — the database is
# small and changes constantly, the photo library is large and mostly
# append-only — and being able to restore one without the other is worth
# more than the tidiness of a single file.
#
# Both are encrypted. The database dump in particular holds password
# hashes, home addresses, phone numbers and children's names; it is the
# most sensitive thing this system can produce, and it ends up sitting on
# whatever disk you point this at.
#
# Where each half comes from:
#
#   Database — pg_dump, run inside the postgres container that already
#   has it. The real tool, not a reimplementation: this is the one path
#   you'll only ever exercise in an emergency, so it should be the boring
#   well-tested one.
#
#   Photos — the app's own -backup-files, because they live in
#   S3-compatible object storage that nothing on the host necessarily
#   knows how to reach, while the app already holds the credentials.
#
# Safe to run from cron; see DEPLOY.md for an entry.
set -euo pipefail

cd "$(dirname "$0")/.."

OUT_DIR="${1:-./backups}"
PASSPHRASE_FILE="${BACKUP_PASSPHRASE_FILE:-./backup-passphrase}"
KEEP="${BACKUP_KEEP:-14}"

die() { echo "backup: $*" >&2; exit 1; }

command -v gpg >/dev/null || die "gpg isn't installed. On Debian/Ubuntu: apt-get install gnupg"
command -v docker >/dev/null || die "docker isn't installed, or isn't on this user's PATH"

# The passphrase lives in a file, never in an argument: anything on a
# command line is visible to every other process on the box via ps.
[ -f "$PASSPHRASE_FILE" ] || die "no passphrase file at $PASSPHRASE_FILE.
Create one with:  openssl rand -base64 48 > $PASSPHRASE_FILE && chmod 600 $PASSPHRASE_FILE
Then keep a copy somewhere that is NOT this server — a backup you can't decrypt is not a backup."

# 600 or tighter. A passphrase any local account can read protects
# nothing once someone is on the box.
perms="$(stat -c '%a' "$PASSPHRASE_FILE")"
case "$perms" in
  600|400) ;;
  *) die "$PASSPHRASE_FILE is mode $perms — it must not be readable by other users. Fix with: chmod 600 $PASSPHRASE_FILE" ;;
esac
[ -s "$PASSPHRASE_FILE" ] || die "$PASSPHRASE_FILE is empty"

mkdir -p "$OUT_DIR"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
db_file="$OUT_DIR/scoutsite-db-$stamp.sql.gz.gpg"
files_file="$OUT_DIR/scoutsite-photos-$stamp.tar.gpg"

encrypt() {
  gpg --batch --yes --quiet \
      --symmetric --cipher-algo AES256 \
      --passphrase-file "$PASSPHRASE_FILE" \
      --output "$1"
}

# --- Database ---------------------------------------------------------
#
# -T because there's no terminal in cron. --clean --if-exists so the dump
# can be replayed over an existing database; restore.sh normally targets
# an empty one, but a dump that can only restore onto a blank slate is
# half a backup.
echo "backup: dumping the database…" >&2
if ! docker compose exec -T db pg_dump \
      --username=scoutsite --dbname=scoutsite \
      --clean --if-exists --no-owner --no-privileges \
    | gzip -9 \
    | encrypt "$db_file"; then
  rm -f "$db_file"
  die "the database dump failed — is the 'db' service running? (docker compose ps)"
fi

# --- Photos -----------------------------------------------------------
#
# Runs a one-off app container rather than exec'ing the running one: the
# running server has no shell (distroless) and this needs its own
# argv. Storage being unconfigured is not a failure — plenty of installs
# have no object storage — but it must be said out loud rather than
# silently producing nothing.
echo "backup: archiving photos and documents…" >&2
if docker compose run --rm --no-TTY app -backup-files 2>/tmp/scoutsite-backup-files.$$ | encrypt "$files_file"; then
  cat /tmp/scoutsite-backup-files.$$ >&2 || true
else
  rm -f "$files_file"
  if grep -q "storage isn't configured" /tmp/scoutsite-backup-files.$$ 2>/dev/null; then
    echo "backup: file storage isn't configured, so there are no photos to archive — database only." >&2
  else
    cat /tmp/scoutsite-backup-files.$$ >&2 || true
    rm -f /tmp/scoutsite-backup-files.$$
    die "the photo archive failed"
  fi
fi
rm -f /tmp/scoutsite-backup-files.$$

# --- Manifest ---------------------------------------------------------
#
# Checksums of the encrypted files, so restore.sh can tell a truncated or
# corrupted transfer from a wrong passphrase — two failures that would
# otherwise look identical at 2am.
( cd "$OUT_DIR" && sha256sum "$(basename "$db_file")" $( [ -f "$files_file" ] && basename "$files_file" ) \
    > "scoutsite-$stamp.sha256" )

# --- Prune ------------------------------------------------------------
if [ "$KEEP" -gt 0 ]; then
  ls -1t "$OUT_DIR"/scoutsite-db-*.sql.gz.gpg 2>/dev/null | tail -n "+$((KEEP+1))" | while read -r old; do
    base="${old%%.sql.gz.gpg}"; base="${base/scoutsite-db-/scoutsite-}"
    rm -f "$old" "${base}"*.tar.gpg "${base}.sha256" 2>/dev/null || true
  done
  ls -1t "$OUT_DIR"/scoutsite-photos-*.tar.gpg 2>/dev/null | tail -n "+$((KEEP+1))" | xargs -r rm -f
  ls -1t "$OUT_DIR"/scoutsite-*.sha256 2>/dev/null | tail -n "+$((KEEP+1))" | xargs -r rm -f
fi

echo "backup: done." >&2
ls -lh "$OUT_DIR"/*"$stamp"* >&2
echo >&2
echo "backup: these files are only useful with $PASSPHRASE_FILE. Copy both off this server." >&2
