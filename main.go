package main

import (
	"github.com/turtlemonvh/blanket/command"
)

var (
	COMMIT  string
	BRANCH  string
	VERSION string
)

func main() {
	command.Run(VERSION, BRANCH, COMMIT)
}
