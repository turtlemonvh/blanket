package server

import (
	"github.com/codahale/metrics"
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
