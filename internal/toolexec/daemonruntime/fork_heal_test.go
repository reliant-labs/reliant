// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// The heal recounts the machine, then stops the largest background command,
// giving it the reason its output will show.
func TestForkHealer_HealStopsTheLargestOnAFreshCount(t *testing.T) {
	var recounted bool
	var gotWhy string
	h := &forkHealer{
		recount: func() bool { recounted = true; return true },
		tasks:   func(int) uint64 { return 7 },
		stopLargest: func(tasks func(int) uint64, why string) string {
			if !recounted {
				t.Error("sized commands before recounting the machine")
			}
			if tasks(1) != 7 {
				t.Error("the heal did not pass the group sizes through")
			}
			gotWhy = why
			return "`sleep 9` (7 tasks)"
		},
	}
	if got := h.heal(context.Background()); got != "`sleep 9` (7 tasks)" {
		t.Fatalf("heal() = %q", got)
	}
	if !strings.Contains(gotWhy, "could not start any new process") || !strings.Contains(gotWhy, "largest background command") {
		t.Errorf("the stopped command's reason does not explain itself: %s", gotWhy)
	}
}

// A managed daemon gives up by exiting, once: it cancels in-flight work,
// stops the runtime after the flush delay, force-exits after the backstop,
// leaves its reason for kubelet, and Start reports why.
func TestForkHealer_ManagedGiveUpStopsTheDaemonOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		termLog := filepath.Join(t.TempDir(), "termination-log")
		if err := os.WriteFile(termLog, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		var cancelled, stopped, exited atomic.Int32
		h := &forkHealer{
			managed:        true,
			exit:           func(int) { exited.Add(1) },
			flushDelay:     2 * time.Second,
			exitBackstop:   20 * time.Second,
			terminationLog: termLog,
		}
		h.arm(func() { cancelled.Add(1) }, func() { stopped.Add(1) })

		if h.gaveUp() != nil {
			t.Fatal("gaveUp before giving up")
		}
		h.giveUp("This machine cannot start new processes: its process table is full.")
		h.giveUp("again")
		if cancelled.Load() != 1 {
			t.Fatalf("in-flight work cancelled %d times, want 1", cancelled.Load())
		}
		var full *ProcessTableFullError
		if err := h.gaveUp(); !errors.As(err, &full) || !strings.Contains(full.Reason, "process table is full") {
			t.Fatalf("gaveUp() = %v, want a ProcessTableFullError with the first reason", err)
		}

		time.Sleep(2 * time.Second)
		synctest.Wait()
		if stopped.Load() != 1 || exited.Load() != 0 {
			t.Fatalf("after the flush delay: stopped=%d exited=%d, want 1 and 0", stopped.Load(), exited.Load())
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()
		if exited.Load() != 1 {
			t.Fatalf("exited %d times after the backstop, want 1", exited.Load())
		}
		b, err := os.ReadFile(termLog)
		if err != nil || !strings.Contains(string(b), "process table full") {
			t.Fatalf("termination log = %q (%v), want the reason", b, err)
		}
	})
}

// A person's own daemon never exits for this: there is no supervisor whose
// restart would free the table.
func TestForkHealer_DesktopNeverExits(t *testing.T) {
	var stopped, exited atomic.Int32
	h := &forkHealer{exit: func(int) { exited.Add(1) }}
	h.arm(func() {}, func() { stopped.Add(1) })
	h.giveUp("full")
	if h.gaveUp() != nil || stopped.Load() != 0 || exited.Load() != 0 {
		t.Fatal("a desktop daemon gave up")
	}
	var nilHealer *forkHealer
	nilHealer.arm(nil, nil)
	nilHealer.giveUp("x")
	if nilHealer.heal(context.Background()) != "" || nilHealer.gaveUp() != nil {
		t.Fatal("a nil healer did something")
	}
}
