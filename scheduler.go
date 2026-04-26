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
		prompt := schedulePrompt(s)
		if _, err := EnqueueForSchedule(ctx, a.db, s.ContextID, s.ID, 0, 0, prompt, false); err != nil {
			return err
		}
		next, status, err := a.nextScheduleAfterRun(s, now)
		if err != nil {
			return err
		}
		if err := UpdateScheduleAfterRun(ctx, a.db, s.ID, now, next, status); err != nil {
			return err
		}
	}
	return nil
}

func schedulePrompt(s Schedule) string {
	if s.ScriptPath == "" {
		return s.Prompt
	}
	prompt := strings.TrimSpace(s.Prompt)
	if prompt == "" {
		prompt = "Run or follow the scheduled workspace script."
	}
	return fmt.Sprintf("Scheduled task script: /home/agent/workspace/%s\n\n%s", filepathToSlash(s.ScriptPath), prompt)
}

func filepathToSlash(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

func (a *App) nextScheduleAfterRun(s Schedule, now time.Time) (*time.Time, string, error) {
	switch s.Kind {
	case ScheduleKindCron, "":
		next, err := NextCronTime(s.CronExpr, now, a.cfg.ServiceTimezone)
		if err != nil {
			return nil, "", err
		}
		return &next, ScheduleStatusActive, nil
	case ScheduleKindInterval:
		if s.IntervalSeconds <= 0 {
			return nil, "", fmt.Errorf("invalid interval schedule")
		}
		next := now.Add(time.Duration(s.IntervalSeconds) * time.Second)
		return &next, ScheduleStatusActive, nil
	case ScheduleKindOnce:
		return nil, ScheduleStatusCompleted, nil
	default:
		return nil, "", fmt.Errorf("unsupported schedule kind %q", s.Kind)
	}
}

func InitialNextRun(kind, spec string, loc *time.Location) (Schedule, error) {
	now := time.Now().In(loc)
	var s Schedule
	s.Kind = kind
	s.Status = ScheduleStatusActive
	s.Enabled = true
	switch kind {
	case ScheduleKindCron:
		next, err := NextCronTime(spec, now, loc)
		if err != nil {
			return s, err
		}
		s.CronExpr = spec
		s.NextRunAt = next
	case ScheduleKindInterval:
		d, err := ParseTaskDuration(spec)
		if err != nil {
			return s, err
		}
		s.IntervalSeconds = int64(d.Seconds())
		s.NextRunAt = now.Add(d)
	case ScheduleKindOnce:
		runAt, err := ParseTaskTime(spec, loc)
		if err != nil {
			return s, err
		}
		if !runAt.After(now) {
			return s, fmt.Errorf("one-shot time must be in the future")
		}
		s.RunAt = &runAt
		s.NextRunAt = runAt
	default:
		return s, fmt.Errorf("unsupported schedule kind %q", kind)
	}
	return s, nil
}

func recomputeNextRun(s Schedule, loc *time.Location) (time.Time, error) {
	switch s.Kind {
	case ScheduleKindCron, "":
		return NextCronTime(s.CronExpr, time.Now().In(loc), loc)
	case ScheduleKindInterval:
		if s.IntervalSeconds <= 0 {
			return time.Time{}, fmt.Errorf("invalid interval schedule")
		}
		return time.Now().In(loc).Add(time.Duration(s.IntervalSeconds) * time.Second), nil
	case ScheduleKindOnce:
		if s.RunAt == nil {
			return time.Time{}, fmt.Errorf("one-shot schedule has no run time")
		}
		if !s.RunAt.After(time.Now().In(loc)) {
			return time.Time{}, fmt.Errorf("one-shot schedule time is in the past")
		}
		return *s.RunAt, nil
	default:
		return time.Time{}, fmt.Errorf("unsupported schedule kind %q", s.Kind)
	}
}

func ParseTaskDuration(spec string) (time.Duration, error) {
	spec = strings.TrimSpace(spec)
	if strings.HasSuffix(spec, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(spec, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid duration %q", spec)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(spec)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return d, nil
}

func ParseTaskTime(spec string, loc *time.Location) (time.Time, error) {
	spec = strings.TrimSpace(spec)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, spec, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("expected RFC3339 or local time like 2026-04-25T15:04")
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
	domStar := len(s.dom) == 31
	dowStar := len(s.dow) == 7
	dayMatches := false
	switch {
	case domStar && dowStar:
		dayMatches = true
	case domStar:
		dayMatches = s.dow[int(t.Weekday())]
	case dowStar:
		dayMatches = s.dom[t.Day()]
	default:
		dayMatches = s.dom[t.Day()] || s.dow[int(t.Weekday())]
	}
	return s.minute[t.Minute()] &&
		s.hour[t.Hour()] &&
		dayMatches &&
		s.month[int(t.Month())]
}
