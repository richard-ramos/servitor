package main

import (
	"strings"
	"testing"
)

func TestFilterSuccessfulRunStderrDropsChatGPTModelProbeNoise(t *testing.T) {
	in := strings.Join([]string{
		`2026 ERROR failed to refresh available models: unexpected status 401 Unauthorized, url: https://chatgpt.com/backend-api/codex/models?client_version=0.123.0`,
		`keep this warning`,
	}, "\n")
	got := filterSuccessfulRunStderr(in)
	if strings.Contains(got, "failed to refresh available models") {
		t.Fatalf("expected model probe noise to be filtered, got %q", got)
	}
	if !strings.Contains(got, "keep this warning") {
		t.Fatalf("expected unrelated warning to be kept, got %q", got)
	}
}
