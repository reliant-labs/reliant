// Copyright (c) 2025 Reliant Labs

//go:build prodreplay

package replaytest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"github.com/reliant-labs/reliant/internal/observability"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/temporal/claimcheck"
	"github.com/reliant-labs/reliant/internal/workersetup"
	v2workflow "github.com/reliant-labs/reliant/internal/workflow"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// TestReplayRecordedHistory replays real histories — downloaded from a
// deployed Temporal — through the workflow code of THIS checkout. It answers
// the question every wedge investigation and every release starts with: will
// these in-flight runs replay on this build, or fail their next workflow task
// with TMPRL1100?
//
// The checked-in fixtures (TestReplayFixtures) pin the shapes we record in a
// harness; this pins the runs that actually exist. It is not part of the suite
// (build tag prodreplay) because its inputs are histories and a database the
// suite does not have, and both carry user data that is never committed:
//
//	temporal workflow show --workflow-id <id> --output json > /tmp/h/<id>.json
//	REPLAY_HISTORY=/tmp/h \
//	REPLAY_PAYLOAD_DSN='postgres://…/reliant?sslmode=disable' \
//	  go test -tags prodreplay -run TestReplayRecordedHistory -v -count=1 \
//	  ./internal/workflow/runtime/replaytest/
//
// REPLAY_HISTORY is one history file or a directory of them (each *.json is a
// subtest). REPLAY_PAYLOAD_DSN is the reliant database holding the runs'
// claim-checked payloads (temporal_payload_blobs); it is only read, so point
// it at a read-only connection (`options=-c default_transaction_read_only=on`
// in the DSN). Without it, a history containing a claim-check reference fails
// with claimcheck.ErrNoStore — a harness problem, not a replay break.
// REPLAY_VERBOSE=1 shows the workflow's own logging during the replay.
//
// A pass means the recorded history replays; it does not run the next task.
// What that task does is decided by the code paths after the replay point,
// which the unit tests cover.
func TestReplayRecordedHistory(t *testing.T) {
	target := os.Getenv("REPLAY_HISTORY")
	if target == "" {
		t.Skip("set REPLAY_HISTORY to a `temporal workflow show --output json` file or a directory of them")
	}
	quietProcessLogging()

	histories := []string{target}
	if info, err := os.Stat(target); err != nil {
		t.Fatalf("REPLAY_HISTORY: %v", err)
	} else if info.IsDir() {
		histories, err = filepath.Glob(filepath.Join(target, "*.json"))
		if err != nil || len(histories) == 0 {
			t.Fatalf("no *.json histories under %s (glob error: %v)", target, err)
		}
	}

	var converterOpts []rtemporal.DataConverterOption
	if dsn := os.Getenv("REPLAY_PAYLOAD_DSN"); dsn != "" {
		sqlDB, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatalf("open payload store: %v", err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		converterOpts = append(converterOpts, rtemporal.WithPayloadStore(readOnlyStore{claimcheck.NewPostgresStore(sqlDB)}))
	}

	for _, history := range histories {
		t.Run(strings.TrimSuffix(filepath.Base(history), ".json"), func(t *testing.T) {
			// Same registration as newProductionMirroredReplayer; only the
			// data converter differs, because a real history carries
			// claim-check references.
			replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{
				DataConverter:            rtemporal.NewFlexibleDataConverter(converterOpts...),
				Interceptors:             []interceptor.WorkerInterceptor{observability.NewOTelWorkerInterceptor()},
				DisableDeadlockDetection: true,
				EnableLoggingInReplay:    os.Getenv("REPLAY_VERBOSE") == "1",
			})
			if err != nil {
				t.Fatalf("create workflow replayer: %v", err)
			}
			replayer.RegisterWorkflowWithOptions(v2.DynamicWorkflow, sdkworkflow.RegisterOptions{
				Name: v2workflow.WorkflowDynamic,
			})
			replayer.RegisterWorkflowWithOptions(workersetup.GenerateTitleWorkflow, sdkworkflow.RegisterOptions{
				Name: "GenerateTitleWorkflow",
			})

			if err := replayer.ReplayWorkflowHistoryFromJSONFile(replayLogger(), history); err != nil {
				t.Fatalf("replay of %s FAILED on this build — a run with this history wedges on its next workflow task: %v", history, err)
			}
		})
	}
}

// readOnlyStore serves recorded blobs and discards writes. Replay re-encodes
// every activity input the workflow re-issues, and the claim-check codec PUTs
// each large one; against a read-only connection that write fails and the SDK
// panics inside ExecuteActivity ("yield during panic unwinding"), which reads
// exactly like a replay break and is not one. The key is content-derived, so
// dropping the write loses nothing.
type readOnlyStore struct{ claimcheck.Store }

func (readOnlyStore) Put(context.Context, string, []byte, int) error { return nil }
