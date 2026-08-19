# Deploying to a VPS — cold server to running site

This is the full path from a brand-new VPS with nothing on it to
`https://troop.47-yonkers.org` and `https://pack.47-yonkers.org` serving
real traffic. Everything runs from the single `docker-compose.yml` in this
repo — the same file used for local development, just pointed at
production settings via `.env` (see the comments at the top of
`docker-compose.yml`). There is no second compose file to maintain.

Written for Ubuntu 22.04/24.04, the most common VPS default. The Docker
install step differs slightly on other distros (Debian, Rocky, etc.) —
the rest of this guide is the same regardless.

## 0. What you need before starting

- A VPS with a public IP address and root or sudo SSH access.
- Access to DNS management for `47-yonkers.org` (your registrar or DNS
  provider's dashboard).
- This repo's code, either via `git clone` (recommended — makes future
  updates a `git pull`) or the zip you already have.

## 1. Point DNS at the server (do this first — it takes time to propagate)

In your DNS provider's dashboard, create two **A records**:

| Host | Type | Value |
|---|---|---|
| `troop.47-yonkers.org` | A | your VPS's public IP |
| `pack.47-yonkers.org` | A | your VPS's public IP |

Kick this off now since propagation can take anywhere from a couple of
minutes to an hour. You can check whether it's resolved with:

```bash
dig +short troop.47-yonkers.org
dig +short pack.47-yonkers.org
```

Both should print your VPS's IP once propagated. The rest of this guide
doesn't strictly require DNS to have propagated yet, but Caddy (step 6)
won't be able to get HTTPS certificates until it has.

## 2. Initial server setup

SSH into the VPS as root (or a sudo user), then:

```bash
sudo apt-get update && sudo apt-get upgrade -y
```

