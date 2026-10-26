package command

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/client"
)

// `blanket rm` of a task that no longer exists says so plainly, rather
// than echoing the server's "No item for id ..." text (#249).
func TestRmNotFoundMessage(t *testing.T) {
	id := "68b4c1f2a3e4d5b6c7a8f9e0"

	msg, ok := rmNotFoundMessage(id, &client.APIError{Status: http.StatusNotFound, Message: "No item for id " + id})
	assert.True(t, ok)
	assert.Equal(t, "error: 404 no such task "+id, msg)

	// Wrapped, as a caller adding context would.
	_, ok = rmNotFoundMessage(id, fmt.Errorf("delete: %w", &client.APIError{Status: http.StatusNotFound}))
	assert.True(t, ok)

	// Anything else falls through to printAPIError.
	_, ok = rmNotFoundMessage(id, &client.APIError{Status: http.StatusBadRequest, Message: "bad id"})
	assert.False(t, ok)
	_, ok = rmNotFoundMessage(id, errors.New("connection refused"))
	assert.False(t, ok)
}

// `rm --force` on a running task (#252): the first delete is refused, so
// it force-cancels and retries until the server lets the delete through
// (here, on the third try, as when the worker confirms the kill).
func TestForceRemoveTask_CancelsThenDeletes(t *testing.T) {
	running := &client.APIError{Status: http.StatusBadRequest, Message: "Cannot delete a task that is still running; cancel it first"}
	deletes, cancels := 0, 0
	del := func() error {
		deletes++
		if deletes < 3 {
			return running
		}
		return nil
	}
	cancel := func() error { cancels++; return nil }

	assert.NoError(t, forceRemoveTask(del, cancel, time.Second, time.Millisecond))
	assert.Equal(t, 3, deletes)
	assert.Equal(t, 2, cancels)
}

// A task that never becomes deletable: the last 400 comes back once the
// wait runs out.
func TestForceRemoveTask_GivesUp(t *testing.T) {
	pending := &client.APIError{Status: http.StatusBadRequest, Message: "Cannot delete a task until its worker confirms it has stopped; try again shortly"}
	err := forceRemoveTask(func() error { return pending }, func() error { return nil }, 20*time.Millisecond, time.Millisecond)
	assert.Equal(t, pending, err)
}

// Anything but a 400 (a 404, a connection error) is returned at once,
// without cancelling.
func TestForceRemoveTask_OtherErrorsReturnAtOnce(t *testing.T) {
	missing := &client.APIError{Status: http.StatusNotFound}
	cancels := 0
	err := forceRemoveTask(func() error { return missing }, func() error { cancels++; return nil }, time.Second, time.Millisecond)
	assert.Equal(t, missing, err)
	assert.Zero(t, cancels)
}
