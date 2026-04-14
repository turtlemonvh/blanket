package tailed_file

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestStreamLogSingleSub exercises the happy path: one subscriber reads every
// line that a producer writes to a file being tailed, then the tail is
// stopped and the subscriber goes away cleanly.
//
// Previously this test used a fixed 1s sleep before closing the tail and
// relied on the tailer having polled the file by then — roughly 1-in-5 flaky.
// Now it reads in a bounded loop until either all N lines have arrived or a
// generous timeout fires.
func TestStreamLogSingleSub(t *testing.T) {
	const numLines = 10
	const readTimeout = 10 * time.Second

	tmpfile, err := ioutil.TempFile("", "logstream")
	if err != nil {
		log.Fatal(err)
	}
	defer tmpfile.Close()
	defer os.Remove(tmpfile.Name())

	testTfc := NewTailedFileCollection()
	sub, err := testTfc.Follow(tmpfile.Name())
	if err != nil {
		log.Fatal(err)
	}

	// Write all lines before reading. The tailer polls, so writing up front
	// is fine — we just need the lines on disk before the deadline.
	for i := 0; i < numLines; i++ {
		line := []byte(fmt.Sprintf("%d: %s\n", i, time.Now().String()))
		if _, err := tmpfile.Write(line); err != nil {
			log.Fatal(err)
		}
	}
	tmpfile.Sync()

	// Read until we have all N lines, or fail on timeout.
	nlines := 0
	deadline := time.After(readTimeout)
readLoop:
	for {
		select {
		case line, ok := <-sub.NewLines:
			if !ok {
				break readLoop
			}
			assert.Equal(t, int64(1), testTfc.GetSubscriberCount())
			nlines++
			log.Println("Read line from tailed file: ", line)
			if nlines == numLines {
				break readLoop
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %d lines; got %d", numLines, nlines)
		}
	}

	assert.Equal(t, numLines, nlines)
	assert.Equal(t, int64(1), testTfc.GetSubscriberCount())
	testTfc.StopTailedFile(tmpfile.Name())
	assert.Equal(t, int64(0), testTfc.GetSubscriberCount())
}
