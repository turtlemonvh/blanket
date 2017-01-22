/*

Launch blanket server

- Serves on a local port
- May change over to use unix sockets later


- some things may want access to task structs but are not going to be able to query the database directly
- define routes here, but write actual functions in other sub folders

- expvar usage in docker
    - https://github.com/docker/docker/blob/master/api/server/profiler.go
*/

package server

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/gin-gonic/contrib/ginrus"
	"github.com/gin-gonic/gin"
	"github.com/rs/cors"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/queue"
	"github.com/turtlemonvh/blanket/lib/tailed_file"
	"gopkg.in/tylerb/graceful.v1"
	"net/http"
	"time"
)

var DB database.BlanketDB
var Q queue.BlanketQueue

// FIXME: Pass in all configuration so decoupled from viper
func Serve(pDB database.BlanketDB, pQ queue.BlanketQueue) *graceful.Server {
	// FIXME: Better variable names
	DB = pDB
	Q = pQ

	// https://godoc.org/github.com/rs/cors
	c := cors.New(cors.Options{
		AllowedOrigins:     []string{"*"},
		AllowedMethods:     []string{"GET", "POST", "PUT", "DELETE"},
		OptionsPassthrough: false,
	})

	// If we don't return early from handler function we get a 404 for the options request
	makeCorsHandler := func(c *cors.Cors) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			c.HandlerFunc(w, r)
			// Allow it to return to avoid a 404
			if r.Method == "OPTIONS" && w.Header().Get("Access-Control-Allow-Origin") == r.Header.Get("Origin") {
				w.WriteHeader(http.StatusOK)
			}
		}
	}

	if log.GetLevel() != log.DebugLevel {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(ginrus.Ginrus(log.StandardLogger(), time.RFC3339, true))
	r.Use(gin.Recovery())
	r.Use(gin.WrapF(makeCorsHandler(c)))

	// Make the result dir browseable
	r.StaticFS("/results", gin.Dir(viper.GetString("tasks.resultsPath"), true))

	// Serve from bindata
	r.StaticFS("/ui", assetFS())

	r.GET("/version", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"version": "0.1",
			"name":    "blanket",
			"author":  "Timothy Van Heest <timothy.vanheest@gmail.com>",
		})
	})

	r.GET("/ops/status/", MetricsHandler)
	r.GET("/config/", getConfigProcessed)

	r.GET("/task_type/", getTaskTypes)
	r.GET("/task_type/:name", getTaskType)

	// Called by user
	r.POST("/task/", postTask)            // add a new task to the queue
	r.DELETE("/task/:id", removeTask)     // delete all information, including killing if running
	r.GET("/task/:id/log", streamTaskLog) // stdout log
	r.PUT("/task/:id/cancel", cancelTask) // stop execution of a task; will be moved to state STOPPED

	r.GET("/task/", getTasks)   // fixme; pull from queue or database or both
	r.GET("/task/:id", getTask) // fetch just 1 by id

	// Called by worker
	r.POST("/task/claim/:workerid", claimTask)      // claim a task; called by a worker
	r.PUT("/task/:id/run", runTask)                 // start running a task
	r.PUT("/task/:id/finish", finishTask)           // update state
	r.PUT("/task/:id/progress", updateTaskProgress) // update progress

	// FIXME: Pause worker
	r.GET("/worker/:id", getWorker)
	r.GET("/worker/", getWorkers)
	r.POST("/worker/", launchNewWorker)         // called from front end, doesn't actually hit database
	r.PUT("/worker/:id/stop", stopWorker)       // stop/pause worker; will stop after current task stops
	r.PUT("/worker/:id/restart", restartWorker) // re-start an existing worker
	r.PUT("/worker/:id", updateWorker)          // used for initial creation + status updates
	r.DELETE("/worker/:id", deleteWorker)       // remove from database; can only be called on a stopped worker
	r.GET("/worker/:id/logs", getWorkerLogfile) // server sent events

	// Start server
	log.WithFields(log.Fields{
		"port": viper.GetInt("port"),
	}).Warn("Main server started")

	// FIXME: Launch background process for automatically
	// - cleaning queue
	// - cleaning db
	// - cleaning workers

	// Graceful shutdown, leaving up to 2 seconds for requests to complete
	return &graceful.Server{
		Timeout: 2 * time.Second,
		Server: &http.Server{
			Addr:    fmt.Sprintf(":%d", viper.GetInt("port")),
			Handler: r,
		},
		BeforeShutdown: func() bool {
			// Called first
			log.Warn("Called BeforeShutdown")
			tailed_file.StopAll()
			return true
		},
		ShutdownInitiated: func() {
			// Called second
			log.Warn("Called ShutdownInitiated")
		},
	}
}
