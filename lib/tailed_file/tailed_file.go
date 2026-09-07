package tailed_file

import (
	"expvar"
	"github.com/hpcloud/tail"
	log "github.com/sirupsen/logrus"
	"gopkg.in/tomb.v1"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// How many bytes to back up when loading file
	DefaultFileOffset = 5000
	DefaultLinesKept  = 100
)

var (
	defaultTfc             *TailedFileCollection
	nTailedFiles           = expvar.NewInt("nTailedFiles")
	nTailedFileSubscribers = expvar.NewInt("nTailedFileSubscribers")
)

type TailedFileCollection struct {
	fileList map[string]*TailedFile
	sync.Mutex
}

type TailedFileSubscriber struct {
	NewLines   chan string
	IsCaughtUp bool
	Id         int64
	TailedFile *TailedFile

	// done is closed by Stop, before Stop takes tf.Lock(). The tailer
	// goroutine (StartTailedFile's inner func) holds tf.Lock() across its
	// entire per-line fan-out, including the blocking send to each
	// subscriber's NewLines -- so a handler that has stopped reading
	// NewLines and then calls Stop would otherwise deadlock: the tailer
	// sits parked mid-send holding the lock Stop needs, and nothing
	// drains NewLines to free it. Selecting on done in that send (see the
	// fan-out loop below) gives the tailer an escape hatch that needs no
	// lock, so Stop can always make progress -- see turtlemonvh/blanket#123,
	// which hit exactly this with GET /task/:id/log's bare `defer sub.Stop()`.
	done     chan struct{}
	stopOnce sync.Once
}

// Use the mutex to guard access to FileOffset, Subscribers.
//
// subscriberCount mirrors len(Subscribers) but is read/written with
// sync/atomic instead of tf's mutex -- deliberately, not just as an
// optimization. The tailer goroutine below (StartTailedFile's inner func)
// holds tf.Lock() for the entire fan-out to subscriber channels, including
// the blocking send itself (subscriber channels are unbuffered until a
// caller has backfilled past lines into them). A reader that needs the
// subscriber count -- GetSubscriberCount, called from the cleanup
// goroutine's gauge update on every 2s tick, and StopIfNoSubscribers --
// must not take tf.Lock() to get it: if it did, and it ran while the
// tailer goroutine was mid-send waiting on a slow/absent receiver, the two
// would deadlock (the reader blocked on the lock the tailer holds, the
// tailer blocked on a channel only that same reader's goroutine drains).
// This isn't hypothetical -- it reproduced as a hang in
// TestStreamLogSingleSub during turtlemonvh/blanket#119 once
// GetSubscriberCount started taking tf.Lock(). The atomic counter sidesteps
// it entirely: readers never need the lock, so they can never queue behind
// a blocked send. See the writers (Subscribe, Stop, TailedFileSubscriber.Stop)
// for where it's kept in sync with the map.
type TailedFile struct {
	Filepath        string
	PastLines       []string
	FileOffset      int64
	Tailer          *tail.Tail
	Subscribers     map[int64]*TailedFileSubscriber
	FilesContainer  *TailedFileCollection
	subscriberCount int64
	sync.Mutex
}

func init() {
	// Initialize container for list of actively tailed files
	defaultTfc = NewTailedFileCollection()

	// Track number of open files
	ticker := time.NewTicker(2 * time.Second)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				defaultTfc.updateGauges()
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()
}

// updateGauges refreshes the nTailedFiles/nTailedFileSubscribers expvars.
// Pulled out of the init() ticker goroutine (rather than inlined) so a test
// can invoke it directly against a concurrent workload instead of waiting on
// the 2-second ticker to fire — see TestConcurrentCollectionAccessAgainstCleanup.
//
// Both fileCount and GetSubscriberCount take their own locks internally, so
// this reads a consistent-enough snapshot without holding tfc's lock across
// both calls (that would just widen the window another goroutine could be
// blocked in, for gauges that are inherently a little stale anyway).
func (tfc *TailedFileCollection) updateGauges() {
	nTailedFiles.Set(tfc.fileCount())
	nTailedFileSubscribers.Set(tfc.GetSubscriberCount())
}

/*
 * Functions operating on a TailedFiles object
 */
