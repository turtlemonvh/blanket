package tailed_file

import (
	"github.com/hpcloud/tail"
	"math/rand"
	"os"
	"sync"
)

const (
	// How many bytes to back up when loading file
	DefaultFileOffset = 1000
	DefaultLinesKept  = 100
)

var (
	tailedFiles *TailedFiles
)

type TailedFiles struct {
	files map[string]*TailedFile
	sync.Mutex
}

type TailedFileSubscriber struct {
	NewLines   chan string
	IsCaughtUp bool
	Id         int64
}

type TailedFile struct {
	Filepath    string
	PastLines   []string
	FileOffset  int64
	Tailer      *tail.Tail
	Subscribers map[int64]*TailedFileSubscriber
	sync.Mutex  // guards FileOffset
}

func init() {
	// Initialize container for list of actively tailed files
	tailedFiles = &TailedFiles{
		files: make(map[string]*TailedFile),
	}
}

/*
 * Functions operating on a TailedFiles object
 */

func (tfs *TailedFiles) GetTailedFile(p string) (*TailedFile, error) {
	tfs.Lock()
	defer tfs.Unlock()

	// Check if it already exists
	if tailedFiles.files[p] != nil {
		return tailedFiles.files[p], nil
	}
	tf, err := StartTailedFile(p)
	if err != nil {
		return nil, err
	}

	tailedFiles.files[p] = tf

	return tf, nil
}
func GetTailedFile(p string) (*TailedFile, error) {
	return tailedFiles.GetTailedFile(p)
}

// Does not return an error if it doesn't exist
func (tfs *TailedFiles) StopTailedFile(p string) error {
	tfs.Lock()
	defer tfs.Unlock()
	if tailedFiles.files[p] == nil {
		return nil
	}
	return tailedFiles.files[p].Close()
}
func StopTailedFile(p string) error {
	return tailedFiles.StopTailedFile(p)
}

func (tfs *TailedFiles) StopAll() {
	tfs.Lock()
	defer tfs.Unlock()
	for _, tf := range tfs.files {
		tf.Close()
	}
}
func StopAll() {
	tailedFiles.StopAll()
}

func (tfs *TailedFiles) Follow(p string) (*TailedFileSubscriber, error) {
	tf, err := tfs.GetTailedFile(p)
	if err != nil {
		return nil, err
	}
	return tf.Subscribe(), nil
}
func Follow(p string) (*TailedFileSubscriber, error) {
	return tailedFiles.Follow(p)
}

func (tfs *TailedFiles) Unfollow(p string, id int64) error {
	tf, err := tfs.GetTailedFile(p)
	if err != nil {
		return err
	}
	tf.Unsubscribe(id)
	return nil
}
func Unfollow(p string, id int64) error {
	return tailedFiles.Unfollow(p, id)
}

/*
 * Functions operating on a single TailedFile object
 */

// Alternative approaches:
// - https://groups.google.com/d/msg/golang-nuts/-pPG4Oacsf0/0DxUv__DgKoJ
// - https://golang.org/pkg/container/ring/
func StartTailedFile(p string) (*TailedFile, error) {
	// Launch go routine to read from file
	// Adds each line to NewContent
	// Increments CurrentIndex, adds this line at CurrentIndex

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
		Filepath:    p,
		PastLines:   make([]string, DefaultLinesKept),
		FileOffset:  0,
		Tailer:      tailer,
		Subscribers: make(map[int64]*TailedFileSubscriber),
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

// FIXME: Do this on the TailedFiles level
// - Remove from list of available tailed files
// - Close all channels
// - Stop the tailer
func (tf *TailedFile) Close() error {
	tf.Lock()
	defer tf.Unlock()
	delete(tailedFiles.files, tf.Filepath)
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

func (tf *TailedFile) Unsubscribe(id int64) {
	tf.Lock()
	defer tf.Unlock()
	delete(tf.Subscribers, id)
}
