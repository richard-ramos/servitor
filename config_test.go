package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestChatGPTProxyClientTokenWrapsNonJWTSeed(t *testing.T) {
	got := chatGPTProxyClientToken("servitor-test-seed")
	if strings.Count(got, ".") != 2 {
		t.Fatalf("expected JWT-shaped token, got %q", got)
	}
	if !tokenHasUsableJWTExpiry(got) {
		t.Fatal("expected generated token to have a usable expiry")
	}
	if got != chatGPTProxyClientToken("servitor-test-seed") {
		t.Fatal("expected deterministic token for stable proxy/client auth")
	}
}

func TestChatGPTProxyClientTokenKeepsUsableJWT(t *testing.T) {
	jwt := makeTestJWT(time.Now().Add(time.Hour).Unix())
	if got := chatGPTProxyClientToken(jwt); got != jwt {
		t.Fatal("expected usable JWT to be preserved")
	}
}

func TestTokenHasUsableJWTExpiryRejectsExpiredToken(t *testing.T) {
	if tokenHasUsableJWTExpiry(makeTestJWT(time.Now().Add(-time.Hour).Unix())) {
		t.Fatal("expected expired token to be rejected")
	}
}

func makeTestJWT(exp int64) string {
	payload, _ := json.Marshal(map[string]int64{"exp": exp})
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}
