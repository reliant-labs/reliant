// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A remove of a checkout holds the path's lock while it runs, so it waits
// behind an earlier delete of the same path instead of running beside it,
// and runs nothing when it is given up on first.
func TestGitRemove_WaitsForAnEarlierDeleteOfThePath(t *testing.T) {
	t.Parallel()
	clone := filepath.Join(t.TempDir(), "clone")
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := checkoutTeardowns.lock(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(gitRemoveRequest{Path: clone + "/"})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := handleGitRemove(ctx, payload); err == nil {
		t.Fatal("the remove ran beside an earlier delete of the same path")
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("a remove given up on while it waited deleted the checkout: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := handleGitRemove(context.Background(), payload)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the remove finished while an earlier delete held the path (err %v)", err)
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("the checkout survived its remove: %v", err)
	}
}

// git.await_teardown answers idle only once no delete of the path is running:
// asked while one still runs, it waits for it, and its wait ending first is a
// failure, never an idle answer. Other paths are not held up.
func TestGitAwaitTeardown_AnswersOnceNoDeleteOfThePathRuns(t *testing.T) {
	t.Parallel()
	clone := filepath.Join(t.TempDir(), "clone")
	payload, _ := json.Marshal(gitAwaitTeardownRequest{Path: clone})

	idle := func(ctx context.Context, payload []byte) bool {
		out, err := handleGitAwaitTeardown(ctx, payload)
		if err != nil {
			return false
		}
		var resp gitAwaitTeardownResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Idle
	}
	if !idle(context.Background(), payload) {
		t.Fatal("no delete is running, but the machine did not say so")
	}

	unlock, err := checkoutTeardowns.lock(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if idle(ctx, payload) {
		t.Fatal("answered idle while a delete of the path was still running")
	}
	other, _ := json.Marshal(gitAwaitTeardownRequest{Path: filepath.Join(t.TempDir(), "other")})
	if !idle(context.Background(), other) {
		t.Fatal("a delete of one path held up another")
	}

	answered := make(chan bool, 1)
	go func() { answered <- idle(context.Background(), payload) }()
	select {
	case <-answered:
		t.Fatal("answered while a delete of the path was still running")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	if !<-answered {
		t.Fatal("not idle once the delete finished")
	}
}
