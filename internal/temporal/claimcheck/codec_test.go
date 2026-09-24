// Copyright (c) 2025 Reliant Labs
package claimcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
)

// fakeStore is an in-memory Store that counts calls.
type fakeStore struct {
	mu     sync.Mutex
	blobs  map[string][]byte
	puts   atomic.Int64
	gets   atomic.Int64
	putErr error
}

func newFakeStore() *fakeStore { return &fakeStore{blobs: map[string][]byte{}} }

func (s *fakeStore) Put(_ context.Context, key string, data []byte, _ int) error {
	s.puts.Add(1)
	if s.putErr != nil {
		return s.putErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs[key] = bytes.Clone(data)
	return nil
}

func (s *fakeStore) Get(_ context.Context, key string) ([]byte, error) {
	s.gets.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.blobs[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return bytes.Clone(data), nil
}

func jsonPayload(dataLen int) *commonpb.Payload {
	return &commonpb.Payload{
		Metadata: map[string][]byte{converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON)},
		Data:     []byte(`"` + strings.Repeat("x", dataLen) + `"`),
	}
}

func mustEncode(t *testing.T, c *Codec, ps ...*commonpb.Payload) []*commonpb.Payload {
	t.Helper()
	out, err := c.Encode(ps)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return out
}

func TestRoundTripBelowAndAboveThreshold(t *testing.T) {
	store := newFakeStore()
	codec := NewCodec(store)
	small := jsonPayload(100)
	large := jsonPayload(200 << 10)

	encoded := mustEncode(t, codec, small, large)
	if encoded[0] != small {
		t.Errorf("small payload was rewritten; want pass-through")
	}
	if !isReference(encoded[1]) {
		t.Fatalf("large payload not claim-checked: metadata=%v", encoded[1].Metadata)
	}
	if len(encoded[1].Data) > 256 {
		t.Errorf("reference body is %d bytes; want small", len(encoded[1].Data))
	}
	if store.puts.Load() != 1 {
		t.Errorf("puts = %d, want 1", store.puts.Load())
	}

	// Decode with a fresh codec (empty cache) to exercise the store path.
	decoded, err := NewCodec(store).Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !proto.Equal(decoded[0], small) || !proto.Equal(decoded[1], large) {
		t.Errorf("round trip mismatch")
	}
}

func TestThresholdBoundary(t *testing.T) {
	const threshold = 4096
	codec := NewCodec(newFakeStore(), WithThreshold(threshold))
	// Find a payload exactly at the threshold and one just below.
	var at *commonpb.Payload
	for n := threshold - 64; n < threshold; n++ {
		if p := jsonPayload(n); proto.Size(p) == threshold {
			at = p
			break
		}
	}
	if at == nil {
		t.Fatal("could not construct a payload of exactly threshold size")
	}
	below := jsonPayload(len(at.Data) - 2 - 1)
	if proto.Size(below) != threshold-1 {
		t.Fatalf("below size = %d, want %d", proto.Size(below), threshold-1)
	}
	out := mustEncode(t, codec, at, below)
	if !isReference(out[0]) {
		t.Errorf("payload of exactly threshold bytes should be offloaded")
	}
	if isReference(out[1]) {
		t.Errorf("payload of threshold-1 bytes should stay inline")
	}
}

func TestForeignEncodingPassesThrough(t *testing.T) {
	codec := NewCodec(newFakeStore())
	foreign := &commonpb.Payload{
		Metadata: map[string][]byte{converter.MetadataEncoding: []byte("binary/encrypted")},
		Data:     []byte("opaque"),
	}
	out, err := codec.Decode([]*commonpb.Payload{foreign})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out[0] != foreign {
		t.Errorf("foreign payload was not passed through untouched")
	}
}

func TestInputsNotMutated(t *testing.T) {
	store := newFakeStore()
	codec := NewCodec(store)
	large := jsonPayload(100 << 10)
	snapshot := proto.Clone(large)
	in := []*commonpb.Payload{large}

	encoded := mustEncode(t, codec, in...)
	if in[0] != large || !proto.Equal(large, snapshot) {
		t.Fatalf("Encode mutated its input")
	}
	refSnapshot := proto.Clone(encoded[0])
	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !proto.Equal(encoded[0], refSnapshot) {
		t.Fatalf("Decode mutated its input")
	}
	// Mutating a decoded payload must not leak into the next decode (cache
	// must hand out fresh messages).
	decoded[0].Data[1] = 'Z'
	again, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !proto.Equal(again[0], snapshot) {
		t.Fatalf("decoded payload shares memory with the cache")
	}
}

func TestHashMismatchDetected(t *testing.T) {
	store := newFakeStore()
	encoded := mustEncode(t, NewCodec(store), jsonPayload(100<<10))
	// Replace the stored blob with a valid zstd stream of different content.
	other := jsonPayload(100 << 10)
	other.Data[1] = 'y'
	raw, _ := proto.Marshal(other)
	codec := NewCodec(store)
	for k := range store.blobs {
		store.blobs[k] = codec.encoder.EncodeAll(raw, nil)
	}
	_, err := codec.Decode(encoded)
	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("Decode err = %v, want hash mismatch", err)
	}
}

