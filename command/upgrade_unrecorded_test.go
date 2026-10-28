package command

// turtlemonvh/blanket#278, the CLI half: two journal/disk desyncs that the
// upgrade path handled and --abort / --resume did not.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/upgrade"
)

// The CLI is killed after Swap() renamed the staged binary into place but
// before the journal recorded SWAPPED (#203's window). --resume detects the
// swap from the installed binary's digest; --abort said "nothing was
// installed".
func TestUpgradeAbort_SeesAnUnrecordedSwap(t *testing.T) {
	withUpgradeFlags(t)
	stub := &stubRestartServer{abortedResp: `{"aborted":{"state":"PAUSED"},"respawned":false}`}
	port := startStubRestartServer(t, stub)

	dir := t.TempDir()
	bin := filepath.Join(dir, "blanket")
	// What the rename left at the installed path: the staged (new) binary.
	require.NoError(t, os.WriteFile(bin, []byte("new binary"), 0o755))
	sum, err := upgrade.FileSHA256(bin)
	require.NoError(t, err)

	j := upgrade.NewJournal("abc", "upgrade", time.Now())
	j.FromVersion, j.ToVersion = "v0.3.0", "v0.4.0"
	j.BinaryPath = bin
	j.Port = port
	j.SHA256 = sum                                            // digest of the file that was staged
	j.StagedPath = filepath.Join(dir, ".blanket-upgrade-123") // consumed by the rename
	for _, s := range []string{upgrade.JournalStaged, upgrade.JournalBackedUp, upgrade.JournalPaused} {
		j.Advance(s, "")
	}
	p := filepath.Join(dir, "upgrade", upgrade.JournalName)
	require.NoError(t, j.Save(p))

	res := &upgradeResult{Action: "upgrade", JournalPath: p}
	code := runUpgradeAbort(res)
	t.Logf("exit %d: %q", code, res.Message)

	assert.NotEqual(t, "Aborted; nothing was installed.", res.Message,
		"the installed binary hashes to the staged digest: the swap happened")
	assert.Contains(t, res.Message, "already installed")
}

// The server crashed (or was restarted by its supervisor) after SWAPPED and
// before the drain: the CLI's drain call failed and the journal says FAILED.
// The supervisor brought the server back on the new binary, so the record
// is IDLE and nothing was drained -- every worker still runs the old binary.
// --resume used to verify the new server and report the upgrade done.
func TestUpgradeResume_WarnsWhenNoDrainRan(t *testing.T) {
	withUpgradeFlags(t)
	stub := &stubRestartServer{execed: true, oldVersion: "v0.3.0", newVersion: "v0.4.0"}
	port := startStubRestartServer(t, stub)
	p, _ := failedJournal(t, port,
		upgrade.JournalStaged, upgrade.JournalBackedUp, upgrade.JournalPaused,
		upgrade.JournalSwapped, upgrade.JournalSwapped) // never DRAINED

	res := &upgradeResult{Action: "upgrade", JournalPath: p}
	code := runUpgradeResume(res, "blanket")
	t.Logf("exit %d state %s warnings %v message %q", code, res.State, res.Warnings, res.Message)

	assert.True(t, hasWarning(res, "no drain was recorded"),
		"no drain ever ran, so workers started before the upgrade still run the old binary")
}
