package server

import (
	log "github.com/Sirupsen/logrus"
	"github.com/gin-gonic/gin"
	"github.com/turtlemonvh/blanket/tasks"
	"net/http"
)

func getTaskTypes(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	// Read from disk
	tts, err := tasks.ReadTypes()
	if err != nil {
		log.WithFields(log.Fields{
			"error": err.Error(),
		}).Warn("Error reading task types")
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.JSON(http.StatusOK, tts)
}

func getTaskType(c *gin.Context) {
	name := c.Param("name")
	c.Header("Content-Type", "application/json")

	tt, err := tasks.FetchTaskType(name)
	if err != nil {
		// FIXME: Handle not found errors differently
		c.String(http.StatusInternalServerError, MakeErrorString(err.Error()))
		return
	}
	c.JSON(http.StatusOK, tt)
	return
}
