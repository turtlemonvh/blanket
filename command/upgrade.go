package command

/*

`blanket upgrade` (turtlemonvh/blanket#23 phase 6) — the last piece of the
clean upgrade process, and the command the whole of phases 1–5 was built
to make possible.

It is a *driver*, not a second implementation of anything. The state
machine is the server's (phase 5), the backup is the server's (phase 4),
the migration is the server's (phase 4), and the respawn debt to the
workers is the server's (phases 3 and 5). What this command owns is the
part that lives outside the process: choosing a release, proving the bytes
are the right bytes, putting the file on disk without ever leaving a
partial one there, keeping the file it replaced, and writing down what it
did so a `kill -9` of the CLI is recoverable.

## The sequence

	discover   -> which version, and where do its bytes come from
	verify     -> SHA256SUMS, always, with no way to turn it off
	stage      -> a temp file beside the installed binary
	begin      -> POST /ops/restart/begin
	backup     -> POST /ops/backup (advances the record to BACKED_UP)
	pause      -> POST /ops/restart/pause; no worker is forked from here on
	slot+swap  -> keep the old binary, rename the new one into place
	swapped    -> POST /ops/restart/swapped
	drain      -> POST /ops/restart/drain, unless --drain-mode never
	exec       -> POST /ops/restart/exec
	verify     -> a server answers with a NEW instance id and the new version

Every step is journalled before the next one starts, so --resume can pick
the sequence up and --abort can unwind it.

## Why an upgrade drains and a routine restart does not

Brief decision row 3: a worker rides out a server restart by design, so
stopping the fleet for a config change costs real work for nothing. An
upgrade is the case on the other side of that line — the new binary
contains new *worker* code, and a worker that survived the restart is
running the old one. So `blanket upgrade` asks for the drain that
`POST /ops/restart/begin; POST /ops/restart/exec` does not, and
`--drain-mode never` is how an operator who would rather keep a long task
running says so.

## Why there is no --no-verify

Row 11. A flag that disables the only integrity check on a binary about to
replace the one you are running is a flag that every "just make it work"
answer on the internet tells people to pass. Releases published before
phase 6 carry no SHA256SUMS asset and therefore cannot be auto-upgraded
*to*; the error says so and points at --bundle, which carries its own
checksums and is verified identically.

*/

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/timing"
	"github.com/turtlemonvh/blanket/lib/upgrade"
)

var upgradeConf struct {
	Check     bool
	StageOnly bool
	NoRestart bool
	Yes       bool
	JSON      bool
	Resume    bool
	Abort     bool
	PrintPlan bool
	Bundle    string
	BaseURL   string
}

// verifyBudget is how long the CLI waits for a replacement server to
// answer. Unscaled constant through timing.Scale, per lib/timing's rule.
const verifyBudget = 90 * time.Second

var upgradeCmd = &cobra.Command{
	Use:   "upgrade [version]",
	Short: "Install a newer blanket and restart the server onto it",
	Long: `Install a newer blanket and restart the running server onto it.

With no version argument the newest published release is used. Binaries are
verified against the release's SHA256SUMS before anything is installed, and
the binary being replaced is kept in a rollback slot -- see ` + "`blanket rollback`" + `.

  blanket upgrade --check              is a newer version available?
  blanket upgrade --print-plan         the exact steps, with real paths
  blanket upgrade --yes                do it
  blanket upgrade v0.5.0 --yes         a specific version
  blanket upgrade --bundle b.tar.gz --yes   from an offline bundle
  blanket upgrade --stage-only         download + verify, install nothing
  blanket upgrade --no-restart --yes   install, leave the server alone
  blanket upgrade --resume --yes       finish an attempt that was interrupted
  blanket upgrade --abort              unwind an attempt that was interrupted

Exit codes: 0 done (or, for --check, an upgrade is available), 10 nothing to
do, 11 staged/installed without a restart, 12 verification failed, 13 the
restart was refused, 1 error, 2 usage.`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		InitializeLogging()
		target := ""
		if len(args) == 1 {
			target = args[0]
		}
		os.Exit(runUpgrade(target))
	},
}

func init() {
	upgradeCmd.Flags().BoolVar(&upgradeConf.Check, "check", false, "Report whether an upgrade is available and change nothing")
	upgradeCmd.Flags().BoolVar(&upgradeConf.StageOnly, "stage-only", false, "Download and verify the new binary but do not install it")
	upgradeCmd.Flags().BoolVar(&upgradeConf.NoRestart, "no-restart", false, "Install the new binary but leave the running server alone")
	upgradeCmd.Flags().BoolVar(&upgradeConf.Yes, "yes", false, "Confirm: actually replace the binary and restart")
	upgradeCmd.Flags().BoolVar(&upgradeConf.JSON, "json", false, "Emit one JSON object instead of prose")
	upgradeCmd.Flags().BoolVar(&upgradeConf.Resume, "resume", false, "Continue the attempt recorded in the upgrade journal")
	upgradeCmd.Flags().BoolVar(&upgradeConf.Abort, "abort", false, "Unwind the attempt recorded in the upgrade journal")
	upgradeCmd.Flags().BoolVar(&upgradeConf.PrintPlan, "print-plan", false, "Print the steps this would run, with real paths, and stop")
	upgradeCmd.Flags().StringVar(&upgradeConf.Bundle, "bundle", "", "Install from an offline bundle (.tar.gz or an extracted directory)")
	upgradeCmd.Flags().StringVar(&upgradeConf.BaseURL, "releases-base-url", "", "Releases API base URL (tests; default the upgrade.releasesBaseURL config key)")
	// Hidden because it exists for scripts/upgrade.sh: a test suite that
	// reaches api.github.com is a test suite that fails whenever GitHub
	// rate-limits an unauthenticated CI runner.
	_ = upgradeCmd.Flags().MarkHidden("releases-base-url")
	RootCmd.AddCommand(upgradeCmd)
}

