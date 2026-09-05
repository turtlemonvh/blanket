package docs

import (
	"os"
	"strings"
	"testing"
)

// docs/ lives two directories up from lib/docs/ (lib/docs -> lib -> repo
// root). Real deployments get their FS from main's go:embed instead.
func TestMain(m *testing.M) {
	SetFS(os.DirFS("../../docs"))
	os.Exit(m.Run())
}

func TestPage_KnownKeys(t *testing.T) {
	for _, key := range Keys() {
		content, err := Page(key)
		if err != nil {
			t.Errorf("Page(%q) returned error: %v", key, err)
		}
		if strings.TrimSpace(content) == "" {
			t.Errorf("Page(%q) returned empty content", key)
		}
	}
}

func TestPage_UnknownKey(t *testing.T) {
	_, err := Page("does-not-exist")
	if err == nil {
		t.Fatal("expected an error for an unknown page key")
	}
	if !strings.Contains(err.Error(), "overview") {
		t.Errorf("expected error to list valid keys (e.g. 'overview'), got: %v", err)
	}
}

func TestPage_NotInitialized(t *testing.T) {
	SetFS(nil)
	defer SetFS(os.DirFS("../../docs"))

	_, err := Page("overview")
	if err == nil {
		t.Fatal("expected an error when the docs filesystem hasn't been set")
	}
	if !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("expected a clear 'not initialized' error, got: %v", err)
	}
}
