package upgrade

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJournalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", JournalName)

	// A missing journal is (nil, nil): "no upgrade has ever run here" is a
	// normal answer, not an error.
	j, err := LoadJournal(p)
	if err != nil || j != nil {
		t.Fatalf("LoadJournal on a missing file = (%v, %v), want (nil, nil)", j, err)
	}

	j = NewJournal("abc123", "upgrade", time.Unix(1000, 0))
	j.FromVersion = "v0.4.0"
	j.ToVersion = "v0.5.0"
	j.BinaryPath = "/home/x/.local/bin/blanket"
	j.StagedPath = "/home/x/.local/bin/.blanket-upgrade-9"
	j.SHA256 = "deadbeef"
	if err := j.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	j.Advance(JournalStaged, "verified")
	j.Advance(JournalSwapped, "installed")
	if err := j.Save(p); err != nil {
		t.Fatal(err)
	}

	back, err := LoadJournal(p)
	if err != nil {
		t.Fatal(err)
	}
	if back.State != JournalSwapped {
		t.Errorf("state = %q, want %q", back.State, JournalSwapped)
	}
	if back.ToVersion != "v0.5.0" || back.BinaryPath != j.BinaryPath {
		t.Errorf("fields did not survive the round trip: %+v", back)
	}
	// Steps accumulate rather than replace: a human reading the file after
	// a failure needs the sequence, not just the end of it.
	if len(back.Steps) != 3 {
		t.Errorf("want 3 steps (PLANNED, STAGED, SWAPPED), got %d: %+v", len(back.Steps), back.Steps)
	}
}

// TestJournalSaveIsAtomic checks the temp-file-plus-rename discipline
// leaves nothing behind: a half-written journal that still parses is worse
// than no journal, because it will be believed.
func TestJournalSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, JournalName)
	j := NewJournal("id", "upgrade", time.Now())
	for i := 0; i < 5; i++ {
		if err := j.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != JournalName {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only %s in %s, got %v", JournalName, dir, names)
	}
}

func TestLoadJournalRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, JournalName)
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadJournal(p); err == nil {
		t.Fatal("expected an error for an unparseable journal")
	}
}

func TestJournalRankAndTerminal(t *testing.T) {
	if JournalRank(JournalStaged) >= JournalRank(JournalSwapped) {
		t.Error("STAGED must rank before SWAPPED")
	}
	if JournalRank(JournalFailed) != -1 {
		t.Error("terminal states have no rank in the forward order")
	}
	for _, s := range []string{JournalVerified, JournalAborted, JournalFailed} {
		if !(&Journal{State: s}).Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	if (&Journal{State: JournalStaged}).Terminal() {
		t.Error("STAGED is not terminal; --resume has work to do from it")
	}
}
