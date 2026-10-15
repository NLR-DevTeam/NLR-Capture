package capture

import (
	"sync"
	"sync/atomic"
)

// Store keeps the captured sessions in arrival order. It is safe for
// concurrent use. Sessions are replaced, never modified, so the slices
// returned by All stay valid after later updates.
type Store struct {
	mu      sync.RWMutex
	list    []*Session
	index   map[int64]int
	version atomic.Uint64
	nextID  atomic.Int64
	// snap caches the slice returned by All for the current version.
	snap    []*Session
	snapVer uint64

	// Notify, when set, is called after every change.
	Notify func()
	// Limit is the number of sessions kept; the oldest are dropped first.
	// Zero means no limit.
	Limit int
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{index: map[int64]int{}}
}

func (st *Store) changed() {
	st.version.Add(1)
	if st.Notify != nil {
		st.Notify()
	}
}

// Version changes whenever the store does.
func (st *Store) Version() uint64 { return st.version.Load() }

// Add assigns s an ID, stores it and returns it.
func (st *Store) Add(s *Session) *Session {
	s.ID = st.nextID.Add(1)
	st.mu.Lock()
	st.index[s.ID] = len(st.list)
	st.list = append(st.list, s)
	if st.Limit > 0 && len(st.list) > st.Limit+st.Limit/10 {
		st.trimLocked(len(st.list) - st.Limit)
	}
	st.mu.Unlock()
	st.changed()
	return s
}

// AddAll stores several sessions with one notification.
func (st *Store) AddAll(ss []*Session) {
	st.mu.Lock()
	for _, s := range ss {
		s.ID = st.nextID.Add(1)
		st.index[s.ID] = len(st.list)
		st.list = append(st.list, s)
	}
	st.mu.Unlock()
	st.changed()
}

func (st *Store) trimLocked(n int) {
	st.list = append([]*Session(nil), st.list[n:]...)
	st.reindexLocked()
}

func (st *Store) reindexLocked() {
	st.index = make(map[int64]int, len(st.list))
	for i, s := range st.list {
		st.index[s.ID] = i
	}
}

// Update replaces the session with the given ID by a copy changed by f and
// returns the new session, or nil if the session is gone.
func (st *Store) Update(id int64, f func(s *Session)) *Session {
	st.mu.Lock()
	i, ok := st.index[id]
	if !ok {
		st.mu.Unlock()
		return nil
	}
	ns := *st.list[i]
	f(&ns)
	st.list[i] = &ns
	st.mu.Unlock()
	st.changed()
	return &ns
}

// Get returns the session with the given ID, or nil.
func (st *Store) Get(id int64) *Session {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if i, ok := st.index[id]; ok {
		return st.list[i]
	}
	return nil
}

// All returns the sessions in arrival order. The slice must not be
// modified.
func (st *Store) All() []*Session {
	v := st.version.Load()
	st.mu.RLock()
	if st.snap != nil && st.snapVer == v {
		s := st.snap
		st.mu.RUnlock()
		return s
	}
	out := make([]*Session, len(st.list))
	copy(out, st.list)
	st.mu.RUnlock()
	st.mu.Lock()
	st.snap, st.snapVer = out, v
	st.mu.Unlock()
	return out
}

// Len returns the number of sessions.
func (st *Store) Len() int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.list)
}

// Clear removes every session.
func (st *Store) Clear() {
	st.mu.Lock()
	st.list = nil
	st.index = map[int64]int{}
	st.mu.Unlock()
	st.changed()
}

// Remove deletes the sessions with the given IDs.
func (st *Store) Remove(ids map[int64]bool) {
	if len(ids) == 0 {
		return
	}
	st.mu.Lock()
	out := st.list[:0:0]
	for _, s := range st.list {
		if !ids[s.ID] {
			out = append(out, s)
		}
	}
	st.list = out
	st.reindexLocked()
	st.mu.Unlock()
	st.changed()
}
