// Copyright (c) 2025 Reliant Labs

package daemonliveness_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemonliveness"
)

// fakeRepo is an in-memory Repository with known truth for both the per-user
// and per-daemon lookups. It records nothing; the divergence test below
// asserts only on returned booleans, not on call counts.
type fakeRepo struct {
	// userLastSeen[userID] = freshest last_stream_activity across that user's
	// daemons; missing key means no attachment row exists.
	userLastSeen map[string]time.Time
	// daemonLastSeen[daemonID] = last_stream_activity for that daemon.
	daemonLastSeen map[string]time.Time
}

func (f *fakeRepo) GetUserLiveness(_ context.Context, userID string, staleThreshold time.Duration) (daemonliveness.Status, error) {
	ts, ok := f.userLastSeen[userID]
	if !ok {
		return daemonliveness.Status{Live: false}, nil
	}
	live := time.Since(ts) <= staleThreshold
	return daemonliveness.Status{Live: live, LastSeen: ts}, nil
}

func (f *fakeRepo) GetDaemonLiveness(_ context.Context, daemonID string, staleThreshold time.Duration) (daemonliveness.Status, error) {
	ts, ok := f.daemonLastSeen[daemonID]
	if !ok {
		return daemonliveness.Status{Live: false}, nil
	}
	live := time.Since(ts) <= staleThreshold
	return daemonliveness.Status{Live: live, LastSeen: ts}, nil
}

// oldIsDaemonOnline mirrors the pre-refactor IsDaemonOnline behavior — a
// direct DB read with no NATS pull-RPC. We assert the new daemonliveness
// answer matches this baseline across the test grid.
func oldIsDaemonOnline(ctx context.Context, repo *fakeRepo, userID string) (bool, error) {
	s, err := repo.GetUserLiveness(ctx, userID, daemonliveness.DefaultStaleThreshold)
	if err != nil {
		return false, err
	}
	return s.Live, nil
}

// oldIsDaemonOnlineByID mirrors the would-be per-daemon DB check.
func oldIsDaemonOnlineByID(ctx context.Context, repo *fakeRepo, daemonID string) (bool, error) {
	s, err := repo.GetDaemonLiveness(ctx, daemonID, daemonliveness.DefaultStaleThreshold)
	if err != nil {
		return false, err
	}
	return s.Live, nil
}

// TestDivergence_NewAndOldAgree is the safety net the simplification proposal
// calls out: every release-gating run, the new Reachable/ReachableByUser must
// return the same boolean as the pre-refactor direct-DB path on a fixed grid
// of truth scenarios. When this test starts failing, the new path has drifted
// from the old one and needs investigation BEFORE the old code is deleted.
//
// Cases cover the fresh/stale/missing × user/daemonID matrix (6 total).
// NATS is passed as nil — the proposal calls for the DB path being the
// canonical answer for the by-user variant and a clean fallback for the
// by-daemon variant. With nc=nil, both paths reduce to the DB read, which is
// exactly the baseline `oldIsDaemonOnline` evaluates against.
func TestDivergence_NewAndOldAgree(t *testing.T) {
	now := time.Now().UTC()
	fresh := now.Add(-5 * time.Second) // well within DefaultStaleThreshold (30s)
	stale := now.Add(-2 * time.Minute) // well outside DefaultStaleThreshold
	repo := &fakeRepo{
		userLastSeen: map[string]time.Time{
			"user-fresh": fresh,
			"user-stale": stale,
			// "user-missing" intentionally absent
		},
		daemonLastSeen: map[string]time.Time{
			"daemon-fresh": fresh,
			"daemon-stale": stale,
			// "daemon-missing" intentionally absent
		},
	}

	t.Run("by_user", func(t *testing.T) {
		cases := []struct {
			name   string
			userID string
			want   bool
		}{
			{"fresh", "user-fresh", true},
			{"stale", "user-stale", false},
			{"missing", "user-missing", false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				oldGot, err := oldIsDaemonOnline(ctx, repo, tc.userID)
				if err != nil {
					t.Fatalf("old path: %v", err)
				}
				newStatus, err := daemonliveness.ReachableByUser(ctx, nil, repo, tc.userID)
				if err != nil {
					t.Fatalf("new path: %v", err)
				}
				if oldGot != tc.want {
					t.Fatalf("baseline disagrees with truth: oldGot=%v want=%v (test grid is buggy)", oldGot, tc.want)
				}
				if newStatus.Live != oldGot {
					t.Errorf("DIVERGENCE: ReachableByUser(%q).Live = %v, old IsDaemonOnline = %v",
						tc.userID, newStatus.Live, oldGot)
				}
			})
		}
	})

	t.Run("by_daemon", func(t *testing.T) {
		cases := []struct {
			name     string
			daemonID string
			want     bool
		}{
			{"fresh", "daemon-fresh", true},
			{"stale", "daemon-stale", false},
			{"missing", "daemon-missing", false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				oldGot, err := oldIsDaemonOnlineByID(ctx, repo, tc.daemonID)
				if err != nil {
					t.Fatalf("old path: %v", err)
				}
				// With nc=nil, Reachable falls through to the DB path.
				newStatus, err := daemonliveness.Reachable(ctx, nil, repo, tc.daemonID)
				if err != nil {
					t.Fatalf("new path: %v", err)
				}
				if oldGot != tc.want {
					t.Fatalf("baseline disagrees with truth: oldGot=%v want=%v (test grid is buggy)", oldGot, tc.want)
				}
				if newStatus.Live != oldGot {
					t.Errorf("DIVERGENCE: Reachable(%q).Live = %v, old by-id check = %v",
						tc.daemonID, newStatus.Live, oldGot)
				}
			})
		}
	})
}

// TestReachableByUser_RejectsEmptyAndNil guards the obvious misuse cases — if
// either of these signatures changes we want a loud test failure rather than
// a silent "false, nil" answer to a caller bug.
func TestReachableByUser_RejectsEmptyAndNil(t *testing.T) {
	ctx := context.Background()
	if _, err := daemonliveness.ReachableByUser(ctx, nil, &fakeRepo{}, ""); err == nil {
		t.Error("expected error for empty userID")
	}
	if _, err := daemonliveness.ReachableByUser(ctx, nil, nil, "u-1"); err == nil {
		t.Error("expected error for nil repo")
	}
}
