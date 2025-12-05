package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
)

type Config struct {
	ListenAddr        string
	CacheDir          string
	CacheSizeBytes    int64
	RepackInterval    time.Duration
	UpstreamTimeout   time.Duration
	AllowedUpstreams  []string
	LogLevel          string
	AuthMode          string
	StaticToken       string
	MaxPackSizeBytes  int64
	MetricsPath       string
	HealthPath        string
	UserAgent         string
	AllowInsecureHTTP bool
}

func Load() (*Config, error) {
	return LoadArgs(os.Args[1:])
}

func LoadArgs(args []string) (*Config, error) {
	cfg := &Config{}

	fs := flag.NewFlagSet("smart-git-proxy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.StringVar(&cfg.ListenAddr, "listen-addr", envOrDefault("LISTEN_ADDR", ":8080"), "HTTP listen address")
	fs.StringVar(&cfg.CacheDir, "cache-dir", envOrDefault("CACHE_DIR", "/mnt/git-cache"), "cache directory for packs")
	fs.StringVar(&cfg.LogLevel, "log-level", envOrDefault("LOG_LEVEL", "info"), "log level: debug,info,warn,error")

	allowedUpstreamsStr := fs.String("allowed-upstreams", envOrDefault("ALLOWED_UPSTREAMS", "github.com"), "comma-separated list of allowed upstream hosts")
	fs.StringVar(&cfg.AuthMode, "auth-mode", envOrDefault("AUTH_MODE", "pass-through"), "auth mode: pass-through|static|none")
	fs.StringVar(&cfg.StaticToken, "static-token", envOrDefault("STATIC_TOKEN", ""), "static token used when auth-mode=static (sent as Authorization: Bearer)")
	fs.StringVar(&cfg.MetricsPath, "metrics-path", envOrDefault("METRICS_PATH", "/metrics"), "path for Prometheus metrics")
	fs.StringVar(&cfg.HealthPath, "health-path", envOrDefault("HEALTH_PATH", "/healthz"), "path for health checks")
	fs.StringVar(&cfg.UserAgent, "user-agent", envOrDefault("USER_AGENT", "smart-git-proxy"), "User-Agent header to send upstream")
	fs.BoolVar(&cfg.AllowInsecureHTTP, "allow-insecure-http", envOrBool("ALLOW_INSECURE_HTTP", false), "allow proxying to http upstreams")

	cacheSizeStr := fs.String("cache-size", envOrDefault("CACHE_SIZE_BYTES", "200GB"), "cache size limit (e.g. 200GB)")
	repackIntervalStr := fs.String("repack-interval", envOrDefault("REPACK_INTERVAL", "6h"), "background repack interval (not yet implemented)")
	upstreamTimeoutStr := fs.String("upstream-timeout", envOrDefault("UPSTREAM_TIMEOUT", "60s"), "timeout for upstream requests")
	maxPackSizeStr := fs.String("max-pack-size", envOrDefault("MAX_PACK_SIZE_BYTES", "2GB"), "max allowed upstream pack size")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	var err error
	if cfg.CacheSizeBytes, err = parseBytes(*cacheSizeStr); err != nil {
		return nil, fmt.Errorf("invalid cache-size: %w", err)
	}
	if cfg.MaxPackSizeBytes, err = parseBytes(*maxPackSizeStr); err != nil {
		return nil, fmt.Errorf("invalid max-pack-size: %w", err)
	}
	if cfg.RepackInterval, err = time.ParseDuration(*repackIntervalStr); err != nil {
		return nil, fmt.Errorf("invalid repack-interval: %w", err)
	}
	if cfg.UpstreamTimeout, err = time.ParseDuration(*upstreamTimeoutStr); err != nil {
		return nil, fmt.Errorf("invalid upstream-timeout: %w", err)
	}

	// Parse allowed upstreams
	for _, h := range strings.Split(*allowedUpstreamsStr, ",") {
		h = strings.TrimSpace(h)
		if h != "" {
			cfg.AllowedUpstreams = append(cfg.AllowedUpstreams, h)
		}
	}
	if len(cfg.AllowedUpstreams) == 0 {
		return nil, errors.New("at least one allowed upstream is required")
	}

	if err := validateAuth(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func validateAuth(cfg *Config) error {
	switch cfg.AuthMode {
	case "pass-through":
		return nil
	case "static":
		if cfg.StaticToken == "" {
			return errors.New("auth-mode=static requires STATIC_TOKEN")
		}
		return nil
	case "none":
		return nil
	default:
		return fmt.Errorf("unknown auth-mode: %s", cfg.AuthMode)
	}
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envOrBool(key string, def bool) bool {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return def
}

func parseBytes(v string) (int64, error) {
	num, err := humanize.ParseBytes(v)
	if err != nil {
		return 0, err
	}
	return int64(num), nil
}
