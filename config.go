package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TelegramBotToken                string
	CodexAuthMode                   string
	CodexHostAuthDir                string
	CodexAuthRefreshIntervalSeconds int
	OpenAIAPIKey                    string
	OpenAIProxyHost                 string
	OpenAIProxyBindHost             string
	OpenAIProxyPort                 int
	OpenAIProxyClientToken          string
	OpenAIUpstreamBaseURL           string
	ChatGPTCodexUpstreamBaseURL     string
	DefaultCodexModel               string
	DefaultReasoningEffort          string
	DataDir                         string
	AdminUserIDs                    map[int64]bool
	MaxConcurrentContainers         int
	MaxRunSeconds                   int
	MaxOutputBytes                  int64
	MaxHistoryMessages              int
	MaxAttachmentBytes              int64
	ProgressUpdates                 bool
	ProgressIntervalSeconds         int
	AgentMessagesEnabled            bool
	AgentMessageMaxPerRun           int
	AgentMessageMaxChars            int
	AgentMessagePollIntervalMS      int
	ServiceTimezone                 *time.Location
	ContainerImage                  string
	SkipDockerBuild                 bool
	SkillsDir                       string
	AgentsFile                      string
}

const defaultReasoningEffort = "xhigh"

func normalizeReasoningEffort(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return "low", true
	case "medium":
		return "medium", true
	case "high":
		return "high", true
	case "xhigh":
		return "xhigh", true
	default:
		return "", false
	}
}

func reasoningEffortChoices() string {
	return "low, medium, high, xhigh"
}

func effectiveReasoningEffort(cfg Config, value string) string {
	if effort, ok := normalizeReasoningEffort(value); ok {
		return effort
	}
	if effort, ok := normalizeReasoningEffort(cfg.DefaultReasoningEffort); ok {
		return effort
	}
	return defaultReasoningEffort
}

func LoadConfig() (Config, error) {
	_ = loadDotEnv(".env")
	dataDir := envString("DATA_DIR", "./data")
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve DATA_DIR: %w", err)
	}
	locName := envString("SERVICE_TIMEZONE", "UTC")
	loc, err := time.LoadLocation(locName)
	if err != nil {
		return Config{}, fmt.Errorf("invalid SERVICE_TIMEZONE %q: %w", locName, err)
	}
	authMode := strings.ToLower(envString("CODEX_AUTH_MODE", AuthModeChatGPT))
	proxyClientToken := envString("OPENAI_PROXY_CLIENT_TOKEN", randomToken())
	if authMode == AuthModeChatGPT {
		proxyClientToken = chatGPTProxyClientToken(proxyClientToken)
	}
	upstream := envString("OPENAI_UPSTREAM_BASE_URL", "https://api.openai.com/v1")
	if _, err := url.ParseRequestURI(upstream); err != nil {
		return Config{}, fmt.Errorf("invalid OPENAI_UPSTREAM_BASE_URL: %w", err)
	}
	chatGPTUpstream := envString("CHATGPT_CODEX_UPSTREAM_BASE_URL", "https://chatgpt.com/backend-api/codex")
	if _, err := url.ParseRequestURI(chatGPTUpstream); err != nil {
		return Config{}, fmt.Errorf("invalid CHATGPT_CODEX_UPSTREAM_BASE_URL: %w", err)
	}
	defaultEffort, ok := normalizeReasoningEffort(envString("DEFAULT_REASONING_EFFORT", defaultReasoningEffort))
	if !ok {
		return Config{}, fmt.Errorf("invalid DEFAULT_REASONING_EFFORT: must be one of %s", reasoningEffortChoices())
	}
	cfg := Config{
		TelegramBotToken:                os.Getenv("TELEGRAM_BOT_TOKEN"),
		CodexAuthMode:                   authMode,
		CodexHostAuthDir:                envString("CODEX_HOST_AUTH_DIR", "~/.codex"),
		CodexAuthRefreshIntervalSeconds: envInt("CODEX_AUTH_REFRESH_INTERVAL_SECONDS", 300),
		OpenAIAPIKey:                    os.Getenv("OPENAI_API_KEY"),
		OpenAIProxyHost:                 envString("OPENAI_PROXY_HOST", "127.0.0.1"),
		OpenAIProxyBindHost:             envString("OPENAI_PROXY_BIND_HOST", ""),
		OpenAIProxyPort:                 envInt("OPENAI_PROXY_PORT", 3021),
		OpenAIProxyClientToken:          proxyClientToken,
		OpenAIUpstreamBaseURL:           upstream,
		ChatGPTCodexUpstreamBaseURL:     chatGPTUpstream,
		DefaultCodexModel:               envString("DEFAULT_CODEX_MODEL", "gpt-5.4-mini"),
		DefaultReasoningEffort:          defaultEffort,
		DataDir:                         absDataDir,
		AdminUserIDs:                    parseAdminIDs(os.Getenv("ADMIN_USER_IDS")),
		MaxConcurrentContainers:         envInt("MAX_CONCURRENT_CONTAINERS", 3),
		MaxRunSeconds:                   envInt("MAX_RUN_SECONDS", 1800),
		MaxOutputBytes:                  int64(envInt("MAX_OUTPUT_BYTES", 200000)),
		MaxHistoryMessages:              envInt("MAX_HISTORY_MESSAGES", 20),
		MaxAttachmentBytes:              int64(envInt("MAX_ATTACHMENT_BYTES", 25000000)),
		ProgressUpdates:                 envBool("PROGRESS_UPDATES", true),
		ProgressIntervalSeconds:         envInt("PROGRESS_INTERVAL_SECONDS", 300),
		AgentMessagesEnabled:            envBool("AGENT_MESSAGES_ENABLED", true),
		AgentMessageMaxPerRun:           envInt("AGENT_MESSAGE_MAX_PER_RUN", 5),
		AgentMessageMaxChars:            envInt("AGENT_MESSAGE_MAX_CHARS", 1000),
		AgentMessagePollIntervalMS:      envInt("AGENT_MESSAGE_POLL_INTERVAL_MS", 500),
		ServiceTimezone:                 loc,
		ContainerImage:                  envString("CONTAINER_IMAGE", "servitor-agent:latest"),
		SkipDockerBuild:                 envBool("SERVITOR_SKIP_DOCKER_BUILD", false),
		SkillsDir:                       envString("SKILLS_DIR", filepath.Join(envString("CODEX_HOST_AUTH_DIR", "~/.codex"), "skills")),
		AgentsFile:                      envString("AGENTS_FILE", filepath.Join(envString("CODEX_HOST_AUTH_DIR", "~/.codex"), "AGENTS.md")),
	}
	if cfg.MaxConcurrentContainers < 1 {
		cfg.MaxConcurrentContainers = 1
	}
	if cfg.MaxHistoryMessages < 1 {
		cfg.MaxHistoryMessages = 1
	}
	if cfg.ProgressIntervalSeconds < 30 {
		cfg.ProgressIntervalSeconds = 30
	}
	if cfg.AgentMessageMaxPerRun < 0 {
		cfg.AgentMessageMaxPerRun = 0
	}
	if cfg.AgentMessageMaxChars < 100 {
		cfg.AgentMessageMaxChars = 100
	}
	if cfg.AgentMessagePollIntervalMS < 100 {
		cfg.AgentMessagePollIntervalMS = 100
	}
	return cfg, nil
}

