package server

import (
	"encoding/json"
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cast"
	"github.com/turtlemonvh/blanket/worker"
	"net/http"
)

// Search in the database for all items
// For each item in the db, check that a process exists that has the right name
func getWorkers(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	result := "["
	isFirst := true
	err := DB.View(func(tx *bolt.Tx) error {
		var err error

		b := tx.Bucket([]byte("workers"))
		if b == nil {
			errorString := "Database format error: Bucket 'workers' does not exist."
			return fmt.Errorf(errorString)
		}

		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			// Create a worker object from bytes
			// We do this instead of just appending bytes as a form of validation, and to allow filtering later
			w := worker.WorkerConf{}
			err = json.Unmarshal(v, &w)
			if err != nil {
				return err
			}

			if !isFirst {
				result += ","
			}
			isFirst = false
			result += string(v)
		}
		return nil
	})
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}
	result += "]"

	c.String(http.StatusOK, result)
}

// Get just the configuration for this worker as json
func getWorker(c *gin.Context) {
	workerId := c.Param("id")
	c.Header("Content-Type", "application/json")

	result := ""
	err := DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("workers"))
		if b == nil {
			errorString := "Database format error: Bucket 'workers' does not exist."
			return fmt.Errorf(errorString)
		}
		result += string(b.Get([]byte(workerId)))
		return nil
	})
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, result)
}

// Register with pid
// Continue to write to old log via append
func updateWorker(c *gin.Context) {
	var err error
	var workerPid int

	c.Header("Content-Type", "application/json")

	workerPid, err = cast.ToIntE(c.Param("id"))
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "The 'id' parameter must be an integer, not %s."}`, workerPid)
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// Marshall into worker conf object and validate
	w := worker.WorkerConf{}
	d := json.NewDecoder(c.Request.Body)
	err = d.Decode(&w)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// Validate worker conf before saving
	if workerPid != w.Pid {
		errMsg := fmt.Sprintf(`{"error": "Problem parsing conf. Pid does not equal the expected value ('%d' != '%d')"}`, w.Pid, workerPid)
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	err = DB.Update(func(tx *bolt.Tx) error {
		var err error
		bucket := tx.Bucket([]byte("workers"))
		if bucket == nil {
			return fmt.Errorf("Database format error: Bucket 'workers' does not exist.")
		}

		sbts, err := json.Marshal(w)
		if err != nil {
			return err
		}
		sid := fmt.Sprintf("%d", w.Pid)
		err = bucket.Put([]byte(sid), []byte(sbts))
		if err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}
	c.String(http.StatusOK, "{}")
}

// Send SigTerm to the worker's pid
// Allow the user to pass an option to not signal; this would be used if the process is already exiting
// Currently used to show that the worker is shutting down
func shutDownWorker(c *gin.Context) {
	workerId := c.Param("id")
	c.Header("Content-Type", "application/json")

	var result []byte
	err := DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("workers"))
		if b == nil {
			errorString := "Database format error: Bucket 'workers' does not exist."
			return fmt.Errorf(errorString)
		}
		result = append(result, b.Get([]byte(workerId))...)
		return nil
	})
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// Turn into a worker conf object
	w := worker.WorkerConf{}
	err = json.Unmarshal(result, &w)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	// Send SIGTERM
	err = w.Stop()
	if err != nil && err.Error() == "os: process already finished" {
		err = deleteWorkerEntry(workerId)
	}
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, `{"status": "ok"}`)
}

func deleteWorkerEntry(workerId string) error {
	return DB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("workers"))
		if b == nil {
			errorString := "Database format error: Bucket 'workers' does not exist."
			return fmt.Errorf(errorString)
		}
		err := b.Delete([]byte(workerId))
		return err
	})
}

// Remove the worker's record from the db if it exists
func deleteWorker(c *gin.Context) {
	workerId := c.Param("id")
	c.Header("Content-Type", "application/json")

	err := deleteWorkerEntry(workerId)
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, fmt.Sprintf(`{"id": "%s"}`, workerId))
}

// FIXME: Wait until worker registers itself in the database to return, up to X seconds
func launchWorker(c *gin.Context) {
	c.Header("Content-Type", "application/json")

	w := worker.WorkerConf{}

	var err error
	err = c.BindJSON(&w)
	if err != nil {
		return
	}

	// Always a daemon; default check interval is 2 seconds
	w.Daemon = true
	if w.CheckInterval == 0 {
		w.CheckInterval = 2
	}

	err = w.Run()
	if err != nil {
		errMsg := fmt.Sprintf(`{"error": "%s"}`, err.Error())
		c.String(http.StatusInternalServerError, errMsg)
		return
	}

	c.String(http.StatusOK, "{}")
}

// FIXME: Use tail to grab last bit of file: https://godoc.org/github.com/hpcloud/tail
// FIXME: Allow sse: https://godoc.org/github.com/julienschmidt/sse#Streamer.SendString
// ALT: https://github.com/mozilla-services/heka/blob/dev/logstreamer/filehandling.go#L294
// https://github.com/brho/plan9/blob/master/sys/src/cmd/tail.c
// - reads backwards until it finds a given line
func getWorkerLogfile(c *gin.Context) {
	c.Header("Content-Type", "text/plain")

	workerId := c.Param("id")

	w := worker.WorkerConf{}
	err := DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("workers"))
		if b == nil {
			errorString := "Database format error: Bucket 'workers' does not exist."
			return fmt.Errorf(errorString)
		}

		err := json.Unmarshal(b.Get([]byte(workerId)), &w)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		c.String(http.StatusInternalServerError, fmt.Sprintf(`Error: "%s"`, err.Error()))
		return
	}
	if w.Pid == 0 {
		c.String(http.StatusNotFound, fmt.Sprintf(`Error: Worker with id %s not found`, workerId))
		return
	}

	// Open file and stream out contents
	/*
		tf := *tail.Tail{}
		tf, err = tail.TailFile(c.Logfile, tail.Config{
			Follow: false,
			//Logger: tail.DiscardingLogger,
		})
	*/

	// Open file and send all contents
	// https://godoc.org/github.com/gin-gonic/gin#Context.File
	c.File(w.Logfile)
}
