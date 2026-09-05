package server

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kardianos/osext"
	"github.com/manucorporat/sse"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

//go:embed all:ui/templates all:ui/static
var uiFS embed.FS

// uiStaticFS serves /ui/static/*.
func uiStaticFS() http.FileSystem {
	sub, err := fs.Sub(uiFS, "ui/static")
	if err != nil {
		panic(err)
	}
	return http.FS(sub)
}

// Template funcs shared across all pages.
var uiFuncs = template.FuncMap{
	"add":  func(a, b int) int { return a + b },
	"join": strings.Join,
	"shortId": func(id objectid.ObjectId) string {
		h := id.Hex()
		if len(h) >= 8 {
			return h[:8]
		}
		return h
	},
	"hex": func(id objectid.ObjectId) string { return id.Hex() },
	"fmtTs": func(ts int64) string {
		if ts == 0 {
			return ""
		}
		return time.Unix(ts, 0).UTC().Format("2006/01/02 15:04:05")
	},
	"isCancelable": func(state string) bool {
		return state == "WAITING" || state == "SCHEDULED" || state == "CLAIMED" || state == "RUNNING"
	},
	"isTerminal": func(state string) bool {
		for _, s := range tasks.ValidTerminalTaskStates {
			if s == state {
				return true
			}
		}
		return false
	},
	// scheduleDesc is the same friendly text the JSON API exposes as a
	// task's "scheduleDescription" field, so the UI and API never disagree
	// about how a schedule reads.
	"scheduleDesc": tasks.ScheduleDescriptionFor,
	// cronDesc is scheduleDesc's building block, without the trailing
	// "(paused)"/"(stopped)" annotation — for the places that show a
	// status badge right next to the schedule and don't want it twice.
	"cronDesc": cronDescription,
}

// uiTemplates is populated lazily per page so the partial templates
// (tasks-rows, workers-rows, …) can be included alongside their parent.
var uiTemplates = map[string]*template.Template{}

// mustParseUIPage parses layout + the named page (+ optional partial files)
// and caches the result. Panics on error — templates are embedded, so any
// parse failure is a build-time bug.
func mustParseUIPage(name string, files ...string) *template.Template {
	if t, ok := uiTemplates[name]; ok {
		return t
	}
	paths := append([]string{"ui/templates/_layout.html"}, files...)
	t, err := template.New(name).Funcs(uiFuncs).ParseFS(uiFS, paths...)
	if err != nil {
		panic(fmt.Errorf("ui: parse %s: %w", name, err))
	}
	uiTemplates[name] = t
	return t
}

// mustParsePartial parses standalone partial template(s) without the layout.
func mustParsePartial(name string, files ...string) *template.Template {
	key := "partial:" + name
	if t, ok := uiTemplates[key]; ok {
		return t
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, "ui/templates/"+f)
	}
	t, err := template.New(name).Funcs(uiFuncs).ParseFS(uiFS, paths...)
	if err != nil {
		panic(fmt.Errorf("ui: parse partial %s: %w", name, err))
	}
	uiTemplates[key] = t
	return t
}

// TaskTypeView is the render-friendly projection of tasks.TaskType.
type TaskTypeView struct {
	Name          string
	Description   string
	Documentation string
	Tags          []string
	Executor      string
	Timeout       int
	LoadedTs      int64
	ConfigFile    string
	VersionHash   string
}

// SettingView is one row on the About page.
type SettingView struct {
	Key   string
	Value string
}

// buildTaskTypeView projects a tasks.TaskType into its render-friendly view.
func buildTaskTypeView(tt *tasks.TaskType) TaskTypeView {
	cfg := tt.Config
	executor := cfg.GetString("executor")
	if executor == "" {
		executor = "bash"
	}
	return TaskTypeView{
		Name:          tt.GetName(),
		Description:   tt.GetDescription(),
		Documentation: tt.GetDocumentation(),
		Tags:          cfg.GetStringSlice("tags"),
		Executor:      executor,
		Timeout:       cfg.GetInt("timeout"),
		LoadedTs:      tt.LoadedTs,
		ConfigFile:    tt.ConfigFile,
		VersionHash:   tt.ConfigVersionHash,
	}
}

