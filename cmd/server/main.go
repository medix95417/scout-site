// Command server runs the Scouting unit website — Troop and Pack served
// from one binary, one database, tenant-resolved per request by hostname.
//
// Usage:
//
//	server                       run the HTTP server (default)
//	server -migrate              apply pending database migrations, then exit
//	server -seed                 insert the Troop/Pack unit rows, then exit
//	server -seed-demo            insert a full set of test logins/activity data (one per role), then exit — see DEMO_DATA.md
//	server -send-event-reminders email RSVP'd members of soon-starting events, then exit (run via cron)
//	server -refresh-calendar-feeds  re-fetch every subscribed external calendar, then exit (run via cron)
//	server -grant-role           grant an existing user a role in a unit, then exit (see DEPLOY.md "Adding a unit later")
//	server -backfill-thumbnails  generate a cached thumbnail for every image file that doesn't already have one, then exit (safe to re-run) — runs automatically in the background on every normal server startup too, this is only for running it on demand/synchronously
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/47-yonkers/scout-site/internal/auth"
	"github.com/47-yonkers/scout-site/internal/backup"
	"github.com/47-yonkers/scout-site/internal/bootstrap"
	"github.com/47-yonkers/scout-site/internal/calendar"
	"github.com/47-yonkers/scout-site/internal/config"
	"github.com/47-yonkers/scout-site/internal/csp"
	"github.com/47-yonkers/scout-site/internal/csrf"
	"github.com/47-yonkers/scout-site/internal/db"
	"github.com/47-yonkers/scout-site/internal/demoseed"
	"github.com/47-yonkers/scout-site/internal/mailer"
	"github.com/47-yonkers/scout-site/internal/reminders"
	"github.com/47-yonkers/scout-site/internal/storage"
	"github.com/47-yonkers/scout-site/internal/units"
	"github.com/47-yonkers/scout-site/internal/web"
)

