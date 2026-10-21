// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// Repo.MutateChatSelection's guarantee — concurrent composer edits of one chat
// never lose each other — comes from its advisory lock, so its transaction is
// READ COMMITTED (internal/db/SCHEMA.md rule 7). The race: another writer
// holds the chat's lock with its edit written; the mutation queues behind it;
// the writer commits. At READ COMMITTED the mutation's read after the lock is
// the first statement able to see the edit, so mutate runs once, on it. At
// SERIALIZABLE the snapshot is taken at the lock statement, before the wait,
// so the read fails with 40001 and the edit lands only on a retry — which the
// probe sees even though RunTx hides it from the caller.
func TestMutateChatSelectionLoserReadsTheWinnersWrite(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	if err := store.InsertChat(ctx, env.ORM, convChat("sel-race", time.Now().UTC())); err != nil {
		t.Fatalf("InsertChat: %v", err)
	}
	probe := env.Probe(t)

	winner := env.HoldLocks(t,
		`SELECT pg_advisory_xact_lock(hashtextextended('chat-selection:sel-race', 0))`,
		`INSERT INTO chat_selections (chat_id, user_id, workflow_name, params, version, updated_at)
		 VALUES ('sel-race', '`+storetest.SeedUserID+`', 'builder', '{"winner":true}', 1, NOW())`)

	var seen []*chatselection.Selection
	done := make(chan error, 1)
	go func() {
		_, err := probe.Repo.MutateChatSelection(ctx, "sel-race", func(cur *chatselection.Selection) (*chatselection.Selection, error) {
			seen = append(seen, cur)
			if cur == nil {
				return &chatselection.Selection{UserID: storetest.SeedUserID, WorkflowName: "builder", Params: map[string]any{"loser": true}}, nil
			}
			next := *cur
			next.Params = map[string]any{"loser": true}
			for k, v := range cur.Params {
				next.Params[k] = v
			}
			return &next, nil
		})
		done <- err
	}()
	winner.WaitForWaiter()
	winner.Commit()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("MutateChatSelection: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("MutateChatSelection did not return within 30s of the winner committing")
	}

	if states := probe.SQLStates(); slices.Contains(states, "40001") {
		t.Fatalf("the mutation lost the lock race with a serialization failure (SQLSTATE %v) and was retried; its transactions began at %v, want [read committed]", states, probe.Isolations())
	}
	if got := probe.Isolations(); !slices.Equal(got, []string{"read committed"}) {
		t.Fatalf("transactions began at %v, want one at [read committed]", got)
	}
	if len(seen) != 1 || seen[0] == nil || seen[0].Params["winner"] != true {
		t.Fatalf("mutate saw %+v; want one call, seeing the winner's edit", seen)
	}
	got, err := store.GetChatSelection(ctx, env.ORM, "sel-race", false)
	if err != nil {
		t.Fatalf("GetChatSelection: %v", err)
	}
	if got.Version != 2 || got.Params["winner"] != true || got.Params["loser"] != true {
		t.Fatalf("stored selection = %+v; want version 2 holding both edits", got)
	}
}

// N composer edits of one chat at once, through the service the handlers
// call. Each Merge sets its own key, so serial execution ends with every key
// stored at version N; a lost update drops a key. No writer may hit a
// serialization failure: the lock queues them, it does not abort them.
func TestChatSelectionConcurrentWritersLoseNothing(t *testing.T) {
	const writers = 16
	env := storetest.New(t)
	ctx := context.Background()
	at := time.Now().UTC()
	for _, id := range []string{"sel-many", "sel-cas"} {
		if err := store.InsertChat(ctx, env.ORM, convChat(id, at)); err != nil {
			t.Fatalf("InsertChat(%s): %v", id, err)
		}
	}
	probe := env.Probe(t)
	svc := chatselection.New(probe.Repo)

	// run starts every writer at once and returns each one's error.
	run := func(write func(i int) error) []error {
		errs := make([]error, writers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = write(i)
			}()
		}
		close(start)
		wg.Wait()
		return errs
	}
	requireNoRetries := func(t *testing.T) {
		t.Helper()
		if states := probe.SQLStates(); slices.Contains(states, "40001") {
			t.Fatalf("writers hit serialization failures (SQLSTATE %v); began %d transactions at %v", states, len(probe.Isolations()), probe.Isolations())
		}
		levels := probe.Isolations()
		if len(levels) != writers {
			t.Fatalf("began %d transactions for %d writers (retries?): %v", len(levels), writers, levels)
		}
		for _, l := range levels {
			if l != "read committed" {
				t.Fatalf("transactions began at %v, want every one at read committed", levels)
			}
		}
	}

	t.Run("unconditional merges", func(t *testing.T) {
		probe.Reset()
		errs := run(func(i int) error {
			_, err := svc.Merge(ctx, "sel-many", storetest.SeedUserID, "builder", map[string]any{fmt.Sprintf("k%02d", i): float64(i)}, 0)
			return err
		})
		if err := errors.Join(errs...); err != nil {
			t.Fatalf("Merge: %v", err)
		}
		requireNoRetries(t)
		got, err := svc.Get(ctx, "sel-many")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Version != writers || len(got.Params) != writers {
			t.Fatalf("stored version %d with %d params %v; want version %d holding all %d keys", got.Version, len(got.Params), got.Params, writers, writers)
		}
		for i := range writers {
			if k := fmt.Sprintf("k%02d", i); got.Params[k] != float64(i) {
				t.Fatalf("lost update: %s = %v in %v", k, got.Params[k], got.Params)
			}
		}
	})

	// Every writer read version 1 and names it: exactly one wins, and every
	// other one is told the selection changed since it read it.
	t.Run("versioned merges", func(t *testing.T) {
		if _, err := svc.Merge(ctx, "sel-cas", storetest.SeedUserID, "builder", map[string]any{"seed": true}, 0); err != nil {
			t.Fatalf("seed: %v", err)
		}
		probe.Reset()
		errs := run(func(i int) error {
			_, err := svc.Merge(ctx, "sel-cas", storetest.SeedUserID, "builder", map[string]any{fmt.Sprintf("k%02d", i): true}, 1)
			return err
		})
		var won, conflicted int
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, chatselection.ErrVersionConflict):
				conflicted++
			default:
				t.Fatalf("Merge: %v", err)
			}
		}
		if won != 1 || conflicted != writers-1 {
			t.Fatalf("%d won and %d conflicted; want 1 and %d", won, conflicted, writers-1)
		}
		requireNoRetries(t)
		got, err := svc.Get(ctx, "sel-cas")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Version != 2 || len(got.Params) != 2 || got.Params["seed"] != true {
			t.Fatalf("stored %+v; want version 2 with the seed and the one winner's key", got)
		}
	})
}
