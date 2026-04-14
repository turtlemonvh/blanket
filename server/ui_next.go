package server

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

//go:embed all:ui_next/templates all:ui_next/static
var uiNextFS embed.FS

// uiNextStaticFS serves /ui-next/static/*.
func uiNextStaticFS() http.FileSystem {
	sub, err := fs.Sub(uiNextFS, "ui_next/static")
	if err != nil {
		panic(err)
	}
	return http.FS(sub)
}

// Template funcs shared across all pages.
var uiNextFuncs = template.FuncMap{
	"add":   func(a, b int) int { return a + b },
	"join":  strings.Join,
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
		return state == "WAITING" || state == "CLAIMED" || state == "RUNNING"
	},
	"isTerminal": func(state string) bool {
		for _, s := range tasks.ValidTerminalTaskStates {
			if s == state {
				return true
			}
		}
		return false
	},
}

// uiNextTemplates is populated lazily per page so the partial templates
// (tasks-rows, workers-rows, …) can be included alongside their parent.
var uiNextTemplates = map[string]*template.Template{}

// mustParseUINextPage parses layout + the named page (+ optional partial files)
// and caches the result. Panics on error — templates are embedded, so any
// parse failure is a build-time bug.
func mustParseUINextPage(name string, files ...string) *template.Template {
	if t, ok := uiNextTemplates[name]; ok {
		return t
	}
	paths := append([]string{"ui_next/templates/_layout.html"}, files...)
	t, err := template.New(name).Funcs(uiNextFuncs).ParseFS(uiNextFS, paths...)
	if err != nil {
		panic(fmt.Errorf("ui-next: parse %s: %w", name, err))
	}
	uiNextTemplates[name] = t
	return t
}

// mustParsePartial parses standalone partial template(s) without the layout.
func mustParsePartial(name string, files ...string) *template.Template {
	key := "partial:" + name
	if t, ok := uiNextTemplates[key]; ok {
		return t
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, "ui_next/templates/"+f)
	}
	t, err := template.New(name).Funcs(uiNextFuncs).ParseFS(uiNextFS, paths...)
	if err != nil {
		panic(fmt.Errorf("ui-next: parse partial %s: %w", name, err))
	}
	uiNextTemplates[key] = t
	return t
}

// TaskTypeView is the render-friendly projection of tasks.TaskType.
type TaskTypeView struct {
	Name        string
	Tags        []string
	LoadedTs    int64
	ConfigFile  string
	VersionHash string
}

// SettingView is one row on the About page.
type SettingView struct {
	Key   string
	Value string
}