func main() {
	migrateOnly := flag.Bool("migrate", false, "apply pending migrations and exit")
	seedOnly := flag.Bool("seed", false, "insert seed data (Troop/Pack units) and exit")
	seedDemo := flag.Bool("seed-demo", false, "insert a full set of test logins and activity data (one per role — see DEMO_DATA.md) and exit; safe to re-run, no-ops if demo data already exists")
	bootstrapAdmin := flag.Bool("bootstrap-admin", false, "create the first super-admin login from ADMIN_EMAIL/ADMIN_PASSWORD/ADMIN_FIRST_NAME/ADMIN_LAST_NAME env vars, then exit")
	sendEventReminders := flag.Bool("send-event-reminders", false, "email everyone RSVP'd yes/maybe to an event starting within REMINDER_WINDOW_HOURS (default 24), then exit — meant to be run periodically via cron, see DEPLOY.md")
	refreshCalendarFeeds := flag.Bool("refresh-calendar-feeds", false, "re-fetch every enabled external calendar subscription and update the events imported from it, then exit — meant to be run periodically via cron, see DEPLOY.md")
	grantRole := flag.Bool("grant-role", false, "grant an existing user (GRANT_EMAIL) a role (GRANT_ROLE) in a unit (GRANT_UNIT_SLUG) and exit — for giving an already-existing account a foothold in a unit -bootstrap-admin didn't reach, e.g. one added after -bootstrap-admin first ran")
	backfillThumbnails := flag.Bool("backfill-thumbnails", false, "generate a cached thumbnail for every image file that doesn't already have one, then exit. The normal server already does this automatically in the background on every startup — use this flag only to run it on demand and see the result immediately instead of waiting/checking logs (safe to re-run either way)")
	backupFiles := flag.Bool("backup-files", false, "write every stored photo and document to stdout as a tar archive, then exit — the photo half of a backup, since these live in object storage rather than the database. Meant to be piped straight into an encryption tool; see scripts/backup.sh")
	restoreFiles := flag.Bool("restore-files", false, "read a tar archive produced by -backup-files from stdin and put every object back, then exit. Additive: it replaces objects at matching keys and never deletes anything the archive doesn't mention. See scripts/restore.sh")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("db migrate: %v", err)
	}

	if *migrateOnly {
		log.Println("migrations applied, exiting (-migrate)")
		return
	}

	if *seedOnly {
		if err := db.Seed(ctx, pool); err != nil {
			log.Fatalf("db seed: %v", err)
		}
		return
	}

	if *seedDemo {
		summary, err := demoseed.Run(ctx, pool)
		if err != nil {
			log.Fatalf("seed-demo: %v", err)
		}
		printDemoSeedSummary(summary)
		return
	}

	if *bootstrapAdmin {
		in := bootstrap.AdminInput{
			Email:     os.Getenv("ADMIN_EMAIL"),
			Password:  os.Getenv("ADMIN_PASSWORD"),
			FirstName: os.Getenv("ADMIN_FIRST_NAME"),
			LastName:  os.Getenv("ADMIN_LAST_NAME"),
		}
		if in.Email == "" || in.Password == "" || in.FirstName == "" || in.LastName == "" {
			log.Fatal("bootstrap-admin: ADMIN_EMAIL, ADMIN_PASSWORD, ADMIN_FIRST_NAME, and ADMIN_LAST_NAME must all be set")
		}
		familyID, err := bootstrap.CreateAdmin(ctx, pool, in)
		if err != nil {
			log.Fatalf("bootstrap-admin: %v", err)
		}
		log.Printf("bootstrap-admin: created admin family %s for %s — you can now log in at either subdomain", familyID, in.Email)
		return
	}

	if *grantRole {
		email := os.Getenv("GRANT_EMAIL")
		unitSlug := os.Getenv("GRANT_UNIT_SLUG")
		role := os.Getenv("GRANT_ROLE")
		if email == "" || unitSlug == "" || role == "" {
			log.Fatal("grant-role: GRANT_EMAIL, GRANT_UNIT_SLUG, and GRANT_ROLE must all be set")
		}
		if err := bootstrap.GrantRole(ctx, pool, email, unitSlug, role); err != nil {
			log.Fatalf("grant-role: %v", err)
		}
		log.Printf("grant-role: granted %s the %s role in unit %s", email, role, unitSlug)
		return
	}

	// Constructed here — after every CLI-only early exit above, right
	// before the two remaining paths (-send-event-reminders and the main
	// server) that actually need to send mail — because mailer.New now
	// also takes the pool: SMTP host/port/username/from can be overridden
	// from /admin/settings (see internal/settings.TextSettings), resolved
	// fresh on every send rather than baked in once at startup, so an
	// admin's change there takes effect without a restart. SMTP_PASSWORD
	// stays environment-variable-only regardless (see
	// internal/mailer.Mailer.effective).
	mail := mailer.New(mailer.Config{
		Host:     cfg.SMTPHost,
		Port:     cfg.SMTPPort,
		Username: cfg.SMTPUsername,
		Password: cfg.SMTPPassword,
		From:     cfg.SMTPFrom,
		TLSMode:  cfg.SMTPTLSMode,

		Provider: cfg.MailProvider,
		APIToken: cfg.FastmailAPIToken,

		BulkPerMinute: cfg.MailBulkPerMinute,
	}, pool)
	if !mail.Enabled(ctx) {
		log.Println("email is not configured (no SMTP_HOST or MAIL_PROVIDER environment variable and no host set on /admin/settings) — password reset and event reminders will report a clear error instead of sending")
	}

	if *refreshCalendarFeeds {
		results, err := calendar.RefreshAllFeeds(ctx, pool)
		if err != nil {
			log.Fatalf("refresh-calendar-feeds: %v", err)
		}
		var failed int
		for _, res := range results {
			if res.Err != nil {
				failed++
				// Logged rather than fatal: one unreachable calendar
				// must not stop the others being refreshed, and the
				// failure is already recorded against the feed for the
				// admin page to show.
				log.Printf("refresh-calendar-feeds: %s: %v", res.FeedName, res.Err)
				continue
			}
			log.Printf("refresh-calendar-feeds: %s: %d new, %d updated, %d removed, %d held for review, %d ignored",
				res.FeedName, res.Created, res.Updated, res.Removed, res.Conflicts, res.Ignored)
		}
		log.Printf("refresh-calendar-feeds: %d feeds, %d failed", len(results), failed)
		return
	}

	if *sendEventReminders {
		result, err := reminders.Send(ctx, pool, mail, cfg.ReminderWindow)
		if err != nil {
			log.Fatalf("send-event-reminders: %v", err)
		}
		log.Printf("send-event-reminders: sent %d, failed %d (window %s)", result.Sent, result.Failed, cfg.ReminderWindow)
		return
	}

	// SecureCookie should be true in production (served over HTTPS behind
	// Caddy) and false for local http:// development — inferred from
	// whether a cookie domain is set, since local dev leaves it empty.
	secureCookie := cfg.CookieDomain != ""

	// File storage is optional, same as email below: a bad or unreachable
	// S3_ENDPOINT must never take the whole site down (see storage.New's
	// doc comment) — so any error here is logged and continued past, not
	// fatal. Only actually configuring it wrong (or not at all) costs you
	// the file library/event photos, not the entire server.
	store, err := storage.New(ctx, storage.Config{
		Endpoint:  cfg.S3Endpoint,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
		Bucket:    cfg.S3Bucket,
		UseSSL:    cfg.S3UseSSL,
	})
	if err != nil {
		log.Printf("file storage is misconfigured (%v) — the file library and event photos will report a clear error instead of working", err)
		store = nil
	} else if store == nil {
		log.Println("file storage is not configured (no S3_ENDPOINT environment variable) — the file library and event photos will report a clear error instead of failing to start")
	}

	if *backupFiles {
		if store == nil {
			log.Fatal("backup-files: file storage isn't configured (see S3_ENDPOINT above) — there are no photos to back up. The database dump is separate; see scripts/backup.sh")
		}
		// Progress goes to stderr because stdout is the archive itself.
		res, err := backup.Export(ctx, store, os.Stdout)
		if err != nil {
			log.Fatalf("backup-files: %v", err)
		}
		log.Printf("backup-files: wrote %d objects, %d bytes", res.Objects, res.Bytes)
		return
	}

	if *restoreFiles {
		if store == nil {
			log.Fatal("restore-files: file storage isn't configured (see S3_ENDPOINT above) — there's nowhere to put the photos back")
		}
		res, err := backup.Import(ctx, store, os.Stdin)
		if err != nil {
			log.Fatalf("restore-files: %v", err)
		}
		log.Printf("restore-files: restored %d objects, %d bytes", res.Objects, res.Bytes)
		return
	}

	if *backfillThumbnails {
		if store == nil {
			log.Fatal("backfill-thumbnails: file storage isn't configured (see S3_ENDPOINT above) — nothing to back fill")
		}
		result, err := web.BackfillThumbnails(ctx, pool, store)
		if err != nil {
			log.Fatalf("backfill-thumbnails: %v", err)
		}
		log.Printf("backfill-thumbnails: generated %d, skipped %d (already cached), failed %d", result.Generated, result.Skipped, result.Failed)
		return
	}

	// Catches up any photo uploaded before eager thumbnail generation
	// existed (see FileUpload) without making an operator remember to
	// run -backfill-thumbnails by hand — every deploy of this code runs
	// it automatically, the same way db.Migrate above already
	// auto-applies any pending schema change with no manual step. Backgrounded
	// so a large existing library doesn't delay the server coming up;
	// BackfillThumbnails' own per-file skip-if-already-cached check makes
	// re-running it on every single startup cheap once the library is
	// caught up, rather than needing its own "have I already done this"
	// tracking.
	if store != nil {
		go func() {
			result, err := web.BackfillThumbnails(ctx, pool, store)
			if err != nil {
				log.Printf("thumbnail backfill: %v", err)
				return
			}
			if result.Generated > 0 || result.Failed > 0 {
				log.Printf("thumbnail backfill: generated %d, skipped %d (already cached), failed %d", result.Generated, result.Skipped, result.Failed)
			}
		}()
	}

	handlers, err := web.New(pool, cfg.CookieDomain, secureCookie, mail, store)
	if err != nil {
		log.Fatalf("web: %v", err)
	}
	handlers.TrustProxyHeaders = cfg.TrustProxyHeaders
	handlers.UnsubscribeSecret = []byte(cfg.SessionSecret)

	mux := http.NewServeMux()
	handlers.Routes(mux)

	// Middleware order matters, and reads inside-out: the LAST wrap here
	// is the OUTERMOST, so this list runs bottom-to-top. Resolve the unit
	// first (everything downstream assumes it's in context), then attach
	// the logged-in user, then CSRF — which is the order
	// scout-website-architecture-phase1.md and CLAUDE.md both describe.
	//
	// CSRF used to be wrapped last of the three, which put it OUTSIDE
	// auth.WithUser and therefore ran it before any session existed in
	// the request context. That was invisible while CSRF only compared a
	// token, and became load-bearing once it had to size the request-body
	// limit by whether the caller is signed in: outside auth, every
	// request looks anonymous.
	var handler http.Handler = mux
	handler = csrf.Middleware(secureCookie, func(r *http.Request) bool {
		_, ok := auth.UserFromContext(r.Context())
		return ok
	})(handler)
	handler = auth.WithUser(pool)(handler)
	handler = units.Middleware(pool)(handler)
	// Before securityHeaders only by convention — they touch different
	// headers. Must be outside every template-rendering handler, since
	// those read the nonce it puts in the request context.
	handler = csp.Middleware(handler)
	handler = securityHeaders(handler)
	handler = requestLogger(handler)

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: handler,

		// ReadHeaderTimeout, not ReadTimeout, is what defends against a
		// client that opens a connection and dribbles headers forever
		// (slowloris): headers are small and must arrive promptly whoever
		// you are.
		ReadHeaderTimeout: 10 * time.Second,

		// ReadTimeout covers reading the entire request BODY as well, so
		// it has to be large enough for the largest upload the site
		// accepts (internal/csrf's maxRequestBodySize, 250 MB) over a
		// domestic connection. It was 10 seconds, which meant a parent
		// uploading a batch of camp photos on a 5 Mbit/s line — roughly
		// 40 seconds for 25 MB — had the connection killed mid-upload and
		// saw a broken page rather than any error the app wrote. 250 MB at
		// a slow-but-real 2 Mbit/s is about 17 minutes, hence the value
		// below; the size cap, not the clock, is what bounds abuse here.
		ReadTimeout: 20 * time.Minute,
		// Must comfortably exceed the slowest a single request can
		// legitimately block for — currently that's a synchronous outbound
		// SMTP send (forgot-password, a newsletter recipient), bounded by
		// internal/mailer's own dial+conversation timeouts at ~30-45s
		// worst case. WriteTimeout counts from when the request headers
		// are read and covers the handler's own execution time, not just
		// writing the response body, so it previously being shorter than
		// even the mailer's dial timeout meant the standard library could
		// kill the connection out from under a slow-but-otherwise-fine
		// send before the handler ever got to render its own error page —
		// the client just sees a dropped connection, not a friendly
		// "couldn't send that email" message.
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		log.Println("shutting down...")
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	log.Printf("listening on %s (cookie domain %q)", cfg.ListenAddr, cfg.CookieDomain)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s%s %s", r.Method, r.Host, r.URL.Path, time.Since(start))
	})
}

