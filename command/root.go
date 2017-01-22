package command

/*

Imports the commands folder
Directs to relevant command line option

*/

import (
	log "github.com/Sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"os"
)

var blanketCmdV *cobra.Command
var (
	CfgFile  string
	LogLevel string
)

func init() {
	//cobra.OnInitialize(initConfig)
	RootCmd.PersistentFlags().Int32P("port", "p", 8773, "Port the server will run on")
	RootCmd.PersistentFlags().StringVar(&LogLevel, "logLevel", "info", "the logging level to use")
	RootCmd.PersistentFlags().StringVarP(&CfgFile, "config", "c", "", "config file (default is blanket.yaml|json|toml)")
	RootCmd.AddCommand(versionCmd)
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
	viper.SetDefault("tasks.typesPath", "types")
	// FIXME: Why is this a slice? It makes sending a target result dir to a client pretty tough.
	viper.SetDefault("tasks.resultsPath", []string{"results"})
	viper.SetDefault("workers.logfileNameTemplate", "worker.{{.Id.Hex}}.log")

	// Time multiplier can be used in tests to speed up tests
	viper.SetDefault("timeMultiplier", "1.0")

	viper.SetConfigName("blanket")
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
