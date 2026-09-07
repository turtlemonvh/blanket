package upgrade

/*

The CLI-owned journal (turtlemonvh/blanket#23 phase 6).

Phase 5 split the restart's state in two and deferred this half, because
nothing in the server reads it and a file format defined before its only
writer exists is a format defined by guesswork. This is the writer.

## Why the CLI needs a file of its own at all

The restart record in the database's `meta` bucket is what the *next
server process* reads. It cannot be what the CLI reads, for two reasons
that both bite at exactly the wrong moment:

  - the CLI cannot open the database. bolt's exclusive lock belongs to the
    server, and the interesting part of an upgrade is the window where the
    server is going away and coming back;
  - the record is cleared by the next boot, by design (phase 5's "a boot is
    the strongest evidence obtainable that the process which wrote it is
    gone"). The facts `--resume`, `--abort` and `blanket rollback` need —
    where the staged binary is, what the previous binary was, which backup
    was taken — must survive precisely the event that erases the record.

So the journal holds what only the driver knows, and it holds it across a
`kill -9` of the driver itself.

## The format

One JSON object per file, at `<state dir>/journal.json`, rewritten in full
on every transition via a temp file plus rename — a torn journal is worse
than none, because a half-written one still parses often enough to be
believed. `Steps` accumulates rather than replaces, so a human reading the
file after a failure sees the sequence and not just the end of it.

Fields are additive-only and unknown fields are ignored on read: an older
blanket must be able to read a journal a newer one wrote, since reading the
journal is exactly what a downgrade does.

*/

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// JournalSchema is bumped only for a change an older reader could not
// survive. Adding a field is not such a change.
const JournalSchema = 1

// The states one upgrade attempt moves through, from the *driver's* point
// of view. They deliberately mirror the server's restart states where the
// two coincide, so a journal and a `GET /ops/restart/status` read as the
// same story, and add the ones the server never sees: staging a download
// and keeping a rollback slot both happen entirely outside the server.
const (
	// JournalPlanned: a target has been chosen and nothing has been
	// downloaded. Written before the first network byte so a CLI killed
	// mid-download leaves a file that says what it was doing.
	JournalPlanned = "PLANNED"
	// JournalStaged: the new binary is on disk next to the installed one,
	// verified against SHA256SUMS, and not yet installed.
	JournalStaged = "STAGED"
	// JournalBackedUp: POST /ops/backup succeeded and BackupPath names it.
	JournalBackedUp = "BACKED_UP"
	// JournalPaused: the server is refusing worker spawn.
	JournalPaused = "PAUSED"
	// JournalSwapped: the installed binary is now the new one and the old
	// one is in a rollback slot. The single irreversible-looking step, and
	// the reason SlotPath is written in the same transition.
	JournalSwapped = "SWAPPED"
	// JournalDrained: workers stopped with respawn intent.
	JournalDrained = "DRAINED"
	// JournalExeced: POST /ops/restart/exec was accepted; the old server
	// is going away.
	JournalExeced = "EXECED"
	// JournalVerified: a server answered again, with a new instance id and
	// the expected version. Terminal, and the only success.
	JournalVerified = "VERIFIED"
	// JournalAborted: --abort ran, or the CLI unwound cleanly.
	JournalAborted = "ABORTED"
	// JournalFailed: terminal failure. The journal is kept, because it is
	// what --resume and rollback read.
	JournalFailed = "FAILED"
)

// journalOrder is the forward-only ordering of the non-terminal states,
// used by --resume to work out what is left to do.
var journalOrder = []string{
	JournalPlanned,
	JournalStaged,
	JournalBackedUp,
	JournalPaused,
	JournalSwapped,
	JournalDrained,
	JournalExeced,
	JournalVerified,
}

// JournalRank is the position of a state in the forward order, or -1 for a
// terminal/unknown one.
func JournalRank(state string) int {
	for i, s := range journalOrder {
		if s == state {
			return i
		}
	}
	return -1
}

