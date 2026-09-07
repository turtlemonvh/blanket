package command

/*

`blanket rollback` (turtlemonvh/blanket#23 phase 6).

The other half of `blanket upgrade`, and the reason that command keeps a
copy of what it replaces. It puts the previous binary back, and with
--restore-db the pre-upgrade database backup with it.

## Why the database is opt-in and the binary is not

They are not the same kind of undo. Putting the binary back loses nothing:
the file it replaces is still in a slot, and the tasks in the database are
untouched. Putting the *database* back discards every task, worker record
and queue entry created since the backup was taken — which, on an install
that has been running since the upgrade, is all the work it has done.

So the binary rolls back on `--yes` and the database only on
`--restore-db`, and the two are deliberately different words.

## Why --restore-db needs the server stopped

The same reason `blanket migrate --restore` does, and it is not a policy
choice: bolt holds an exclusive flock for the life of an open handle, and
overwriting the file underneath a running server does not restore anything
— the server has the old pages mapped and flushes them back over the new
contents on its next write. RestoreBackup proves the database is idle by
taking the lock itself, which is the only real check there is.

*/

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	boltlib "github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/upgrade"
)

var rollbackConf struct {
	Yes       bool
	JSON      bool
	List      bool
	RestoreDB bool
	Slot      string
}

var rollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Put the previous blanket binary back, and optionally its database backup",
	Long: `Reinstall the binary that the last ` + "`blanket upgrade`" + ` replaced.

Up to three rollback slots are kept, each holding the binary that was
replaced and the name of the database backup taken just before the upgrade.

  blanket rollback --list                what can be rolled back to
  blanket rollback --yes                 put the previous binary back and restart
  blanket rollback --slot <dir> --yes    a specific slot
  blanket rollback --restore-db --yes    also restore that slot's database backup
                                         (needs the server stopped)

Exit codes match ` + "`blanket upgrade`" + `: 0 done, 10 nothing to roll back to,
11 the binary was replaced without a restart, 12 verification failed,
13 the restart was refused, 1 error, 2 usage.`,
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		InitializeLogging()
		os.Exit(runRollback())
	},
}

func init() {
	rollbackCmd.Flags().BoolVar(&rollbackConf.Yes, "yes", false, "Confirm: actually replace the binary and restart")
	rollbackCmd.Flags().BoolVar(&rollbackConf.JSON, "json", false, "Emit one JSON object instead of prose")
	rollbackCmd.Flags().BoolVar(&rollbackConf.List, "list", false, "List the rollback slots and exit")
	rollbackCmd.Flags().BoolVar(&rollbackConf.RestoreDB, "restore-db", false, "Also restore the slot's pre-upgrade database backup (server must be stopped)")
	rollbackCmd.Flags().StringVar(&rollbackConf.Slot, "slot", "", "Roll back to this slot directory (default: the newest)")
	RootCmd.AddCommand(rollbackCmd)
}

