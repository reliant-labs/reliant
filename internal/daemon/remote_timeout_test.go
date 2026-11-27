// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"testing"
)

// The stat in front of every write and edit decides whether the edit happens
// at all, so it must not give up sooner than the write it guards. At 5s it
// failed edits and writes on a busy machine that answered a moment later
// (prod obs d76eb00b, 948d47c3, 43ae8b5c), while the 30s write on the same
// machine would have completed. This drives the real RemoteClient methods
// and reads the budget each one sends.
func TestRemoteMetadataOpsGetTheWriteBudget(t *testing.T) {
	budgets := map[string]int32{}
	sender := senderFunc(func(_ context.Context, _, commandType string, _ []byte, timeoutMs int32) ([]byte, error) {
		budgets[commandType] = timeoutMs
		if commandType == "fs.list_dir" {
			return []byte(`{"path":"/w","entries":[]}`), nil
		}
		return []byte(`{}`), nil
	})
	c := NewRemoteClient(sender, "user-1")
	ctx := context.Background()

	if _, err := c.WriteFile(ctx, "/w/a.go", "x"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := c.StatFile(ctx, "/w/a.go"); err != nil {
		t.Fatalf("StatFile: %v", err)
	}
	if _, err := c.ListDirectory(ctx, "/w"); err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}
	if err := c.CreateDirectory(ctx, "/w/d"); err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}
	if err := c.DeletePath(ctx, "/w/d"); err != nil {
		t.Fatalf("DeletePath: %v", err)
	}

	write := budgets["fs.write_file"]
	if write <= 0 {
		t.Fatalf("fs.write_file sent no budget: %d", write)
	}
	for _, cmd := range []string{"fs.stat", "fs.list_dir", "fs.mkdir", "fs.delete"} {
		if got := budgets[cmd]; got < write {
			t.Errorf("%s budget = %dms, below the %dms write it guards", cmd, got, write)
		}
	}
}
