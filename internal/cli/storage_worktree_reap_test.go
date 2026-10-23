package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/reliant-labs/forge/internal/storage"
)

// TestOnlyAnExplicitStorageGCMayReapWorktrees: worktree and anonymous-volume
// removal is decided by a person or agent running `forge storage gc --apply`,
// never by a pass that runs on its own. The installed daily job runs the same
// command, so it is marked --scheduled; `storage daemon` is periodic by
// definition. (The hourly job and `forge env up`'s background pass run
// NonDisruptiveGC, which has no worktree layer and only observes volumes.)
func TestOnlyAnExplicitStorageGCMayReapWorktrees(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := storage.Save(path, storage.DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	orig := fullGCFn
	t.Cleanup(func() { fullGCFn = orig })
	var reap []bool
	fullGCFn = func(_ context.Context, r storage.Runner, _ bool) error {
		reap = append(reap, r.Explicit)
		return nil
	}
	run := func(args ...string) {
		t.Helper()
		cmd := newStorageCmd()
		cmd.SetArgs(append([]string{"--policy", path}, args...))
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		ctx := context.Background()
		if args[0] == "daemon" {
			// The daemon runs its first pass at once, then waits on ctx.
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			cancel()
		}
		if err := cmd.ExecuteContext(ctx); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	run("gc", "--apply")
	run("gc", "--apply", "--scheduled")
	run("daemon")
	if want := []bool{true, false, false}; !slices.Equal(reap, want) {
		t.Fatalf("Explicit for gc / scheduled gc / daemon = %v, want %v", reap, want)
	}
}

// The daily job the schedule installs is a --scheduled pass.
func TestDailyScheduleIsMarkedScheduled(t *testing.T) {
	args := dailyStorageArgs("/x/forge-storage", nil, "/p/storage.json")
	if !slices.Contains(args, "--scheduled") || !slices.Contains(args, "--apply") {
		t.Fatalf("daily job argv %v must apply and be marked --scheduled", args)
	}
}
