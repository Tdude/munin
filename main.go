package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Tdude/muntra/internal/api"
	"github.com/Tdude/muntra/internal/auth"
	"github.com/Tdude/muntra/internal/collect"
	"github.com/Tdude/muntra/internal/config"
	"github.com/Tdude/muntra/internal/flush"
	"github.com/Tdude/muntra/internal/migrate"
	"github.com/Tdude/muntra/internal/rollup"
	"github.com/Tdude/muntra/internal/salt"
	"github.com/Tdude/muntra/internal/store"
	"github.com/Tdude/muntra/internal/tracker"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		runHealthcheck()
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	redis, err := store.NewRedis(ctx, cfg.RedisURL)
	if err != nil {
		logger.Error("redis connect failed", "err", err)
		os.Exit(1)
	}
	defer redis.Close()

	pg, err := store.NewPostgres(ctx, cfg.PostgresDSN)
	if err != nil {
		logger.Error("postgres connect failed", "err", err)
		os.Exit(1)
	}
	defer pg.Close()

	if err := migrate.Apply(ctx, pg, "/schema"); err != nil {
		logger.Error("migrate failed", "err", err)
		os.Exit(1)
	}

	saltSvc := salt.New(redis)
	go saltSvc.Run(ctx)

	flusher := flush.New(redis, pg, cfg.FlushInterval, cfg.FlushBatchSize)
	go flusher.Run(ctx)

	rollupWorker := rollup.New(pg, cfg.RollupInterval)
	go rollupWorker.Run(ctx)

	apiHandler := api.NewHandler(pg, cfg.AllowedSites)
	authed := auth.BearerToken(cfg.DashboardToken)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /collect", collect.NewHandler(redis, saltSvc, cfg.AllowedSites, cfg.SiteOrigins))
	mux.HandleFunc("GET /script.js", tracker.Handler())
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("GET /api/stats", authed(http.HandlerFunc(apiHandler.Stats)))
	mux.Handle("GET /api/timeseries", authed(http.HandlerFunc(apiHandler.Timeseries)))
	mux.Handle("GET /api/breakdown", authed(http.HandlerFunc(apiHandler.Breakdown)))
	mux.Handle("GET /api/live", authed(http.HandlerFunc(apiHandler.Live)))

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("server starting", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown requested")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown failed", "err", err)
	}
	logger.Info("bye")
}

// runHealthcheck is invoked as `/muntra healthcheck` by the Docker healthcheck.
// The distroless base image has no wget/curl, so the binary self-checks.
func runHealthcheck() {
	addr := os.Getenv("MUNTRA_HTTP_ADDR")
	if addr == "" {
		addr = ":8090"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		os.Exit(1)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck status:", resp.StatusCode)
		os.Exit(1)
	}
}