func readTaskTypeViews() []TaskTypeView {
	tts, err := tasks.ReadTypes()
	if err != nil {
		log.WithField("err", err).Warn("ui: read task types")
		return nil
	}
	views := make([]TaskTypeView, 0, len(tts))
	for i := range tts {
		views = append(views, buildTaskTypeView(&tts[i]))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views
}

// uiTasksPage renders the tasks list page.
func (s *ServerConfig) uiTasksPage(c *gin.Context) {
	tks, _, err := s.DB.GetTasks(database.TaskSearchConfFromContext(c))
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	views := readTaskTypeViews()
	typeNames := make([]string, 0, len(views))
	for _, v := range views {
		typeNames = append(typeNames, v.Name)
	}
	t := mustParseUIPage("tasks", "ui/templates/tasks.html", "ui/templates/tasks_rows.html")
	s.renderUI(c, t, gin.H{
		"Title":         "Tasks",
		"Tasks":         tks,
		"TaskStates":    tasks.ValidTaskStates,
		"TaskTypeNames": typeNames,
	})
}

// uiTaskDetailPage renders one task's metadata, env vars, and log stream.
//
// A recurring *template* (the record a cron submission creates) is served
// from this same URL but through series_detail.html instead — one URL per
// task record, a different template for a record that has no worker, no
// progress, no logs and no result dir of its own, but does have a
// schedule, lifecycle actions, and a list of past runs. Branching inside
// task_detail.html would have meant wrapping nearly every row and both log
// sections in {{if}}; see renderSeriesDetail in ui_schedule.go.
func (s *ServerConfig) uiTaskDetailPage(c *gin.Context) {
	taskId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.DB.GetTask(taskId)
	if err != nil {
		c.String(http.StatusNotFound, err.Error())
		return
	}
	if isSeriesTemplate(task) {
		s.renderSeriesDetail(c, task)
		return
	}
	t := mustParseUIPage("task-detail",
		"ui/templates/task_detail.html",
		"ui/templates/series_card.html")
	s.renderUI(c, t, gin.H{
		"Title":  "Task " + taskId.Hex()[:8],
		"Task":   task,
		"Series": s.lookupSeries(task.ParentId),
	})
}

// uiTasksRowsPartial renders just the tbody for htmx swaps.
func (s *ServerConfig) uiTasksRowsPartial(c *gin.Context) {
	tc := database.TaskSearchConfFromContext(c)
	tks, _, err := s.DB.GetTasks(tc)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	t := mustParsePartial("tasks-rows", "tasks_rows.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "tasks-rows", gin.H{
		"Tasks": tks,
		// ?parentId=<id> means the caller is already looking at one
		// series' runs (the series detail page's Past runs table), so the
		// per-row "part of series …" backlink would repeat on every row.
		"HideSeriesLink": tc.FilterParentId,
	}); err != nil {
		log.WithField("err", err).Warn("ui: render tasks-rows")
	}
}

func (s *ServerConfig) uiWorkersPage(c *gin.Context) {
	ws, err := s.DB.GetWorkers()
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	t := mustParseUIPage("workers", "ui/templates/workers.html", "ui/templates/workers_rows.html")
	s.renderUI(c, t, gin.H{"Title": "Workers", "Workers": ws})
}

// uiWorkerDetailPage renders one worker's metadata and log stream.
func (s *ServerConfig) uiWorkerDetailPage(c *gin.Context) {
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
	t := mustParseUIPage("worker-detail", "ui/templates/worker_detail.html")
	s.renderUI(c, t, gin.H{"Title": "Worker " + workerId.Hex()[:8], "Worker": w})
}

// uiNewWorkerPartial returns the "new worker" form.
func (s *ServerConfig) uiNewWorkerPartial(c *gin.Context) {
	t := mustParsePartial("new-worker-form", "new_worker_form.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "new-worker-form", nil); err != nil {
		log.WithField("err", err).Warn("ui: render new-worker-form")
	}
}

