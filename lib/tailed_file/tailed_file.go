package tailed_file

import (
	log "github.com/Sirupsen/logrus"
	"github.com/codahale/metrics"
	"github.com/hpcloud/tail"
	"gopkg.in/tomb.v1"
	"math/rand"
	"os"
	"sync"
	"time"
)

const (
	// How many bytes to back up when loading file
	DefaultFileOffset = 5000
	DefaultLinesKept  = 100
)

var (
	defaultTfc             *TailedFileCollection
	nTailedFiles           = metrics.Gauge("nTailedFiles")
	nTailedFileSubscribers = metrics.Gauge("nTailedFiles")
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
}

// Use the mutex to guard access to FileOffset, Subscribers
type TailedFile struct {
	Filepath       string
	PastLines      []string
	FileOffset     int64
	Tailer         *tail.Tail
	Subscribers    map[int64]*TailedFileSubscriber
	FilesContainer *TailedFileCollection
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
				// Update gauges
				nTailedFiles.Set(int64(len(defaultTfc.fileList)))
				nTailedFiles.Set(defaultTfc.GetSubscriberCount())
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()
}

/*
 * Functions operating on a TailedFiles object
 */
func NewTailedFileCollection() *TailedFileCollection {
	return &TailedFileCollection{
		fileList: make(map[string]*TailedFile),
	}
}

func (tfc *TailedFileCollection) GetSubscriberCount() int64 {
	ntotal := 0
	for _, tf := range tfc.fileList {
		ntotal += len(tf.Subscribers)
	}
	return int64(ntotal)
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
		log.WithFields(log.Fields{
			"tailer": tailer,
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

			// Send to each subscriber channel
			// In practice shouldn't block because buffered
			for _, sub := range tf.Subscribers {
				sub.NewLines <- nline.Text
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

	// Checking without lock to prevent deadlock in delete operation
	if len(tf.Subscribers) == 0 {
		// Still no new subscribers
		log.WithFields(log.Fields{
			"file":              tf.Filepath,
			"subs":              tf.Subscribers,
			"filesInCollection": tf.FilesContainer.fileList,
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
	return tf.Tailer.Stop()
}

// Return a channel that sends strings
func (tf *TailedFile) Subscribe() *TailedFileSubscriber {
	// Buffer by the file offset so adding initial lines doesn't block
	sub := &TailedFileSubscriber{
		NewLines:   make(chan string, tf.FileOffset),
		IsCaughtUp: false,
		TailedFile: tf,
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

		log.WithFields(log.Fields{
			"subs":  tf.Subscribers,
			"subId": sub.Id,
		}).Info("Subscribed")

		nlines := 0
		for i := tf.FileOffset + 1; i < tf.FileOffset+int64(len(tf.PastLines)); i++ {
			item := tf.PastLines[i%int64(len(tf.PastLines))]
			if item != "" {
				sub.NewLines <- item
				nlines++
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

// Deregister subscriber
func (tfs *TailedFileSubscriber) Stop() {
	tfs.TailedFile.Lock()
	defer tfs.TailedFile.Unlock()
	delete(tfs.TailedFile.Subscribers, tfs.Id)
	close(tfs.NewLines)
	log.WithFields(log.Fields{
		"subs":  tfs.TailedFile.Subscribers,
		"subId": tfs.Id,
	}).Info("Unsubscribed")

	if len(tfs.TailedFile.Subscribers) == 0 {
		// Stop tailing if there are still no subscribers after a few seconds
		go tfs.TailedFile.FilesContainer.StopIfNoSubscribers(tfs.TailedFile)
	}
}