func (c Config) ProxyListenAddr() string {
	host := strings.TrimSpace(c.OpenAIProxyBindHost)
	if host == "" {
		host = c.OpenAIProxyHost
		if isLoopbackHost(host) {
			if bridge := dockerBridgeBindHost(); bridge != "" {
				host = bridge
			}
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(c.OpenAIProxyPort))
}

func (c Config) ContainerProxyBaseURL() string {
	return fmt.Sprintf("http://host.docker.internal:%d/v1", c.OpenAIProxyPort)
}

func (c Config) ContainerProxyChatGPTBaseURL() string {
	return fmt.Sprintf("http://host.docker.internal:%d/backend-api/codex", c.OpenAIProxyPort)
}

func (c Config) ProxyUpstreamBaseURL() string {
	if c.CodexAuthMode == AuthModeChatGPT {
		return c.ChatGPTCodexUpstreamBaseURL
	}
	return c.OpenAIUpstreamBaseURL
}

func (c Config) ValidateForRun() error {
	var missing []string
	if c.TelegramBotToken == "" {
		missing = append(missing, "TELEGRAM_BOT_TOKEN")
	}
	if c.CodexAuthMode == AuthModeAPIKey && c.OpenAIAPIKey == "" {
		missing = append(missing, "OPENAI_API_KEY")
	}
	if c.CodexAuthMode == AuthModeChatGPT && c.CodexHostAuthDir == "" {
		missing = append(missing, "CODEX_HOST_AUTH_DIR")
	}
	if len(c.AdminUserIDs) == 0 {
		missing = append(missing, "ADMIN_USER_IDS")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
	}
	if isLoopbackHost(c.OpenAIProxyHost) && c.OpenAIProxyBindHost == "" && dockerBridgeBindHost() == "" {
		return fmt.Errorf("OPENAI_PROXY_BIND_HOST is required because OPENAI_PROXY_HOST=%q is loopback and no docker bridge address was detected", c.OpenAIProxyHost)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func dockerBridgeBindHost() string {
	iface, err := net.InterfaceByName("docker0")
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() {
			return ip4.String()
		}
	}
	return ""
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k != "" && os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
	return scanner.Err()
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "yes"
}

func randomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "servitor-placeholder"
	}
	return "servitor-" + hex.EncodeToString(b[:])
}

func chatGPTProxyClientToken(seed string) string {
	if tokenHasUsableJWTExpiry(seed) {
		return seed
	}
	digest := sha256.Sum256([]byte(seed))
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{
		"aud": "chatgpt",
		"exp": int64(4102444800), // 2100-01-01T00:00:00Z
		"iat": int64(1735689600), // 2025-01-01T00:00:00Z
		"iss": "servitor",
		"sub": "servitor-" + hex.EncodeToString(digest[:12]),
	})
	signature := base64.RawURLEncoding.EncodeToString(digest[:])
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + signature
}

func tokenHasUsableJWTExpiry(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return false
	}
	return claims.Exp > time.Now().Add(10*time.Minute).Unix()
}

func parseAdminIDs(raw string) map[int64]bool {
	out := make(map[int64]bool)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err == nil {
			out[id] = true
		}
	}
	return out
}
