// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"

	"github.com/reliant-labs/reliant/internal/temporal/stopnudge"
)

type discardPublisher struct{}

func (discardPublisher) Publish(string, []byte) error { return nil }

// The process's client is decorated (stopnudge.Wrap, internal/app
// NewTemporalClient), and worker.New panics on anything but the SDK's own
// client: v1.7.85's worker crash-looped at boot with "Client must be created
// with client.Dial() or client.NewLazyClient()". NewWorker builds on the
// client under the decorator. A lazy client never dials, so this runs fast.
func TestNewWorker_AcceptsTheDecoratedProductionClient(t *testing.T) {
	lazy, err := client.NewLazyClient(client.Options{HostPort: "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lazy.Close)
	wrapped := stopnudge.Wrap(lazy, discardPublisher{}, "pause")
	if wrapped == lazy {
		t.Fatal("stopnudge.Wrap returned the client undecorated; this test no longer covers production's wrapper")
	}
	if got := sdkClient(wrapped); got != lazy {
		t.Fatalf("sdkClient = %T, want the SDK client under the decorator", got)
	}
	if _, _, err := NewWorker(&Config{TemporalClient: wrapped, TaskQueueSuffix: "decorated-client"}); err != nil {
		t.Fatalf("NewWorker with the decorated client: %v", err)
	}
}

// The real worker, built with the decorated client against a real Temporal,
// starts polling: what /ready means in prod.
func TestNewWorker_DecoratedClientPollsARealServer(t *testing.T) {
	testkit.Slow(t, "boots a Temporal dev server")
	opts := testsuite.DevServerOptions{LogLevel: "never", ClientOptions: &client.Options{Namespace: "reliant-workersetup-wrapped"}}
	if path, err := exec.LookPath("temporal"); err == nil {
		opts.ExistingPath = path
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	srv, err := testsuite.StartDevServer(ctx, opts)
	if err != nil {
		t.Fatalf("start Temporal dev server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	w, _, err := NewWorker(&Config{TemporalClient: stopnudge.Wrap(srv.Client(), discardPublisher{}, "pause"), TaskQueueSuffix: "decorated-client"})
	if err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- w.Run(context.Background()) }()
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = w.Drain(stop)
		<-runErr
	})
	queue := TaskQueueName("decorated-client")
	for _, kind := range []enums.TaskQueueType{enums.TASK_QUEUE_TYPE_WORKFLOW, enums.TASK_QUEUE_TYPE_ACTIVITY} {
		for {
			resp, err := srv.Client().DescribeTaskQueue(ctx, queue, kind)
			if err == nil && len(resp.GetPollers()) > 0 {
				break
			}
			select {
			case err := <-runErr:
				t.Fatalf("worker stopped before polling: %v", err)
			case <-ctx.Done():
				t.Fatalf("no %s pollers on %s: %v", kind, queue, err)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}
