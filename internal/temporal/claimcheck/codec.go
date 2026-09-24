// Copyright (c) 2025 Reliant Labs

// Package claimcheck keeps bulk payload bytes out of Temporal workflow history.
//
// forge:exclude-contract
//
// A Codec is a converter.PayloadCodec. On Encode, any payload whose serialized
// size reaches the threshold is proto-marshaled whole (metadata included),
// zstd-compressed, written to a content-addressed Store, and replaced in
// history by a small reference payload:
//
//	Payload{Metadata: {"encoding": "claim-check/v1"},
//	        Data:     {"key":"sha256:<hex>","size":<raw>,"zsize":<compressed>}}
//
// Decode reverses that for reference payloads only; every other encoding
// passes through untouched, which is what the PayloadCodec contract requires
// and what lets pre-codec histories replay unchanged.
//
// # Determinism
//
// The key is the sha256 of the marshaled payload, so Decode is a pure function
// of the reference: replay reads back exactly the bytes the original run saw.
// Decode verifies the hash, so a corrupted or mismatched row fails the
// workflow task instead of feeding different data to a replay.
//
// # Failure semantics
//
// Store errors are returned, never swallowed. Inside workflow code the SDK
// panics on an encode error, which fails the workflow task and makes Temporal
// retry it — the correct outcome while the database is down. Falling back to
// inline would silently reintroduce the history-size problem this exists to
// solve.
package claimcheck

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
)

// Encoding is the metadata "encoding" value of a reference payload.
const Encoding = "claim-check/v1"

const (
	// DefaultThreshold is the serialized payload size at or above which a
	// payload is offloaded. Below it, the Store round trip and the reference
	// overhead are not worth it, and small payloads compress poorly anyway.
	DefaultThreshold = 32 << 10

	// DefaultCacheBytes bounds the in-process cache of decoded payloads.
	// Replays after sticky-cache eviction re-decode the same keys; the cache
	// turns those into memory reads instead of workflow-task-path DB reads.
	DefaultCacheBytes = 64 << 20

	// DefaultStoreTimeout bounds every Store call. PayloadCodec carries no
	// context, so without this a hung database would hang the caller.
	DefaultStoreTimeout = 10 * time.Second

	keyPrefix = "sha256:"

	// maxDecodedSize caps zstd decompression. Temporal rejects payloads far
	// below this, so anything larger is corruption, not data.
	maxDecodedSize = 256 << 20
)

// Store persists compressed payload blobs by content key. Implementations
// must be safe for concurrent use.
type Store interface {
	// Put stores data under key. Re-putting an existing key must succeed and
	// refresh its retention (the content is identical by construction).
	// rawSize is the uncompressed payload size, for observability.
	Put(ctx context.Context, key string, data []byte, rawSize int) error
	// Get returns the data stored under key, or an error wrapping ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, error)
}

// ErrNotFound is returned (wrapped) by Store.Get for an unknown key.
var ErrNotFound = errors.New("claim-check blob not found")

// ErrNoStore is returned when a reference payload reaches a Codec that was
// built without a Store. That process cannot read what the others wrote, so
// it must fail loudly rather than hand an undecodable payload onward.
var ErrNoStore = errors.New("claim-check payload encountered but this process has no payload store configured " +
	"(temporal.WithPayloadStore / ExternalClientConfig.PayloadStore); every reader of the namespace must share the codec")

// reference is the JSON body of a reference payload.
type reference struct {
	Key   string `json:"key"`
	Size  int    `json:"size"`
	ZSize int    `json:"zsize"`
}

// Option configures a Codec.
type Option func(*Codec)

// WithThreshold sets the offload threshold in bytes (proto.Size of the payload).
func WithThreshold(n int) Option { return func(c *Codec) { c.threshold = n } }

// WithCacheBytes sets the decode cache budget. Zero disables the cache.
func WithCacheBytes(n int) Option { return func(c *Codec) { c.cache = newByteLRU(n) } }

// WithStoreTimeout sets the per-call Store timeout.
func WithStoreTimeout(d time.Duration) Option { return func(c *Codec) { c.storeTimeout = d } }

// Codec is a converter.PayloadCodec that claim-checks large payloads.
type Codec struct {
	store        Store
	threshold    int
	storeTimeout time.Duration
	cache        *byteLRU
	encoder      *zstd.Encoder
	decoder      *zstd.Decoder
}

var _ converter.PayloadCodec = (*Codec)(nil)

// NewCodec builds a Codec over store. A nil store yields a decode-only guard:
// Encode passes everything through inline, and Decode fails with ErrNoStore
// on any reference payload.
func NewCodec(store Store, opts ...Option) *Codec {
	// EncodeAll/DecodeAll are safe for concurrent use on a shared instance.
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		panic(fmt.Sprintf("claimcheck: zstd encoder: %v", err)) // static options; cannot fail
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecodedSize), zstd.WithDecoderConcurrency(0))
	if err != nil {
		panic(fmt.Sprintf("claimcheck: zstd decoder: %v", err))
	}
	c := &Codec{
		store:        store,
		threshold:    DefaultThreshold,
		storeTimeout: DefaultStoreTimeout,
		cache:        newByteLRU(DefaultCacheBytes),
		encoder:      encoder,
		decoder:      decoder,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Encode replaces each payload at or above the threshold with a reference.
// Inputs are never mutated; untouched payloads are returned as-is.
func (c *Codec) Encode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	out := make([]*commonpb.Payload, len(payloads))
	for i, p := range payloads {
		if c.store == nil || proto.Size(p) < c.threshold || isReference(p) {
			out[i] = p
			continue
		}
		ref, err := c.encodeOne(p)
		if err != nil {
			return nil, err
		}
		out[i] = ref
	}
	return out, nil
}

