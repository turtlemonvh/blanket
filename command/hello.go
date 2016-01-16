package command

import (
	"fmt"
	"github.com/spf13/cobra"
)

func init() {
	RootCmd.AddCommand(helloCmd)
}

var helloCmd = &cobra.Command{
	Use:   "hello",
	Short: "Say Hi",
	Run: func(cmd *cobra.Command, args []string) {
		Hello()
	},
}

func Hello() {
	fmt.Println("Hello world")
}
