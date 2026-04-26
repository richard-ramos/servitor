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

func TestInitialNextRunIntervalAndOnce(t *testing.T) {
	loc := time.UTC
	interval, err := InitialNextRun(ScheduleKindInterval, "2h", loc)
	if err != nil {
		t.Fatal(err)
	}
	if interval.IntervalSeconds != 7200 || interval.Kind != ScheduleKindInterval {
		t.Fatalf("unexpected interval schedule: %+v", interval)
	}

	runAt := time.Now().In(loc).Add(time.Hour).Truncate(time.Second)
	once, err := InitialNextRun(ScheduleKindOnce, runAt.Format(time.RFC3339), loc)
	if err != nil {
		t.Fatal(err)
	}
	if once.RunAt == nil || !once.NextRunAt.Equal(runAt) {
		t.Fatalf("unexpected once schedule: %+v", once)
	}
}

func TestCronDOMDOWUsesOrSemantics(t *testing.T) {
	loc := time.UTC
	after := time.Date(2026, 4, 25, 0, 0, 0, 0, loc)
	next, err := NextCronTime("0 9 1 * 1", after, loc)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 4, 27, 9, 0, 0, 0, loc) // Monday, not day-of-month 1.
	if !next.Equal(want) {
		t.Fatalf("got %s want %s", next, want)
	}
}
