package gitserve

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
)

// ServeInfoRefs handles GET /info/refs?service=git-upload-pack
// It runs git-upload-pack --stateless-rpc --advertise-refs and adds the pkt-line header.
func ServeInfoRefs(w http.ResponseWriter, r *http.Request, repoPath string, cacheStatus string) error {
	service := r.URL.Query().Get("service")
	if service != "git-upload-pack" {
		http.Error(w, "unsupported service", http.StatusBadRequest)
		return fmt.Errorf("unsupported service: %s", service)
	}

	w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	if cacheStatus != "" {
		w.Header().Set("X-Git-Proxy-Status", cacheStatus)
	}
	w.WriteHeader(http.StatusOK)

	// Write pkt-line service announcement
	// Format: 4-digit hex length + "# service=git-upload-pack\n" + flush
	announcement := "# service=git-upload-pack\n"
	pktLine := fmt.Sprintf("%04x%s", len(announcement)+4, announcement)
	if _, err := w.Write([]byte(pktLine)); err != nil {
		return err
	}
	// Flush packet (0000)
	if _, err := w.Write([]byte("0000")); err != nil {
		return err
	}

	// Run git upload-pack to get refs
	cmd := exec.CommandContext(r.Context(), "git", "upload-pack", "--stateless-rpc", "--advertise-refs", repoPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start git upload-pack: %w", err)
	}

	// Stream output to client
	if _, err := io.Copy(w, stdout); err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("copy stdout: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("wait git upload-pack: %w, stderr: %s", err, stderrBuf.String())
	}

	return nil
}

// ServeUploadPack handles POST /git-upload-pack
// It runs git-upload-pack --stateless-rpc with the request body as stdin.
func ServeUploadPack(w http.ResponseWriter, r *http.Request, repoPath string, cacheStatus string) error {
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	if cacheStatus != "" {
		w.Header().Set("X-Git-Proxy-Status", cacheStatus)
	}

	// Handle gzip-compressed request body
	var body io.Reader = r.Body
	if strings.Contains(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			return fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		body = gz
	}

	cmd := exec.CommandContext(r.Context(), "git", "upload-pack", "--stateless-rpc", repoPath)
	cmd.Stdin = body

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start git upload-pack: %w", err)
	}

	// Stream stdout to response
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, stdout); err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("copy stdout: %w, stderr: %s", err, stderrBuf.String())
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("wait git upload-pack: %w, stderr: %s", err, stderrBuf.String())
	}

	return nil
}
