package server

import (
	"errors"
	"fmt"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"net/http"
)

const (
	MAX_REQUEST_TIME_SECONDS = 5
)

// Utility functions

func MakeErrorString(errmsg string) string {
	return fmt.Sprintf(`{"error": "%s"}`, errmsg)
}

// Return just the keys for a bool map
func MapKeys(m map[string]bool) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// InvalidIdError indicates a path or query parameter failed to parse as a
// lib/objectid.ObjectId -- the id is malformed, not merely absent (see
// #115). Every handler that resolves an id maps this to 400; a
// well-formed but absent id surfaces instead as a
// database.ItemNotFoundError from the DB layer, which statusForDBError
// maps to 404.
type InvalidIdError struct {
	Value string
}

func (e InvalidIdError) Error() string {
	return fmt.Sprintf("'%s' is not a valid id", e.Value)
}

// SafeObjectId parses idStr as an objectid.ObjectId, returning an
// InvalidIdError if it doesn't parse. The single place every :id
// path/query parameter (task or worker) is resolved -- see getTaskId and
// getWorkerId, its ServerConfig-bound wrappers for the common c.Param("id")
// case.
func SafeObjectId(idStr string) (objectid.ObjectId, error) {
	if !objectid.IsObjectIdHex(idStr) {
		return objectid.ObjectId{}, InvalidIdError{Value: idStr}
	}
	return objectid.ObjectIdHex(idStr), nil
}

// statusForDBError maps a database.ItemNotFoundError (a missing worker or
// task id) to 404, consistently across handlers. Any other error keeps
// whatever status the caller was already using for it (fallback).
func statusForDBError(err error, fallback int) int {
	if _, ok := err.(database.ItemNotFoundError); ok {
		return http.StatusNotFound
	}
	return fallback
}

// statusForTransitionError is statusForDBError plus the two task-transition
// conflicts introduced with the RunId fencing token
// (turtlemonvh/blanket#23 phase 1). Both map to 409, which the worker's
// retry classifier treats as "do not retry": either another run owns this
// task, or the task has moved somewhere this transition can never apply
// from. Retrying either would just burn the worker's deadline.
func statusForTransitionError(err error, fallback int) int {
	if errors.Is(err, database.ErrRunIdMismatch) || errors.Is(err, database.ErrTaskStateConflict) {
		return http.StatusConflict
	}
	return statusForDBError(err, fallback)
}

// Error types
