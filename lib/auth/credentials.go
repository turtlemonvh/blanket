package auth

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// CredentialsFileName holds the local admin token `blanket auth init`
	// writes, beside the database (A.5). The CLI on the server's host
	// reads it, so local commands keep working once auth is on.
	CredentialsFileName = "credentials"
	// SetupCodeFileName holds the one-time setup code while auth is off.
	SetupCodeFileName = "setup-code"
	// TokenEnvVar is how a CLI on another host, or a script, passes a
	// token (A.5). It is read once at startup and then removed from the
	// process environment, so no child -- a worker, or a task a worker
	// runs -- inherits it.
	TokenEnvVar = "BLANKET_TOKEN"
)

// DataDir is the directory the credential and setup-code files live in:
// the one holding the database.
func DataDir(databasePath string) string {
	if abs, err := filepath.Abs(databasePath); err == nil {
		databasePath = abs
	}
	return filepath.Dir(databasePath)
}

// CredentialsPath is <dataDir>/credentials.
func CredentialsPath(dataDir string) string {
	return filepath.Join(dataDir, CredentialsFileName)
}

// SetupCodePath is <dataDir>/setup-code.
func SetupCodePath(dataDir string) string {
	return filepath.Join(dataDir, SetupCodeFileName)
}

// ReadSecretFile returns a credential or setup-code file's contents,
// trimmed. A missing file is ("", nil).
func ReadSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// TokenSource resolves the token a blanket client sends. Order: an
// explicit token (the --token flag, or BLANKET_TOKEN captured at
// startup), then the credentials file.
//
// The file is re-read whenever its modification time changes. That is
// what lets a worker started before `blanket auth init` keep working
// afterwards: its next request picks up the credential init just wrote,
// instead of failing with 401 until someone relaunches it.
type TokenSource struct {
	explicit string
	path     string

	mu    sync.Mutex
	mtime time.Time
	size  int64
	value string
}

// NewTokenSource returns a source that prefers explicit and otherwise
// reads credentialsPath. Either may be empty.
func NewTokenSource(explicit, credentialsPath string) *TokenSource {
	return &TokenSource{explicit: strings.TrimSpace(explicit), path: credentialsPath}
}

// Token returns the token to send, or "" for none.
func (s *TokenSource) Token() string {
	if s == nil {
		return ""
	}
	if s.explicit != "" {
		return s.explicit
	}
	if s.path == "" {
		return ""
	}
	fi, err := os.Stat(s.path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.value, s.mtime, s.size = "", time.Time{}, 0
		return ""
	}
	if fi.ModTime().Equal(s.mtime) && fi.Size() == s.size {
		return s.value
	}
	v, err := ReadSecretFile(s.path)
	if err != nil {
		return ""
	}
	s.value, s.mtime, s.size = v, fi.ModTime(), fi.Size()
	return s.value
}
