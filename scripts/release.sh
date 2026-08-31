#!/usr/bin/env bash
#
# Cut a release: bump internal/version, turn the CHANGELOG's [Unreleased]
# section into a dated one, commit, and tag.
#
#   scripts/release.sh patch|minor|major
#   scripts/release.sh                     # infer the bump from merged PR labels
#
# Inferring reads the labels of every PR merged since the last tag and
# takes the largest bump any of them asks for:
#
#   release:major  -> 2.1.0 -> 3.0.0
#   release:minor  -> 2.1.0 -> 2.2.0
#   release:patch  -> 2.1.0 -> 2.1.1   (also the default when unlabelled)
#
# Inferring needs the gh CLI and network access; passing the bump
# explicitly needs neither, which is what makes this runnable by hand
# when a release has to happen without CI.
#
# Deliberately does NOT push. Cutting a release and publishing it are
# separate decisions, and this script is run from a workflow that has its
# own opinion about how the result gets to main.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION_FILE=internal/version/version.go
CHANGELOG=CHANGELOG.md

current=$(grep -oP 'const Version = "\K[^"]+' "$VERSION_FILE")
IFS=. read -r major minor patch <<<"$current"

bump="${1:-}"

if [[ -z "$bump" ]]; then
  # No explicit bump: ask GitHub what the merged PRs since the last tag
  # were labelled. An unlabelled release is a patch — the safe default,
  # since claiming a bigger bump than the work justifies is worse than
  # the reverse.
  last_tag=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
  if [[ -n "$last_tag" ]]; then
    since=$(git log -1 --format=%aI "$last_tag")
  else
    since=$(git log --reverse -1 --format=%aI)
  fi

  labels=$(gh pr list --state merged --limit 100 \
      --json mergedAt,labels \
      --jq "[.[] | select(.mergedAt > \"$since\") | .labels[].name] | .[]" 2>/dev/null || echo "")

  bump=patch
  if grep -qx 'release:minor' <<<"$labels"; then bump=minor; fi
  if grep -qx 'release:major' <<<"$labels"; then bump=major; fi
  echo "inferred bump from merged PR labels since ${last_tag:-the beginning}: $bump"
fi

case "$bump" in
  major) major=$((major + 1)); minor=0; patch=0 ;;
  minor) minor=$((minor + 1)); patch=0 ;;
  patch) patch=$((patch + 1)) ;;
  *) echo "usage: $0 [patch|minor|major]" >&2; exit 2 ;;
esac

next="$major.$minor.$patch"
today=$(date -u +%Y-%m-%d)

if ! grep -q '^## \[Unreleased\]' "$CHANGELOG"; then
  echo "error: no '## [Unreleased]' section in $CHANGELOG — nothing to release" >&2
  exit 1
fi

# Refuse to cut an empty release. An [Unreleased] heading immediately
# followed by the previous version's heading means nothing has landed,
# and a version with no entries tells a reader nothing.
if awk '/^## \[Unreleased\]/{f=1;next} /^## \[/{exit} f&&NF{found=1} END{exit !found}' "$CHANGELOG"; then
  :
else
  echo "error: the [Unreleased] section is empty — nothing to release" >&2
  exit 1
fi

# Refuse to cut a release whose notes have the same heading twice.
#
# Each merged change tends to prepend its own "### Added"/"### Fixed"
# block to [Unreleased], so by release time the section can carry two
# Addeds and a Changed in the middle. Harmless in a diff and invisible
# until the moment it's dated and permanent, which is exactly when it
# stops being easy to fix — this has now happened twice. Cheaper to
# catch here, where the fix is to merge two blocks by hand and re-run.
dupes=$(awk '/^## \[Unreleased\]/{f=1;next} /^## \[/{exit} f&&/^### /{print}' "$CHANGELOG" \
        | sed -E 's/ \(continued\)$//' | sort | uniq -d)
if [ -n "$dupes" ]; then
  echo "error: [Unreleased] has the same heading more than once:" >&2
  echo "$dupes" | sed 's/^/  /' >&2
  echo "Merge those blocks into one section each, then re-run. A released" >&2
  echo "changelog listing \"Added\" twice is confusing and can't be tidied" >&2
  echo "without editing a dated section after the fact." >&2
  exit 1
fi

echo "$current -> $next ($bump)"

sed -i "s/^const Version = \"$current\"\$/const Version = \"$next\"/" "$VERSION_FILE"

# Leave a fresh, empty [Unreleased] above the newly dated section so the
# next change has somewhere to go.
sed -i "0,/^## \[Unreleased\]\$/s//## [Unreleased]\n\n## [$next] — $today/" "$CHANGELOG"

gofmt -l . >/dev/null
go build ./... >/dev/null

git add "$VERSION_FILE" "$CHANGELOG"
git commit -q -m "Release $next"
git tag -a "v$next" -m "Release $next"

echo "committed and tagged v$next (not pushed)"
