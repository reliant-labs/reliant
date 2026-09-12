// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Request-direction chunking (server -> daemon).
//
// These mirror the reply-direction tests in nats_payload_test.go and reuse the
// same embedded-NATS harness (startBridgeAndRouter, testMaxPayload = 16KB, so
// the effective single-message budget is 8KB after natsPayloadHeadroom).
// ---------------------------------------------------------------------------

// echoingDaemonMgr records every command it receives (so tests can assert on
// the REASSEMBLED request) and returns a canned response. payloadTestMgr's
// cmdResp short-circuits before recording, which is right for the reply-side
// tests and useless for these.
type echoingDaemonMgr struct {
	recordingDaemonMgr
	resp *reliantv1.DaemonCommandResponse
}

func (m *echoingDaemonMgr) SendDaemonCommand(_ context.Context, _ string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	m.mu.Lock()
	// Copy the payload: the caller's buffer is the assembler's, and the test
	// asserts on it after the round trip.
	m.commands = append(m.commands, &reliantv1.DaemonCommandRequest{
		RequestId:   req.RequestId,
		CommandType: req.CommandType,
		Payload:     append([]byte(nil), req.Payload...),
		TimeoutMs:   req.TimeoutMs,
	})
	m.mu.Unlock()
	return m.resp, nil
}

// The production failure: a 2.5MB PNG base64-encodes to ~3.4MB inside the
// fs.write_binary_file envelope and the write was REJECTED outright. An
// oversize request must transit intact, byte-for-byte, exactly as an oversize
// reply already does.
func TestChunkedRequest_MultiMBDaemonCommandRoundTrip(t *testing.T) {
	// Varied bytes so any reorder/misalignment shows up; ~2.2MB of JSON after
	// the envelope, ~280 chunks against the 8KB budget.
	blob := make([]byte, 2200*1024)
	for i := range blob {
		blob[i] = byte('a' + i%26)
	}
	wantPayload, err := json.Marshal(map[string]string{"path": "dog.png", "data": string(blob)})
	require.NoError(t, err)

	mgr := &echoingDaemonMgr{
		resp: &reliantv1.DaemonCommandResponse{Success: true, Payload: []byte(`{"ok":true}`)},
	}
	_, router := startBridgeAndRouter(t, mgr)

	start := time.Now()
	got, err := router.SendDaemonCommand(context.Background(), "user-1", "fs.write_binary_file", wantPayload, 20000)
	require.NoError(t, err, "oversize request must round-trip, not be rejected")
	assert.Equal(t, []byte(`{"ok":true}`), got)
	assert.Less(t, time.Since(start), 20*time.Second)

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.commands, 1, "daemon side must see exactly one reassembled command")
	assert.Equal(t, "fs.write_binary_file", mgr.commands[0].CommandType)
	assert.True(t, bytes.Equal(wantPayload, mgr.commands[0].Payload),
		"reassembled request payload must be byte-identical (%d bytes received, %d sent)",
		len(mgr.commands[0].Payload), len(wantPayload))
}

// A request within the limit stays ONE plain message on the wire — no chunk
// headers, byte-identical to today's publish. A mixed pair must degrade to
// exactly the old behavior for anything that fits.
func TestChunkedRequest_SmallRequestSingleMessageOnWire(t *testing.T) {
	mgr := &payloadTestMgr{
		cmdResp: &reliantv1.DaemonCommandResponse{Success: true, Payload: []byte(`{}`)},
	}
	nc, router := startBridgeAndRouter(t, mgr)

	// Tap the request subject alongside the bridge.
	tap, err := nc.SubscribeSync(daemonSubject(daemonCommandSubject, "user-1", "daemon-1"))
	require.NoError(t, err)
	defer func() { _ = tap.Unsubscribe() }()
	require.NoError(t, nc.Flush())

	_, err = router.SendDaemonCommand(context.Background(), "user-1", "fs.stat", []byte(`{"path":"a.go"}`), 5000)
	require.NoError(t, err)

	msg, err := tap.NextMsg(2 * time.Second)
	require.NoError(t, err)
	assert.Empty(t, msg.Header.Get(chunkHeaderID), "small request must not carry chunk headers")
	assert.Contains(t, string(msg.Data), `"command_type":"fs.stat"`)

	_, err = tap.NextMsg(250 * time.Millisecond)
	assert.ErrorIs(t, err, nats.ErrTimeout, "a small request must be exactly one message on the wire")
}

