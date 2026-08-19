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
//	server -grant-role           grant an existing user a role in a unit, then exit (see DEPLOY.md "Adding a unit later")
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
	"github.com/47-yonkers/scout-site/internal/bootstrap"
	"github.com/47-yonkers/scout-site/internal/config"
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
	grantRole := flag.Bool("grant-role", false, "grant an existing user (GRANT_EMAIL) a role (GRANT_ROLE) in a unit (GRANT_UNIT_SLUG) and exit — for giving an already-existing account a foothold in a unit -bootstrap-admin didn't reach, e.g. one added after -bootstrap-admin first ran")
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
	}, pool)
	if !mail.Enabled(ctx) {
		log.Println("email is not configured (no SMTP_HOST environment variable and no host set on /admin/settings) — password reset and event reminders will report a clear error instead of sending")
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

	store, err := storage.New(ctx, storage.Config{
		Endpoint:  cfg.S3Endpoint,
		AccessKey: cfg.S3AccessKey,
		SecretKey: cfg.S3SecretKey,
		Bucket:    cfg.S3Bucket,
		UseSSL:    cfg.S3UseSSL,
	})
	if err != nil {
		log.Fatalf("storage: %v", err)
	}

	handlers, err := web.New(pool, cfg.CookieDomain, secureCookie, mail, store)
	if err != nil {
		log.Fatalf("web: %v", err)
	}

	mux := http.NewServeMux()
	handlers.Routes(mux)

	// Middleware order matters: resolve the unit first (everything else
	// assumes it's in context), then attach the logged-in user if any.
	var handler http.Handler = mux
	handler = auth.WithUser(pool)(handler)
	handler = units.Middleware(pool)(handler)
	handler = csrf.Middleware(secureCookie)(handler)
	handler = requestLogger(handler)

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
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
