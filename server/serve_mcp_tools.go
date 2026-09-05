package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/docs"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

// textResult wraps s as a successful single-text-block tool result. Every
// MCP tool handler in this file returns plain text, not JSON — see the
// design plan's "Context budget" section for why.
func textResult(s string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}, nil, nil
}

type blanketDocsArgs struct {
	Page string `json:"page" jsonschema:"doc page: overview, authoring, schema, tags, usage, api, or flow"`
}

func (s *ServerConfig) mcpDocs(ctx context.Context, req *mcp.CallToolRequest, args blanketDocsArgs) (*mcp.CallToolResult, any, error) {
	content, err := docs.Page(args.Page)
	if err != nil {
		return nil, nil, err
	}
	return textResult(content)
}

type blanketTaskTypesArgs struct {
	Name string `json:"name,omitempty" jsonschema:"task type name; omit to list all"`
}

func (s *ServerConfig) mcpTaskTypes(ctx context.Context, req *mcp.CallToolRequest, args blanketTaskTypesArgs) (*mcp.CallToolResult, any, error) {
	if args.Name != "" {
		tt, err := tasks.FetchTaskType(args.Name)
		if err != nil {
			return nil, nil, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "name: %s\n", tt.GetName())
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(tt.Config.GetStringSlice("tags"), ", "))
		fmt.Fprintf(&b, "description: %s\n", tt.GetDescription())
		fmt.Fprintf(&b, "documentation: %s\n", tt.GetDocumentation())
		fmt.Fprintf(&b, "required env: %s\n", strings.Join(tt.EnvNames("required"), ", "))
		fmt.Fprintf(&b, "default env: %s\n", strings.Join(tt.EnvNames("default"), ", "))
		return textResult(b.String())
	}

	tts, err := tasks.ReadTypes()
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-30s %s\n", "NAME", "TAGS", "DESCRIPTION")
	for _, tt := range tts {
		fmt.Fprintf(&b, "%-24s %-30s %s\n", tt.GetName(), strings.Join(tt.Config.GetStringSlice("tags"), ","), tt.GetDescription())
	}
	return textResult(b.String())
}

const (
	mcpDefaultLogLines  = 50
	mcpDefaultTaskLimit = 20
	mcpMaxTaskLimit     = 100
)

// clampLogLines applies the mcp.maxLogLines guard: n defaults to
// mcpDefaultLogLines when unset, and is capped at mcp.maxLogLines
// regardless of what the caller asked for. A worker or task log can be
// tens of megabytes, so this is a hard context guard, not a preference.
func clampLogLines(n int) int {
	if n <= 0 {
		n = mcpDefaultLogLines
	}
	if max := viper.GetInt("mcp.maxLogLines"); max > 0 && n > max {
		n = max
	}
	return n
}

type blanketTasksArgs struct {
	Id       string   `json:"id,omitempty" jsonschema:"task id; if set, returns detail with a log tail"`
	States   []string `json:"states,omitempty" jsonschema:"filter by task state"`
	Types    []string `json:"types,omitempty" jsonschema:"filter by type name"`
	Limit    int      `json:"limit,omitempty" jsonschema:"max tasks to list, default 20, max 100"`
	LogLines int      `json:"log_lines,omitempty" jsonschema:"log tail lines when id set, default 50"`
}

