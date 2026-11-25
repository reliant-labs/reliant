// Copyright (c) 2026 Reliant Labs
//go:build linux

package proctree

import (
	"os"
	"slices"
	"syscall"
	"testing"
)

// credsandbox hands every init its liveness pipe as an *os.File named
// "reliant-cred-sandbox-liveness", which is no path. A pipe names nothing on
// disk, so it must not leave the init unaccounted: an unaccounted init is
// undumpable and holds every worktree reclaim on the machine while it runs.
func TestAccountedFootprint_AnonymousPipeUnderAnyName(t *testing.T) {
	dir := t.TempDir()
	var fds [2]int
	if err := syscall.Pipe(fds[:]); err != nil {
		t.Fatal(err)
	}
	liveness := os.NewFile(uintptr(fds[0]), "reliant-cred-sandbox-liveness")
	t.Cleanup(func() { _ = liveness.Close(); _ = syscall.Close(fds[1]) })

	cmd := sandboxInitCmd(t)
	cmd.Dir = dir
	cmd.ExtraFiles = []*os.File{liveness}
	got, ok := accountedFootprint(cmd)
	if !ok {
		t.Fatal("an init handed an anonymous pipe must be accounted")
	}
	if want := []string{resolved(t, dir), resolved(t, cmd.Path)}; !slices.Equal(got, want) {
		t.Errorf("footprint %q, want %q", got, want)
	}
	if fp := (&Limiter{}).Prepare(cmd, Owner{}).footprint; fp == nil {
		t.Error("Prepare left the init unaccounted")
	}

	// A regular file known only by a relative name is still unaccountable.
	rel, err := os.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rel.Close() })
	cmd.ExtraFiles = append(cmd.ExtraFiles, rel)
	if _, ok := accountedFootprint(cmd); ok {
		t.Error("a directory known only by a relative name cannot be accounted for")
	}
}
