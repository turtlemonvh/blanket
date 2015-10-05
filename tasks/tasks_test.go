package tasks

import (
	"testing"
)

func TestStringTask(t *testing.T) {
	t1 := Task{1111111111, 1111111112, "Animal"}

	if t1.String() != "Animal - 1111111111" {
		t.Fatalf("bad: %s %s", t1.String(), "Animal - 1111111111")
	}
}
