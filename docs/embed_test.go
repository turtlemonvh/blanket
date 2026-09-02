// docs/embed_test.go
package docs

import (
	"strings"
	"testing"
)

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
