// Copyright (c) 2026 Reliant Labs
//go:build linux || darwin

package osutil

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A zombie runs nothing: GroupMembers must not list it and GroupOwned must not
// accept it as proof of the group. This test process is the zombie's parent and
// does not reap it until the end.
func TestGroupMembersAndOwnershipExcludeZombies(t *testing.T) {
	if _, ok := GroupMembers(1); !ok {
		t.Skip("needs a readable process table")
	}
	cmd := exec.Command("sleep", "0.2")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Wait() }()
	pgid := cmd.Process.Pid
	members, _ := GroupMembers(pgid)
	if len(members) != 1 {
		t.Fatalf("members = %v, want the running leader", members)
	}

	deadline := time.Now().Add(conditionTimeout)
	for !isZombie(pgid) {
		if time.Now().After(deadline) {
			t.Fatal("the child never became a zombie")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, _ := GroupMembers(pgid); len(got) != 0 {
		t.Fatalf("members = %v, want a zombie left out", got)
	}
	if GroupOwned(pgid, members) {
		t.Fatal("a zombie proved the group")
	}
}

func TestGroupMembersOfNoGroup(t *testing.T) {
	if members, ok := GroupMembers(0); !ok || len(members) != 0 {
		t.Fatalf("GroupMembers(0) = %v, %v; want empty, true", members, ok)
	}
	if GroupOwned(0, nil) {
		t.Fatal("no recorded members proved a group")
	}
}
