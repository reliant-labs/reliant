package daemonruntime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
)

func createBinary(t *testing.T, path string, data []byte) ([]byte, error) {
	t.Helper()
	payload, err := json.Marshal(fsWriteBinaryFileRequest{Path: path, Data: base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		t.Fatal(err)
	}
	return DefaultRegistry().Handle(context.Background(), daemon.CreateBinaryFileCommand, payload)
}

func TestCreateBinaryFile_NewPathIsCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "shot.png")
	data := []byte{0x89, 'P', 'N', 'G', 0xff, 0x00}
	raw, err := createBinary(t, path, data)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var resp fsWriteBinaryFileResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Created || resp.BytesWritten != len(data) {
		t.Fatalf("response = %+v", resp)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read back % x (%v), want % x", got, err, data)
	}
}

func TestCreateBinaryFile_ExistingFileFailsWithExistsAndIsUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.png")
	theirs := []byte("someone else's image")
	if err := os.WriteFile(path, theirs, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := createBinary(t, path, []byte{0x89, 'P', 'N', 'G'})
	if err == nil || !strings.Contains(err.Error(), daemon.ErrFileExists.Error()) {
		t.Fatalf("err = %v, want the exists refusal", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, theirs) {
		t.Fatalf("the existing file was changed to % x", got)
	}
}
