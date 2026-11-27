// Copyright (c) 2026 Reliant Labs

package blobstore

import (
	"bytes"
	"io"
	"runtime"
	"testing"
	"testing/iotest"
)

// zeros is an endless stream of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func capacity(chunks [][]byte) (n int64) {
	for _, c := range chunks {
		n += int64(cap(c))
	}
	return n
}

// bufferPart holds at most its limit: a body of unknown size costs
// PutObject one part's worth of memory at most, and a small body little.
func TestBufferPartHoldsAtMostItsLimit(t *testing.T) {
	t.Parallel()
	chunks, n, ended, err := bufferPart(io.LimitReader(zeros{}, 3*s3PartSize), s3PartSize)
	if err != nil || ended || n != s3PartSize || capacity(chunks) != s3PartSize {
		t.Fatalf("a long body: %d bytes in %d bytes of chunks, ended %t, %v; want exactly one part, not ended", n, capacity(chunks), ended, err)
	}
	chunks, n, ended, err = bufferPart(bytes.NewReader(make([]byte, 100)), s3PartSize)
	if err != nil || !ended || n != 100 || capacity(chunks) > s3FirstChunk {
		t.Fatalf("a small body: %d bytes in %d bytes of chunks, ended %t, %v", n, capacity(chunks), ended, err)
	}
	chunks, n, ended, err = bufferPart(io.LimitReader(zeros{}, 1000), 1001)
	if err != nil || !ended || n != 1000 || capacity(chunks) > 1001 {
		t.Fatalf("a MaxBytes+1 limit: %d bytes in %d bytes of chunks, ended %t, %v", n, capacity(chunks), ended, err)
	}
	if got, err := io.ReadAll(chunksReader(chunks)); err != nil || len(got) != 1000 {
		t.Fatalf("chunksReader = %d bytes, %v", len(got), err)
	}
	chunks, _, ended, err = bufferPart(bytes.NewReader(nil), s3PartSize)
	if err != nil || !ended || len(chunks) != 0 {
		t.Fatalf("an empty body: %d chunks, ended %t, %v", len(chunks), ended, err)
	}
	for maxBytes, want := range map[int64]int64{0: s3PartSize, 1000: 1001, s3PartSize - 1: s3PartSize, s3PartSize: s3PartSize, 1 << 40: s3PartSize} {
		if got := s3BufferLimit(maxBytes); got != want {
			t.Errorf("s3BufferLimit(%d) = %d, want %d", maxBytes, got, want)
		}
	}
}

// chunkReader is one stream over its chunks, readable in any read size and
// seekable anywhere (iotest.TestReader checks Read and Seek against the
// concatenation).
func TestChunkReader(t *testing.T) {
	t.Parallel()
	for _, chunks := range [][][]byte{nil, {[]byte("a")}, {[]byte("abc"), []byte("d"), []byte("efghij")}} {
		want := bytes.Join(chunks, nil)
		if err := iotest.TestReader(chunksReader(chunks), want); err != nil {
			t.Errorf("chunks %q: %v", chunks, err)
		}
	}
	r := chunksReader([][]byte{[]byte("ab")})
	if _, err := r.Seek(-1, io.SeekStart); err == nil {
		t.Error("Seek to a negative position succeeded")
	}
	if _, err := r.Seek(0, 42); err == nil {
		t.Error("Seek with an invalid whence succeeded")
	}
}

// What buffering a part allocates, all told: one part, never the doubling
// a growing bytes.Buffer reaches (32 MiB of capacity for a 16 MiB part, and
// more in the copies on the way). Not parallel, so no other test allocates
// meanwhile.
func TestBufferPartAllocatesOnePart(t *testing.T) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	chunks, n, _, err := bufferPart(io.LimitReader(zeros{}, s3PartSize+1), s3PartSize)
	runtime.ReadMemStats(&after)
	if err != nil || n != s3PartSize {
		t.Fatalf("bufferPart = %d, %v", n, err)
	}
	const slack = 1 << 20
	if got := after.TotalAlloc - before.TotalAlloc; got > s3PartSize+slack {
		t.Fatalf("buffering one %d-byte part allocated %d bytes, want at most %d", s3PartSize, got, s3PartSize+slack)
	}
	runtime.KeepAlive(chunks)
}
