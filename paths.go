package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var safeFilenameRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func NewID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func ContextDir(cfg Config, contextID string) string {
	return filepath.Join(cfg.DataDir, "contexts", contextID)
}

func ContextWorkspaceDir(cfg Config, contextID string) string {
	return filepath.Join(ContextDir(cfg, contextID), "workspace")
}

func ContextCodexDir(cfg Config, contextID string) string {
	return filepath.Join(ContextDir(cfg, contextID), ".codex")
}

func ContextRunsDir(cfg Config, contextID string) string {
	return filepath.Join(ContextDir(cfg, contextID), "runs")
}

func ValidateContextPath(cfg Config, contextID, p string) (string, error) {
	if strings.ContainsRune(p, 0) || strings.Contains(p, "..") || strings.Contains(p, ":") || strings.Contains(p, ",") {
		return "", fmt.Errorf("unsafe path fragment")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(ContextDir(cfg, contextID))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
		return resolved, nil
	}
	return "", fmt.Errorf("path escapes context root: %s", p)
}

func ValidateMountTarget(target string) error {
	switch target {
	case "/home/agent/workspace", "/home/agent/.codex", "/run-artifacts":
		return nil
	default:
		return fmt.Errorf("unsupported mount target: %s", target)
	}
}

func ResolveWorkspaceFile(cfg Config, contextID, relPath string, maxBytes int64) (string, error) {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return "", fmt.Errorf("missing workspace-relative path")
	}
	if strings.ContainsRune(relPath, 0) || strings.Contains(relPath, ":") || strings.Contains(relPath, ",") {
		return "", fmt.Errorf("unsafe path fragment")
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("path must be relative to the workspace")
	}
	clean := filepath.Clean(relPath)
	if clean == "." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || clean == ".." {
		return "", fmt.Errorf("path escapes workspace")
	}
	root, err := filepath.EvalSymlinks(ContextWorkspaceDir(cfg, contextID))
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes workspace")
	}
	st, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file")
	}
	if maxBytes > 0 && st.Size() > maxBytes {
		return "", fmt.Errorf("file exceeds max size")
	}
	return resolved, nil
}

func SanitizeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "\x00", "")
	name = safeFilenameRE.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._-")
	if name == "" {
		name = "attachment"
	}
	if len(name) > 120 {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if len(ext) > 20 {
			ext = ""
		}
		maxBase := 120 - len(ext)
		if maxBase < 1 {
			maxBase = 1
		}
		name = base[:maxBase] + ext
	}
	return name
}

func ValidatePublicHTTPSRepo(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" {
		return fmt.Errorf("repo URL must be https")
	}
	if u.User != nil {
		return fmt.Errorf("repo URL must not include credentials")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("repo URL host is not public")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return fmt.Errorf("repo URL host is not public")
		}
	}
	return nil
}
