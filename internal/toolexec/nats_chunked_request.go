// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// Request-direction chunking. See the header comment in nats_chunked.go for
// why this is not simply the reply path with the arrows reversed.

const (
	// maxChunkedRequestBytes is the hard absolute cap on a single chunked
	// request. Beyond it the router refuses before publishing anything.
	//
	// It is deliberately HALF the reply cap. A reply is reassembled on the
	// server, once per in-flight round trip the server itself initiated. A
	// request is reassembled on the daemon gateway, where an arbitrary number
	// of callers fan in to one process that also holds every daemon's gRPC
	// stream — so the per-item cap is tighter and, more importantly, bounded
	// again in aggregate by maxChunkAssemblyBytes below. 32 MB still clears
	// the motivating case (a 2.5 MB PNG is ~3.4 MB base64 in its envelope) by
	// an order of magnitude.
	maxChunkedRequestBytes = 32 << 20 // 32 MB

	// maxChunkAssemblyBytes bounds the TOTAL bytes reserved across all
	// partially-assembled requests on one receiver. The per-request cap alone
	// does not bound anything useful here: a shared subject accepts unbounded
	// concurrent streams, so N callers each under the per-request cap still
	// add up. This is the number that actually stops request reassembly from
	// being a DoS against the user's own machine.
	maxChunkAssemblyBytes = 256 << 20 // 256 MB

	// maxInFlightChunkAssemblies bounds the COUNT of partial assemblies,
	// independent of size. A flood of tiny two-chunk streams that each reserve
	// a few bytes would never trip the byte budget while still growing the map
	// without limit.
	maxInFlightChunkAssemblies = 1024

	// chunkAssemblyIdleTimeout is how long a partial assembly may sit without
	// receiving its next chunk before it is treated as abandoned. Chunks of
	// one request are published back-to-back by a single goroutine, so a gap
	// this long means the publisher died or the connection dropped mid-stream.
	// It mirrors chunkReassemblyTimeout on the reply side.
	chunkAssemblyIdleTimeout = 30 * time.Second

	// chunkAssemblySweepInterval is the minimum gap between eviction sweeps.
	// Sweeping is driven by arriving chunks rather than a background ticker:
	// the assembler then has no goroutine to own, start or stop, and its
	// memory is already bounded by the two budgets above, so nothing is at
	// risk between sweeps. The cost is that a partial abandoned on an
	// otherwise idle receiver is evicted by the next request rather than by
	// the clock, which is the same moment its memory would start to matter.
	chunkAssemblySweepInterval = 5 * time.Second

	// maxChunkPrealloc caps how much we allocate up front from a stream's
	// DECLARED total size. The declared size is attacker-controlled in the
	// sense that it arrives on the wire before any of the bytes do, so
	// preallocating it verbatim would let a one-chunk message with a 32 MB
	// header claim 32 MB. Above this, the buffer grows as chunks actually
	// arrive.
	maxChunkPrealloc = 1 << 20 // 1 MB
)

// errChunkAssemblyRejected is returned by chunkAssembler.accept when a new
// stream cannot be admitted under the receiver's budgets.
var errChunkAssemblyRejected = errors.New("chunked request rejected")

