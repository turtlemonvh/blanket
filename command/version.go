package command

import (
	"fmt"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number of blanket",
	Long:  `All software has versions. This is blanket's`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(Version)
	},
}
