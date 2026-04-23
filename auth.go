package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	AuthModeChatGPT = "chatgpt"
	AuthModeAPIKey  = "api_key"
)

type AuthProvider interface {
	Authorization(ctx context.Context) (string, error)
	Validate(ctx context.Context) error
	Name() string
}

type APIKeyAuthProvider struct {
	key string
}

func (p APIKeyAuthProvider) Authorization(ctx context.Context) (string, error) {
	if p.key == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is required for api_key auth mode")
	}
	return "Bearer " + p.key, nil
}

func (p APIKeyAuthProvider) Validate(ctx context.Context) error {
	_, err := p.Authorization(ctx)
	return err
}

func (p APIKeyAuthProvider) Name() string {
	return AuthModeAPIKey
}

type ChatGPTAuthProvider struct {
	authDir         string
	authFile        string
	refreshInterval time.Duration
	mu              sync.Mutex
	lastStatusCheck time.Time
}

func NewAuthProvider(cfg Config) (AuthProvider, error) {
	switch cfg.CodexAuthMode {
	case AuthModeChatGPT:
		authDir, err := expandPath(cfg.CodexHostAuthDir)
		if err != nil {
			return nil, err
		}
		return &ChatGPTAuthProvider{
			authDir:         authDir,
			authFile:        filepath.Join(authDir, "auth.json"),
			refreshInterval: time.Duration(cfg.CodexAuthRefreshIntervalSeconds) * time.Second,
		}, nil
	case AuthModeAPIKey:
		return APIKeyAuthProvider{key: cfg.OpenAIAPIKey}, nil
	default:
		return nil, fmt.Errorf("unsupported CODEX_AUTH_MODE %q", cfg.CodexAuthMode)
	}
}

func (p *ChatGPTAuthProvider) Authorization(ctx context.Context) (string, error) {
	if err := p.maybeRefresh(ctx); err != nil {
		return "", err
	}
	token, err := readCodexAccessToken(p.authFile)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("no ChatGPT access token found in %s; run codex login --device-auth on the host", p.authFile)
	}
	return "Bearer " + token, nil
}

func (p *ChatGPTAuthProvider) Validate(ctx context.Context) error {
	if _, err := os.Stat(p.authFile); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("missing Codex auth cache %s; run codex login --device-auth on the host", p.authFile)
		}
		return err
	}
	_, err := p.Authorization(ctx)
	return err
}

func (p *ChatGPTAuthProvider) Name() string {
	return AuthModeChatGPT
}

func (p *ChatGPTAuthProvider) maybeRefresh(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refreshInterval > 0 && time.Since(p.lastStatusCheck) < p.refreshInterval {
		return nil
	}
	statusCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(statusCtx, "codex", "login", "status")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+p.authDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("codex login status failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	p.lastStatusCheck = time.Now()
	return nil
}

func readCodexAccessToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	return findStringPath(raw, []string{"tokens", "access_token"}), nil
}

func readCodexAccountID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	return findStringPath(raw, []string{"tokens", "account_id"}), nil
}

func findStringPath(v any, path []string) string {
	if len(path) == 0 {
		if s, ok := v.(string); ok {
			return s
		}
		return ""
	}
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	return findStringPath(m[path[0]], path[1:])
}

func expandPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Abs(path)
}