// uiSubmitWorker spawns a daemon worker from form input and returns
// the refreshed rows partial. Mirrors server.launchWorker without the JSON
// response shape.
func (s *ServerConfig) uiSubmitWorker(c *gin.Context) {
	rawTags := strings.TrimSpace(c.PostForm("tags"))
	tags := []string{}
	if rawTags != "" {
		for _, t := range strings.Split(rawTags, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
	}
	interval := cast.ToFloat64(c.PostForm("checkInterval"))
	if interval <= 0 {
		interval = worker.DEFAULT_CHECK_INTERVAL_SECONDS
	}
	if interval < worker.MIN_CHECK_INTERVAL_SECONDS {
		c.String(http.StatusBadRequest, worker.ErrCheckIntervalTooLow.Error())
		return
	}

	w := worker.WorkerConf{
		Id:            objectid.NewObjectId(),
		Tags:          tags,
		Daemon:        true,
		CheckInterval: interval,
	}
	if err := w.Run(); err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}

	// Short poll for the worker to register itself in the DB so the
	// refreshed rows include it. Mirrors the API handler's wait loop.
	deadline := time.Now().Add(time.Duration(float64(MAX_REQUEST_TIME_SECONDS)*s.TimeMultiplier) * time.Second)
	for time.Now().Before(deadline) {
		found, _ := s.DB.GetWorker(w.Id)
		if found.Pid != 0 {
			break
		}
		time.Sleep(time.Duration(250*s.TimeMultiplier) * time.Millisecond)
	}
	s.WorkerEvents.Notify()
	s.uiWorkersRowsPartial(c)
}

func (s *ServerConfig) uiWorkersRowsPartial(c *gin.Context) {
	ws, err := s.DB.GetWorkers()
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	t := mustParsePartial("workers-rows", "workers_rows.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "workers-rows", gin.H{"Workers": ws}); err != nil {
		log.WithField("err", err).Warn("ui: render workers-rows")
	}
}

func (s *ServerConfig) uiTaskTypesPage(c *gin.Context) {
	t := mustParseUIPage("task-types",
		"ui/templates/task_types.html",
		"ui/templates/task_types_rows.html")
	s.renderUI(c, t, gin.H{"Title": "Task Types", "TaskTypes": readTaskTypeViews()})
}

// uiTaskTypeDetailPage renders one task type's description, documentation,
// and settings.
func (s *ServerConfig) uiTaskTypeDetailPage(c *gin.Context) {
	name := c.Param("name")
	tt, err := tasks.FetchTaskType(name)
	if err != nil {
		c.String(http.StatusNotFound, err.Error())
		return
	}
	t := mustParseUIPage("task-type-detail", "ui/templates/task_type_detail.html")
	view := buildTaskTypeView(tt)
	s.renderUI(c, t, gin.H{"Title": "Task Type " + view.Name, "TaskType": view})
}

func (s *ServerConfig) uiTaskTypesRowsPartial(c *gin.Context) {
	t := mustParsePartial("task-types-rows", "task_types_rows.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "task-types-rows", gin.H{"TaskTypes": readTaskTypeViews()}); err != nil {
		log.WithField("err", err).Warn("ui: render task-types-rows")
	}
}

// Settings keys hidden from the About page — internal/test-only knobs.
var aboutHiddenKeys = map[string]bool{
	"timemultiplier":  true, // test-time speedup
	"tasks.typespath": true, // legacy singular; superseded by typesPaths
}

// Settings keys whose values should be rendered as absolute paths (or a
// list of absolute paths for slice-valued keys).
var aboutPathKeys = map[string]bool{
	"database":          true,
	"tasks.typespaths":  true,
	"tasks.resultspath": true,
}

// Settings keys whose values are slices.
var aboutSliceKeys = map[string]bool{
	"tasks.typespaths":  true,
	"tasks.resultspath": true,
}

func absPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func (s *ServerConfig) uiAboutPage(c *gin.Context) {
	keys := viper.AllKeys()
	sort.Strings(keys)
	settings := make([]SettingView, 0, len(keys))
	for _, k := range keys {
		if aboutHiddenKeys[k] {
			continue
		}
		var value string
		switch {
		case aboutSliceKeys[k]:
			parts := viper.GetStringSlice(k)
			if aboutPathKeys[k] {
				for i, p := range parts {
					parts[i] = absPath(p)
				}
			}
			value = strings.Join(parts, ", ")
		case aboutPathKeys[k]:
			value = absPath(viper.GetString(k))
		default:
			value = viper.GetString(k)
		}
		settings = append(settings, SettingView{Key: k, Value: value})
	}

	binaryPath, err := osext.Executable()
	if err != nil {
		log.WithField("err", err).Warn("ui: resolve binary path")
		binaryPath = "(unknown)"
	}

	configPath := viper.ConfigFileUsed()
	configContents := ""
	if configPath != "" {
		if b, err := os.ReadFile(configPath); err == nil {
			configContents = string(b)
		} else {
			configContents = fmt.Sprintf("(could not read: %s)", err)
		}
	}

	t := mustParseUIPage("about", "ui/templates/about.html")
	s.renderUI(c, t, gin.H{
		"Title":          "About",
		"Version":        s.Version,
		"BinaryPath":     binaryPath,
		"PID":            os.Getpid(),
		"ConfigPath":     configPath,
		"ConfigContents": configContents,
		"Settings":       settings,
	})
}

// ScheduleEditorView is the data contract for the "schedule-editor"
// template partial (ui/templates/schedule_editor.html) — the shared
// notBefore/cron editor used by the create form and, once
// turtlemonvh/blanket#98 lands, a live series' "change the schedule"
// form. The template's own header comment documents the form fields it
// emits and how the server reads them back.
type ScheduleEditorView struct {
	// IDPrefix prefixes every element id the editor renders, so more than
	// one editor can coexist on a page. Required, e.g. "schedule".
	IDPrefix string
	// Mode is the preselected radio: "once" (default) or "repeating".
	Mode string
	// NotBefore is the initial datetime-local value ("2006-01-02T15:04").
	NotBefore string
	// Cron is the initial cron expression.
	Cron string
	// Collapsed hides the editor behind a "Schedule task?" checkbox —
	// scheduling is opt-in on the create form. False renders the fields
	// unconditionally, for a task whose schedule already exists.
	Collapsed bool
	// Preview is the server-rendered initial contents of the live cron
	// preview; nil renders the "type a cron expression" hint.
	Preview *SchedulePreviewView
}

// SchedulePreviewView is the data contract for the "schedule-preview"
// template — the live human-readable rendering of a cron expression.
// Exactly one of Description / Error is set for a non-empty expression.
type SchedulePreviewView struct {
	Cron        string
	Description string
	Next        []string
	Error       string
}

// nextFirePreviewCount is how many upcoming fire times the cron preview
// lists. Matches the "next" array of GET /schedule/describe.
const nextFirePreviewCount = 3

// buildSchedulePreview renders expr for the live preview. An empty
// expression yields an empty view (the "type a cron expression" hint); an
// unparseable one carries the parser's message in Error rather than
// failing the request, because htmx only swaps 2xx responses — a 4xx here
// would leave the user with no feedback at all.
func buildSchedulePreview(expr string, now time.Time) *SchedulePreviewView {
	expr = strings.TrimSpace(expr)
	view := &SchedulePreviewView{Cron: expr}
	if expr == "" {
		return view
	}
	desc, err := tasks.DescribeCron(expr)
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.Description = desc
	fires, err := tasks.NextCronFires(expr, now, nextFirePreviewCount)
	if err != nil {
		view.Error = err.Error()
		return view
	}
	for _, f := range fires {
		view.Next = append(view.Next, f.Local().Format("2006/01/02 15:04:05"))
	}
	return view
}

