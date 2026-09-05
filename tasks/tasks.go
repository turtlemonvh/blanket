package tasks

import (
	"bytes"
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"os"
	"os/exec"
	"text/template"
)

var (
	// SCHEDULED, RECURRING, and PAUSED are additive states layered onto
	// the original WAITING-first state machine (see docs/task_flow.md):
	//   - SCHEDULED: submitted with a future scheduledTs; not yet in the
	//     claimable queue. The scheduler loop (server/scheduler.go)
	//     promotes it to WAITING once due.
	//   - RECURRING: a template task carrying a cronExpr. It never runs
	//     itself; the scheduler loop spawns a child task (a normal
	//     WAITING-onward task, linked via parentId) at each cron fire and
	//     advances the template's nextFireTs.
	//   - PAUSED: a RECURRING template that has been paused
	//     (PUT /task/:id/pause); carries a nonzero pausedTs. The
	//     scheduler never fires a PAUSED template (it only looks at
	//     RECURRING ones); PUT /task/:id/resume returns it to RECURRING.
	// A RECURRING or PAUSED template is stopped for good via
	// PUT /task/:id/cancel (-> STOPPED, record kept) or DELETE /task/:id
	// (record removed); either way, an already-spawned child keeps
	// running to its own completion independently.
	ValidTaskStates         = []string{"WAITING", "SCHEDULED", "RECURRING", "PAUSED", "CLAIMED", "RUNNING", "ERROR", "SUCCESS", "STOPPED", "TIMEDOUT"}
	ValidTerminalTaskStates = []string{"ERROR", "SUCCESS", "STOPPED", "TIMEDOUT"}
)

// FIXME: Reason field for why a task was stopped? audit trail of actions?
type Task struct {
	Id            objectid.ObjectId `json:"id"`            // time sortable id
	Pid           int               `json:"pid"`           // the process id used to run the task on disk
	CreatedTs     int64             `json:"createdTs"`     // when it was first added to the queue
	StartedTs     int64             `json:"startedTs"`     // when it was pulled from the queue
	LastUpdatedTs int64             `json:"lastUpdatedTs"` // last time any information changed
	TypeId        string            `json:"type"`          // String name
	ResultDir     string            `json:"resultDir"`     // Full path
	TypeDigest    string            `json:"typeDigest"`    // version hash of config file
	Timeout       int64             `json:"timeout"`       // The max time the task is allowed to run
	State         string            `json:"state"`         // See ValidTaskStates
	WorkerId      objectid.ObjectId `json:"workerId"`      // Id of the worker that processed this task; set when CLAIMED
	Progress      int               `json:"progress"`      // 0-100
	ExecEnv       map[string]string `json:"defaultEnv"`    // Combined with default env
	Tags          []string          `json:"tags"`          // tags for capabilities of workers

	// Scheduling (turtlemonvh/blanket#61). All additive: zero values
	// (0, "", zero ObjectId) mean "no scheduling", so records written
	// before this feature existed still load and behave exactly as
	// before.
	ScheduledTs int64             `json:"scheduledTs"` // unix ts before which this task must not be queued; 0 = no delay. Set from a one-shot "notBefore" submission.
	CronExpr    string            `json:"cronExpr"`    // standard 5-field cron expression; non-empty makes this a RECURRING (or PAUSED) template that spawns children instead of running itself
	NextFireTs  int64             `json:"nextFireTs"`  // next time a RECURRING template should fire; meaningful only when CronExpr != "" and State == "RECURRING"
	ParentId    objectid.ObjectId `json:"parentId"`    // id of the RECURRING template that spawned this task, if any; zero ObjectId for a task submitted directly or a template itself
	PausedTs    int64             `json:"pausedTs"`    // unix ts this template was paused at; 0 unless State == "PAUSED". Set by PUT /task/:id/pause, cleared by /resume.
}

func (t *Task) String() string {
	return fmt.Sprintf("%s %s [%d]", t.TypeId, t.Id.Hex(), t.CreatedTs)
}

// MarshalJSON adds a computed "scheduleDescription" field to every Task's
// JSON representation (GET /task/:id, GET /task/, POST /task/'s response,
// ...) without persisting it to the DB record itself -- it's derived
// entirely from State/CronExpr/ScheduledTs, which are already stored, so
// there's nothing to keep in sync on write. See ScheduleDescriptionFor.
func (t Task) MarshalJSON() ([]byte, error) {
	type alias Task // avoids infinite recursion into this same method
	return json.Marshal(struct {
		alias
		ScheduleDescription string `json:"scheduleDescription"`
	}{
		alias:               alias(t),
		ScheduleDescription: ScheduleDescriptionFor(t),
	})
}

// Get the command object used to run this task
// Task type is passed in so the same config is used for every step
// Maybe task type TOML should be copied when task is added so that it is saved
func (t *Task) GetCmd(tt *TaskType) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	var err error

	// Evaluate template
	tmpl, err := template.New("tasks").Parse(tt.Config.GetString("command"))
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("problem parsing task type's 'command' parameter as go template")
		return cmd, err
	}
	var cmdString bytes.Buffer
	err = tmpl.Execute(&cmdString, t.ExecEnv)
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"taskId": t.Id,
		}).Error("error evaluating template for command")
		return cmd, err
	}

	executor := tt.Config.GetString("executor")
	if executor == "" {
		executor = "bash"
	}
	switch executor {
	case "cmd":
		cmd = exec.Command("cmd", "/c", cmdString.String())
	case "powershell":
		cmd = exec.Command("powershell", "-Command", cmdString.String())
	default:
		cmd = exec.Command(executor, "-c", cmdString.String())
	}

	// Modify execution environment with env variables
	// e.g. http://craigwickesser.com/2015/02/golang-cmd-with-custom-environment/
	env := os.Environ()
	for k, v := range t.ExecEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env

	return cmd, nil
}

func (t *Task) GetTaskType() (*TaskType, error) {
	return FetchTaskType(t.TypeId)
}
