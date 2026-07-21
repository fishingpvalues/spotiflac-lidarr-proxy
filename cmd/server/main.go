package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	fiberadaptor "github.com/gofiber/fiber/v3/middleware/adaptor"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/newznab"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/sabnzbd"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/api/verify"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/config"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/health"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/metrics"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/queue"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/spotiflac"
	"github.com/fishingpvalues/spotiflac-lidarr-proxy/internal/storage"
	sabnzbdstatus "github.com/fishingpvalues/spotiflac-lidarr-proxy/pkg/sabnzbd"
)

// version is set at build time via -ldflags "-X main.version=..."; see
// Dockerfile/release.yml. Falls back to "develop" (not e.g. "dev") for
// local/CI builds without that ldflag - Lidarr's SABnzbd client requires
// either a semver-shaped version or the literal string "develop", which
// it special-cases to assume SABnzbd 3.0.0+ (see Sabnzbd.cs ParseVersion);
// anything else fails with "Unknown Version: <value>" and Lidarr refuses
// the download client entirely.
var version = "develop"

// verbose counts -v occurrences (-v, -vv, ...), like ssh/curl/ansible.
// It only ever raises verbosity above SPF_LOG_LEVEL, never lowers it.
var verbose int

func main() {
	rootCmd := &cobra.Command{
		Use:   "server",
		Short: "Spotiflac-Lidarr Proxy server",
	}
	rootCmd.PersistentFlags().CountVarP(&verbose, "verbose", "v",
		"increase log verbosity above SPF_LOG_LEVEL (-v debug, -vv trace)")

	rootCmd.AddCommand(&cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP server",
		RunE:  runServe,
	})

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