// uiSchedulePreviewPartial backs GET /ui/partials/schedule-preview?cron=,
// the HTML sibling of the JSON GET /schedule/describe: the cron input
// fetches it as the user types (debounced) and swaps the rendering in
// under the field. See buildSchedulePreview for why it's always a 200.
func (s *ServerConfig) uiSchedulePreviewPartial(c *gin.Context) {
	t := mustParsePartial("schedule-preview", "schedule_editor.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	view := buildSchedulePreview(c.Query("cron"), time.Now())
	if err := t.ExecuteTemplate(c.Writer, "schedule-preview", view); err != nil {
		log.WithField("err", err).Warn("ui: render schedule-preview")
	}
}

// uiNewTaskPartial returns the "new task" form pre-populated with types.
func (s *ServerConfig) uiNewTaskPartial(c *gin.Context) {
	t := mustParsePartial("new-task-form", "new_task_form.html", "schedule_editor.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "new-task-form", gin.H{
		"TaskTypes": readTaskTypeViews(),
		"Schedule": ScheduleEditorView{
			IDPrefix:  "schedule",
			Mode:      "once",
			Collapsed: true,
		},
	}); err != nil {
		log.WithField("err", err).Warn("ui: render new-task-form")
	}
}

// envVarView is one row in the task-type env editor.
type envVarView struct {
	Name        string
	Value       string
	Type        string
	Description string
}

// collectEnvVars extracts a slice of envVarView from a TOML array at path.
// Handles the shape: [{name=..., value=..., description=..., type=...}, ...]
func collectEnvVars(tt *tasks.TaskType, path string) []envVarView {
	raw, ok := tt.Config.Get(path).([]interface{})
	if !ok {
		return nil
	}
	out := make([]envVarView, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		out = append(out, envVarView{
			Name:        toStr(m["name"]),
			Value:       toStr(m["value"]),
			Type:        toStr(m["type"]),
			Description: toStr(m["description"]),
		})
	}
	return out
}

func toStr(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// uiTaskTypeEnvPartial renders the env-var editor for a chosen task type.
func (s *ServerConfig) uiTaskTypeEnvPartial(c *gin.Context) {
	typeName := c.Query("type")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if typeName == "" {
		return
	}
	tt, err := tasks.FetchTaskType(typeName)
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	data := gin.H{
		"Description": tt.Config.GetString("description"),
		"Defaults":    collectEnvVars(tt, "environment.default"),
		"Required":    collectEnvVars(tt, "environment.required"),
		"Optional":    collectEnvVars(tt, "environment.optional"),
	}
	t := mustParsePartial("task-type-env", "task_type_env.html")
	if err := t.ExecuteTemplate(c.Writer, "task-type-env", data); err != nil {
		log.WithField("err", err).Warn("ui: render task-type-env")
	}
}

// uiBlankPartial is used to clear a target on Cancel.
func (s *ServerConfig) uiBlankPartial(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, "")
}

// uiCustomEnvRowPartial returns one empty "custom setting" row that
// the env editor appends to its tbody when the user clicks "Add custom setting".
func (s *ServerConfig) uiCustomEnvRowPartial(c *gin.Context) {
	t := mustParsePartial("custom-env-row", "custom_env_row.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "custom-env-row", nil); err != nil {
		log.WithField("err", err).Warn("ui: render custom-env-row")
	}
}

// datetimeLocalLayouts are the shapes an <input type="datetime-local">
// submits: browsers omit the seconds component unless the value has one,
// and neither shape carries a timezone. They're parsed in the *server's*
// local zone, which is only a fallback — see scheduleRequestFromForm.
var datetimeLocalLayouts = []string{"2006-01-02T15:04", "2006-01-02T15:04:05"}

