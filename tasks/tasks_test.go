package tasks

import (
	"testing"
)

func TestStringTaskType(t *testing.T) {
	t1 := TaskType{1111111111, 1111111112, "Animal", map[string]string{"thing": "cat"}, "."}

	if t1.String() != "Animal - 1111111111" {
		t.Fatalf("bad: %s %s", t1.String(), "Animal - 1111111111")
	}
}

/*

Tests for filling in a template with a task instance context

- https://golang.org/pkg/text/template/

*/
