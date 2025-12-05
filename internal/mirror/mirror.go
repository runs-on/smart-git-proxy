package mirror

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Status indicates what happened during EnsureRepo
type Status string

const (
	StatusHit   Status = "mirror-hit"   // Served from existing fresh mirror
	StatusClone Status = "mirror-clone" // Had to clone new mirror
	StatusSync  Status = "mirror-sync"  // Had to sync stale mirror
)

// Mirror manages bare git repository mirrors.
type Mirror struct {
	root       string
	staleAfter time.Duration
	log        *slog.Logger

	group     singleflight.Group
	lastSync  sync.Map // map[repoKey]time.Time
	repoLocks sync.Map // map[repoKey]*sync.Mutex
}

// New creates a new Mirror manager.
func New(root string, staleAfter time.Duration, log *slog.Logger) (*Mirror, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create mirror root: %w", err)
	}
	return &Mirror{
		root:       root,
		staleAfter: staleAfter,
		log:        log,
	}, nil
}

// RepoPath returns the filesystem path for a repo mirror.
func (m *Mirror) RepoPath(host, owner, repo string) string {
	return filepath.Join(m.root, host, owner, repo+".git")
}

// EnsureRepo ensures the mirror exists and is synced.
// authHeader is the Authorization header value from the client request (can be empty).
// Returns the path to the bare repo and the cache status.
func (m *Mirror) EnsureRepo(ctx context.Context, host, owner, repo, upstreamURL, authHeader string) (string, Status, error) {
	repoPath := m.RepoPath(host, owner, repo)
	key := fmt.Sprintf("%s/%s/%s", host, owner, repo)

	// Check if repo exists
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		// Clone new repo (use singleflight to avoid concurrent clones)
		_, err, _ := m.group.Do("clone:"+key, func() (interface{}, error) {
			return nil, m.cloneRepo(ctx, repoPath, upstreamURL, authHeader)
		})
		if err != nil {
			return "", "", err
		}
		m.lastSync.Store(key, time.Now())
		return repoPath, StatusClone, nil
	}

	// Repo exists - check if it requires auth
	if m.requiresAuth(repoPath) {
		if err := m.validateAuth(ctx, upstreamURL, authHeader); err != nil {
			m.log.Warn("auth validation failed", "repo", key, "err", err)
			return "", "", fmt.Errorf("authentication required: %w", err)
		}
	}

	// Check if we need to sync
	if m.isStale(key) {
		// Sync using singleflight (concurrent requests share same fetch)
		_, err, _ := m.group.Do("sync:"+key, func() (interface{}, error) {
			return nil, m.syncRepo(ctx, repoPath, upstreamURL, authHeader)
		})
		if err != nil {
			m.log.Warn("sync failed, serving stale", "repo", key, "err", err)
			// Continue serving stale data, but still report as hit
			return repoPath, StatusHit, nil
		}
		m.lastSync.Store(key, time.Now())
		return repoPath, StatusSync, nil
	}

	return repoPath, StatusHit, nil
}

// isStale returns true if the repo needs syncing.
func (m *Mirror) isStale(key string) bool {
	lastSync, ok := m.lastSync.Load(key)
	if !ok {
		return true
	}
	return time.Since(lastSync.(time.Time)) > m.staleAfter
}

// requiresAuth checks if a repo was cloned with authentication.
func (m *Mirror) requiresAuth(repoPath string) bool {
	_, err := os.Stat(filepath.Join(repoPath, ".requires-auth"))
	return err == nil
}

// markRequiresAuth marks a repo as requiring authentication.
func (m *Mirror) markRequiresAuth(repoPath string) error {
	return os.WriteFile(filepath.Join(repoPath, ".requires-auth"), []byte("1"), 0o644)
}

// validateAuth validates the auth token can access the upstream repo using git ls-remote.
func (m *Mirror) validateAuth(ctx context.Context, upstreamURL, authHeader string) error {
	args := []string{"ls-remote", "--exit-code", "-q"}
	if authHeader != "" {
		args = append(args, "-c", fmt.Sprintf("http.extraheader=Authorization: %s", authHeader))
	}
	args = append(args, upstreamURL, "HEAD")

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git ls-remote failed: %w\noutput: %s", err, output)
	}
	return nil
}

// cloneRepo creates a new bare mirror.
func (m *Mirror) cloneRepo(ctx context.Context, repoPath, upstreamURL, authHeader string) error {
	m.log.Info("cloning mirror", "path", repoPath, "upstream", upstreamURL, "hasAuth", authHeader != "")

	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	// Build git command with optional auth
	args := []string{"clone", "--bare", "--mirror"}
	if authHeader != "" {
		args = append(args, "-c", fmt.Sprintf("http.extraheader=Authorization: %s", authHeader))
	}
	args = append(args, upstreamURL, repoPath)

	cmd := exec.CommandContext(ctx, "git", args...)
	// Ignore global/system git config to avoid URL rewrite loops
	// (e.g., when user has insteadOf rules that would redirect back to us)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %w\noutput: %s", err, output)
	}

	// Mark repo as requiring auth if it was cloned with auth
	if authHeader != "" {
		if err := m.markRequiresAuth(repoPath); err != nil {
			m.log.Warn("failed to mark repo as requiring auth", "path", repoPath, "err", err)
		}
	}

	m.log.Info("clone complete", "path", repoPath)
	return nil
}

// syncRepo fetches updates from upstream.
func (m *Mirror) syncRepo(ctx context.Context, repoPath, upstreamURL, authHeader string) error {
	m.log.Debug("syncing mirror", "path", repoPath, "hasAuth", authHeader != "")

	// Build git command with optional auth
	args := []string{"-C", repoPath}
	if authHeader != "" {
		args = append(args, "-c", fmt.Sprintf("http.extraheader=Authorization: %s", authHeader))
	}
	args = append(args, "fetch", "--all", "--prune", "--force")

	cmd := exec.CommandContext(ctx, "git", args...)
	// Ignore global/system git config to avoid URL rewrite loops
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch failed: %w\noutput: %s", err, output)
	}

	m.log.Debug("sync complete", "path", repoPath)
	return nil
}

// GetRepoLock returns a mutex for the given repo (for exclusive operations).
func (m *Mirror) GetRepoLock(host, owner, repo string) *sync.Mutex {
	key := fmt.Sprintf("%s/%s/%s", host, owner, repo)
	lock, _ := m.repoLocks.LoadOrStore(key, &sync.Mutex{})
	return lock.(*sync.Mutex)
}
