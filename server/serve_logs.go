package server

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/manucorporat/sse"
	"github.com/turtlemonvh/blanket/lib/tailed_file"
)

const (
	LOGLINE_WAIT_DURATION  = 5
	DEFAULT_LOG_TAIL_LINES = 500
)

// SSEServerRestartingEvent is the SSE event name every streaming route
// emits as its last frame when the server is shutting down, right after a
// `retry:` hint telling the browser how soon to reconnect.
//
// Two reasons it exists (turtlemonvh/blanket#23 phase 2): net/http's
// Shutdown waits indefinitely on an active connection and never
// force-closes it, so a streaming handler that doesn't return on its own
// hangs shutdown forever; and a UI whose stream just died silently goes
// stale with no indication. The UI renders it as a banner -- see
// server/ui/static/sse-restart-banner.js.
const SSEServerRestartingEvent = "server-restarting"

// sseRestartRetryMs is the reconnect delay, in milliseconds, sent to
// EventSource clients before a restart. Short: the server is expected back
// within a second or two, and the browser applies its own backoff if it
// isn't.
const sseRestartRetryMs = 1000

// writeServerRestarting emits the shutdown frame on an open SSE stream.
// Callers return false from their c.Stream step immediately afterwards.
func writeServerRestarting(w io.Writer) {
	// `retry:` is a bare SSE field, not an event, so it is written
	// directly rather than through sse.Encode.
	fmt.Fprintf(w, "retry: %d\n\n", sseRestartRetryMs)
	sse.Encode(w, sse.Event{
		Event: SSEServerRestartingEvent,
		Data:  "the server is shutting down; reconnecting",
	})
}

// Function to server logfile lines from a subscription.
// isComplete should return true if we know that the subscription is finished.
// - task stopped when in terminal state
// - worker stopped when no longer heartbeating
func (s *ServerConfig) streamLog(c *gin.Context, sub *tailed_file.TailedFileSubscriber, isComplete func() bool) {
	loglineChannelIsEmpty := false
	lineno := 1

	// Closed when the server starts shutting down. Without this case the
	// handler would sit in the select below for a full LOGLINE_WAIT_DURATION
	// on an idle log, and net/http's Shutdown -- which never force-closes an
	// active connection -- would wait for it. See server/lifecycle.go.
	shutdown := s.shutdownChan()

	c.Stream(func(w io.Writer) bool {
		// This function returns a boolean indicating whether the stream should stay open
		// Every time this is called, also checks if client has left
		timer := time.NewTimer(time.Second * time.Duration(s.TimeMultiplier*LOGLINE_WAIT_DURATION))

		select {
		case <-shutdown:
			timer.Stop()
			c.Writer.Header()["Content-Type"] = []string{"text/event-stream"}
			writeServerRestarting(w)
			return false
		case logline := <-sub.NewLines:
			timer.Stop()
			c.Writer.Header()["Content-Type"] = []string{"text/event-stream"}
			sse.Encode(c.Writer, sse.Event{
				Id:    strconv.Itoa(lineno),
				Event: "message",
				Data:  logline + "\n",
			})
			lineno++
			loglineChannelIsEmpty = false
		case <-timer.C:
			loglineChannelIsEmpty = true
		}

		// If we have emptied the channel, decide whether to stop sending data
		if loglineChannelIsEmpty {
			// Check whether the process is complete
			// If so, return false so we quit streaming
			if isComplete() {
				return false
			}
		}

		return true
	})
}

// tailLines reads the last n lines from a file. Returns an empty string if
// the file doesn't exist or is empty.
func tailLines(filepath string, n int) (string, error) {
	f, err := os.Open(filepath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}

	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func (s *ServerConfig) tailTaskLog(c *gin.Context) {
	taskId, err := s.getTaskId(c)
	if err != nil {
		return
	}
	task, err := s.DB.GetTask(taskId)
	if err != nil {
		c.String(http.StatusNotFound, err.Error())
		return
	}
	n := DEFAULT_LOG_TAIL_LINES
	if q := c.Query("n"); q != "" {
		if parsed, err := strconv.Atoi(q); err == nil && parsed > 0 {
			n = parsed
		}
	}
	stdoutPath := path.Join(task.ResultDir, fmt.Sprintf("blanket.stdout.log"))
	content, err := tailLines(stdoutPath, n)
	if err != nil {
		c.String(http.StatusOK, "")
		return
	}
	c.Header("Content-Type", "text/plain")
	c.String(http.StatusOK, content)
}

func (s *ServerConfig) tailWorkerLog(c *gin.Context) {
	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	w, err := s.DB.GetWorker(workerId)
	if err != nil {
		c.String(http.StatusNotFound, err.Error())
		return
	}
	n := DEFAULT_LOG_TAIL_LINES
	if q := c.Query("n"); q != "" {
		if parsed, err := strconv.Atoi(q); err == nil && parsed > 0 {
			n = parsed
		}
	}
	content, err := tailLines(w.Logfile, n)
	if err != nil {
		c.String(http.StatusOK, "")
		return
	}
	c.Header("Content-Type", "text/plain")
	c.String(http.StatusOK, content)
}