// normalizeFormNotBefore turns the schedule editor's two start-time fields
// into a single value tasks.ParseNotBefore understands.
//
// The editor's small script fills the hidden notBeforeISO field with the
// picked instant resolved in the *browser's* timezone (an RFC3339 UTC
// string), which is what a user means when they pick "2:30 PM" — so that
// field wins whenever it's present. With scripting off only the bare
// datetime-local value arrives; a wall-clock time with no zone has to be
// resolved against something, and the server's own zone is the closest
// available guess (it's also the zone the flash and `blanket ps` render
// schedules in). Anything else — a duration like "10m", an RFC3339
// timestamp, unix seconds — is passed through untouched, so a hand-rolled
// POST to /ui/tasks keeps the REST API's full vocabulary.
func normalizeFormNotBefore(raw, isoRaw string) string {
	if iso := strings.TrimSpace(isoRaw); iso != "" {
		return iso
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for _, layout := range datetimeLocalLayouts {
		if ts, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return strconv.FormatInt(ts.Unix(), 10)
		}
	}
	return raw
}

// scheduleRequestFromForm builds the {"notBefore": …} / {"cron": …} map
// applySchedule consumes from the schedule editor's fields.
//
// The scheduleMode radio decides which field counts, so a stale value left
// in the hidden half of the editor can never reach the server as a second,
// conflicting schedule (the editor's script also disables those inputs;
// this is the half that doesn't depend on scripting). With no scheduleMode
// at all — a hand-rolled POST — both fields are passed through and
// applySchedule enforces the API's own mutual-exclusion rule.
func scheduleRequestFromForm(c *gin.Context) (map[string]interface{}, error) {
	mode := strings.TrimSpace(c.PostForm("scheduleMode"))
	notBefore := normalizeFormNotBefore(c.PostForm("notBefore"), c.PostForm("notBeforeISO"))
	cronExpr := strings.TrimSpace(c.PostForm("cron"))

	switch mode {
	case "once":
		cronExpr = ""
	case "repeating":
		notBefore = ""
	case "":
		// No mode selected; keep whatever was sent.
	default:
		return nil, fmt.Errorf("unknown schedule mode %q; expected 'once' or 'repeating'", mode)
	}

	req := map[string]interface{}{}
	if notBefore != "" {
		req["notBefore"] = notBefore
	}
	if cronExpr != "" {
		req["cron"] = cronExpr
	}
	return req, nil
}

