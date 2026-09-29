package controller

import (
	"context"
	"sort"
	"sync"
)

// AccessLocks orders selected-application changes with tunnel publication in one manager.
// Entries exist only while held or awaited; cancellation never retains a resource key.
type AccessLocks struct {
	mu      sync.Mutex
	entries map[string]*accessLockEntry
}
type accessLockEntry struct {
	token chan struct{}
	refs  int
}

func NewAccessLocks() *AccessLocks { return &AccessLocks{entries: make(map[string]*accessLockEntry)} }
func (l *AccessLocks) acquire(ctx context.Context, keys []string) (func(), error) {
	keys = append([]string(nil), keys...)
	sort.Strings(keys)
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for i, key := range keys {
		if i > 0 && keys[i-1] == key {
			continue
		}
		l.mu.Lock()
		entry := l.entries[key]
		if entry == nil {
			entry = &accessLockEntry{token: make(chan struct{}, 1)}
			entry.token <- struct{}{}
			l.entries[key] = entry
		}
		entry.refs++
		l.mu.Unlock()
		drop := func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			entry.refs--
			if entry.refs == 0 {
				delete(l.entries, key)
			}
		}
		select {
		case <-ctx.Done():
			drop()
			release()
			return nil, ctx.Err()
		case <-entry.token:
			releases = append(releases, func() { entry.token <- struct{}{}; drop() })
		}
	}
	return release, nil
}