(Optional but recommended, if you're currently SSHed in as root)
create a non-root user with sudo access so you're not operating as root
day-to-day:

```bash
sudo adduser deploy
sudo usermod -aG sudo deploy
# log out and back in as `deploy` for the rest of this guide
```

Set up a firewall so only SSH, HTTP, and HTTPS are reachable from the
internet — Postgres and the app's internal port never need to be, and
`docker-compose.yml` already binds them to localhost only, but a firewall
is good defense-in-depth on top of that:

```bash
sudo apt-get install -y ufw
sudo ufw allow OpenSSH
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
```

## 3. Install Docker

```bash
sudo apt-get install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
  sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Let your user run `docker` without `sudo` each time:

```bash
sudo usermod -aG docker $USER
# log out and back in for this to take effect
```

Verify:

```bash
docker --version
docker compose version
```

Docker's systemd service is enabled automatically by this install, so the
whole stack will come back up on its own after a server reboot — nothing
extra to configure there.

## 4. Get the code onto the server

**Option A — git clone (recommended, makes future updates a `git pull`):**

```bash
git clone https://github.com/medix95417/scout-site.git
cd scout-site
```

It's a private repo, so this will prompt for credentials — use your
GitHub username and a personal access token as the password (GitHub no
longer accepts your account password here). If you'd rather not enter a
token each time, this is actually the right place to set up an SSH deploy
key (unlike the sandbox this was originally built in, a VPS has normal
outbound SSH access): generate a keypair with `ssh-keygen -t ed25519`,
add the printed public key to the repo's Settings → Deploy keys on
GitHub (read-only access is enough for pulling), then clone with the
`git@github.com:medix95417/scout-site.git` SSH URL instead.

**Option B — upload the zip:**

```bash
# from your own machine:
scp scout-site-phase1.zip deploy@YOUR_VPS_IP:~
# then on the VPS:
unzip scout-site-phase1.zip
cd scout-site
```

## 5. Configure the environment

```bash
cp .env.example .env
nano .env   # or vim, whatever you're comfortable with
```

Set these for production:

- `SESSION_SECRET` — generate with `openssl rand -base64 32`.
- `POSTGRES_PASSWORD` — generate with `openssl rand -base64 24`. Don't
  leave this at the `scoutsite` default on a public server. Any character
  the generator produces is safe here — the app builds its own database
  connection string with proper escaping rather than requiring a
  URL-safe password.
- `COOKIE_DOMAIN=.47-yonkers.org` — this is what makes single sign-on
  actually work across both subdomains (see
  `scout-website-architecture-phase1.md` Section 2). Leaving it blank
  (the local-dev default) means each subdomain would need a separate
  login, which defeats the point.
- `TROOP_HOST` / `PACK_HOST` — leave as-is unless you're using different
  hostnames than `troop.47-yonkers.org` / `pack.47-yonkers.org`.
- `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`,
  `SMTP_TLS_MODE` — optional, but needed for self-service "forgot
  password" emails and event reminder emails to actually send (see
  README.md "Email"). Any normal SMTP provider works — a Google
  Workspace account, Mailgun, Postmark, SES SMTP, etc. Leave `SMTP_HOST`
  blank to skip email for now and add it later; nothing else in the site
  depends on it. Once the site is running, `SMTP_HOST`/`SMTP_PORT`/
  `SMTP_USERNAME`/`SMTP_FROM` can also be set or changed from
  `/admin/settings` (super_admin only) without touching this file or
  restarting — a value set there overrides the matching environment
  variable, and clearing the field on that page falls back to it again.
  `SMTP_PASSWORD` is the one exception: it's only ever read from this
  file, never from the database, so it still needs a restart to change
  (see README.md's "Also added post-Phase-2" section on why).
- `S3_ACCESS_KEY` / `S3_SECRET_KEY` — generate with `openssl rand -base64 24`
  each. These back the bundled `minio` service (file library + event
  photos — see README.md "Files"); don't leave them at the local-dev
  defaults on a public server, same reasoning as `POSTGRES_PASSWORD`
  above. Leave `S3_ENDPOINT`/`S3_BUCKET`/`S3_USE_SSL` as-is unless you're
  pointing at a real cloud bucket instead of the bundled MinIO (see
  `.env.example`'s comment on that).

Lock the file down since it now holds real secrets:

```bash
chmod 600 .env
```

## 6. Bring the stack up

```bash
docker compose up -d --build
```

First run takes a minute or two — it's compiling the Go binary inside the
`app` image and pulling the Postgres and Caddy images. Watch it come up:

```bash
docker compose logs -f caddy
```

Once DNS has propagated, Caddy automatically requests and installs Let's
Encrypt certificates for both hostnames — you'll see that happen in the
logs. Ctrl-C to stop following once you see it's issued certificates (or
it's fine to just leave it running in another terminal).

## 7. Initialize the database

```bash
docker compose run --rm app -migrate
docker compose run --rm app -seed
docker compose run --rm -e ADMIN_EMAIL=you@example.com -e ADMIN_PASSWORD=a-real-password \
  -e ADMIN_FIRST_NAME=Your -e ADMIN_LAST_NAME=Name app -bootstrap-admin
```

Use a real password for `ADMIN_PASSWORD` here — this is your actual login
going forward, not a placeholder.

**Testing on a staging server instead of going straight to real data?**
`docker compose run --rm app -seed-demo` creates a full set of obviously-fake
test logins (one per role) plus sample calendar/ledger activity — see
`DEMO_DATA.md`. Skip this on a real production server; it's meant for
kicking the tires before real families are in the system.

## 8. Verify

Visit `https://troop.47-yonkers.org` and `https://pack.47-yonkers.org` —
both should load over a valid HTTPS certificate (check for the padlock).
Log in with the admin credentials from step 7 on one subdomain, then load
the other without logging in again — if you see yourself logged in on
both, single sign-on via the shared cookie domain is working. Check
`/roster`, `/calendar`, and `/audit` load correctly.

## Ongoing operations

**Deploying updates**, once you've made changes (locally or by pulling
new code from GitHub):

```bash
git pull            # if using Option A above
docker compose up -d --build
```

This rebuilds only what changed — `db` and `caddy` aren't touched unless
you edited `docker-compose.yml` or `Caddyfile`.

