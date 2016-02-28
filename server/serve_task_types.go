package server

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/tasks"
	"net/http"
	"path"
)

func getTaskTypes(c *gin.Context) {
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

	isFirst := true
	for _, tt := range tts {
		js, err := tt.ToJSON()
		if err != nil {
			log.WithFields(log.Fields{
				"error": err.Error(),
			}).Warn("Error marshalling task type to json")
			c.String(http.StatusInternalServerError, "[]")
			return
		}
		if !isFirst {
			result += ","
		}
		result += js
		isFirst = false
	}

	result += "]"
	c.String(http.StatusOK, result)
}

func getTaskType(c *gin.Context) {
	name := c.Param("name")
	c.Header("Content-Type", "application/json")

	filepath := path.Join(viper.GetString("tasks.typesPath"), fmt.Sprintf("%s.toml", name))
	tt, err := tasks.ReadTaskTypeFromFilepath(filepath)
	if err != nil {
		// FIXME: Check for different types of errors
		log.WithFields(log.Fields{
			"error": err.Error(),
		}).Warn("Error reading task type")
		c.String(http.StatusInternalServerError, "{}")
		return
	}
	js, err := tt.ToJSON()
	if err != nil {
		log.WithFields(log.Fields{
			"error": err.Error(),
		}).Warn("Error marshalling task type to json")
		c.String(http.StatusInternalServerError, "{}")
		return
	}
	c.String(http.StatusOK, js)
	return
}
