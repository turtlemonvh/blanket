package command

/*

`blanket migrate` (turtlemonvh/blanket#23 phase 4).

Three modes, one implementation each of which is shared with the server:

	blanket migrate --check              report, change nothing, exit 0/1
	blanket migrate                      back up, then migrate
	blanket migrate --restore <p> --yes  put a backup back

The middle one is the important design point. It does **not** contain a
migration loop of its own — it calls bolt.PrepareDatabase, which is the
exact function the server calls when it opens the database. A CLI with its
own copy of the sequence is a CLI that will one day disagree with the
server about what a database needs, and the disagreement will surface as
data loss rather than as a compile error.

## Why it needs the server stopped

bolt takes an exclusive flock for the life of an open handle. There is no
arrangement in which a second process migrates a database a server has
open. So `migrate` and `migrate --restore` both take the lock themselves
and report who has it if they cannot. `--check` takes a shared lock, and is
likewise blocked by a running server — deliberately; see
bolt.InspectDatabase.

## Why you rarely need to run it

Migrations auto-apply forward when the server opens the database (brief
decision row 4). This command exists for the operator who wants to see what
an upgrade will do before doing it, to migrate ahead of time in a
maintenance window, and — the case that matters — to recover when a
migration died mid-flight and the server is refusing to start.

*/

import (
	"fmt"
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	boltlib "github.com/turtlemonvh/blanket/lib/bolt"
)

var migrateConf struct {
	Check   bool
	Restore string
	Yes     bool
}

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Inspect, apply, or roll back database schema migrations",
	Long: `Inspect, apply, or roll back blanket's database schema migrations.

The server applies pending migrations itself when it starts, after taking a
mandatory backup, so this command is for looking before you leap and for
recovering afterwards.

  blanket migrate --check                  what would happen, and nothing else
  blanket migrate                          back up, then apply what's pending
  blanket migrate --restore PATH --yes     replace the database with a backup

All three need the server stopped: only one process can hold the database's
lock at a time.`,
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		InitializeLogging()

		switch {
		case migrateConf.Restore != "":
			os.Exit(runMigrateRestore())
		case migrateConf.Check:
			os.Exit(runMigrateCheck())
		default:
			os.Exit(runMigrate())
		}
	},
}

func init() {
	migrateCmd.Flags().BoolVar(&migrateConf.Check, "check", false, "Report the current and target schema versions and exit 1 if anything is pending")
	migrateCmd.Flags().StringVar(&migrateConf.Restore, "restore", "", "Replace the database with this backup file (requires --yes)")
	migrateCmd.Flags().BoolVar(&migrateConf.Yes, "yes", false, "Confirm a destructive action (--restore)")
	RootCmd.AddCommand(migrateCmd)
}

// runMigrateCheck implements --check. Exit 0 means "nothing to do", exit 1
// means "something is pending", exit 2 means "could not tell" — so a
// script can branch on it without parsing prose.
func runMigrateCheck() int {
	path := viper.GetString("database")
	insp, err := boltlib.InspectDatabase(path, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not read %s: %v\n", path, err)
		if who := boltlib.DescribeLockHolder(path); who != "" {
			fmt.Fprintf(os.Stderr, "hint: %s. Stop blanket, or ask the running server with `curl localhost:%d/config/`.\n", who, viper.GetInt("port"))
		}
		return 2
	}

	fmt.Printf("database:       %s\n", insp.Path)
	stamped := "yes"
	if insp.Unstamped {
		stamped = "no (an unstamped database is read as version 1 and stamped on first open)"
	}
	fmt.Printf("stamped:        %s\n", stamped)
	fmt.Printf("schema version: %d\n", insp.CurrentVersion)
	fmt.Printf("target version: %d\n", insp.TargetVersion)

	if insp.Marker != nil {
		fmt.Printf("\nA migration v%d -> v%d is marked as in progress.\n", insp.Marker.From, insp.Marker.To)
		if insp.CurrentVersion >= insp.Marker.To {
			fmt.Printf("It completed; the marker is stale and will be cleared on the next open.\n")
		} else {
			fmt.Printf("It did not complete. Restore the pre-migration backup before starting blanket:\n\n")
			fmt.Printf("    blanket migrate --restore %s --yes\n\n", insp.Marker.BackupPath)
			return 1
		}
	}

	if insp.CurrentVersion > insp.TargetVersion {
		fmt.Printf("\nThis database was written by a NEWER blanket than this one. Migrations are\n")
		fmt.Printf("forward-only, so this binary cannot open it. Install a newer blanket.\n")
		return 1
	}

	if len(insp.Pending) == 0 {
		fmt.Printf("\nUp to date; nothing pending.\n")
		return 0
	}

	fmt.Printf("\n%d pending migration(s):\n", len(insp.Pending))
	for _, m := range insp.Pending {
		fmt.Printf("  v%d  %s\n", m.Version, m.Name)
	}
	fmt.Printf("\nThe server applies these itself at startup, after backing up to %s.\n",
		boltlib.DefaultBackupDir(insp.Path))
	return 1
}

// runMigrate applies pending migrations by running the server's own
// open-time sequence.
func runMigrate() int {
	path := viper.GetString("database")

	before, err := boltlib.InspectDatabase(path, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not read %s: %v\n", path, err)
		if who := boltlib.DescribeLockHolder(path); who != "" {
			fmt.Fprintf(os.Stderr, "hint: %s. Stop blanket first.\n", who)
		}
		return 2
	}
	if before.UpToDate() {
		fmt.Printf("Database %s is already at schema version %d; nothing to do.\n", path, before.CurrentVersion)
		return 0
	}

	db := boltlib.MustOpenBoltDatabase()
	defer db.Close()

	if err := boltlib.PrepareDatabase(db, prepareOptionsFromConfig()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	after, err := boltlib.SchemaVersionOf(db)
	if err != nil {
		log.WithField("err", err).Warn("could not read the schema version back")
		return 0
	}
	fmt.Printf("Database %s is at schema version %d.\n", path, after)
	return 0
}

// runMigrateRestore implements --restore. The confirmation flag is
// mandatory rather than an interactive prompt: this runs in maintenance
// windows and from scripts, where a prompt is a hang.
func runMigrateRestore() int {
	if !migrateConf.Yes {
		fmt.Fprintf(os.Stderr, "error: --restore replaces %s with %s. Re-run with --yes to confirm.\n",
			viper.GetString("database"), migrateConf.Restore)
		return 1
	}

	res, err := boltlib.RestoreBackup(migrateConf.Restore, viper.GetString("database"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("Restored %s to %s (schema version %d).\n", res.BackupPath, res.DatabasePath, res.SchemaVersion)
	if res.DisplacedPath != "" {
		fmt.Printf("The database that was there is kept at %s; delete it once you're satisfied.\n", res.DisplacedPath)
	}
	return 0
}

// prepareOptionsFromConfig builds the open-time options every entry point
// shares, so the CLI and command/serve.go cannot configure backups
// differently.
func prepareOptionsFromConfig() *boltlib.PrepareOptions {
	return &boltlib.PrepareOptions{
		BackupDir: viper.GetString("database.backupDir"),
		Retention: viper.GetInt("database.backupRetention"),
	}
}
