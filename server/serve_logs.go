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
	"github.com/turtlemonvh/blanket/lib/combined_log"
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

// streamRawLog is streamLog's counterpart for GET /task/:id/log's raw
// shape (turtlemonvh/blanket#123): rt already holds up to
// uiLogHistoryLines of the file's history (tailed_file.ReplayAndFollow,
// opened by the caller) in addition to its live channel, where streamLog's
// tailed_file.Follow subscription carries only whatever a shared ring
// buffer happened to have. The history is printed as the same `message`
// SSE frames a live line produces, then the function behaves exactly like
// streamLog against rt.Lines.
//
// isComplete has the same contract as streamLog's: true once the task is
// terminal (or unreachable), which is only checked once the live channel
// has gone quiet for a full idle window.
func (s *ServerConfig) streamRawLog(c *gin.Context, rt *tailed_file.ReplayTail, isComplete func() bool) {
	lineno := 1
	if len(rt.History) > 0 {
		c.Writer.Header()["Content-Type"] = []string{"text/event-stream"}
		for _, logline := range rt.History {
			sse.Encode(c.Writer, sse.Event{
				Id:    strconv.Itoa(lineno),
				Event: "message",
				Data:  logline + "\n",
			})
			lineno++
		}
		// c.Stream below only flushes once its step function returns, and
		// the first call is about to block for a full idle window waiting
		// on a live line -- on a quiet task that would leave this history
		// invisible for LOGLINE_WAIT_DURATION after connecting, exactly the
		// gap replaying it exists to close. Same fix as ui_logs.go's
		// uiTaskLogStream, same reason.
		c.Writer.Flush()
	}

	loglineChannelIsEmpty := false

	// Closed when the server starts shutting down -- see streamLog's own
	// comment on shutdown above.
	shutdown := s.shutdownChan()

	c.Stream(func(w io.Writer) bool {
		timer := time.NewTimer(time.Second * time.Duration(s.TimeMultiplier*LOGLINE_WAIT_DURATION))

		select {
		case <-shutdown:
			timer.Stop()
			c.Writer.Header()["Content-Type"] = []string{"text/event-stream"}
			writeServerRestarting(w)
			return false
		case logline, ok := <-rt.Lines:
			timer.Stop()
			if !ok {
				// The tailer stopped from under us; nothing more is
				// coming down this channel.
				return false
			}
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

		if loglineChannelIsEmpty {
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
	content, _, err := tailLinesTruncated(filepath, n)
	return content, err
}

// tailLinesTruncated is tailLines plus a flag saying whether anything was
// dropped off the front — the synchronous completion payload reports
// stdoutTruncated / stderrTruncated so a caller can tell "that's all the
// output" from "that's the tail of the output" (turtlemonvh/blanket#27).
func tailLinesTruncated(filepath string, n int) (string, bool, error) {
	f, err := os.Open(filepath)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}

	truncated := len(lines) > n
	if truncated {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 0 {
		return "", false, nil
	}
	return strings.Join(lines, "\n") + "\n", truncated, nil
}

// Log file names the worker writes under a task's ResultDir
// (SetupExecutionDirectory). Named here because three surfaces now join
// them to a result dir -- the raw log routes below, the completion payload
// (serve_sync.go), and the UI's log pane (ui_logs.go).
const (
	TaskStdoutLogFile = "blanket.stdout.log"
	TaskStderrLogFile = "blanket.stderr.log"

	// TaskCombinedLogFile is the worker's record of how the two streams
	// above interleaved: one NDJSON line per line of output, in arrival
	// order, tagged with the stream it came from (lib/combined_log). It
	// is the only thing on disk that carries that ordering -- the two
	// per-stream files can't -- so the UI's combined log view reads it
	// when it is there and falls back to grouping the two files when it
	// isn't (a task run by a worker older than turtlemonvh/blanket#104,
	// or one with `workers.combinedLog = false`).
	//
	// It is built by tailing the two files, and the worker stops tailing
	// shortly after the task exits, so it covers everything the task
	// itself wrote and not output from a process the task left running
	// behind it. The two files above always have all of it.
	TaskCombinedLogFile = combined_log.FileName
)

// rawLogStreamFor resolves the `?stream=` parameter shared by
// GET /task/:id/log and GET /task/:id/log/tail onto one of the task's two
// log files.
//
// Absent or empty means stdout, so both routes behave exactly as they
// always have; `stderr` selects the other file. `both` is deliberately
// *not* accepted here: these routes emit an undecorated byte stream with
// nothing to tell the two apart, so interleaving them would hand the
// caller an ambiguous result. A client that wants both at once has the
// structured stream (`?format=ndjson`), whose log events carry a `stream`
// discriminator.
//
// Returns ("", "", false) after writing a 400 for an unrecognized value:
// silently falling back to stdout would let a typo look like an empty
// stderr.
func rawLogStreamFor(c *gin.Context) (filename string, stream string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(c.Query("stream"))) {
	case "", LogStreamStdout:
		return TaskStdoutLogFile, LogStreamStdout, true
	case LogStreamStderr:
		return TaskStderrLogFile, LogStreamStderr, true
	default:
		c.String(http.StatusBadRequest, MakeErrorString(fmt.Sprintf(
			"Invalid 'stream' parameter '%s'; must be 'stdout' or 'stderr'. For both at once use the structured stream (?format=ndjson), whose log events carry a 'stream' field.",
			c.Query("stream"))))
		return "", "", false
	}
}

func (s *ServerConfig) tailTaskLog(c *gin.Context) {
	taskId, err := s.getTaskId(c)
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.DB.GetTask(taskId)
	if err != nil {
		c.String(statusForDBError(err, http.StatusInternalServerError), err.Error())
		return
	}
	logFile, _, ok := rawLogStreamFor(c)
	if !ok {
		return
	}
	n := DEFAULT_LOG_TAIL_LINES
	if q := c.Query("n"); q != "" {
		if parsed, err := strconv.Atoi(q); err == nil && parsed > 0 {
			n = parsed
		}
	}
	content, err := tailLines(path.Join(task.ResultDir, logFile), n)
	if err != nil {
		c.String(http.StatusOK, "")
		return
	}
	c.Header("Content-Type", "text/plain")
	c.String(http.StatusOK, content)
}

func (s *ServerConfig) tailWorkerLog(c *gin.Context) {
	workerId, err := s.getWorkerId(c)
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	w, err := s.DB.GetWorker(workerId)
	if err != nil {
		c.String(statusForDBError(err, http.StatusInternalServerError), err.Error())
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
