package server

import (
	"github.com/gin-gonic/gin"
	"github.com/kardianos/osext"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"net/http"
	"path"
)

// Get all configuration variables
// We use this instead of viper.AllSettings so that we have just 1 value for each path
func (s *ServerConfig) getConfigProcessed(c *gin.Context) {
	conf := make(map[string]interface{})
	keys := viper.AllKeys()

	for _, key := range keys {
		conf[key] = viper.GetString(key)
	}

	// Identity of this server *process*, which no config key can supply:
	// the instance id a worker's heartbeat response carries, and when the
	// process started. Exposed here so an operator (or a script) can see
	// what the workers are seeing without sending a heartbeat, and so
	// "did the server restart?" is answerable from one GET.
	// turtlemonvh/blanket#23 phase 3.
	conf["instanceId"] = s.InstanceId()
	conf["serverStartedTs"] = s.StartedTs()

	execPath, err := osext.Executable()
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("Problem getting executable path")
	} else {
		conf["basepath"] = path.Dir(execPath)
	}

	c.JSON(http.StatusOK, conf)
}
