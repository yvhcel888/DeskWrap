// DeskWrap unit tests - service lifecycle guards (cross-platform).
package main

import (
	"os/exec"
	"testing"
	"time"
)

// A user action that lands inside the auto-restart window must cancel the
// pending auto-restart. Otherwise the watcher of the dead process starts a
// second service on top of the one the user just launched (the "retry" +
// autoRestart double-spawn).
func TestAutoRestartWaitAbortsWhenUserRestarts(t *testing.T) {
	prevQuit := quitting.Load()
	quitting.Store(false)
	defer quitting.Store(prevQuit)

	serviceMu.Lock()
	prevGen := serviceGen
	serviceGen++ // stand in for the user's stopService()+startService()
	gen := serviceGen
	serviceMu.Unlock()
	defer func() {
		serviceMu.Lock()
		serviceGen = prevGen
		serviceMu.Unlock()
	}()

	var fired bool
	done := make(chan struct{})
	go func() {
		fired = autoRestartWait(gen - 1) // the watcher of the *old* process
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("autoRestartWait did not return after a user restart")
	}
	if fired {
		t.Fatal("stale watcher restarted the service: duplicate process")
	}
}

// The normal path must still wait out the full window and then restart.
func TestAutoRestartWaitReturnsTrueWhenUndisturbed(t *testing.T) {
	prevQuit := quitting.Load()
	quitting.Store(false)
	defer quitting.Store(prevQuit)

	serviceMu.Lock()
	gen := serviceGen
	serviceMu.Unlock()

	start := time.Now()
	if !autoRestartWait(gen) {
		t.Fatal("autoRestartWait = false for the current generation, want true")
	}
	if elapsed := time.Since(start); elapsed < 1500*time.Millisecond {
		t.Fatalf("returned after %s, want the full 1.5s window", elapsed)
	}
}

// The guard must also cover the fast path: the OS reaping the dead process
// can finish well inside the window, so the wait cannot be skipped - it is
// what makes the stale-generation check possible at all.
func TestAutoRestartWaitFastPathStillGuarded(t *testing.T) {
	prevQuit := quitting.Load()
	quitting.Store(false)
	defer quitting.Store(prevQuit)

	c := exec.Command("cmd.exe", "/c", "exit", "3")
	if err := c.Start(); err != nil {
		t.Skipf("cannot spawn cmd.exe: %v", err)
	}
	_ = c.Wait() // as watchServiceExit would: already dead when we get here

	serviceMu.Lock()
	gen := serviceGen
	serviceGen++ // user restarts while the exit watcher is deciding
	serviceMu.Unlock()
	defer func() {
		serviceMu.Lock()
		serviceGen = gen
		serviceMu.Unlock()
	}()

	if autoRestartWait(gen) {
		t.Fatal("autoRestartWait = true although the generation moved on")
	}
}
