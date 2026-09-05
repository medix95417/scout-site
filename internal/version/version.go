// Package version holds this build's release version, shown in the site
// footer (see internal/web's baseData.Version).
package version

// Version is this build's release version, shown in the site footer.
//
// Don't edit this by hand. The "Release" GitHub Actions workflow — or
// scripts/release.sh, which it runs — rewrites this line, dates the
// CHANGELOG's [Unreleased] section, commits, and tags. The next number
// comes from the labels of the pull requests merged since the last tag
// (see CHANGELOG.md's versioning note).
//
// Still a plain constant rather than something injected at build time
// via ldflags: the deploy step is `git pull` + `docker compose up -d
// --build` (see DEPLOY.md), so nothing is passing linker flags in. A
// constant that lives next to the changelog entry it corresponds to,
// and travels with the commit that set it, is the simplest thing that
// works for that.
const Version = "2.8.0"
