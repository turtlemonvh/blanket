package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/turtlemonvh/blanket/command"
	"github.com/turtlemonvh/blanket/lib/docs"
)

var (
	COMMIT     string
	BRANCH     string
	VERSION    string
	BUILD_DATE string
)

// docsFS embeds docs/*.md so the blanket_docs MCP tool (lib/docs) can serve
// them without that package needing to reach outside its own directory --
// go:embed can only see files at or below the package it's declared in, and
// the root package is the one place that sits next to docs/. See #66: this
// keeps docs/ itself markdown-only, no code mixed in.
//
//go:embed docs/*.md
var docsFS embed.FS

func main() {
	docsSub, err := fs.Sub(docsFS, "docs")
	if err != nil {
		log.Fatalf("could not load embedded docs: %v", err)
	}
	docs.SetFS(docsSub)

	command.Run(VERSION, BRANCH, COMMIT, BUILD_DATE)
}