func TestMissingStoreFailsLoudly(t *testing.T) {
	encoded := mustEncode(t, NewCodec(newFakeStore()), jsonPayload(100<<10))
	storeless := NewCodec(nil)
	_, err := storeless.Decode(encoded)
	if !errors.Is(err, ErrNoStore) {
		t.Fatalf("Decode err = %v, want ErrNoStore", err)
	}
	// Without a store, Encode must not offload (it has nowhere to put it).
	large := jsonPayload(100 << 10)
	if out := mustEncode(t, storeless, large); out[0] != large {
		t.Errorf("storeless codec rewrote a payload")
	}
}

func TestEncodeStoreErrorIsReturned(t *testing.T) {
	store := newFakeStore()
	store.putErr = errors.New("db down")
	_, err := NewCodec(store).Encode([]*commonpb.Payload{jsonPayload(100 << 10)})
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("Encode err = %v, want store error (no inline fallback)", err)
	}
}

func TestCacheHitAvoidsStoreGet(t *testing.T) {
	store := newFakeStore()
	encoded := mustEncode(t, NewCodec(store), jsonPayload(100<<10))

	reader := NewCodec(store)
	for range 5 {
		if _, err := reader.Decode(encoded); err != nil {
			t.Fatalf("Decode: %v", err)
		}
	}
	if got := store.gets.Load(); got != 1 {
		t.Errorf("store gets = %d, want 1 (later decodes served from cache)", got)
	}

	// The encoding process seeds its own cache: decoding what it just wrote
	// costs no store read.
	store.gets.Store(0)
	writer := NewCodec(store)
	enc := mustEncode(t, writer, jsonPayload(90<<10))
	if _, err := writer.Decode(enc); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got := store.gets.Load(); got != 0 {
		t.Errorf("store gets after self-encode = %d, want 0", got)
	}
}

func TestCacheEvictsByBytes(t *testing.T) {
	l := newByteLRU(100)
	l.add("a", make([]byte, 60))
	l.add("b", make([]byte, 30))
	l.get("a") // a is now most recent
	l.add("c", make([]byte, 30))
	if _, ok := l.get("b"); ok {
		t.Errorf("b should have been evicted (least recent)")
	}
	if _, ok := l.get("a"); !ok {
		t.Errorf("a should be retained")
	}
	if l.curBytes > 100 {
		t.Errorf("curBytes = %d exceeds budget", l.curBytes)
	}
	l.add("huge", make([]byte, 101))
	if _, ok := l.get("huge"); ok {
		t.Errorf("value larger than budget should not be cached")
	}
}

func TestConcurrentDecode(t *testing.T) {
	store := newFakeStore()
	writer := NewCodec(store)
	var refs []*commonpb.Payload
	var originals []*commonpb.Payload
	for i := range 8 {
		p := jsonPayload(40<<10 + i)
		originals = append(originals, p)
		refs = append(refs, mustEncode(t, writer, p)[0])
	}
	// Small cache forces concurrent eviction + store reads.
	reader := NewCodec(store, WithCacheBytes(100<<10))
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 20 {
				i := (g + j) % len(refs)
				out, err := reader.Decode([]*commonpb.Payload{refs[i]})
				if err != nil {
					errs <- err
					return
				}
				if !proto.Equal(out[0], originals[i]) {
					errs <- fmt.Errorf("payload %d mismatch", i)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
