package tasks

import (
	"testing"
)

func TestStringTaskType(t *testing.T) {
	id := "65c42a37-130a-4537-8804-1832419b90b3"
	t1 := TaskType{
		Id:            id,
		CreatedTs:     1111111111,
		LastUpdatedTs: 1111111112,
		Type:          "Animal",
		DefaultEnv:    map[string]string{"thing": "cat"},
		//ConfigPath:    fmt.Sprintf("tasks/%s/", id),
	}

	if t1.String() != "Animal (65c42a37-130a-4537-8804-1832419b90b3) [1111111111]" {
		t.Fatalf("bad: %s %s", t1.String(), "Animal [1111111111]")
	}
}

/*

Tests for filling in a template with a task instance context

- https://golang.org/pkg/text/template/

*/