// uiSubmitTask handles the New Task form submit and returns fresh rows.
// Form fields named `env.<NAME>` are collected into the task's ExecEnv.
func (s *ServerConfig) uiSubmitTask(c *gin.Context) {
	taskType := c.PostForm("type")
	if taskType == "" {
		c.String(http.StatusBadRequest, "type is required")
		return
	}
	tt, err := tasks.FetchTaskType(taskType)
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}

	// Validate the backing task type before creating a task from it —
	// blanket has no task-type authoring UI, so this is where a broken
	// TOML (bad template, missing executor) first surfaces to a user
	// rather than failing later at exec time. Errors block submission;
	// warnings don't block, but are surfaced to the user via a flash
	// message in addition to the server log — see triggerTaskTypeWarnings
	// and turtlemonvh/blanket#64.
	findings := tasks.ValidateTaskType(tt, nil)
	var errMsgs []string
	for _, f := range findings {
		if f.Level == tasks.LevelError {
			errMsgs = append(errMsgs, fmt.Sprintf("%s %s", f.Code, f.Message))
		} else {
			log.WithFields(log.Fields{
				"taskType": taskType,
				"code":     f.Code,
			}).Warn(f.Message)
		}
	}
	if len(errMsgs) > 0 {
		c.String(http.StatusBadRequest, "task type %q failed validation:\n%s", taskType, strings.Join(errMsgs, "\n"))
		return
	}

	if err := c.Request.ParseForm(); err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	childEnv := map[string]string{}
	for key, vals := range c.Request.PostForm {
		if !strings.HasPrefix(key, "env.") || len(vals) == 0 {
			continue
		}
		v := vals[0]
		if v == "" {
			continue
		}
		childEnv[strings.TrimPrefix(key, "env.")] = v
	}

	// "Add custom setting" rows emit paired customEnvName/customEnvValue
	// arrays; zip by index. A blank name (user added a row but didn't fill
	// it) is silently dropped. Declared env.* fields take precedence.
	names := c.Request.PostForm["customEnvName"]
	values := c.Request.PostForm["customEnvValue"]
	for i, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || strings.HasPrefix(name, "env.") {
			continue
		}
		if _, exists := childEnv[name]; exists {
			continue
		}
		v := ""
		if i < len(values) {
			v = values[i]
		}
		childEnv[name] = v
	}

	for name := range tt.RequiredEnv() {
		if childEnv[name] == "" {
			// uiFormError rather than a bare 400: this one is reachable by
			// filling the form in wrong, so the user has to be able to see it.
			s.uiFormError(c, http.StatusBadRequest, fmt.Sprintf("missing required env var: %s", name))
			return
		}
	}

	t, err := tt.NewTask(childEnv)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}

	// Scheduling: the form's notBefore/cron fields feed the exact same
	// applySchedule path POST /task/ uses, so the two surfaces can't drift
	// on what a schedule means. scheduleRequestFromForm has already picked
	// the field belonging to the selected radio.
	scheduleReq, err := scheduleRequestFromForm(c)
	if err != nil {
		s.uiFormError(c, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now()
	if err := s.applyScheduleChecked(&t, scheduleReq, now); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrScheduledCapacityExceeded) {
			status = http.StatusTooManyRequests
		}
		s.uiFormError(c, status, err.Error())
		return
	}
	// applySchedule deliberately treats an already-past notBefore as "run
	// now" for the REST API (see its doc comment). Through a date picker
	// that's almost always a mistake — a user who filled in a start time
	// and got an immediately-queued task has no way to tell it went wrong
	// — so the form rejects it instead.
	if _, asked := scheduleReq["notBefore"]; asked && t.State != "SCHEDULED" {
		s.uiFormError(c, http.StatusBadRequest,
			"the requested start time is in the past; pick a time in the future, or leave 'Schedule task?' unchecked to run the task now")
		return
	}

	// enqueueTask saves and (only for an immediately-runnable task) queues;
	// a SCHEDULED task or a RECURRING template is deliberately left off the
	// queue for the scheduler loop to handle.
	if err := s.enqueueTask(c.Request.Context(), &t); err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}

	// The response body below is (and must stay) raw <tr> rows: htmx infers
	// the parsing context for an ajax response from the first tag it sees,
	// and picks a <table><tbody>…wrapping for a leading <tr> so the browser
	// will actually build table rows out of it. An hx-swap-oob element
	// appended after those rows gets caught by that same table-parsing
	// context and silently foster-parented out of the fragment htmx swaps
	// in — it never reaches the live DOM. So warnings can't be inlined into
	// this response; instead fire a client-side event and let a tiny
	// follow-up request (a plain, table-free response) render them. See
	// triggerTaskTypeWarnings and turtlemonvh/blanket#64.
	s.triggerTaskTypeWarnings(c, taskType, scheduleFlashFor(t))
	s.uiTasksRowsPartial(c)
}

// scheduleFlashFor is the "your task isn't running yet, here's when it
// will" line the create form flashes after a scheduled submission: the
// task's state plus its friendly schedule description (the same text
// `blanket ps` and the task JSON carry). Empty for a plain task that went
// straight onto the queue — nothing to explain there.
//
// ASCII only, deliberately: it travels back to the browser inside an
// HX-Trigger response *header*.
func scheduleFlashFor(t tasks.Task) string {
	if t.State != "SCHEDULED" && t.State != "RECURRING" {
		return ""
	}
	desc := tasks.ScheduleDescriptionFor(t)
	if desc == "" {
		return t.State
	}
	return fmt.Sprintf("%s - %s", t.State, desc)
}

// uiFormError answers a rejected new-task submit. htmx never swaps a 4xx
// response body, so the message also rides back on an HX-Trigger event
// that #task-form-error (inside the form, see new_task_form.html) turns
// into a visible inline error — otherwise a rejected submit looks like
// nothing happened at all. The plain-text body is kept for non-htmx
// callers (curl, the Go tests).
func (s *ServerConfig) uiFormError(c *gin.Context, status int, msg string) {
	payload, err := json.Marshal(gin.H{
		"task-form-error": gin.H{"error": msg},
	})
	if err != nil {
		log.WithField("err", err).Warn("ui: marshal HX-Trigger payload")
	} else {
		c.Header("HX-Trigger", string(payload))
	}
	c.String(status, msg)
}