// publishChunkedRequest publishes msg to its subject. A payload within the
// connection's max_payload budget goes out as a single plain message —
// byte-identical on the wire to today's PublishMsg, so a small request is
// indistinguishable from the pre-chunking protocol. An oversize payload is
// split into chunk messages that a chunkAssembler on the receiving side
// reassembles.
//
// Every chunk carries msg.Reply and msg.Header (trace propagation included),
// so the receiver can respond and trace from any chunk and the synthesized
// message is indistinguishable from an un-chunked one.
func publishChunkedRequest(nc *nats.Conn, msg *nats.Msg) (int, error) {
	maxPayload := nc.MaxPayload()
	if !exceedsNATSPayloadLimit(len(msg.Data), maxPayload) {
		return 1, nc.PublishMsg(msg)
	}
	if len(msg.Data) > maxChunkedRequestBytes {
		return 0, fmt.Errorf("%w: %d bytes > %d", errRequestExceedsAbsoluteCap, len(msg.Data), int64(maxChunkedRequestBytes))
	}

	budget := chunkPayloadBudget(maxPayload)
	id := nats.NewInbox() // opaque unique correlation token
	total := len(msg.Data)
	count := (total + budget - 1) / budget
	for seq := 0; seq < count; seq++ {
		start := seq * budget
		end := min(start+budget, total)

		header := nats.Header{}
		for k, v := range msg.Header {
			header[k] = v
		}
		header[chunkHeaderID] = []string{id}
		header[chunkHeaderSeq] = []string{strconv.Itoa(seq)}
		header[chunkHeaderCount] = []string{strconv.Itoa(count)}
		header[chunkHeaderBytes] = []string{strconv.Itoa(total)}

		chunk := &nats.Msg{
			Subject: msg.Subject,
			Reply:   msg.Reply,
			Data:    msg.Data[start:end],
			Header:  header,
		}
		if err := nc.PublishMsg(chunk); err != nil {
			return seq, fmt.Errorf("publish request chunk %d/%d: %w", seq+1, count, err)
		}
	}
	// Flush so a connection-level failure surfaces here rather than as an
	// unexplained timeout while the caller waits for a reply that the daemon
	// was never given the bytes to produce.
	if err := nc.Flush(); err != nil {
		return count, fmt.Errorf("flush request chunks: %w", err)
	}
	return count, nil
}

// errRequestExceedsAbsoluteCap is returned by publishChunkedRequest when the
// request is too large even for chunking.
var errRequestExceedsAbsoluteCap = errors.New("request exceeds absolute chunked-request cap")

// chunkAssembly is one in-progress reassembly.
type chunkAssembly struct {
	buf        []byte
	nextSeq    int
	count      int
	declared   int // reserved against the global byte budget
	lastUpdate time.Time
	// first holds the opening chunk's Reply/Header/Sub so the synthesized
	// message can respond and trace exactly as an un-chunked one would.
	first *nats.Msg
}

// chunkAssembler demultiplexes interleaved chunk streams arriving on a SHARED
// subject and reassembles each into its original message.
//
// This is the piece with no counterpart on the reply side. A reply inbox is
// private to one round trip, so the reply path can read chunks in order off
// its own subscription and needs no state between messages. A request subject
// is shared by every caller addressing that daemon, so chunks of unrelated
// requests arrive interleaved and the receiver must key partial state by
// correlation id — which is why both budgets and the idle eviction exist.
//
// Safe for concurrent use; nats.go delivers one subscription's messages
// serially, but a bridge has one assembler shared across many subscriptions.
type chunkAssembler struct {
	mu        sync.Mutex
	pending   map[string]*chunkAssembly
	reserved  int
	lastSweep time.Time

	// now is overridable so eviction can be tested without sleeping.
	now func() time.Time
}

func newChunkAssembler() *chunkAssembler {
	return &chunkAssembler{
		pending: make(map[string]*chunkAssembly),
		now:     time.Now,
	}
}

