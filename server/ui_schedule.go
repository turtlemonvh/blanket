package server

/*

Scheduling UI (turtlemonvh/blanket#98) — the three surfaces layered on top
of the #61/#94 scheduling backend:

  - /ui/upcoming             the Upcoming page: one-time SCHEDULED tasks
                             (cancelable inline) and live/paused series
                             templates (linked to their detail page).
  - /ui/tasks/:id            renders series_detail.html instead of
                             task_detail.html when the record is a series
                             template — same URL per task record, a
                             different template. See uiTaskDetailPage.
  - the series card          rendered on any child task's detail page and
                             linked compactly from its list row.

Everything mutating goes through a small set of /ui/series/:id/* endpoints
rather than calling the JSON API directly from htmx. They're thin wrappers
over the same pauseTaskById / resumeTaskById / cancelTaskById /
changeTaskScheduleById functions the REST handlers use, and exist for one
reason: they can re-render the schedule block as their response, so a
successful action swaps in fresh state and a rejected one (bad cron, wrong
state) swaps in the same block with the parser's message shown inline. The
JSON endpoints return `{}` / an error string, which htmx can't put anywhere
useful without a second round trip.

*/

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
)

// upcomingListLimit bounds how many records either Upcoming section will
// render. The same order of magnitude as the scheduler's own per-tick scan
// (DefaultSchedulerMaxScheduled), but much smaller: this is a page a human
// reads, and a thousand pending schedules is already past the point where
// a flat list is the right presentation.
const upcomingListLimit = 500

// isSeriesTemplate reports whether t is a recurring *template* — the
// record a cron submission creates, which spawns children instead of
// running itself — as opposed to a plain task or one of its child runs.
//
// CronExpr is the load-bearing part of the test: a STOPPED record is a
// cancelled template only if it still carries a cron expression (see
// tasks.ScheduleDescriptionFor, which draws the same distinction). A
// STOPPED *task* has none.
func isSeriesTemplate(t tasks.Task) bool {
	if t.CronExpr == "" {
		return false
	}
	switch t.State {
	case "RECURRING", "PAUSED", "STOPPED":
		return true
	}
	return false
}

// seriesStatusLabel renders a template's state in the vocabulary the issue
// asks for (live / paused / cancelled) rather than the raw state name.
func seriesStatusLabel(state string) string {
	switch state {
	case "RECURRING":
		return "Live"
	case "PAUSED":
		return "Paused"
	case "STOPPED":
		return "Cancelled"
	}
	return state
}

// SeriesView is the render-friendly projection of a series template, used
// by the series detail page, its schedule block, and the series card shown
// on a child task's detail page.
type SeriesView struct {
	Task tasks.Task
	// Found is false when a child task's ParentId names a template whose
	// record has since been deleted (DELETE /task/:id removes it outright,
	// unlike cancel). The card still renders, showing the id it can't
	// resolve, rather than silently dropping the link.
	Found       bool
	Id          string // hex id; set even when Found is false
	StatusLabel string // "Live" / "Paused" / "Cancelled"
	Description string // friendly cron text, e.g. "Every 5 minutes"
	// Live is true for a template that is still firing (RECURRING), i.e.
	// the only state where NextFireTs means anything.
	Live bool
	// Cancelled is true for a STOPPED template: the record is kept, but
	// pause/resume/change-schedule no longer apply to it.
	Cancelled bool
}

// buildSeriesView projects a template task into its render-friendly view.
func buildSeriesView(t tasks.Task) SeriesView {
	desc := ""
	if t.CronExpr != "" {
		desc = cronDescription(t.CronExpr)
	}
	return SeriesView{
		Task:        t,
		Found:       true,
		Id:          t.Id.Hex(),
		StatusLabel: seriesStatusLabel(t.State),
		Description: desc,
		Live:        t.State == "RECURRING",
		Cancelled:   t.State == "STOPPED",
	}
}

// cronDescription renders expr as friendly English, falling back to the
// raw expression if it somehow doesn't parse — a stored template's cron
// was validated on the way in, so this is belt-and-braces.
func cronDescription(expr string) string {
	desc, err := tasks.DescribeCron(expr)
	if err != nil {
		return expr
	}
	return desc
}

// lookupSeries resolves a child task's ParentId into a SeriesView for the
// series card. Returns nil when the task isn't part of a series at all.
func (s *ServerConfig) lookupSeries(parentId objectid.ObjectId) *SeriesView {
	if parentId.IsZero() {
		return nil
	}
	parent, err := s.DB.GetTask(parentId)
	if err != nil {
		log.WithFields(log.Fields{"parentId": parentId.Hex(), "err": err}).
			Debug("ui: series card parent lookup failed")
		return &SeriesView{Id: parentId.Hex()}
	}
	v := buildSeriesView(parent)
	return &v
}

