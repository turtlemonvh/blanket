package upgrade

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSlotRotation(t *testing.T) {
	dir := t.TempDir()
	slotsDir := filepath.Join(dir, "slots")
	bin := filepath.Join(dir, "blanket")

	base := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	versions := []string{"v0.1.0", "v0.2.0", "v0.3.0", "v0.4.0", "v0.5.0"}
	for i, v := range versions {
		if err := os.WriteFile(bin, []byte("binary "+v), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := saveSlotAt(slotsDir, bin, v, "/backups/b-"+v+".db", "u"+v, base.Add(time.Duration(i)*time.Millisecond)); err != nil {
			t.Fatalf("saveSlot %s: %v", v, err)
		}
	}

	slots, err := ListSlots(slotsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 5 {
		t.Fatalf("want 5 slots before pruning, got %d", len(slots))
	}
	// Newest first, so slots[0] is what `blanket rollback --yes` uses.
	if slots[0].Version != "v0.5.0" {
		t.Errorf("newest slot is %s, want v0.5.0", slots[0].Version)
	}

	removed, err := PruneSlots(slotsDir, DefaultSlots)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Errorf("want 2 pruned, got %d (%v)", len(removed), removed)
	}
	slots, _ = ListSlots(slotsDir)
	if len(slots) != 3 {
		t.Fatalf("want 3 slots kept, got %d", len(slots))
	}
	for i, want := range []string{"v0.5.0", "v0.4.0", "v0.3.0"} {
		if slots[i].Version != want {
			t.Errorf("slot %d is %s, want %s", i, slots[i].Version, want)
		}
	}

	// The saved binary must verify against the digest recorded with it --
	// that check is what stops a rollback turning a bad upgrade into an
	// unbootable install.
	s := slots[0]
	if err := VerifyFileDigest(s.BinaryPath(), s.BinaryName, s.SHA256); err != nil {
		t.Errorf("slot binary does not verify: %v", err)
	}
	if got, _ := os.ReadFile(s.BinaryPath()); string(got) != "binary v0.5.0" {
		t.Errorf("slot holds %q", got)
	}
	if s.BackupPath != "/backups/b-v0.5.0.db" {
		t.Errorf("backup path not recorded: %q", s.BackupPath)
	}
}

func TestListSlotsIgnoresStrangers(t *testing.T) {
	dir := t.TempDir()
	slotsDir := filepath.Join(dir, "slots")
	if err := os.MkdirAll(filepath.Join(slotsDir, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slotsDir, "README"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	slots, err := ListSlots(slotsDir)
	if err != nil {
		t.Fatalf("a directory of strangers should not be an error: %v", err)
	}
	if len(slots) != 0 {
		t.Errorf("want 0 slots, got %d", len(slots))
	}
}

func TestListSlotsOnMissingDir(t *testing.T) {
	slots, err := ListSlots(filepath.Join(t.TempDir(), "nope"))
	if err != nil || slots != nil {
		t.Fatalf("a missing slots dir is (nil, nil), got (%v, %v)", slots, err)
	}
}

func TestPruneKeepsAtLeastOne(t *testing.T) {
	dir := t.TempDir()
	slotsDir := filepath.Join(dir, "slots")
	bin := filepath.Join(dir, "blanket")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveSlot(slotsDir, bin, "v1.0.0", "", "id"); err != nil {
		t.Fatal(err)
	}
	if _, err := PruneSlots(slotsDir, 0); err != nil {
		t.Fatal(err)
	}
	slots, _ := ListSlots(slotsDir)
	if len(slots) != 1 {
		t.Errorf("keep<1 must still keep one slot, got %d", len(slots))
	}
}

// TestSlotOrderingWithinOneSecond pins down the reason slot names keep
// milliseconds. With second precision, two slots written in the same
// second sort by the *version* in the name, so an upgrade immediately
// followed by a rollback would make the older slot look like the newer one
// -- and `blanket rollback` would restore the binary it had just replaced.
func TestSlotOrderingWithinOneSecond(t *testing.T) {
	dir := t.TempDir()
	slotsDir := filepath.Join(dir, "slots")
	bin := filepath.Join(dir, "blanket")
	base := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	// v9.9.1 saved first, v9.9.0 second, both inside the same second. A
	// lexical sort on a second-precision name would put v9.9.1 last, and
	// therefore "newest".
	for i, v := range []string{"v9.9.1", "v9.9.0"} {
		if err := os.WriteFile(bin, []byte("binary "+v), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := saveSlotAt(slotsDir, bin, v, "", "id", base.Add(time.Duration(i*10)*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}

	slots, err := ListSlots(slotsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 2 {
		t.Fatalf("want 2 slots, got %d", len(slots))
	}
	if slots[0].Version != "v9.9.0" {
		t.Errorf("newest slot is %s; the one saved LAST is the newest, whatever its version string", slots[0].Version)
	}
}
