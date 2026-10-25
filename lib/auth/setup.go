package auth

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/turtlemonvh/blanket/lib/objectid"
)

// setupAlphabet has no 0/O or 1/I, so a code read off a log is typed back
// right. 32 symbols x 8 characters = 40 bits; the server also delays every
// wrong guess (server/serve_auth.go), so guessing online is hopeless.
const setupAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewSetupCode returns a fresh one-time setup code, formatted XXXX-XXXX.
func NewSetupCode() (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(setupAlphabet)))
	for i := 0; i < 8; i++ {
		if i == 4 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(setupAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// HashSetupCode is what the meta bucket stores for a setup code. Case,
// dashes and spaces are ignored, so "abcd efgh" matches "ABCD-EFGH".
func HashSetupCode(code string) string {
	norm := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	return HashToken("setup:" + norm)
}

// SetupStore is the meta-bucket state `auth init` needs, alongside Store.
type SetupStore interface {
	// SetSetupCodeHash records the pending setup code's hash; "" clears it.
	SetSetupCodeHash(hash string) error
	SetupCodeHash() (string, error)
	// SetAuthEnabledTs records when auth was switched on (0 when off).
	SetAuthEnabledTs(ts int64) error
}

// IssueToken generates a token for t, stores it, and returns the
// plaintext with the stored row. Shared by POST /token/, POST /auth/init
// and the offline `blanket auth init`, so every token is minted one way.
func IssueToken(store Store, t Token) (string, Token, error) {
	secret, err := NewTokenString()
	if err != nil {
		return "", Token{}, err
	}
	t.Id = objectid.NewObjectId()
	t.CreatedTs = time.Now().Unix()
	if err := store.CreateToken(t, HashToken(secret)); err != nil {
		return "", Token{}, err
	}
	return secret, t, nil
}

// InitAdminToken is the row `blanket auth init` creates. It never expires:
// expired tokens still count toward "auth is on", so an expiring admin
// token would eventually lock everyone out.
func InitAdminToken() Token {
	return Token{Name: "admin (blanket auth init)", Owner: "admin", Scopes: []Scope{ScopeAdmin}}
}

// WriteSecretFile writes content to path readable by the owner only. The
// file is written beside path and renamed into place, so an existing file
// with looser permissions is replaced rather than reused, and a reader
// never sees a half-written token.
//
// On Windows the mode only controls the read-only bit; the file inherits
// its ACL from the directory, which for a data directory under the user's
// profile is already private to that user.
func WriteSecretFile(path, content string) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op once renamed
	if err := f.Chmod(0600); err != nil && !isWindows() {
		f.Close()
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("could not move %s into place: %w", path, err)
	}
	return nil
}

// RemoveIfExists deletes path, ignoring its absence.
func RemoveIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