// listTasksInStates returns every task in one of the given states, capped
// at upcomingListLimit.
func (s *ServerConfig) listTasksInStates(states ...string) ([]tasks.Task, error) {
	allowed := make(map[string]bool, len(states))
	for _, st := range states {
		allowed[st] = true
	}
	smallest, largest := fullIdRange()
	found, _, err := s.DB.GetTasks(&database.TaskSearchConf{
		Limit:             upcomingListLimit,
		AllowedTaskStates: allowed,
		SmallestId:        smallest,
		LargestId:         largest,
	})
	return found, err
}

// upcomingOneTime returns the SCHEDULED one-shot tasks, soonest first.
func (s *ServerConfig) upcomingOneTime() ([]tasks.Task, error) {
	found, err := s.listTasksInStates("SCHEDULED")
	if err != nil {
		return nil, err
	}
	sort.SliceStable(found, func(i, j int) bool {
		return found[i].ScheduledTs < found[j].ScheduledTs
	})
	return found, nil
}

// upcomingSeries returns the live and paused series templates. STOPPED
// (cancelled) templates are deliberately excluded — they keep their record
// and stay reachable at /ui/tasks/:id, but they are not "upcoming".
//
// Ordering: live templates first, soonest next-fire at the top, then the
// paused ones most-recently-paused first. A paused template's NextFireTs
// is whatever it was when it was paused (resume recomputes it from now),
// so sorting the whole list on that one field would interleave the two
// groups on a number that means nothing for half of them.
func (s *ServerConfig) upcomingSeries() ([]SeriesView, error) {
	found, err := s.listTasksInStates("RECURRING", "PAUSED")
	if err != nil {
		return nil, err
	}
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if a.State != b.State {
			return a.State == "RECURRING"
		}
		if a.State == "RECURRING" {
			return a.NextFireTs < b.NextFireTs
		}
		return a.PausedTs > b.PausedTs
	})
	views := make([]SeriesView, 0, len(found))
	for _, t := range found {
		views = append(views, buildSeriesView(t))
	}
	return views, nil
}

// uiUpcomingPage renders the Upcoming page: scheduled one-shot tasks and
// live/paused series templates, in two independently refreshing sections.
func (s *ServerConfig) uiUpcomingPage(c *gin.Context) {
	oneTime, err := s.upcomingOneTime()
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	series, err := s.upcomingSeries()
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	t := mustParseUIPage("upcoming",
		"ui/templates/upcoming.html",
		"ui/templates/upcoming_onetime_rows.html",
		"ui/templates/upcoming_series_rows.html")
	s.renderUI(c, t, gin.H{
		"Title":   "Upcoming",
		"OneTime": oneTime,
		"Series":  series,
	})
}

// uiUpcomingOneTimeRowsPartial renders just the one-time tbody, for htmx
// swaps (SSE push, or a re-fetch after cancelling a row).
func (s *ServerConfig) uiUpcomingOneTimeRowsPartial(c *gin.Context) {
	oneTime, err := s.upcomingOneTime()
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	t := mustParsePartial("upcoming-onetime-rows", "upcoming_onetime_rows.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "upcoming-onetime-rows", gin.H{"OneTime": oneTime}); err != nil {
		log.WithField("err", err).Warn("ui: render upcoming-onetime-rows")
	}
}

// uiUpcomingSeriesRowsPartial renders just the series tbody.
func (s *ServerConfig) uiUpcomingSeriesRowsPartial(c *gin.Context) {
	series, err := s.upcomingSeries()
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	t := mustParsePartial("upcoming-series-rows", "upcoming_series_rows.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(c.Writer, "upcoming-series-rows", gin.H{"Series": series}); err != nil {
		log.WithField("err", err).Warn("ui: render upcoming-series-rows")
	}
}

