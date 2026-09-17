// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
)

// A generated image is the largest thing that crosses this path — the 2.2 MB
// PNG that motivated fs.write_binary_file base64-encodes to ~2.9 MB inside the
// command envelope. This drives the REAL RemoteClient.WriteBinaryFile at that
// size and asserts the envelope it produces is the one the daemon's handler
// parses, because a save that fails only above some size threshold is
// indistinguishable, from the caller, from one that fails always.
func TestWriteBinaryFileEnvelopeSurvivesMultiMBImage(t *testing.T) {
	// 2.2 MB of non-UTF8 bytes, like real PNG data.
	content := make([]byte, 2_200_000)
	for i := range content {
		content[i] = byte(i % 251)
	}

	var captured []byte
	client := &RemoteClient{
		userID: "user-1",
		sender: senderFunc(func(_ context.Context, _, commandType string, payload []byte, _ int32) ([]byte, error) {
			if commandType != "fs.write_binary_file" {
				t.Fatalf("command type = %q, want fs.write_binary_file", commandType)
			}
			captured = payload
			return json.Marshal(map[string]any{"created": true, "bytes_written": len(content)})
		}),
	}

	if _, err := client.WriteBinaryFile(context.Background(), "/tmp/img.png", content); err != nil {
		t.Fatalf("WriteBinaryFile: %v", err)
	}

	// The envelope must be valid JSON — this is the exact json.Unmarshal the
	// daemon handler performs, and the one that reported "invalid payload".
	var req struct {
		Path string `json:"path"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatalf("daemon-side unmarshal of a %d-byte envelope failed: %v", len(captured), err)
	}

	decoded, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	if len(decoded) != len(content) {
		t.Fatalf("round-tripped %d bytes, want %d", len(decoded), len(content))
	}
	for i := range decoded {
		if decoded[i] != content[i] {
			t.Fatalf("byte %d corrupted: got %d want %d", i, decoded[i], content[i])
		}
	}
}

type senderFunc func(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)

func (f senderFunc) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return f(ctx, userID, commandType, payload, timeoutMs)
}
