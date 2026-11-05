package cliutil

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// defaultHeartbeatInterval is how long a step may run before Heartbeat first
// speaks, and how often it repeats.
//
// 15s is under the shortest "no output" cut-off the agent sandboxes forge runs
// in apply (a silent minute reads as a hang), and long enough that a fast
// step, and so a clean `--quiet` run, prints nothing extra.
const defaultHeartbeatInterval = 15 * time.Second

// Heartbeat reports that label is still running, on w, every 15 seconds until
// the returned stop is called. A step that finishes inside the first interval
// prints nothing. Output goes to w (stderr at every call site), never stdout:
// a --json document or a verdict line stays alone there.
//
//	stop := cliutil.Heartbeat(os.Stderr, "generate: buf generate")
//	err := run()
//	stop()
//
// stop is idempotent and waits for the reporter to exit, so no line can land
// after the step's own output.
func Heartbeat(w io.Writer, label string) (stop func()) {
	return HeartbeatEvery(w, label, defaultHeartbeatInterval)
}

// HeartbeatEvery is Heartbeat with an explicit interval, for a caller (a test)
// that cannot wait 15 seconds.
func HeartbeatEvery(w io.Writer, label string, interval time.Duration) (stop func()) {
	if w == nil || interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	exited := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(exited)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fmt.Fprintf(w, "⏳ %s — still running (%s elapsed)\n", label, time.Since(start).Round(time.Second))
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-exited
	}
}
