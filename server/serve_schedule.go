package server

/*

Series lifecycle for RECURRING templates (turtlemonvh/blanket#61 rework,
PR #94 review): pause/resume/change-schedule, plus the friendly-text
GET /schedule/describe endpoint the create form's live preview calls.
Cancel (-> STOPPED) and delete already existed and live in
serve_tasks.go / server.go; this file is the rest of the lifecycle:

	SCHEDULED  --PUT .../schedule {notBefore}-->  SCHEDULED (new ScheduledTs)
	RECURRING  --PUT .../pause-->                 PAUSED
	PAUSED     --PUT .../resume-->                RECURRING (NextFireTs recomputed)
	RECURRING/PAUSED --PUT .../schedule {cron}-->  same state, new CronExpr
	                                                (NextFireTs recomputed too, unless PAUSED)

All three "By" functions below follow the same read-check-write shape as
cancelTaskById in serve_tasks.go: fetch, validate the current state,
mutate, SaveTask, notify. They're plain GetTask+SaveTask (not
lib/bolt's atomic ModifyTaskInBoltTransaction helper) -- consistent with
how the scheduler loop itself already updates these same fields
(promoteDueScheduledTasks, fireOnce), and not a meaningfully bigger race
window than that loop already accepts for a feature at this scale.

*/

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
)

// ErrTaskNotPausable is returned by pauseTaskById when the task isn't a
// live RECURRING template.
var ErrTaskNotPausable = errors.New("task must be in state RECURRING to pause")

// ErrTaskNotResumable is returned by resumeTaskById when the task isn't
// currently PAUSED.
var ErrTaskNotResumable = errors.New("task must be in state PAUSED to resume")

// ErrTaskScheduleNotChangeable is returned by changeTaskScheduleById when
// the task's current state doesn't accept the kind of schedule change
// being requested (a "cron" body needs RECURRING/PAUSED; a "notBefore"
// body needs SCHEDULED).
var ErrTaskScheduleNotChangeable = errors.New("task's current state does not accept this schedule change")

// pauseTaskById transitions a RECURRING template to PAUSED, recording
// PausedTs. No scheduler change is needed to make this take effect:
// fireDueRecurringTasks only ever looks at templates in state RECURRING
// (see server/scheduler.go), so a PAUSED one is already excluded.
func (s *ServerConfig) pauseTaskById(ctx context.Context, taskId objectid.ObjectId) error {
	task, err := s.DB.GetTask(taskId)
	if err != nil {
		return err
	}
	if task.State != "RECURRING" {
		return ErrTaskNotPausable
	}

	now := time.Now()
	task.State = "PAUSED"
	task.PausedTs = now.Unix()
	task.LastUpdatedTs = now.Unix()
	if err := s.DB.SaveTask(&task); err != nil {
		return err
	}
	s.TaskEvents.Notify()
	return nil
}

// resumeTaskById transitions a PAUSED template back to RECURRING, clears
// PausedTs, and recomputes NextFireTs from now -- so a template paused for
// a long stretch doesn't immediately fire a backlog of "missed" runs on
// resume.
func (s *ServerConfig) resumeTaskById(ctx context.Context, taskId objectid.ObjectId) error {
	task, err := s.DB.GetTask(taskId)
	if err != nil {
		return err
	}
	if task.State != "PAUSED" {
		return ErrTaskNotResumable
	}

	now := time.Now()
	next, err := tasks.NextCronFire(task.CronExpr, now)
	if err != nil {
		return err
	}
	task.State = "RECURRING"
	task.PausedTs = 0
	task.NextFireTs = next.Unix()
	task.LastUpdatedTs = now.Unix()
	if err := s.DB.SaveTask(&task); err != nil {
		return err
	}
	s.TaskEvents.Notify()
	return nil
}

