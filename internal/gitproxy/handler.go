package gitproxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/crohr/smart-git-proxy/internal/config"
	"github.com/crohr/smart-git-proxy/internal/gitserve"
	"github.com/crohr/smart-git-proxy/internal/metrics"
	"github.com/crohr/smart-git-proxy/internal/mirror"
)

// Kind represents the type of git request.
type Kind string

const (
	KindInfo Kind = "info"
	KindPack Kind = "pack"
)

type Server struct {
	cfg     *config.Config
	mirror  *mirror.Mirror
	log     *slog.Logger
	metrics *metrics.Metrics

	// Track last cache status per repo for display in upload-pack
	statusCache sync.Map // map[repoKey]mirror.Status
}

func New(cfg *config.Config, m *mirror.Mirror, log *slog.Logger, metrics *metrics.Metrics) *Server {
	return &Server{cfg: cfg, mirror: m, log: log, metrics: metrics}
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		s.log.Debug("incoming request", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)

		host, owner, repo, kind, err := s.resolveTarget(r)
		if err != nil {
			s.log.Error("resolve target failed", "err", err, "path", r.URL.Path)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		repoKey := fmt.Sprintf("%s/%s/%s", host, owner, repo)
		s.log.Debug("resolved target", "host", host, "owner", owner, "repo", repo, "kind", kind)
		s.metrics.RequestsTotal.WithLabelValues(repoKey, string(kind), r.RemoteAddr).Inc()

		switch kind {
		case KindInfo:
			s.handleInfoRefs(w, r, host, owner, repo, repoKey, start)
		case KindPack:
			s.handleUploadPack(w, r, host, owner, repo, repoKey, start)
		default:
			http.Error(w, "unsupported path", http.StatusBadRequest)
		}
	})
}

func (s *Server) handleInfoRefs(w http.ResponseWriter, r *http.Request, host, owner, repo, repoKey string, start time.Time) {
	service := r.URL.Query().Get("service")
	if service != "git-upload-pack" {
		http.Error(w, "unsupported service", http.StatusBadRequest)
		return
	}

	// Build upstream URL for cloning/syncing
	upstreamURL := fmt.Sprintf("https://%s/%s/%s.git", host, owner, repo)

	// Get auth header from request (pass-through to upstream)
	authHeader := r.Header.Get("Authorization")
	s.log.Debug("auth check", "hasAuth", authHeader != "", "repo", repoKey)

	// Ensure mirror is synced
	repoPath, status, err := s.mirror.EnsureRepo(r.Context(), host, owner, repo, upstreamURL, authHeader)
	if err != nil {
		s.fail(w, repoKey, KindInfo, err)
		return
	}

	// Store status for the upcoming upload-pack request
	s.statusCache.Store(repoKey, status)
	s.log.Info("request", "repo", repoKey, "status", status)

	// Serve refs from local mirror
	if err := gitserve.ServeInfoRefs(w, r, repoPath, string(status)); err != nil {
		s.log.Error("serve info/refs failed", "err", err, "repo", repoKey)
		// Response already started, can't change status
	}

	s.metrics.ResponsesTotal.WithLabelValues(repoKey, string(KindInfo), "200").Inc()
	s.metrics.UpstreamLatency.WithLabelValues(repoKey, string(KindInfo)).Observe(time.Since(start).Seconds())
}

func (s *Server) handleUploadPack(w http.ResponseWriter, r *http.Request, host, owner, repo, repoKey string, start time.Time) {
	// Get mirror path (should already exist from info/refs)
	repoPath := s.mirror.RepoPath(host, owner, repo)

	// Get cached status from info/refs call
	cacheStatus := ""
	if v, ok := s.statusCache.Load(repoKey); ok {
		cacheStatus = string(v.(mirror.Status))
	}

	// Serve pack from local mirror
	if err := gitserve.ServeUploadPack(w, r, repoPath, cacheStatus); err != nil {
		s.log.Error("serve upload-pack failed", "err", err, "repo", repoKey)
		// Response already started, can't change status
	}

	s.metrics.ResponsesTotal.WithLabelValues(repoKey, string(KindPack), "200").Inc()
	s.metrics.UpstreamLatency.WithLabelValues(repoKey, string(KindPack)).Observe(time.Since(start).Seconds())
}

func (s *Server) resolveTarget(r *http.Request) (host, owner, repo string, kind Kind, err error) {
	// Path format: /{host}/{owner}/{repo}/info/refs or /{host}/{owner}/{repo}/git-upload-pack
	pathStr := strings.TrimPrefix(r.URL.Path, "/")
	if pathStr == "" {
		return "", "", "", "", errors.New("empty path")
	}

	// Parse the path to extract components
	u, err := url.Parse("https://placeholder/" + pathStr)
	if err != nil {
		return "", "", "", "", fmt.Errorf("invalid path: %w", err)
	}

	// Determine kind from suffix
	switch {
	case strings.HasSuffix(u.Path, "/info/refs"):
		kind = KindInfo
	case strings.HasSuffix(u.Path, "/git-upload-pack"):
		kind = KindPack
	default:
		return "", "", "", "", fmt.Errorf("unsupported endpoint: %s", u.Path)
	}

	// Remove git endpoint suffix to get repo path
	repoPath := strings.TrimPrefix(u.Path, "/")
	repoPath = strings.TrimSuffix(repoPath, "/info/refs")
	repoPath = strings.TrimSuffix(repoPath, "/git-upload-pack")
	repoPath = strings.TrimSuffix(repoPath, ".git")

	// Split into host/owner/repo
	parts := strings.SplitN(repoPath, "/", 3)
	if len(parts) < 3 {
		return "", "", "", "", errors.New("invalid path: expected /{host}/{owner}/{repo}/...")
	}
	host = parts[0]
	owner = parts[1]
	repo = parts[2]

	// Handle nested paths (e.g., owner/repo/subgroup)
	if strings.Contains(repo, "/") {
		// For GitLab-style nested groups, combine them
		repo = path.Base(repo)
	}

	// Validate against allowed upstreams
	allowed := false
	for _, h := range s.cfg.AllowedUpstreams {
		if h == host {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", "", "", "", fmt.Errorf("upstream %q not in allowed list", host)
	}

	return host, owner, repo, kind, nil
}

func (s *Server) fail(w http.ResponseWriter, repo string, kind Kind, err error) {
	s.metrics.ErrorsTotal.WithLabelValues(repo, string(kind)).Inc()
	s.log.Error("request failed", "err", err, "repo", repo, "kind", kind)
	http.Error(w, err.Error(), http.StatusBadGateway)
}
