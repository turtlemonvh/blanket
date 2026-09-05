package tasks

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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
