package command

/*

Imports the commands folder
Directs to relevant command line option

*/

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/queue"
	"github.com/turtlemonvh/blanket/server"
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
	viper.SetDefault("tasks.resultsPath", []string{"results"})
	viper.SetDefault("workers.logfileNameTemplate", "worker.{{.Id.Hex}}.log")

	// Time multiplier can be used in tests to speed up tests
	viper.SetDefault("timeMultipler", "1.0")

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

var RootCmd = &cobra.Command{
	Use:   "blanket",
	Short: "Blanket is a RESTy wrapper for other programs",
	Long: `A fast and easy way to wrap applications and make 
           them available via nice clean REST interfaces with 
           built in UI, command line tools, and queuing, all 
           in a single binary!`,
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		InitializeLogging()

		// Connect to database
		boltdb := database.OpenBoltDatabase()
		defer boltdb.Close()

		// DB and Q initializers are fatal if they don't succeed
		// Serve gracefully
		s := server.Serve(database.NewBlanketBoltDB(boltdb), queue.NewBlanketBoltQueue(boltdb))
		s.ListenAndServe()
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of blanket",
	Long:  `All software has versions. This is blanket's`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("blanket v0.1")
	},
}
