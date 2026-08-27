// Package version holds this build's release version, shown in the site
// footer (see internal/web's baseData.Version).
package version

// Version is this build's release version — bump alongside a new
// CHANGELOG.md entry when cutting a release (see CHANGELOG.md's own
// versioning note). Deliberately a plain constant rather than something
// injected at build time via ldflags: this project has no CI-driven
// release pipeline (DEPLOY.md's deploy step is `git pull` +
// `docker compose up -d --build`), so there's no build step to inject
// anything into — a constant kept next to the changelog entry it
// corresponds to is the simplest thing that could work.
const Version = "1.8.0"
