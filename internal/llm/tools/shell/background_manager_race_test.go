// Copyright (c) 2025 Reliant Labs
package shell

import (
	"context"
	"sync"
	"testing"
	"time"
)

// BackgroundProcess has TWO locks with different jobs, and conflating them is
// the bug these tests pin:
//
//	m.mu       guards the SHAPE of the process map — which ids exist
//	p.outputMu guards a process's OWN mutable fields — Status, ExitCode,
//	           EndTime, Ports, cmd, and the output buffers
//
// Holding m.mu says nothing about a process already in the map, so reading
// Status under the map lock alone raced the completion path writing it. These
// tests are only meaningful under -race; without it a torn read of a string
// header usually goes unnoticed, which is exactly why this survived.

// A process exits (writing Status) while readers poll it. This is the reported
// race: handleProcessCompletion vs GetProcess, via KillProcess in a t.Cleanup.
func TestConcurrentReadsDuringProcessCompletion(t *testing.T) {
	m := newTestBGManager()

	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    "echo hello",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers: every accessor that touches a process's own fields.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = m.GetProcess(proc.ID)
				_ = m.GetAllProcesses()
				_ = m.GetProcessesBySession(proc.SessionID)
				_, _, _, _ = m.GetProcessStatus(proc.ID)
			}
		}()
	}

	// Let the process exit naturally under the readers, then kill it to force
	// the other write path too.
	time.Sleep(50 * time.Millisecond)
	_ = m.KillProcess(proc.ID)

	close(stop)
	wg.Wait()
}

// Concurrent kills of several processes while readers enumerate them. Covers
// KillAllRunning's status scan and the getters' port refresh together.
func TestConcurrentKillAllWithReaders(t *testing.T) {
	m := newTestBGManager()

	dir := t.TempDir()
	for range 3 {
		if _, err := m.StartProcess(context.Background(), StartProcessOptions{
			Command:    "sleep 5",
			WorkingDir: dir,
		}); err != nil {
			t.Fatalf("StartProcess: %v", err)
		}
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = m.GetAllProcesses()
				_ = m.GetProcessesByChat("")
				_ = m.GetProcessesByWorktree("")
			}
		}()
	}

	time.Sleep(20 * time.Millisecond)
	m.KillAllRunning()

	close(stop)
	wg.Wait()
}

// CleanupOldProcesses takes the map lock and then each process's lock. Run it
// against live readers and writers to prove that order does not deadlock —
// the failure mode a naive fix for the race would introduce.
func TestCleanupDoesNotDeadlockAgainstReaders(t *testing.T) {
	m := newTestBGManager()

	proc, err := m.StartProcess(context.Background(), StartProcessOptions{
		Command:    "echo hello",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	t.Cleanup(func() { _ = m.KillProcess(proc.ID) })

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = m.GetProcess(proc.ID)
				m.CleanupOldProcesses(time.Hour)
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: cleanup and readers did not finish")
	}
}
