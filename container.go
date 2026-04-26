package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type DockerRunner struct {
	cfg      Config
	redactor *Redactor
}

func NewDockerRunner(cfg Config, redactor *Redactor) *DockerRunner {
	return &DockerRunner{cfg: cfg, redactor: redactor}
}

func (r *DockerRunner) BuildImage(ctx context.Context) error {
	if r.cfg.SkipDockerBuild {
		return nil
	}
	cmd := exec.CommandContext(ctx, "docker", "build", "-f", "Dockerfile.agent", "-t", r.cfg.ContainerImage, ".")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build: %s: %w", r.redactor.Redact(out.String()), err)
	}
	return nil
}

func (r *DockerRunner) Run(ctx context.Context, dbRunID int64, c Context, prompt string, resumeSession string, onAgentMessage AgentMessageHandler, onAgentAction AgentActionHandler) RunResult {
	artifactDir := filepath.Join(ContextRunsDir(r.cfg, c.ID), fmt.Sprintf("%d", dbRunID))
	result := RunResult{ArtifactDir: artifactDir, ContainerID: fmt.Sprintf("servitor-%d", dbRunID)}
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		result.ErrorText = err.Error()
		result.FailureClass = "artifact_setup"
		result.Retryable = false
		return result
	}
	promptPath := filepath.Join(artifactDir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		result.ErrorText = err.Error()
		result.FailureClass = "artifact_setup"
		return result
	}
	if err := writeCodexConfig(r.cfg, c.ID, c.ReasoningEffort); err != nil {
		result.ErrorText = err.Error()
		result.FailureClass = "codex_config"
		return result
	}
	workspace, err := ValidateContextPath(r.cfg, c.ID, c.WorkspaceDir)
	if err != nil {
		result.ErrorText = err.Error()
		result.FailureClass = "mount_validation"
		return result
	}
	codexDir, err := ValidateContextPath(r.cfg, c.ID, ContextCodexDir(r.cfg, c.ID))
	if err != nil {
		result.ErrorText = err.Error()
		result.FailureClass = "mount_validation"
		return result
	}
	artifacts, err := ValidateContextPath(r.cfg, c.ID, artifactDir)
	if err != nil {
		result.ErrorText = err.Error()
		result.FailureClass = "mount_validation"
		return result
	}
	for _, target := range []string{"/home/agent/workspace", "/home/agent/.codex", "/run-artifacts"} {
		if err := ValidateMountTarget(target); err != nil {
			result.ErrorText = err.Error()
			result.FailureClass = "mount_validation"
			return result
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(r.cfg.MaxRunSeconds)*time.Second)
	defer cancel()
	args := []string{
		"run", "--rm",
		"--name", result.ContainerID,
		"--cidfile", filepath.Join(artifactDir, "container.cid"),
		"--add-host", "host.docker.internal:host-gateway",
		"--network", "bridge",
		"--memory", "2g",
		"--pids-limit", "512",
		"--cpus", "2",
		"-e", "SERVITOR_CODEX_SESSION_ID=" + resumeSession,
		"-e", "SERVITOR_AGENT_MESSAGES_FILE=/run-artifacts/agent_messages.jsonl",
		"-e", "SERVITOR_AGENT_ACTIONS_FILE=/run-artifacts/agent_actions.jsonl",
		"-e", fmt.Sprintf("SERVITOR_AGENT_MESSAGE_MAX_CHARS=%d", r.cfg.AgentMessageMaxChars),
		"-e", fmt.Sprintf("SERVITOR_AGENT_MESSAGE_MAX_COUNT=%d", r.cfg.AgentMessageMaxPerRun),
	}
	if r.cfg.CodexAuthMode == AuthModeAPIKey {
		args = append(args,
			"-e", "OPENAI_API_KEY="+r.cfg.OpenAIProxyClientToken,
			"-e", "OPENAI_BASE_URL="+r.cfg.ContainerProxyBaseURL(),
		)
	}
	args = append(args,
		"--mount", mountSpec(workspace, "/home/agent/workspace"),
		"--mount", mountSpec(codexDir, "/home/agent/.codex"),
		"--mount", mountSpec(artifacts, "/run-artifacts"),
		r.cfg.ContainerImage,
	)
	cmd := exec.CommandContext(runCtx, "docker", args...)
	responsePath := filepath.Join(artifactDir, "response.jsonl")
	result.ResponsePath = responsePath
	stderrPath := filepath.Join(artifactDir, "stderr.log")
	result.StderrPath = stderrPath
	result.LastMessagePath = filepath.Join(artifactDir, "last_message.txt")
	stdoutFile, err := os.Create(responsePath)
	if err != nil {
		result.ErrorText = err.Error()
		result.FailureClass = "artifact_setup"
		return result
	}
	defer stdoutFile.Close()
	var stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: stdoutFile, limit: r.cfg.MaxOutputBytes}
	cmd.Stderr = &limitedWriter{w: &stderr, limit: r.cfg.MaxOutputBytes}
	if err := cmd.Start(); err != nil {
		result.ErrorText = r.redactor.Redact(err.Error())
		result.ExitCode = exitCode(err)
		result.FailureClass = "container_start"
		result.Retryable = true
		return result
	}
	agentMessagesDone := make(chan struct{})
	go func() {
		defer close(agentMessagesDone)
		r.monitorAgentMessages(runCtx, ctx, filepath.Join(artifactDir, "agent_messages.jsonl"), onAgentMessage)
	}()
	agentActionsDone := make(chan struct{})
	go func() {
		defer close(agentActionsDone)
		r.monitorAgentActions(runCtx, ctx, filepath.Join(artifactDir, "agent_actions.jsonl"), onAgentAction)
	}()
	err = cmd.Wait()
	cancel()
	<-agentMessagesDone
	<-agentActionsDone
	sanitizedStderr := r.redactor.Redact(stderr.String())
	if err == nil {
		sanitizedStderr = filterSuccessfulRunStderr(sanitizedStderr)
	}
	_ = os.WriteFile(stderrPath, []byte(sanitizedStderr), 0o600)
	if cid, cidErr := os.ReadFile(filepath.Join(artifactDir, "container.cid")); cidErr == nil {
		result.ContainerID = strings.TrimSpace(string(cid))
	}
	last, _ := os.ReadFile(result.LastMessagePath)
	result.LastMessage = strings.TrimSpace(r.redactor.Redact(string(last)))
	result.SessionID = extractSessionID(responsePath)
	if err != nil {
		result.ErrorText = r.redactor.Redact(err.Error())
		result.ExitCode = exitCode(err)
		if errors.Is(runCtx.Err(), context.Canceled) {
			result.ErrorText = "run cancelled"
			result.FailureClass = "cancelled"
			result.Retryable = false
			result.Canceled = true
		} else if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result.FailureClass = "timeout"
			result.Retryable = false
		} else if strings.Contains(sanitizedStderr, "Error loading config.toml") {
			result.FailureClass = "codex_config"
			result.Retryable = false
		} else if fileSize(responsePath) == 0 {
			result.FailureClass = "container_start"
			result.Retryable = true
		} else if looksTransient(sanitizedStderr) {
			result.FailureClass = "transient_api"
			result.Retryable = true
		} else {
			result.FailureClass = "agent_failed"
			result.Retryable = false
		}
		return result
	}
	result.ExitCode = 0
	result.StartedCodex = true
	return result
}

