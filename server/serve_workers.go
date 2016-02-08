package server

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

// Search in the database for all items
// For each item in the db, check that a process exists that has the right name
func getWorkers(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.String(http.StatusOK, `{"status": "ok"}`)
}

// Get just the configuration for this worker as json
func getWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.String(http.StatusOK, `{"status": "ok"}`)
}

// Register with a sequential number, filling in gaps (lowest # available)
func registerWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.String(http.StatusOK, `{"status": "ok"}`)
}

// Send SigTerm to the worker's pid
// Allow the user to pass an option to not signal; this would be used if the process is already exiting
// Currently used to show that the worker is shutting down
func shutDownWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.String(http.StatusOK, `{"status": "ok"}`)
}

// Remove the worker's record from the db if it exists
func deregisterWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")
	c.String(http.StatusOK, `{"status": "ok"}`)
}
