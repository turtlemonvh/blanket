package tailed_file

import (
	log "github.com/Sirupsen/logrus"
	"github.com/hpcloud/tail"
	"math/rand"
	"os"
	"sync"
	_ "time"
)

const (
	// How many bytes to back up when loading file
	DefaultFileOffset = 5000
	DefaultLinesKept  = 100
)

var (
	defaultTfc *TailedFileCollection
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
	defaultTfc = &TailedFileCollection{
		fileList: make(map[string]*TailedFile),
	}

	// Start cleanup loop
	// Seems to cause problems
	/*
		go func() {
			for true {
				defaultTfc.Lock()
				log.WithFields(log.Fields{
					"files": defaultTfc.fileList,
				}).Info("Checking for files with no subscribers")

				for _, tf := range defaultTfc.fileList {
					log.WithFields(log.Fields{
						"nsubs": len(tf.Subscribers),
						"file":  tf.Filepath,
					}).Info("Checking # subscribers")

					if len(tf.Subscribers) == 0 {
						log.WithFields(log.Fields{
							"tailedFile": tf,
							"file":       tf.Filepath,
						}).Info("Closing tailed file because no subscribers remain")
						defaultTfc.StopTailedFile(tf.Filepath)
					}
				}
				defaultTfc.Unlock()
				time.Sleep(10000 * time.Millisecond)
			}
		}()
	*/
}

/*
 * Functions operating on a TailedFiles object
 */

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
// - calls Close() on each tailed file
// - removes any references to this file
func (tfc *TailedFileCollection) StopTailedFile(p string) error {
	tfc.Lock()
	defer tfc.Unlock()
	if tfc.fileList[p] == nil {
		return nil
	}
	err := tfc.fileList[p].Close()
	delete(tfc.fileList, p)

	return err
}
func StopTailedFile(p string) error {
	return defaultTfc.StopTailedFile(p)
}

// Stop every tailed file
// Called at shut down
func (tfc *TailedFileCollection) StopAll() {
	tfc.Lock()
	defer tfc.Unlock()
	for _, tf := range tfc.fileList {
		tf.Close()
		delete(tfc.fileList, tf.Filepath)
	}
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

	// Start back DefaultFileOffset bytes or the start of the file
	fileOffset := int64(DefaultFileOffset)
	if finfo.Size() < fileOffset {
		fileOffset = finfo.Size()
	}

	tailer, err := tail.TailFile(p, tail.Config{
		Location: &tail.SeekInfo{
			Offset: -fileOffset,
			Whence: os.SEEK_END,
		},
		Follow: true,
		Poll:   true, // better cross platform support than inotify
	})
	if err != nil {
		return nil, err
	}

	tf := &TailedFile{
		Filepath:       p,
		PastLines:      make([]string, DefaultLinesKept),
		FileOffset:     0,
		Tailer:         tailer,
		Subscribers:    make(map[int64]*TailedFileSubscriber),
		FilesContainer: tfc,
	}

	// Shuts down when channel closes
	go func() {
		for nline := range tailer.Lines {
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

// - Remove from list of available tailed files
// - Close all channels
// - Stop the tailer
func (tf *TailedFile) Close() error {
	tf.Lock()
	defer tf.Unlock()
	for _, sub := range tf.Subscribers {
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
		}).Info("Subscribing")

		for i := tf.FileOffset + 1; i < tf.FileOffset+int64(len(tf.PastLines)); i++ {
			item := tf.PastLines[i%int64(len(tf.PastLines))]
			if item != "" {
				sub.NewLines <- item
			}
		}
		sub.IsCaughtUp = true
	}()

	return sub
}

// Deregister subscriber
func (tfs *TailedFileSubscriber) Stop() {
	tfs.TailedFile.Lock()
	defer tfs.TailedFile.Unlock()

	log.WithFields(log.Fields{
		"subs":  tfs.TailedFile.Subscribers,
		"subId": tfs.Id,
	}).Info("Unsubscribing")

	delete(tfs.TailedFile.Subscribers, tfs.Id)
}
