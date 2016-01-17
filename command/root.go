package command

/*

Imports the commands folder
Directs to relevant command line option

*/

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/server"
	"log"
)

var blanketCmdV *cobra.Command
var CfgFile string

func init() {
	//cobra.OnInitialize(initConfig)
	RootCmd.PersistentFlags().Int32P("port", "p", 8773, "Port the server will run on")
	RootCmd.PersistentFlags().StringVarP(&CfgFile, "config", "c", "", "config file (default is path/config.yaml|json|toml)")
	RootCmd.AddCommand(versionCmd)
	blanketCmdV = RootCmd
}

func InitializeConfig() {
	// Add reloads for select config values
	// https://github.com/spf13/viper#watching-and-re-reading-config-files
	viper.SetDefault("port", 8773)
	viper.SetDefault("database", "blanket.db")
	viper.SetConfigName("blanket")
	viper.AddConfigPath("/etc/blanket/")
	viper.AddConfigPath("$HOME/.blanket")
	viper.AddConfigPath(".")
	viper.SetConfigFile(CfgFile)
	err := viper.ReadInConfig()
	if err != nil {
		log.Printf("Please either add a config file in one of the predefined locations or pass in a path explicitly.")
		log.Fatalf("Fatal error config file: %s \n", err)
	}

	// https://github.com/spf13/viper#working-with-environment-variables
	viper.SetEnvPrefix("BLANKET_APP_")
	viper.AutomaticEnv()

	viper.BindPFlag("port", blanketCmdV.PersistentFlags().Lookup("port"))
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
		server.Serve()
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
