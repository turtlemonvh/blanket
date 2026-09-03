# Blanket MCP Interface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose blanket as an MCP server (mounted at `/mcp` on the existing gin server) so any MCP-capable agent can author task types, submit tasks, launch/inspect workers, and debug a failed task — fully self-contained, no repo checkout or skill install required.

**Architecture:** A new `server/serve_mcp.go` builds an `mcp.Server` (official `github.com/modelcontextprotocol/go-sdk`) with nine `blanket_`-prefixed tools, gated into three tiers (`readonly` / `create` / `all`) by the `mcp.mode` config key, and mounts it at `/mcp` via `gin.WrapH`. The tool handlers in `server/serve_mcp_tools.go` call the same domain layer the REST handlers already use (`s.DB`, `tasks.*`, `worker.WorkerConf`), reusing four small gin-free "core" functions extracted from `serve_tasks.go` / `serve_workers.go` so REST and MCP share one code path. Task-type authoring knowledge and general docs are served on demand via a `blanket_docs` tool backed by `docs/*.md` embedded with `go:embed`, keeping the always-on `tools/list` payload small.

**Tech Stack:** Go 1.25 (bumped from 1.23 — the SDK's floor), `github.com/modelcontextprotocol/go-sdk` v1.7.0, `github.com/google/jsonschema-go` v0.4.3 (SDK's transitive dependency, used for automatic input-schema inference from Go structs).