// Step is one recorded transition.
type Step struct {
	State string `json:"state"`
	Ts    int64  `json:"ts"`
	Note  string `json:"note,omitempty"`
}

// Journal is the whole file.
type Journal struct {
	Schema int    `json:"schema"`
	Id     string `json:"id"`
	Action string `json:"action"` // "upgrade" or "rollback"
	State  string `json:"state"`

	StartedTs int64 `json:"startedTs"`
	UpdatedTs int64 `json:"updatedTs"`

	FromVersion string `json:"fromVersion,omitempty"`
	ToVersion   string `json:"toVersion,omitempty"`

	// BinaryPath is the *installed* path being replaced, already resolved
	// through any symlink. StagedPath is the verified temp file beside it.
	BinaryPath string `json:"binaryPath,omitempty"`
	StagedPath string `json:"stagedPath,omitempty"`
	AssetName  string `json:"assetName,omitempty"`
	SHA256     string `json:"sha256,omitempty"`

	// SlotPath is the rollback slot holding the binary that was replaced.
	SlotPath string `json:"slotPath,omitempty"`
	// BackupPath is the pre-upgrade database backup, as reported by the
	// server that took it.
	BackupPath string `json:"backupPath,omitempty"`

	// Source is "github" or "bundle", and BundlePath names the bundle.
	Source     string `json:"source,omitempty"`
	BundlePath string `json:"bundlePath,omitempty"`

	// Port, RestartId and FromInstanceId tie this attempt to the server
	// side of it. FromInstanceId is what makes "a different process came
	// back" checkable rather than assumed.
	Port           int    `json:"port,omitempty"`
	RestartId      string `json:"restartId,omitempty"`
	FromInstanceId string `json:"fromInstanceId,omitempty"`
	ToInstanceId   string `json:"toInstanceId,omitempty"`

	// DrainRequested records the decision, not the outcome: a resume must
	// not silently change whether the fleet gets stopped.
	DrainRequested bool `json:"drainRequested,omitempty"`

	// Supervised is what the server being replaced said about itself
	// before it went away. Recorded here rather than re-derived, because
	// by the time it matters -- deciding whether anything is going to
	// start the replacement -- there is no server left to ask.
	Supervised bool `json:"supervised,omitempty"`

	Error string `json:"error,omitempty"`
	Steps []Step `json:"steps,omitempty"`
}

// JournalName is the file's basename inside the upgrade state directory.
const JournalName = "journal.json"

// LoadJournal reads a journal. A missing file is (nil, nil): "no upgrade
// has ever run here" is a normal answer, not an error.
func LoadJournal(p string) (*Journal, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j Journal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("%s is not a readable upgrade journal: %w", p, err)
	}
	return &j, nil
}

// NewJournal starts one.
func NewJournal(id, action string, now time.Time) *Journal {
	return &Journal{
		Schema:    JournalSchema,
		Id:        id,
		Action:    action,
		State:     JournalPlanned,
		StartedTs: now.Unix(),
		UpdatedTs: now.Unix(),
		Steps:     []Step{{State: JournalPlanned, Ts: now.Unix()}},
	}
}

// Advance records a transition. It does not enforce the ordering: the
// sequencer in command/upgrade.go decides what may follow what, and a
// journal that refused to record a state would be a journal that lies
// about what happened.
func (j *Journal) Advance(state, note string) {
	now := time.Now().Unix()
	j.State = state
	j.UpdatedTs = now
	j.Steps = append(j.Steps, Step{State: state, Ts: now, Note: note})
}

// Save writes the journal atomically: temp file in the same directory,
// fsync, rename. The directory is created if needed.
func (j *Journal) Save(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(p), ".journal-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has succeeded

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, p)
}

// Terminal reports whether this journal describes a finished attempt —
// one --resume has nothing to do with.
func (j *Journal) Terminal() bool {
	switch j.State {
	case JournalVerified, JournalAborted, JournalFailed:
		return true
	}
	return false
}