// The shared-subject hazard: unlike a reply (private inbox, one stream), every
// requester publishes chunks onto the SAME subject, so streams interleave
// arbitrarily. Each must reassemble into its own request with no
// cross-contamination.
func TestChunkedRequest_InterleavedConcurrentRequestsReassemble(t *testing.T) {
	const concurrency = 8
	mgr := &echoingDaemonMgr{
		resp: &reliantv1.DaemonCommandResponse{Success: true, Payload: []byte(`{}`)},
	}
	_, router := startBridgeAndRouter(t, mgr)

	// Each request is ~150KB (≈19 chunks) of a distinct repeated marker, so a
	// single byte from the wrong stream is detectable.
	want := make([][]byte, concurrency)
	for i := range want {
		marker := fmt.Sprintf("stream-%02d-", i)
		body := bytes.Repeat([]byte(marker), 150*1024/len(marker))
		p, err := json.Marshal(map[string]string{"path": marker, "data": string(body)})
		require.NoError(t, err)
		want[i] = p
	}

	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = router.SendDaemonCommand(context.Background(), "user-1",
				"fs.write_binary_file", want[i], 20000)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "concurrent oversize request %d failed", i)
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.commands, concurrency)
	seen := map[string]bool{}
	for _, cmd := range mgr.commands {
		var got struct{ Path, Data string }
		require.NoError(t, json.Unmarshal(cmd.Payload, &got))
		idx := -1
		for i := range want {
			if got.Path == fmt.Sprintf("stream-%02d-", i) {
				idx = i
			}
		}
		require.NotEqual(t, -1, idx, "unrecognized reassembled path %q", got.Path)
		assert.False(t, seen[got.Path], "stream %s reassembled twice", got.Path)
		seen[got.Path] = true
		assert.True(t, bytes.Equal(want[idx], cmd.Payload),
			"stream %s was cross-contaminated (got %d bytes, want %d)",
			got.Path, len(cmd.Payload), len(want[idx]))
	}
	assert.Len(t, seen, concurrency)
}

// A requester that dies mid-stream leaves a partial assembly on the receiving
// side. It must be evicted rather than held forever — an unbounded reassembly
// buffer on the daemon gateway is a DoS against the user's own machine.
func TestChunkAssembler_AbandonedPartialIsEvicted(t *testing.T) {
	now := time.Now()
	asm := newChunkAssembler()
	asm.now = func() time.Time { return now }

	// Two of three chunks arrive, then the publisher vanishes.
	for seq := 0; seq < 2; seq++ {
		msg, done, err := asm.accept(requestChunkMsg("abandoned", seq, 3, 300, make([]byte, 100)))
		require.NoError(t, err)
		require.False(t, done)
		require.Nil(t, msg)
	}
	assert.Equal(t, 1, asm.inFlight(), "partial assembly should be held while fresh")
	assert.Equal(t, 300, asm.reservedBytes())

	// Past the idle deadline a sweep must drop it and release its reservation.
	now = now.Add(chunkAssemblyIdleTimeout + time.Second)
	asm.sweep()
	assert.Equal(t, 0, asm.inFlight(), "abandoned partial must be evicted")
	assert.Equal(t, 0, asm.reservedBytes(), "evicted partial must release its byte reservation")
}

// Eviction also happens on the arrival path, so a receiver that is busy (and
// therefore actually at risk of running out of memory) reclaims abandoned
// partials without depending on anyone calling sweep.
func TestChunkAssembler_EvictsOnArrival(t *testing.T) {
	now := time.Now()
	asm := newChunkAssembler()
	asm.now = func() time.Time { return now }

	_, _, err := asm.accept(requestChunkMsg("dead", 0, 2, 200, make([]byte, 100)))
	require.NoError(t, err)
	require.Equal(t, 1, asm.inFlight())

	now = now.Add(chunkAssemblyIdleTimeout + time.Second)
	_, _, err = asm.accept(requestChunkMsg("live", 0, 2, 200, make([]byte, 100)))
	require.NoError(t, err)
	assert.Equal(t, 1, asm.inFlight(), "the stale stream must be gone, leaving only the new one")
	assert.Equal(t, 200, asm.reservedBytes())
}

