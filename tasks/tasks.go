package tasks

import (
	"fmt"
)

// Arguments are typed per task
type Task struct {
	CreatedTs     int    `json:"createdTs"`
	LastUpdatedTs int    `json:"lastUpdatedTs"`
	Type          string `json:"type"`
}

func (t *Task) String() string {
	return fmt.Sprintf("%s - %d", t.Type, t.CreatedTs)
}
