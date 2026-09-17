// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The existing round-trip test uses a handful of bytes. This drives the same
// handler at the size a real generated image arrives at — a 2.2 MB PNG is
// ~2.9 MB of base64 inside the envelope — because "invalid payload" from this
// handler is a json.Unmarshal failure, and a truncated or corrupted envelope
// only shows up once the payload is large enough to be split in transit.
func TestWriteBinaryFileHandlesMultiMBImageEnvelope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "generated.png")

	content := make([]byte, 2_200_000)
	for i := range content {
		content[i] = byte(i % 251)
	}

	payload, err := json.Marshal(fsWriteBinaryFileRequest{
		Path: path,
		Data: base64.StdEncoding.EncodeToString(content),
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	raw, err := handleFSWriteBinaryFile(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleFSWriteBinaryFile on a %d-byte envelope: %v", len(payload), err)
	}

	var resp fsWriteBinaryFileResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.BytesWritten != len(content) {
		t.Fatalf("bytes_written = %d, want %d", resp.BytesWritten, len(content))
	}

	// Byte-for-byte: a PNG that differs anywhere is a PNG that will not open.
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(onDisk) != len(content) {
		t.Fatalf("wrote %d bytes, want %d", len(onDisk), len(content))
	}
	for i := range onDisk {
		if onDisk[i] != content[i] {
			t.Fatalf("byte %d differs: got %d want %d", i, onDisk[i], content[i])
		}
	}
}
