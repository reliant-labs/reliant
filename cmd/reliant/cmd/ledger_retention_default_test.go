// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/pkg/config"
)

// TestLedgerRetentionDefaultReachesTheWorker pins the deployed ledger
// retention end to end: a WorkerConfig that leaves the retention settings
// unset must project RELIANT_WORKER_CHAT_UPDATE_RETENTION and
// RELIANT_WORKER_STEP_EXECUTION_RETENTION as "0s" (deletion off) and the start
// delay as "1h" into the worker's env through config_gen.workloadEnvMap, and
// explicit settings must reach it as written; the worker config loaded from
// that env must parse to those durations. The retention loop's own tests take
// durations directly, so without this a dropped env-map entry, or a default
// that enabled deletion, would ship unnoticed.
func TestLedgerRetentionDefaultReachesTheWorker(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))

	const fixture = "deploy/kcl/testdata/ledger_retention_default.k"
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}
	names := []string{
		"RELIANT_WORKER_CHAT_UPDATE_RETENTION",
		"RELIANT_WORKER_STEP_EXECUTION_RETENTION",
		"RELIANT_WORKER_LEDGER_RETENTION_START_DELAY",
	}
	for _, tc := range []struct {
		selector                string
		want                    map[string]string
		chats, steps, startWait time.Duration
	}{
		{"default_worker", map[string]string{names[0]: "0s", names[1]: "0s", names[2]: "1h"}, 0, 0, time.Hour},
		{"enabled_worker", map[string]string{names[0]: "720h", names[1]: "2160h", names[2]: "0s"}, 720 * time.Hour, 2160 * time.Hour, 0},
	} {
		t.Run(tc.selector, func(t *testing.T) {
			var env map[string]any
			require.NoError(t, kcleval.Select(context.Background(), fixture, tc.selector, &env, project))
			for name, want := range tc.want {
				got, ok := env[name]
				require.True(t, ok, "the projected env has no %s: %v", name, env)
				require.Equal(t, want, got, name)
			}

			// Load the worker config from the variables as projected.
			for _, name := range names {
				t.Setenv(name, "")
				require.NoError(t, os.Unsetenv(name))
				v, _ := env[name].(string)
				t.Setenv(name, v)
			}
			worker, err := config.LoadWorkerConfig(findServerCmd(t, "worker"))
			require.NoError(t, err)
			require.Equal(t, tc.chats, worker.GetChatUpdateRetention().AsDuration())
			require.Equal(t, tc.steps, worker.GetStepExecutionRetention().AsDuration())
			require.Equal(t, tc.startWait, worker.GetLedgerRetentionStartDelay().AsDuration())
		})
	}
}