// ---------------------------------------------------------------------------
// Result reporting
// ---------------------------------------------------------------------------

// upgradeResult is the --json document, and the field set the prose
// output is derived from so the two can never drift.
type upgradeResult struct {
	Action           string   `json:"action"`
	State            string   `json:"state"`
	ExitCode         int      `json:"exitCode"`
	FromVersion      string   `json:"fromVersion,omitempty"`
	ToVersion        string   `json:"toVersion,omitempty"`
	UpgradeAvailable bool     `json:"upgradeAvailable"`
	Source           string   `json:"source,omitempty"`
	BinaryPath       string   `json:"binaryPath,omitempty"`
	StagedPath       string   `json:"stagedPath,omitempty"`
	SlotPath         string   `json:"slotPath,omitempty"`
	BackupPath       string   `json:"backupPath,omitempty"`
	JournalPath      string   `json:"journalPath,omitempty"`
	InstanceId       string   `json:"instanceId,omitempty"`
	Message          string   `json:"message,omitempty"`
	Error            string   `json:"error,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
}

// emit prints the result in whichever shape was asked for and returns the
// exit code, so every return path in this file is one line.
func (r *upgradeResult) emit() int {
	if upgradeConf.JSON {
		b, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return ExitError
		}
		fmt.Println(string(b))
		return r.ExitCode
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if r.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", r.Error)
	}
	if r.Message != "" {
		fmt.Println(r.Message)
	}
	return r.ExitCode
}

func (r *upgradeResult) fail(code int, err error) int {
	r.ExitCode = code
	r.Error = err.Error()
	return r.emit()
}

// ---------------------------------------------------------------------------
// The command
// ---------------------------------------------------------------------------

func runUpgrade(targetVersion string) int {
	res := &upgradeResult{Action: "upgrade", FromVersion: RawVersion, JournalPath: upgradeJournalPath()}

	// Flag combinations that cannot mean anything, refused before any
	// state is touched.
	switch {
	case upgradeConf.Abort && (upgradeConf.Resume || upgradeConf.Check || upgradeConf.StageOnly):
		return res.fail(ExitUsage, errors.New("--abort cannot be combined with --resume, --check or --stage-only"))
	case upgradeConf.Resume && upgradeConf.Check:
		return res.fail(ExitUsage, errors.New("--resume cannot be combined with --check"))
	case upgradeConf.StageOnly && upgradeConf.NoRestart:
		return res.fail(ExitUsage, errors.New("--stage-only already installs nothing; --no-restart adds nothing to it"))
	case upgradeConf.Bundle != "" && targetVersion != "":
		return res.fail(ExitUsage, errors.New("a bundle already names its version; drop the version argument"))
	}

	installed, err := resolveInstalledBinary()
	if err != nil {
		return res.fail(ExitError, err)
	}
	res.BinaryPath = installed
	// Sweep any `<binary>.old-*` left by a previous windows swap whose
	// process has since exited. A no-op elsewhere.
	upgrade.SweepDisplaced(installed)

	if upgradeConf.Abort {
		return runUpgradeAbort(res)
	}
	if upgradeConf.Resume {
		return runUpgradeResume(res, installed)
	}

	// -------------------------------------------------------------------
	// Discovery
	// -------------------------------------------------------------------
	src, err := resolveSource(targetVersion)
	if err != nil {
		return res.fail(ExitError, err)
	}
	defer src.Close()
	res.ToVersion = src.Version
	res.Source = src.Kind

	upToDate := upgrade.SameVersion(RawVersion, src.Version)
	res.UpgradeAvailable = !upToDate

	if upgradeConf.PrintPlan {
		res.ExitCode = ExitOK
		res.Message = printPlan(installed, src)
		return res.emit()
	}

	if upgradeConf.Check {
		if upToDate {
			res.ExitCode = ExitNothingToDo
			res.Message = fmt.Sprintf("blanket %s is the newest release; nothing to do.", displayRawVersion())
		} else {
			res.ExitCode = ExitOK
			res.Message = fmt.Sprintf("An upgrade is available: %s (you have %s).\nRun `blanket upgrade --yes` to install it.",
				src.Version, displayRawVersion())
		}
		return res.emit()
	}

	if upToDate {
		res.ExitCode = ExitNothingToDo
		res.Message = fmt.Sprintf("Already running %s; nothing to do.", src.Version)
		return res.emit()
	}

	if !upgradeConf.Yes && !upgradeConf.StageOnly {
		return res.fail(ExitUsage, fmt.Errorf(
			"this replaces %s with %s and restarts the server. Re-run with --yes to confirm, "+
				"or --print-plan to see the steps first", installed, src.Version))
	}

	// -------------------------------------------------------------------
	// Journal + staging
	// -------------------------------------------------------------------
	j := upgrade.NewJournal(objectid.NewObjectId().Hex(), "upgrade", time.Now())
	j.FromVersion = RawVersion
	j.ToVersion = src.Version
	j.BinaryPath = installed
	j.AssetName = src.AssetName
	j.Source = src.Kind
	j.BundlePath = src.BundlePath
	j.Port = viper.GetInt("port")
	if err := j.Save(res.JournalPath); err != nil {
		return res.fail(ExitError, fmt.Errorf("could not write the upgrade journal: %w", err))
	}

	// Leftover staging files from a CLI that was killed mid-download are
	// dead weight, not resumable state: an unverified partial download can
	// only be thrown away.
	upgrade.CleanStaging(installed, "")

	staged, err := src.Stage(installed)
	if err != nil {
		j.Error = err.Error()
		j.Advance(upgrade.JournalFailed, "staging failed")
		_ = j.Save(res.JournalPath)
		code := ExitError
		if errors.Is(err, upgrade.ErrChecksumMismatch) || errors.Is(err, upgrade.ErrNoChecksumEntry) {
			code = ExitVerificationFailed
		}
		return res.fail(code, err)
	}
	res.StagedPath = staged.Path
	j.StagedPath = staged.Path
	j.SHA256 = staged.SHA256
	j.Advance(upgrade.JournalStaged, fmt.Sprintf("verified sha256 %s", staged.SHA256))
	if err := j.Save(res.JournalPath); err != nil {
		return res.fail(ExitError, err)
	}

	if upgradeConf.StageOnly {
		res.ExitCode = ExitStagedOnly
		res.State = upgrade.JournalStaged
		res.Message = fmt.Sprintf("Staged %s at %s (sha256 %s).\nNothing has been installed. Run `blanket upgrade --resume --yes` to finish.",
			src.Version, staged.Path, staged.SHA256)
		return res.emit()
	}

	return finishUpgrade(res, j, staged.Path)
}

// ---------------------------------------------------------------------------
// The part --resume and the first run share
// ---------------------------------------------------------------------------

// finishUpgrade runs everything from "there is a verified binary staged"
// to "a new server answered", journalling each step.
//
// Split out so --resume is literally the same code rather than a parallel
// implementation of it: a resume path that drifts from the first-run path
// is a resume path that is only exercised when something has already gone
// wrong.
func finishUpgrade(res *upgradeResult, j *upgrade.Journal, stagedPath string) int {
	jp := res.JournalPath
	port := viper.GetInt("port")
	if j.Port != 0 {
		port = j.Port
	}

	save := func() {
		if err := j.Save(jp); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("could not update the upgrade journal: %v", err))
		}
	}
	failWith := func(code int, err error) int {
		j.Error = err.Error()
		j.Advance(upgrade.JournalFailed, err.Error())
		save()
		res.State = j.State
		return res.fail(code, err)
	}

	// -------------------------------------------------------------------
	// Is there a server to restart at all?
	// -------------------------------------------------------------------
	// Retried rather than asked once: a server re-execing in place is not
	// listening for a moment, and reading that instant as "no server is
	// running" would swap the binary and stop, leaving an install whose
	// server is still on the version it was upgraded away from.
	var st *restartStatus
	serverUp := false
	if s, err := awaitRestartStatus(port, timing.Scale(statusProbeBudget)); err == nil {
		st, serverUp = s, true
	} else if !errors.Is(err, errServerDown) {
		// The server answered and said no -- a 403 from the ops guard, or
		// a 500. Going ahead would mean swapping a binary under a server
		// we could not then restart.
		return failWith(ExitRestartRefused, err)
	}

	// A restart already in flight is normally a refusal -- two drivers
	// racing over one state machine is how an install ends up paused with
	// nobody to un-pause it. The exception is *our own* attempt: a
	// --resume picking up after the CLI was killed finds the record it
	// wrote itself, matched by id, and continues from wherever it got to.
	//
	// Which is why what follows is driven by the server's rank in the
	// state machine rather than by a resuming/not-resuming flag: the
	// transitions are forward-only, so "do the ones that have not happened
	// yet" is both the first-run path and the resume path, written once.
	rank := -1
	if serverUp {
		rank = database.RestartStateRank(st.Restart.State)
		if st.Restart.State != "" && st.Restart.State != database.RestartStateIdle &&
			(st.Restart.Id == "" || st.Restart.Id != j.RestartId) {
			return failWith(ExitRestartRefused, fmt.Errorf(
				"a restart is already in flight (%s, id %s). Finish it, or `curl -H 'X-Blanket-Restart: 1' -X POST http://localhost:%d/ops/restart/abort`",
				st.Restart.State, st.Restart.Id, port))
		}
		j.FromInstanceId = st.InstanceId
		j.Supervised = st.Supervised
		if j.BackupPath == "" && st.Restart.BackupPath != "" {
			j.BackupPath = st.Restart.BackupPath
			res.BackupPath = st.Restart.BackupPath
		}
	}

	// -------------------------------------------------------------------
	// begin -> backup -> pause
	// -------------------------------------------------------------------
	if serverUp && !upgradeConf.NoRestart {
		if rank < database.RestartStateRank(database.RestartStateStaged) {
			body, _ := json.Marshal(map[string]string{
				"reason":   fmt.Sprintf("blanket upgrade %s -> %s", displayRawVersion(), j.ToVersion),
				"execMode": viper.GetString("restart.execMode"),
			})
			if _, err := opsCallTimeout(port, http.MethodPost, "/ops/restart/begin", body, httpx.DefaultRequestTimeout); err != nil {
				return failWith(ExitRestartRefused, err)
			}
			if s, err := fetchRestartStatus(port); err == nil {
				j.RestartId = s.Restart.Id
			}
		}

		// The backup is what makes the whole thing reversible: `blanket
		// rollback --restore-db` restores exactly this file. It is taken
		// by the server, because the CLI cannot open a database the server
		// holds the lock on.
		if rank < database.RestartStateRank(database.RestartStateBackedUp) {
			bb, err := opsCallTimeout(port, http.MethodPost, "/ops/backup", nil, 5*time.Minute)
			if err != nil {
				return failWith(ExitError, fmt.Errorf("pre-upgrade backup failed: %w", err))
			}
			var backup struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(bb, &backup)
			j.BackupPath = backup.Path
			res.BackupPath = backup.Path
			j.Advance(upgrade.JournalBackedUp, backup.Path)
			save()
		}

		if rank < database.RestartStateRank(database.RestartStatePaused) {
			if _, err := opsCallTimeout(port, http.MethodPost, "/ops/restart/pause", nil, httpx.DefaultRequestTimeout); err != nil {
				return failWith(ExitRestartRefused, err)
			}
			j.Advance(upgrade.JournalPaused, "worker spawn paused")
			save()
		}
	}

	// -------------------------------------------------------------------
	// Keep the old binary, then swap
	// -------------------------------------------------------------------
	slotsDir := upgradeSlotsDir()
	if fi, err := os.Stat(j.BinaryPath); err == nil {
		if w := upgrade.SpaceWarning(slotsDir, fi.Size(), upgradeSlotCount()); w != "" {
			res.Warnings = append(res.Warnings, w)
		}
	}
	slot, err := upgrade.SaveSlot(slotsDir, j.BinaryPath, j.FromVersion, j.BackupPath, j.Id)
	if err != nil {
		return failWith(ExitError, fmt.Errorf("could not keep a rollback copy of %s: %w", j.BinaryPath, err))
	}
	j.SlotPath = slot.Dir
	res.SlotPath = slot.Dir

	if err := upgrade.Swap(stagedPath, j.BinaryPath); err != nil {
		return failWith(ExitError, fmt.Errorf("could not install %s: %w", j.BinaryPath, err))
	}
	j.StagedPath = ""
	j.Advance(upgrade.JournalSwapped, fmt.Sprintf("installed %s, previous binary kept at %s", j.ToVersion, slot.Dir))
	save()

	// Pruning runs after the new slot exists, never before: a prune that
	// ran first would, on a full disk, delete a good rollback point and
	// then fail to create its replacement.
	if removed, err := upgrade.PruneSlots(slotsDir, upgradeSlotCount()); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("could not prune old rollback slots: %v", err))
	} else if len(removed) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("pruned %d old rollback slot(s)", len(removed)))
	}

	if !serverUp {
		res.ExitCode = ExitStagedOnly
		res.State = j.State
		j.Advance(upgrade.JournalVerified, "no server was running; nothing to restart")
		save()
		res.Message = fmt.Sprintf("Installed %s at %s.\nNo blanket server was answering on port %d, so nothing was restarted.",
			j.ToVersion, j.BinaryPath, port)
		return res.emit()
	}
	if upgradeConf.NoRestart {
		res.ExitCode = ExitStagedOnly
		res.State = j.State
		res.Message = fmt.Sprintf("Installed %s at %s.\nThe running server is still on %s; restart it when you're ready "+
			"(`blanket upgrade --resume --yes` drives the restart).", j.ToVersion, j.BinaryPath, displayRawVersion())
		return res.emit()
	}

	return execAndVerify(res, j, port)
}

