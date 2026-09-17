// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every other chunking test runs against a 16KB max_payload so it can trip the
// limit cheaply. Production runs the NATS DEFAULT, 1MB — deploy/nats/nats.conf
// sets no max_payload — which is a regime none of them touch: the per-chunk
// budget is 1MB-8KB rather than 8KB, and a real generated image is only three
// or four chunks instead of ~280. Chunk-boundary arithmetic that is exercised
// hundreds of times per stream at 8KB is exercised three times here, so an
// off-by-one at the final partial chunk shows up in exactly this configuration
// and in no other test.
//
// This pins the real thing end to end: an actual base64 image envelope, at the
// real payload limit, through the real bridge and router.
func TestChunkedRequest_ProductionMaxPayloadImageRoundTrip(t *testing.T) {
	// A 2.2MB PNG, which is what generate_image produces, base64'd into the
	// fs.write_binary_file envelope exactly as RemoteClient.WriteBinaryFile
	// builds it.
	raw := make([]byte, 2_200_000)
	for i := range raw {
		raw[i] = byte(i % 251)
	}
	wantPayload, err := json.Marshal(map[string]string{
		"path": "/tmp/scratchpad/dog.png",
		"data": base64.StdEncoding.EncodeToString(raw),
	})
	require.NoError(t, err)

	mgr := &echoingDaemonMgr{
		resp: &reliantv1.DaemonCommandResponse{Success: true, Payload: []byte(`{"created":true}`)},
	}
	// Mirrors startBridgeAndRouter, but on a connection whose max_payload is
	// production's rather than the 16KB test value that helper hardcodes.
	nc := startProdPayloadNATS(t)
	bridge := NewNATSToolBridge(nc, nil, mgr)
	bridge.OnDaemonConnected("user-1", "daemon-1")
	t.Cleanup(func() { _ = bridge.Close() })
	require.NoError(t, nc.Flush(), "flush subscriptions before issuing requests")

	router := NewNATSDaemonRouter(nc, WithResolver(staticResolver{
		daemons: []DaemonInfo{{DaemonID: "daemon-1", Type: "local", Status: "connected"}},
	}))

	got, err := router.SendDaemonCommand(
		context.Background(), "user-1", "fs.write_binary_file", wantPayload, 30000)
	require.NoError(t, err, "a real image write must round-trip at the production max_payload")
	assert.Equal(t, []byte(`{"created":true}`), got)

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.commands, 1, "daemon must see exactly one reassembled command")
	assert.True(t, bytes.Equal(wantPayload, mgr.commands[0].Payload),
		"reassembled payload must be byte-identical (%d received, %d sent)",
		len(mgr.commands[0].Payload), len(wantPayload))
}

// startProdPayloadNATS runs an in-process NATS with the DEFAULT max_payload
// (1MB) — what deploy/nats/nats.conf gets by not setting one.
func startProdPayloadNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.MaxPayload = 1024 * 1024
	srv := natstest.RunServer(&opts)
	t.Cleanup(srv.Shutdown)

	if !srv.ReadyForConnections(2 * time.Second) {
		t.Fatal("test nats server failed to come up")
	}
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}