func NewTailedFileCollection() *TailedFileCollection {
	return &TailedFileCollection{
		fileList: make(map[string]*TailedFile),
	}
}

// fileCount returns how many files are currently tailed, guarded by tfc's
// lock so it's safe to call from the cleanup goroutine concurrently with
// GetTailedFile/StopTailedFile/StopAll.
func (tfc *TailedFileCollection) fileCount() int64 {
	tfc.Lock()
	defer tfc.Unlock()
	return int64(len(tfc.fileList))
}

func (tfc *TailedFileCollection) GetSubscriberCount() int64 {
	tfc.Lock()
	defer tfc.Unlock()

	// atomic.LoadInt64, not tf.Lock() -- see the subscriberCount comment on
	// TailedFile for why taking tf's mutex here can deadlock against the
	// tailer goroutine.
	var ntotal int64
	for _, tf := range tfc.fileList {
		ntotal += atomic.LoadInt64(&tf.subscriberCount)
	}
	return ntotal
}

func (tfc *TailedFileCollection) GetTailedFile(p string) (*TailedFile, error) {
	tfc.Lock()
	defer tfc.Unlock()

	// Check if it already exists
	if tfc.fileList[p] != nil {
		log.WithFields(log.Fields{
			"file": p,
		}).Info("Found existing TailedFile")
		return tfc.fileList[p], nil
	}
	tf, err := StartTailedFile(p)
	if err != nil {
		return nil, err
	}

	tfc.fileList[p] = tf

	return tf, nil
}
func GetTailedFile(p string) (*TailedFile, error) {
	return defaultTfc.GetTailedFile(p)
}

// Stop a specific tailed file
// - locks the object
// - calls Stop() on each tailed file
// - removes any references to this file
func (tfc *TailedFileCollection) StopTailedFile(p string) error {
	tfc.Lock()
	defer tfc.Unlock()
	if tfc.fileList[p] == nil {
		return nil
	}
	err := tfc.fileList[p].Stop()
	delete(tfc.fileList, p)

	return err
}
func StopTailedFile(p string) error {
	return defaultTfc.StopTailedFile(p)
}

// Stop every tailed file
// Called at shut down
// Does the same thing as StopTailedFile but with a lock over the whole loop
func (tfc *TailedFileCollection) StopAll() {
	tfc.Lock()
	defer tfc.Unlock()
	for _, tf := range tfc.fileList {
		log.WithFields(log.Fields{
			"filepath": tf.Filepath,
		}).Info("Stopping tailed file in StopAll")
		tf.Stop()
		delete(tfc.fileList, tf.Filepath)
	}
	log.Info("Finished closing all tailedfiles in StopAll")
}
func StopAll() {
	defaultTfc.StopAll()
}

// Follow a file at a given path
func (tfc *TailedFileCollection) Follow(p string) (*TailedFileSubscriber, error) {
	tf, err := tfc.GetTailedFile(p)
	if err != nil {
		return nil, err
	}
	return tf.Subscribe(), nil
}
func Follow(p string) (*TailedFileSubscriber, error) {
	return defaultTfc.Follow(p)
}

/*
 * Functions operating on a single TailedFile object
 */

