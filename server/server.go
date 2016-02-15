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
	"github.com/boltdb/bolt"
	"github.com/gin-gonic/gin"
	"github.com/rs/cors"
	"github.com/spf13/viper"
	"gopkg.in/tylerb/graceful.v1"
	"net/http"
	"time"
)

var DB *bolt.DB

func openDatabase() *bolt.DB {
	db, err := bolt.Open(viper.GetString("database"), 0666, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	return db
}

/*
	FIXME:
	- we probably want to allow the user to import a set of tasks instead of having defaults
	- actually, a default task that runs an arbitrary command on the command line would be useful
	- we'll need to create the directory for it

	- may want to move initialization into a separate init command; this is what django does
	- then just check for valid initialization in startup

	- include a timeout for all tasks
	- include task state
	- include progress %
	- allow tasks to lauch sub tasks

	- make scripts editable in the interface (like bamboo)

	- task types should always be read from disk
	- task should include a hash of the config file of the task type
*/

func setUpDatabase() error {
	DB = openDatabase()
	defer DB.Close()

	// Set up base task types
	err := DB.Update(func(tx *bolt.Tx) error {
		var err error

		// Create tasks bucket
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			b, err = tx.CreateBucket([]byte("tasks"))
			if err != nil {
				log.Fatal(err)
			}
		}

		// Create workers bucket
		b = tx.Bucket([]byte("workers"))
		if b == nil {
			b, err = tx.CreateBucket([]byte("workers"))
			if err != nil {
				log.Fatal(err)
			}
		}

		return nil
	})

	return err
}

func Serve() {
	// FIXME: Handle Ctrl-C

	// Connect to database
	// FIXME: May want to make the database a module level constant to make it more accessible
	if err := setUpDatabase(); err != nil {
		log.Fatal(err)
	}
	DB = openDatabase()
	defer DB.Close()

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

	// Basic info routes
	r := gin.Default()

	//r.Use(gin.WrapF(c.HandlerFunc))
	r.Use(gin.WrapF(makeCorsHandler(c)))

	// Make the result dir browseable
	r.StaticFS("/results", gin.Dir(viper.GetString("tasks.results_path"), true))

	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"version": "0.1",
			"name":    "blanket",
			"author":  "Timothy Van Heest <timothy.vanheest@gmail.com>",
		})
	})

	r.GET("/task/", getTasks)
	r.GET("/task/:id", getTask)                     // fetch just 1 by id
	r.POST("/task/", postTask)                      // create a new one
	r.PUT("/task/:id/state", updateTaskState)       // update state
	r.PUT("/task/:id/progress", updateTaskProgress) // update progress
	r.DELETE("/task/:id", removeTask)               // delete all information, including killing if running

	r.GET("/task_type/", getTaskTypes)
	r.GET("/task_type/:name", getTaskType)

	r.GET("/worker/", getWorkers)
	r.GET("/worker/:id", getWorker)
	r.PUT("/worker/:id", updateWorker)            // initial post and status update
	r.PUT("/worker/:id/shutdown", shutDownWorker) // not called by worker itself
	r.DELETE("/worker/:id", deleteWorker)         // remove from database

	// Start server
	log.WithFields(log.Fields{
		"port": viper.GetInt("port"),
	}).Warn("Main server started")

	// Graceful shutdown, leaving up to 2 seconds for requests to complete
	srv := &graceful.Server{
		Timeout: 2 * time.Second,
		Server: &http.Server{
			Addr:    fmt.Sprintf(":%d", viper.GetInt("port")),
			Handler: r,
		},
		BeforeShutdown: func() {
			// Called first
			log.Warn("Called BeforeShutdown")
		},
		ShutdownInitiated: func() {
			// Called second
			log.Warn("Called ShutdownInitiated")
		},
	}
	srv.ListenAndServe()
}
