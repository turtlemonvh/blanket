package command

/*

Imports the commands folder
Directs to relevant command line option

*/

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"os"
	"path/filepath"
	"runtime"
)

var blanketCmdV *cobra.Command
var (
	CfgFile  string
	LogLevel string
	Version  string
)

func Run(VERSION string, BRANCH string, COMMIT string, BUILD_DATE string) {
	if VERSION != "" {
		Version = fmt.Sprintf("blanket %s (built %s)", VERSION, BUILD_DATE)
	} else {
		Version = fmt.Sprintf("blanket (dev) branch=%s commit=%s (built %s)", BRANCH, COMMIT, BUILD_DATE)
	}
	RootCmd.Execute()
}

func init() {
	//cobra.OnInitialize(initConfig)
	RootCmd.PersistentFlags().Int32P("port", "p", 8773, "Port the server will run on")
	RootCmd.PersistentFlags().StringVar(&LogLevel, "logLevel", "info", "the logging level to use")
	RootCmd.PersistentFlags().StringVarP(&CfgFile, "config", "c", "", "config file (default is config.json|yaml|toml in the blanket config dir)")
	RootCmd.AddCommand(versionCmd)
	RootCmd.AddCommand(taskValidateCmd)
	blanketCmdV = RootCmd

	// FIXME: Add support for multiple outputs and handling log levels via command line or env variable
	// https://golang.org/src/io/multi.go?s=1355:1397#L47
	log.SetOutput(os.Stdout)
	log.SetLevel(log.WarnLevel)
}

func InitializeConfig() {
	// Add reloads for select config values
	// https://github.com/spf13/viper#watching-and-re-reading-config-files
	viper.SetDefault("port", 8773)
	viper.SetDefault("database", "blanket.db")
	viper.SetDefault("tasks.typesPaths", []string{"types"})
	// FIXME: Why is this a slice? It makes sending a target result dir to a client pretty tough.
	viper.SetDefault("tasks.resultsPath", []string{"results"})
	viper.SetDefault("workers.logfileNameTemplate", "worker.{{.Id.Hex}}.log")
	viper.SetDefault("mcp.enabled", true)
	viper.SetDefault("mcp.mode", "all")
	viper.SetDefault("mcp.writeTypesPath", "")
	viper.SetDefault("mcp.validateStrict", false)
	viper.SetDefault("mcp.maxLogLines", 200)

	// Synchronous ("blocking") task submission -- POST /task/?wait
	// (turtlemonvh/blanket#27). defaultWait applies to a bare ?wait;
	// maxWait is a hard cap (a larger ?wait is a 400, not a clamp) and is
	// the only control on how long an unauthenticated caller can hold a
	// connection and a goroutine open, so it is deliberately conservative.
	// maxLogLines bounds the stdout/stderr tails in the completion
	// payload; maxResultBytes bounds the declared result_file that gets
	// parsed into it.
	viper.SetDefault("tasks.sync.defaultWait", "30s")
	viper.SetDefault("tasks.sync.maxWait", "300s")
	viper.SetDefault("tasks.sync.maxLogLines", 200)
	viper.SetDefault("tasks.sync.maxResultBytes", 1048576)

	// How often the scheduler loop checks for due SCHEDULED tasks and
	// RECURRING task templates (turtlemonvh/blanket#61). Accepts anything
	// time.ParseDuration understands, e.g. "2s", "500ms".
	viper.SetDefault("scheduler.interval", "2s")

	// Upper bound on how many SCHEDULED+RECURRING+PAUSED tasks may be
	// live at once. POST /task/ returns 429 once a new notBefore-future or
	// cron submission would reach this many; it also bounds how many rows
	// a single scheduler tick will scan (server.DefaultSchedulerMaxScheduled's
	// doc comment explains why the same number serves both purposes).
	viper.SetDefault("scheduler.maxScheduled", 10000)

	// Time multiplier can be used in tests to speed up tests
	viper.SetDefault("timeMultiplier", "1.0")

	viper.SetConfigName("config")
	if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			viper.AddConfigPath(filepath.Join(localAppData, "blanket"))
		}
	} else {
		configHome := os.Getenv("XDG_CONFIG_HOME")
		if configHome == "" {
			if home, err := os.UserHomeDir(); err == nil {
				configHome = filepath.Join(home, ".config")
			}
		}
		if configHome != "" {
			viper.AddConfigPath(filepath.Join(configHome, "blanket"))
		}
	}
	viper.AddConfigPath("/etc/blanket/")
	viper.AddConfigPath("$HOME/.blanket")
	viper.AddConfigPath(".")
	viper.SetConfigFile(CfgFile)
	err := viper.ReadInConfig()
	if err != nil {
		log.WithFields(log.Fields{
			"error": err.Error(),
		}).Fatal("Please either add a config file in one of the predefined locations or pass in a path explicitly.")
	}

	// https://github.com/spf13/viper#working-with-environment-variables
	viper.SetEnvPrefix("BLANKET_APP_")
	viper.AutomaticEnv()

	viper.BindPFlag("port", blanketCmdV.PersistentFlags().Lookup("port"))
	viper.BindPFlag("logLevel", blanketCmdV.PersistentFlags().Lookup("logLevel"))
}

func InitializeLogging() {
	var level log.Level
	var err error
	level, err = log.ParseLevel(viper.GetString("logLevel"))
	if err != nil {
		log.WithFields(log.Fields{
			"levelChoice": viper.GetString("logLevel"),
		}).Error("invalid choice for option 'level'. Ignoring and continuing.")
	} else {
		log.SetLevel(level)
		log.WithFields(log.Fields{
			"level": level,
		}).Info("setting loglevel from config")
	}
}
