package main

import (
	"github.com/turtlemonvh/blanket/command"
)

var (
	COMMIT     string
	BRANCH     string
	VERSION    string
	BUILD_DATE string
)

func main() {
	command.Run(VERSION, BRANCH, COMMIT, BUILD_DATE)
}