**Backups** — Postgres holds everything (roster, calendar, audit log,
and eventually Phase 2's fund ledger), so this matters:

```bash
docker compose exec -T db pg_dump -U scoutsite scoutsite > backup-$(date +%F).sql
```

Automate this with a daily cron job, and — importantly — copy the
resulting file *off* this VPS somewhere (another machine, cloud storage,
etc.). A backup that only lives on the same server it's backing up
doesn't protect you if that server is lost.

Uploaded files and event photos live separately, in the `minio` service's
`minio_data` volume (Postgres only stores their metadata — see
`internal/storage`) — back that up too, e.g.:

```bash
docker compose exec -T minio tar -cf - -C /data . > files-backup-$(date +%F).tar
```

Same rule applies: copy it off-server, not just onto this same disk.

**Event reminder emails** — if you've configured `SMTP_HOST` (step 5),
reminder emails don't send themselves; something needs to run the
`-send-event-reminders` command periodically. It's safe to run as often
as you like (each RSVP'd member is only ever emailed once per event, no
matter how many times the command runs), so an hourly cron job is a
reasonable default:

```bash
crontab -e
```

Add a line like:

```
0 * * * *  cd /home/deploy/scout-site && /usr/bin/docker compose run --rm app -send-event-reminders >> /home/deploy/reminders.log 2>&1
```

Adjust the path to wherever you cloned/unzipped the repo. This reuses the
same `.env` the rest of the stack reads, so no separate configuration is
needed — it just needs `SMTP_HOST` to be set for anything to actually go
out. If `SMTP_HOST` is left blank, running this command is harmless; it
logs a warning and exits without sending anything.

**Adding a unit later (or fixing "my account has roster access on one
subdomain but not the other")** — `-bootstrap-admin` only grants the
`super_admin` role in units that already exist in the database at the
moment it runs. If a unit was added afterward (or role assignments
otherwise ended up uneven between the Troop and Pack — e.g. an account
picked up a leader role via `/admin/roster` on one subdomain but never
got one on the other), that account's "Manage Roster" link simply won't
show up on the subdomain it has no role in — this is expected, not a
bug, since role assignments are per-unit even though login is shared.
Fix it with:

```bash
docker compose run --rm \
  -e GRANT_EMAIL=you@example.com \
  -e GRANT_UNIT_SLUG=pack-47 \
  -e GRANT_ROLE=cubmaster \
  app -grant-role
```

`GRANT_UNIT_SLUG` is the `slug` column in the `units` table — `troop-47`
and `pack-47` for the two units `-seed` creates. `GRANT_ROLE` is any role
from the `member_role` enum (`super_admin`, `cubmaster`, `den_leader`,
`scoutmaster`, `assistant_scoutmaster`, `senior_patrol_leader`,
`patrol_leader`, `treasurer`, `parent`, `scout`) — for getting someone's
first foothold in a unit, `super_admin` (unit-wide access to everything)
or the unit's top leadership role (`cubmaster` for the Pack, `scoutmaster`
for the Troop) are the usual choices; use `treasurer` specifically to
give someone Phase 2 fund-accounting access (see `PHASE2_TREASURY.md`) —
note that role requires two-factor login setup. It's safe to re-run; granting a role
someone already has is a no-op. Once an account has any unit-wide
leadership role in a unit, everyday role changes for that unit (adding
other leaders, Den Leaders, etc.) can go back through `/admin/roster` —
this command is only needed to get that first role in.

**Logs:**

```bash
docker compose logs -f app     # application logs
docker compose logs -f caddy   # reverse proxy / certificate logs
docker compose logs -f db      # database logs
```

**Restarting:**

```bash
docker compose restart          # restart everything
docker compose restart app      # just the app, e.g. after an env var change
```

**Stopping everything** (data persists in the `db_data` volume):

```bash
docker compose down
```

## Security checklist

- [ ] `.env` has a real `SESSION_SECRET`, `POSTGRES_PASSWORD`, `S3_ACCESS_KEY`, and `S3_SECRET_KEY` (not the local-dev defaults).
- [ ] `.env` is `chmod 600` and never committed to git (already in `.gitignore`).
- [ ] `COOKIE_DOMAIN=.47-yonkers.org` is set.
- [ ] `ufw` (or your firewall of choice) only allows SSH, 80, and 443 from the internet.
- [ ] Backups are automated and copied off-server.
- [ ] The admin password from step 7 is a real, unique password.
- [ ] If email is configured, `-send-event-reminders` is on a cron job (see "Ongoing operations" above).
