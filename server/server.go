/*

Launch blanket server

- Serves on a local port
- May change over to use unix sockets later

- some things may want access to task structs but are not going to be able to query the database directly
- define routes here, but write actual functions in other sub folders
*/

package server

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/gin-gonic/contrib/ginrus"
	"github.com/gin-gonic/gin"
	"github.com/rs/cors"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/queue"
	"github.com/turtlemonvh/blanket/lib/tailed_file"
	"gopkg.in/tylerb/graceful.v1"
	"net/http"
	"time"
)

type ServerConfig struct {
	DB             database.BlanketDB
	Q              queue.BlanketQueue
	ResultsPath    string
	Port           int
	TimeMultiplier float64
}

func (s *ServerConfig) GetRouter() *gin.Engine {
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
	r.StaticFS("/results", gin.Dir(s.ResultsPath, true))

	// Serve ui from bindata
	r.StaticFS("/ui", assetFS())

	// Redirect to ui
	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/ui")
	})
	r.GET("/version", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"version": "0.1",
			"name":    "blanket",
			"author":  "Timothy Van Heest <timothy.vanheest@gmail.com>",
		})
	})

	r.GET("/ops/status/", MetricsHandler)
	r.GET("/config/", s.getConfigProcessed)

	r.GET("/task_type/", s.getTaskTypes)
	r.GET("/task_type/:name", s.getTaskType)

	// Called by user
	r.GET("/task/", s.getTasks)             // list tasks in db
	r.GET("/task/:id", s.getTask)           // fetch just 1 by id
	r.POST("/task/", s.postTask)            // add a new task to the queue
	r.DELETE("/task/:id", s.removeTask)     // delete all information from db, including killing if running
	r.GET("/task/:id/log", s.streamTaskLog) // stream stdout log
	r.PUT("/task/:id/cancel", s.cancelTask) // stop execution of a task; will be moved to state STOPPED

	// Called by worker
	r.POST("/task/claim/:workerid", s.claimTask)      // claim a task
	r.PUT("/task/:id/run", s.markTaskAsRunning)       // mark a task as running
	r.PUT("/task/:id/progress", s.updateTaskProgress) // update progress
	r.PUT("/task/:id/finish", s.markTaskAsFinished)   // update state

	r.GET("/worker/:id", s.getWorker)
	r.GET("/worker/", s.getWorkers)
	r.POST("/worker/", s.launchNewWorker)         // called from front end, doesn't actually hit database
	r.PUT("/worker/:id/stop", s.stopWorker)       // stop/pause worker; will stop after current task stops
	r.PUT("/worker/:id/restart", s.restartWorker) // re-start an existing worker
	r.PUT("/worker/:id", s.updateWorker)          // used for initial creation + status updates
	r.DELETE("/worker/:id", s.deleteWorker)       // remove from database; can only be called on a stopped worker
	r.GET("/worker/:id/logs", s.getWorkerLogfile) // server sent events

	return r
}

func (s *ServerConfig) Serve() *graceful.Server {
	// Start server
	log.WithFields(log.Fields{
		"port": s.Port,
	}).Info("Starting main server")

	// FIXME: Launch background process for automatically
	// - cleaning queue
	// - cleaning db
	// - cleaning workers

	// Graceful shutdown, leaving up to 2 seconds for requests to complete
	return &graceful.Server{
		Timeout: 2 * time.Second,
		Server: &http.Server{
			Addr:    fmt.Sprintf(":%d", s.Port),
			Handler: s.GetRouter(),
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
