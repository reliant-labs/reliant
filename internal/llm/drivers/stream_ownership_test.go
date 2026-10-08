// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockingSendClient streams the way the real drivers do: every event is an
// unconditional channel send, so a consumer that stops reading wedges it.
type blockingSendClient struct {
	events   int
	finished chan struct{}
}

func (c *blockingSendClient) Name() string                      { return "blocking-send" }
func (c *blockingSendClient) ValidateKey(context.Context) error { return nil }
func (c *blockingSendClient) SendMessages(context.Context, []string, []message.Message, []tools.Tool) (*llm.DriverResponse, error) {
	return &llm.DriverResponse{}, nil
}

func (c *blockingSendClient) StreamResponse(ctx context.Context, _ []string, _ []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	ch := make(chan llm.DriverEvent)
	go func() {
		defer close(c.finished)
		defer close(ch)
		for i := 0; i < c.events; i++ {
			ch <- llm.DriverEvent{Type: llm.EventContentDelta, Content: "x"}
		}
		// What every driver does when its request context dies mid-stream.
		ch <- llm.DriverEvent{Type: llm.EventError, Error: ctx.Err()}
	}()
	return ch
}

func shortGrace(t *testing.T) {
	t.Helper()
	prev := consumerGoneGrace
	consumerGoneGrace = 50 * time.Millisecond
	t.Cleanup(func() { consumerGoneGrace = prev })
}

// TestBaseDriverStream_ConsumerThatWalksAwayDoesNotWedgeTheDriver: CallLLM
// stops reading on cancellation, on the progress timeout and on a handler
// error. Before, the driver goroutine then blocked forever on its next send,
// holding its HTTP stream and everything accumulated from it.
func TestBaseDriverStream_ConsumerThatWalksAwayDoesNotWedgeTheDriver(t *testing.T) {
	shortGrace(t)
	client := &blockingSendClient{events: 50, finished: make(chan struct{})}
	d := &baseDriver{client: client, model: models.Model{ID: "m"}}

	ctx, cancel := context.WithCancel(context.Background())
	events := d.StreamResponse(ctx, nil, nil, nil)
	<-events // the consumer reads one event...
	cancel() // ...then cancels and never reads again.

	select {
	case <-client.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("driver goroutine is still blocked sending to a consumer that left — it pins its stream for the life of the process")
	}
}

// TestBaseDriverStream_CancelledConsumerStillGetsBufferedEvents guards the
// other direction: a cancelled consumer that KEEPS reading — CallLLM drains
// what the driver already produced so partial output is not dropped — must
// receive every event, not have them discarded because ctx is done.
func TestBaseDriverStream_CancelledConsumerStillGetsBufferedEvents(t *testing.T) {
	shortGrace(t)
	client := &blockingSendClient{events: 20, finished: make(chan struct{})}
	d := &baseDriver{client: client, model: models.Model{ID: "m"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var got []llm.DriverEvent
	for ev := range d.StreamResponse(ctx, nil, nil, nil) {
		got = append(got, ev)
	}
	require.Len(t, got, 21, "every content event plus the terminal error")
	assert.Equal(t, llm.EventError, got[20].Type)
	for _, ev := range got {
		assert.Equal(t, models.ModelID("m"), ev.Model.ID, "events are still enriched with the model")
	}
}
