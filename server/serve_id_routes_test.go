// serve_id_routes_test.go is the cross-resource regression test for #115:
// a malformed :id path parameter on any task or worker route must answer
// 400, never the 500 a handful of handlers used to write (getTaskId,
// SafeObjectId's various inline callers). A well-formed id naming nothing
// in the database is a separate case, handled per-route below since the
// right answer isn't always 404 -- DELETE /task/:id and DELETE
// /worker/:id are documented, deliberate idempotent-delete endpoints (see
// removeTask's "Always returns 200, even if item doesn't exist" comment)
// and are left alone here; every other route in this table looks its
// record up before doing anything, so a missing one is 404.
//
// Routes needing a request body or query parameter beyond the id itself
// (PUT /task/:id/schedule, /run, /progress, /finish; PUT /worker/:id) are
// exercised in their own resource test files instead -- getTaskId /
// getWorkerId run before any of those are read, so the malformed-id case
// is already fully covered by the bodyless requests here, but a
// meaningful missing-id assertion for those needs the extra parameters
// that would clutter this table.
package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/objectid"
)

// idRouteCase is one :id route exercised for the malformed-vs-missing
// distinction. pathTemplate has a single "%s" for the id.
type idRouteCase struct {
	name              string
	method            string
	pathTemplate      string
	wantMissingStatus int // status for a well-formed id naming nothing
}

var idRouteCases = []idRouteCase{
	// Tasks
	{"GET /task/:id", "GET", "/task/%s", http.StatusNotFound},
	{"DELETE /task/:id", "DELETE", "/task/%s", http.StatusOK}, // idempotent delete
	{"PUT /task/:id/cancel", "PUT", "/task/%s/cancel", http.StatusNotFound},
	{"PUT /task/:id/pause", "PUT", "/task/%s/pause", http.StatusNotFound},
	{"PUT /task/:id/resume", "PUT", "/task/%s/resume", http.StatusNotFound},
	{"GET /task/:id/log/tail", "GET", "/task/%s/log/tail", http.StatusNotFound},

	// Workers
	{"GET /worker/:id", "GET", "/worker/%s", http.StatusNotFound},
	{"PUT /worker/:id/stop", "PUT", "/worker/%s/stop", http.StatusNotFound},
	{"PUT /worker/:id/restart", "PUT", "/worker/%s/restart", http.StatusNotFound},
	{"GET /worker/:id/log/tail", "GET", "/worker/%s/log/tail", http.StatusNotFound},
	// DELETE /worker/:id is excluded here: unlike DELETE /task/:id it
	// requires a JSON body (deleteWorker's "is this worker stopped" check),
	// so a bodyless request 400s on that bind rather than on id
	// resolution -- see TestDeleteWorker_InvalidId / _MissingIdIsNoop in
	// serve_workers_test.go for its malformed/missing coverage instead.

	// UI (rendered error/flash pattern rather than JSON, but same status codes)
	{"GET /ui/tasks/:id", "GET", "/ui/tasks/%s", http.StatusNotFound},
	{"GET /ui/workers/:id", "GET", "/ui/workers/%s", http.StatusNotFound},
	{"PUT /ui/series/:id/pause", "PUT", "/ui/series/%s/pause", http.StatusNotFound},
}

func TestIdRoutes_MalformedIdIs400(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	for _, tc := range idRouteCases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, fmt.Sprintf(tc.pathTemplate, "not-a-valid-id"), nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
		})
	}
}

func TestIdRoutes_WellFormedMissingId(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	r := s.GetRouter()

	for _, tc := range idRouteCases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, fmt.Sprintf(tc.pathTemplate, objectid.NewObjectId().Hex()), nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.wantMissingStatus, w.Code, "body: %s", w.Body.String())
		})
	}
}