// execAndVerify runs swapped -> drain -> exec -> verify.
func execAndVerify(res *upgradeResult, j *upgrade.Journal, port int) int {
	jp := res.JournalPath
	save := func() {
		if err := j.Save(jp); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("could not update the upgrade journal: %v", err))
		}
	}
	failWith := func(code int, err error) int {
		j.Error = err.Error()
		j.Advance(upgrade.JournalFailed, err.Error())
		save()
		res.State = j.State
		return res.fail(code, err)
	}

	// Forward-only again: a --resume that already got as far as `swapped`
	// or `drain` must not repeat them, since repeating a state is a 409.
	rank := -1
	if s, err := fetchRestartStatus(port); err == nil {
		rank = database.RestartStateRank(s.Restart.State)
	}

	if rank < database.RestartStateRank(database.RestartStateSwapped) {
		if _, err := opsCallTimeout(port, http.MethodPost, "/ops/restart/swapped", nil, httpx.DefaultRequestTimeout); err != nil {
			return failWith(ExitRestartRefused, err)
		}
		j.Advance(upgrade.JournalSwapped, "server told the binary changed")
		save()
	}

	// Drain unless told not to. An upgrade changes worker code, which is
	// exactly the case brief decision row 3 says to drain for.
	drainMode := viper.GetString("restart.drainMode")
	switch {
	case rank >= database.RestartStateRank(database.RestartStateDraining):
		// Already drained by the attempt this one is resuming.
	case drainMode == database.DrainModeNever:
		res.Warnings = append(res.Warnings, "--drain-mode never: workers keep running the old binary until they are restarted")
	default:
		j.DrainRequested = true
		budget := viper.GetDuration("restart.drainTimeout")
		if budget <= 0 {
			budget = 60 * time.Second
		}
		db, err := opsCallTimeout(port, http.MethodPost, "/ops/restart/drain", nil, timing.Scale(budget)+30*time.Second)
		if err != nil {
			return failWith(ExitRestartRefused, err)
		}
		var drain struct {
			Drained bool     `json:"drained"`
			Running []string `json:"running"`
			Stopped int      `json:"stopped"`
		}
		_ = json.Unmarshal(db, &drain)
		if !drain.Drained {
			// Information, not failure -- the server's own view (see
			// opsRestartDrain). A worker three hours into a render is a
			// reason for a human to reconsider, not for the CLI to decide.
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("the drain timed out with %d worker(s) still running; they carry respawn intent and will be brought back", len(drain.Running)))
		}
		j.Advance(upgrade.JournalDrained, fmt.Sprintf("drained=%v", drain.Drained))
		save()
	}

	// exec: the response is written before the shutdown starts, so a 202
	// here means the restart began rather than that the connection held.
	eb, err := opsCallTimeout(port, http.MethodPost, "/ops/restart/exec", nil, httpx.DefaultRequestTimeout)
	if err != nil {
		return failWith(ExitRestartRefused, err)
	}
	var execResp struct {
		ExecMode string `json:"execMode"`
		ExitCode int    `json:"exitCode"`
		Pid      int    `json:"pid"`
	}
	_ = json.Unmarshal(eb, &execResp)
	j.Advance(upgrade.JournalExeced, "execMode="+execResp.ExecMode)
	save()

	return verifyRestart(res, j, port, execResp.ExecMode)
}