func (s *ServerConfig) mcpTasks(ctx context.Context, req *mcp.CallToolRequest, args blanketTasksArgs) (*mcp.CallToolResult, any, error) {
	if args.Id != "" {
		if !objectid.IsObjectIdHex(args.Id) {
			return nil, nil, fmt.Errorf("%q is not a valid task id", args.Id)
		}
		taskId := objectid.ObjectIdHex(args.Id)
		task, err := s.DB.GetTask(taskId)
		if err != nil {
			return nil, nil, err
		}

		logLines := clampLogLines(args.LogLines)
		stdoutPath := path.Join(task.ResultDir, "blanket.stdout.log")
		logTail, _ := tailLines(stdoutPath, logLines)

		var b strings.Builder
		fmt.Fprintf(&b, "id: %s\n", task.Id.Hex())
		fmt.Fprintf(&b, "type: %s\n", task.TypeId)
		fmt.Fprintf(&b, "state: %s\n", task.State)
		fmt.Fprintf(&b, "progress: %d%%\n", task.Progress)
		fmt.Fprintf(&b, "workerId: %s\n", task.WorkerId.Hex())
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(task.Tags, ", "))
		fmt.Fprintf(&b, "createdTs: %d\n", task.CreatedTs)
		fmt.Fprintf(&b, "lastUpdatedTs: %d\n", task.LastUpdatedTs)
		fmt.Fprintf(&b, "\n--- log tail (%d lines) ---\n%s", logLines, logTail)
		return textResult(b.String())
	}

	limit := args.Limit
	if limit <= 0 {
		limit = mcpDefaultTaskLimit
	}
	if limit > mcpMaxTaskLimit {
		limit = mcpMaxTaskLimit
	}

	tc := &database.TaskSearchConf{
		Limit:             limit,
		AllowedTaskStates: map[string]bool{},
		AllowedTaskTypes:  map[string]bool{},
		SmallestId:        objectid.NewObjectIdWithTime(time.Unix(0, 0)),
		LargestId:         objectid.NewObjectIdWithTime(time.Unix(database.FAR_FUTURE_SECONDS, 0)),
	}
	for _, st := range args.States {
		tc.AllowedTaskStates[st] = true
	}
	for _, ty := range args.Types {
		tc.AllowedTaskTypes[ty] = true
	}

	result, _, err := s.DB.GetTasks(tc)
	if err != nil {
		return nil, nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-16s %-8s %s\n", "ID", "TYPE", "STATE", "PROGRESS")
	for _, t := range result {
		fmt.Fprintf(&b, "%-24s %-16s %-8s %d%%\n", t.Id.Hex(), t.TypeId, t.State, t.Progress)
	}
	return textResult(b.String())
}

type blanketWorkersArgs struct {
	Id       string `json:"id,omitempty" jsonschema:"worker id; if set, returns detail with a log tail"`
	LogLines int    `json:"log_lines,omitempty" jsonschema:"log tail lines when id set, default 50"`
}

func (s *ServerConfig) mcpWorkers(ctx context.Context, req *mcp.CallToolRequest, args blanketWorkersArgs) (*mcp.CallToolResult, any, error) {
	if args.Id != "" {
		if !objectid.IsObjectIdHex(args.Id) {
			return nil, nil, fmt.Errorf("%q is not a valid worker id", args.Id)
		}
		workerId := objectid.ObjectIdHex(args.Id)
		w, err := s.DB.GetWorker(workerId)
		if err != nil {
			return nil, nil, err
		}

		logLines := clampLogLines(args.LogLines)
		logTail, _ := tailLines(w.Logfile, logLines)

		var b strings.Builder
		fmt.Fprintf(&b, "id: %s\n", w.Id.Hex())
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(w.Tags, ", "))
		fmt.Fprintf(&b, "pid: %d\n", w.Pid)
		fmt.Fprintf(&b, "stopped: %t\n", w.Stopped)
		fmt.Fprintf(&b, "checkInterval: %.1fs\n", w.CheckInterval)
		fmt.Fprintf(&b, "startedTs: %d\n", w.StartedTs)
		fmt.Fprintf(&b, "\n--- log tail (%d lines) ---\n%s", logLines, logTail)
		return textResult(b.String())
	}

	ws, err := s.DB.GetWorkers()
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-30s %-8s %s\n", "ID", "TAGS", "PID", "STOPPED")
	for _, w := range ws {
		fmt.Fprintf(&b, "%-24s %-30s %-8d %t\n", w.Id.Hex(), strings.Join(w.Tags, ","), w.Pid, w.Stopped)
	}
	return textResult(b.String())
}

type blanketWriteTaskTypeArgs struct {
	Name string `json:"name" jsonschema:"task type name (without .toml)"`
	Toml string `json:"toml" jsonschema:"TOML contents of the task type"`
}

