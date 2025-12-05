package gitproxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"log/slog"

	"github.com/crohr/smart-git-proxy/internal/cache"
	"github.com/crohr/smart-git-proxy/internal/config"
	"github.com/crohr/smart-git-proxy/internal/metrics"
	"github.com/crohr/smart-git-proxy/internal/upstream"
)

type Server struct {
	cfg     *config.Config
	cache   *cache.Cache
	up      *upstream.Client
	log     *slog.Logger
	metrics *metrics.Metrics
}

func New(cfg *config.Config, cache *cache.Cache, up *upstream.Client, log *slog.Logger, m *metrics.Metrics) *Server {
	return &Server{cfg: cfg, cache: cache, up: up, log: log, metrics: m}
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		s.log.Debug("incoming request", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)

		targetURL, repo, kind, err := s.resolveTarget(r)
		if err != nil {
			s.log.Error("resolve target failed", "err", err, "path", r.URL.Path)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.log.Debug("resolved target", "targetURL", targetURL, "repo", repo, "kind", kind)
		s.metrics.RequestsTotal.WithLabelValues(repo, string(kind), r.RemoteAddr).Inc()

		switch kind {
		case cache.KindInfo:
			s.handleInfoRefs(w, r, targetURL, repo, start)
		case cache.KindPack:
			s.handleUploadPack(w, r, targetURL, repo, start)
		default:
			http.Error(w, "unsupported path", http.StatusBadRequest)
		}
	})
}

func (s *Server) handleInfoRefs(w http.ResponseWriter, r *http.Request, targetURL, repo string, start time.Time) {
	service := r.URL.Query().Get("service")
	if service != "git-upload-pack" {
		http.Error(w, "unsupported service", http.StatusBadRequest)
		return
	}
	key := cacheKey(targetURL, service, r.Header.Get("Git-Protocol"))

	if f, _, err := s.cache.Get(repo, cache.KindInfo, key); err == nil {
		defer f.Close()
		s.metrics.CacheHits.WithLabelValues(repo, string(cache.KindInfo)).Inc()
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)
		s.metrics.ResponsesTotal.WithLabelValues(repo, string(cache.KindInfo), "200").Inc()
		return
	}
	s.metrics.CacheMisses.WithLabelValues(repo, string(cache.KindInfo)).Inc()

	headers := s.forwardHeaders(r, true)
	resp, err := s.up.Do(r.Context(), http.MethodGet, targetURL, nil, headers)
	if err != nil {
		s.fail(w, repo, cache.KindInfo, err)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	writer, err := s.cache.NewWriter(repo, cache.KindInfo, key)
	if err != nil {
		s.log.Warn("cache writer init failed", "err", err, "repo", repo)
	}

	var dst io.Writer = w
	if writer != nil {
		dst = io.MultiWriter(w, writer)
	}

	n, copyErr := io.Copy(dst, resp.Body)
	s.metrics.UpstreamBytes.WithLabelValues(repo, string(cache.KindInfo)).Add(float64(n))
	s.metrics.UpstreamLatency.WithLabelValues(repo, string(cache.KindInfo)).Observe(time.Since(start).Seconds())

	if writer != nil {
		if copyErr == nil && resp.StatusCode == http.StatusOK {
			if err := writer.Commit(); err != nil {
				s.log.Warn("cache commit failed", "err", err, "repo", repo)
			}
		} else {
			writer.Abort()
		}
	}
	if copyErr != nil {
		s.log.Warn("stream failed", "err", copyErr, "repo", repo)
	}
	s.metrics.ResponsesTotal.WithLabelValues(repo, string(cache.KindInfo), fmt.Sprintf("%d", resp.StatusCode)).Inc()
}

