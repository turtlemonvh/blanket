package server

// Tests for the phone layout and PWA support (turtlemonvh/blanket#216).

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/tasks"
)

func TestFmtDuration(t *testing.T) {
	cases := map[time.Duration]string{
		-5 * time.Second:              "0s",
		0:                             "0s",
		42 * time.Second:              "42s",
		3*time.Minute + 5*time.Second: "3m 05s",
		2*time.Hour + 7*time.Minute + 59*time.Second:  "2h 07m",
		3*24*time.Hour + 4*time.Hour + 30*time.Minute: "3d 4h",
	}
	for d, want := range cases {
		assert.Equal(t, want, fmtDuration(d), "fmtDuration(%s)", d)
	}
}

func TestTaskDurationAt(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ts := func(ago time.Duration) int64 { return now.Add(-ago).Unix() }

	cases := []struct {
		name string
		task tasks.Task
		want string
	}{
		{"waiting is time queued since creation",
			tasks.Task{State: "WAITING", CreatedTs: ts(4*time.Minute + 10*time.Second)}, "queued 4m 10s"},
		{"running is time since start",
			tasks.Task{State: "RUNNING", CreatedTs: ts(time.Hour), StartedTs: ts(90 * time.Second)}, "1m 30s"},
		{"claimed is time since start",
			tasks.Task{State: "CLAIMED", StartedTs: ts(5 * time.Second)}, "5s"},
		{"claimed without a start time has none",
			tasks.Task{State: "CLAIMED"}, ""},
		{"terminal is start to finish, not start to now",
			tasks.Task{State: "SUCCESS", StartedTs: ts(time.Hour), LastUpdatedTs: ts(time.Hour - 75*time.Second)}, "1m 15s"},
		{"error is terminal too",
			tasks.Task{State: "ERROR", StartedTs: ts(10 * time.Second), LastUpdatedTs: ts(0)}, "10s"},
		{"cancelled before starting has no duration",
			tasks.Task{State: "STOPPED", CreatedTs: ts(time.Hour), LastUpdatedTs: ts(0)}, ""},
		{"scheduled has none",
			tasks.Task{State: "SCHEDULED", CreatedTs: ts(time.Hour)}, ""},
		{"recurring template has none",
			tasks.Task{State: "RECURRING", CreatedTs: ts(time.Hour)}, ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, taskDurationAt(c.task, now), c.name)
	}
}

func TestExitSummary(t *testing.T) {
	zero, three := 0, 3
	assert.Equal(t, `<span class="ctx-exit">no exit code</span>`, string(exitSummary(nil)))
	assert.Equal(t, `<span class="ctx-exit">exit 0</span>`, string(exitSummary(&zero)))
	assert.Equal(t, `<span class="ctx-exit ctx-exit-bad">exit 3</span>`, string(exitSummary(&three)))
}

func TestUIInstanceName(t *testing.T) {
	defer func(orig func() (string, error)) { osHostname = orig }(osHostname)
	defer viper.Set(instanceNameKey, "")

	osHostname = func() (string, error) { return "build-box", nil }
	viper.Set(instanceNameKey, "")
	assert.Equal(t, "build-box", uiInstanceName(), "defaults to the hostname")

	viper.Set(instanceNameKey, "  gpu rig  ")
	assert.Equal(t, "gpu rig", uiInstanceName(), "ui.instanceName overrides it, trimmed")

	viper.Set(instanceNameKey, "")
	osHostname = func() (string, error) { return "", errors.New("no hostname") }
	assert.Equal(t, "", uiInstanceName(), "no hostname and no config means no name")
}