// Alternative approaches:
// - https://groups.google.com/d/msg/golang-nuts/-pPG4Oacsf0/0DxUv__DgKoJ
// - https://golang.org/pkg/container/ring/
func (tfc *TailedFileCollection) StartTailedFile(p string) (*TailedFile, error) {
	// Launch go routine to read from file
	// Adds each line to NewContent
	// Increments CurrentIndex, adds this line at CurrentIndex

	log.WithFields(log.Fields{
		"file": p,
	}).Info("Creating new tailed file")

	// Check for valid file path
	finfo, err := os.Stat(p)
	if err != nil {
		return nil, err
	}

	// By default start at the start of the file
	offsetConf := &tail.SeekInfo{
		Offset: 0,
		Whence: os.SEEK_SET,
	}
	if finfo.Size() >= int64(DefaultFileOffset) {
		// If the current size of the file is larger than the default offset, seek to a location in the file
		offsetConf = &tail.SeekInfo{
			Offset: -int64(DefaultFileOffset),
			Whence: os.SEEK_END,
		}
	}

	// FIXME: Runs in a goroutine so can be racey wrt fast growing files
	// https://github.com/hpcloud/tail/blob/master/tail.go#L132
	tailer, err := tail.TailFile(p, tail.Config{
		Location: offsetConf,
		Follow:   true,
		Poll:     true, // better cross platform support than inotify
	})
	if err != nil {
		return nil, err
	}

	// FIXME: FileOffset should be linesOffset
	tf := &TailedFile{
		Filepath:       p,
		PastLines:      make([]string, DefaultLinesKept),
		FileOffset:     0,
		Tailer:         tailer,
		Subscribers:    make(map[int64]*TailedFileSubscriber),
		FilesContainer: tfc,
	}

	log.WithFields(log.Fields{
		"file":       p,
		"offsetConf": offsetConf,
	}).Info("Preparing to read line in tailed logfile")

	// Shuts down when channel closes
	go func() {
		// Log the path, never the *tail.Tail itself: logrus' TextFormatter
		// fmt.Sprint()s field values, which reflects over every field of the
		// struct — including the file handle, reader and tomb counters that
		// hpcloud/tail's own goroutine mutates concurrently. That read is a
		// data race we cannot synchronize from out here.
		log.WithFields(log.Fields{
			"file": p,
		}).Info("In tailedfile goroutine, starting loop over lines")

		for nline := range tailer.Lines {
			log.WithFields(log.Fields{
				"file": p,
			}).Info("Read line in tailed logfile")

			// Lock to make sure no new subscribers are added until we update
			tf.Lock()

			// Add to the list of past lines and increment offset
			tf.FileOffset = (tf.FileOffset + 1) % int64(len(tf.PastLines))
			tf.PastLines[tf.FileOffset] = nline.Text

			// Send to each subscriber channel, or move on if that
			// subscriber is on its way out (sub.done closed by Stop).
			// Without the second case this send blocks forever against a
			// subscriber whose handler stopped reading and is trying to
			// Stop -- see the done field's comment on TailedFileSubscriber.
			for _, sub := range tf.Subscribers {
				select {
				case sub.NewLines <- nline.Text:
				case <-sub.done:
				}
			}

			// Free lock again
			tf.Unlock()
		}
	}()

	return tf, nil
}
func StartTailedFile(p string) (*TailedFile, error) {
	return defaultTfc.StartTailedFile(p)
}

// Closes tailed file if it still has 0 subscribers after 5 seconds
// Should not usually be called directly
func (tfc *TailedFileCollection) StopIfNoSubscribers(tf *TailedFile) {
	// FIXME: Use timeMultiplier
	time.Sleep(5 * time.Second)

	// atomic.LoadInt64, not tf.Lock() -- see the subscriberCount comment on
	// TailedFile. This is still check-then-act (a subscriber can join
	// between this read and StopTailedFile below), same as before the
	// #119 fix -- only the read itself is now race-free.
	subCount := atomic.LoadInt64(&tf.subscriberCount)

	if subCount == 0 {
		// Still no new subscribers
		log.WithFields(log.Fields{
			"file":              tf.Filepath,
			"subs":              subCount,
			"filesInCollection": tf.FilesContainer.fileCount(),
		}).Info("stopping tailed file because no subscribers remain")
		tfc.StopTailedFile(tf.Filepath)
	}
}

// Should usually only be called by functions on TailedFileCollection since those handle
// changes to the fileList
// - Close all subscriber channels
// - Removes all subscribers from the list
// - Stops the tailer
func (tf *TailedFile) Stop() error {
	tf.Lock()
	defer tf.Unlock()

	if tf.Tailer.Err() != tomb.ErrStillAlive {
		// Check to see if this is already exiting to avoid panic when closing closed channel
		log.WithFields(log.Fields{
			"filepath": tf.Filepath,
		}).Warn("Not stopping tailed file because already dying or dead")
		return nil
	}

	for _, sub := range tf.Subscribers {
		delete(tf.Subscribers, sub.Id)
		close(sub.NewLines)
	}
	atomic.StoreInt64(&tf.subscriberCount, 0)
	return tf.Tailer.Stop()
}