func mountSpec(source, target string) string {
	return "type=bind,source=" + source + ",target=" + target
}

type limitedWriter struct {
	w       io.Writer
	limit   int64
	written int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.limit > 0 && l.written+int64(len(p)) > l.limit {
		allowed := l.limit - l.written
		if allowed > 0 {
			_, _ = l.w.Write(p[:allowed])
			l.written += allowed
		}
		return 0, fmt.Errorf("output limit exceeded")
	}
	n, err := l.w.Write(p)
	l.written += int64(n)
	return n, err
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func looksTransient(s string) bool {
	lower := strings.ToLower(s)
	for _, part := range []string{"timeout", "temporarily unavailable", "connection reset", "connection refused", "rate limit", "429", "502", "503", "504"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

func filterSuccessfulRunStderr(s string) string {
	if strings.TrimSpace(s) == "" {
		return s
	}
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "failed to refresh available models") &&
			strings.Contains(line, "https://chatgpt.com/backend-api/codex/models") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func extractSessionID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var obj map[string]any
		if json.Unmarshal(scanner.Bytes(), &obj) != nil {
			continue
		}
		for _, key := range []string{"session_id", "sessionId", "conversation_id", "conversationId"} {
			if v, ok := obj[key].(string); ok && v != "" {
				return v
			}
		}
		if item, ok := obj["session"].(map[string]any); ok {
			if v, ok := item["id"].(string); ok && v != "" {
				return v
			}
		}
	}
	return ""
}
