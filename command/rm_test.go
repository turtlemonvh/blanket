package command

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

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