// verifyRestart waits for a replacement server and checks it is one.
func verifyRestart(res *upgradeResult, j *upgrade.Journal, port int, execMode string) int {
	jp := res.JournalPath
	save := func() { _ = j.Save(jp) }
	budget := timing.Scale(verifyBudget)

	// Under `exit` on an unsupervised box, nothing is going to start the
	// replacement -- that is the whole meaning of the mode. Windows is
	// always here (brief decision row 9: it never self-restarts, because a
	// detached replacement escapes a service's job object and then holds
	// the database lock somewhere `sc stop` cannot reach).
	if execMode == "exit" && !j.Supervised {
		// The port has to be quiet, and *stay* quiet, before the CLI adds
		// a process of its own. A server re-execing in place stops
		// answering for a moment, and the first refused connection looks
		// exactly like one that has exited for good -- but starting a
		// second server against the former leaves it parked on the
		// database lock, from where it wins the port at the next restart
		// and answers as the version that was supposed to have gone. That
		// is turtlemonvh/blanket#87's intermittent
		// "a server came back but reports <old version>".
		gone := waitForServerGone(port, timing.Scale(30*time.Second)) &&
			portStaysQuiet(port, timing.Scale(quietWindow))
		if !gone {
			// Something is serving this port. Whatever it is, a second
			// server is not the answer; the verification below decides
			// whether it is the replacement.
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("a server was still answering on port %d, so the CLI did not start another one", port))
		} else if runtime.GOOS == "windows" && isWindowsService() {
			res.ExitCode = ExitStagedOnly
			res.State = j.State
			res.Message = fmt.Sprintf("Installed %s at %s and stopped the old server.\nStart it again with:\n\n    sc start blanket\n",
				j.ToVersion, j.BinaryPath)
			return res.emit()
		} else {
			pid, logPath, err := startServerDetached(j.BinaryPath)
			if err != nil {
				j.Error = err.Error()
				j.Advance(upgrade.JournalFailed, err.Error())
				save()
				res.State = j.State
				return res.fail(ExitRestartRefused, fmt.Errorf(
					"the new binary is installed but the server could not be started: %w (start it yourself with `%s`)", err, j.BinaryPath))
			}
			res.Warnings = append(res.Warnings, fmt.Sprintf("started the replacement server (pid %d); its output is in %s", pid, logPath))
		}
	}

	// A verification with nothing to compare against is not one. If the
	// server was between processes when this attempt read its status,
	// FromInstanceId is empty and "a different process answered" cannot be
	// checked -- say so, so the operator knows the version is the only
	// evidence there is.
	if j.FromInstanceId == "" {
		res.Warnings = append(res.Warnings,
			"the instance id of the server being replaced was never read, so this checks the version that answered but not that it is a different process")
	}

	st, err := waitForNewServer(port, j.FromInstanceId, j.ToVersion, budget)
	if err != nil {
		// A server on the wrong version is a different story from a port
		// nothing answered on, and gets the version-mismatch wording:
		// what is running is not what was installed, which is a question
		// about PATH and stray processes, not about a server that never
		// came up.
		var wrong *wrongVersionError
		if errors.As(err, &wrong) {
			return failVerification(res, j, save, wrong.Got)
		}
		j.Error = err.Error()
		j.Advance(upgrade.JournalFailed, err.Error())
		save()
		res.State = j.State
		return res.fail(ExitVerificationFailed, fmt.Errorf(
			"%w\nThe new binary IS installed at %s. Start the server by hand, or run `blanket rollback --yes` to put %s back",
			err, j.BinaryPath, displayVersionOr(j.FromVersion)))
	}

	res.InstanceId = st.InstanceId
	j.ToInstanceId = st.InstanceId

	// waitForNewServer already refused anything on the wrong version, so
	// this is belt and braces rather than the check itself -- kept because
	// it is the one place j.ToVersion and the banner are compared for the
	// record, and because a future caller passing no wanted version would
	// otherwise have no check at all.
	//
	// The server reports a banner ("blanket v0.5.0 (built ...)"), so the
	// check is on the tag inside it rather than on string equality.
	got := upgrade.VersionFromBanner(st.Version)
	if got != "" && !upgrade.SameVersion(got, j.ToVersion) {
		return failVerification(res, j, save, got)
	}

	j.Advance(upgrade.JournalVerified, "instanceId="+st.InstanceId)
	save()
	res.State = upgrade.JournalVerified
	res.ExitCode = ExitOK
	verb := "Upgraded to"
	if res.Action == "rollback" {
		verb = "Rolled back to"
	}
	res.Message = fmt.Sprintf("%s %s.\n  binary:     %s\n  previous:   %s\n  backup:     %s\n  instanceId: %s (was %s)",
		verb, displayVersionOr(j.ToVersion), j.BinaryPath, orDash(j.SlotPath), orDash(j.BackupPath), st.InstanceId, orDash(j.FromInstanceId))
	return res.emit()
}

