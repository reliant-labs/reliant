// Copyright (c) 2025 Reliant Labs
package serverworker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/triggers/workflowevent"
	"github.com/reliant-labs/reliant/internal/workersetup"
)

// Supervise stops the nodes in reverse: the Temporal worker stops taking
// work first (its Drain is what releases in-flight activities), the loops
// after it, and the account recorder its activities record into last.
func TestWorkerNodesStopTakingWorkFirst(t *testing.T) {
	nodes := workerNodes(workerComponents{
		accountRecorder: &accountlimits.Recorder{},
		usagePoller:     &accountlimits.Poller{},
		relay:           &workflowevent.Relay{},
		temporalWorker:  &workersetup.Worker{},
	})
	stop := make([]string, 0, len(nodes))
	for i := len(nodes) - 1; i >= 0; i-- {
		stop = append(stop, nodes[i].Name)
	}
	require.Equal(t, []string{
		"temporal-worker",
		"workflow-event-relay",
		"account-usage-poller",
		"write-ledger-prune",
		"account-recorder",
	}, stop)

	worker := nodes[len(nodes)-1]
	require.NotNil(t, worker.Run, "the Temporal worker polls")
	require.NotNil(t, worker.Drain, "the Temporal worker drains: that is what releases activities")
	require.NotNil(t, worker.Ready, "the Temporal worker gates readiness")
	for _, loop := range nodes[1:3] {
		require.NotNil(t, loop.Run, "%s runs", loop.Name)
	}
	require.NotNil(t, nodes[0].Close, "the recorder flushes on close")
}
