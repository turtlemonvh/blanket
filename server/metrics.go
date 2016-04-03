package server

import (
	"expvar"
	"fmt"
	"github.com/codahale/metrics"
	"github.com/gin-gonic/gin"
	"net/http"
	"runtime"
	"time"
)

var (
	// Updated periodically
	nGoRoutines = metrics.Gauge("nGoRoutines")
	nCGoCalls   = metrics.Gauge("nCGoCalls")
)

func init() {
	ticker := time.NewTicker(2 * time.Second)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				// Update gauges
				nGoRoutines.Set(int64(runtime.NumGoroutine()))
				nCGoCalls.Set(runtime.NumCgoCall())
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()
}

// Output expvar metrics as json
// From: https://golang.org/src/expvar/expvar.go
func MetricsHandler(c *gin.Context) {
	w := c.Writer
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	fmt.Fprintf(w, "{\n")
	first := true
	expvar.Do(func(kv expvar.KeyValue) {
		if !first {
			fmt.Fprintf(w, ",\n")
		}
		first = false
		fmt.Fprintf(w, "%q: %s", kv.Key, kv.Value)
	})
	fmt.Fprintf(w, "\n}\n")
	c.Status(http.StatusOK)
}
