package server

// Phone support for the web UI (turtlemonvh/blanket#216): the instance
// name in the header, the server-computed task duration (the mobile cards
// lead with it; desktop shows it as a Duration column), and the web app manifest that makes the UI installable as a
// PWA.
//
// There is deliberately no service worker. Everything the UI shows is
// live, so an offline cache would only ever serve stale task states; the
// manifest and icons alone are what "Add to Home Screen" needs.

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/tasks"
)

// instanceNameKey is the optional config key that names this blanket in
// the UI header and the installed app's name. Empty means "use the
// machine's hostname", which is what tells two installs on one phone
// apart without anyone having to configure anything.
const instanceNameKey = "ui.instanceName"

// osHostname is os.Hostname, swappable in tests.
var osHostname = os.Hostname

// uiInstanceName returns the name shown in the UI header: ui.instanceName
// when set, else the hostname, else "" (the header then shows the brand
// alone).
func uiInstanceName() string {
	if name := strings.TrimSpace(viper.GetString(instanceNameKey)); name != "" {
		return name
	}
	host, err := osHostname()
	if err != nil {
		return ""
	}
	return host
}

// fmtDuration renders a duration compactly enough for a phone card's top
// line and a desktop table column: "42s", "3m 05s", "2h 07m", "3d 4h". Seconds are dropped once the
// duration is long enough that they're noise, and nothing here ticks on
// the client -- the value is refreshed whenever the row is re-rendered.
func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int64(d / time.Second)
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm %02ds", secs/60, secs%60)
	case secs < 86400:
		return fmt.Sprintf("%dh %02dm", secs/3600, (secs%3600)/60)
	default:
		return fmt.Sprintf("%dd %dh", secs/86400, (secs%86400)/3600)
	}
}

// taskDurationAt is a task's duration text (card, Duration column, and
// task detail row), as of now:
//
//   - WAITING: how long it has been queued, "queued 4m 10s".
//   - CLAIMED / RUNNING: time since it started.
//   - terminal: how long it ran, start to its last update (the finish).
//
// Anything else -- a SCHEDULED task, a series template, a terminal task
// that was cancelled before it ever started -- has no meaningful duration
// and gets "".
func taskDurationAt(t tasks.Task, now time.Time) string {
	since := func(ts int64) time.Duration { return now.Sub(time.Unix(ts, 0)) }
	switch t.State {
	case "WAITING":
		if t.CreatedTs == 0 {
			return ""
		}
		return "queued " + fmtDuration(since(t.CreatedTs))
	case "CLAIMED", "RUNNING":
		if t.StartedTs == 0 {
			return ""
		}
		return fmtDuration(since(t.StartedTs))
	}
	if !isTerminalState(t.State) || t.StartedTs == 0 || t.LastUpdatedTs < t.StartedTs {
		return ""
	}
	return fmtDuration(time.Duration(t.LastUpdatedTs-t.StartedTs) * time.Second)
}

// exitSummary is the exit status as the task card's context line words
// it: "exit 0" / "exit 3" (red) / "no exit code". Plain text rather than
// a second exitCode badge: the desktop Exit column's badge is still in the
// same row (just hidden on a phone), and two identical badges in one row
// make "the exit code" ambiguous to anything reading the DOM.
func exitSummary(code *int) template.HTML {
	if code == nil {
		return template.HTML(`<span class="ctx-exit">no exit code</span>`)
	}
	class := "ctx-exit"
	if *code != 0 {
		class += " ctx-exit-bad"
	}
	return template.HTML(fmt.Sprintf(`<span class="%s">exit %d</span>`, class, *code))
}

func isTerminalState(state string) bool {
	for _, s := range tasks.ValidTerminalTaskStates {
		if s == state {
			return true
		}
	}
	return false
}

// webManifest is the PWA manifest served at /ui/manifest.webmanifest.
// It's generated rather than a static file so the installed app's name
// carries the instance name: two blankets installed on one phone should
// not both be called "Blanket".
//
// The icons are a placeholder "B" on the accent blue; turtlemonvh/blanket#55
// replaces them with a real logo.
func webManifest() gin.H {
	name := "Blanket"
	if inst := uiInstanceName(); inst != "" {
		name = "Blanket · " + inst
	}
	return gin.H{
		"name":             name,
		"short_name":       "Blanket",
		"description":      "Long-running command-line tasks behind a web UI.",
		"id":               "/ui/",
		"start_url":        "/ui/",
		"scope":            "/ui/",
		"display":          "standalone",
		"background_color": "#ffffff",
		"theme_color":      "#2a6fb0",
		"icons": []gin.H{
			{"src": "/ui/static/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "/ui/static/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "/ui/static/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
	}
}

// uiManifest serves the web app manifest with the media type browsers
// expect for it.
func (s *ServerConfig) uiManifest(c *gin.Context) {
	c.Header("Content-Type", "application/manifest+json")
	c.JSON(http.StatusOK, webManifest())
}