// Return a channel that sends strings
func (tf *TailedFile) Subscribe() *TailedFileSubscriber {
	// Buffer by the file offset so adding initial lines doesn't block.
	// Read FileOffset under tf's lock: the tailer goroutine in
	// StartTailedFile mutates it under the same lock every time a line
	// arrives, so reading it here unlocked would race with that write
	// (turtlemonvh/blanket#119 -- this one surfaced via the server
	// package's TestStreamTaskLog_StaysOpenUntilTerminal, which is a real
	// concurrent Follow-while-tailing rather than the cleanup-goroutine
	// races this issue started from, but the same fix applies: don't read
	// a tf field the tailer writes without going through tf.Lock()).
	tf.Lock()
	bufSize := tf.FileOffset
	tf.Unlock()

	sub := &TailedFileSubscriber{
		NewLines:   make(chan string, bufSize),
		IsCaughtUp: false,
		TailedFile: tf,
		done:       make(chan struct{}),
	}

	// Launch a goroutine that sends the first N lines on this channel
	// Will unlock when either
	// - all lines have been read
	// - channel is closed
	go func() {
		tf.Lock()
		defer tf.Unlock()

		// Assign a unique integer id
		sub.Id = rand.Int63()
		for true {
			if tf.Subscribers[sub.Id] != nil {
				sub.Id = rand.Int63()
			} else {
				break
			}
		}
		tf.Subscribers[sub.Id] = sub
		atomic.AddInt64(&tf.subscriberCount, 1)

		log.WithFields(log.Fields{
			"subs":  len(tf.Subscribers),
			"subId": sub.Id,
		}).Info("Subscribed")

		// Ring buffer backfill: walk all N slots starting from the oldest
		// (FileOffset+1) and ending at the newest (FileOffset). The original
		// loop bound excluded the newest slot, silently dropping the most
		// recent line whenever Subscribe landed after the tailer had already
		// written to PastLines[FileOffset].
		//
		// The send selects on sub.done for the same reason the tailer's
		// fan-out does: this goroutine holds tf.Lock() across the whole
		// backfill, and NewLines is buffered by FileOffset as read
		// *before* this goroutine ran. If the tailer got a line in between,
		// the buffer is one short and the last send blocks -- and a
		// subscriber that never reads (a handler on its way out) then
		// parks us here holding the lock Stop needs. Seen as an
		// intermittent CI deadlock in TestSubscriberStop_WhileNotReading.
		nlines := 0
		for i := tf.FileOffset + 1; i <= tf.FileOffset+int64(len(tf.PastLines)); i++ {
			item := tf.PastLines[i%int64(len(tf.PastLines))]
			if item == "" {
				continue
			}
			select {
			case sub.NewLines <- item:
				nlines++
			case <-sub.done:
				log.WithFields(log.Fields{
					"subId": sub.Id,
				}).Info("Subscriber stopped during backfill")
				return
			}
		}
		sub.IsCaughtUp = true

		log.WithFields(log.Fields{
			"subId":  sub.Id,
			"nlines": nlines,
		}).Info("Subscriber caught up")

	}()

	return sub
}

// Deregister subscriber. Safe to call more than once (only the first
// call does anything) and safe to call from a handler that has stopped
// reading NewLines -- see the done field's comment above for why that
// used to deadlock.
func (tfs *TailedFileSubscriber) Stop() {
	tfs.stopOnce.Do(func() {
		// Close done *before* taking the lock: if the tailer goroutine is
		// parked mid-send to this subscriber holding tf.Lock(), this is
		// what lets it give up on that send and continue, so the Lock()
		// below can't wait on a goroutine that is itself waiting on us.
		close(tfs.done)

		tfs.TailedFile.Lock()
		defer tfs.TailedFile.Unlock()
		delete(tfs.TailedFile.Subscribers, tfs.Id)
		atomic.AddInt64(&tfs.TailedFile.subscriberCount, -1)
		close(tfs.NewLines)
		log.WithFields(log.Fields{
			"subs":  len(tfs.TailedFile.Subscribers),
			"subId": tfs.Id,
		}).Info("Unsubscribed")

		if len(tfs.TailedFile.Subscribers) == 0 {
			// Stop tailing if there are still no subscribers after a few seconds
			go tfs.TailedFile.FilesContainer.StopIfNoSubscribers(tfs.TailedFile)
		}
	})
}