func runRollback() int {
	// The two commands share --json, so they share the emitter.
	upgradeConf.JSON = rollbackConf.JSON
	res := &upgradeResult{Action: "rollback", FromVersion: RawVersion, JournalPath: upgradeJournalPath()}

	installed, err := resolveInstalledBinary()
	if err != nil {
		return res.fail(ExitError, err)
	}
	res.BinaryPath = installed
	upgrade.SweepDisplaced(installed)

	slotsDir := upgradeSlotsDir()
	slots, err := upgrade.ListSlots(slotsDir)
	if err != nil {
		return res.fail(ExitError, err)
	}

	if rollbackConf.List {
		res.ExitCode = ExitOK
		res.Message = formatSlots(slotsDir, slots)
		if len(slots) == 0 {
			res.ExitCode = ExitNothingToDo
		}
		return res.emit()
	}

	if len(slots) == 0 {
		return res.fail(ExitNothingToDo, fmt.Errorf("no rollback slots in %s; there is nothing to roll back to", slotsDir))
	}

	slot := slots[0]
	if rollbackConf.Slot != "" {
		want := filepath.Clean(rollbackConf.Slot)
		found := false
		for _, s := range slots {
			if filepath.Clean(s.Dir) == want || filepath.Base(s.Dir) == rollbackConf.Slot {
				slot, found = s, true
				break
			}
		}
		if !found {
			return res.fail(ExitUsage, fmt.Errorf("no rollback slot %q; `blanket rollback --list` shows what there is", rollbackConf.Slot))
		}
	}
	res.ToVersion = slot.Version
	res.SlotPath = slot.Dir
	res.BackupPath = slot.BackupPath

	if rollbackConf.RestoreDB && slot.BackupPath == "" {
		return res.fail(ExitUsage, fmt.Errorf("slot %s recorded no database backup, so there is nothing for --restore-db to restore", filepath.Base(slot.Dir)))
	}

	if !rollbackConf.Yes {
		what := fmt.Sprintf("this replaces %s with %s from %s", installed, displayVersionOr(slot.Version), filepath.Base(slot.Dir))
		if rollbackConf.RestoreDB {
			what += fmt.Sprintf(", and REPLACES THE DATABASE with %s (every task recorded since then is lost)", slot.BackupPath)
		}
		return res.fail(ExitUsage, fmt.Errorf("%s. Re-run with --yes to confirm", what))
	}

	// The saved binary is verified against the digest recorded when it was
	// saved, before it is put anywhere. A rollback that installed a
	// corrupted binary would turn a bad upgrade into an unbootable install
	// -- and the slot has been sitting on disk since some earlier day.
	if err := upgrade.VerifyFileDigest(slot.BinaryPath(), slot.BinaryName, slot.SHA256); err != nil {
		return res.fail(ExitVerificationFailed, fmt.Errorf("rollback slot %s is damaged: %w", filepath.Base(slot.Dir), err))
	}

	port := viper.GetInt("port")
	serverUp := serverAnswers(port)

	if rollbackConf.RestoreDB && serverUp {
		return res.fail(ExitUsage, fmt.Errorf(
			"--restore-db replaces the database file, which cannot be done while a server holds its lock.\n"+
				"Stop blanket on port %d and run this again", port))
	}

	j := upgrade.NewJournal(objectid.NewObjectId().Hex(), "rollback", time.Now())
	j.FromVersion = RawVersion
	j.ToVersion = slot.Version
	j.BinaryPath = installed
	j.SlotPath = slot.Dir
	j.BackupPath = slot.BackupPath
	j.Port = port
	_ = j.Save(res.JournalPath)

	// -------------------------------------------------------------------
	// Announce, back up, pause -- the same opening the upgrade uses. The
	// binary about to be installed is old, not trusted: a rollback is a
	// restart too, and skipping the pause would let the server fork a
	// worker from a half-swapped path.
	// -------------------------------------------------------------------
	if serverUp {
		st, err := fetchRestartStatus(port)
		if err != nil {
			return res.fail(ExitRestartRefused, err)
		}
		if st.Restart.State != "" && st.Restart.State != "IDLE" {
			return res.fail(ExitRestartRefused, fmt.Errorf(
				"a restart is already in flight (%s). Finish or abort it first", st.Restart.State))
		}
		j.FromInstanceId = st.InstanceId
		j.Supervised = st.Supervised

		body, _ := json.Marshal(map[string]string{
			"reason":   fmt.Sprintf("blanket rollback %s -> %s", displayRawVersion(), displayVersionOr(slot.Version)),
			"execMode": viper.GetString("restart.execMode"),
		})
		if _, err := opsCallTimeout(port, http.MethodPost, "/ops/restart/begin", body, httpx.DefaultRequestTimeout); err != nil {
			return res.fail(ExitRestartRefused, err)
		}
		// A backup before a rollback, not only before an upgrade: the
		// binary going back is older than the schema in the file, and if
		// it turns out to refuse the database ("written by a NEWER
		// blanket"), the operator needs a copy of what they had at this
		// moment, not at the moment of the upgrade.
		if bb, err := opsCallTimeout(port, http.MethodPost, "/ops/backup", nil, 5*time.Minute); err == nil {
			var backup struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(bb, &backup)
			j.Advance(upgrade.JournalBackedUp, backup.Path)
		} else {
			res.Warnings = append(res.Warnings, fmt.Sprintf("could not take a pre-rollback backup: %v", err))
		}
		if _, err := opsCallTimeout(port, http.MethodPost, "/ops/restart/pause", nil, httpx.DefaultRequestTimeout); err != nil {
			return res.fail(ExitRestartRefused, err)
		}
		j.Advance(upgrade.JournalPaused, "worker spawn paused")
		_ = j.Save(res.JournalPath)
	}

	// -------------------------------------------------------------------
	// Stage the slot's binary and swap it in. Staging rather than copying
	// straight over: the installed path is never a partial file, on this
	// path as much as on the upgrade path.
	// -------------------------------------------------------------------
	sums := upgrade.Sums{slot.BinaryName: slot.SHA256}
	staged, err := upgrade.StageFromFile(installed, slot.BinaryName, slot.BinaryPath(), sums)
	if err != nil {
		return res.fail(ExitVerificationFailed, err)
	}
	res.StagedPath = staged.Path

	// Keep what we are replacing, so a rollback is itself undoable -- the
	// operator who rolls back and discovers the old binary was not the
	// problem should not have to go and find the new one again.
	if newSlot, err := upgrade.SaveSlot(slotsDir, installed, RawVersion, "", j.Id); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("could not keep a copy of the binary being rolled back: %v", err))
	} else {
		defer func() { _, _ = upgrade.PruneSlots(slotsDir, upgradeSlotCount()) }()
		_ = newSlot
	}

	if err := upgrade.Swap(staged.Path, installed); err != nil {
		os.Remove(staged.Path)
		return res.fail(ExitError, fmt.Errorf("could not install %s: %w", installed, err))
	}
	j.StagedPath = ""
	j.Advance(upgrade.JournalSwapped, "restored "+displayVersionOr(slot.Version))
	_ = j.Save(res.JournalPath)

	// -------------------------------------------------------------------
	// The database, if asked. Server is known to be down at this point.
	// -------------------------------------------------------------------
	if rollbackConf.RestoreDB {
		rr, err := boltlib.RestoreBackup(slot.BackupPath, viper.GetString("database"))
		if err != nil {
			return res.fail(ExitError, fmt.Errorf("the binary was rolled back but the database was not: %w", err))
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"restored %s (schema version %d); the database that was there is kept at %s", rr.BackupPath, rr.SchemaVersion, rr.DisplacedPath))
	}

	if !serverUp {
		res.ExitCode = ExitStagedOnly
		res.State = j.State
		j.Advance(upgrade.JournalVerified, "no server was running")
		_ = j.Save(res.JournalPath)
		res.Message = fmt.Sprintf("Rolled back to %s at %s.\nNo server was running, so nothing was restarted.",
			displayVersionOr(slot.Version), installed)
		return res.emit()
	}

	// execAndVerify is shared with `blanket upgrade`, and verifies against
	// j.ToVersion -- which here is the slot's version. A dev-build slot
	// has none, and VersionFromBanner returning "" is exactly the case
	// that check already skips.
	return execAndVerify(res, j, port)
}

func formatSlots(slotsDir string, slots []upgrade.Slot) string {
	if len(slots) == 0 {
		return fmt.Sprintf("No rollback slots in %s.\nOne is created every time `blanket upgrade` replaces the binary.", slotsDir)
	}
	out := fmt.Sprintf("Rollback slots in %s (newest first):\n", slotsDir)
	for i, s := range slots {
		marker := "  "
		if i == 0 {
			marker = "* "
		}
		out += fmt.Sprintf("%s%s\n    version:  %s\n    binary:   %s\n    backup:   %s\n    saved:    %s\n",
			marker, filepath.Base(s.Dir), displayVersionOr(s.Version), s.BinaryPath(), orDash(s.BackupPath),
			time.Unix(s.CreatedTs, 0).Format(time.RFC3339))
	}
	out += "\n* is what `blanket rollback --yes` would use."
	return out
}
