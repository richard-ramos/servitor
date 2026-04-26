package main

import "testing"

func TestCallbackSigningRoundTrip(t *testing.T) {
	app := &App{cfg: Config{DataDir: t.TempDir()}}
	token, err := app.signCallbackData("act_123", "approve")
	if err != nil {
		t.Fatal(err)
	}
	actionID, value, ok := app.verifyCallbackData(token)
	if !ok || actionID != "act_123" || value != "approve" {
		t.Fatalf("bad verification action=%s value=%s ok=%t", actionID, value, ok)
	}
	if _, _, ok := app.verifyCallbackData(token + "x"); ok {
		t.Fatal("expected tampered token rejection")
	}
}
