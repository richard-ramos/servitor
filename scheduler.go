package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (a *App) RunSchedulerLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.runDueSchedules(ctx); err != nil {
				fmt.Printf("scheduler error: %s\n", a.redactor.Redact(err.Error()))
			}
		}
	}
}

func (a *App) runDueSchedules(ctx context.Context) error {
	now := time.Now().In(a.cfg.ServiceTimezone)
	due, err := DueSchedules(ctx, a.db, now)
	if err != nil {
		return err
	}
	for _, s := range due {
		if _, err := Enqueue(ctx, a.db, s.ContextID, 0, 0, s.Prompt, false); err != nil {
			return err
		}
		next, err := NextCronTime(s.CronExpr, now.Add(time.Minute), a.cfg.ServiceTimezone)
		if err != nil {
			return err
		}
		if err := UpdateScheduleAfterRun(ctx, a.db, s.ID, now, next); err != nil {
			return err
		}
	}
	return nil
}

type cronSpec struct {
	minute, hour, dom, month, dow map[int]bool
}

func NextCronTime(expr string, after time.Time, loc *time.Location) (time.Time, error) {
	spec, err := parseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	t := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	deadline := t.AddDate(5, 0, 0)
	for t.Before(deadline) {
		if spec.matches(t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("no matching time found")
}

func parseCron(expr string) (cronSpec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cronSpec{}, fmt.Errorf("expected 5 fields")
	}
	var err error
	var s cronSpec
	if s.minute, err = parseCronField(fields[0], 0, 59); err != nil {
		return s, fmt.Errorf("minute: %w", err)
	}
	if s.hour, err = parseCronField(fields[1], 0, 23); err != nil {
		return s, fmt.Errorf("hour: %w", err)
	}
	if s.dom, err = parseCronField(fields[2], 1, 31); err != nil {
		return s, fmt.Errorf("day-of-month: %w", err)
	}
	if s.month, err = parseCronField(fields[3], 1, 12); err != nil {
		return s, fmt.Errorf("month: %w", err)
	}
	if s.dow, err = parseCronField(fields[4], 0, 7); err != nil {
		return s, fmt.Errorf("day-of-week: %w", err)
	}
	if s.dow[7] {
		s.dow[0] = true
		delete(s.dow, 7)
	}
	return s, nil
}

func parseCronField(field string, min, max int) (map[int]bool, error) {
	out := make(map[int]bool)
	for _, part := range strings.Split(field, ",") {
		if part == "*" {
			for i := min; i <= max; i++ {
				out[i] = true
			}
			continue
		}
		step := 1
		if base, stepStr, ok := strings.Cut(part, "/"); ok {
			part = base
			n, err := strconv.Atoi(stepStr)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("invalid step %q", stepStr)
			}
			step = n
		}
		start, end := 0, 0
		if a, b, ok := strings.Cut(part, "-"); ok {
			var err error
			start, err = strconv.Atoi(a)
			if err != nil {
				return nil, err
			}
			end, err = strconv.Atoi(b)
			if err != nil {
				return nil, err
			}
		} else if part == "*" {
			start, end = min, max
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, err
			}
			start, end = n, n
		}
		if start < min || end > max || start > end {
			return nil, fmt.Errorf("range %d-%d outside %d-%d", start, end, min, max)
		}
		for i := start; i <= end; i += step {
			out[i] = true
		}
	}
	return out, nil
}

func (s cronSpec) matches(t time.Time) bool {
	return s.minute[t.Minute()] &&
		s.hour[t.Hour()] &&
		s.dom[t.Day()] &&
		s.month[int(t.Month())] &&
		s.dow[int(t.Weekday())]
}
