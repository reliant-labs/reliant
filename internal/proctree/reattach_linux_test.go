// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"fmt"
	"os"
	"testing"
)

// A self-update's exec keeps the pid, so the new image names its command
// cgroups cmd-<pid>-<n> again from n=1. Reattaching a cgroup the previous
// image made moves the sequence past it, so no new command's cgroup is named
// after one still in use; a cgroup not named for this process is refused.
func TestReattachMovesTheCgroupSequencePastTheReattachedOne(t *testing.T) {
	before := cgroupSeq.Load()
	ours := fmt.Sprintf("/sys/fs/cgroup/test-reattach/commands/%s%d-%d", cmdPrefix, os.Getpid(), before+40)
	tree := (*Limiter)(nil).Reattach(1, ours, Owner{})
	if got := cgroupSeq.Load(); got < before+40 {
		t.Fatalf("cgroupSeq = %d after reattaching sequence %d", got, before+40)
	}
	if tree.Cgroup() != "" {
		t.Fatalf("an absent cgroup is not taken back, got %q", tree.Cgroup())
	}

	afterOurs := cgroupSeq.Load()
	other := fmt.Sprintf("/sys/fs/cgroup/test-reattach/commands/%s%d-%d", cmdPrefix, os.Getpid()+1, afterOurs+1000)
	(*Limiter)(nil).Reattach(1, other, Owner{})
	if got := cgroupSeq.Load(); got != afterOurs {
		t.Fatalf("another process's cgroup moved the sequence to %d", got)
	}
}
