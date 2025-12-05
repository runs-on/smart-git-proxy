package gitproxy_test

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crohr/smart-git-proxy/internal/cache"
	"github.com/crohr/smart-git-proxy/internal/config"
	"github.com/crohr/smart-git-proxy/internal/gitproxy"
	"github.com/crohr/smart-git-proxy/internal/logging"
	"github.com/crohr/smart-git-proxy/internal/metrics"
	"github.com/crohr/smart-git-proxy/internal/upstream"
)

func TestE2E_ClonePublicRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	// Check git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	// Setup temp dirs
	cacheDir := t.TempDir()
	cloneDir := t.TempDir()

	// Create config
	cfg := &config.Config{
		ListenAddr:        ":0", // not used directly, we use httptest
		UpstreamBase:      "https://github.com",
		CacheDir:          cacheDir,
		CacheSizeBytes:    100 * 1024 * 1024, // 100MB
		AuthMode:          "none",
		LogLevel:          "info",
		UpstreamTimeout:   60 * time.Second,
		UserAgent:         "smart-git-proxy-test",
		AllowInsecureHTTP: false,
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		t.Fatalf("logger init: %v", err)
	}

	cacheStore, err := cache.New(cfg.CacheDir, cfg.CacheSizeBytes, logger)
	if err != nil {
		t.Fatalf("cache init: %v", err)
	}

	metricsRegistry := metrics.NewUnregistered()
	upClient := upstream.NewClient(cfg.UpstreamTimeout, cfg.AllowInsecureHTTP, cfg.UserAgent)
	server := gitproxy.New(cfg, cacheStore, upClient, logger, metricsRegistry)

	// Start test server
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Use a tiny public repo for the test
	// octocat/Hello-World is GitHub's demo repo, very small
	testRepo := "octocat/Hello-World"
	repoURL := "https://github.com/" + testRepo

	// Clone via proxy using url.insteadOf
	clonePath := filepath.Join(cloneDir, "hello-world")
	insteadOf := ts.URL + "/https://github.com/"

	t.Logf("Proxy URL: %s", ts.URL)
	t.Logf("Clone target: %s", clonePath)

	// First clone - should hit upstream
	cmd := exec.Command("git",
		"-c", "url."+insteadOf+".insteadOf=https://github.com/",
		"clone", "--depth=1", repoURL, clonePath,
	)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("first clone failed: %v\noutput: %s", err, out)
	}
	t.Logf("First clone output: %s", out)

	// Verify clone succeeded
	readmePath := filepath.Join(clonePath, "README")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Fatalf("README not found after clone")
	}

	// Check cache was populated
	cacheFiles, err := countFiles(cacheDir)
	if err != nil {
		t.Fatalf("counting cache files: %v", err)
	}
	if cacheFiles == 0 {
		t.Error("expected cache to have files after first clone")
	}
	t.Logf("Cache files after first clone: %d", cacheFiles)

	// Second clone to different dir - should hit cache
	clonePath2 := filepath.Join(cloneDir, "hello-world-2")
	cmd2 := exec.Command("git",
		"-c", "url."+insteadOf+".insteadOf=https://github.com/",
		"clone", "--depth=1", repoURL, clonePath2,
	)
	cmd2.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out2, err := cmd2.CombinedOutput()
	if err != nil {
		t.Fatalf("second clone failed: %v\noutput: %s", err, out2)
	}
	t.Logf("Second clone output: %s", out2)

	// Verify second clone succeeded
	if _, err := os.Stat(filepath.Join(clonePath2, "README")); os.IsNotExist(err) {
		t.Fatalf("README not found after second clone")
	}

	// Check metrics for cache hits
	hits := metricsRegistry.CacheHits.WithLabelValues(
		"github.com/"+testRepo,
		string(cache.KindInfo),
	)
	// We can't easily read the counter value, but at least verify no panic
	_ = hits

	t.Log("E2E clone test passed")
}

func TestE2E_FetchPublicRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	cacheDir := t.TempDir()
	cloneDir := t.TempDir()

	cfg := &config.Config{
		ListenAddr:        ":0",
		UpstreamBase:      "https://github.com",
		CacheDir:          cacheDir,
		CacheSizeBytes:    100 * 1024 * 1024,
		AuthMode:          "none",
		LogLevel:          "info",
		UpstreamTimeout:   60 * time.Second,
		UserAgent:         "smart-git-proxy-test",
		AllowInsecureHTTP: false,
	}

	logger, _ := logging.New(cfg.LogLevel)
	cacheStore, _ := cache.New(cfg.CacheDir, cfg.CacheSizeBytes, logger)
	metricsRegistry := metrics.NewUnregistered()
	upClient := upstream.NewClient(cfg.UpstreamTimeout, cfg.AllowInsecureHTTP, cfg.UserAgent)
	server := gitproxy.New(cfg, cacheStore, upClient, logger, metricsRegistry)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	testRepo := "octocat/Hello-World"
	repoURL := "https://github.com/" + testRepo
	insteadOf := ts.URL + "/https://github.com/"
	clonePath := filepath.Join(cloneDir, "hello-world")

	// Initial clone
	cmd := exec.Command("git",
		"-c", "url."+insteadOf+".insteadOf=https://github.com/",
		"clone", "--depth=1", repoURL, clonePath,
	)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone failed: %v\noutput: %s", err, out)
	}

	// Now do a fetch - should also work through proxy
	fetchCmd := exec.Command("git",
		"-c", "url."+insteadOf+".insteadOf=https://github.com/",
		"fetch", "--all",
	)
	fetchCmd.Dir = clonePath
	fetchCmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := fetchCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch failed: %v\noutput: %s", err, out)
	}
	t.Logf("Fetch output: %s", out)

	t.Log("E2E fetch test passed")
}

func TestE2E_LsRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	cacheDir := t.TempDir()

	cfg := &config.Config{
		ListenAddr:        ":0",
		UpstreamBase:      "https://github.com",
		CacheDir:          cacheDir,
		CacheSizeBytes:    100 * 1024 * 1024,
		AuthMode:          "none",
		LogLevel:          "info",
		UpstreamTimeout:   60 * time.Second,
		UserAgent:         "smart-git-proxy-test",
		AllowInsecureHTTP: false,
	}

	logger, _ := logging.New(cfg.LogLevel)
	cacheStore, _ := cache.New(cfg.CacheDir, cfg.CacheSizeBytes, logger)
	metricsRegistry := metrics.NewUnregistered()
	upClient := upstream.NewClient(cfg.UpstreamTimeout, cfg.AllowInsecureHTTP, cfg.UserAgent)
	server := gitproxy.New(cfg, cacheStore, upClient, logger, metricsRegistry)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	testRepo := "octocat/Hello-World"
	repoURL := "https://github.com/" + testRepo
	insteadOf := ts.URL + "/https://github.com/"

	// ls-remote through proxy
	cmd := exec.Command("git",
		"-c", "url."+insteadOf+".insteadOf=https://github.com/",
		"ls-remote", repoURL,
	)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ls-remote failed: %v\noutput: %s", err, out)
	}

	// Should contain refs
	if !strings.Contains(string(out), "refs/heads/master") {
		t.Errorf("ls-remote output missing refs/heads/master:\n%s", out)
	}

	t.Logf("ls-remote output: %s", out)
	t.Log("E2E ls-remote test passed")
}

func countFiles(dir string) (int, error) {
	var count int
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			count++
		}
		return nil
	})
	return count, err
}