// uiFormErrorPartial renders the message uiFormError signaled. It's the
// follow-up GET #task-form-error issues on the "task-form-error" event;
// rendering server-side (rather than writing the text in with JS) keeps
// escaping in one place.
func (s *ServerConfig) uiFormErrorPartial(c *gin.Context) {
	t := mustParsePartial("task-form-error", "flash.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "task-form-error", gin.H{"Error": c.Query("error")}); err != nil {
		log.WithField("err", err).Warn("ui: render task-form-error")
	}
}

// triggerTaskTypeWarnings sets an HX-Trigger response header that fires a
// "task-type-warnings" client-side event naming the just-submitted task
// type. #flash-area (see _layout.html) listens for that event and issues
// its own follow-up GET to uiTaskTypeWarningsPartial, which re-validates
// the type and renders any warning-level findings. Always fires (even when
// there are no warnings) so a later warning-free submission clears a stale
// message left by an earlier one.
// scheduled, when non-empty, rides along on the same event and is shown as
// a success flash ("Task scheduled. SCHEDULED - Once, at …") — the created
// task is waiting on a schedule rather than queued right now, which the
// refreshed rows alone don't make obvious.
func (s *ServerConfig) triggerTaskTypeWarnings(c *gin.Context, taskType, scheduled string) {
	payload, err := json.Marshal(gin.H{
		"task-type-warnings": gin.H{"type": taskType, "scheduled": scheduled},
	})
	if err != nil {
		log.WithField("err", err).Warn("ui: marshal HX-Trigger payload")
		return
	}
	c.Header("HX-Trigger", string(payload))
}

// uiTaskTypeWarningsPartial re-validates the named task type and renders
// any warning-level findings into #flash-area via a self-referential
// out-of-band swap. It's the follow-up request triggered by
// triggerTaskTypeWarnings, kept separate from the task-create response
// itself — see the comment in uiSubmitTask for why. See turtlemonvh/blanket#64.
func (s *ServerConfig) uiTaskTypeWarningsPartial(c *gin.Context) {
	typeName := c.Query("type")
	var findings []tasks.Finding
	if typeName != "" {
		if tt, err := tasks.FetchTaskType(typeName); err == nil {
			for _, f := range tasks.ValidateTaskType(tt, nil) {
				if f.Level != tasks.LevelError {
					findings = append(findings, f)
				}
			}
		}
	}
	t := mustParsePartial("flash-oob", "flash.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "flash-oob", gin.H{
		"TaskType":  typeName,
		"Findings":  findings,
		"Scheduled": c.Query("scheduled"),
	}); err != nil {
		log.WithField("err", err).Warn("ui: render flash-oob")
	}
}

// renderUI executes the layout with the page's content block bound.
func (s *ServerConfig) renderUI(c *gin.Context, t *template.Template, data gin.H) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		log.WithField("err", err).Warn("ui: render page")
	}
}

func (s *ServerConfig) sseStream(c *gin.Context, hub *EventHub, eventName string) {
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	seq := 0
	send := func(w io.Writer) {
		c.Writer.Header()["Content-Type"] = []string{"text/event-stream"}
		sse.Encode(c.Writer, sse.Event{
			Event: eventName,
			Data:  "refresh",
		})
		seq++
	}

	// Send an initial event so the client catches up immediately.
	c.Stream(func(w io.Writer) bool {
		if seq == 0 {
			send(w)
			return true
		}
		select {
		case <-ch:
			send(w)
		case <-time.After(30 * time.Second):
			fmt.Fprintf(w, ": keepalive\n\n")
		}
		return true
	})
}

func (s *ServerConfig) sseTaskEvents(c *gin.Context) {
	s.sseStream(c, s.TaskEvents, "tasks-changed")
}

func (s *ServerConfig) sseWorkerEvents(c *gin.Context) {
	s.sseStream(c, s.WorkerEvents, "workers-changed")
}
