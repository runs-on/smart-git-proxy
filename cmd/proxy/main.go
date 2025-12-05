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

	"github.com/crohr/smart-git-proxy/internal/cache"
	"github.com/crohr/smart-git-proxy/internal/config"
	"github.com/crohr/smart-git-proxy/internal/gitproxy"
	"github.com/crohr/smart-git-proxy/internal/logging"
	"github.com/crohr/smart-git-proxy/internal/metrics"
	"github.com/crohr/smart-git-proxy/internal/upstream"
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

	cacheStore, err := cache.New(cfg.CacheDir, cfg.CacheSizeBytes, logger)
	if err != nil {
		logger.Error("cache init failed", "err", err)
		os.Exit(1)
	}

	metricsRegistry := metrics.New()
	upClient := upstream.NewClient(cfg.UpstreamTimeout, cfg.AllowInsecureHTTP, cfg.UserAgent)
	server := gitproxy.New(cfg, cacheStore, upClient, logger, metricsRegistry)

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
		logger.Info("listening", "addr", cfg.ListenAddr, "cache_dir", cfg.CacheDir, "allowed_upstreams", cfg.AllowedUpstreams)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
}
