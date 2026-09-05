package tasks

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	cronhuman "github.com/lnquy/cron"
	"github.com/robfig/cron/v3"
)

// ParseNotBefore interprets a user-supplied "notBefore" value relative to
// now and returns the absolute unix-seconds timestamp it names. Three
// shapes are accepted, tried in this order:
//
//   - a Go duration, e.g. "10m", "90s", "2h" -- relative to now
//   - an RFC3339 timestamp, e.g. "2026-09-05T08:00:00Z" -- absolute
//   - a bare unix-seconds integer, e.g. "1780000000" -- absolute
//
// An empty string returns (0, nil): "no delay requested".
func ParseNotBefore(raw string, now time.Time) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}

	if d, err := time.ParseDuration(raw); err == nil {
		return now.Add(d).Unix(), nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Unix(), nil
	}
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return sec, nil
	}

	return 0, fmt.Errorf("could not parse 'notBefore' value %q as a duration (e.g. \"10m\"), an RFC3339 timestamp, or a unix-seconds integer", raw)
}

// cronParseOption restricts cron expressions to the standard 5-field format
// (minute hour dom month dow), matching cron.ParseStandard. Spelled out
// explicitly (rather than calling cron.ParseStandard per-call) so the same
// *cron.Parser is reused across calls.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// NextCronFire parses expr as a standard 5-field cron expression and
// returns the next time it fires strictly after `after`. Only the parser
// from robfig/cron is used here -- blanket runs its own scheduler loop
// (server/scheduler.go) rather than robfig/cron's own goroutine scheduler.
func NextCronFire(expr string, after time.Time) (time.Time, error) {
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return sched.Next(after), nil
}

// NextCronFires returns the next n times expr fires strictly after `after`,
// in order. Used by GET /schedule/describe for the create form's live
// preview.
func NextCronFires(expr string, after time.Time, n int) ([]time.Time, error) {
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	out := make([]time.Time, 0, n)
	t := after
	for i := 0; i < n; i++ {
		t = sched.Next(t)
		out = append(out, t)
	}
	return out, nil
}

// cronDescriptor renders a standard 5-field cron expression as an
// English sentence, e.g. "*/5 * * * *" -> "Every 5 minutes". Built once at
// package init with no options (English-only, 12-hour clock, Sunday=0 --
// cron-expression-descriptor's own defaults); NewDescriptor only errors on
// bad *options*, never on input expressions, so a package-level error here
// would mean a real bug in this call, not user input.
//
// github.com/lnquy/cron is a maintained Go port of the widely used
// cron-expression-descriptor / cRonstrue libraries (used across .NET, JS,
// Python, Go, ...); chosen over hand-rolling this since natural-language
// cron descriptions have a lot of small-expression edge cases (step
// values, ranges, day-of-week names, "L"/"W"/"#") that a mature port
// already covers.
var cronDescriptor, cronDescriptorErr = cronhuman.NewDescriptor()

// DescribeCron returns a short, human-friendly English description of a
// standard 5-field cron expression (e.g. "Every 5 minutes"), or an error
// with the parser's message if expr is invalid. Validated with the same
// *cron.Parser as NextCronFire first, so callers get one consistent error
// message regardless of which check tripped.
func DescribeCron(expr string) (string, error) {
	if _, err := cronParser.Parse(expr); err != nil {
		return "", fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	if cronDescriptorErr != nil {
		return "", cronDescriptorErr
	}
	desc, err := cronDescriptor.ToDescription(expr, cronhuman.Locale_en)
	if err != nil {
		return "", fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return desc, nil
}

// ScheduleDescriptionFor returns the value of Task.MarshalJSON's computed
// "scheduleDescription" field for t: a short human-readable summary of its
// schedule, or "" for a task with no schedule of its own (a plain task, or
// a child task spawned from a RECURRING template -- its ParentId points at
// the template that has the description, not the child).
//
//   - SCHEDULED: "Once, at <ScheduledTs as RFC3339, local time>".
//   - RECURRING / PAUSED / a STOPPED template (CronExpr still set, i.e. it
//     was cancelled rather than deleted): the cron description, annotated
//     with "(paused)" / "(stopped)" for those two states so the same text
//     works standalone (MCP tool output, `blanket ps`) without the state
//     column alongside it.
//   - Anything else (including a STOPPED *non*-template task, which has no
//     CronExpr): "".
//
// Falls back to the raw expression (rather than erroring) if CronExpr
// somehow fails to parse -- that shouldn't happen for a template that was
// only ever set via applySchedule/changeTaskScheduleById, both of which
// validate first, but a stored record should never make JSON encoding
// itself fail.
func ScheduleDescriptionFor(t Task) string {
	switch t.State {
	case "SCHEDULED":
		return fmt.Sprintf("Once, at %s", time.Unix(t.ScheduledTs, 0).Local().Format(time.RFC3339))
	case "RECURRING", "PAUSED":
		if t.CronExpr == "" {
			return ""
		}
		desc, err := DescribeCron(t.CronExpr)
		if err != nil {
			desc = t.CronExpr
		}
		if t.State == "PAUSED" {
			return desc + " (paused)"
		}
		return desc
	case "STOPPED":
		if t.CronExpr == "" {
			return ""
		}
		desc, err := DescribeCron(t.CronExpr)
		if err != nil {
			desc = t.CronExpr
		}
		return desc + " (stopped)"
	default:
		return ""
	}
}
