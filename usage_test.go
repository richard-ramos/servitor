package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseUsageFromJSONLBestEffort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "response.jsonl")
	data := []byte(`{"type":"event","usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7},"model":"gpt-test"}` + "\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got := ParseUsageFromJSONL(path)
	if got.Model != "gpt-test" || got.InputTokens != 3 || got.OutputTokens != 4 || got.TotalTokens != 7 {
		t.Fatalf("unexpected usage: %+v", got)
	}
}
