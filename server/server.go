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
	"strconv"
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
		var result []tasks.TaskType

		for i := 0; i < 10; i++ {
			newName := fmt.Sprintf("PondSize_%d", i)
			newTask, _ := tasks.NewTaskType(newName, map[string]string{
				"NFISH": strconv.Itoa(100 * i),
			})
			result = append(result, newTask)
		}

		c.JSON(200, result)
	})

	log.WithFields(log.Fields{
		"port": viper.GetInt("port"),
	}).Warn("Main server started")

	r.Run(fmt.Sprintf(":%d", viper.GetInt("port")))
}