func (s *Server) handleUploadPack(w http.ResponseWriter, r *http.Request, targetURL, repo string, start time.Time) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		s.fail(w, repo, cache.KindPack, err)
		return
	}
	bodyHash := sha256.Sum256(bodyBytes)
	key := cacheKey(targetURL, hex.EncodeToString(bodyHash[:]), r.Header.Get("Git-Protocol"))

	if f, _, err := s.cache.Get(repo, cache.KindPack, key); err == nil {
		defer f.Close()
		s.metrics.CacheHits.WithLabelValues(repo, string(cache.KindPack)).Inc()
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)
		s.metrics.ResponsesTotal.WithLabelValues(repo, string(cache.KindPack), "200").Inc()
		return
	}
	s.metrics.CacheMisses.WithLabelValues(repo, string(cache.KindPack)).Inc()

	headers := s.forwardHeaders(r, false)
	s.log.Debug("upload-pack request", "url", targetURL, "bodyLen", len(bodyBytes))
	resp, err := s.up.Do(r.Context(), http.MethodPost, targetURL, bytes.NewReader(bodyBytes), headers)
	if err != nil {
		s.fail(w, repo, cache.KindPack, err)
		return
	}
	defer resp.Body.Close()
	s.log.Debug("upload-pack response", "status", resp.StatusCode, "contentType", resp.Header.Get("Content-Type"))

	// Log error response body for debugging
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		s.log.Error("upstream error response", "status", resp.StatusCode, "body", string(errBody))
		// Create a new reader with the error body so it can still be streamed to client
		resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(errBody), resp.Body))
	}

	if resp.ContentLength > 0 && s.cfg.MaxPackSizeBytes > 0 && resp.ContentLength > s.cfg.MaxPackSizeBytes {
		http.Error(w, "upstream pack too large", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	writer, err := s.cache.NewWriter(repo, cache.KindPack, key)
	if err != nil {
		s.log.Warn("cache writer init failed", "err", err, "repo", repo)
	}

	var dst io.Writer = w
	if writer != nil {
		dst = io.MultiWriter(w, writer)
	}
	n, copyErr := io.Copy(dst, resp.Body)
	s.metrics.UpstreamBytes.WithLabelValues(repo, string(cache.KindPack)).Add(float64(n))
	s.metrics.UpstreamLatency.WithLabelValues(repo, string(cache.KindPack)).Observe(time.Since(start).Seconds())

	if writer != nil {
		if copyErr == nil && resp.StatusCode == http.StatusOK {
			if err := writer.Commit(); err != nil {
				s.log.Warn("cache commit failed", "err", err, "repo", repo)
			}
		} else {
			writer.Abort()
		}
	}
	if copyErr != nil {
		s.log.Warn("stream failed", "err", copyErr, "repo", repo)
	}
	s.metrics.ResponsesTotal.WithLabelValues(repo, string(cache.KindPack), fmt.Sprintf("%d", resp.StatusCode)).Inc()
}

func (s *Server) forwardHeaders(r *http.Request, isInfo bool) http.Header {
	h := http.Header{}
	forward := []string{"Git-Protocol", "User-Agent", "Accept", "Content-Encoding", "Accept-Encoding"}
	for _, k := range forward {
		if v := r.Header.Values(k); len(v) > 0 {
			for _, vv := range v {
				h.Add(k, vv)
			}
		}
	}

	authMode := s.cfg.AuthMode
	switch authMode {
	case "pass-through":
		if v := r.Header.Get("Authorization"); v != "" {
			h.Set("Authorization", v)
		}
	case "static":
		h.Set("Authorization", "Bearer "+s.cfg.StaticToken)
	case "none":
		// nothing
	}

	if isInfo {
		h.Set("Accept", "application/x-git-upload-pack-advertisement")
	} else {
		h.Set("Content-Type", "application/x-git-upload-pack-request")
		h.Set("Accept", "application/x-git-upload-pack-result")
	}
	return h
}

func (s *Server) resolveTarget(r *http.Request) (string, string, cache.Kind, error) {
	// Path format: /{host}/{owner}/{repo}/info/refs or /{host}/{owner}/{repo}/git-upload-pack
	pathStr := strings.TrimPrefix(r.URL.Path, "/")
	if pathStr == "" {
		return "", "", "", errors.New("empty path")
	}

	// Extract hostname from first path segment
	parts := strings.SplitN(pathStr, "/", 2)
	if len(parts) < 2 {
		return "", "", "", errors.New("invalid path: expected /{host}/{owner}/{repo}/...")
	}
	host := parts[0]
	rest := parts[1]

	// Validate against allowed upstreams
	allowed := false
	for _, h := range s.cfg.AllowedUpstreams {
		if h == host {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", "", "", fmt.Errorf("upstream %q not in allowed list", host)
	}

	// Build full URL (always https)
	targetStr := "https://" + host + "/" + rest
	if r.URL.RawQuery != "" {
		targetStr = targetStr + "?" + r.URL.RawQuery
	}

	u, err := url.Parse(targetStr)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid target url: %w", err)
	}

	kind := cache.KindPack
	switch {
	case strings.HasSuffix(u.Path, "/info/refs"):
		kind = cache.KindInfo
	case strings.HasSuffix(u.Path, "/git-upload-pack"):
		kind = cache.KindPack
	default:
		return "", "", "", fmt.Errorf("unsupported endpoint: %s", u.Path)
	}

	repoPath := strings.TrimPrefix(u.Path, "/")
	repoPath = strings.TrimSuffix(repoPath, "/info/refs")
	repoPath = strings.TrimSuffix(repoPath, "/git-upload-pack")
	repo := path.Join(u.Host, repoPath)

	return u.String(), repo, kind, nil
}

func cacheKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) fail(w http.ResponseWriter, repo string, kind cache.Kind, err error) {
	s.metrics.ErrorsTotal.WithLabelValues(repo, string(kind)).Inc()
	s.log.Error("request failed", "err", err, "repo", repo, "kind", kind)
	http.Error(w, err.Error(), http.StatusBadGateway)
}