// accept feeds one received message to the assembler.
//
// A message with no chunk header is returned unchanged with done=true — the
// single-message fast path, which must stay indistinguishable from the
// pre-chunking protocol. A chunk that completes its stream returns the
// synthesized full message with done=true. Any other chunk returns
// (nil, false, nil): more is expected.
//
// An error means the stream is unusable (malformed headers, over a budget,
// out of order) and has been discarded; the caller should fail the request.
func (a *chunkAssembler) accept(msg *nats.Msg) (full *nats.Msg, done bool, err error) {
	id := msg.Header.Get(chunkHeaderID)
	if id == "" {
		return msg, true, nil
	}

	seq, err := strconv.Atoi(msg.Header.Get(chunkHeaderSeq))
	if err != nil || seq < 0 {
		return nil, false, fmt.Errorf("chunked request: invalid chunk seq %q", msg.Header.Get(chunkHeaderSeq))
	}
	count, err := strconv.Atoi(msg.Header.Get(chunkHeaderCount))
	if err != nil || count <= 0 {
		return nil, false, fmt.Errorf("chunked request: invalid chunk count %q", msg.Header.Get(chunkHeaderCount))
	}
	total, err := strconv.Atoi(msg.Header.Get(chunkHeaderBytes))
	if err != nil || total < 0 {
		return nil, false, fmt.Errorf("chunked request: invalid total size %q", msg.Header.Get(chunkHeaderBytes))
	}
	if total > maxChunkedRequestBytes {
		return nil, false, fmt.Errorf("chunked request: declared size %s exceeds the %s per-request cap",
			formatByteSize(int64(total)), formatByteSize(maxChunkedRequestBytes))
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Evict abandoned partials before admitting anything new, so a dead
	// publisher's reservation cannot keep a live request out.
	a.sweepLocked()

	asm, ok := a.pending[id]
	if !ok {
		if seq != 0 {
			// Either the opening chunk was lost or this stream was already
			// evicted. Either way it can never complete.
			return nil, false, fmt.Errorf("chunked request: stream %q resumed at sequence %d with no opening chunk", id, seq)
		}
		if len(a.pending) >= maxInFlightChunkAssemblies {
			return nil, false, fmt.Errorf("%w: %d partial requests already in flight (limit %d)",
				errChunkAssemblyRejected, len(a.pending), maxInFlightChunkAssemblies)
		}
		if a.reserved+total > maxChunkAssemblyBytes {
			return nil, false, fmt.Errorf("%w: %s in flight plus %s exceeds the %s reassembly budget",
				errChunkAssemblyRejected, formatByteSize(int64(a.reserved)),
				formatByteSize(int64(total)), formatByteSize(maxChunkAssemblyBytes))
		}
		asm = &chunkAssembly{
			buf:      make([]byte, 0, min(total, maxChunkPrealloc)),
			count:    count,
			declared: total,
			first:    msg,
		}
		a.pending[id] = asm
		a.reserved += total
	}

	if seq != asm.nextSeq {
		a.dropLocked(id)
		return nil, false, fmt.Errorf("chunked request: out-of-order chunk for stream %q (seq %d, want %d)", id, seq, asm.nextSeq)
	}
	if len(asm.buf)+len(msg.Data) > asm.declared {
		a.dropLocked(id)
		return nil, false, fmt.Errorf("chunked request: stream %q sent more than its declared %d bytes", id, asm.declared)
	}
	asm.buf = append(asm.buf, msg.Data...)
	asm.nextSeq++
	asm.lastUpdate = a.now()

	if asm.nextSeq < asm.count {
		return nil, false, nil
	}

	a.dropLocked(id)
	if len(asm.buf) != asm.declared {
		return nil, false, fmt.Errorf("chunked request: stream %q reassembled %d bytes, expected %d", id, len(asm.buf), asm.declared)
	}

	// Synthesize the message the sender would have published had it fit:
	// same subject, reply and headers (minus the chunk envelope), and the
	// opening chunk's Sub so msg.Respond still works.
	header := nats.Header{}
	for k, v := range asm.first.Header {
		switch k {
		case chunkHeaderID, chunkHeaderSeq, chunkHeaderCount, chunkHeaderBytes:
			continue
		}
		header[k] = v
	}
	return &nats.Msg{
		Subject: asm.first.Subject,
		Reply:   asm.first.Reply,
		Header:  header,
		Data:    asm.buf,
		Sub:     asm.first.Sub,
	}, true, nil
}

// sweep evicts every partial assembly idle past chunkAssemblyIdleTimeout.
func (a *chunkAssembler) sweep() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evictIdleLocked()
}

// sweepLocked rate-limits eviction to chunkAssemblySweepInterval so a busy
// receiver does not walk the map on every chunk of a 400-chunk stream.
func (a *chunkAssembler) sweepLocked() {
	now := a.now()
	if now.Sub(a.lastSweep) < chunkAssemblySweepInterval {
		return
	}
	a.lastSweep = now
	a.evictIdleLocked()
}

func (a *chunkAssembler) evictIdleLocked() {
	cutoff := a.now().Add(-chunkAssemblyIdleTimeout)
	for id, asm := range a.pending {
		if asm.lastUpdate.Before(cutoff) {
			a.dropLocked(id)
		}
	}
}

// dropLocked removes an assembly and releases its byte reservation.
func (a *chunkAssembler) dropLocked(id string) {
	if asm, ok := a.pending[id]; ok {
		a.reserved -= asm.declared
		delete(a.pending, id)
	}
}

// inFlight reports the number of partial assemblies held.
func (a *chunkAssembler) inFlight() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending)
}

// reservedBytes reports the total declared size of all partial assemblies.
func (a *chunkAssembler) reservedBytes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reserved
}
