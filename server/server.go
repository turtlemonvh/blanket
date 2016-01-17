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
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/tasks"
	"net/http"
	"time"
)

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
	db := openDatabase()
	defer db.Close()

	// Set up base task types
	err := db.Update(func(tx *bolt.Tx) error {
		var err error

		// Create tasks bucket
		b := tx.Bucket([]byte("tasks"))
		if b == nil {
			b, err = tx.CreateBucket([]byte("tasks"))
			if err != nil {
				log.Fatal(err)
			}
		}

		return nil
	})

	return err

}

func Serve() {
	// Connect to database
	// FIXME: May want to make the database a module level constant to make it more accessible
	if err := setUpDatabase(); err != nil {
		log.Fatal(err)
	}
	db := openDatabase()
	defer db.Close()

	// Basic info routes
	r := gin.Default()

	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"version": "0.1",
			"name":    "blanket",
			"author":  "Timothy Van Heest <timothy.vanheest@gmail.com>",
		})
	})

	r.GET("/task/", func(c *gin.Context) {
		result := "["
		if err := db.View(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte("tasks"))
			if b == nil {
				return fmt.Errorf("Database not formatted correctly; bucket 'tasks' does not exist")
			}

			c := b.Cursor()
			isFirst := true
			for k, v := c.First(); k != nil; k, v = c.Next() {
				if !isFirst {
					result += ","
				}
				result += string(v)
				isFirst = false
			}

			return nil
		}); err != nil {
			c.Header("Content-Type", "application/json")
			c.String(http.StatusInternalServerError, "[]")
			return
		}
		result += "]"

		c.Header("Content-Type", "application/json")
		c.String(http.StatusOK, result)
	})

	r.GET("/task_type/", func(c *gin.Context) {
		c.Header("Content-Type", "application/json")

		// Read from disk
		result := "["

		tts, err := tasks.ReadTypes()
		if err != nil {
			log.WithFields(log.Fields{
				"error": err.Error(),
			}).Warn("Error reading task types")
			c.String(http.StatusInternalServerError, "[]")
			return
		}
		for _, tt := range tts {
			js, err := tt.ToJSON()
			if err != nil {
				log.WithFields(log.Fields{
					"error": err.Error(),
				}).Warn("Error marshalling task type to json")
				c.String(http.StatusInternalServerError, "[]")
				return
			}
			result += js
		}

		result += "]"
		c.String(http.StatusOK, result)
	})

	// Start server
	log.WithFields(log.Fields{
		"port": viper.GetInt("port"),
	}).Warn("Main server started")

	r.Run(fmt.Sprintf(":%d", viper.GetInt("port")))
}
