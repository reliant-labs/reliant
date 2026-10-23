package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// lockedWriter serializes both streams, even when they share one sink. Output
// stays live (including while a full lane queues for a heavy slot), rather than
// retaining a potentially huge verbose test log in memory until every lane ends.
type lockedWriter struct {
	mu  *sync.Mutex
	dst io.Writer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dst.Write(p)
}

func (r *runner) runSteps(ctx context.Context, steps []step) int {
	var output sync.Mutex
	stdout := lockedWriter{&output, r.out}
	stderr := lockedWriter{&output, r.errOut}
	execute := r.execute
	if execute == nil {
		execute = func(ctx context.Context, s step, out, errOut io.Writer) error {
			cmd := exec.CommandContext(ctx, s.argv[0], s.argv[1:]...)
			cmd.Dir = s.dir
			cmd.Env = append(os.Environ(), s.env...)
			cmd.Stdout, cmd.Stderr = out, errOut
			return cmd.Run()
		}
	}
	run := func(s step) bool {
		fmt.Fprintf(stdout, "==> %s: %s\n", s.name, summarize(s.argv))
		if r.dry {
			return false
		}
		start := time.Now()
		err := execute(ctx, s, stdout, stderr)
		status := "ok"
		if err != nil {
			status = "FAILED: " + err.Error()
		}
		fmt.Fprintf(stdout, "<== %s: %s (%s)\n", s.name, status, time.Since(start).Round(time.Millisecond))
		return err != nil
	}
	// Lockfile validation and both generators are prerequisites. Generation
	// mutates test inputs and must never overlap with a test or another generator.
	// A failure must not leave a green test run judging stale generated output.
	var tests, exclusive []step
	for _, s := range steps {
		if s.prerequisite {
			if run(s) {
				return 1
			}
		} else if s.exclusive {
			exclusive = append(exclusive, s)
		} else {
			tests = append(tests, s)
		}
	}
	jobs := r.jobs
	if jobs < 1 {
		jobs = 2
	}
	indices := make(chan int)
	failed := make([]bool, len(tests))
	var workers sync.WaitGroup
	for j := 0; j < jobs && j < len(tests); j++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range indices {
				failed[i] = run(tests[i])
			}
		}()
	}
	for i := range tests {
		indices <- i
	}
	close(indices)
	workers.Wait()
	anyFailed := false
	for _, bad := range failed {
		anyFailed = anyFailed || bad
	}
	// Electron's release contract fixtures rewrite endpoint caches (and, when
	// reachable, KCL). They cannot share their inputs with Go/web readers.
	for _, s := range exclusive {
		if run(s) {
			anyFailed = true
		}
	}
	if anyFailed {
		return 1
	}
	return 0
}
