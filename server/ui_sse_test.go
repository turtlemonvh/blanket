// Tests for the /ui/sse/* event streams and the client-side lifecycle
// script that closes them.
//
// Background (issue #103): browsers keep navigated-away pages -- and their
// EventSource connections -- alive in the back/forward cache, so a few tab
// switches exhausted the six-connections-per-host limit and the freshly
// loaded page hung. The client half of the fix is
// server/ui/static/sse-lifecycle.js, loaded from _layout.html; the server
// half is sseStream noticing a gone client immediately instead of after a
// full keepalive interval.
//
// These need a real listener rather than httptest.NewRecorder: gin's
// c.Stream calls CloseNotify() on the ResponseWriter, which a recorder
// doesn't implement.

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// waitFor polls cond until it's true or the deadline passes. Returns how long
// it took, and whether it ever became true.
func waitFor(timeout time.Duration, cond func() bool) (time.Duration, bool) {
	start := time.Now()
	deadline := start.Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return time.Since(start), true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return time.Since(start), cond()
}

// A client that goes away (tab closed, EventSource.close(), bfcache
// eviction) must free its stream promptly. Before the fix the handler sat
// inside its 30s keepalive wait, holding a goroutine and a CLOSE-WAIT
// socket; this asserts it lets go well inside that window.
func TestUI_SSEStream_ReleasesSubscriberOnClientDisconnect(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	srv := httptest.NewServer(s.GetRouter())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse/tasks", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	// The handler sends one event straight away so the page catches up
	// without waiting for the first change.
	initial := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		n, rerr := resp.Body.Read(buf)
		if rerr != nil && n == 0 {
			initial <- ""
			return
		}
		initial <- string(buf[:n])
	}()

	select {
	case got := <-initial:
		assert.Contains(t, got, "tasks-changed")
		assert.Contains(t, got, "refresh")
	case <-time.After(5 * time.Second):
		t.Fatal("no initial SSE event within 5s")
	}

	if n, ok := waitFor(2*time.Second, func() bool { return s.TaskEvents.SubscriberCount() == 1 }); !ok {
		t.Fatalf("stream never registered a subscriber (waited %s, count=%d)", n, s.TaskEvents.SubscriberCount())
	}

	// Drop the client the way a browser does when it closes an EventSource.
	cancel()
	resp.Body.Close()

	took, ok := waitFor(2*time.Second, func() bool { return s.TaskEvents.SubscriberCount() == 0 })
	if !ok {
		t.Fatalf("stream still subscribed %s after the client disconnected (count=%d); "+
			"sseStream is not watching the request context", took, s.TaskEvents.SubscriberCount())
	}
	t.Logf("subscriber released %s after disconnect", took)
}

// A stream that nobody disconnects keeps running: the initial event is not
// the whole conversation, a later Notify reaches the client too.
func TestUI_SSEStream_DeliversLaterEvents(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	srv := httptest.NewServer(s.GetRouter())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse/workers", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	reads := make(chan string, 4)
	go func() {
		buf := make([]byte, 256)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				reads <- string(buf[:n])
			}
			if rerr != nil {
				close(reads)
				return
			}
		}
	}()

	// Initial catch-up event.
	select {
	case got := <-reads:
		assert.Contains(t, got, "workers-changed")
	case <-time.After(5 * time.Second):
		t.Fatal("no initial SSE event within 5s")
	}

	if _, ok := waitFor(2*time.Second, func() bool { return s.WorkerEvents.SubscriberCount() == 1 }); !ok {
		t.Fatal("stream never registered a subscriber")
	}
	s.WorkerEvents.Notify()

	select {
	case got := <-reads:
		assert.Contains(t, got, "workers-changed")
	case <-time.After(5 * time.Second):
		t.Fatal("Notify() did not reach the open stream within 5s")
	}
}

// The lifecycle script has to actually be on the page and actually be
// served; it is easy to drop one half of that and not notice until a
// browser starts hanging again.
func TestUI_Layout_LoadsSSELifecycleScript(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	for _, path := range []string{"/ui/", "/ui/workers", "/ui/task-types", "/ui/about"} {
		w := getUI(r, path)
		assert.Equal(t, http.StatusOK, w.Code, path)
		body := w.Body.String()
		assert.Contains(t, body, `src="/ui/static/sse-lifecycle.js"`, path)

		// It must load after the SSE extension: the script reads
		// window.htmx and bails out if the extension hasn't registered.
		assert.Less(t,
			strings.Index(body, "/ui/static/htmx-sse.js"),
			strings.Index(body, "/ui/static/sse-lifecycle.js"),
			"sse-lifecycle.js must be loaded after htmx-sse.js on "+path)
	}

	// Served out of the go:embed'd ui/static tree.
	asset := getUI(r, "/ui/static/sse-lifecycle.js")
	assert.Equal(t, http.StatusOK, asset.Code)
	js := asset.Body.String()
	assert.Contains(t, js, "pagehide")
	assert.Contains(t, js, "pageshow")
	assert.Contains(t, js, "htmx:beforeCleanupElement")
	assert.Contains(t, js, "htmx:afterProcessNode")
}
