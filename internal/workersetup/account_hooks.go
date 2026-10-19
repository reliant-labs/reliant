// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

// accountDriverFor builds a driver that talks as one account. nil leaves
// account routing off (handlers.AccountRouting.active).
var accountDriverFor handlers.DriverForAccountFunc

// accountRecorder is the recorder call_llm installs on a request and benches a
// refused account through.
func accountRecorder(repo db.Repository) handlers.AccountLimitRecorder {
	return benchOnlyRecorder{repo: repo}
}

// benchOnlyRecorder takes a refused account out of rotation. It records no
// usage snapshots (For installs no observer): the account-limits recorder does
// that and replaces this one. Benching is not optional, though: without it a
// refused account stays eligible, the router would hand the thread straight
// back to it, and failover would never trigger.
type benchOnlyRecorder struct{ repo db.Repository }

// probeRung is how long an account is benched when its refusal named no reset:
// the first rung of the run's probe ladder, so the router tries it again soon.
const probeRung = 5 * time.Minute

func (benchOnlyRecorder) For(string, string) ratelimit.Observer { return nil }

func (r benchOnlyRecorder) Bench(ctx context.Context, userID, accountID string, until time.Time, window string) (time.Time, error) {
	probe := until.IsZero()
	if probe {
		until = time.Now().Add(probeRung)
	}
	return until, r.repo.BenchAccount(ctx, userID, accountID, until, window, probe)
}
