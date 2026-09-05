package tasks

import (
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
