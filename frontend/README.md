# Locally served assets

`npm ci --ignore-scripts && npm run build:assets` compiles Tailwind 3.4.19 and
copies the existing pinned htmx 2.0.3, Quill 2.0.2 and QRious 4.0.2 browser
bundles from npm distributions, with licenses. package-lock.json verifies
package integrity. SHA256SUMS records the generated output. No browser contacts
a JavaScript CDN. Every script, including local scripts, requires a CSP nonce.

Keep complete class names in templates or Go helpers; Tailwind scans both,
excluding Go tests. Do not construct partial class names at runtime. The CSS
version remains 3.x to preserve the existing site's utility semantics.

Commit generated vendor assets for direct Go builds. Docker regenerates them;
CI regenerates and compares them to the committed versions. Upgrade packages
explicitly, review their changes/licenses, and regenerate the files and hashes.

References:
- https://v3.tailwindcss.com/docs/installation
- https://v3.tailwindcss.com/docs/content-configuration
