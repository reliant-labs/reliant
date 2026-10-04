// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"sync"
	"testing"
)

// Request ids key the gateway's pending-response map, so two concurrent
// requests sharing one cross their replies. The old time.Now().UnixNano() id
// collided for goroutines started together — on macOS in 1996 of 2000 trials
// of three — which is exactly how a parallel workspace create fans out.
func TestNewRequestIDIsUniqueUnderConcurrency(t *testing.T) {
	const goroutines = 64
	const perGoroutine = 64

	start := make(chan struct{})
	ids := make([][]string, goroutines)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			<-start
			for range perGoroutine {
				ids[g] = append(ids[g], newRequestID())
			}
		})
	}
	close(start)
	wg.Wait()

	seen := make(map[string]bool, goroutines*perGoroutine)
	for _, batch := range ids {
		for _, id := range batch {
			if seen[id] {
				t.Fatalf("request id %q issued twice to concurrent callers", id)
			}
			seen[id] = true
		}
	}
}
