package tailed_file

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"io/ioutil"
	"log"
	"os"
	"testing"
	"time"
)

func TestStreamLogSingleSub(t *testing.T) {
	// Close this channel when writing to the file is finished
	c := make(chan struct{})

	// Start a process writing to a file
	tmpfile, err := ioutil.TempFile("", "logstream")
	if err != nil {
		log.Fatal(err)
	}
	defer tmpfile.Close()
	defer os.Remove(tmpfile.Name())
	go func() {
		for i := 0; i < 10; i++ {
			line := []byte(fmt.Sprintf("%d: %s\n", i, time.Now().String()))
			log.Printf("Writing line %d", i)
			if _, err := tmpfile.Write(line); err != nil {
				log.Fatal(err)
			}
		}
		tmpfile.Sync()
		close(c)
	}()

	// Start log tailer collection
	testTfc := NewTailedFileCollection()

	// Follow file
	sub, err := testTfc.Follow(tmpfile.Name())
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		<-c
		// FIXME: Kind of brittle, but gives file tailer time to flush
		log.Printf("Closing tailed file after 1 second")
		time.Sleep(time.Second)
		// Must be closed to shop reads on sub.NewLines channel
		log.Printf("Closing tailed file")
		assert.Equal(t, testTfc.GetSubscriberCount(), int64(1))
		testTfc.StopTailedFile(tmpfile.Name())
		assert.Equal(t, testTfc.GetSubscriberCount(), int64(0))
	}()

	// Read log lines
	nlines := 0
	for line := range sub.NewLines {
		// check that only 1 subscriber
		// check must be in here since the registration of the subscriber is async
		assert.Equal(t, testTfc.GetSubscriberCount(), int64(1))

		// increment counter
		nlines += 1
		log.Println("Read line from tailed file: ", line)
	}

	// Check that count is the expected value
	assert.Equal(t, nlines, 10)
	assert.Equal(t, testTfc.GetSubscriberCount(), int64(0))
}
