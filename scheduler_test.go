package main

import (
	"testing"
	"time"
)

func TestNextCronTime(t *testing.T) {
	loc := time.UTC
	next, err := NextCronTime("*/15 * * * *", time.Date(2026, 4, 22, 10, 7, 0, 0, loc), loc)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 4, 22, 10, 15, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("got %s want %s", next, want)
	}
}
