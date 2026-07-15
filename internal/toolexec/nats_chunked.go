// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
)

// Transparent chunked replies for the daemon request/reply paths.
//
// NATS enforces a per-message max_payload (default 1 MB). Daemon command
// replies (fs.search, worktree.git_changes, file reads, ...) routinely exceed
// it, and a user-initiated RPC has no way to "narrow the search" — the
// transport has to make large replies just work. So the bridge splits an
// oversize reply into chunks published to the requester's reply inbox, each
// carrying a small header envelope (correlation id, sequence, total count,
// total size), and the router reassembles them before handing the bytes to
// the caller. Small replies keep the single-message fast path, byte-identical
// to a plain msg.Respond, so a mixed old/new pair degrades to exactly the old
// behavior for anything that fits.
//
// Both ends live in our own binaries and deploy together — no cross-version
// protocol negotiation. Policy (e.g. truncating what an LLM sees) lives in
// the layers above; the transport only moves bytes.

// Chunk header keys. Presence of chunkHeaderID on a reply message marks it as
// part of a chunked stream; its absence means a plain single-message reply.
const (
	chunkHeaderID    = "Reliant-Chunk-Id"    // correlation token for one reply stream
	chunkHeaderSeq   = "Reliant-Chunk-Seq"   // 0-based chunk index
	chunkHeaderCount = "Reliant-Chunk-Count" // total number of chunks
	chunkHeaderBytes = "Reliant-Chunk-Bytes" // total reassembled payload size
)

// maxChunkedReplyBytes is the hard absolute cap on a chunked reply. It bounds
// requester-side reassembly memory; beyond it the bridge substitutes the
// structured "response too large" error instead of chunking.
const maxChunkedReplyBytes = 64 << 20 // 64 MB

// chunkReassemblyTimeout bounds the wait between consecutive chunks of one
// reply. Chunks are published back-to-back, so a gap this long means the
// publisher died mid-stream — fail instead of waiting out the (possibly
// multi-minute) overall request timeout.
const chunkReassemblyTimeout = 30 * time.Second

// errReplyExceedsAbsoluteCap is returned by publishReply when the reply is
// too large even for chunking. Callers substitute a structured error reply.
var errReplyExceedsAbsoluteCap = errors.New("reply exceeds absolute chunked-reply cap")

// chunkPayloadBudget is the per-chunk payload size for a connection limit:
// max_payload minus headroom for the chunk headers (and any future ones).
func chunkPayloadBudget(maxPayload int64) int {
	budget := maxPayload - natsPayloadHeadroom
	if budget <= 0 {
		budget = maxPayload
	}
	return int(budget)
}

// publishReply publishes data to the reply subject. A payload within the
// connection's max_payload budget goes out as a single plain message —
// byte-identical on the wire to msg.Respond. An oversize payload is split
// into chunk messages that requestWithChunkedReply reassembles. Returns the
// number of messages published (1 == single-message fast path), or
// errReplyExceedsAbsoluteCap when the payload is over maxChunkedReplyBytes.
func publishReply(nc *nats.Conn, reply string, data []byte) (int, error) {
	if reply == "" {
		return 0, errors.New("publishReply: empty reply subject")
	}
	maxPayload := nc.MaxPayload()
	if !exceedsNATSPayloadLimit(len(data), maxPayload) {
		return 1, nc.Publish(reply, data)
	}
	if len(data) > maxChunkedReplyBytes {
		return 0, fmt.Errorf("%w: %d bytes > %d", errReplyExceedsAbsoluteCap, len(data), int64(maxChunkedReplyBytes))
	}

	budget := chunkPayloadBudget(maxPayload)
	id := nats.NewInbox() // opaque unique correlation token
	count := (len(data) + budget - 1) / budget
	for seq := 0; seq < count; seq++ {
		start := seq * budget
		end := min(start+budget, len(data))
		m := &nats.Msg{
			Subject: reply,
			Data:    data[start:end],
			Header: nats.Header{
				chunkHeaderID:    []string{id},
				chunkHeaderSeq:   []string{strconv.Itoa(seq)},
				chunkHeaderCount: []string{strconv.Itoa(count)},
				chunkHeaderBytes: []string{strconv.Itoa(len(data))},
			},
		}
		if err := nc.PublishMsg(m); err != nil {
			return seq, fmt.Errorf("publish reply chunk %d/%d: %w", seq+1, count, err)
		}
	}
	return count, nil
}

