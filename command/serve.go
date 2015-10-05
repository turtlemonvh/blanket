/*

Launch blanket server

*/

package command

import (
	log "github.com/Sirupsen/logrus"
	"github.com/gin-gonic/gin"
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
		c.JSON(200, tasks.Task{1111111111, 1111111112, "Animal"})
	})

	log.Warn("Main server on port 8080")
	r.Run(":8080")
}
