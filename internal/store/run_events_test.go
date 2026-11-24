package store

import (
	"sync"
	"testing"
)

func TestRunWatchersCoalesceAndUnsubscribeConcurrently(t *testing.T) {
	db := openTest(t)
	changes, stop := db.WatchRun("one")
	other, stopOther := db.WatchRun("other")
	defer stopOther()
	var group sync.WaitGroup
	for range 50 {
		group.Go(func() { db.notifyRun("one") })
	}
	group.Wait()
	if len(changes) != 1 || len(other) != 0 {
		t.Fatal("run wakeups are not bounded or isolated")
	}
	<-changes
	stop()
	stop()
	db.notifyRun("one")
	if len(changes) != 0 {
		t.Fatal("unsubscribed listener was retained")
	}
	for range 50 {
		group.Go(func() { _, stop := db.WatchRun("one"); db.notifyRun("one"); stop() })
	}
	group.Wait()
	db.runListenersMu.Lock()
	defer db.runListenersMu.Unlock()
	if len(db.runListeners) != 1 {
		t.Fatal("listeners leaked")
	}
}

func TestRunStateWatchersExcludeOutputAndShareCommittedStateChanges(t *testing.T) {
	db := openTest(t)
	output, stopOutput := db.WatchRun("one")
	defer stopOutput()
	state, stopState := db.WatchRunState("one")
	defer stopState()
	db.notifyRun("one")
	if len(output) != 1 || len(state) != 0 {
		t.Fatal("output woke the control subscription")
	}
	<-output
	db.notifyRunState("one")
	if len(output) != 1 || len(state) != 1 {
		t.Fatal("state change did not wake both subscriptions")
	}
	<-state
	stopState()
	db.notifyRunState("one")
	if len(state) != 0 {
		t.Fatal("state listener was retained after unsubscribe")
	}
}
