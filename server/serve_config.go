package server

import (
	log "github.com/Sirupsen/logrus"
	"github.com/gin-gonic/gin"
	"github.com/kardianos/osext"
	"github.com/spf13/viper"
	"net/http"
	"path"
)

// FIXME: Includes both nested and flat variables
// May want to reduce to a single representation
// Flattening everything would probably be easiest
func getConfig(c *gin.Context) {
	conf := viper.AllSettings()

	execPath, err := osext.Executable()
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("Problem getting executable path")
	} else {
		conf["basePath"] = path.Dir(execPath)
	}

	c.JSON(http.StatusOK, conf)
}
