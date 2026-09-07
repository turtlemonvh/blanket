package command

import (
	"errors"
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	bolt "github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/server"
)

var serverLongDesc string = `A fast and easy way to wrap applications and make them available via nice clean REST interfaces with built in UI, command line tools, and queuing, all in a single binary!`
var RootCmd = &cobra.Command{
	Use:   "blanket",
	Short: "Blanket is a RESTy wrapper for other programs",
	Long:  serverLongDesc,
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		InitializeLogging()

		// Connect to database. MustOpenBoltDatabase only takes the file
		// lock; everything about the database's *contents* -- the schema
		// version, an in-flight migration marker, the mandatory
		// pre-migration backup -- is settled below, because those checks
		// can legitimately say "do not start" and a log.Fatal inside the
		// open helper could not distinguish their exit semantics.
		db := bolt.MustOpenBoltDatabase()
		// Belt and braces: the server closes this itself as the last step
		// of its shutdown sequence (ServerConfig.Cleanup below), which is
		// what releases the bolt lock before a SIGUSR2 re-exec. bbolt's
		// Close is a no-op on an already-closed handle, so this defer only
		// covers paths that never reach the server at all.
		defer db.Close()

		// Schema check + auto-migrate (turtlemonvh/blanket#23 phase 4).
		// Three of the failure shapes are not "the database is broken"
		// but "a human has to act", and each has its own exit story:
		//
		//   *ErrMigrationInProgress  a newer binary is mid-migration.
		//                            Exit non-zero and touch nothing --
		//                            never retry, or two binaries race
		//                            for the lock forever.
		//   *ErrSchemaTooNew         the database is from a newer
		//                            blanket. Nothing to do but say so.
		//   *ErrMigrationIncomplete  a migration died mid-flight; the
		//                            message carries the exact restore
		//                            command.
		//
		// All three are fatal here. What they have in common is that
		// starting anyway would be worse than not starting.
		DB, err := bolt.OpenBlanketBoltDB(db, prepareOptionsFromConfig())
		if err != nil {
			var inProgress *bolt.ErrMigrationInProgress
			if errors.As(err, &inProgress) {
				log.Warn(err.Error())
				db.Close()
				os.Exit(1)
			}
			log.Fatal(err.Error())
		}

		// DB and Q initializers are fatal if they don't succeed
		// Serve gracefully

		c := server.ServerConfig{
			DB:                    DB,
			Q:                     bolt.NewBlanketBoltQueue(db),
			Port:                  viper.GetInt("port"),
			ResultsPath:           viper.GetString("tasks.resultsPath"),
			TimeMultiplier:        viper.GetFloat64("timeMultiplier"),
			Version:               Version,
			SchedulerInterval:     viper.GetDuration("scheduler.interval"),
			SchedulerMaxScheduled: viper.GetInt("scheduler.maxScheduled"),
			// The reaper is on by default (see command/root.go); this is
			// the one place the config key is read, so a hand-built
			// ServerConfig in a test never acquires the loop implicitly.
			ReaperEnabled:          viper.GetBool("reaper.enabled"),
			ReaperInterval:         viper.GetDuration("reaper.interval"),
			ReaperWorkerStaleAfter: viper.GetDuration("reaper.workerStaleAfter"),
			ReaperWorkerDeadAfter:  viper.GetDuration("reaper.workerDeadAfter"),
			ReaperTaskStaleAfter:   viper.GetDuration("reaper.taskStaleAfter"),
			ReaperMaxRequeues:      viper.GetInt("reaper.maxRequeues"),
			BackupDir:              viper.GetString("storage.backupDir"),
			// The restart state machine (turtlemonvh/blanket#23 phase 5).
			// Read here, like every other config key, so a hand-built
			// ServerConfig in a test gets the documented defaults from
			// server/restart.go rather than viper's global state.
			ExecMode:        viper.GetString("restart.execMode"),
			DrainMode:       viper.GetString("restart.drainMode"),
			DrainTimeout:    viper.GetDuration("restart.drainTimeout"),
			RestartDeadline: viper.GetDuration("restart.deadline"),
			Cleanup: func() {
				if err := db.Close(); err != nil {
					log.WithField("err", err).Warn("error closing database at shutdown")
				}
			},
		}

		// Blocks until SIGINT/SIGTERM (drain, then exit leaving no restart
		// intent), SIGUSR2 on unix (drain, then re-exec in place -- never
		// returns), or POST /ops/restart/exec. See server/lifecycle.go.
		if err := c.Serve().ListenAndServe(); err != nil {
			// A requested restart in --exec-mode=exit asks for a specific
			// exit code, and it must not be 0: the systemd unit blanket
			// installs says Restart=on-failure, so a clean exit is exactly
			// what would leave the server down. See
			// server.RestartExitCode.
			var exitErr *server.ExitCodeError
			if errors.As(err, &exitErr) {
				log.Warn(exitErr.Msg)
				db.Close()
				os.Exit(exitErr.Code)
			}
			log.WithField("err", err).Fatal("server exited with an error")
		}
	},
}
