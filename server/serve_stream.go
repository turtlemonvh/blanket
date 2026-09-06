package server

// Structured task event streams -- turtlemonvh/blanket#27, PR 2.
//
// One event schema, one encoder, two framings, two routes:
//
//	POST /task/?wait&stream   the synchronous submit, streamed
//	GET  /task/:id/log        with ?format=ndjson / Accept: application/x-ndjson
//
// Every event is a JSON object carrying ts, taskId and type. `state`
// events report a transition, `log` events carry one line of the task's
// stdout or stderr, and the terminal `result` event carries the exact
// CompletionPayload a blocking ?wait would have returned -- byte for
// byte, so a client that can parse one mode can parse the other. That
// repetition is deliberate (the design's decision 5): one parser for
// both modes beats saving a few kilobytes.
//
// Framing is negotiated, the payload is not. NDJSON (one object per
// line) is the default for `&stream` because the callers that motivated
// the issue -- curl in a pipeline, the Go CLI, an agent's HTTP client --
// all parse line-delimited JSON trivially; SSE is available via
// `Accept: text/event-stream` for browser consumers and matches how
// /task/:id/log already behaves.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/manucorporat/sse"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/tailed_file"
	"github.com/turtlemonvh/blanket/tasks"
)

// Event type discriminators (the `type` field, and the SSE `event:` name).
const (
	EventTypeState  = "state"
	EventTypeLog    = "log"
	EventTypeResult = "result"
)

// Values of a log event's `stream` field.
const (
	LogStreamStdout = "stdout"
	LogStreamStderr = "stderr"
)

// Framings the encoder can write. The JSON object is identical in both.
const (
	FramingNDJSON = "ndjson"
	FramingSSE    = "sse"

	ContentTypeNDJSON = "application/x-ndjson"
	ContentTypeSSE    = "text/event-stream"
)

const (
	// logDrainGrace is how long a stream waits for more log lines once the
	// task has reached a terminal state before giving up and emitting the
	// result event. The worker Sync()s the log file on its own poll
	// interval and the tailer polls the file, so the last lines of a task
	// routinely land a moment after the state flips.
	logDrainGrace = 400 * time.Millisecond
	// logDrainMax bounds that wait, so a chatty task can't keep the drain
	// alive indefinitely. The result event repeats the output tail
	// anyway, so nothing is lost by cutting the live delivery short.
	logDrainMax = 3 * time.Second
)

/*
 * The event schema
 */

// EventEnvelope is the part of every event that is always present. It is
// embedded (unnamed) in each concrete event type, so its fields are
// flattened into the same JSON object and come first.
type EventEnvelope struct {
	// Ts is unix seconds, matching every timestamp on the task record.
	Ts int64 `json:"ts"`
	// TaskId is the hex id of the task this stream is about. Every event
	// on a stream carries the same one; it is there so a caller
	// multiplexing several streams doesn't have to track it out of band.
	TaskId string `json:"taskId"`
	// Type is one of EventTypeState / EventTypeLog / EventTypeResult.
	Type string `json:"type"`
}

// EventType reports the event's discriminator, which is also the SSE
// `event:` name. Defined on the envelope so every concrete event type
// satisfies StreamEvent for free.
func (e EventEnvelope) EventType() string { return e.Type }

// StreamEvent is anything the encoder can write.
type StreamEvent interface {
	EventType() string
}

// StateEvent reports a task state transition the stream observed.
//
// These are what make a wait debuggable: without them a caller that
// waits 30 seconds and times out cannot tell "no worker was free to
// claim this" (state stayed WAITING) from "the task is genuinely slow"
// (state reached RUNNING).
type StateEvent struct {
	EventEnvelope
	State string `json:"state"`
	// PreviousState is null on the first event of a stream, which reports
	// the state the task was already in rather than a transition.
	PreviousState *string `json:"previousState"`
}

// LogEvent is one line of task output.
//
// Delivery is best-effort and lags the task: the worker Sync()s
// blanket.stdout.log on its own poll interval, and the streams aren't
// attached until the task reaches CLAIMED (the files don't exist until
// the worker sets the execution directory up). The terminal result event
// repeats the tail of both streams, so a client that reads to the end
// always has the authoritative output even if it missed live lines.
type LogEvent struct {
	EventEnvelope
	// Stream is LogStreamStdout or LogStreamStderr.
	Stream string `json:"stream"`
	// Seq counts lines within one stream of one connection, from 1. It is
	// per-stream (stdout and stderr each start at 1) and per-connection:
	// it is not a position in the file, and a reconnecting client will
	// see it restart.
	Seq int `json:"seq"`
	// Line is the log line, without its trailing newline.
	Line string `json:"line"`
}

