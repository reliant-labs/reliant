// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// nonUTF8 is deliberately not valid UTF-8: the PNG magic number followed by
// invalid continuation sequences and a lone 0xFF. These are exactly the bytes
// a JSON string field destroys.
var nonUTF8 = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
	0xff, 0xfe, 0x80, 0x81, 0xc3, 0x28, 0xed, 0xa0, 0x80,
}

// captureSender records the payload a RemoteClient call put on the wire and
// replies with a canned response, so a test can inspect the encoding without
// a daemon.
type captureSender struct {
	commandType string
	payload     []byte
	reply       []byte
}

func (s *captureSender) SendDaemonCommand(
	ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32,
) ([]byte, error) {
	s.commandType = commandType
	s.payload = payload
	if s.reply != nil {
		return s.reply, nil
	}
	return []byte(`{}`), nil
}

// TestRemoteWriteFileCorruptsBinary is why WriteBinaryFile exists. WriteFile's
// content crosses as a JSON string, and encoding/json substitutes U+FFFD for
// every byte that is not valid UTF-8 — no error, just different bytes.
func TestRemoteWriteFileCorruptsBinary(t *testing.T) {
	sender := &captureSender{}
	client := NewRemoteClient(sender, "user-1")

	if _, err := client.WriteFile(context.Background(), "/w/out.png", string(nonUTF8)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var sent struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(sender.payload, &sent); err != nil {
		t.Fatalf("unmarshal sent payload: %v", err)
	}
	if bytes.Equal([]byte(sent.Content), nonUTF8) {
		t.Fatalf("WriteFile preserved non-UTF-8 bytes; WriteBinaryFile may be unnecessary")
	}
	t.Logf("WriteFile put %d bytes on the wire for a %d byte input", len(sent.Content), len(nonUTF8))
	t.Logf("  original: % x", nonUTF8)
	t.Logf("  on wire:  % x", []byte(sent.Content))
}

// TestRemoteWriteBinaryFileEncodesBase64 pins the wire contract Phase 4 will
// depend on: command fs.write_binary_file, payload {path, data} with data
// base64, decoding back to the exact input bytes.
func TestRemoteWriteBinaryFileEncodesBase64(t *testing.T) {
	sender := &captureSender{reply: []byte(`{"created":true,"bytes_written":17}`)}
	client := NewRemoteClient(sender, "user-1")

	res, err := client.WriteBinaryFile(context.Background(), "/w/out.png", nonUTF8)
	if err != nil {
		t.Fatalf("WriteBinaryFile: %v", err)
	}
	if !res.Created || res.BytesWritten != len(nonUTF8) {
		t.Errorf("WriteResult = %+v, want Created=true BytesWritten=%d", res, len(nonUTF8))
	}
	if sender.commandType != "fs.write_binary_file" {
		t.Errorf("commandType = %q, want fs.write_binary_file", sender.commandType)
	}

	var sent struct {
		Path string `json:"path"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(sender.payload, &sent); err != nil {
		t.Fatalf("unmarshal sent payload: %v", err)
	}
	if sent.Path != "/w/out.png" {
		t.Errorf("path = %q, want /w/out.png", sent.Path)
	}
	decoded, err := base64.StdEncoding.DecodeString(sent.Data)
	if err != nil {
		t.Fatalf("data field was not base64: %v", err)
	}
	if !bytes.Equal(decoded, nonUTF8) {
		t.Fatalf("bytes did not survive encoding:\n  wrote: % x\n  wire:  % x", nonUTF8, decoded)
	}
}

// TestLocalWriteBinaryFileRoundTrip covers the monolith path, where there is
// no JSON hop but the interface must still behave identically.
func TestLocalWriteBinaryFileRoundTrip(t *testing.T) {
	client := NewLocalClient()
	path := filepath.Join(t.TempDir(), "generated", "out.png")

	res, err := client.WriteBinaryFile(context.Background(), path, nonUTF8)
	if err != nil {
		t.Fatalf("WriteBinaryFile: %v", err)
	}
	if !res.Created {
		t.Errorf("Created = false, want true for a new file")
	}
	if res.BytesWritten != len(nonUTF8) {
		t.Errorf("BytesWritten = %d, want %d", res.BytesWritten, len(nonUTF8))
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, nonUTF8) {
		t.Fatalf("bytes did not round-trip:\n  wrote: % x\n  read:  % x", nonUTF8, got)
	}

	// And through the read side, which is what a consumer actually uses.
	readBack, err := client.ReadBinaryFile(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("ReadBinaryFile: %v", err)
	}
	if !bytes.Equal(readBack, nonUTF8) {
		t.Fatalf("write→read changed bytes:\n  wrote: % x\n  read:  % x", nonUTF8, readBack)
	}

	// Overwriting reports Created=false.
	res, err = client.WriteBinaryFile(context.Background(), path, []byte{0x00, 0xff})
	if err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if res.Created {
		t.Errorf("Created = true on overwrite, want false")
	}
}
