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

// Safely convert a string to an object id
func SafeObjectId(workerIdStr string) (objectid.ObjectId, error) {
	oid := objectid.NewObjectId()
	if !objectid.IsObjectIdHex(workerIdStr) {
		return oid, fmt.Errorf("Invalid worker id")
	}
	return objectid.ObjectIdHex(workerIdStr), nil
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
