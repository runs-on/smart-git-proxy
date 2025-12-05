package config

import (
	"os"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := LoadArgs([]string{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Fatalf("listen addr default mismatch: %s", cfg.ListenAddr)
	}
	if cfg.CacheDir == "" {
		t.Fatalf("cache dir default empty")
	}
	if cfg.CacheSizeBytes <= 0 {
		t.Fatalf("cache size default invalid: %d", cfg.CacheSizeBytes)
	}
}

func TestStaticAuthRequiresToken(t *testing.T) {
	clearEnv(t)
	_, err := LoadArgs([]string{"-auth-mode=static"})
	if err == nil {
		t.Fatalf("expected error when static token missing")
	}
}

func TestEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("CACHE_SIZE_BYTES", "1GB")
	cfg, err := LoadArgs([]string{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.CacheSizeBytes != 1_000_000_000 {
		t.Fatalf("expected cache size override, got %d", cfg.CacheSizeBytes)
	}
	if cfg.RepackInterval != 6*time.Hour {
		t.Fatalf("unexpected repack interval: %s", cfg.RepackInterval)
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"LISTEN_ADDR", "CACHE_DIR", "CACHE_SIZE_BYTES", "ALLOWED_UPSTREAMS", "LOG_LEVEL",
		"AUTH_MODE", "STATIC_TOKEN", "REPACK_INTERVAL", "MAX_PACK_SIZE_BYTES",
		"UPSTREAM_TIMEOUT",
	} {
		_ = os.Unsetenv(k)
	}
}
