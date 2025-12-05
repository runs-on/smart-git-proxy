package gitproxy_test

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crohr/smart-git-proxy/internal/config"
	"github.com/crohr/smart-git-proxy/internal/gitproxy"
	"github.com/crohr/smart-git-proxy/internal/logging"
	"github.com/crohr/smart-git-proxy/internal/metrics"
	"github.com/crohr/smart-git-proxy/internal/mirror"
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
	mirrorDir := t.TempDir()
	cloneDir := t.TempDir()

	// Create config
	cfg := &config.Config{
		ListenAddr:       ":0", // not used directly, we use httptest
		AllowedUpstreams: []string{"github.com"},
		MirrorDir:        mirrorDir,
		SyncStaleAfter:   2 * time.Second,
		AuthMode:         "none",
		LogLevel:         "info",
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		t.Fatalf("logger init: %v", err)
	}

	mirrorStore, err := mirror.New(cfg.MirrorDir, cfg.SyncStaleAfter, config.SizeSpec{}, logger)
	if err != nil {
		t.Fatalf("mirror init: %v", err)
	}

	metricsRegistry := metrics.NewUnregistered()
	server := gitproxy.New(cfg, mirrorStore, logger, metricsRegistry)

	// Start test server
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Use a tiny public repo for the test
	// octocat/Hello-World is GitHub's demo repo, very small
	testRepo := "octocat/Hello-World"
	repoURL := "https://github.com/" + testRepo

	// Clone via proxy using url.insteadOf
	clonePath := filepath.Join(cloneDir, "hello-world")
	insteadOf := ts.URL + "/github.com/"

	t.Logf("Proxy URL: %s", ts.URL)
	t.Logf("Clone target: %s", clonePath)

	// First clone - should clone mirror from upstream
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

	// Check mirror was populated
	mirrorPath := filepath.Join(mirrorDir, "github.com", "octocat", "Hello-World.git")
	if _, err := os.Stat(mirrorPath); os.IsNotExist(err) {
		t.Fatalf("mirror not created at %s", mirrorPath)
	}
	t.Logf("Mirror created at: %s", mirrorPath)

	// Second clone to different dir - should use existing mirror (fast)
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

	t.Log("E2E clone test passed")
}

func TestE2E_FetchPublicRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	mirrorDir := t.TempDir()
	cloneDir := t.TempDir()

	cfg := &config.Config{
		ListenAddr:       ":0",
		AllowedUpstreams: []string{"github.com"},
		MirrorDir:        mirrorDir,
		SyncStaleAfter:   2 * time.Second,
		AuthMode:         "none",
		LogLevel:         "info",
	}

	logger, _ := logging.New(cfg.LogLevel)
	mirrorStore, _ := mirror.New(cfg.MirrorDir, cfg.SyncStaleAfter, config.SizeSpec{}, logger)
	metricsRegistry := metrics.NewUnregistered()
	server := gitproxy.New(cfg, mirrorStore, logger, metricsRegistry)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	testRepo := "octocat/Hello-World"
	repoURL := "https://github.com/" + testRepo
	insteadOf := ts.URL + "/github.com/"
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

	mirrorDir := t.TempDir()

	cfg := &config.Config{
		ListenAddr:       ":0",
		AllowedUpstreams: []string{"github.com"},
		MirrorDir:        mirrorDir,
		SyncStaleAfter:   2 * time.Second,
		AuthMode:         "none",
		LogLevel:         "info",
	}

	logger, _ := logging.New(cfg.LogLevel)
	mirrorStore, _ := mirror.New(cfg.MirrorDir, cfg.SyncStaleAfter, config.SizeSpec{}, logger)
	metricsRegistry := metrics.NewUnregistered()
	server := gitproxy.New(cfg, mirrorStore, logger, metricsRegistry)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	testRepo := "octocat/Hello-World"
	repoURL := "https://github.com/" + testRepo
	insteadOf := ts.URL + "/github.com/"

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

