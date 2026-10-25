package auth

import (
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// LastUsedInterval is the most often one token's lastUsed is written
// (A.4), so bolt's single writer isn't taken on every request.
const LastUsedInterval = time.Minute

// LastUsedBatcher records token use without a write per request. Touch is
// cheap and never blocks on the database: at most once per interval per
// token it hands one write to a goroutine.
type LastUsedBatcher struct {
	store    Store
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	written map[string]time.Time // hash -> last time a write was started
	wg      sync.WaitGroup
}

// NewLastUsedBatcher returns a batcher writing to store.
func NewLastUsedBatcher(store Store) *LastUsedBatcher {
	return &LastUsedBatcher{
		store:    store,
		interval: LastUsedInterval,
		now:      time.Now,
		written:  map[string]time.Time{},
	}
}

// Touch notes that the token stored under hash was just used.
func (b *LastUsedBatcher) Touch(hash string) {
	now := b.now()
	b.mu.Lock()
	last, ok := b.written[hash]
	if ok && now.Sub(last) < b.interval {
		b.mu.Unlock()
		return
	}
	b.written[hash] = now
	b.mu.Unlock()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		if err := b.store.TouchToken(hash, now.Unix()); err != nil {
			// Never log the hash: it is not the token, but it is the
			// key that finds the row.
			log.WithField("err", err).Debug("could not record token lastUsed")
		}
	}()
}

// Wait blocks until every write already started has finished. Tests use
// it; the server's shutdown sequence doesn't need to, since a lost
// lastUsed write costs nothing.
func (b *LastUsedBatcher) Wait() {
	b.wg.Wait()
}
