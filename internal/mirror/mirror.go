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
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %w\noutput: %s", err, output)
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
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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