// ResultEvent terminates every stream that isn't cut short by the client
// hanging up. It embeds the completion payload verbatim, so its JSON is
// the blocking ?wait body plus the three envelope fields -- including
// waitOutcome, which is how a stream expresses the distinction the
// blocking mode expresses with 200 vs 504 (a stream's status is already
// sent by the time the outcome is known).
type ResultEvent struct {
	EventEnvelope
	CompletionPayload
}

func newEnvelope(taskId, eventType string) EventEnvelope {
	return EventEnvelope{Ts: time.Now().Unix(), TaskId: taskId, Type: eventType}
}

// NewStateEvent builds a state event. An empty previous means "first
// event on this stream", encoded as previousState: null.
func NewStateEvent(taskId, state, previous string) StateEvent {
	ev := StateEvent{EventEnvelope: newEnvelope(taskId, EventTypeState), State: state}
	if previous != "" {
		p := previous
		ev.PreviousState = &p
	}
	return ev
}

// NewLogEvent builds a log event for one line of the named stream.
func NewLogEvent(taskId, stream string, seq int, line string) LogEvent {
	return LogEvent{
		EventEnvelope: newEnvelope(taskId, EventTypeLog),
		Stream:        stream,
		Seq:           seq,
		Line:          line,
	}
}

// NewResultEvent wraps a completion payload as the terminal event.
func NewResultEvent(taskId string, payload CompletionPayload) ResultEvent {
	return ResultEvent{EventEnvelope: newEnvelope(taskId, EventTypeResult), CompletionPayload: payload}
}

/*
 * The encoder
 */

// StreamEncoder writes events in one framing. Both framings marshal the
// event to the same JSON; only the wrapping differs, which is the whole
// point of having a single encoder rather than one per route.
type StreamEncoder struct {
	w       io.Writer
	flusher http.Flusher
	framing string
}

// NewStreamEncoder returns an encoder writing to w. If w is an
// http.Flusher (a gin ResponseWriter, an httptest recorder) each event is
// flushed as it is written -- without that a streamed event sits in the
// response buffer until the handler returns, which defeats the point.
func NewStreamEncoder(w io.Writer, framing string) *StreamEncoder {
	enc := &StreamEncoder{w: w, framing: framing}
	if f, ok := w.(http.Flusher); ok {
		enc.flusher = f
	}
	return enc
}

// Encode writes one event.
//
//	ndjson: <json>\n
//	sse:    event:<type>\ndata:<json>\n\n
//
// A write error means the client is gone; callers treat it as a
// disconnect and stop.
func (e *StreamEncoder) Encode(ev StreamEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}

	if e.framing == FramingSSE {
		// Data is handed over as a pre-marshalled string so the JSON on
		// the wire is byte-identical to the NDJSON framing's; passing the
		// struct would let the sse package re-encode it.
		err = sse.Encode(e.w, sse.Event{Event: ev.EventType(), Data: string(b)})
	} else {
		_, err = e.w.Write(append(b, '\n'))
	}
	if err != nil {
		return err
	}
	if e.flusher != nil {
		e.flusher.Flush()
	}
	return nil
}

// ContentType is the response Content-Type for the encoder's framing.
func (e *StreamEncoder) ContentType() string {
	if e.framing == FramingSSE {
		return ContentTypeSSE
	}
	return ContentTypeNDJSON
}

/*
 * Content negotiation
 */

func acceptsSSE(c *gin.Context) bool {
	return strings.Contains(strings.ToLower(c.GetHeader("Accept")), ContentTypeSSE)
}

// streamFramingFor picks the framing for POST /task/?wait&stream: NDJSON
// unless the caller asked for SSE via Accept.
func streamFramingFor(c *gin.Context) string {
	if acceptsSSE(c) {
		return FramingSSE
	}
	return FramingNDJSON
}

// structuredLogStreamRequested reports whether GET /task/:id/log should
// answer with the structured event stream instead of its historical raw
// SSE log lines.
//
// The default is deliberately unchanged: the htmx UI's SSE extension
// consumes the raw form, and this route predates the event schema. A
// caller opts in explicitly with ?format=ndjson or an
// `Accept: application/x-ndjson` header.
func structuredLogStreamRequested(c *gin.Context) bool {
	switch strings.ToLower(strings.TrimSpace(c.Query("format"))) {
	case "ndjson", "events":
		return true
	}
	return strings.Contains(strings.ToLower(c.GetHeader("Accept")), ContentTypeNDJSON)
}

