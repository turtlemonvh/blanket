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
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/tasks"
)

func Serve() {
	r := gin.Default()

	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"version": "0.1",
			"name":    "blanket",
			"author":  "Timothy Van Heest <timothy.vanheest@gmail.com>",
		})
	})

	// Example
	r.GET("/task/", func(c *gin.Context) {
		c.JSON(200, tasks.Task{
			1111111111,
			1111111112,
			123,
			map[string]string{"NTURTLES": "100"},
		})
	})

	r.GET("/task_type/", func(c *gin.Context) {
		c.JSON(200, tasks.TaskType{
			1111111111,
			1111111112,
			"PondSize",
			map[string]string{"NFISH": "5000"},
			".",
		})
	})

	log.WithFields(log.Fields{
		"port": viper.GetInt("port"),
	}).Warn("Main server started")

	r.Run(fmt.Sprintf(":%d", viper.GetInt("port")))
}