func (c *Codec) encodeOne(p *commonpb.Payload) (*commonpb.Payload, error) {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("claim-check: marshal payload: %w", err)
	}
	sum := sha256.Sum256(raw)
	key := keyPrefix + hex.EncodeToString(sum[:])
	compressed := c.encoder.EncodeAll(raw, nil)

	ctx, cancel := context.WithTimeout(context.Background(), c.storeTimeout)
	defer cancel()
	// Always Put, even on a cache hit: the upsert refreshes last_referenced_at,
	// which is what keeps a blob reused by a new run alive past the GC horizon.
	if err := c.store.Put(ctx, key, compressed, len(raw)); err != nil {
		return nil, fmt.Errorf("claim-check: store put %s (%d bytes): %w", key, len(raw), err)
	}
	// The same process usually decodes what it just encoded (activity result
	// → next workflow task); seed the cache so that is not a DB read.
	c.cache.add(key, raw)

	body, err := json.Marshal(reference{Key: key, Size: len(raw), ZSize: len(compressed)})
	if err != nil {
		return nil, fmt.Errorf("claim-check: marshal reference: %w", err)
	}
	return &commonpb.Payload{
		Metadata: map[string][]byte{converter.MetadataEncoding: []byte(Encoding)},
		Data:     body,
	}, nil
}

// Decode resolves reference payloads; all other payloads pass through.
func (c *Codec) Decode(payloads []*commonpb.Payload) ([]*commonpb.Payload, error) {
	out := make([]*commonpb.Payload, len(payloads))
	for i, p := range payloads {
		if !isReference(p) {
			out[i] = p
			continue
		}
		decoded, err := c.decodeOne(p)
		if err != nil {
			return nil, err
		}
		out[i] = decoded
	}
	return out, nil
}

func (c *Codec) decodeOne(p *commonpb.Payload) (*commonpb.Payload, error) {
	if c.store == nil {
		return nil, ErrNoStore
	}
	var ref reference
	if err := json.Unmarshal(p.GetData(), &ref); err != nil {
		return nil, fmt.Errorf("claim-check: malformed reference: %w", err)
	}
	if !strings.HasPrefix(ref.Key, keyPrefix) {
		return nil, fmt.Errorf("claim-check: unsupported key %q", ref.Key)
	}

	raw, ok := c.cache.get(ref.Key)
	if !ok {
		var err error
		if raw, err = c.fetch(ref); err != nil {
			return nil, err
		}
		c.cache.add(ref.Key, raw)
	}

	// Unmarshal into a fresh message on every call: the cache holds bytes,
	// never a shared *Payload that a caller could mutate.
	decoded := &commonpb.Payload{}
	if err := proto.Unmarshal(raw, decoded); err != nil {
		return nil, fmt.Errorf("claim-check: unmarshal payload %s: %w", ref.Key, err)
	}
	return decoded, nil
}

func (c *Codec) fetch(ref reference) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.storeTimeout)
	defer cancel()
	compressed, err := c.store.Get(ctx, ref.Key)
	if err != nil {
		return nil, fmt.Errorf("claim-check: store get %s: %w", ref.Key, err)
	}
	raw, err := c.decoder.DecodeAll(compressed, make([]byte, 0, ref.Size))
	if err != nil {
		return nil, fmt.Errorf("claim-check: decompress %s: %w", ref.Key, err)
	}
	sum := sha256.Sum256(raw)
	if got := keyPrefix + hex.EncodeToString(sum[:]); got != ref.Key {
		return nil, fmt.Errorf("claim-check: content hash mismatch for %s (stored bytes hash to %s)", ref.Key, got)
	}
	return raw, nil
}

func isReference(p *commonpb.Payload) bool {
	return string(p.GetMetadata()[converter.MetadataEncoding]) == Encoding
}

// byteLRU is a mutex-guarded LRU bounded by total value bytes. Workers decode
// on many goroutines at once, so every method locks.
type byteLRU struct {
	mu       sync.Mutex
	maxBytes int
	curBytes int
	order    *list.List // front = most recent; values are *lruEntry
	entries  map[string]*list.Element
}

type lruEntry struct {
	key   string
	value []byte
}

func newByteLRU(maxBytes int) *byteLRU {
	return &byteLRU{maxBytes: maxBytes, order: list.New(), entries: map[string]*list.Element{}}
}

func (l *byteLRU) get(key string) ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.entries[key]
	if !ok {
		return nil, false
	}
	l.order.MoveToFront(el)
	return el.Value.(*lruEntry).value, true
}

// add caches value. Callers must not mutate value afterwards; values are
// only ever read (proto.Unmarshal copies out of them).
func (l *byteLRU) add(key string, value []byte) {
	if len(value) > l.maxBytes {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.entries[key]; ok {
		l.order.MoveToFront(el)
		return
	}
	l.entries[key] = l.order.PushFront(&lruEntry{key: key, value: value})
	l.curBytes += len(value)
	for l.curBytes > l.maxBytes {
		oldest := l.order.Back()
		entry := oldest.Value.(*lruEntry)
		l.order.Remove(oldest)
		delete(l.entries, entry.key)
		l.curBytes -= len(entry.value)
	}
}
