package store

type runWatchKey struct {
	id        string
	stateOnly bool
}

// WatchRun coalesces committed changes into wakeups. Subscribers read persisted
// state by cursor; slow clients cannot block workers or accumulate output here.
// It is internal plumbing; callers must authorize access before subscribing.
func (s *Store) WatchRun(runID string) (<-chan struct{}, func()) {
	return s.watchRun(runWatchKey{id: runID})
}

// WatchRunState excludes output commits so a busy log cannot flood control
// connections with redundant status reads. Authorization is the caller's duty.
func (s *Store) WatchRunState(runID string) (<-chan struct{}, func()) {
	return s.watchRun(runWatchKey{id: runID, stateOnly: true})
}

func (s *Store) watchRun(key runWatchKey) (<-chan struct{}, func()) {
	changes := make(chan struct{}, 1)
	s.runListenersMu.Lock()
	if s.runListeners == nil {
		s.runListeners = make(map[runWatchKey]map[chan struct{}]struct{})
	}
	if s.runListeners[key] == nil {
		s.runListeners[key] = make(map[chan struct{}]struct{})
	}
	s.runListeners[key][changes] = struct{}{}
	s.runListenersMu.Unlock()
	return changes, func() {
		s.runListenersMu.Lock()
		defer s.runListenersMu.Unlock()
		delete(s.runListeners[key], changes)
		if len(s.runListeners[key]) == 0 {
			delete(s.runListeners, key)
		}
	}
}

func (s *Store) notifyRun(runID string) {
	s.notifyRunKey(runWatchKey{id: runID})
}

func (s *Store) notifyRunState(runID string) {
	s.notifyRun(runID)
	s.notifyRunKey(runWatchKey{id: runID, stateOnly: true})
}

func (s *Store) notifyRunKey(key runWatchKey) {
	s.runListenersMu.Lock()
	defer s.runListenersMu.Unlock()
	for listener := range s.runListeners[key] {
		select {
		case listener <- struct{}{}:
		default:
		}
	}
}
