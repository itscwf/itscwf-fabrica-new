// Command server: itscwf-fabrica-new — merged backend + Hermes proxy + embedded frontend.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/itscwf/itscwf-fabrica-new/internal/config"
	"github.com/itscwf/itscwf-fabrica-new/internal/db"
	"github.com/itscwf/itscwf-fabrica-new/internal/handlers"
	"github.com/itscwf/itscwf-fabrica-new/internal/middleware"
	"github.com/itscwf/itscwf-fabrica-new/migrations"
	"github.com/itscwf/itscwf-fabrica-new/pkg/fabrica"
)

//go:embed static
var frontendFS embed.FS

func main() {
	var (
		configPath   = flag.String("config", envOr("FABRICA_CONFIG", "config.yaml"), "config YAML file")
		port         = flag.String("port", "", "override PORT")
		migrateOnly  = flag.Bool("migrate-only", false, "apply schema and exit")
		hashPassword = flag.String("hash-password", "", "print bcrypt hash of password and exit")
		showVersion  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("%s %s (%s)\n", fabrica.Name, fabrica.Version, fabrica.Commit)
		return
	}
	if *hashPassword != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*hashPassword), bcrypt.DefaultCost)
		if err != nil {
			fmt.Fprintln(os.Stderr, "erro ao gerar hash:", err)
			os.Exit(1)
		}
		fmt.Println(string(hash))
		return
	}

	if err := run(*configPath, *port, *migrateOnly); err != nil {
		slog.Error("startup_failed", "error", err)
		os.Exit(1)
	}
}

func run(configPath, portOverride string, migrateOnly bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if portOverride != "" {
		cfg.Port = portOverride
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)

	sqlDB, err := db.Open(db.Options{
		Path:         cfg.DBPath,
		BusyTimeout:  cfg.Database.BusyTimeout,
		MaxOpenConns: cfg.Database.MaxOpenConns,
		LogLevel:     cfg.LogLevel,
	})
	if err != nil {
		return err
	}
	defer func() {
		if cerr := db.Close(sqlDB); cerr != nil {
			logger.Warn("db_close_failed", "error", cerr)
		}
	}()

	rawDB, err := sqlDB.DB()
	if err != nil {
		return fmt.Errorf("banco: %w", err)
	}
	applied, err := migrations.Up(rawDB)
	if err != nil {
		return err
	}
	if len(applied) > 0 {
		logger.Info("schema_migrations_applied", "files", strings.Join(applied, ","))
	}
	missing, err := migrations.MissingTables(rawDB)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("schema incompleto — tabelas ausentes: %s", strings.Join(missing, ", "))
	}

	if err := db.Migrate(sqlDB); err != nil {
		return err
	}
	journalMode, err := db.JournalMode(sqlDB)
	if err != nil {
		return err
	}
	logger.Info("database_ready", "db_path", cfg.DBPath, "journal_mode", journalMode)

	if migrateOnly {
		logger.Info("migrate_only_done")
		return nil
	}

	warnAboutConfig(cfg, logger)

	tokens, err := fabrica.NewTokenManager(cfg.Auth.JWTSecret, cfg.Auth.Issuer, cfg.Auth.TokenTTL)
	if err != nil {
		return err
	}

	hermesClient := fabrica.NewHermesClient(
		cfg.Hermes.Bin, cfg.Hermes.WorkDir, cfg.Hermes.CronJobsPath,
		cfg.Hermes.KanbanBoard, time.Duration(cfg.Hermes.TimeoutSecs)*time.Second,
	)

	kanban := fabrica.NewHTTPClient(
		fabrica.KanbanAPIURL(),
		cfg.Hermes.KanbanBoard,
	)

	// Router com todas as rotas
	router := handlers.NewRouter(handlers.Deps{
		Config:   cfg,
		DB:       sqlDB,
		Logger:   logger,
		Hermes:   hermesClient,
		Kanban:   kanban,
		Auth:     middleware.NewJWTAuth(tokens),
		Recorder: handlers.NewRecorder(sqlDB),
	})

	// Sirve o frontend embarcado no mesmo servidor (SPA fallback)
	frontendHandler := spaHandler(frontendFS)

	// Router principal com frontend na raiz
	mux := http.NewServeMux()
	mux.Handle("/", frontendHandler)
	mux.Handle("/api/", router)

	srv := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      mux,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("server_starting",
			"service", fabrica.Name,
			"version", fabrica.Version,
			"addr", srv.Addr,
			"env", cfg.Env,
			"db_path", cfg.DBPath,
			"frontend", "embedded",
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		return err
	case sig := <-stop:
		logger.Info("shutdown_signal", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return err
	}
	logger.Info("server_stopped")
	return nil
}

// spaHandler serves the embedded React SPA with fallback to index.html for client-side routing.
func spaHandler(fsys embed.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fpath := path.Join("/", r.URL.Path)
		// Try to serve the file
		f, err := fsys.Open(fpath)
		if err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// Fallback to index.html for SPA routes (not API routes)
		if !strings.HasPrefix(r.URL.Path, "/api") {
			index, err := fsys.Open("index.html")
			if err == nil {
				index.Close()
				r.URL.Path = "/"
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.IsProduction() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func warnAboutConfig(cfg *config.Config, logger *slog.Logger) {
	if cfg.EphemeralSecret {
		logger.Warn("jwt_secret_ephemeral", "msg", "JWT_SECRET nao definido")
	}
	if cfg.Auth.AdminPasswordHash == "" {
		logger.Warn("admin_password_plaintext", "msg", "defina ADMIN_PASSWORD_HASH")
	}
	if cfg.Hermes.CronJobsPath != "" {
		if _, err := os.Stat(cfg.Hermes.CronJobsPath); err != nil {
			logger.Warn("hermes_cron_jobs_unreadable", "path", cfg.Hermes.CronJobsPath, "error", err)
		}
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