// renderSeriesDetail renders the series detail page for a template task.
// Called from uiTaskDetailPage, so a series lives at the same /ui/tasks/:id
// URL as any other task record; only the template differs.
func (s *ServerConfig) renderSeriesDetail(c *gin.Context, task tasks.Task) {
	// Past runs, newest first — the opposite of the main Tasks list's
	// default, because on a series page the interesting run is the one
	// that just happened, not the first one ever.
	smallest, largest := fullIdRange()
	runs, _, err := s.DB.GetTasks(&database.TaskSearchConf{
		Limit:          upcomingListLimit,
		ReverseSort:    true,
		FilterParentId: true,
		ParentId:       task.Id,
		SmallestId:     smallest,
		LargestId:      largest,
	})
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}

	t := mustParseUIPage("series-detail",
		"ui/templates/series_detail.html",
		"ui/templates/series_schedule.html",
		"ui/templates/tasks_rows.html")
	s.renderUI(c, t, gin.H{
		"Title":  "Series " + task.Id.Hex()[:8],
		"Series": buildSeriesView(task),
		"Tasks":  runs,
		// Every row below belongs to this series; repeating "part of
		// series …" on each one would be noise.
		"HideSeriesLink": true,
	})
}

// renderSeriesSchedule writes the schedule block for task, optionally with
// an error message shown inline (a rejected cron expression, a pause on an
// already-cancelled series). Written as the response of every
// /ui/series/:id/* action, and fetched on its own by
// /ui/partials/series-schedule.
func (s *ServerConfig) renderSeriesSchedule(c *gin.Context, task tasks.Task, status int, errMsg string) {
	t := mustParsePartial("series-schedule", "series_schedule.html")
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(status)
	if err := t.ExecuteTemplate(c.Writer, "series-schedule", gin.H{
		"Series": buildSeriesView(task),
		"Error":  errMsg,
	}); err != nil {
		log.WithField("err", err).Warn("ui: render series-schedule")
	}
}

// seriesFromParam resolves the :id path parameter to a task, writing the
// error response itself and returning ok=false when it can't.
func (s *ServerConfig) seriesFromParam(c *gin.Context) (tasks.Task, bool) {
	taskId, err := SafeObjectId(c.Param("id"))
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return tasks.Task{}, false
	}
	task, err := s.DB.GetTask(taskId)
	if err != nil {
		c.String(http.StatusNotFound, err.Error())
		return tasks.Task{}, false
	}
	return task, true
}

// uiSeriesSchedulePartial re-renders the schedule block on its own —
// used by the series detail page's initial load path and by tests.
func (s *ServerConfig) uiSeriesSchedulePartial(c *gin.Context) {
	idStr := c.Query("id")
	taskId, err := SafeObjectId(idStr)
	if err != nil {
		c.String(http.StatusBadRequest, err.Error())
		return
	}
	task, err := s.DB.GetTask(taskId)
	if err != nil {
		c.String(http.StatusNotFound, err.Error())
		return
	}
	s.renderSeriesSchedule(c, task, http.StatusOK, "")
}

// uiSeriesAction is the shared body of the pause / resume / cancel
// endpoints: run `act`, re-read the record either way, and answer with the
// re-rendered schedule block (carrying the error message inline when the
// action was rejected).
func (s *ServerConfig) uiSeriesAction(c *gin.Context, act func(task tasks.Task) error) {
	task, ok := s.seriesFromParam(c)
	if !ok {
		return
	}
	errMsg := ""
	status := http.StatusOK
	if err := act(task); err != nil {
		errMsg = err.Error()
		status = http.StatusOK // 200 so htmx swaps the block; the error is in it
	}
	// Re-read so the block reflects whatever actually landed in the DB.
	if updated, err := s.DB.GetTask(task.Id); err == nil {
		task = updated
	}
	s.renderSeriesSchedule(c, task, status, errMsg)
}

func (s *ServerConfig) uiSeriesPause(c *gin.Context) {
	s.uiSeriesAction(c, func(task tasks.Task) error {
		return s.pauseTaskById(c.Request.Context(), task.Id)
	})
}

func (s *ServerConfig) uiSeriesResume(c *gin.Context) {
	s.uiSeriesAction(c, func(task tasks.Task) error {
		return s.resumeTaskById(c.Request.Context(), task.Id)
	})
}

func (s *ServerConfig) uiSeriesCancel(c *gin.Context) {
	s.uiSeriesAction(c, func(task tasks.Task) error {
		return s.cancelTaskById(c.Request.Context(), task.Id, false)
	})
}

// uiSeriesChangeSchedule applies a new cron expression from the schedule
// editor's form post. The REST endpoint (PUT /task/:id/schedule) takes a
// JSON body; this takes the form field and hands it to the same
// changeTaskScheduleById, so an invalid expression comes back as the
// parser's own message rendered inline in the block.
func (s *ServerConfig) uiSeriesChangeSchedule(c *gin.Context) {
	cronExpr := c.PostForm("cron")
	s.uiSeriesAction(c, func(task tasks.Task) error {
		return s.changeTaskScheduleById(c.Request.Context(), task.Id, map[string]interface{}{
			"cron": cronExpr,
		})
	})
}