// requestWithChunkedReply performs a NATS request whose reply may arrive as a
// chunked stream published by publishReply. It is a drop-in replacement for
// nc.RequestMsg: a plain single-message reply (no chunk headers) is returned
// as-is, preserving today's semantics exactly — including nats.ErrTimeout and
// nats.ErrNoResponders (NextMsg translates the server's 503 status). A
// chunked reply is reassembled into a single synthesized message.
//
// timeout bounds the wait for the first reply message (like RequestMsg);
// subsequent chunks must each arrive within chunkReassemblyTimeout and before
// the overall deadline. Reassembly is capped at maxChunkedReplyBytes.
func requestWithChunkedReply(nc *nats.Conn, reqMsg *nats.Msg, timeout time.Duration) (*nats.Msg, error) {
	if timeout <= 0 {
		return nil, nats.ErrTimeout
	}

	inbox := nc.NewRespInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sub.Unsubscribe() }()
	// A large chunked reply can burst faster than we append; default pending
	// limits (64MB) sit right at the reassembly cap, so lift them — memory is
	// already bounded by maxChunkedReplyBytes below.
	_ = sub.SetPendingLimits(-1, -1)

	reqMsg.Reply = inbox
	if err := nc.PublishMsg(reqMsg); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	first, err := sub.NextMsg(timeout)
	if err != nil {
		return nil, err
	}
	id := first.Header.Get(chunkHeaderID)
	if id == "" {
		// Single-message fast path — the common case.
		return first, nil
	}

	count, err := strconv.Atoi(first.Header.Get(chunkHeaderCount))
	if err != nil || count <= 0 {
		return nil, fmt.Errorf("chunked reply: invalid chunk count %q", first.Header.Get(chunkHeaderCount))
	}
	totalBytes, err := strconv.Atoi(first.Header.Get(chunkHeaderBytes))
	if err != nil || totalBytes < 0 {
		return nil, fmt.Errorf("chunked reply: invalid total size %q", first.Header.Get(chunkHeaderBytes))
	}
	if totalBytes > maxChunkedReplyBytes {
		return nil, fmt.Errorf("chunked reply: declared size %s exceeds the %s reassembly cap",
			formatByteSize(int64(totalBytes)), formatByteSize(maxChunkedReplyBytes))
	}
	if seq := first.Header.Get(chunkHeaderSeq); seq != "0" {
		return nil, fmt.Errorf("chunked reply: stream starts at sequence %q, want 0", seq)
	}

	buf := make([]byte, 0, totalBytes)
	buf = append(buf, first.Data...)
	for seq := 1; seq < count; seq++ {
		wait := min(chunkReassemblyTimeout, time.Until(deadline))
		if wait <= 0 {
			return nil, fmt.Errorf("chunked reply: deadline exceeded after %d/%d chunks: %w", seq, count, nats.ErrTimeout)
		}
		msg, err := sub.NextMsg(wait)
		if err != nil {
			return nil, fmt.Errorf("chunked reply: waiting for chunk %d/%d: %w", seq+1, count, err)
		}
		if got := msg.Header.Get(chunkHeaderID); got != id {
			return nil, fmt.Errorf("chunked reply: interleaved reply stream (chunk id %q, want %q)", got, id)
		}
		if got := msg.Header.Get(chunkHeaderSeq); got != strconv.Itoa(seq) {
			return nil, fmt.Errorf("chunked reply: out-of-order chunk (seq %q, want %d)", got, seq)
		}
		if len(buf)+len(msg.Data) > maxChunkedReplyBytes {
			return nil, fmt.Errorf("chunked reply: reassembled size exceeds the %s cap", formatByteSize(maxChunkedReplyBytes))
		}
		buf = append(buf, msg.Data...)
	}
	if len(buf) != totalBytes {
		return nil, fmt.Errorf("chunked reply: reassembled %d bytes, expected %d", len(buf), totalBytes)
	}
	return &nats.Msg{Subject: first.Subject, Data: buf}, nil
}