// failVerification records "a server answered, on the wrong version" and
// returns the exit code for it. Shared by the two places that reach that
// conclusion -- the wait giving up with only an impostor answering, and
// the final check on the server it did accept.
func failVerification(res *upgradeResult, j *upgrade.Journal, save func(), got string) int {
	j.Error = fmt.Sprintf("expected %s, the server reports %s", j.ToVersion, got)
	j.Advance(upgrade.JournalFailed, j.Error)
	save()
	res.State = j.State
	return res.fail(ExitVerificationFailed, fmt.Errorf(
		"a server came back but reports %s, not %s. Check what is on PATH; `blanket rollback --yes` puts the previous binary back", got, j.ToVersion))
}

// isWindowsService reports whether this process looks like it is running
// under the Windows service control manager, in which case the operator
// (or the SCM's recovery policy) restarts blanket rather than the CLI.
func isWindowsService() bool { return os.Getenv("BLANKET_WINDOWS_SERVICE") == "1" }

func orDash(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func displayRawVersion() string { return displayVersionOr(RawVersion) }

func displayVersionOr(v string) string {
	if strings.TrimSpace(v) == "" {
		return "a development build"
	}
	return v
}

// ---------------------------------------------------------------------------
// --resume and --abort
// ---------------------------------------------------------------------------

func runUpgradeResume(res *upgradeResult, installed string) int {
	j, err := upgrade.LoadJournal(res.JournalPath)
	if err != nil {
		return res.fail(ExitError, err)
	}
	if j == nil {
		return res.fail(ExitNothingToDo, fmt.Errorf("no upgrade journal at %s; there is nothing to resume", res.JournalPath))
	}
	res.ToVersion, res.FromVersion, res.State = j.ToVersion, j.FromVersion, j.State
	if j.State == upgrade.JournalVerified {
		res.ExitCode = ExitNothingToDo
		res.Message = fmt.Sprintf("The last upgrade (%s -> %s) completed; nothing to resume.", displayVersionOr(j.FromVersion), j.ToVersion)
		return res.emit()
	}
	if !upgradeConf.Yes {
		return res.fail(ExitUsage, fmt.Errorf("resuming from %s replaces %s and restarts the server. Re-run with --yes", j.State, installed))
	}

	switch j.State {
	case upgrade.JournalPlanned:
		return res.fail(ExitUsage, errors.New(
			"the interrupted attempt never staged a verified binary; a partial download cannot be resumed. Run `blanket upgrade --yes` again"))
	case upgrade.JournalStaged, upgrade.JournalFailed:
		if j.StagedPath == "" {
			return res.fail(ExitUsage, errors.New("the journal records no staged binary; run `blanket upgrade --yes` again"))
		}
		if _, err := os.Stat(j.StagedPath); err != nil {
			return res.fail(ExitUsage, fmt.Errorf("the staged binary %s is gone; run `blanket upgrade --yes` again", j.StagedPath))
		}
		// Re-verify before use. The file has been sitting on disk since
		// some earlier process wrote it, and "it was correct when we
		// staged it" is a claim about a different moment.
		if err := upgrade.VerifyFileDigest(j.StagedPath, j.AssetName, j.SHA256); err != nil {
			os.Remove(j.StagedPath)
			return res.fail(ExitVerificationFailed, err)
		}
		res.StagedPath = j.StagedPath
		return finishUpgrade(res, j, j.StagedPath)
	case upgrade.JournalBackedUp, upgrade.JournalPaused:
		// The binary was never swapped; the staged file is the way back in.
		if j.StagedPath != "" {
			return finishUpgrade(res, j, j.StagedPath)
		}
		return res.fail(ExitUsage, errors.New("nothing staged to resume with; run `blanket upgrade --abort` then try again"))
	case upgrade.JournalSwapped, upgrade.JournalDrained, upgrade.JournalExeced:
		// The new binary is already installed; all that is left is to get
		// a server onto it.
		port := j.Port
		if port == 0 {
			port = viper.GetInt("port")
		}
		// The exec mode matters here and the journal does not record it,
		// so ask the server that is coming back. Assuming `exit` -- as
		// this did -- means the CLI starts a replacement of its own
		// against a server that re-execs in place, and that second
		// process then sits on the database lock waiting for a port it
		// must never get (see verifyRestart).
		st, err := awaitRestartStatus(port, timing.Scale(statusProbeBudget))
		if err == nil {
			if st.Restart.State != "" && st.Restart.State != "IDLE" {
				return execAndVerify(res, j, port)
			}
			if j.FromInstanceId == "" {
				j.FromInstanceId = st.InstanceId
			}
			return verifyRestart(res, j, port, st.ResolvedExecMode)
		}
		// Nothing answered for the whole probe: the server really is
		// down, and `exit` is the mode that says the CLI starts it.
		return verifyRestart(res, j, port, "exit")
	}
	return res.fail(ExitUsage, fmt.Errorf("cannot resume from state %q", j.State))
}

func runUpgradeAbort(res *upgradeResult) int {
	j, err := upgrade.LoadJournal(res.JournalPath)
	if err != nil {
		return res.fail(ExitError, err)
	}
	port := viper.GetInt("port")
	if j != nil && j.Port != 0 {
		port = j.Port
	}

	// Always try to clear the server side, journal or no journal: a
	// paused server is the failure mode that outlives everything else,
	// and it is the one an operator typing `--abort` most needs undone.
	if _, err := opsCallTimeout(port, http.MethodPost, "/ops/restart/abort?reason=blanket+upgrade+--abort", nil, httpx.DefaultRequestTimeout); err != nil {
		var oe *opsError
		if errors.Is(err, errServerDown) {
			res.Warnings = append(res.Warnings, "no server is running; nothing to un-pause")
		} else if errors.As(err, &oe) && oe.Status == http.StatusConflict {
			res.Warnings = append(res.Warnings, "the server had no restart in flight")
		} else {
			res.Warnings = append(res.Warnings, fmt.Sprintf("could not abort the server-side restart: %v", err))
		}
	}

	if j == nil {
		res.ExitCode = ExitNothingToDo
		res.Message = "No upgrade journal; nothing of the CLI's to unwind."
		return res.emit()
	}
	res.ToVersion, res.FromVersion = j.ToVersion, j.FromVersion

	if j.StagedPath != "" {
		if err := os.Remove(j.StagedPath); err == nil {
			res.Warnings = append(res.Warnings, "removed the staged binary "+j.StagedPath)
		}
		j.StagedPath = ""
	}
	upgrade.CleanStaging(j.BinaryPath, "")

	if upgrade.JournalRank(j.State) >= upgrade.JournalRank(upgrade.JournalSwapped) {
		// The binary is already the new one. Undoing that is a rollback,
		// which is a separate, deliberate act with its own confirmation --
		// silently reverting a binary out from under a server that is
		// already running it would be the worst kind of helpful.
		res.ExitCode = ExitOK
		j.Advance(upgrade.JournalAborted, "binary was already swapped; left in place")
		_ = j.Save(res.JournalPath)
		res.State = j.State
		res.Message = fmt.Sprintf("Aborted. The new binary (%s) was already installed at %s and has been LEFT there.\n"+
			"Run `blanket rollback --yes` to put %s back.", j.ToVersion, j.BinaryPath, displayVersionOr(j.FromVersion))
		return res.emit()
	}

	j.Advance(upgrade.JournalAborted, "nothing was installed")
	_ = j.Save(res.JournalPath)
	res.State = j.State
	res.ExitCode = ExitOK
	res.Message = "Aborted; nothing was installed."
	return res.emit()
}

// ---------------------------------------------------------------------------
// --print-plan
// ---------------------------------------------------------------------------

// printPlan emits the manual sequence from docs/upgrade.md ("Restarting by
// hand") with this install's real values substituted in.
//
// It is the same seven steps, in the same order, because it is the same
// sequence: the command runs it over HTTP instead of via curl. Printing
// something that only resembled what the command does would make this a
// documentation generator rather than a plan.
func printPlan(installed string, src *upgradeSource) string {
	port := viper.GetInt("port")
	var b strings.Builder
	fmt.Fprintf(&b, "# blanket upgrade %s -> %s\n", displayRawVersion(), src.Version)
	fmt.Fprintf(&b, "# The CLI runs exactly these steps over HTTP. Journal: %s\n\n", upgradeJournalPath())
	fmt.Fprintf(&b, "BASE=http://localhost:%d\n", port)
	fmt.Fprintf(&b, "OPS=(-H 'X-Blanket-Restart: 1')\n\n")

	fmt.Fprintf(&b, "# 0. Verify and stage the new binary beside the installed one.\n")
	if src.Kind == "bundle" {
		fmt.Fprintf(&b, "#    source: %s (%s)\n", src.BundlePath, src.AssetName)
	} else {
		fmt.Fprintf(&b, "#    source: %s\n", orDash(src.DownloadURL))
		fmt.Fprintf(&b, "#    checksums: %s from the %s release\n", upgrade.SumsAssetName, src.Version)
	}
	fmt.Fprintf(&b, "#    staged as: %s%s* in %s\n\n", upgrade.StagePrefix, "XXXXXX", filepath.Dir(installed))

	fmt.Fprintf(&b, "# 1. Where are we? (IDLE, unless a previous attempt is still open.)\ncurl -sS \"${OPS[@]}\" $BASE/ops/restart/status\n\n")
	fmt.Fprintf(&b, "# 2. Announce it. Nothing is paused or stopped yet.\ncurl -sS \"${OPS[@]}\" -H 'Content-Type: application/json' \\\n     -d '{\"reason\": \"upgrade to %s\"}' \\\n     -X POST $BASE/ops/restart/begin\n\n", src.Version)
	fmt.Fprintf(&b, "# 3. Back up, while the server is still serving (-> BACKED_UP).\ncurl -sS \"${OPS[@]}\" -X POST $BASE/ops/backup\n\n")
	fmt.Fprintf(&b, "# 4. Stop the server spawning workers from a binary about to be replaced.\ncurl -sS \"${OPS[@]}\" -X POST $BASE/ops/restart/pause\n\n")
	fmt.Fprintf(&b, "# 5. Keep the old binary, then swap. (The CLI does this itself.)\n")
	fmt.Fprintf(&b, "#    rollback slot: %s/<timestamp>-%s/\n", upgradeSlotsDir(), strings.TrimSpace(displayRawVersion()))
	fmt.Fprintf(&b, "install -m 0755 <staged> %s\ncurl -sS \"${OPS[@]}\" -X POST $BASE/ops/restart/swapped\n\n", installed)
	if viper.GetString("restart.drainMode") == "never" {
		fmt.Fprintf(&b, "# 6. Drain: SKIPPED (--drain-mode never).\n\n")
	} else {
		fmt.Fprintf(&b, "# 6. Drain: an upgrade changes worker code, so the fleet is stopped\n#    with respawn intent. Waits up to restart.drainTimeout (%s).\ncurl -sS \"${OPS[@]}\" -X POST $BASE/ops/restart/drain\n\n", viper.GetDuration("restart.drainTimeout"))
	}
	fmt.Fprintf(&b, "# 7. Go. 202, then the server re-execs in place or exits 75 for its\n#    supervisor -- see --exec-mode (configured: %s).\ncurl -sS \"${OPS[@]}\" -X POST $BASE/ops/restart/exec\n\n", viper.GetString("restart.execMode"))
	fmt.Fprintf(&b, "# 8. Verify. A different instanceId means a different process came back.\nuntil curl -fsS $BASE/version >/dev/null 2>&1; do sleep 0.5; done\ncurl -sS \"${OPS[@]}\" $BASE/ops/restart/status   # -> IDLE\ncurl -sS $BASE/config/ | grep instanceId\n\n")
	fmt.Fprintf(&b, "# Changed your mind before step 7:\n# curl -sS \"${OPS[@]}\" -X POST $BASE/ops/restart/abort\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Where the bytes come from
// ---------------------------------------------------------------------------

// upgradeSource is "a version and a way to get its binary", so the online
// and offline paths differ in one place instead of everywhere.
type upgradeSource struct {
	Kind        string // "github" | "bundle"
	Version     string
	AssetName   string
	DownloadURL string
	BundlePath  string
	LocalPath   string
	Sums        upgrade.Sums

	bundle *upgrade.Bundle
}

func (s *upgradeSource) Close() error {
	if s == nil {
		return nil
	}
	return s.bundle.Close()
}

// Stage puts the source's binary next to targetPath, verified.
func (s *upgradeSource) Stage(targetPath string) (*upgrade.StagedFile, error) {
	if s.LocalPath != "" {
		return upgrade.StageFromFile(targetPath, s.AssetName, s.LocalPath, s.Sums)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	return upgrade.StageFromURL(ctx, httpx.Client(), s.DownloadURL, targetPath, s.AssetName, s.Sums)
}

func releasesClient() *upgrade.Client {
	base := upgradeConf.BaseURL
	if base == "" {
		base = viper.GetString("upgrade.releasesBaseURL")
	}
	return &upgrade.Client{
		BaseURL: base,
		Repo:    viper.GetString("upgrade.repo"),
		HTTP:    httpx.Client(),
	}
}

func resolveSource(targetVersion string) (*upgradeSource, error) {
	asset := upgrade.LocalAssetName()

	if upgradeConf.Bundle != "" {
		b, err := upgrade.OpenBundle(upgradeConf.Bundle)
		if err != nil {
			return nil, err
		}
		local := b.BinaryPath(asset)
		if _, err := os.Stat(local); err != nil {
			b.Close()
			return nil, fmt.Errorf("the bundle %s carries no %s (it has: %s)", upgradeConf.Bundle, asset, bundleBinaryNames(b))
		}
		return &upgradeSource{
			Kind:       "bundle",
			Version:    b.Manifest.Version,
			AssetName:  asset,
			BundlePath: upgradeConf.Bundle,
			LocalPath:  local,
			Sums:       b.Sums,
			bundle:     b,
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := releasesClient()
	var rel *upgrade.Release
	var err error
	if targetVersion == "" {
		rel, err = c.Latest(ctx)
	} else {
		rel, err = c.Tag(ctx, targetVersion)
	}
	if err != nil {
		return nil, err
	}

	sums, err := c.Sums(ctx, rel)
	if err != nil {
		if errors.Is(err, upgrade.ErrNoChecksums) {
			return nil, fmt.Errorf(
				"%s publishes no %s, so its binaries cannot be verified and will not be installed.\n"+
					"Releases cut before this feature existed are in that category permanently. Download the\n"+
					"assets by hand, or use `blanket upgrade --bundle <bundle.tar.gz>` with a bundle that carries its own checksums",
				rel.TagName, upgrade.SumsAssetName)
		}
		return nil, err
	}

	a, ok := rel.Asset(asset)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s asset (this is %s/%s)", rel.TagName, asset, runtime.GOOS, runtime.GOARCH)
	}
	return &upgradeSource{
		Kind:        "github",
		Version:     rel.TagName,
		AssetName:   asset,
		DownloadURL: a.URL,
		Sums:        sums,
	}, nil
}

func bundleBinaryNames(b *upgrade.Bundle) string {
	names := make([]string, 0, len(b.Manifest.Binaries))
	for _, bin := range b.Manifest.Binaries {
		names = append(names, bin.Name)
	}
	if len(names) == 0 {
		return "nothing"
	}
	return strings.Join(names, ", ")
}
