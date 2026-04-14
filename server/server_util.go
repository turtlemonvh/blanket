package server

import (
	"fmt"
	"github.com/turtlemonvh/blanket/lib/objectid"
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

// Error types
