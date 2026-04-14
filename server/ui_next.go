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
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
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
	t := mustParseUINextPage("tasks", "ui_next/templates/tasks.html", "ui_next/templates/tasks_rows.html")
	s.renderUINext(c, t, gin.H{"Title": "Tasks", "Tasks": tks})
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

// uiNextBlankPartial is used to clear a target on Cancel.
func (s *ServerConfig) uiNextBlankPartial(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, "")
}

// uiNextSubmitTask handles the New Task form submit and returns fresh rows.
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
	if tt.HasRequiredEnv() {
		c.String(http.StatusBadRequest, "task type has required env vars; use the API for now")
		return
	}
	t, err := tt.NewTask(map[string]string{})
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

