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
	DB = openDatabase()
	defer DB.Close()

	// Basic info routes
	r := gin.Default()

	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"version": "0.1",
			"name":    "blanket",
			"author":  "Timothy Van Heest <timothy.vanheest@gmail.com>",
		})
	})

	r.GET("/task/", getTasks)
	r.GET("/task/:id", getTask) // fetch just 1 by id
	r.POST("/task/", postTask)  // create a new one
	//r.PUT("/task/", updateTask)    // update progress
	r.DELETE("/task/:id", removeTask) // delete all information, including killing if running

	r.GET("/task_type/", getTaskTypes)
	r.GET("/task_type/:name", getTaskType)

	// Start server
	log.WithFields(log.Fields{
		"port": viper.GetInt("port"),
	}).Warn("Main server started")

	r.Run(fmt.Sprintf(":%d", viper.GetInt("port")))
}
