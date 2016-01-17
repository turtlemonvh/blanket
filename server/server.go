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
	"strconv"
	"time"
)

func openDatabase() *bolt.DB {
	db, err := bolt.Open(viper.GetString("database"), 0666, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	return db
}

func setUpDatabase() error {
	db := openDatabase()
	defer db.Close()

	// Set up base task types
	err := db.Update(func(tx *bolt.Tx) error {
		var err error
		b := tx.Bucket([]byte("tasktypes"))
		if b == nil {
			b, err = tx.CreateBucket([]byte("tasktypes"))
			if err != nil {
				log.Fatal(err)
			}
		}

		// Scan through all entries looking for one with the name
		c := b.Cursor()
		echoExists := false
		for k, v := c.First(); k != nil; k, v = c.Next() {
			fmt.Printf("'%s' :: %s\n", k, v)
			if string(k) == "echotask" {
				echoExists = true
				break
			}
		}

		if !echoExists {
			// Create tasktype
			echoTaskType, _ := tasks.NewTaskType("echotask", map[string]string{})
			outStr, err := echoTaskType.ToJSON()
			if err != nil {
				log.Fatal(err)
			}
			err = b.Put([]byte(echoTaskType.Id), []byte(outStr))
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

		bashTask, _ := tasks.NewTaskType("Frogs", map[string]string{
			"NFISH": "5000",
		})

		var result []tasks.Task
		for i := 0; i < 10; i++ {
			newTask, _ := bashTask.NewTask(map[string]string{
				"NTURTLES": "100",
				"COUNT":    strconv.Itoa(i),
			})
			result = append(result, newTask)
		}
		c.JSON(200, result)
	})

	r.GET("/task_type/", func(c *gin.Context) {
		result := "["
		if err := db.View(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte("tasktypes"))
			if b == nil {
				return fmt.Errorf("Database not formatted correctly; bucket 'tasktypes' does not exist")
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

	// Start server
	log.WithFields(log.Fields{
		"port": viper.GetInt("port"),
	}).Warn("Main server started")

	r.Run(fmt.Sprintf(":%d", viper.GetInt("port")))
}
