package upgrade

/*

Rollback slots (turtlemonvh/blanket#23 phase 6, brief decision row 9).

A slot is "the pair of things you need to put an install back the way it
was": the binary that was replaced, and the database backup taken
immediately before the migration that upgrade ran. Three are kept, matching
`storage.backupRetention` — the database half of a slot is exactly one of
those backup files, so keeping four slots and three backups would mean one
slot whose database half had been pruned out from under it.

The binary is **copied** into the slot rather than renamed there. A rename
is atomic and cheap, and it is also wrong: `~/.local/bin` and the blanket
data directory are routinely on different filesystems, where rename fails
outright, and a failure at that point would leave the install with no
binary at all.

There is no size cap (row 9). A cap that silently stopped keeping slots
would defeat the purpose in the exact case it was added for, so the policy
is a count plus a free-space *warning* — see diskfree.Available and
WarnIfTight below.

*/

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/turtlemonvh/blanket/lib/diskfree"
)

// SlotsName is the directory, inside the upgrade state directory, holding
// the slots.
const SlotsName = "slots"

// DefaultSlots is how many rollback slots are kept. Three, matching the
// backup retention.
const DefaultSlots = 3

// SlotMetaName is the metadata file inside each slot.
const SlotMetaName = "slot.json"

// Slot is one rollback point.
type Slot struct {
	// Dir is the slot directory; BinaryName is the file inside it.
	Dir        string `json:"-"`
	BinaryName string `json:"binaryName"`

	// Version is the tag the saved binary reports, "" for a dev build.
	Version string `json:"version,omitempty"`
	// SHA256 is the digest of the saved binary, taken at save time. It is
	// what `blanket rollback` verifies before putting the file back: a
	// rollback that installs a corrupted binary turns a bad upgrade into
	// an unbootable install.
	SHA256 string `json:"sha256"`
	// InstalledPath is where the binary came from, and where a rollback
	// puts it back.
	InstalledPath string `json:"installedPath"`
	// BackupPath is the pre-upgrade database backup, if one was taken.
	BackupPath string `json:"backupPath,omitempty"`
	// UpgradeId ties the slot to a journal.
	UpgradeId string `json:"upgradeId,omitempty"`
	CreatedTs int64  `json:"createdTs"`
}

// BinaryPath is the saved binary's full path.
func (s Slot) BinaryPath() string { return filepath.Join(s.Dir, s.BinaryName) }

var slotDirRe = regexp.MustCompile(`^\d{14}\.\d{3}-`)

// slotDirName is `<UTC timestamp>-<version>`, and the timestamp keeps
// milliseconds.
//
// Timestamp first so a plain lexical sort is a chronological one, which is
// what makes ListSlots and PruneSlots agree about which slot is oldest
// without either of them having to open a file. Milliseconds because
// second precision does not: two slots written in the same second would
// then sort by the *version* in the name, so an upgrade and its rollback
// in quick succession could make the older slot look like the newer one
// and `blanket rollback` would restore the binary it just replaced. The
// backup filenames keep milliseconds for the same reason.
func slotDirName(now time.Time, version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		v = "dev"
	}
	v = regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(v, "_")
	return fmt.Sprintf("%s-%s", now.UTC().Format("20060102150405.000"), v)
}

// SaveSlot copies the binary at installedPath into a new slot under
// slotsDir and writes its metadata.
func SaveSlot(slotsDir, installedPath, version, backupPath, upgradeId string) (*Slot, error) {
	return saveSlotAt(slotsDir, installedPath, version, backupPath, upgradeId, time.Now())
}

func saveSlotAt(slotsDir, installedPath, version, backupPath, upgradeId string, now time.Time) (*Slot, error) {
	name := slotDirName(now, version)
	dir := filepath.Join(slotsDir, name)
	// Two upgrades inside one second is not a thing that happens, but a
	// test loop does it constantly; disambiguate rather than clobber.
	for i := 1; ; i++ {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			break
		}
		dir = filepath.Join(slotsDir, fmt.Sprintf("%s.%d", name, i))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	binName := filepath.Base(installedPath)
	dest := filepath.Join(dir, binName)
	if err := copyFileMode(installedPath, dest, 0o755); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	sum, err := FileSHA256(dest)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	s := &Slot{
		Dir:           dir,
		BinaryName:    binName,
		Version:       version,
		SHA256:        sum,
		InstalledPath: installedPath,
		BackupPath:    backupPath,
		UpgradeId:     upgradeId,
		CreatedTs:     now.Unix(),
	}
	if err := s.writeMeta(); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return s, nil
}

func (s *Slot) writeMeta() error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, SlotMetaName), append(b, '\n'), 0o644)
}

// SetBackupPath records the database backup on an existing slot. The
// backup is taken by the *server* partway through the upgrade, after the
// slot already exists, so this is a second write rather than an argument.
func (s *Slot) SetBackupPath(p string) error {
	s.BackupPath = p
	return s.writeMeta()
}

// ListSlots returns the slots newest first. Directories that are not slots
// are ignored rather than reported: the state directory is the operator's
// too, and a stray file there is not an error.
func ListSlots(slotsDir string) ([]Slot, error) {
	entries, err := os.ReadDir(slotsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Slot
	for _, e := range entries {
		if !e.IsDir() || !slotDirRe.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(slotsDir, e.Name())
		b, err := os.ReadFile(filepath.Join(dir, SlotMetaName))
		if err != nil {
			continue
		}
		var s Slot
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		s.Dir = dir
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return filepath.Base(out[i].Dir) > filepath.Base(out[j].Dir)
	})
	return out, nil
}

// PruneSlots removes all but the newest `keep` slots and returns what it
// removed.
//
// Pruning runs *after* a new slot is written, never before — the same rule
// the backup retention follows, for the same reason: a prune that ran
// first would, in the disk-full case this code exists to survive, delete a
// good rollback point and then fail to create its replacement.
func PruneSlots(slotsDir string, keep int) ([]string, error) {
	if keep < 1 {
		keep = 1
	}
	slots, err := ListSlots(slotsDir)
	if err != nil {
		return nil, err
	}
	var removed []string
	for i := keep; i < len(slots); i++ {
		if err := os.RemoveAll(slots[i].Dir); err != nil {
			return removed, err
		}
		removed = append(removed, slots[i].Dir)
	}
	return removed, nil
}

// SpaceWarning returns a human-readable warning when the filesystem
// holding dir has less room than `keep` more copies of a `size`-byte
// binary would need, or "" when there is room or no answer.
//
// "No answer" is a pass, not a refusal — the same call diskfree makes for
// backups. Refusing to keep a rollback slot on a platform where
// diskfree has no implementation would remove the safety net on exactly
// the systems least able to spare it.
func SpaceWarning(dir string, size int64, keep int) string {
	avail, err := diskfree.Available(dir)
	if err != nil || size <= 0 {
		return ""
	}
	need := uint64(size) * uint64(keep)
	if avail >= need {
		return ""
	}
	return fmt.Sprintf("only %s free on %s; %d rollback slots of %s each need %s. "+
		"Older slots are pruned automatically, but a full disk will fail the next upgrade.",
		humanBytes(avail), dir, keep, humanBytes(uint64(size)), humanBytes(need))
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// copyFileMode copies src to dst, creating dst with mode and fsyncing it
// before the caller is told it exists.
func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
