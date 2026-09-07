package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func sha256Of(t *testing.T, s string) string {
	t.Helper()
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func writeFile(p, body string) error {
	return os.WriteFile(p, []byte(body), 0o644)
}