// changeTaskScheduleById applies a PUT /task/:id/schedule body to task
// taskId. req must set exactly one of:
//
//   - "cron": a standard 5-field cron expression, valid only when taskId
//     names a RECURRING or PAUSED template. Replaces CronExpr; if the
//     template is RECURRING (not paused), NextFireTs is recomputed from
//     now too -- a PAUSED template's NextFireTs is recomputed on resume
//     instead, per resumeTaskById.
//   - "notBefore": the same duration/RFC3339/unix-seconds shapes
//     ParseNotBefore accepts on submit, valid only when taskId names a
//     SCHEDULED one-shot task. The resolved time must still be in the
//     future -- unlike submit's notBefore, there's no "past means run
//     immediately" fallback here, since silently promoting a task out of
//     SCHEDULED as a side effect of an edit would be surprising.
//
// Any other state for the given field, or a request setting both fields
// (or neither), is rejected with ErrTaskScheduleNotChangeable or a plain
// validation error respectively -- both map to 400 in changeTaskSchedule.
func (s *ServerConfig) changeTaskScheduleById(ctx context.Context, taskId objectid.ObjectId, req map[string]interface{}) error {
	task, err := s.DB.GetTask(taskId)
	if err != nil {
		return err
	}

	cronExpr, _ := req["cron"].(string)
	notBefore, _ := req["notBefore"].(string)

	if cronExpr != "" && notBefore != "" {
		return fmt.Errorf("'cron' and 'notBefore' are mutually exclusive")
	}

	now := time.Now()
	switch {
	case cronExpr != "":
		if task.State != "RECURRING" && task.State != "PAUSED" {
			return ErrTaskScheduleNotChangeable
		}
		next, err := tasks.NextCronFire(cronExpr, now)
		if err != nil {
			return err
		}
		task.CronExpr = cronExpr
		if task.State == "RECURRING" {
			task.NextFireTs = next.Unix()
		}

	case notBefore != "":
		if task.State != "SCHEDULED" {
			return ErrTaskScheduleNotChangeable
		}
		ts, err := tasks.ParseNotBefore(notBefore, now)
		if err != nil {
			return err
		}
		if ts <= now.Unix() {
			return fmt.Errorf("'notBefore' must resolve to a time in the future")
		}
		task.ScheduledTs = ts

	default:
		return fmt.Errorf("request body must set exactly one of 'cron' or 'notBefore'")
	}

	task.LastUpdatedTs = now.Unix()
	if err := s.DB.SaveTask(&task); err != nil {
		return err
	}
	s.TaskEvents.Notify()
	return nil
}

/*
 * Request handlers
 */

func (s *ServerConfig) pauseTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	taskId, err := s.getTaskId(c)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}

	if err := s.pauseTaskById(c.Request.Context(), taskId); err != nil {
		c.String(statusForDBError(err, http.StatusBadRequest), MakeErrorString(err.Error()))
		return
	}
	c.String(http.StatusOK, `{}`)
}

func (s *ServerConfig) resumeTask(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	taskId, err := s.getTaskId(c)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}

	if err := s.resumeTaskById(c.Request.Context(), taskId); err != nil {
		c.String(statusForDBError(err, http.StatusBadRequest), MakeErrorString(err.Error()))
		return
	}
	c.String(http.StatusOK, `{}`)
}

func (s *ServerConfig) changeTaskSchedule(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	taskId, err := s.getTaskId(c)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}

	var req map[string]interface{}
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.String(http.StatusBadRequest, MakeErrorString("Error decoding JSON in request body."))
		return
	}

	if err := s.changeTaskScheduleById(c.Request.Context(), taskId, req); err != nil {
		c.String(statusForDBError(err, http.StatusBadRequest), MakeErrorString(err.Error()))
		return
	}
	c.String(http.StatusOK, `{}`)
}

// describeSchedule backs GET /schedule/describe?cron=<expr>: a friendly
// description plus the next few fire times, for the create form's live
// preview (and anywhere else a caller wants to sanity-check a cron
// expression before submitting it).
func (s *ServerConfig) describeSchedule(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	expr := c.Query("cron")
	if expr == "" {
		c.String(http.StatusBadRequest, MakeErrorString("query parameter 'cron' is required"))
		return
	}

	desc, err := tasks.DescribeCron(expr)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}
	fireTimes, err := tasks.NextCronFires(expr, time.Now(), 3)
	if err != nil {
		c.String(http.StatusBadRequest, MakeErrorString(err.Error()))
		return
	}
	next := make([]string, len(fireTimes))
	for i, ft := range fireTimes {
		next[i] = ft.Local().Format(time.RFC3339)
	}

	c.JSON(http.StatusOK, gin.H{
		"cron":        expr,
		"description": desc,
		"next":        next,
	})
}
