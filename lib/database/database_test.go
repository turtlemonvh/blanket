package database

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/tasks"
)

// Every task state has an answer from DELETE /task/:id's guard
// (turtlemonvh/blanket#252); a new state added to ValidTaskStates without
// a decision here fails this test.
func TestCheckTaskDeletable_EveryState(t *testing.T) {
	want := map[string]error{
		"WAITING":   nil,
		"SCHEDULED": nil,
		"RECURRING": nil,
		"PAUSED":    nil,
		"CLAIMED":   ErrTaskStillRunning,
		"RUNNING":   ErrTaskStillRunning,
		"ERROR":     nil,
		"SUCCESS":   nil,
		"STOPPED":   nil,
		"TIMEDOUT":  nil,
	}
	assert.Len(t, want, len(tasks.ValidTaskStates))
	now := time.Now()
	for _, state := range tasks.ValidTaskStates {
		expected, ok := want[state]
		if !assert.True(t, ok, "no expectation for state %s", state) {
			continue
		}
		assert.Equal(t, expected, CheckTaskDeletable(&tasks.Task{State: state}, now), state)
	}
}

func TestCheckTaskDeletable_StopPending(t *testing.T) {
	now := time.Now()
	pending := &tasks.Task{State: "STOPPED", StopPendingTs: now.Unix()}
	assert.Equal(t, ErrTaskStopPending, CheckTaskDeletable(pending, now))
	assert.NoError(t, CheckTaskDeletable(pending, now.Add(StopAckGrace+time.Second)))
}
