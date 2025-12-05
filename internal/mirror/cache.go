package mirror

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/crohr/smart-git-proxy/internal/config"
)

const (
	// DefaultMaxSizePercent is the default percentage of available disk space to use
	DefaultMaxSizePercent = 80.0
	// MinFreeSpace is the minimum free space to maintain (1GB)
	MinFreeSpace = 1024 * 1024 * 1024
)

// Cache manages LRU eviction of mirror repositories.
type Cache struct {
	root       string
	maxSize    config.SizeSpec
	log        *slog.Logger
	mu         sync.Mutex
	accessTime sync.Map // map[repoKey]time.Time
}

// NewCache creates a new cache manager.
func NewCache(root string, maxSize config.SizeSpec, log *slog.Logger) *Cache {
	return &Cache{
		root:    root,
		maxSize: maxSize,
		log:     log,
	}
}

// Touch updates the access time for a repository.
func (c *Cache) Touch(key string) {
	c.accessTime.Store(key, time.Now())
}

// MaybeEvict checks disk usage and evicts LRU repositories if needed.
// Should be called after cloning a new repo.
func (c *Cache) MaybeEvict() {
	c.mu.Lock()
	defer c.mu.Unlock()

	maxBytes := c.getMaxSize()
	if maxBytes <= 0 {
		return // No limit configured and couldn't determine disk size
	}

	currentSize, err := c.getDirSize()
	if err != nil {
		c.log.Warn("failed to get mirror dir size", "err", err)
		return
	}

	if currentSize <= maxBytes {
		c.log.Debug("cache size within limits", "current", formatSize(currentSize), "max", formatSize(maxBytes))
		return
	}

	c.log.Info("cache size exceeded, starting eviction", "current", formatSize(currentSize), "max", formatSize(maxBytes))

	// Get all repos sorted by access time (oldest first)
	repos, err := c.listReposWithAccessTime()
	if err != nil {
		c.log.Warn("failed to list repos for eviction", "err", err)
		return
	}

	// Sort by access time (oldest first)
	sort.Slice(repos, func(i, j int) bool {
		return repos[i].accessTime.Before(repos[j].accessTime)
	})

	// Evict until we're under the limit
	targetSize := int64(float64(maxBytes) * 0.90) // Aim for 90% of max to avoid thrashing
	for _, repo := range repos {
		if currentSize <= targetSize {
			break
		}

		repoSize, err := getDirSize(repo.path)
		if err != nil {
			c.log.Warn("failed to get repo size", "path", repo.path, "err", err)
			continue
		}

		c.log.Info("evicting repo", "key", repo.key, "size", formatSize(repoSize), "lastAccess", repo.accessTime)
		if err := os.RemoveAll(repo.path); err != nil {
			c.log.Warn("failed to remove repo", "path", repo.path, "err", err)
			continue
		}

		// Clean up empty parent directories
		c.cleanEmptyParents(repo.path)

		currentSize -= repoSize
		c.accessTime.Delete(repo.key)
	}

	c.log.Info("eviction complete", "newSize", formatSize(currentSize))
}

type repoInfo struct {
	key        string
	path       string
	accessTime time.Time
}

// listReposWithAccessTime returns all repos with their access times.
func (c *Cache) listReposWithAccessTime() ([]repoInfo, error) {
	var repos []repoInfo

	// Walk the mirror directory looking for .git directories
	err := filepath.WalkDir(c.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		// Look for bare repos (directories ending in .git or containing HEAD file)
		if d.IsDir() && filepath.Ext(path) == ".git" {
			// Check if it's actually a git repo
			if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
				key := c.pathToKey(path)
				accessTime := c.getAccessTime(key, path)
				repos = append(repos, repoInfo{
					key:        key,
					path:       path,
					accessTime: accessTime,
				})
				return filepath.SkipDir
			}
		}
		return nil
	})

	return repos, err
}

// pathToKey converts a repo path back to a key (host/owner/repo).
func (c *Cache) pathToKey(path string) string {
	rel, err := filepath.Rel(c.root, path)
	if err != nil {
		return path
	}
	// Remove .git suffix
	rel = rel[:len(rel)-4]
	return rel
}

// getAccessTime returns the access time for a repo, falling back to mtime.
func (c *Cache) getAccessTime(key, path string) time.Time {
	if t, ok := c.accessTime.Load(key); ok {
		return t.(time.Time)
	}

	// Fall back to modification time of HEAD file
	info, err := os.Stat(filepath.Join(path, "HEAD"))
	if err == nil {
		return info.ModTime()
	}

	// Fall back to directory modification time
	info, err = os.Stat(path)
	if err == nil {
		return info.ModTime()
	}

	return time.Time{}
}

// getMaxSize returns the maximum size in bytes.
func (c *Cache) getMaxSize() int64 {
	// Get disk stats for percentage calculations
	var stat syscall.Statfs_t
	if err := syscall.Statfs(c.root, &stat); err != nil {
		c.log.Warn("failed to get disk stats", "err", err)
		return 0
	}
	available := int64(stat.Bavail) * int64(stat.Bsize)

	var totalUsable int64

	if !c.maxSize.IsZero() {
		if c.maxSize.IsPercent() {
			// Use configured percentage
			totalUsable = int64(float64(available) * c.maxSize.Percent / 100.0)
		} else {
			// Use absolute size
			return c.maxSize.Bytes
		}
	} else {
		// Default: 80% of available disk space
		totalUsable = int64(float64(available) * DefaultMaxSizePercent / 100.0)
	}

	// Ensure we leave at least MinFreeSpace
	if available-totalUsable < MinFreeSpace {
		totalUsable = available - MinFreeSpace
	}
	if totalUsable < 0 {
		totalUsable = 0
	}

	c.log.Debug("calculated max cache size", "available", formatSize(available), "max", formatSize(totalUsable))
	return totalUsable
}

// getDirSize returns the total size of the mirror directory.
func (c *Cache) getDirSize() (int64, error) {
	return getDirSize(c.root)
}

// getDirSize returns the total size of a directory.
func getDirSize(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size, err
}

// cleanEmptyParents removes empty parent directories up to the root.
func (c *Cache) cleanEmptyParents(path string) {
	dir := filepath.Dir(path)
	for dir != c.root && dir != "." && dir != "/" {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(dir); err != nil {
			break
		}
		dir = filepath.Dir(dir)
	}
}

// formatSize formats bytes into human-readable string.
func formatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
