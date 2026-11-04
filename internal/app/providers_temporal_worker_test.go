// Copyright (c) 2025 Reliant Labs

package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/temporal"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The worker root builds its Temporal worker on the process's client exactly
// as `reliant server worker` does: NewTemporalClient's client, with every
// decorator it applies, handed to NewTemporalWorker. worker.New panics on any
// client the SDK did not create ("Client must be created with client.Dial()
// or client.NewLazyClient()"); v1.8.14-rc.3 crash-looped at boot that way
// once NewTemporalClient began wrapping its client in stopnudge (#1927).
// Only the dial is swapped, for a lazy client, so no server is needed: a
// decorator added to NewTemporalClient later, without an Unwrap the worker
// can see through, fails here instead of in prod.
func TestWorkerBuildsOnTheProductionTemporalClient(t *testing.T) {
	var dialed client.Client
	lazyDial := func(_ context.Context, cfg temporal.ExternalClientConfig) (client.Client, error) {
		c, err := client.NewLazyClient(client.Options{
			HostPort:  fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
			Namespace: cfg.Namespace,
		})
		dialed = c
		return c, err
	}
	// A NATS connection is what makes NewTemporalClient decorate its client.
	natsConn := &NATSConn{}
	tc, err := newTemporalClient(TemporalClientDeps{
		Ctx:      context.Background(),
		Temporal: &configv1.TemporalConfig{Host: "127.0.0.1", Port: 1, Namespace: "reliant"},
		NATS:     natsConn,
	}, lazyDial)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tc.Close(context.Background()) })
	require.NotEqual(t, dialed, tc.Client,
		"NewTemporalClient no longer decorates its client; this test no longer covers production's wrapper")

	w, err := NewTemporalWorker(TemporalWorkerDeps{
		Temporal:    tc,
		NATS:        natsConn,
		Streaming:   &StreamingHub{},
		Tools:       &WorkerToolsFactory{},
		Executor:    &toolexec.RemoteExecutor{},
		Credentials: &WorkerIntegrationCredentials{},
		Recorder:    &AccountRecorder{},
		Worker:      &configv1.WorkerConfig{},
	})
	require.NoError(t, err)
	require.NotNil(t, w)
}
