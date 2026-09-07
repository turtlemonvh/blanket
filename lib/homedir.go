package lib

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome replaces a leading "~" or "~/" in p with the current user's
// home directory, the way a shell would. Any other path (including
// "~user/...") is returned unchanged. It stands in for the archived
// github.com/mitchellh/go-homedir's Expand (turtlemonvh/blanket#141),
// which is the only thing blanket ever used from that module.
func ExpandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}