func TestUI_LayoutShowsInstanceNameAndPWATags(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()
	defer viper.Set(instanceNameKey, "")
	viper.Set(instanceNameKey, "night-shift")

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	body := getUI(r, "/ui/").Body.String()
	assert.Contains(t, body, `class="instance-name"`)
	assert.Contains(t, body, ">night-shift<")
	assert.Contains(t, body, `<link rel="manifest" href="/ui/manifest.webmanifest">`)
	assert.Contains(t, body, `<meta name="theme-color" content="#2a6fb0">`)
	assert.Contains(t, body, `<link rel="apple-touch-icon" href="/ui/static/apple-touch-icon.png">`)
	assert.Contains(t, body, `<meta name="apple-mobile-web-app-capable" content="yes">`)
	// The phone tab bar marks the current section.
	assert.Regexp(t, regexp.MustCompile(`<nav class="tabbar"[^>]*>\s*<a href="/ui/" class="active" aria-current="page">Tasks</a>`), body)

	workers := getUI(r, "/ui/workers").Body.String()
	assert.Regexp(t, regexp.MustCompile(`<a href="/ui/workers" class="active" aria-current="page">Workers</a>`), workers)
}

func TestUI_Manifest(t *testing.T) {
	defer viper.Set(instanceNameKey, "")
	viper.Set(instanceNameKey, "lab")

	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	w := getUI(r, "/ui/manifest.webmanifest")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/manifest+json", w.Header().Get("Content-Type"))

	var m struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
		StartURL  string `json:"start_url"`
		Scope     string `json:"scope"`
		Display   string `json:"display"`
		Icons     []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m))
	assert.Equal(t, "Blanket · lab", m.Name)
	assert.Equal(t, "Blanket", m.ShortName)
	assert.Equal(t, "/ui/", m.StartURL)
	assert.Equal(t, "/ui/", m.Scope)
	assert.Equal(t, "standalone", m.Display)

	// Installability: a 192 and a 512 icon, one of them maskable, and every
	// icon the manifest names is actually served.
	sizes := map[string]bool{}
	maskable := false
	for _, ic := range m.Icons {
		sizes[ic.Sizes] = true
		if strings.Contains(ic.Purpose, "maskable") {
			maskable = true
		}
		iw := getUI(r, ic.Src)
		assert.Equal(t, http.StatusOK, iw.Code, "icon %s", ic.Src)
		assert.Equal(t, "image/png", iw.Header().Get("Content-Type"), "icon %s", ic.Src)
	}
	assert.True(t, sizes["192x192"], "needs a 192x192 icon")
	assert.True(t, sizes["512x512"], "needs a 512x512 icon")
	assert.True(t, maskable, "needs a maskable icon")

	assert.Equal(t, http.StatusOK, getUI(r, "/ui/static/apple-touch-icon.png").Code)
}

// Every cancel and every delete in the task rows asks first, naming the
// task type (#216); the card's duration and context cells are rendered.
func TestUI_TasksRows_ConfirmsAndCardCells(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	newTask := func() string {
		w := postTask(r, "echo_task")
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		var created struct {
			Id string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
		return created.Id
	}
	newTask() // stays WAITING
	done := newTask()
	require.Equal(t, http.StatusOK, putNoBody(r, "/task/"+done+"/cancel").Code)

	body := getUI(r, "/ui/partials/tasks-rows").Body.String()
	assert.Contains(t, body, `hx-confirm="Cancel this echo_task task (WAITING)? It will not run."`)
	assert.Contains(t, body, `hx-confirm="Delete this echo_task task (`+done[:8]+`) and its results? This cannot be undone."`)
	assert.Contains(t, body, `class="cell-duration m-only"`)
	assert.Contains(t, body, "queued ")
	assert.Contains(t, body, "created ")
	assert.Contains(t, body, "· finished ")

	// No mutating control without a confirm.
	for _, attr := range []string{"hx-put=", "hx-delete="} {
		for _, chunk := range strings.Split(body, "<a ")[1:] {
			end := strings.Index(chunk, ">")
			tag := chunk[:end]
			if strings.Contains(tag, attr) {
				assert.Contains(t, tag, "hx-confirm=", "control without a confirm: <a %s>", tag)
			}
		}
	}
}
