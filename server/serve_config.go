package server

import (
	log "github.com/Sirupsen/logrus"
	"github.com/gin-gonic/gin"
	"github.com/kardianos/osext"
	"github.com/spf13/viper"
	"net/http"
	"path"
)

// Get all configuration variables
// We use this instead of viper.AllSettings so that we have just 1 value for each path
func getConfigProcessed(c *gin.Context) {
	conf := make(map[string]interface{})
	keys := viper.AllKeys()

	for _, key := range keys {
		conf[key] = viper.GetString(key)
	}

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
