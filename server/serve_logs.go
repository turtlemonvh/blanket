package server

import (
	"github.com/gin-gonic/gin"
	"github.com/manucorporat/sse"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/tailed_file"
	"io"
	"strconv"
	"time"
)

const (
	// Max time to wait for a new value
	LOGLINE_WAIT_DURATION = 5
)

// Function to server logfile lines from a subscription.
// isComplete should return true if we know that the subscription is finished.
// - task stopped when in terminal state
// - worker stopped when no longer heartbeating
func streamLog(c *gin.Context, sub *tailed_file.TailedFileSubscriber, isComplete func() bool) {
	loglineChannelIsEmpty := false
	lineno := 1
	c.Stream(func(w io.Writer) bool {
		// This function returns a boolean indicating whether the stream should stay open
		// Every time this is called, also checks if client has left
		timer := time.NewTimer(time.Second * time.Duration(viper.GetFloat64("timeMultiplier")*LOGLINE_WAIT_DURATION))

		select {
		case logline := <-sub.NewLines:
			// Send event with message content
			timer.Stop()
			c.Render(-1, sse.Event{
				Id:    strconv.Itoa(lineno),
				Event: "message",
				Data:  logline,
			})
			lineno++
			loglineChannelIsEmpty = false
		case <-timer.C:
			loglineChannelIsEmpty = true
		}

		// If we have emptied the channel, decide whether to stop sending data
		if loglineChannelIsEmpty {
			// Check whether the process is complete
			// If so, return false so we quit streaming
			if isComplete() {
				return false
			}
		}

		return true
	})

}