// startEventStream writes the response head for a streaming response and
// returns the encoder for its body. Called before the outcome is known,
// which is why a stream is always a 200: by the time the task's state is
// decided the status line is long gone. (fail_on_error therefore has no
// effect on a stream; the result event's task.state carries the outcome.)
func (s *ServerConfig) startEventStream(c *gin.Context, framing string) *StreamEncoder {
	enc := NewStreamEncoder(c.Writer, framing)

	h := c.Writer.Header()
	h.Set("Content-Type", enc.ContentType())
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	if framing == FramingSSE {
		h.Set("Connection", "keep-alive")
	}
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	return enc
}

/*
 * The stream itself
 */

// logTail is one attached tailed_file subscription plus the per-stream
// sequence counter for the events it produces.
type logTail struct {
	stream string
	sub    *tailed_file.TailedFileSubscriber
	seq    int
}

func (l *logTail) next() int {
	l.seq++
	return l.seq
}

// followTail attaches to one of the task's log files, or returns nil if
// it isn't there yet -- the worker creates both files in
// SetupExecutionDirectory, so a task that hasn't been claimed has
// neither, and the caller simply retries on its next wake.
func followTail(task tasks.Task, filename, stream string) *logTail {
	sub, err := tailed_file.Follow(path.Join(task.ResultDir, filename))
	if err != nil {
		return nil
	}
	return &logTail{stream: stream, sub: sub}
}

// stopTail releases a tail subscription without deadlocking.
//
// The tailer goroutine sends to subscriber channels while holding the
// TailedFile lock, and TailedFileSubscriber.Stop needs that same lock --
// so a handler that stops reading and then unsubscribes can wedge the
// tailer permanently. Draining the channel concurrently with Stop lets
// any in-flight send complete; the drain then ends when Stop closes the
// channel.
func stopTail(l *logTail) {
	if l == nil || l.sub == nil {
		return
	}
	drained := make(chan struct{})
	go func() {
		for range l.sub.NewLines {
		}
		close(drained)
	}()
	l.sub.Stop()
	<-drained
}

// tailAttachable reports whether the task has progressed far enough for
// its log files to exist. Before CLAIMED there is nothing on disk to
// follow; a terminal state still counts, so a task that finished before
// the stream ever saw it RUNNING still gets its output streamed.
func tailAttachable(state string) bool {
	return state == "CLAIMED" || state == "RUNNING" || tasks.IsTerminalState(state)
}

// runTaskEventStream is the body of both structured streams.
//
// It re-reads the task on every wake and reports what changed: EventHub
// is a global broadcast that carries no payload, so a notification is
// only ever a hint that *something* moved, never a description of what.
// A one-second fallback ticker (scaled by timeMultiplier) covers a
// missed notification.
//
// wait > 0 gives the stream a budget: when it expires the stream emits a
// result event with waitOutcome "wait_timeout" and closes, leaving the
// task running. wait <= 0 means no budget -- the stream runs until the
// task is terminal or the client hangs up, which is what GET
// /task/:id/log wants.
//
// hubSub may be nil (nothing to wake on but the ticker). It is owned by
// the caller, which must unsubscribe it; every tail subscription opened
// here is released on every exit path.
func (s *ServerConfig) runTaskEventStream(c *gin.Context, enc *StreamEncoder, task tasks.Task, hubSub chan struct{}, wait time.Duration) {
	mult := s.timeMultiplier()
	ctx := c.Request.Context()
	taskId := task.Id
	idHex := taskId.Hex()

	ticker := time.NewTicker(time.Duration(float64(syncPollInterval) * mult))
	defer ticker.Stop()

	var deadline <-chan time.Time
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		deadline = timer.C
	}

	var stdout, stderr *logTail
	// Closed-by-the-tailer flags: once a TailedFile has been stopped from
	// under us (server shutdown, or the file's last subscriber leaving)
	// its channel is closed and must neither be re-followed nor Stop()ed
	// a second time.
	var stdoutClosed, stderrClosed bool
	defer func() {
		stopTail(stdout)
		stopTail(stderr)
	}()

	lastState := ""
	last := task
	writeFailed := false

	emit := func(ev StreamEvent) bool {
		if err := enc.Encode(ev); err != nil {
			log.WithFields(log.Fields{
				"err":    err.Error(),
				"taskId": idHex,
			}).Debug("error writing task event; treating as a client disconnect")
			writeFailed = true
			return false
		}
		return true
	}

	emitLog := func(l *logTail, line string) bool {
		return emit(NewLogEvent(idHex, l.stream, l.next(), line))
	}

	for {
		if cur, err := s.DB.GetTask(taskId); err != nil {
			var notFound database.ItemNotFoundError
			if errors.As(err, &notFound) {
				// Deleted out from under the stream: it can never reach a
				// terminal state, so close rather than spin to the deadline.
				log.WithFields(log.Fields{"taskId": idHex}).Info("task disappeared while streaming its events; closing stream")
				return
			}
			log.WithFields(log.Fields{
				"err":    err.Error(),
				"taskId": idHex,
			}).Debug("error re-reading task while streaming its events")
		} else {
			last = cur
			if cur.State != lastState {
				if !emit(NewStateEvent(idHex, cur.State, lastState)) {
					return
				}
				lastState = cur.State
			}

			if tailAttachable(cur.State) {
				if stdout == nil && !stdoutClosed {
					stdout = followTail(cur, "blanket.stdout.log", LogStreamStdout)
				}
				if stderr == nil && !stderrClosed {
					stderr = followTail(cur, "blanket.stderr.log", LogStreamStderr)
				}
			}

			if tasks.IsTerminalState(cur.State) {
				// Give the tailers a moment to hand over anything the
				// worker flushed on its way out, then close with the
				// authoritative payload.
				s.drainTails(ctx, &stdout, &stderr, emitLog)
				if writeFailed {
					return
				}
				emit(NewResultEvent(idHex, s.buildCompletionPayload(cur, WaitOutcomeCompleted)))
				return
			}
		}

		var outCh, errCh <-chan string
		if stdout != nil {
			outCh = stdout.sub.NewLines
		}
		if stderr != nil {
			errCh = stderr.sub.NewLines
		}

		select {
		case <-hubSub:
		case <-ticker.C:
		case line, ok := <-outCh:
			if !ok {
				stdout, stdoutClosed = nil, true
				continue
			}
			if !emitLog(stdout, line) {
				return
			}
		case line, ok := <-errCh:
			if !ok {
				stderr, stderrClosed = nil, true
				continue
			}
			if !emitLog(stderr, line) {
				return
			}
		case <-ctx.Done():
			// Client hung up. The task is left completely alone; its
			// results stay fetchable by id.
			log.WithFields(log.Fields{"taskId": idHex}).Info("client disconnected from task event stream; task left running")
			return
		case <-deadline:
			// Same meaning as the blocking mode's 504, expressed in the
			// payload because the status is already sent.
			emit(NewResultEvent(idHex, s.buildCompletionPayload(last, WaitOutcomeTimeout)))
			return
		}
	}
}