// A whole-assembler byte budget bounds total memory across concurrent partial
// requests, independent of how large any single one is. The per-request cap
// alone bounds nothing on a shared subject: N callers each under it still add
// up.
func TestChunkAssembler_RejectsBeyondGlobalBudget(t *testing.T) {
	asm := newChunkAssembler()

	// Fill the global budget with in-flight partials at the per-request cap.
	admitted := 0
	for i := 0; i*maxChunkedRequestBytes < maxChunkAssemblyBytes; i++ {
		_, _, err := asm.accept(requestChunkMsg(fmt.Sprintf("hog-%d", i), 0, 2, maxChunkedRequestBytes, make([]byte, 8)))
		require.NoError(t, err)
		admitted++
	}
	require.Positive(t, admitted)

	_, _, err := asm.accept(requestChunkMsg("one-too-many", 0, 2, maxChunkedRequestBytes, make([]byte, 8)))
	require.Error(t, err, "a new assembly beyond the global budget must be rejected, not admitted")
	assert.ErrorIs(t, err, errChunkAssemblyRejected)
	assert.Contains(t, err.Error(), "reassembly budget")
	assert.LessOrEqual(t, asm.reservedBytes(), maxChunkAssemblyBytes)
}

// The COUNT of partial assemblies is bounded too: a flood of tiny two-chunk
// streams would never trip the byte budget while still growing the map.
func TestChunkAssembler_RejectsBeyondInFlightCount(t *testing.T) {
	asm := newChunkAssembler()
	for i := 0; i < maxInFlightChunkAssemblies; i++ {
		_, _, err := asm.accept(requestChunkMsg(fmt.Sprintf("tiny-%d", i), 0, 2, 16, make([]byte, 8)))
		require.NoError(t, err)
	}
	_, _, err := asm.accept(requestChunkMsg("one-too-many", 0, 2, 16, make([]byte, 8)))
	require.Error(t, err)
	assert.ErrorIs(t, err, errChunkAssemblyRejected)
	assert.Equal(t, maxInFlightChunkAssemblies, asm.inFlight())
}

// Above the absolute per-request cap the transport still refuses — but the
// message must no longer tell the caller to split the payload itself, because
// splitting is now the transport's job.
func TestChunkedRequest_OverAbsoluteCapRejectedWithActionableError(t *testing.T) {
	nc := startPayloadTestNATS(t)
	router := NewNATSDaemonRouter(nc)

	oversize, err := json.Marshal(map[string]string{
		"path": "huge.bin",
		"data": string(bytes.Repeat([]byte("x"), maxChunkedRequestBytes+(1<<20))),
	})
	require.NoError(t, err)

	start := time.Now()
	_, err = router.SendDaemonCommand(context.Background(), "user-1", "fs.write_binary_file", oversize, 5000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "request too large")
	assert.Contains(t, err.Error(), "32.0 MB", "error must name the absolute cap, not the per-message limit")
	assert.NotContains(t, err.Error(), "split large payloads into smaller chunks",
		"splitting is the transport's job now; the hint must not push it back on the caller")
	assert.Contains(t, err.Error(), "out of band", "error must point at the remedy that actually works")
	assert.Less(t, time.Since(start), 10*time.Second)
}

// A chunk whose stream was already evicted (or whose opening chunk was lost)
// can never complete, so it must fail the request immediately rather than be
// silently dropped — a requester blocked on its inbox has no other way to
// learn the request died.
func TestChunkAssembler_OrphanChunkFailsFast(t *testing.T) {
	asm := newChunkAssembler()
	_, done, err := asm.accept(requestChunkMsg("orphan", 3, 5, 500, make([]byte, 100)))
	require.Error(t, err)
	assert.False(t, done)
	assert.Contains(t, err.Error(), "no opening chunk")
	assert.Equal(t, 0, asm.inFlight())
}

// requestChunkMsg builds a synthetic request chunk for direct assembler tests.
func requestChunkMsg(id string, seq, count, total int, data []byte) *nats.Msg {
	return &nats.Msg{
		Subject: "daemon.command.user-1.daemon-1",
		Reply:   "_INBOX.test",
		Data:    data,
		Header: nats.Header{
			chunkHeaderID:    []string{id},
			chunkHeaderSeq:   []string{strconv.Itoa(seq)},
			chunkHeaderCount: []string{strconv.Itoa(count)},
			chunkHeaderBytes: []string{strconv.Itoa(total)},
		},
	}
}
