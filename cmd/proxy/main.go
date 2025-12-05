package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/crohr/smart-git-proxy/internal/cloudmap"
	"github.com/crohr/smart-git-proxy/internal/config"
	"github.com/crohr/smart-git-proxy/internal/gitproxy"
	"github.com/crohr/smart-git-proxy/internal/logging"
	"github.com/crohr/smart-git-proxy/internal/metrics"
	"github.com/crohr/smart-git-proxy/internal/mirror"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		log.Fatalf("logger init: %v", err)
	}

	mirrorStore, err := mirror.New(cfg.MirrorDir, cfg.SyncStaleAfter, cfg.MirrorMaxSize, logger)
	if err != nil {
		logger.Error("mirror init failed", "err", err)
		os.Exit(1)
	}

	metricsRegistry := metrics.New()
	server := gitproxy.New(cfg, mirrorStore, logger, metricsRegistry)

	mux := http.NewServeMux()
	mux.Handle(cfg.HealthPath, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}))
	mux.Handle(cfg.MetricsPath, promhttp.Handler())
	mux.Handle("/", server.Handler())

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		logger.Info("listening", "addr", cfg.ListenAddr, "mirror_dir", cfg.MirrorDir, "allowed_upstreams", cfg.AllowedUpstreams, "sync_stale_after", cfg.SyncStaleAfter)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	// AWS Cloud Map registration and heartbeat
	var cloudMapMgr *cloudmap.Manager
	if cfg.AWSCloudMapServiceID != "" {
		var err error
		cloudMapMgr, err = cloudmap.New(context.Background(), cfg.AWSCloudMapServiceID, logger)
		if err != nil {
			logger.Error("cloud map init failed", "err", err)
			os.Exit(1)
		}
		if err := cloudMapMgr.Start(context.Background()); err != nil {
			logger.Error("cloud map start failed", "err", err)
			os.Exit(1)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Deregister from Cloud Map before shutting down HTTP server
	if cloudMapMgr != nil {
		cloudMapMgr.Stop(ctx)
	}

	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
}