// drainTails emits whatever log lines are still in flight once the task
// has gone terminal, then returns. Bounded twice over -- logDrainGrace of
// quiet ends it, logDrainMax ends it regardless -- because the result
// event repeats both output tails anyway, so this is a nicety rather
// than the delivery guarantee.
func (s *ServerConfig) drainTails(ctx context.Context, stdout, stderr **logTail, emitLog func(*logTail, string) bool) {
	mult := s.timeMultiplier()
	hard := time.NewTimer(time.Duration(float64(logDrainMax) * mult))
	defer hard.Stop()

	for {
		var outCh, errCh <-chan string
		if *stdout != nil {
			outCh = (*stdout).sub.NewLines
		}
		if *stderr != nil {
			errCh = (*stderr).sub.NewLines
		}
		if outCh == nil && errCh == nil {
			return
		}

		grace := time.NewTimer(time.Duration(float64(logDrainGrace) * mult))
		select {
		case line, ok := <-outCh:
			grace.Stop()
			if !ok {
				*stdout = nil
				continue
			}
			if !emitLog(*stdout, line) {
				return
			}
		case line, ok := <-errCh:
			grace.Stop()
			if !ok {
				*stderr = nil
				continue
			}
			if !emitLog(*stderr, line) {
				return
			}
		case <-grace.C:
			return
		case <-hard.C:
			grace.Stop()
			return
		case <-ctx.Done():
			grace.Stop()
			return
		}
	}
}

/*
 * Route entry points
 */

// streamSyncSubmit answers POST /task/?wait&stream: the whole life of the
// task as events, ending with the same payload the blocking mode returns.
// hubSub was taken before the task was enqueued (see postTask) and is
// unsubscribed by that caller.
func (s *ServerConfig) streamSyncSubmit(c *gin.Context, hubSub chan struct{}, task tasks.Task, p syncWaitParams) {
	enc := s.startEventStream(c, streamFramingFor(c))
	s.runTaskEventStream(c, enc, task, hubSub, p.Wait)
}

// streamTaskLogEvents answers GET /task/:id/log for a caller that asked
// for the structured stream. Unlike the synchronous submit there is no
// wait budget: the stream stays open until the task is terminal or the
// client goes away.
func (s *ServerConfig) streamTaskLogEvents(c *gin.Context, task tasks.Task) {
	hubSub := s.TaskEvents.Subscribe()
	defer s.TaskEvents.Unsubscribe(hubSub)

	enc := s.startEventStream(c, FramingNDJSON)
	s.runTaskEventStream(c, enc, task, hubSub, 0)
}
