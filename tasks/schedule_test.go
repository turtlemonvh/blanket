package tasks

import (
	"strings"
	"testing"
	"time"
)

func TestParseNotBefore_Empty(t *testing.T) {
	ts, err := ParseNotBefore("", time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts != 0 {
		t.Fatalf("expected 0 for empty input, got %d", ts)
	}
}

func TestParseNotBefore_Duration(t *testing.T) {
	now := time.Unix(1000, 0)
	ts, err := ParseNotBefore("10m", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := now.Add(10 * time.Minute).Unix()
	if ts != want {
		t.Fatalf("expected %d, got %d", want, ts)
	}
}

func TestParseNotBefore_RFC3339(t *testing.T) {
	now := time.Unix(1000, 0)
	ts, err := ParseNotBefore("2026-09-05T08:00:00Z", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want, _ := time.Parse(time.RFC3339, "2026-09-05T08:00:00Z")
	if ts != want.Unix() {
		t.Fatalf("expected %d, got %d", want.Unix(), ts)
	}
}

func TestParseNotBefore_UnixSeconds(t *testing.T) {
	ts, err := ParseNotBefore("1780000000", time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts != 1780000000 {
		t.Fatalf("expected 1780000000, got %d", ts)
	}
}

func TestParseNotBefore_Invalid(t *testing.T) {
	_, err := ParseNotBefore("not-a-time", time.Unix(1000, 0))
	if err == nil {
		t.Fatal("expected error for unparseable notBefore value")
	}
}

func TestNextCronFire_Valid(t *testing.T) {
	// "every 5 minutes"
	after := time.Date(2026, 9, 5, 8, 2, 0, 0, time.UTC)
	next, err := NextCronFire("*/5 * * * *", after)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 9, 5, 8, 5, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("expected next fire %v, got %v", want, next)
	}
}

func TestNextCronFire_StrictlyAfter(t *testing.T) {
	// exactly on a fire boundary; next fire must be the *following*
	// occurrence, not the same instant.
	after := time.Date(2026, 9, 5, 8, 5, 0, 0, time.UTC)
	next, err := NextCronFire("*/5 * * * *", after)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 9, 5, 8, 10, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("expected next fire %v, got %v", want, next)
	}
}

func TestNextCronFire_Invalid(t *testing.T) {
	_, err := NextCronFire("not a cron expr", time.Now())
	if err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestNextCronFire_SixFieldsRejected(t *testing.T) {
	// The parser is configured for the standard 5-field format; a 6-field
	// (seconds-included) expression should be rejected rather than
	// silently misinterpreted.
	_, err := NextCronFire("*/5 * * * * *", time.Now())
	if err == nil {
		t.Fatal("expected error for 6-field cron expression")
	}
}

func TestNextCronFires_ReturnsRequestedCount(t *testing.T) {
	after := time.Date(2026, 9, 5, 8, 2, 0, 0, time.UTC)
	got, err := NextCronFires("*/5 * * * *", after, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 fire times, got %d", len(got))
	}
	want := []time.Time{
		time.Date(2026, 9, 5, 8, 5, 0, 0, time.UTC),
		time.Date(2026, 9, 5, 8, 10, 0, 0, time.UTC),
		time.Date(2026, 9, 5, 8, 15, 0, 0, time.UTC),
	}
	for i, w := range want {
		if !got[i].Equal(w) {
			t.Fatalf("fire[%d]: expected %v, got %v", i, w, got[i])
		}
	}
}

func TestNextCronFires_Invalid(t *testing.T) {
	_, err := NextCronFires("not a cron expr", time.Now(), 3)
	if err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestDescribeCron_Valid(t *testing.T) {
	desc, err := DescribeCron("*/5 * * * *")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc == "" {
		t.Fatal("expected a non-empty description")
	}
	// Loose match: we don't want this test to be pinned to the exact
	// wording of a third-party library, just that it produced *something*
	// recognizably about "every 5 minutes".
	if !strings.Contains(strings.ToLower(desc), "5 minutes") {
		t.Fatalf("expected description to mention '5 minutes', got %q", desc)
	}
}

func TestDescribeCron_Invalid(t *testing.T) {
	_, err := DescribeCron("not a cron expr")
	if err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestScheduleDescriptionFor_Scheduled(t *testing.T) {
	ts := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC).Unix()
	desc := ScheduleDescriptionFor(Task{State: "SCHEDULED", ScheduledTs: ts})
	if !strings.HasPrefix(desc, "Once, at ") {
		t.Fatalf("expected description to start with 'Once, at ', got %q", desc)
	}
	if !strings.Contains(desc, time.Unix(ts, 0).Local().Format(time.RFC3339)) {
		t.Fatalf("expected description to contain the RFC3339 local time, got %q", desc)
	}
}

func TestScheduleDescriptionFor_Recurring(t *testing.T) {
	desc := ScheduleDescriptionFor(Task{State: "RECURRING", CronExpr: "*/5 * * * *"})
	if desc == "" {
		t.Fatal("expected a non-empty description for a RECURRING template")
	}
	if strings.Contains(desc, "(paused)") || strings.Contains(desc, "(stopped)") {
		t.Fatalf("RECURRING description should not be annotated, got %q", desc)
	}
}

func TestScheduleDescriptionFor_Paused(t *testing.T) {
	desc := ScheduleDescriptionFor(Task{State: "PAUSED", CronExpr: "*/5 * * * *"})
	if !strings.HasSuffix(desc, "(paused)") {
		t.Fatalf("expected PAUSED description to end with '(paused)', got %q", desc)
	}
}

func TestScheduleDescriptionFor_StoppedTemplate(t *testing.T) {
	desc := ScheduleDescriptionFor(Task{State: "STOPPED", CronExpr: "*/5 * * * *"})
	if !strings.HasSuffix(desc, "(stopped)") {
		t.Fatalf("expected STOPPED template description to end with '(stopped)', got %q", desc)
	}
}

func TestScheduleDescriptionFor_StoppedNonTemplate(t *testing.T) {
	// A plain cancelled task (never scheduled/recurring) has no CronExpr
	// and shouldn't get a schedule description at all.
	desc := ScheduleDescriptionFor(Task{State: "STOPPED"})
	if desc != "" {
		t.Fatalf("expected empty description for a non-template STOPPED task, got %q", desc)
	}
}

func TestScheduleDescriptionFor_PlainStates(t *testing.T) {
	for _, state := range []string{"WAITING", "CLAIMED", "RUNNING", "SUCCESS", "ERROR", "TIMEDOUT"} {
		if desc := ScheduleDescriptionFor(Task{State: state}); desc != "" {
			t.Fatalf("expected empty description for state %q, got %q", state, desc)
		}
	}
}