func (s *ServerConfig) mcpWriteTaskType(ctx context.Context, req *mcp.CallToolRequest, args blanketWriteTaskTypeArgs) (*mcp.CallToolResult, any, error) {
	if args.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	if strings.ContainsAny(args.Name, `/\`) || args.Name == "." || args.Name == ".." {
		return nil, nil, fmt.Errorf("name must be a bare task type name, not a path")
	}

	tt, readErr := tasks.ReadTaskType(strings.NewReader(args.Toml))
	tt.Config.Set("name", args.Name)

	typesDirs := viper.GetStringSlice("tasks.typesPaths")
	existing, _ := tasks.ReadTaskTypesForValidation(typesDirs)
	tagIdx := tasks.BuildTagIndex(typesDirs, existing, tasks.KnownTagsOptions{})

	findings := tasks.ValidateTaskType(&tt, readErr)
	findings = append(findings, tasks.LintTags(&tt, tagIdx, tasks.TagLintOptions{})...)

	strict := viper.GetBool("mcp.validateStrict")
	blocking := false
	for _, f := range findings {
		if f.Level == tasks.LevelError || (strict && f.Level == tasks.LevelWarn) {
			blocking = true
			break
		}
	}

	if blocking {
		var b strings.Builder
		fmt.Fprintf(&b, "refused to write %q: validation failed\n", args.Name)
		for _, f := range findings {
			fmt.Fprintf(&b, "%s %s: %s\n", f.Code, f.Level, f.Message)
		}
		return textResult(b.String())
	}

	writeDir := viper.GetString("mcp.writeTypesPath")
	if writeDir == "" {
		if len(typesDirs) == 0 {
			return nil, nil, fmt.Errorf("no tasks.typesPaths configured to write into")
		}
		writeDir = typesDirs[0]
	}
	writePath := path.Join(writeDir, args.Name+".toml")
	_, statErr := os.Stat(writePath)
	existed := statErr == nil
	if err := os.WriteFile(writePath, []byte(args.Toml), 0644); err != nil {
		return nil, nil, err
	}

	var b strings.Builder
	if existed {
		fmt.Fprintf(&b, "overwrote %s\n", writePath)
	} else {
		fmt.Fprintf(&b, "wrote %s\n", writePath)
	}
	for _, f := range findings {
		fmt.Fprintf(&b, "%s %s: %s\n", f.Code, f.Level, f.Message)
	}
	return textResult(b.String())
}

type blanketSubmitTaskArgs struct {
	Type      string            `json:"type" jsonschema:"task type name"`
	Env       map[string]string `json:"env,omitempty" jsonschema:"env vars for the task"`
	NotBefore string            `json:"notBefore,omitempty" jsonschema:"delay: duration/RFC3339/unix-seconds; excl. w/ cron"`
	Cron      string            `json:"cron,omitempty" jsonschema:"5-field cron; makes this a recurring template"`
}

// mcpSubmitTask mirrors REST/CLI submit semantics: same optional
// notBefore/cron fields (mutually exclusive, same accepted shapes, same
// errors), applied the same way (newTaskForType, then
// applyScheduleChecked -- which also enforces scheduler.maxScheduled, same
// as POST /task/'s 429). The result echoes the resulting state plus the
// schedule description / next fire time, so an agent can see whether it
// got a SCHEDULED/RECURRING task without a follow-up blanket_tasks call.
func (s *ServerConfig) mcpSubmitTask(ctx context.Context, req *mcp.CallToolRequest, args blanketSubmitTaskArgs) (*mcp.CallToolResult, any, error) {
	t, err := s.newTaskForType(args.Type, args.Env)
	if err != nil {
		return nil, nil, err
	}

	scheduleReq := map[string]interface{}{}
	if args.NotBefore != "" {
		scheduleReq["notBefore"] = args.NotBefore
	}
	if args.Cron != "" {
		scheduleReq["cron"] = args.Cron
	}
	if err := s.applyScheduleChecked(&t, scheduleReq, time.Now()); err != nil {
		return nil, nil, err
	}

	if err := s.enqueueTask(ctx, &t); err != nil {
		return nil, nil, err
	}

	msg := fmt.Sprintf("submitted task %s (type=%s, state=%s)", t.Id.Hex(), t.TypeId, t.State)
	if desc := tasks.ScheduleDescriptionFor(t); desc != "" {
		msg += fmt.Sprintf("; schedule: %s", desc)
	}
	if t.State == "RECURRING" {
		msg += fmt.Sprintf("; next fire: %s", time.Unix(t.NextFireTs, 0).Local().Format(time.RFC3339))
	}
	return textResult(msg)
}

type blanketRunTaskArgs struct {
	Type        string            `json:"type" jsonschema:"task type name"`
	Env         map[string]string `json:"env,omitempty" jsonschema:"env vars for the task"`
	WaitSeconds int               `json:"waitSeconds,omitempty" jsonschema:"seconds to wait; capped by the server"`
}

// mcpRunTask is blanket_submit_task's synchronous sibling: submit, wait
// for a terminal state, and hand back state, exit code, output and the
// parsed result artifact in one tool result (turtlemonvh/blanket#27).
//
// A second tool rather than a flag on the existing one, because model
// tool selection is driven by name and description: "run this and give
// me the output" and "queue this and don't wait" are different
// intentions, and letting the model pick a tool expresses that more
// reliably than hoping it sets a boolean. blanket_submit_task's contract
// is untouched.
//
// Two deliberate differences from POST /task/?wait:
//
//   - waitSeconds over tasks.sync.maxWait is clamped, with a note in the
//     result, where the REST endpoint answers 400. A model can't cheaply
//     retry a rejected call, and the tool's own description states the
//     cap, so clamping loses nothing a caller believed.
//   - the output tails are cut to mcp.maxLogLines (via clampLogLines),
//     not tasks.sync.maxLogLines. A tool result goes straight into a
//     context window; 200 lines of each stream is a lot of it.
//
// No notBefore/cron: waiting on a task scheduled for later, or on a
// recurring template that never runs itself, is a guaranteed timeout.
// Those stay on blanket_submit_task.
func (s *ServerConfig) mcpRunTask(ctx context.Context, req *mcp.CallToolRequest, args blanketRunTaskArgs) (*mcp.CallToolResult, any, error) {
	wait := syncDefaultWait()
	clamped := false
	if args.WaitSeconds > 0 {
		wait = time.Duration(args.WaitSeconds) * time.Second
		if max := syncMaxWait(); wait > max {
			wait, clamped = max, true
		}
	}

	t, err := s.newTaskForType(args.Type, args.Env)
	if err != nil {
		return nil, nil, err
	}

	// Subscribe before enqueueing so a fast task can't finish in the gap.
	sub := s.TaskEvents.Subscribe()
	defer s.TaskEvents.Unsubscribe(sub)

	if err := s.enqueueTask(ctx, &t); err != nil {
		return nil, nil, err
	}

	finished, outcome, canceled := s.waitForTerminalState(ctx, sub, t.Id, wait)
	if canceled {
		return nil, nil, ctx.Err()
	}

	var b strings.Builder
	if outcome == WaitOutcomeTimeout {
		fmt.Fprintf(&b, "task %s (type=%s) did not finish within %s; it is still running -- check it with blanket_tasks(id=\"%s\")\n",
			t.Id.Hex(), t.TypeId, wait, t.Id.Hex())
		fmt.Fprintf(&b, "state: %s\n", finished.State)
		return textResult(b.String())
	}

	payload := s.buildCompletionPayload(finished, outcome)
	logLines := clampLogLines(0)

	fmt.Fprintf(&b, "id: %s\n", payload.Task.Id.Hex())
	fmt.Fprintf(&b, "type: %s\n", payload.Task.TypeId)
	fmt.Fprintf(&b, "state: %s\n", payload.Task.State)
	if payload.Task.ExitCode != nil {
		fmt.Fprintf(&b, "exitCode: %d\n", *payload.Task.ExitCode)
	} else {
		fmt.Fprintf(&b, "exitCode: unknown (killed by a signal, or never started)\n")
	}
	if clamped {
		fmt.Fprintf(&b, "note: waitSeconds was clamped to the server's tasks.sync.maxWait of %s\n", wait)
	}
	if payload.Result != nil {
		if encoded, err := json.Marshal(payload.Result); err == nil {
			fmt.Fprintf(&b, "result: %s\n", string(encoded))
		}
	}
	if payload.ResultError != nil {
		fmt.Fprintf(&b, "resultError: %s\n", *payload.ResultError)
	}
	fmt.Fprintf(&b, "\n--- stdout (last %d lines) ---\n%s", logLines, lastNLines(payload.Stdout, logLines))
	fmt.Fprintf(&b, "\n--- stderr (last %d lines) ---\n%s", logLines, lastNLines(payload.Stderr, logLines))
	return textResult(b.String())
}

// lastNLines keeps the final n lines of an already-tailed block of
// output. The completion payload is cut to tasks.sync.maxLogLines, which
// is a server-side sanity bound; this is the tighter context-window
// bound an MCP result wants on top of it.
func lastNLines(s string, n int) string {
	if s == "" || n <= 0 {
		return s
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n") + "\n"
}

type blanketLaunchWorkerArgs struct {
	Tags  []string `json:"tags" jsonschema:"tags the worker can claim tasks for"`
	Count int      `json:"count,omitempty" jsonschema:"workers to launch, default 1"`
}

func (s *ServerConfig) mcpLaunchWorker(ctx context.Context, req *mcp.CallToolRequest, args blanketLaunchWorkerArgs) (*mcp.CallToolResult, any, error) {
	if len(args.Tags) == 0 {
		return nil, nil, fmt.Errorf("tags is required")
	}
	count := args.Count
	if count <= 0 {
		count = 1
	}

	var b strings.Builder
	for i := 0; i < count; i++ {
		w := &worker.WorkerConf{Tags: args.Tags}
		registered, err := s.launchWorkerAndWait(ctx, w)
		if err != nil {
			return nil, nil, err
		}
		fmt.Fprintf(&b, "launched worker %s (pid=%d, tags=%s)\n", registered.Id.Hex(), registered.Pid, strings.Join(registered.Tags, ","))
	}
	return textResult(b.String())
}

type blanketCancelTaskArgs struct {
	Id     string `json:"id" jsonschema:"task id"`
	Force  bool   `json:"force,omitempty" jsonschema:"required to stop RUNNING"`
	Delete bool   `json:"delete,omitempty" jsonschema:"also delete the task + result dir"`
}

func (s *ServerConfig) mcpCancelTask(ctx context.Context, req *mcp.CallToolRequest, args blanketCancelTaskArgs) (*mcp.CallToolResult, any, error) {
	if !objectid.IsObjectIdHex(args.Id) {
		return nil, nil, fmt.Errorf("%q is not a valid task id", args.Id)
	}
	taskId := objectid.ObjectIdHex(args.Id)

	cancelErr := s.cancelTaskById(ctx, taskId, args.Force)
	wasCanceled := cancelErr == nil
	if cancelErr != nil && !(args.Delete && errors.Is(cancelErr, ErrTaskNotCancelable)) {
		return nil, nil, cancelErr
	}

	var msg string
	if wasCanceled {
		msg = fmt.Sprintf("canceled task %s", taskId.Hex())
	} else {
		msg = fmt.Sprintf("task %s was already in a terminal state, not canceled", taskId.Hex())
	}
	if args.Delete {
		if err := s.removeTaskById(ctx, taskId); err != nil {
			return nil, nil, err
		}
		msg += " and deleted it"
	}
	return textResult(msg)
}

type blanketStopWorkerArgs struct {
	Id     string `json:"id" jsonschema:"worker id"`
	Delete bool   `json:"delete,omitempty" jsonschema:"also delete the worker record"`
}

func (s *ServerConfig) mcpStopWorker(ctx context.Context, req *mcp.CallToolRequest, args blanketStopWorkerArgs) (*mcp.CallToolResult, any, error) {
	if !objectid.IsObjectIdHex(args.Id) {
		return nil, nil, fmt.Errorf("%q is not a valid worker id", args.Id)
	}
	workerId := objectid.ObjectIdHex(args.Id)

	// force=false: the MCP tool schema is already near its context budget
	// (see TestToolListFitsContextBudget), so the force option is exposed
	// over the HTTP API (PUT /worker/:id/stop?force=true) but not here.
	if err := s.stopWorkerById(ctx, workerId, false); err != nil {
		return nil, nil, err
	}

	msg := fmt.Sprintf("stopped worker %s", workerId.Hex())
	if args.Delete {
		if err := s.deleteWorkerById(ctx, workerId); err != nil {
			return nil, nil, err
		}
		msg += " and deleted it"
	}
	return textResult(msg)
}