func readTaskTypeViews() []TaskTypeView {
	tts, err := tasks.ReadTypes()
	if err != nil {
		log.WithField("err", err).Warn("ui-next: read task types")
		return nil
	}
	views := make([]TaskTypeView, 0, len(tts))
	for _, tt := range tts {
		cfg := tt.Config
		tags := cfg.GetStringSlice("tags")
		views = append(views, TaskTypeView{
			Name:        tt.GetName(),
			Tags:        tags,
			LoadedTs:    tt.LoadedTs,
			ConfigFile:  tt.ConfigFile,
			VersionHash: tt.ConfigVersionHash,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views
}

// uiNextTasksPage renders the tasks list page.
func (s *ServerConfig) uiNextTasksPage(c *gin.Context) {
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
	t := mustParseUINextPage("tasks", "ui_next/templates/tasks.html", "ui_next/templates/tasks_rows.html")
	s.renderUINext(c, t, gin.H{
		"Title":         "Tasks",
		"Tasks":         tks,
		"TaskStates":    tasks.ValidTaskStates,
		"TaskTypeNames": typeNames,
	})
}

// uiNextTaskDetailPage renders one task's metadata, env vars, and log stream.
func (s *ServerConfig) uiNextTaskDetailPage(c *gin.Context) {
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
	t := mustParseUINextPage("task-detail", "ui_next/templates/task_detail.html")
	s.renderUINext(c, t, gin.H{"Title": "Task " + taskId.Hex()[:8], "Task": task})
}

// uiNextTasksRowsPartial renders just the tbody for htmx swaps.
func (s *ServerConfig) uiNextTasksRowsPartial(c *gin.Context) {
	tks, _, err := s.DB.GetTasks(database.TaskSearchConfFromContext(c))
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	t := mustParsePartial("tasks-rows", "tasks_rows.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "tasks-rows", gin.H{"Tasks": tks}); err != nil {
		log.WithField("err", err).Warn("ui-next: render tasks-rows")
	}
}

func (s *ServerConfig) uiNextWorkersPage(c *gin.Context) {
	ws, err := s.DB.GetWorkers()
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	t := mustParseUINextPage("workers", "ui_next/templates/workers.html", "ui_next/templates/workers_rows.html")
	s.renderUINext(c, t, gin.H{"Title": "Workers", "Workers": ws})
}

// uiNextNewWorkerPartial returns the "new worker" form.
func (s *ServerConfig) uiNextNewWorkerPartial(c *gin.Context) {
	t := mustParsePartial("new-worker-form", "new_worker_form.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "new-worker-form", nil); err != nil {
		log.WithField("err", err).Warn("ui-next: render new-worker-form")
	}
}

// uiNextSubmitWorker spawns a daemon worker from form input and returns
// the refreshed rows partial. Mirrors server.launchWorker without the JSON
// response shape.
func (s *ServerConfig) uiNextSubmitWorker(c *gin.Context) {
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
	s.uiNextWorkersRowsPartial(c)
}

func (s *ServerConfig) uiNextWorkersRowsPartial(c *gin.Context) {
	ws, err := s.DB.GetWorkers()
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	t := mustParsePartial("workers-rows", "workers_rows.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "workers-rows", gin.H{"Workers": ws}); err != nil {
		log.WithField("err", err).Warn("ui-next: render workers-rows")
	}
}

func (s *ServerConfig) uiNextTaskTypesPage(c *gin.Context) {
	t := mustParseUINextPage("task-types",
		"ui_next/templates/task_types.html",
		"ui_next/templates/task_types_rows.html")
	s.renderUINext(c, t, gin.H{"Title": "Task Types", "TaskTypes": readTaskTypeViews()})
}

func (s *ServerConfig) uiNextTaskTypesRowsPartial(c *gin.Context) {
	t := mustParsePartial("task-types-rows", "task_types_rows.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "task-types-rows", gin.H{"TaskTypes": readTaskTypeViews()}); err != nil {
		log.WithField("err", err).Warn("ui-next: render task-types-rows")
	}
}

func (s *ServerConfig) uiNextAboutPage(c *gin.Context) {
	keys := viper.AllKeys()
	sort.Strings(keys)
	settings := make([]SettingView, 0, len(keys))
	for _, k := range keys {
		settings = append(settings, SettingView{Key: k, Value: viper.GetString(k)})
	}
	t := mustParseUINextPage("about", "ui_next/templates/about.html")
	s.renderUINext(c, t, gin.H{"Title": "About", "Settings": settings})
}

// uiNextNewTaskPartial returns the "new task" form pre-populated with types.
func (s *ServerConfig) uiNextNewTaskPartial(c *gin.Context) {
	t := mustParsePartial("new-task-form", "new_task_form.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "new-task-form", gin.H{"TaskTypes": readTaskTypeViews()}); err != nil {
		log.WithField("err", err).Warn("ui-next: render new-task-form")
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

// uiNextTaskTypeEnvPartial renders the env-var editor for a chosen task type.
func (s *ServerConfig) uiNextTaskTypeEnvPartial(c *gin.Context) {
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
		log.WithField("err", err).Warn("ui-next: render task-type-env")
	}
}

// uiNextBlankPartial is used to clear a target on Cancel.
func (s *ServerConfig) uiNextBlankPartial(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, "")
}

// uiNextCustomEnvRowPartial returns one empty "custom setting" row that
// the env editor appends to its tbody when the user clicks "Add custom setting".
func (s *ServerConfig) uiNextCustomEnvRowPartial(c *gin.Context) {
	t := mustParsePartial("custom-env-row", "custom_env_row.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "custom-env-row", nil); err != nil {
		log.WithField("err", err).Warn("ui-next: render custom-env-row")
	}
}

// uiNextSubmitTask handles the New Task form submit and returns fresh rows.
// Form fields named `env.<NAME>` are collected into the task's ExecEnv.
func (s *ServerConfig) uiNextSubmitTask(c *gin.Context) {
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
			c.String(http.StatusBadRequest, fmt.Sprintf("missing required env var: %s", name))
			return
		}
	}

	t, err := tt.NewTask(childEnv)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.DB.SaveTask(&t); err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Q.AddTask(&t); err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	s.uiNextTasksRowsPartial(c)
}

// renderUINext executes the layout with the page's content block bound.
func (s *ServerConfig) renderUINext(c *gin.Context, t *template.Template, data gin.H) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "layout", data); err != nil {
		log.WithField("err", err).Warn("ui-next: render page")
	}
}