// resolveLogLevel applies -v/-vv on top of the configured level, only ever
// making things more verbose (matches ssh/curl/ansible: repeatable -v never
// silences logging that config already asked for).
func resolveLogLevel(configuredLevel string, verboseCount int) zerolog.Level {
	level, err := zerolog.ParseLevel(configuredLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	switch {
	case verboseCount >= 2 && level > zerolog.TraceLevel:
		return zerolog.TraceLevel
	case verboseCount == 1 && level > zerolog.DebugLevel:
		return zerolog.DebugLevel
	default:
		return level
	}
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := zerolog.New(os.Stderr).With().Timestamp().Logger()
	log = log.Level(resolveLogLevel(cfg.LogLevel, verbose))

	q, err := queue.New(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("init queue: %w", err)
	}
	defer q.Close()

	st := storage.New(cfg.OutputDir)

	client := spotiflac.NewClient(
		cfg.SpotiflacCLIPath,
		cfg.JobTimeout,
		cfg.DefaultService,
		cfg.DefaultQuality,
		cfg.VerifyRelayURL,
		cfg.TidalAPIURL,
		cfg.QobuzAPIURL,
		cfg.TidalAPIFallbackURLs,
		cfg.SpotiFLACPython,
		cfg.SpotiFLACPythonVenv,
	)
	client.SetRelayPort(cfg.Port)

	app := fiber.New(fiber.Config{
		AppName:      "spotiflac-lidarr-proxy",
		ServerHeader: "spotiflac-lidarr-proxy",
		// Fiber (fasthttp) defaults to unsafe, zero-copy strings from
		// c.Query()/c.FormValue(): the returned string aliases the
		// connection's read buffer, valid only until the handler returns.
		// addurl.go stores query values (spotify URL, category, ...) in a
		// Job handed to a goroutine that outlives the handler and re-persists
		// them on every later queue.Update() call - without Immutable, a
		// concurrent request on the same connection can silently overwrite
		// that buffer, corrupting an already-queued job's fields the next
		// time it's re-saved. Confirmed against a real production run this
		// session: a job's category was found stored as "jsonc-flac-16"
		// instead of "music-flac-16".
		Immutable: true,
	})

	app.Get("/health", func(c fiber.Ctx) error {
		result := health.Check(q.DB(), cfg.SpotiflacCLIPath, st)
		if !result.Healthy {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status": "unhealthy",
				"failed": result.FailedChecks,
			})
		}
		return c.JSON(fiber.Map{"status": "ok"})
	})

	app.Get("/metrics", func(c fiber.Ctx) error {
		refreshQueueDepthMetrics(q)
		return fiberadaptor.HTTPHandler(metrics.PromHTTPHandler())(c)
	})

	// FSL (Byparr/FlareSolverr) auto-solving callback — receives Turnstile
	// grant callbacks from Byparr's headless browser and forwards to
	// SpotiFLAC's local callback server. No auth required (called by
	// Byparr's browser, not by an authenticated client).
	verifyRelay := api.NewVerifyRelayHandler()
	app.Get("/api/verify-relay", verifyRelay.Handle)

	sabHandler := sabnzbd.NewHandler(q, client, st, cfg, version)
	sabHandler.SetLogger(log)

	verifyStore := verify.NewStore()
	sabHandler.SetVerifyStore(verifyStore)

	nznbHandler := newznab.NewHandler(client, version, cfg.APIKey, cfg.DefaultQuality)
	nznbHandler.SetLogger(log)

	app.Use(api.RequestLogger(log))

	// Deliberately outside "/api": the remote verification service's
	// redirect here carries no API key, and the /api groups' auth
	// middleware below matches by path prefix, so this must not start with
	// "/api" or it would need yet another skiplist exemption. See
	// internal/api/verify.
	verifyHandler := verify.NewHandler(verifyStore)
	verifyHandler.SetLogger(log)
	verifyHandler.RegisterRoutes(app)

	// SABnzbd routes: require auth except version, auth modes
	sabGroup := app.Group("/api/sabnzbd")
	sabGroup.Use(api.APIKeyAuthWithSkiplist(cfg.APIKey, "version", "auth"))
	sabHandler.RegisterRoutesOnGroup(sabGroup)

	// Also register on /api for Lidarr SABnzbd compatibility (urlBase). This
	// group is mounted at the bare "/api" prefix, so its middleware also
	// matches every /api/newznab/* request (fiber matches Use() by path
	// prefix, not by which group's own routes it is). Without "caps" in its
	// own skiplist here too, it 401s t=caps before nznbGroup's skiplist
	// below ever gets a chance to exempt it.
	sabRootGroup := app.Group("/api")
	sabRootGroup.Use(api.APIKeyAuthWithSkiplist(cfg.APIKey, "version", "auth", "caps"))
	sabHandler.RegisterRoutesOnGroup(sabRootGroup)

	// Newznab routes: require auth except caps
	nznbGroup := app.Group("/api/newznab")
	nznbGroup.Use(api.APIKeyAuthWithSkiplist(cfg.APIKey, "caps"))
	nznbHandler.RegisterRoutesOnGroup(nznbGroup)

	log.Info().Int("port", cfg.Port).Str("version", version).Msg("starting server")

	go func() {
		addr := fmt.Sprintf(":%d", cfg.Port)
		if err := app.Listen(addr); err != nil {
			log.Fatal().Err(err).Msg("server failed")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return app.ShutdownWithContext(shutdownCtx)
}

// refreshQueueDepthMetrics updates the spf_queue_depth gauge with current
// counts by status, right before a /metrics scrape. Gauges reflect current
// state at read time rather than being incremented/decremented on every
// queue mutation.
func refreshQueueDepthMetrics(q *queue.SQLiteQueue) {
	for _, status := range []sabnzbdstatus.JobStatus{
		sabnzbdstatus.StatusQueued,
		sabnzbdstatus.StatusDownloading,
		sabnzbdstatus.StatusPaused,
	} {
		_, total, err := q.List(queue.ListParams{Status: string(status)})
		if err != nil {
			continue
		}
		metrics.SetQueueDepth(string(status), total)
	}
}