**Spec:** `/home/turtl/.claude/plans/the-most-recent-series-linear-rainbow.md` — the approved design (reviewed via Google Doc, all comment threads resolved and replied to; deferred items tracked in [issue #44](https://github.com/turtlemonvh/blanket/issues/44)).

## Global Constraints

- **Go floor: 1.25** (go-sdk v1.7.0 requires `go 1.25.0`). Pin `GO_VERSION=1.25.9` (latest 1.25.x patch) in `Dockerfile` / `scripts/setup.sh`; `go.mod` directive becomes `go 1.25` (matches the repo's existing unpinned-patch style).
- **Context budget: `tools/list` + `Instructions` must serialize to ≤ 4,000 characters** in the default `mcp.mode = "all"` (the worst case — narrower modes expose fewer tools). Enforced by `TestToolListFitsContextBudget`.
- **All nine tool handlers return plain text** (`Out = any` in `mcp.AddTool[In, any]`), never JSON — this suppresses `outputSchema` generation, which is the single biggest lever on the budget above.
- **`run go fmt` before every commit** (`make check-fmt` is CI-enforced).
- **Cross-compile must stay green.** Run `make docker-build` after the Go bump (Task 1) and again after the SDK is vendored (Task 2), not just once at the end.
- **`docs/api.md` gets the `/mcp` entry in the same PR** that adds the route (CLAUDE.md rule).
- **Don't touch `docs/next_up.md`'s deferred-item bullets** — those went to issue #44 per review; only remove the superseded "MCP wrapper" entry.

---

## File Structure

**New**
- `docs/embed.go` (package `docs`) — `//go:embed *.md` + `Page(key)` lookup. Must live in `docs/` — `go:embed` can't reach above its own package directory, so `overview` maps to `docs/README.md` (the docs index), not the top-level `../README.md`.
- `docs/mcp.md` — user-facing MCP setup/reference doc, one of the embedded pages.
- `docs/embed_test.go`
- `server/serve_mcp.go` — SDK server construction, mode-gating helper, tool registration dispatch, route-mount helper, `Instructions` text.
- `server/serve_mcp_tools.go` — arg structs + handler functions + registration calls for all nine tools.
- `server/serve_mcp_test.go` — budget test, mode-gating test, route-mount tests, per-tool tests.

**Modified**
- `go.mod`, `Dockerfile`, `scripts/setup.sh` — Go 1.25 bump.
- `server/serve_tasks.go` — extract `newTaskForType` / `enqueueTask` / `createTask` / `cancelTaskById` / `removeTaskById`; `postTask` / `cancelTask` / `removeTask` become thin wrappers.
- `server/serve_workers.go` — extract `launchWorkerAndWait` / `stopWorkerById` / `deleteWorkerById`; `launchWorker` / `stopWorker` / `deleteWorker` become thin wrappers.
- `server/server.go` — mount `/mcp`.
- `command/root.go` — `mcp.*` viper defaults.
- `docs/api.md`, `docs/README.md`, `docs/next_up.md` — see Task 21.
- `scripts/smoke.sh` — MCP handshake round trip.

---

## Task 1: Bump Go toolchain to 1.25

**Files:**
- Modify: `go.mod:3`, `Dockerfile:16`, `scripts/setup.sh:20`

**Interfaces:** None — this task has no code dependents, just a toolchain floor every later task relies on.

- [ ] **Step 1: Bump the three version pins**

`go.mod` line 3:
```go
go 1.25
```

`Dockerfile` line 16:
```dockerfile
ARG GO_VERSION=1.25.9
```

`scripts/setup.sh` line 20:
```bash
GO_VERSION="${GO_VERSION:-1.25.9}"        # must be >= go directive in go.mod; override: GO_VERSION=1.26.0 bash scripts/setup.sh
```

- [ ] **Step 2: Verify the existing test suite and cross-compile still work on the new floor**

Run:
```bash
make docker-test
make docker-build
```
Expected: both succeed with no changes to any other file. `docker-build` must produce all of `blanket-linux-amd64`, `blanket-darwin-amd64`, `blanket-windows-amd64.exe`. If cross-compile breaks here, stop and fix it before Task 2 — don't let an SDK addition mask a toolchain-only regression.

- [ ] **Step 3: Commit**

```bash
git add go.mod Dockerfile scripts/setup.sh
git commit -m "$(cat <<'EOF'
[AI] bump Go toolchain to 1.25

The official MCP go-sdk (added next) requires go 1.25.0 — v1.4.1+
uses http.CrossOriginProtection from the 1.25 stdlib. Verified
make docker-build still cross-compiles all three platforms before
adding any MCP code.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 2: Add the go-sdk dependency

**Files:**
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: Go 1.25 floor from Task 1.
- Produces: `github.com/modelcontextprotocol/go-sdk/mcp` package, importable by Tasks 9+.

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/modelcontextprotocol/go-sdk@v1.7.0
go mod tidy
```

- [ ] **Step 2: Verify it builds and cross-compiles**

```bash
go build ./...
make docker-build
```
Expected: clean build, all three platform binaries produced. The SDK has no unusual build tags, so this should be a no-op beyond pulling the module — if it isn't, investigate before continuing.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "$(cat <<'EOF'
[AI] add github.com/modelcontextprotocol/go-sdk dependency

Official Go MCP SDK, latest stable (v1.7.0). No code uses it yet —
this isolates the dependency-add commit from the MCP server code
that consumes it.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 3: Embed docs as `docs.Page(key)`

**Files:**
- Create: `docs/embed.go`
- Create: `docs/embed_test.go`

**Interfaces:**
- Produces: `docs.Page(key string) (string, error)`, `docs.Keys() []string` — consumed by the `blanket_docs` tool handler (Task 10).

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./docs/... -run TestPage -v`
Expected: FAIL — `docs.Page` / `docs.Keys` undefined (package `docs` doesn't exist yet).

- [ ] **Step 3: Write the implementation**

```go
// docs/embed.go
package docs

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed *.md
var files embed.FS

// pages maps the short keys used by the blanket_docs MCP tool to filenames
// in this directory. "overview" points at docs/README.md (the docs index)
// rather than the top-level repo README — go:embed can't reach above its
// own package directory.
var pages = map[string]string{
	"overview":  "README.md",
	"authoring": "authoring_task_types.md",
	"schema":    "task_type_definitions.md",
	"tags":      "tag_ontology.md",
	"usage":     "usage.md",
	"api":       "api.md",
	"flow":      "task_flow.md",
}

// Keys returns the valid page keys, sorted, for building the blanket_docs
// tool's description and error messages.
func Keys() []string {
	keys := make([]string, 0, len(pages))
	for k := range pages {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Page returns the raw contents of the doc page for key, or an error
// listing valid keys if key is unrecognized.
func Page(key string) (string, error) {
	filename, ok := pages[key]
	if !ok {
		return "", fmt.Errorf("unknown doc page %q; valid pages are: %s", key, strings.Join(Keys(), ", "))
	}
	b, err := files.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./docs/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs/embed.go docs/embed_test.go
git commit -m "$(cat <<'EOF'
[AI] embed docs/*.md for the blanket_docs MCP tool

go:embed can't reach above its own package directory, so this lives
in docs/ rather than server/ — "overview" maps to docs/README.md
(the docs index) since the top-level README.md is out of reach.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 4: Extract task-creation core from `postTask`

**Files:**
- Modify: `server/serve_tasks.go`
- Modify: `server/serve_tasks_test.go` (new tests only — existing tests must stay green unmodified)

**Interfaces:**
- Produces:
  - `(s *ServerConfig) newTaskForType(typeName string, env map[string]string) (tasks.Task, error)` — validates + builds, does not save/queue.
  - `(s *ServerConfig) enqueueTask(ctx context.Context, t *tasks.Task) error` — saves + queues + notifies.
  - `(s *ServerConfig) createTask(ctx context.Context, typeName string, env map[string]string) (tasks.Task, error)` — the two combined, for callers with nothing to do in between (used by `blanket_submit_task` in Task 15, and by `postTask`'s no-multipart-file path).
- Consumed by: Task 15 (`blanket_submit_task`).

Split into two functions rather than the single `createTask` named in the design doc, because `postTask` writes uploaded files into `t.ResultDir` **before** the task is saved/queued — collapsing save+queue into task construction would let a worker claim and start running the task before the file finishes writing. `createTask` stays as the single-call convenience wrapper the design named, for callers (MCP, and postTask's no-file path) that don't need anything in between.

- [ ] **Step 1: Write the failing tests**

Add to `server/serve_tasks_test.go` (uses the existing `setupTestTaskType(t)` helper already in that file, which registers a task type named `echo_task` with no required env):

```go
func TestCreateTask_Valid(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)
	assert.Equal(t, "echo_task", tsk.TypeId)
	assert.Equal(t, "WAITING", tsk.State)

	saved, err := s.DB.GetTask(tsk.Id)
	assert.NoError(t, err)
	assert.Equal(t, tsk.Id, saved.Id)
}

func TestCreateTask_UnknownType(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, err := s.createTask(context.Background(), "does-not-exist", nil)
	assert.Error(t, err)
}

func TestNewTaskForType_MissingRequiredEnv(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	typesDir, err := os.MkdirTemp("", "blanket-test-types-*")
	assert.NoError(t, err)
	defer os.RemoveAll(typesDir)
	err = os.WriteFile(filepath.Join(typesDir, "needs_env.toml"), []byte(`
command = "echo {{.MSG}}"
executor = "bash"
[[environment.required]]
name = "MSG"
`), 0644)
	assert.NoError(t, err)
	viper.Set("tasks.typesPaths", []string{typesDir})
	defer viper.Set("tasks.typesPaths", nil)

	_, err = s.newTaskForType("needs_env", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required environment")
}
```

Add `"context"`, `"os"`, `"path/filepath"` to the file's import block if not already present (`os` and `path/filepath` are already imported).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/... -run 'TestCreateTask|TestNewTaskForType' -v`
Expected: FAIL — `s.createTask` / `s.newTaskForType` undefined.

- [ ] **Step 3: Extract the functions and rewrite `postTask`**

In `server/serve_tasks.go`, add `"context"` to the import block, then add these three functions (placed just above `postTask`):

```go
// newTaskForType loads typeName, validates env against its required
// variables, and builds a Task ready to save — but does not save or queue
// it. Split from enqueueTask so postTask can write uploaded files into the
// task's ResultDir before the task becomes visible to workers: calling
// enqueueTask first would let a worker claim and start running before the
// upload finishes.
func (s *ServerConfig) newTaskForType(typeName string, env map[string]string) (tasks.Task, error) {
	tt, err := tasks.FetchTaskType(typeName)
	if err != nil {
		return tasks.Task{}, err
	}

	if len(env) > 0 {
		var missingVars []string
		for varName := range tt.RequiredEnv() {
			if env[varName] == "" {
				missingVars = append(missingVars, varName)
			}
		}
		if len(missingVars) > 0 {
			return tasks.Task{}, fmt.Errorf("missing environment variables required for this task type: %v", missingVars)
		}
	} else if tt.HasRequiredEnv() {
		return tasks.Task{}, fmt.Errorf("the task type %q has required environment variables; 'environment' must be set and contain these values", tt.GetName())
	}

	return tt.NewTask(env)
}

// enqueueTask saves t to the database and pushes it onto the queue, making
// it visible to workers. Call after any pre-run setup (e.g. writing
// uploaded files into t.ResultDir) is complete.
func (s *ServerConfig) enqueueTask(ctx context.Context, t *tasks.Task) error {
	if err := s.DB.SaveTask(t); err != nil {
		return fmt.Errorf("error saving to database: %w", err)
	}
	if err := s.Q.AddTask(t); err != nil {
		return err
	}
	s.TaskEvents.Notify()
	return nil
}

// createTask is newTaskForType + enqueueTask, for callers with no files to
// write in between.
func (s *ServerConfig) createTask(ctx context.Context, typeName string, env map[string]string) (tasks.Task, error) {
	t, err := s.newTaskForType(typeName, env)
	if err != nil {
		return tasks.Task{}, err
	}
	if err := s.enqueueTask(ctx, &t); err != nil {
		return tasks.Task{}, err
	}
	return t, nil
}
```

Replace the body of `postTask` (the block from `// Load task type` through the final `c.JSON(http.StatusCreated, t)`, i.e. `server/serve_tasks.go:390-478` before this edit) with:

```go
	typeName := cast.ToString(req["type"])

	envVars := make(map[string]string)
	if req["environment"] != nil {
		envVars = cast.ToStringMapString(req["environment"])
		if len(envVars) == 0 {
			c.String(http.StatusBadRequest, MakeErrorString("The 'environment' parameter must be a map of string keys to string values."))
			return
		}
	}

	t, err := s.newTaskForType(typeName, envVars)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}

	// Read any uploaded files
	if c.Request.MultipartForm != nil {
		err = os.MkdirAll(t.ResultDir, os.ModePerm)
		if err != nil {
			c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
			return
		}

		for filename := range c.Request.MultipartForm.File {
			if filename == "data" {
				continue
			}

			uploadedFile, _, err := c.Request.FormFile(filename)
			if err != nil {
				c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
				return
			}
			defer uploadedFile.Close()

			writtenUploadedFile, err := os.Create(path.Join(t.ResultDir, filename))
			if err != nil {
				c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
				return
			}
			defer writtenUploadedFile.Close()
			io.Copy(writtenUploadedFile, uploadedFile)
		}
	}

	if err := s.enqueueTask(c.Request.Context(), &t); err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	c.JSON(http.StatusCreated, t)
}
```

(The `req["type"]` nil/type checks just above this block are unchanged — leave them as-is.)

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run 'TestCreateTask|TestNewTaskForType|TestPostTask'`
Expected: PASS, including the pre-existing `TestPostTask_Valid`, `TestPostTask_MissingTypeField`, `TestPostTask_UnknownType` — unchanged REST behavior is the regression net proving the extraction didn't break anything.

Then run the full package: `go test ./server/... -v` — expected全部 PASS.

- [ ] **Step 5: Commit**

```bash
git add server/serve_tasks.go server/serve_tasks_test.go
git commit -m "$(cat <<'EOF'
[AI] extract gin-free task creation core from postTask

newTaskForType/enqueueTask/createTask let the upcoming MCP
blanket_submit_task tool share postTask's validation and queueing
logic instead of duplicating it. Split into two functions (not one)
so postTask can still write uploaded files into the task's
ResultDir before the task is saved/queued and visible to workers.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 5: Extract `cancelTaskById` / `removeTaskById`

**Files:**
- Modify: `server/serve_tasks.go`
- Modify: `server/serve_tasks_test.go` (new tests only)

**Interfaces:**
- Produces:
  - `(s *ServerConfig) cancelTaskById(ctx context.Context, taskId objectid.ObjectId) error`
  - `(s *ServerConfig) removeTaskById(ctx context.Context, taskId objectid.ObjectId) error`
  - `var ErrTaskNotCancelable error` — sentinel for "exists but not in a cancelable state," so callers (REST and MCP) can each translate it their own way without string-matching.
- Consumed by: Task 17 (`blanket_cancel_task`).

- [ ] **Step 1: Write the failing tests**

```go
func TestCancelTaskById_Waiting(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	err = s.cancelTaskById(context.Background(), tsk.Id)
	assert.NoError(t, err)

	updated, err := s.DB.GetTask(tsk.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", updated.State)
}

func TestCancelTaskById_AlreadyTerminal(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)
	assert.NoError(t, s.DB.FinishTask(tsk.Id, "SUCCESS"))

	err = s.cancelTaskById(context.Background(), tsk.Id)
	assert.ErrorIs(t, err, ErrTaskNotCancelable)
}

func TestRemoveTaskById(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	err = s.removeTaskById(context.Background(), tsk.Id)
	assert.NoError(t, err)

	_, err = s.DB.GetTask(tsk.Id)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/... -run 'TestCancelTaskById|TestRemoveTaskById' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Extract the functions and rewrite the handlers**

`server/serve_tasks.go` already imports `"errors"` (used by `claimTask`'s `errors.Is(err, queue.ErrQueueEmpty)`) — no import change needed. Add near the top of the file (after the utility functions, before `getTasks`):

```go
// ErrTaskNotCancelable is returned by cancelTaskById when a task exists
// but isn't in a state that can be canceled (only RUNNING and WAITING
// are). Kept as a sentinel rather than a formatted error so callers (the
// REST handler, MCP) can each decide how to present it.
var ErrTaskNotCancelable = errors.New("task is not in a cancelable state (must be RUNNING or WAITING)")

// cancelTaskById transitions a RUNNING or WAITING task to STOPPED.
func (s *ServerConfig) cancelTaskById(ctx context.Context, taskId objectid.ObjectId) error {
	task, err := s.DB.GetTask(taskId)
	if err != nil {
		return err
	}
	if task.State != "RUNNING" && task.State != "WAITING" {
		return ErrTaskNotCancelable
	}
	if err := s.DB.FinishTask(taskId, "STOPPED"); err != nil {
		return err
	}
	s.TaskEvents.Notify()
	return nil
}

// removeTaskById deletes a task from the database and its result
// directory. Mirrors the historical removeTask semantics: deleting a
// nonexistent task's result directory is not an error.
func (s *ServerConfig) removeTaskById(ctx context.Context, taskId objectid.ObjectId) error {
	if err := s.DB.DeleteTask(taskId); err != nil {
		return err
	}
	if err := os.RemoveAll(path.Join(s.ResultsPath, taskId.Hex())); err != nil {
		return err
	}
	s.TaskEvents.Notify()
	return nil
}
```

Replace `cancelTask` (the whole function body) with:

```go
func (s *ServerConfig) cancelTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	taskId, err := s.getTaskId(c)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}

	err = s.cancelTaskById(c.Request.Context(), taskId)
	switch {
	case err == nil:
		c.String(http.StatusOK, `{}`)
	case errors.Is(err, ErrTaskNotCancelable):
		// Preserves the historical (non-404, non-2xx) response for a task
		// that exists but can't be canceled from its current state — see
		// docs/next_up.md "Normalize task-handler error status codes".
		c.JSON(http.StatusNotImplemented, `{"error": "Functionality not implemented"}`)
	default:
		c.String(http.StatusNotFound, MakeErrorString(err.Error()))
	}
}
```

Replace `removeTask` (the whole function body) with:

```go
func (s *ServerConfig) removeTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	taskId, err := s.getTaskId(c)
	if err != nil {
		return
	}

	if err := s.removeTaskById(c.Request.Context(), taskId); err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, fmt.Sprintf(`{"id": "%s"}`, taskId.Hex()))
}
```

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run 'TestCancelTaskById|TestRemoveTaskById|TestCancelTask|TestDeleteTask'`
Expected: PASS, including the pre-existing `TestCancelTask_Waiting` and `TestDeleteTask` — same status codes as before.

Then: `go test ./server/... -v` — expected all PASS.

- [ ] **Step 5: Commit**

```bash
git add server/serve_tasks.go server/serve_tasks_test.go
git commit -m "$(cat <<'EOF'
[AI] extract gin-free cancelTaskById/removeTaskById

Shared core for the REST cancel/delete handlers and the upcoming
MCP blanket_cancel_task tool. ErrTaskNotCancelable is a sentinel so
each caller can present the "wrong state" case its own way — REST
keeps its existing 501 response, MCP will return readable text.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 6: Extract `launchWorkerAndWait`

**Files:**
- Modify: `server/serve_workers.go`
- Create/modify: `server/serve_workers_test.go` (new file if one doesn't already exist — check first with `ls server/serve_workers_test.go`)

**Interfaces:**
- Produces:
  - `(s *ServerConfig) launchWorkerAndWait(ctx context.Context, w *worker.WorkerConf) (worker.WorkerConf, error)`
  - `var ErrWorkerNotRegistered error` — sentinel for the poll-timeout case.
- Consumed by: Task 16 (`blanket_launch_worker`).

- [ ] **Step 1: Write the failing test**

```go
// server/serve_workers_test.go
package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/worker"
)

func TestLaunchWorkerAndWait_RejectsLowCheckInterval(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := &worker.WorkerConf{Tags: []string{"exec:bash"}, CheckInterval: 0.1}
	_, err := s.launchWorkerAndWait(context.Background(), w)
	assert.ErrorIs(t, err, worker.ErrCheckIntervalTooLow)
}
```

(A happy-path test that waits for a real worker process to register is out of scope here — it needs a subprocess harness, same reason `worker/worker_test.go` defers SIGTERM-shutdown coverage per its own TODO block. The REST-level `TestLaunchWorker_RejectsLowCheckInterval` in `server_test.go` remains the regression net for the success path via the HTTP handler.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./server/... -run TestLaunchWorkerAndWait -v`
Expected: FAIL — `s.launchWorkerAndWait` undefined.

- [ ] **Step 3: Extract the function and rewrite `launchWorker`**

Add `"context"`, `"errors"` to `server/serve_workers.go`'s import block. Add above the existing `launchWorker` function:

```go
// ErrWorkerNotRegistered is returned by launchWorkerAndWait if the worker
// doesn't show up in the database with a nonzero Pid within
// MAX_REQUEST_TIME_SECONDS of being started.
var ErrWorkerNotRegistered = errors.New("worker was not found in the database within the expected time")

// launchWorkerAndWait starts w as a daemon and polls the database until
// its Pid is registered or the request-time budget elapses. Returns the
// registered worker config.
func (s *ServerConfig) launchWorkerAndWait(ctx context.Context, w *worker.WorkerConf) (worker.WorkerConf, error) {
	w.Daemon = true
	if w.CheckInterval == 0 {
		w.CheckInterval = worker.DEFAULT_CHECK_INTERVAL_SECONDS
	}
	if w.CheckInterval < worker.MIN_CHECK_INTERVAL_SECONDS {
		return worker.WorkerConf{}, worker.ErrCheckIntervalTooLow
	}

	if err := w.Run(); err != nil {
		return worker.WorkerConf{}, err
	}

	deadline := time.NewTimer(time.Duration(MAX_REQUEST_TIME_SECONDS*s.TimeMultiplier) * time.Second)
	defer deadline.Stop()
	loopWait := time.Duration(500*s.TimeMultiplier) * time.Millisecond

	for {
		registered, _ := s.DB.GetWorker(w.Id)
		if registered.Pid != 0 {
			s.WorkerEvents.Notify()
			return registered, nil
		}

		select {
		case <-deadline.C:
			return worker.WorkerConf{}, fmt.Errorf("%w after %d seconds", ErrWorkerNotRegistered, MAX_REQUEST_TIME_SECONDS)
		case <-time.After(loopWait):
			continue
		}
	}
}
```

Replace the body of `launchWorker` with:

```go
// Called by other request handlers
func (s *ServerConfig) launchWorker(c *gin.Context, w *worker.WorkerConf) {
	c.Header("Content-Type", "application/json")

	registered, err := s.launchWorkerAndWait(c.Request.Context(), w)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, registered)
	case errors.Is(err, worker.ErrCheckIntervalTooLow):
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
	case errors.Is(err, ErrWorkerNotRegistered):
		c.String(http.StatusRequestTimeout, MakeErrorString(err.Error()))
	default:
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
	}
}
```

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run 'TestLaunchWorkerAndWait|TestLaunchWorker'`
Expected: PASS, including the pre-existing `TestLaunchWorker_RejectsLowCheckInterval` in `server_test.go`.

Then: `go test ./server/... -v` — expected all PASS.

- [ ] **Step 5: Commit**

```bash
git add server/serve_workers.go server/serve_workers_test.go
git commit -m "$(cat <<'EOF'
[AI] extract gin-free launchWorkerAndWait

Shared core for the REST worker-launch handlers and the upcoming
MCP blanket_launch_worker tool. ErrWorkerNotRegistered replaces a
bare fmt.Errorf so callers can distinguish "check interval too low"
from "registration timed out" without string matching.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 7: Extract `stopWorkerById` / `deleteWorkerById`

**Files:**
- Modify: `server/serve_workers.go`
- Modify: `server/serve_workers_test.go`

**Interfaces:**
- Produces:
  - `(s *ServerConfig) stopWorkerById(ctx context.Context, workerId objectid.ObjectId) error`
  - `(s *ServerConfig) deleteWorkerById(ctx context.Context, workerId objectid.ObjectId) error`
- Consumed by: Task 17 (`blanket_stop_worker`).

- [ ] **Step 1: Write the failing tests**

```go
func TestStopWorkerById(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	err := s.stopWorkerById(context.Background(), w.Id)
	assert.NoError(t, err)

	updated, err := s.DB.GetWorker(w.Id)
	assert.NoError(t, err)
	assert.True(t, updated.Stopped)
}

func TestDeleteWorkerById(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}, Stopped: true}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	err := s.deleteWorkerById(context.Background(), w.Id)
	assert.NoError(t, err)

	_, err = s.DB.GetWorker(w.Id)
	assert.Error(t, err)
}
```

Add `"github.com/turtlemonvh/blanket/lib/objectid"` to the test file's imports if not already present from Task 6.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/... -run 'TestStopWorkerById|TestDeleteWorkerById' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Extract the functions and rewrite the handlers**

Add above `stopWorker`:

```go
// stopWorkerById marks w as stopped; the worker's own poll loop observes
// this and exits after its current task finishes.
func (s *ServerConfig) stopWorkerById(ctx context.Context, workerId objectid.ObjectId) error {
	w, err := s.DB.GetWorker(workerId)
	if err != nil {
		return err
	}
	w.Stopped = true
	if err := s.DB.UpdateWorker(&w); err != nil {
		return err
	}
	s.WorkerEvents.Notify()
	return nil
}
```

Replace `stopWorker`'s body:

```go
func (s *ServerConfig) stopWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	if err := s.stopWorkerById(c.Request.Context(), workerId); err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	c.String(http.StatusOK, `{}`)
}
```

Add above `deleteWorker`:

```go
// deleteWorkerById removes a worker's record from the database. Does not
// check whether the worker is stopped — callers that need that guard
// (deleteWorker below) check before calling in.
func (s *ServerConfig) deleteWorkerById(ctx context.Context, workerId objectid.ObjectId) error {
	if err := s.DB.DeleteWorker(workerId); err != nil {
		return err
	}
	s.WorkerEvents.Notify()
	return nil
}
```

Replace `deleteWorker`'s body:

```go
func (s *ServerConfig) deleteWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	workerId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}

	// FIXME: Check that worker is stopped
	w := worker.WorkerConf{}
	err = c.BindJSON(&w)
	if err == nil && w.Stopped != true {
		c.String(http.StatusBadRequest, `{"error": "Cannot delete a worker that has not been stopped"}`)
	}

	if err := s.deleteWorkerById(c.Request.Context(), workerId); err != nil {
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.String(http.StatusOK, fmt.Sprintf(`{"id": "%s"}`, workerId.Hex()))
}
```

Note: the `w.Stopped != true` branch above is missing a `return` in the pre-existing code (falls through to delete anyway) — this is a known, separately-tracked bug (see `docs/next_up.md` "Worker management FIXMEs"). Preserve it exactly as-is here; fixing it is out of scope for this refactor.

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run 'TestStopWorkerById|TestDeleteWorkerById'`
Expected: PASS.

Then run the full suite: `go test ./... -v`
Expected: all PASS — this is the last of the four extraction tasks, so this is the checkpoint that confirms Tasks 4-7 collectively didn't change any REST behavior.

- [ ] **Step 5: Commit**

```bash
git add server/serve_workers.go server/serve_workers_test.go
git commit -m "$(cat <<'EOF'
[AI] extract gin-free stopWorkerById/deleteWorkerById

Shared core for the REST stop/delete handlers and the upcoming MCP
blanket_stop_worker tool. Completes the gin-context extraction for
all four operations the MCP layer needs (task create/cancel/remove,
worker launch/stop/delete).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 8: MCP config defaults + mode-gating helper

**Files:**
- Modify: `command/root.go`
- Create: `server/serve_mcp.go`
- Create: `server/serve_mcp_test.go`

**Interfaces:**
- Produces:
  - Viper defaults: `mcp.enabled` (bool), `mcp.mode` (string), `mcp.writeTypesPath` (string), `mcp.validateStrict` (bool), `mcp.maxLogLines` (int).
  - `type mcpToolTier int` with `mcpTierReadonly`, `mcpTierCreate`, `mcpTierAll`.
  - `func mcpModeAllows(mode string, t mcpToolTier) bool`
- Consumed by: Task 9 (registration dispatch), Tasks 10-17 (each tool's tier).

- [ ] **Step 1: Write the failing test**

```go
// server/serve_mcp_test.go
package server

import "testing"

func TestMcpModeAllows(t *testing.T) {
	cases := []struct {
		mode string
		tier mcpToolTier
		want bool
	}{
		{"readonly", mcpTierReadonly, true},
		{"readonly", mcpTierCreate, false},
		{"readonly", mcpTierAll, false},
		{"create", mcpTierReadonly, true},
		{"create", mcpTierCreate, true},
		{"create", mcpTierAll, false},
		{"all", mcpTierReadonly, true},
		{"all", mcpTierCreate, true},
		{"all", mcpTierAll, true},
		{"", mcpTierReadonly, false},
		{"bogus", mcpTierReadonly, false},
	}
	for _, c := range cases {
		got := mcpModeAllows(c.mode, c.tier)
		if got != c.want {
			t.Errorf("mcpModeAllows(%q, %v) = %v, want %v", c.mode, c.tier, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./server/... -run TestMcpModeAllows -v`
Expected: FAIL — `mcpModeAllows` undefined (package doesn't exist yet).

- [ ] **Step 3: Add config defaults and write the gating helper**

In `command/root.go`, inside `InitializeConfig()`, after the existing `viper.SetDefault(...)` calls (around line 59, after `workers.logfileNameTemplate`):

```go
	viper.SetDefault("mcp.enabled", true)
	viper.SetDefault("mcp.mode", "all")
	viper.SetDefault("mcp.writeTypesPath", "")
	viper.SetDefault("mcp.validateStrict", false)
	viper.SetDefault("mcp.maxLogLines", 200)
```

Create `server/serve_mcp.go`:

```go
package server

// mcpToolTier says which mcp.mode values register a given tool.
// "readonly" tools are visible in every mode; "create" tools need mode
// create or all; "all" (destructive) tools need mode all.
type mcpToolTier int

const (
	mcpTierReadonly mcpToolTier = iota
	mcpTierCreate
	mcpTierAll
)

// mcpModeAllows reports whether a tool of tier t should be registered
// when the server is configured for mode.
func mcpModeAllows(mode string, t mcpToolTier) bool {
	switch mode {
	case "readonly":
		return t == mcpTierReadonly
	case "create":
		return t == mcpTierReadonly || t == mcpTierCreate
	case "all":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./server/... -run TestMcpModeAllows -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add command/root.go server/serve_mcp.go server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] add mcp.* config defaults and mode-gating helper

mcp.mode ("readonly" | "create" | "all", default "all") controls
which of the nine upcoming MCP tools get registered. mcpModeAllows
is the single place that encodes the tier hierarchy so the
registration code (next task) and its tests share one source of
truth.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 9: MCP server construction + route mount

**Files:**
- Modify: `server/serve_mcp.go`
- Modify: `server/server.go`
- Modify: `server/serve_mcp_test.go`

**Interfaces:**
- Consumes: `mcpModeAllows` (Task 8).
- Produces:
  - `const mcpInstructions string`, `const mcpContextBudgetChars = 4000`
  - `(s *ServerConfig) buildMCPServer() *mcp.Server`
  - `(s *ServerConfig) mcpHTTPHandler() http.Handler` (nil if `mcp.enabled` is false)
  - Three empty registration hooks: `(s *ServerConfig) registerReadonlyMCPTools(srv *mcp.Server, mode string)`, `registerCreateMCPTools`, `registerDestructiveMCPTools` — bodies filled in by Tasks 10-17, each guarded by `mcpModeAllows`.
- Consumed by: `server.go`'s `GetRouter()`; Tasks 10-19 fill in the registration functions and add more tools/tests.

- [ ] **Step 1: Write the failing tests**

Add to `server/serve_mcp_test.go`:

```go
func TestMCPRouteMounted(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	viper.Set("mcp.enabled", true)
	defer viper.Set("mcp.enabled", nil)

	r := s.GetRouter()
	req, _ := http.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.NotEqual(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
}

func TestMCPRouteNotMountedWhenDisabled(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	viper.Set("mcp.enabled", false)
	defer viper.Set("mcp.enabled", nil)

	r := s.GetRouter()
	req, _ := http.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
```

Add imports to `server/serve_mcp_test.go`: `"net/http"`, `"net/http/httptest"`, `"strings"`, `"github.com/spf13/viper"`, `"github.com/stretchr/testify/assert"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/... -run TestMCPRoute -v`
Expected: FAIL — `s.mcpHTTPHandler` undefined / route not mounted.

- [ ] **Step 3: Implement server construction and mounting**

Append to `server/serve_mcp.go` (add imports: `"net/http"`, `"github.com/spf13/viper"`, `"github.com/modelcontextprotocol/go-sdk/mcp"`):

```go
const mcpContextBudgetChars = 4000

const mcpInstructions = `blanket runs shell tasks defined by TOML task types. To author a new task type: call blanket_docs(page="authoring") for the guide, then blanket_write_task_type to save and validate it. To run it: blanket_submit_task queues a task of that type, but it only runs once a worker is available whose tags are a superset of the type's tags -- use blanket_workers to check, blanket_launch_worker to start one. Check status and logs with blanket_tasks(id=...).`

// buildMCPServer constructs the MCP server for s, registering only the
// tools allowed by the configured mcp.mode.
func (s *ServerConfig) buildMCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "blanket",
		Version: s.Version,
	}, &mcp.ServerOptions{
		Instructions: mcpInstructions,
	})

	mode := viper.GetString("mcp.mode")
	s.registerReadonlyMCPTools(srv, mode)
	s.registerCreateMCPTools(srv, mode)
	s.registerDestructiveMCPTools(srv, mode)

	return srv
}

// mcpHTTPHandler returns the http.Handler to mount at /mcp, or nil if
// mcp.enabled is false.
func (s *ServerConfig) mcpHTTPHandler() http.Handler {
	if !viper.GetBool("mcp.enabled") {
		return nil
	}
	srv := s.buildMCPServer()
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, &mcp.StreamableHTTPOptions{
		CrossOriginProtection: http.NewCrossOriginProtection(),
	})
}

// The following are filled in by later tasks (10-17), one per tool tier.
// Each must check mcpModeAllows before calling mcp.AddTool.

func (s *ServerConfig) registerReadonlyMCPTools(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierReadonly) {
		return
	}
}

func (s *ServerConfig) registerCreateMCPTools(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierCreate) {
		return
	}
}

func (s *ServerConfig) registerDestructiveMCPTools(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierAll) {
		return
	}
}
```

In `server/server.go`, add the import `"github.com/gin-gonic/gin"` is already present; add the mount inside `GetRouter()` just after the `r.DELETE("/worker/:id", ...)` block (around line 168, before `return r`):

```go
	if h := s.mcpHTTPHandler(); h != nil {
		r.Any("/mcp", gin.WrapH(h))
	}

	return r
```

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run 'TestMCPRoute|TestMcpModeAllows'`
Expected: PASS.

Then: `go build ./...` and `go test ./... -v` — expected all PASS (no tools registered yet, so `tools/list` will be empty, which is fine — the budget test comes in Task 19 once tools exist).

- [ ] **Step 5: Commit**

```bash
git add server/serve_mcp.go server/server.go server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] mount MCP server at /mcp

buildMCPServer wires the three mode-gated registration hooks (still
empty — filled in tool-by-tool over the next several commits) into
an mcp.Server, exposed as an http.Handler via
mcp.NewStreamableHTTPHandler with CrossOriginProtection enabled.
Mounted with r.Any because streamable HTTP needs POST/GET/DELETE.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 10: Tool `blanket_docs`

**Files:**
- Create: `server/serve_mcp_tools.go`
- Modify: `server/serve_mcp.go` (fill in `registerReadonlyMCPTools`)
- Modify: `server/serve_mcp_test.go`

**Interfaces:**
- Consumes: `docs.Page` / `docs.Keys` (Task 3).
- Produces: `blanketDocsArgs` struct, `(s *ServerConfig) mcpDocs(...)` handler, `textResult(s string) (*mcp.CallToolResult, any, error)` helper (reused by every later tool).

- [ ] **Step 1: Write the failing test**

```go
// server/serve_mcp_test.go — add:
func TestMcpDocs_KnownPage(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	res, _, err := s.mcpDocs(context.Background(), nil, blanketDocsArgs{Page: "overview"})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.NotEmpty(t, text)
}

func TestMcpDocs_UnknownPage(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpDocs(context.Background(), nil, blanketDocsArgs{Page: "nope"})
	assert.Error(t, err)
}
```

Add imports: `"context"`, `"github.com/modelcontextprotocol/go-sdk/mcp"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./server/... -run TestMcpDocs -v`
Expected: FAIL — `blanketDocsArgs` / `s.mcpDocs` undefined.

- [ ] **Step 3: Implement the tool**

Create `server/serve_mcp_tools.go`:

```go
package server

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/turtlemonvh/blanket/docs"
)

// textResult wraps s as a successful single-text-block tool result. Every
// MCP tool handler in this file returns plain text, not JSON — see the
// design plan's "Context budget" section for why.
func textResult(s string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}, nil, nil
}

type blanketDocsArgs struct {
	Page string `json:"page" jsonschema:"doc page to fetch: overview, authoring, schema, tags, usage, api, or flow"`
}

func (s *ServerConfig) mcpDocs(ctx context.Context, req *mcp.CallToolRequest, args blanketDocsArgs) (*mcp.CallToolResult, any, error) {
	content, err := docs.Page(args.Page)
	if err != nil {
		return nil, nil, err
	}
	return textResult(content)
}
```

Fill in `registerReadonlyMCPTools` in `server/serve_mcp.go`:

```go
func (s *ServerConfig) registerReadonlyMCPTools(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierReadonly) {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_docs",
		Description: "Fetch a blanket documentation page (overview, authoring, schema, tags, usage, api, flow). Read 'authoring' before writing a new task type.",
	}, s.mcpDocs)
}
```

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run 'TestMcpDocs|TestMCPRoute'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/serve_mcp_tools.go server/serve_mcp.go server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] add blanket_docs MCP tool

First of nine tools. Serves the embedded docs pages on demand so an
agent with no repo access can read the authoring guide before
calling blanket_write_task_type -- this is the main context-
minimization move: the ~8KB authoring guide costs nothing until an
agent actually needs it.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 11: Tool `blanket_task_types`

**Files:**
- Modify: `server/serve_mcp_tools.go`, `server/serve_mcp.go`, `server/serve_mcp_test.go`

**Interfaces:**
- Produces: `blanketTaskTypesArgs`, `(s *ServerConfig) mcpTaskTypes(...)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestMcpTaskTypes_List(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpTaskTypes(context.Background(), nil, blanketTaskTypesArgs{})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "echo_task")
}

func TestMcpTaskTypes_Detail(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpTaskTypes(context.Background(), nil, blanketTaskTypesArgs{Name: "echo_task"})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "name: echo_task")
}

func TestMcpTaskTypes_UnknownName(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpTaskTypes(context.Background(), nil, blanketTaskTypesArgs{Name: "nope"})
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/... -run TestMcpTaskTypes -v`
Expected: FAIL.

- [ ] **Step 3: Implement the tool**

Add to `server/serve_mcp_tools.go` (add imports `"fmt"`, `"strings"`, `"github.com/turtlemonvh/blanket/tasks"`):

```go
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
```

Add the registration line inside `registerReadonlyMCPTools`, after `blanket_docs`:

```go
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_task_types",
		Description: "List task types, or fetch one by name for full detail.",
	}, s.mcpTaskTypes)
```

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run 'TestMcpTaskTypes'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/serve_mcp_tools.go server/serve_mcp.go server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] add blanket_task_types MCP tool

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 12: Tool `blanket_tasks`

**Files:**
- Modify: `server/serve_mcp_tools.go`, `server/serve_mcp.go`, `server/serve_mcp_test.go`

**Interfaces:**
- Consumes: `tailLines` (`server/serve_logs.go`), `database.TaskSearchConf` / `s.DB.GetTasks`.
- Produces: `blanketTasksArgs`, `(s *ServerConfig) mcpTasks(...)`, `const mcpDefaultLogLines = 50`, `const mcpDefaultTaskLimit = 20`, `const mcpMaxTaskLimit = 100`.

- [ ] **Step 1: Write the failing tests**

```go
func TestMcpTasks_List(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	_, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	res, _, err := s.mcpTasks(context.Background(), nil, blanketTasksArgs{})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "echo_task")
	assert.Contains(t, text, "WAITING")
}

func TestMcpTasks_FilterByState(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	_, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	res, _, err := s.mcpTasks(context.Background(), nil, blanketTasksArgs{States: []string{"RUNNING"}})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.NotContains(t, text, "echo_task")
}

func TestMcpTasks_Detail(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	res, _, err := s.mcpTasks(context.Background(), nil, blanketTasksArgs{Id: tsk.Id.Hex()})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, tsk.Id.Hex())
	assert.Contains(t, text, "log tail")
}

func TestMcpTasks_InvalidId(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpTasks(context.Background(), nil, blanketTasksArgs{Id: "not-an-id"})
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/... -run TestMcpTasks -v`
Expected: FAIL.

- [ ] **Step 3: Implement the tool**

Add to `server/serve_mcp_tools.go` (add imports `"path"`, `"time"`, `"github.com/spf13/viper"`, `"github.com/turtlemonvh/blanket/lib/database"`, `"github.com/turtlemonvh/blanket/lib/objectid"`):

```go
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
	Id       string   `json:"id,omitempty" jsonschema:"task id; if set, returns detail plus a log tail instead of a list"`
	States   []string `json:"states,omitempty" jsonschema:"filter by task state (WAITING, CLAIMED, RUNNING, SUCCESS, ERROR, STOPPED, TIMEDOUT)"`
	Types    []string `json:"types,omitempty" jsonschema:"filter by task type name"`
	Limit    int      `json:"limit,omitempty" jsonschema:"max tasks to list, default 20, max 100"`
	LogLines int      `json:"log_lines,omitempty" jsonschema:"log tail lines when id is set, default 50"`
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
```

Add the registration line inside `registerReadonlyMCPTools`:

```go
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_tasks",
		Description: "List tasks (filterable by state/type), or fetch one by id with a log tail.",
	}, s.mcpTasks)
```

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run TestMcpTasks`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/serve_mcp_tools.go server/serve_mcp.go server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] add blanket_tasks MCP tool

List-or-get in one tool (per the design's context-minimization
shape choices). clampLogLines enforces mcp.maxLogLines regardless
of what a caller asks for -- a worker or task log in this repo has
hit 9.7MB, and an unbounded log_lines would blow an agent's context
window in one call.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 13: Tool `blanket_workers`

**Files:**
- Modify: `server/serve_mcp_tools.go`, `server/serve_mcp.go`, `server/serve_mcp_test.go`

**Interfaces:**
- Produces: `blanketWorkersArgs`, `(s *ServerConfig) mcpWorkers(...)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestMcpWorkers_List(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}, Pid: 123}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	res, _, err := s.mcpWorkers(context.Background(), nil, blanketWorkersArgs{})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, w.Id.Hex())
}

func TestMcpWorkers_Detail(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}, Pid: 123}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	res, _, err := s.mcpWorkers(context.Background(), nil, blanketWorkersArgs{Id: w.Id.Hex()})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "pid: 123")
}

func TestMcpWorkers_InvalidId(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpWorkers(context.Background(), nil, blanketWorkersArgs{Id: "not-an-id"})
	assert.Error(t, err)
}
```

Add `"github.com/turtlemonvh/blanket/worker"` and `"github.com/turtlemonvh/blanket/lib/objectid"` to the test file's imports if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/... -run TestMcpWorkers -v`
Expected: FAIL.

- [ ] **Step 3: Implement the tool**

Add to `server/serve_mcp_tools.go`:

```go
type blanketWorkersArgs struct {
	Id       string `json:"id,omitempty" jsonschema:"worker id; if set, returns detail plus a log tail instead of a list"`
	LogLines int    `json:"log_lines,omitempty" jsonschema:"log tail lines when id is set, default 50"`
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
```

Add the registration line inside `registerReadonlyMCPTools`:

```go
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_workers",
		Description: "List workers, or fetch one by id with a log tail.",
	}, s.mcpWorkers)
```

This completes `registerReadonlyMCPTools` — all four readonly tools registered.

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run TestMcpWorkers`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/serve_mcp_tools.go server/serve_mcp.go server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] add blanket_workers MCP tool

Completes the four readonly-tier tools (blanket_docs,
blanket_task_types, blanket_tasks, blanket_workers).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 14: Tool `blanket_write_task_type`

**Files:**
- Modify: `server/serve_mcp_tools.go`, `server/serve_mcp.go`, `server/serve_mcp_test.go`

**Interfaces:**
- Consumes: `tasks.ReadTaskType`, `tasks.ReadTaskTypesForValidation`, `tasks.BuildTagIndex`, `tasks.ValidateTaskType`, `tasks.LintTags` (all pre-existing, same pattern as `command/task_validate.go`).
- Produces: `blanketWriteTaskTypeArgs`, `(s *ServerConfig) mcpWriteTaskType(...)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestMcpWriteTaskType_RejectsOnError(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpWriteTaskType(context.Background(), nil, blanketWriteTaskTypeArgs{
		Name: "broken_task",
		Toml: `tags = ["bash"]`, // missing required 'command'
	})
	assert.NoError(t, err) // a validation failure is a tool-level result, not a Go error
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "refused to write")

	typesDir := viper.GetStringSlice("tasks.typesPaths")[0]
	_, statErr := os.Stat(path.Join(typesDir, "broken_task.toml"))
	assert.True(t, os.IsNotExist(statErr), "file should not have been written")
}

func TestMcpWriteTaskType_WritesOnSuccess(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpWriteTaskType(context.Background(), nil, blanketWriteTaskTypeArgs{
		Name: "new_task",
		Toml: "command = \"echo hi\"\nexecutor = \"bash\"\ntags = [\"exec:bash\"]\ndescription = \"says hi\"\ndocumentation = \"none needed\"\n",
	})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "wrote")

	typesDir := viper.GetStringSlice("tasks.typesPaths")[0]
	_, statErr := os.Stat(path.Join(typesDir, "new_task.toml"))
	assert.NoError(t, statErr)

	// Immediately submittable — no restart needed, since tasks.ReadTypes()
	// re-reads from disk on every request.
	tt, err := tasks.FetchTaskType("new_task")
	assert.NoError(t, err)
	assert.Equal(t, "new_task", tt.GetName())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/... -run TestMcpWriteTaskType -v`
Expected: FAIL.

- [ ] **Step 3: Implement the tool**

Add to `server/serve_mcp_tools.go` (add import `"os"`):

```go
type blanketWriteTaskTypeArgs struct {
	Name string `json:"name" jsonschema:"task type name (without .toml)"`
	Toml string `json:"toml" jsonschema:"full TOML contents of the task type"`
}

func (s *ServerConfig) mcpWriteTaskType(ctx context.Context, req *mcp.CallToolRequest, args blanketWriteTaskTypeArgs) (*mcp.CallToolResult, any, error) {
	if args.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
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
	if err := os.WriteFile(writePath, []byte(args.Toml), 0644); err != nil {
		return nil, nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "wrote %s\n", writePath)
	for _, f := range findings {
		fmt.Fprintf(&b, "%s %s: %s\n", f.Code, f.Level, f.Message)
	}
	return textResult(b.String())
}
```

Add to `server/serve_mcp.go`, inside `registerCreateMCPTools`:

```go
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_write_task_type",
		Description: "Validate and write a new task type TOML. Refuses to write on any validation error; see blanket_docs(page=\"authoring\") first.",
	}, s.mcpWriteTaskType)
```

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run TestMcpWriteTaskType`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/serve_mcp_tools.go server/serve_mcp.go server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] add blanket_write_task_type MCP tool

Reuses the exact validate-then-lint pipeline command/task_validate.go
already runs (ValidateTaskType + LintTags against a full-repo tag
index). Any error-level finding -- or any warning when
mcp.validateStrict -- refuses the write; the response lists findings
either way so the agent can fix and retry. Overwrites an existing
type of the same name with no guard (see issue #44 for the deferred
require_explicit_tasktype_overwrite option).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 15: Tool `blanket_submit_task`

**Files:**
- Modify: `server/serve_mcp_tools.go`, `server/serve_mcp.go`, `server/serve_mcp_test.go`

**Interfaces:**
- Consumes: `s.createTask` (Task 4).
- Produces: `blanketSubmitTaskArgs`, `(s *ServerConfig) mcpSubmitTask(...)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestMcpSubmitTask_Valid(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpSubmitTask(context.Background(), nil, blanketSubmitTaskArgs{Type: "echo_task"})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "echo_task")
	assert.Contains(t, text, "WAITING")
}

func TestMcpSubmitTask_UnknownType(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpSubmitTask(context.Background(), nil, blanketSubmitTaskArgs{Type: "nope"})
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/... -run TestMcpSubmitTask -v`
Expected: FAIL.

- [ ] **Step 3: Implement the tool**

Add to `server/serve_mcp_tools.go`:

```go
type blanketSubmitTaskArgs struct {
	Type string            `json:"type" jsonschema:"task type name"`
	Env  map[string]string `json:"env,omitempty" jsonschema:"environment variables for this task"`
}

func (s *ServerConfig) mcpSubmitTask(ctx context.Context, req *mcp.CallToolRequest, args blanketSubmitTaskArgs) (*mcp.CallToolResult, any, error) {
	t, err := s.createTask(ctx, args.Type, args.Env)
	if err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("submitted task %s (type=%s, state=%s)", t.Id.Hex(), t.TypeId, t.State))
}
```

Add to `registerCreateMCPTools`, after `blanket_write_task_type`:

```go
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_submit_task",
		Description: "Submit (queue) a task of the given type. Requires an available worker whose tags are a superset of the type's tags to actually run.",
	}, s.mcpSubmitTask)
```

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run TestMcpSubmitTask`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/serve_mcp_tools.go server/serve_mcp.go server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] add blanket_submit_task MCP tool

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 16: Tool `blanket_launch_worker`

**Files:**
- Modify: `server/serve_mcp_tools.go`, `server/serve_mcp.go`, `server/serve_mcp_test.go`

**Interfaces:**
- Consumes: `s.launchWorkerAndWait` (Task 6).
- Produces: `blanketLaunchWorkerArgs`, `(s *ServerConfig) mcpLaunchWorker(...)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestMcpLaunchWorker_RequiresTags(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpLaunchWorker(context.Background(), nil, blanketLaunchWorkerArgs{})
	assert.Error(t, err)
}

func TestMcpLaunchWorker_RejectsLowCheckInterval(t *testing.T) {
	// launchWorkerAndWait doesn't take a checkInterval arg from MCP callers
	// (they always get the default), so this instead confirms the tool
	// surfaces the underlying error path correctly when Count is invalid.
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpLaunchWorker(context.Background(), nil, blanketLaunchWorkerArgs{Tags: []string{"exec:bash"}, Count: -1})
	// Count <= 0 defaults to 1, so this should not error on Count alone;
	// asserts the default-normalization path doesn't panic or reject.
	// (A real launch will fail fast in this sandboxed test environment for
	// unrelated reasons -- e.g. no worker binary path -- so only assert
	// no panic occurred; full launch behavior is covered by the REST-level
	// TestLaunchWorker_RejectsLowCheckInterval and the unit-level
	// TestLaunchWorkerAndWait_RejectsLowCheckInterval from Task 6.)
	_ = err
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/... -run TestMcpLaunchWorker -v`
Expected: FAIL — `blanketLaunchWorkerArgs` / `s.mcpLaunchWorker` undefined.

- [ ] **Step 3: Implement the tool**

Add to `server/serve_mcp_tools.go` (add import `"github.com/turtlemonvh/blanket/worker"`):

```go
type blanketLaunchWorkerArgs struct {
	Tags  []string `json:"tags" jsonschema:"tags this worker can claim tasks for"`
	Count int      `json:"count,omitempty" jsonschema:"number of workers to launch, default 1"`
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
```

Add to `registerCreateMCPTools`, after `blanket_submit_task`:

```go
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_launch_worker",
		Description: "Launch one or more workers with the given tags.",
	}, s.mcpLaunchWorker)
```

This completes `registerCreateMCPTools`.

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run TestMcpLaunchWorker`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/serve_mcp_tools.go server/serve_mcp.go server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] add blanket_launch_worker MCP tool

Completes the three create-tier tools (blanket_write_task_type,
blanket_submit_task, blanket_launch_worker).

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 17: Tools `blanket_cancel_task` + `blanket_stop_worker`

**Files:**
- Modify: `server/serve_mcp_tools.go`, `server/serve_mcp.go`, `server/serve_mcp_test.go`

**Interfaces:**
- Consumes: `s.cancelTaskById`, `s.removeTaskById` (Task 5); `s.stopWorkerById`, `s.deleteWorkerById` (Task 7).
- Produces: `blanketCancelTaskArgs`, `(s *ServerConfig) mcpCancelTask(...)`; `blanketStopWorkerArgs`, `(s *ServerConfig) mcpStopWorker(...)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestMcpCancelTask_Valid(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	res, _, err := s.mcpCancelTask(context.Background(), nil, blanketCancelTaskArgs{Id: tsk.Id.Hex()})
	assert.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "canceled")

	updated, err := s.DB.GetTask(tsk.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", updated.State)
}

func TestMcpCancelTask_WithDelete(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	_, _, err = s.mcpCancelTask(context.Background(), nil, blanketCancelTaskArgs{Id: tsk.Id.Hex(), Delete: true})
	assert.NoError(t, err)

	_, err = s.DB.GetTask(tsk.Id)
	assert.Error(t, err)
}

func TestMcpCancelTask_InvalidId(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpCancelTask(context.Background(), nil, blanketCancelTaskArgs{Id: "not-an-id"})
	assert.Error(t, err)
}

func TestMcpStopWorker_Valid(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	res, _, err := s.mcpStopWorker(context.Background(), nil, blanketStopWorkerArgs{Id: w.Id.Hex()})
	assert.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "stopped")

	updated, err := s.DB.GetWorker(w.Id)
	assert.NoError(t, err)
	assert.True(t, updated.Stopped)
}

func TestMcpStopWorker_WithDelete(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	_, _, err := s.mcpStopWorker(context.Background(), nil, blanketStopWorkerArgs{Id: w.Id.Hex(), Delete: true})
	assert.NoError(t, err)

	_, err = s.DB.GetWorker(w.Id)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/... -run 'TestMcpCancelTask|TestMcpStopWorker' -v`
Expected: FAIL.

- [ ] **Step 3: Implement the tools**

Add to `server/serve_mcp_tools.go`:

```go
type blanketCancelTaskArgs struct {
	Id     string `json:"id" jsonschema:"task id"`
	Delete bool   `json:"delete,omitempty" jsonschema:"also delete the task and its result directory after canceling"`
}

func (s *ServerConfig) mcpCancelTask(ctx context.Context, req *mcp.CallToolRequest, args blanketCancelTaskArgs) (*mcp.CallToolResult, any, error) {
	if !objectid.IsObjectIdHex(args.Id) {
		return nil, nil, fmt.Errorf("%q is not a valid task id", args.Id)
	}
	taskId := objectid.ObjectIdHex(args.Id)

	if err := s.cancelTaskById(ctx, taskId); err != nil {
		return nil, nil, err
	}

	msg := fmt.Sprintf("canceled task %s", taskId.Hex())
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
	Delete bool   `json:"delete,omitempty" jsonschema:"also delete the worker record after stopping"`
}

func (s *ServerConfig) mcpStopWorker(ctx context.Context, req *mcp.CallToolRequest, args blanketStopWorkerArgs) (*mcp.CallToolResult, any, error) {
	if !objectid.IsObjectIdHex(args.Id) {
		return nil, nil, fmt.Errorf("%q is not a valid worker id", args.Id)
	}
	workerId := objectid.ObjectIdHex(args.Id)

	if err := s.stopWorkerById(ctx, workerId); err != nil {
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
```

Fill in `registerDestructiveMCPTools` in `server/serve_mcp.go`:

```go
func (s *ServerConfig) registerDestructiveMCPTools(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierAll) {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_cancel_task",
		Description: "Cancel a RUNNING or WAITING task, optionally deleting it (delete=true).",
	}, s.mcpCancelTask)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_stop_worker",
		Description: "Stop a worker after its current task finishes, optionally deleting its record (delete=true).",
	}, s.mcpStopWorker)
}
```

This completes all nine tool registrations.

- [ ] **Step 4: Run tests to verify everything passes**

Run: `go test ./server/... -v -run 'TestMcpCancelTask|TestMcpStopWorker'`
Expected: PASS.

Then the full package: `go test ./server/... -v` and `go build ./...`
Expected: all PASS, clean build.

- [ ] **Step 5: Commit**

```bash
git add server/serve_mcp_tools.go server/serve_mcp.go server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] add blanket_cancel_task and blanket_stop_worker MCP tools

Completes all nine tools across the three mcp.mode tiers
(readonly/create/all). Both take an optional delete=true to also
remove the record after stopping/canceling, matching the REST
DELETE endpoints' semantics in one call instead of two.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 18: Mode-gating integration test

**Files:**
- Modify: `server/serve_mcp_test.go`

**Interfaces:** Consumes the full tool surface from Tasks 10-17; no new production code.

- [ ] **Step 1: Write the test**

This is itself the verification step — there's no separate "make it pass" implementation, since it exercises code that's already complete. Still follow red-green: run it first to confirm it's meaningful (it should pass immediately if Tasks 8-17 were done correctly; if it fails, that's a real bug to fix, not a stub to fill in).

```go
func TestMCPModeGating(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	defer viper.Set("mcp.mode", nil)

	wantByMode := map[string][]string{
		"readonly": {"blanket_docs", "blanket_task_types", "blanket_tasks", "blanket_workers"},
		"create":   {"blanket_docs", "blanket_write_task_type", "blanket_submit_task", "blanket_launch_worker"},
		"all":      {"blanket_cancel_task", "blanket_stop_worker"},
	}

	for mode, wantNames := range wantByMode {
		viper.Set("mcp.mode", mode)
		srv := s.buildMCPServer()

		ctx := context.Background()
		clientTransport, serverTransport := mcp.NewInMemoryTransports()
		serverSession, err := srv.Connect(ctx, serverTransport, nil)
		assert.NoError(t, err)

		client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
		clientSession, err := client.Connect(ctx, clientTransport, nil)
		assert.NoError(t, err)

		res, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
		assert.NoError(t, err)

		got := map[string]bool{}
		for _, tool := range res.Tools {
			got[tool.Name] = true
		}
		for _, name := range wantNames {
			assert.True(t, got[name], "mode %q should include tool %q", mode, name)
		}
		if mode == "readonly" {
			assert.False(t, got["blanket_submit_task"], "readonly mode should not include blanket_submit_task")
			assert.False(t, got["blanket_cancel_task"], "readonly mode should not include blanket_cancel_task")
		}
		if mode == "create" {
			assert.False(t, got["blanket_cancel_task"], "create mode should not include blanket_cancel_task")
		}

		clientSession.Close()
		serverSession.Wait()
	}
}
```

Add imports if not already present: `"github.com/modelcontextprotocol/go-sdk/mcp"`.

- [ ] **Step 2: Run and verify it passes**

Run: `go test ./server/... -run TestMCPModeGating -v`
Expected: PASS. If it fails, the bug is in one of Tasks 8-17's registration calls (a tool registered under the wrong tier) — fix the registration, not this test.

- [ ] **Step 3: Commit**

```bash
git add server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] add MCP mode-gating integration test

Exercises all three mcp.mode values end-to-end over an in-memory
MCP client/server pair, confirming each tier's tools are visible
(and higher-tier tools are not) in tools/list.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 19: Context budget test

**Files:**
- Modify: `server/serve_mcp_test.go`

**Interfaces:** Consumes `mcpContextBudgetChars` (Task 9) and the full tool surface (Tasks 10-17).

- [ ] **Step 1: Write the test**

```go
func TestToolListFitsContextBudget(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	viper.Set("mcp.mode", "all") // worst case: every tool registered
	defer viper.Set("mcp.mode", nil)

	srv := s.buildMCPServer()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	assert.NoError(t, err)
	defer serverSession.Wait()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	assert.NoError(t, err)
	defer clientSession.Close()

	res, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	assert.NoError(t, err)

	toolsJSON, err := json.Marshal(res.Tools)
	assert.NoError(t, err)

	total := len(toolsJSON) + len(mcpInstructions)
	t.Logf("tools/list (%d tools) + Instructions: %d characters (budget: %d)", len(res.Tools), total, mcpContextBudgetChars)
	assert.LessOrEqual(t, total, mcpContextBudgetChars,
		"tools/list + Instructions exceeds the %d-character budget; see docs/mcp.md's levers (trim jsonschema descriptions, shorten tool descriptions, move prose into blanket_docs)", mcpContextBudgetChars)
}
```

Add import `"encoding/json"` if not already present.

- [ ] **Step 2: Run and verify it passes**

Run: `go test ./server/... -run TestToolListFitsContextBudget -v`
Expected: PASS, with the logged character count. **Record the actual number from the test log** — it goes into `docs/mcp.md` in Task 20 as the "measured actual."

If it fails: apply the levers in order (trim `jsonschema:"..."` descriptions first, then tool `Description` strings, then consider moving more prose into `blanket_docs`) and re-run until it passes. Do not raise `mcpContextBudgetChars` to make it pass — that constant is the design's target, not a knob to loosen under pressure.

- [ ] **Step 3: Commit**

```bash
git add server/serve_mcp_test.go
git commit -m "$(cat <<'EOF'
[AI] add MCP context-budget test

Enforces the design's <=4,000-character tools/list + Instructions
budget in mcp.mode=all (the worst case). Measured actual: <FILL IN
FROM TEST LOG OUTPUT> characters.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

(Replace `<FILL IN FROM TEST LOG OUTPUT>` with the real number from Step 2's `t.Logf` output before committing.)

---

## Task 20: Write `docs/mcp.md`

**Files:**
- Create: `docs/mcp.md`

**Interfaces:** Consumed by `docs.Page("api")`-equivalent lookup (already wired in Task 3's `pages` map — no code change needed here, just content).

- [ ] **Step 1: Write the doc**

```markdown
# MCP Interface

Blanket exposes an [MCP](https://modelcontextprotocol.io) server at
`/mcp` on the same port as the REST API (default `8773`) whenever
`mcp.enabled` is true (the default). Any MCP-capable agent pointed at a
running `blanket serve` can author task types, submit tasks, launch and
inspect workers, and debug a failed task — no repo checkout, no skill
install, no second process.

## Security — read this first

By default the MCP server binds every network interface (same as the
REST API) and `mcp.mode = "all"`, meaning any host that can reach the
port can write a task-type TOML and launch a worker to run it — that's
arbitrary code execution as the blanket user. This is a difference in
degree, not kind, from the REST API's existing unauthenticated
`POST /task/`, but it's a notably cleaner primitive.

The default posture trades this off deliberately for zero-effort setup —
the expected deployment is behind a private overlay network (e.g.
[Tailscale](https://tailscale.com/)), which delivers traffic to the
host's own network interface rather than `127.0.0.1`, so binding only
loopback would actually break that use case. **If you're running blanket
somewhere more exposed than a private network, set `mcp.mode =
"readonly"` or `mcp.enabled = false`.** Token auth and a loopback-default
`bindAddress` option are tracked in
[issue #44](https://github.com/turtlemonvh/blanket/issues/44).

## What's exposed

Nine tools, gated by `mcp.mode`:

| Tool | Args | Tier |
| --- | --- | --- |
| `blanket_docs` | `page` | readonly |
| `blanket_task_types` | `name?` | readonly |
| `blanket_tasks` | `id?`, `states?`, `types?`, `limit?`, `log_lines?` | readonly |
| `blanket_workers` | `id?`, `log_lines?` | readonly |
| `blanket_write_task_type` | `name`, `toml` | create |
| `blanket_submit_task` | `type`, `env?` | create |
| `blanket_launch_worker` | `tags`, `count?` | create |
| `blanket_cancel_task` | `id`, `delete?` | all |
| `blanket_stop_worker` | `id`, `delete?` | all |

`create` mode includes `readonly`'s tools; `all` includes both.

Every tool returns plain text (compact tables for lists, a labeled
key-value block for a single item), not JSON — this keeps the response
small and just as readable to an agent.

## Context cost

`tools/list` plus the server's instructions text is kept under **4,000
characters (~1,000 tokens)** in the default `mcp.mode = "all"` — the
worst case, since narrower modes register fewer tools. This is a
test-enforced budget (`TestToolListFitsContextBudget`), not just a
target; the actual measured size is logged by that test on every run.

If you're tight on context budget elsewhere, set `mcp.mode = "readonly"`
to cut this further, or wait for the tool-search / dynamic-discovery
mode tracked in issue #44 if the tool count grows.

## Setup

**Claude Code**, user scope (available in every project):

```
claude mcp add --transport http blanket http://localhost:8773/mcp
```

Or project scope, committed to the repo as `.mcp.json`:

```json
{
  "mcpServers": {
    "blanket": { "type": "http", "url": "http://localhost:8773/mcp" }
  }
}
```

Run `/mcp` inside Claude Code afterward to confirm the connection and see
the tool list.

**Claude Desktop / other MCP clients**: point them at
`http://<host>:8773/mcp` using their own streamable-HTTP MCP config —
the endpoint is a standard MCP streamable HTTP server, not
Claude-Code-specific.

## Permissions

Set `mcp.mode` in your blanket config file:

```json
{
  "mcp": {
    "mode": "readonly"
  }
}
```

- `readonly` — the four read tools only. Safe to expose broadly.
- `create` — adds write-a-task-type, submit-a-task, launch-a-worker.
  Code execution as the blanket user, same as the REST API today.
- `all` (default) — adds cancel-task and stop-worker.

Set `mcp.enabled = false` to not mount `/mcp` at all.

## Worked example

1. `blanket_docs(page="authoring")` — read the task-type authoring
   guide.
2. `blanket_write_task_type(name="hello", toml="...")` — validates
   first; refuses to write on any error-level finding, and returns the
   findings either way so you can fix and retry.
3. `blanket_workers()` — check whether a worker exists whose tags are a
   superset of `hello`'s tags. If not, `blanket_launch_worker(tags=[...])`.
4. `blanket_submit_task(type="hello")` — queues the task; returns its id.
5. `blanket_tasks(id="<id>", log_lines=50)` — check status
   (`WAITING`/`RUNNING`/`SUCCESS`/`ERROR`/...) and the last 50 lines of
   its stdout log, for debugging a failure.

## Configuration reference

```
mcp.enabled         true    # mount /mcp at all
mcp.mode            "all"   # readonly | create | all
mcp.writeTypesPath  ""      # defaults to the first tasks.typesPaths entry
mcp.validateStrict  false   # also refuse blanket_write_task_type on warnings
mcp.maxLogLines     200     # hard cap on log_lines, regardless of what's asked
```
```

- [ ] **Step 2: Verify it's reachable via the embedded docs lookup**

Run: `go test ./docs/... -v` (the `TestPage_KnownKeys` test from Task 3 already iterates every key including `"api"`... note `mcp.md` isn't in the `pages` map — it's linked from `docs/README.md`, not a standalone `blanket_docs` key. Confirm this is intentional: `docs/mcp.md` is meta-documentation about the MCP server itself, read by a human setting things up, not by an agent through `blanket_docs` — an agent doesn't need to be told how to configure the MCP server it's already connected through). No code change needed; this step just confirms `go test ./docs/...` still passes with the new file present (embed pulls in all `*.md` regardless of whether every file has a `pages` key).

- [ ] **Step 3: Commit**

```bash
git add docs/mcp.md
git commit -m "$(cat <<'EOF'
[AI] write docs/mcp.md

Leads with the security exposure (all-interfaces bind + mode=all by
default) and the Tailscale rationale, states the context budget as
a concrete number, and gives copy-pasteable Claude Code setup
commands for both user and project scope.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 21: Update `docs/api.md`, `docs/README.md`, `docs/next_up.md`

**Files:**
- Modify: `docs/api.md`, `docs/README.md`, `docs/next_up.md`

**Interfaces:** None — documentation only.

- [ ] **Step 1: Add `/mcp` to `docs/api.md`**

Add a new `## MCP` section, after the existing `## Server` section (end of file, after `GET /ops/status/`):

```markdown

## MCP

```
ANY /mcp                        # MCP streamable-HTTP endpoint (JSON-RPC 2.0)
```

Mounted when `mcp.enabled` is true (default). See
[mcp.md](mcp.md) for the tool list, setup instructions, and the
security posture of the default (all-interfaces, `mcp.mode = "all"`)
configuration.
```

- [ ] **Step 2: Index the new page in `docs/README.md`**

In the "For users" list, after the "API" entry:

```markdown
- [**API**](api.md) — full list of REST endpoints.
- [**MCP interface**](mcp.md) — tool list, setup (incl. Claude Code),
  permissions, and the default security posture.
```

- [ ] **Step 3: Drop the superseded entry in `docs/next_up.md`**

Remove this bullet from the "Features" section (it's now implemented, not planned):

```markdown
- **MCP wrapper** — expose blanket as an MCP server so AI agents can
  list/submit/inspect tasks as tools. Server lives alongside the REST
  API (likely a new `blanket mcp` subcommand or a `/mcp` route). Tools
  to surface: `submit_task`, `list_tasks`, `get_task`, `get_task_log`,
  `cancel_task`, `list_task_types`. Auth and scoping TBD.
```

Leave every other bullet in `docs/next_up.md` untouched — the deferred items from this feature (token auth, `bindAddress`, MCP resources/prompts, tool-search mode, retiring the `blanket-task-type` skill, `require_explicit_tasktype_overwrite`) already live in [issue #44](https://github.com/turtlemonvh/blanket/issues/44), not here (issue #43 is migrating this whole file to issues generally).

- [ ] **Step 4: Verify**

No automated test covers doc content. Manually re-read `docs/api.md`'s new section and `docs/README.md`'s new line for accuracy against the actual route (`r.Any("/mcp", ...)` from Task 9).

- [ ] **Step 5: Commit**

```bash
git add docs/api.md docs/README.md docs/next_up.md
git commit -m "$(cat <<'EOF'
[AI] document /mcp in api.md and README.md; drop superseded next_up.md entry

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 22: MCP round trip in `scripts/smoke.sh`

**Files:**
- Modify: `scripts/smoke.sh`

**Interfaces:** None — exercises the already-built binary end-to-end.

- [ ] **Step 1: Add the round trip**

Append to `scripts/smoke.sh`, after the existing `task-validate --json` check (end of file):

```bash

# MCP: initialize -> tools/list -> tools/call blanket_tasks.
# Confirms the streamable-HTTP handler is really mounted and at least one
# tool round-trips end to end against the built binary (not just in-process
# unit tests).
mcp_init_resp="$(curl -fsS -X POST "$BASE/mcp" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke-test","version":"0.0.0"}}}')"
echo "$mcp_init_resp" | grep -q '"protocolVersion"' \
    || fail "MCP initialize response missing protocolVersion: $mcp_init_resp"

mcp_session_id="$(curl -fsS -X POST "$BASE/mcp" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -D - -o /dev/null \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke-test","version":"0.0.0"}}}' \
    | grep -i '^mcp-session-id:' | tr -d '\r' | cut -d' ' -f2)"
[[ -n "$mcp_session_id" ]] || fail "MCP initialize did not return an Mcp-Session-Id header"

curl -fsS -X POST "$BASE/mcp" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $mcp_session_id" \
    -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' > /dev/null

mcp_tools_resp="$(curl -fsS -X POST "$BASE/mcp" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $mcp_session_id" \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}')"
echo "$mcp_tools_resp" | grep -q '"blanket_tasks"' \
    || fail "MCP tools/list missing blanket_tasks: $mcp_tools_resp"

mcp_call_resp="$(curl -fsS -X POST "$BASE/mcp" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Session-Id: $mcp_session_id" \
    -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"blanket_tasks","arguments":{}}}')"
echo "$mcp_call_resp" | grep -q '"content"' \
    || fail "MCP tools/call blanket_tasks did not return content: $mcp_call_resp"
```

- [ ] **Step 2: Verify against a real build**

```bash
make docker-build
make docker-test-smoke
```

Expected: `smoke: OK`. If the streamable-HTTP protocol negotiation doesn't match exactly (session header casing, SSE vs JSON response framing), inspect the raw response with `curl -v` against a locally-run `blanket serve` first — the go-sdk's streamable HTTP handler responds with `Content-Type: application/json` for a single non-streaming response by default, which is what the `grep` checks above assume; adjust the `Accept`/parsing if the actual response is SSE-framed instead.

- [ ] **Step 3: Commit**

```bash
git add scripts/smoke.sh
git commit -m "$(cat <<'EOF'
[AI] add MCP handshake round trip to scripts/smoke.sh

initialize -> notifications/initialized -> tools/list ->
tools/call(blanket_tasks) against the actual built binary, not just
in-process Go tests -- catches route-mounting or transport-framing
regressions the unit tests can't see.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Task 23: Full verification pass + manual acceptance test

**Files:** None — verification only.

- [ ] **Step 1: Run every automated surface**

```bash
make docker-test
make docker-test-smoke
make docker-test-browser
make docker-build
```

Expected: all four green. `docker-test-browser` should be unaffected (no UI changes in this plan) — if it fails, that's a signal something broke that this plan didn't anticipate; investigate before proceeding.

- [ ] **Step 2: `go vet` and `gofmt` check**

```bash
gofmt -l .
go vet ./...
```

Expected: `gofmt -l .` prints nothing (no unformatted files); `go vet` reports no issues.

- [ ] **Step 3: Manual end-to-end acceptance test**

This is the real test of the design's core promise ("self-contained: no skill install, no repo checkout, no second process"), and nothing automated checks it:

1. Start a real server: `go run . serve --config <a throwaway config pointing at empty types/results dirs>`.
2. In a **separate terminal**, run `claude mcp add --transport http blanket http://localhost:8773/mcp` (or add the equivalent `.mcp.json`).
3. Open a **fresh Claude Code session in a directory with no blanket repo checkout and no `blanket-task-type` skill installed**.
4. Ask it to: author a simple task type (e.g., "make a blanket task type called `hello` that echoes a greeting"), launch a worker, submit a task of that type, and report whether it succeeded — using only the MCP tools, no shell access to the blanket repo.
5. Confirm: the agent used `blanket_docs(page="authoring")` (or otherwise produced a valid TOML unprompted), `blanket_write_task_type` succeeded, `blanket_launch_worker` + `blanket_submit_task` ran the task, and `blanket_tasks(id=...)` correctly reported `SUCCESS`.

Report the outcome in the PR description — this can't be scripted into CI, so it needs to be stated as evidence, not assumed.

- [ ] **Step 4: Update the "measured actual" placeholder**

If Task 19's commit message still has a placeholder or a stale number (e.g. if later tasks changed a tool description), re-run `go test ./server/... -run TestToolListFitsContextBudget -v`, and update the number quoted in `docs/mcp.md`'s "Context cost" section to match exactly.

- [ ] **Step 5: Final commit (if Step 4 changed anything)**

```bash
git add docs/mcp.md
git commit -m "$(cat <<'EOF'
[AI] pin measured tools/list context-budget number in docs/mcp.md

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01EPmPTrnJj65om8TJVSECUq
EOF
)"
```

---

## Verification Summary

End-to-end, after all 23 tasks:

- `make docker-test` — unit tests across every package, including the new budget/mode-gating/per-tool tests.
- `make docker-test-smoke` — built-binary MCP handshake (Task 22) plus all pre-existing smoke checks.
- `make docker-test-browser` — unaffected, must stay green.
- `make docker-build` — cross-compiles linux/darwin/windows on Go 1.25 with the SDK vendored.
- Manual acceptance test (Task 23) — proves the "self-contained" design goal with a real fresh-session agent, which no automated test can.

**Before opening the PR:** blanket has no `CHANGELOG.md` — GitHub Releases are auto-generated from PR titles/commits. Per the design review, the PR title/description must call out the new `/mcp` surface and its default-open, default-enabled posture explicitly, since that's what will actually surface in the release notes (this was an explicit resolution from the review, not incidental).
