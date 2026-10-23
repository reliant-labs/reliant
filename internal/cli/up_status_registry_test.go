package cli

import (
	"path/filepath"
	"testing"
)

func TestStatusRecognizesExactWorktreeOwnershipWithoutSessionHeartbeat(t *testing.T) {
	root := t.TempDir()
	ours, sibling := filepath.Join(root, "ours"), filepath.Join(root, "sibling")
	f := fakeFacts{env: map[int][]string{
		100: markerProj(projectIDForDir(ours), "dev", "api"),
		101: markerProj(projectIDForDir(sibling), "dev", "api"),
		102: markerProj(projectIDForDir(ours), "other", "api"),
	}}
	var out envStatusRecords
	reconcileLocalPresence(&out, ours, "dev", []int{100, 101, 102}, f)
	if !out.SessionsRead || len(out.Sessions) != 1 || out.Sessions[0].State != sessionStateRunning {
		t.Fatalf("live owned stack invisible: %+v", out)
	}
	var unrelated envStatusRecords
	reconcileLocalPresence(&unrelated, ours, "dev", []int{101, 102}, f)
	if len(unrelated.Sessions) != 0 {
		t.Fatalf("adopted sibling/env: %+v", unrelated)
	}
}
