// Copy only pinned browser distributions and their licenses into the Go embed.
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const target = 'internal/web/static/vendor';
fs.mkdirSync(target, { recursive: true });
const assets = {
  'htmx-2.0.3.min.js': 'htmx.org/dist/htmx.min.js',
  'htmx-LICENSE.txt': 'htmx.org/LICENSE',
  'quill-2.0.2.js': 'quill/dist/quill.js',
  'quill-2.0.2.snow.css': 'quill/dist/quill.snow.css',
  'quill-LICENSE.txt': 'quill/LICENSE',
  'quill-bundle-LICENSE.txt': 'quill/dist/quill.js.LICENSE.txt',
  'qrious-4.0.2.min.js': 'qrious/dist/qrious.min.js',
  'qrious-LICENSE.txt': 'qrious/LICENSE.md',
  'tailwind-LICENSE.txt': 'tailwindcss/LICENSE',
};
for (const [name, source] of Object.entries(assets)) {
  fs.copyFileSync(path.join('node_modules', source), path.join(target, name));
}
const hashes = Object.keys(assets).concat('tailwind.css').sort().map(name =>
  crypto.createHash('sha256').update(fs.readFileSync(path.join(target, name))).digest('hex') + '  ' + name
);
fs.writeFileSync(path.join(target, 'SHA256SUMS'), hashes.join('\n') + '\n');
