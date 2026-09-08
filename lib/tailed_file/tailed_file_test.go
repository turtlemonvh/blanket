package tailed_file

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"sync"
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

// TestConcurrentCollectionAccessAgainstCleanup is a regression test for
// turtlemonvh/blanket#119: TailedFileCollection.fileList (and each
// TailedFile's Subscribers map) used to be read/written from request-handler
// goroutines (GetTailedFile, Follow, GetSubscriberCount) with no
// synchronization against the package-init cleanup goroutine, which reads
// the same maps on a 2s ticker to update the nTailedFiles/
// nTailedFileSubscribers expvars.
//
// Rather than waiting on the real ticker (slow, and only fires every 2s —
// a bad way to try to provoke a race within a test timeout), this calls
// updateGauges directly: that's the method the ticker body was refactored
// into specifically so it could be driven like this. Run under
// `go test -race`, this reliably caught the collection/subscriber-map races
// before the fix in tailed_file.go (GetSubscriberCount, StopIfNoSubscribers
// and the ticker body all read the maps unlocked).
//
// Deliberately does not exercise TailedFileSubscriber.Stop() here -- races
// between Stop and the tailer goroutine are turtlemonvh/blanket#123's
// scope, not this one.
func TestConcurrentCollectionAccessAgainstCleanup(t *testing.T) {
	const numFiles = 4
	const numWorkers = 8
	const numRounds = 200

	testTfc := NewTailedFileCollection()

	paths := make([]string, numFiles)
	for i := range paths {
		f, err := ioutil.TempFile("", "collection-race")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		defer os.Remove(f.Name())
		paths[i] = f.Name()
	}

	var wg sync.WaitGroup

	// Workers hammer the read/write paths through the collection: creating
	// tailed files, subscribing to them, and reading the aggregate
	// subscriber count -- all of which touch tfc.fileList and/or a
	// TailedFile's Subscribers map.
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < numRounds; i++ {
				p := paths[(w+i)%numFiles]
				switch i % 3 {
				case 0:
					if _, err := testTfc.GetTailedFile(p); err != nil {
						t.Errorf("GetTailedFile(%s): %v", p, err)
						return
					}
				case 1:
					if _, err := testTfc.Follow(p); err != nil {
						t.Errorf("Follow(%s): %v", p, err)
						return
					}
				case 2:
					testTfc.GetSubscriberCount()
				}
			}
		}(w)
	}

	// Drive the cleanup path -- the same gauge update the init() ticker
	// calls every 2s in production -- directly and repeatedly, concurrently
	// with the workers above.
	for c := 0; c < 4; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < numRounds; i++ {
				testTfc.updateGauges()
			}
		}()
	}

	// Also race StopTailedFile against everything above: it deletes from
	// the same fileList map GetTailedFile/Follow/updateGauges read and
	// write, and calling it on a path that's mid-recreation elsewhere is
	// exactly the interleaving the cleanup goroutine could hit in
	// production (a file's last subscriber leaving while the ticker fires).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numRounds; i++ {
			testTfc.StopTailedFile(paths[i%numFiles])
		}
	}()

	wg.Wait()

	testTfc.StopAll()
}

// TestSubscriberStop_WhileNotReading is the regression test for
// turtlemonvh/blanket#123's Stop deadlock: TailedFileSubscriber.Stop used
// to hang forever when called by a handler that had already stopped
// reading NewLines. StartTailedFile's inner goroutine holds tf.Lock() for
// its entire per-line fan-out, including the blocking send itself
// (`sub.NewLines <- nline.Text`); once nobody is left to receive that
// send, the tailer is parked holding the lock forever, and the old Stop
// -- which needed that same lock to remove the subscriber -- waited on
// the very goroutine that was waiting on it.
//
// This reproduces exactly that: subscribe, never read NewLines, write a
// line so the tailer picks it up and parks on the send, then call Stop
// from a goroutine bounded by a timeout -- the ask in the issue for a
// test that would have hung before the fix. A regression here fails the
// test loudly instead of wedging the whole test binary.
func TestSubscriberStop_WhileNotReading(t *testing.T) {
	tmpfile, err := ioutil.TempFile("", "stop-deadlock")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	defer tmpfile.Close()

	testTfc := NewTailedFileCollection()
	sub, err := testTfc.Follow(tmpfile.Name())
	if err != nil {
		t.Fatal(err)
	}

	// NewLines is unbuffered here: its buffer is sized to the file's
	// offset at Subscribe time (bufSize in Subscribe), which is 0 for a
	// file nothing has tailed yet. So the tailer blocks on the very first
	// send once it polls the write below, and -- since this test never
	// reads -- stays parked there until something breaks it out.
	if _, err := tmpfile.WriteString("line one\n"); err != nil {
		t.Fatal(err)
	}
	tmpfile.Sync()

	// Generous wait for the follower's poller to notice the write and get
	// the tailer goroutine parked on the blocking send -- once parked it
	// stays parked (no timeout on that send), so this only needs to be
	// long enough to make that virtually certain, not to win a narrow race.
	time.Sleep(2 * time.Second)

	stopped := make(chan struct{})
	go func() {
		sub.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("TailedFileSubscriber.Stop deadlocked against the tailer goroutine while nobody was reading NewLines")
	}

	assert.Equal(t, int64(0), testTfc.GetSubscriberCount())

	// Idempotent: a second Stop from elsewhere (a deferred call racing a
	// caller that already stopped it explicitly) must not double-close.
	assert.NotPanics(t, sub.Stop)

	testTfc.StopTailedFile(tmpfile.Name())
}

// The backfill variant of the deadlock above: lines already in the ring
// when a subscriber arrives are replayed by Subscribe's goroutine, which
// holds tf.Lock() while it sends them. With NewLines sized from a
// FileOffset read before that goroutine ran, a line the tailer slipped in
// between leaves the buffer one short, the replay parks on the send, and
// Stop -- which needs the lock -- can never return. Writing the lines
// before Follow makes the replay path the one under test every time.
func TestSubscriberStop_WhileNotReading_DuringBackfill(t *testing.T) {
	tmpfile, err := ioutil.TempFile("", "stop-deadlock-backfill")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())
	defer tmpfile.Close()

	if _, err := tmpfile.WriteString("one\ntwo\nthree\n"); err != nil {
		t.Fatal(err)
	}
	tmpfile.Sync()

	testTfc := NewTailedFileCollection()
	defer testTfc.StopAll()
	first, err := testTfc.Follow(tmpfile.Name())
	if err != nil {
		t.Fatal(err)
	}
	// The first subscriber reads, like a real handler: the tailer holds
	// tf.Lock() while it fans out, so a subscriber that never reads would
	// park the tailer and block every later Subscribe on the lock -- a
	// different (pre-existing) hazard from the one under test here.
	go func() {
		for range first.NewLines {
		}
	}()
	// Let the tailer read the three lines into the ring, then attach a
	// second subscriber that never reads: its replay has three lines to
	// send and a buffer sized by whatever FileOffset was a moment earlier.
	time.Sleep(2 * time.Second)
	sub, err := testTfc.Follow(tmpfile.Name())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		sub.Stop()
		first.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("TailedFileSubscriber.Stop deadlocked against Subscribe's backfill goroutine")
	}
}
