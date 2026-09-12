// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// binaryFixture is a short byte string that is deliberately NOT valid UTF-8:
// the PNG magic number, followed by bytes that are invalid UTF-8 continuation
// sequences and a lone 0xFF. Real image payloads contain sequences like these
// within the first few hundred bytes.
func binaryFixture() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0xff, 0xfe, 0x80, 0x81, 0xc3, 0x28, 0xed, 0xa0, 0x80,
	}
}

// TestWriteFileCorruptsBinaryContent documents WHY fs.write_binary_file has to
// exist. fs.write_file carries its content in a JSON string field, and
// encoding/json replaces every byte that is not valid UTF-8 with U+FFFD when
// it marshals a Go string. The daemon therefore receives, and writes, bytes
// that are not the bytes the caller handed it.
//
// This is an assertion that the corruption HAPPENS. If it ever stops happening
// the binary command is unnecessary and this test should fail loudly rather
// than quietly pass.
func TestWriteFileCorruptsBinaryContent(t *testing.T) {
	original := binaryFixture()
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "corrupt.png")

	// Exactly what a server-side caller does: build the request struct and
	// marshal it as the daemon-command payload.
	payload, err := json.Marshal(fsWriteFileRequest{Path: path, Content: string(original)})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if _, err := handleFSWriteFile(context.Background(), payload); err != nil {
		t.Fatalf("handleFSWriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if bytes.Equal(got, original) {
		t.Fatalf("fs.write_file preserved non-UTF-8 bytes; the binary write command may be unnecessary")
	}
	t.Logf("fs.write_file corrupted binary content: wrote %d bytes, got %d bytes back", len(original), len(got))
	t.Logf("  original: % x", original)
	t.Logf("  readback: % x", got)
}

// TestWriteBinaryFileRoundTrip is the assertion that matters: bytes written
// through fs.write_binary_file come back byte-for-byte, including the
// non-UTF-8 sequences that fs.write_file destroys.
func TestWriteBinaryFileRoundTrip(t *testing.T) {
	original := binaryFixture()
	dir := t.TempDir()
	// A path two levels below the temp dir, so the parent-directory creation
	// that fs.write_file callers depend on is exercised too.
	path := filepath.Join(dir, "generated", "images", "out.png")

	payload, err := json.Marshal(fsWriteBinaryFileRequest{
		Path: path,
		Data: base64.StdEncoding.EncodeToString(original),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	raw, err := handleFSWriteBinaryFile(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleFSWriteBinaryFile: %v", err)
	}

	var resp fsWriteBinaryFileResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.Created {
		t.Errorf("Created = false, want true for a new file")
	}
	if resp.BytesWritten != len(original) {
		t.Errorf("BytesWritten = %d, want %d", resp.BytesWritten, len(original))
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("bytes did not round-trip:\n  wrote: % x\n  read:  % x", original, got)
	}

	// And through the read side, which is the path a consumer actually uses.
	readPayload, err := json.Marshal(fsReadBinaryFileRequest{Path: path})
	if err != nil {
		t.Fatalf("marshal read payload: %v", err)
	}
	readRaw, err := handleFSReadBinaryFile(context.Background(), readPayload)
	if err != nil {
		t.Fatalf("handleFSReadBinaryFile: %v", err)
	}
	var readResp fsReadBinaryFileResponse
	if err := json.Unmarshal(readRaw, &readResp); err != nil {
		t.Fatalf("unmarshal read response: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(readResp.Data)
	if err != nil {
		t.Fatalf("decode read data: %v", err)
	}
	if !bytes.Equal(decoded, original) {
		t.Fatalf("write→read round trip changed bytes:\n  wrote: % x\n  read:  % x", original, decoded)
	}
}

// TestWriteBinaryFileOverwriteReportsCreatedFalse pins the create-vs-overwrite
// signal, which mirrors fs.write_file's WriteResult.Created.
func TestWriteBinaryFileOverwriteReportsCreatedFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")

	write := func(data []byte) fsWriteBinaryFileResponse {
		t.Helper()
		payload, err := json.Marshal(fsWriteBinaryFileRequest{
			Path: path,
			Data: base64.StdEncoding.EncodeToString(data),
		})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		raw, err := handleFSWriteBinaryFile(context.Background(), payload)
		if err != nil {
			t.Fatalf("handleFSWriteBinaryFile: %v", err)
		}
		var resp fsWriteBinaryFileResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		return resp
	}

	if resp := write([]byte{0xff, 0x00}); !resp.Created {
		t.Errorf("first write: Created = false, want true")
	}
	second := []byte{0x89, 0xfe, 0x01, 0x02}
	if resp := write(second); resp.Created {
		t.Errorf("second write: Created = true, want false")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Fatalf("overwrite left % x, want % x", got, second)
	}
}

// TestWriteBinaryFileConfinedByPolicy drives the handler directly, past the
// dispatch-time payload check, because that check is a fast reject rather than
// the boundary. The handler must refuse an out-of-root destination on its own,
// including one reached through a symlink swapped in after dispatch ran.
func TestWriteBinaryFileConfinedByPolicy(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on Windows")
	}

	root := t.TempDir()
	outside := t.TempDir()

	escapeLink := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escapeLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	ctx := daemonpolicy.NewContext(context.Background(), &daemonpolicy.Policy{
		GrantID:  "grant-binwrite",
		Tools:    map[string]bool{"fs.write_binary_file": true},
		PathRoot: root,
	})

	data := base64.StdEncoding.EncodeToString(binaryFixture())

	for _, tc := range []struct {
		name string
		path string
	}{
		{"absolute", filepath.Join(outside, "planted.png")},
		{"symlink", filepath.Join(escapeLink, "planted.png")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(fsWriteBinaryFileRequest{Path: tc.path, Data: data})
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			if _, err := handleFSWriteBinaryFile(ctx, payload); err == nil {
				t.Fatalf("a write outside the grant root must be refused")
			} else if !errors.Is(err, daemonpolicy.ErrDenied) {
				t.Fatalf("refused for the wrong reason: %v", err)
			}
			if _, err := os.Stat(filepath.Join(outside, "planted.png")); !os.IsNotExist(err) {
				t.Fatalf("a refused write still created the file (stat err = %v)", err)
			}
		})
	}

	// Inside the root the same command succeeds, so the gate is confining the
	// destination rather than rejecting every binary write.
	t.Run("inside the root", func(t *testing.T) {
		inside := filepath.Join(root, "generated", "ok.png")
		payload, err := json.Marshal(fsWriteBinaryFileRequest{Path: inside, Data: data})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if _, err := handleFSWriteBinaryFile(ctx, payload); err != nil {
			t.Fatalf("a write inside the grant root was refused: %v", err)
		}
		got, err := os.ReadFile(inside)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if !bytes.Equal(got, binaryFixture()) {
			t.Fatalf("confined write changed bytes:\n  wrote: % x\n  read:  % x", binaryFixture(), got)
		}
	})
}

// TestWriteBinaryFileRejectsInvalidBase64 keeps a malformed payload from
// silently producing a truncated or empty file.
func TestWriteBinaryFileRejectsInvalidBase64(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.bin")

	payload, err := json.Marshal(fsWriteBinaryFileRequest{Path: path, Data: "not!valid!base64"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := handleFSWriteBinaryFile(context.Background(), payload); err == nil {
		t.Fatalf("expected an error for malformed base64")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a rejected write must not create the file (stat err = %v)", err)
	}
}