func TestE2E_CloneFullDepth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	mirrorDir := t.TempDir()
	cloneDir := t.TempDir()

	cfg := &config.Config{
		ListenAddr:       ":0",
		AllowedUpstreams: []string{"github.com"},
		MirrorDir:        mirrorDir,
		SyncStaleAfter:   2 * time.Second,
		AuthMode:         "none",
		LogLevel:         "info",
	}

	logger, _ := logging.New(cfg.LogLevel)
	mirrorStore, _ := mirror.New(cfg.MirrorDir, cfg.SyncStaleAfter, config.SizeSpec{}, logger)
	metricsRegistry := metrics.NewUnregistered()
	server := gitproxy.New(cfg, mirrorStore, logger, metricsRegistry)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Clone runs-on/runs-on at full depth
	testRepo := "runs-on/runs-on"
	repoURL := "https://github.com/" + testRepo
	insteadOf := ts.URL + "/github.com/"
	clonePath := filepath.Join(cloneDir, "runs-on")

	t.Logf("Proxy URL: %s", ts.URL)
	t.Logf("Clone target: %s", clonePath)

	// Full clone (no --depth)
	cmd := exec.Command("git",
		"-c", "url."+insteadOf+".insteadOf=https://github.com/",
		"clone", repoURL, clonePath,
	)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("full clone failed: %v\noutput: %s", err, out)
	}
	t.Logf("Clone output: %s", out)

	// Verify clone succeeded by checking for README
	readmePath := filepath.Join(clonePath, "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		t.Fatalf("README.md not found after clone")
	}

	// Verify we have git history (full depth)
	logCmd := exec.Command("git", "rev-list", "--count", "HEAD")
	logCmd.Dir = clonePath
	countOut, err := logCmd.Output()
	if err != nil {
		t.Fatalf("git rev-list failed: %v", err)
	}
	t.Logf("Commit count: %s", strings.TrimSpace(string(countOut)))

	t.Log("E2E full depth clone test passed")
}

// TestE2E_DifferentRefsSameMirror tests that multiple clones of different refs
// share the same mirror - this is the key benefit of the mirror-based approach.
func TestE2E_DifferentRefsSameMirror(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	mirrorDir := t.TempDir()
	cloneDir := t.TempDir()

	cfg := &config.Config{
		ListenAddr:       ":0",
		AllowedUpstreams: []string{"github.com"},
		MirrorDir:        mirrorDir,
		SyncStaleAfter:   2 * time.Second,
		AuthMode:         "none",
		LogLevel:         "debug",
	}

	logger, _ := logging.New(cfg.LogLevel)
	mirrorStore, _ := mirror.New(cfg.MirrorDir, cfg.SyncStaleAfter, config.SizeSpec{}, logger)
	metricsRegistry := metrics.NewUnregistered()
	server := gitproxy.New(cfg, mirrorStore, logger, metricsRegistry)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	testRepo := "octocat/Hello-World"
	repoURL := "https://github.com/" + testRepo
	insteadOf := ts.URL + "/github.com/"

	// First clone at depth=1 (gets just tip)
	clonePath1 := filepath.Join(cloneDir, "clone1")
	cmd1 := exec.Command("git",
		"-c", "url."+insteadOf+".insteadOf=https://github.com/",
		"clone", "--depth=1", repoURL, clonePath1,
	)
	cmd1.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd1.CombinedOutput(); err != nil {
		t.Fatalf("first clone failed: %v\noutput: %s", err, out)
	}

	// Second clone with different depth - should reuse same mirror
	clonePath2 := filepath.Join(cloneDir, "clone2")
	cmd2 := exec.Command("git",
		"-c", "url."+insteadOf+".insteadOf=https://github.com/",
		"clone", "--depth=5", repoURL, clonePath2,
	)
	cmd2.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd2.CombinedOutput(); err != nil {
		t.Fatalf("second clone failed: %v\noutput: %s", err, out)
	}

	// Verify only one mirror exists
	mirrorPath := filepath.Join(mirrorDir, "github.com", "octocat", "Hello-World.git")
	if _, err := os.Stat(mirrorPath); os.IsNotExist(err) {
		t.Fatalf("expected single mirror at %s", mirrorPath)
	}

	// Count number of mirrors - should be exactly 1
	var mirrorCount int
	filepath.Walk(mirrorDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && strings.HasSuffix(path, ".git") {
			mirrorCount++
		}
		return nil
	})
	if mirrorCount != 1 {
		t.Fatalf("expected 1 mirror, got %d", mirrorCount)
	}

	t.Log("E2E different refs same mirror test passed")
}
