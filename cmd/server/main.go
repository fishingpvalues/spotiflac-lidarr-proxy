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
	)

	app := fiber.New(fiber.Config{
		AppName:      "spotiflac-lidarr-proxy",
		ServerHeader: "spotiflac-lidarr-proxy",
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

	sabHandler := sabnzbd.NewHandler(q, client, st, cfg, version)
	sabHandler.SetLogger(log)

	nznbHandler := newznab.NewHandler(client, fmt.Sprintf("http://localhost:%d", cfg.Port), version)
	nznbHandler.SetLogger(log)

	app.Use(api.RequestLogger(log))

	// SABnzbd routes: require auth except version, auth modes
	sabGroup := app.Group("/api/sabnzbd")
	sabGroup.Use(api.APIKeyAuthWithSkiplist(cfg.APIKey, "version", "auth"))
	sabHandler.RegisterRoutesOnGroup(sabGroup)

	// Also register on /api for Lidarr SABnzbd compatibility (urlBase)
	sabRootGroup := app.Group("/api")
	sabRootGroup.Use(api.APIKeyAuthWithSkiplist(cfg.APIKey, "version", "auth"))
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