// securityHeaders sets defense-in-depth response headers on every request.
// The Content-Security-Policy is NOT here — it needs a per-request nonce,
// so it's set by internal/csp.Middleware instead. HSTS is intentionally
// left to Caddy,
// which adds it automatically when it terminates TLS (see Caddyfile);
// setting it here too would risk sending it over plain http:// in local
// development.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// printDemoSeedSummary prints every login -seed-demo created (or, if it
// no-op'd because demo data already existed, says so) — this is the only
// place these credentials are shown, same "plaintext exactly once"
// reasoning as a temporary password from /admin/roster, so this output
// is worth capturing (e.g. redirecting -seed-demo's output to a file)
// rather than relying on scrolling back through terminal history later.
func printDemoSeedSummary(s demoseed.Summary) {
	if s.AlreadySeeded {
		fmt.Println("seed-demo: demo data already exists (found the super_admin demo login) — nothing to do")
		fmt.Println("seed-demo: see DEMO_DATA.md for the full list of logins, or query the families/users tables directly")
		return
	}

	divider := strings.Repeat("-", 78)
	fmt.Println(divider)
	fmt.Println("seed-demo: created the following test logins (see DEMO_DATA.md for what activity data goes with each):")
	fmt.Println(divider)
	for _, p := range s.Personas {
		fmt.Printf("\n%s — %s\n", p.Label, p.Unit)
		fmt.Printf("  email:    %s\n", p.Email)
		fmt.Printf("  password: %s\n", p.Password)
		if p.TOTPSecret != "" {
			fmt.Printf("  two-factor: CONFIRMED — setup key %s\n", p.TOTPSecret)
			fmt.Printf("  backup codes: %s\n", strings.Join(p.BackupCodes, ", "))
		}
		if p.Note != "" {
			fmt.Printf("  note: %s\n", p.Note)
		}
	}
	fmt.Println("\n" + divider)
	fmt.Println("Every login above shares the same password. These are obviously-fake test accounts (example.com addresses) — see DEMO_DATA.md before pointing this at a real production database.")
	fmt.Println(divider)
}
