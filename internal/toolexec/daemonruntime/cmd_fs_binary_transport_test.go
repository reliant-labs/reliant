// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
)

// handlerSender delivers a RemoteClient's command straight to this daemon's
// handler for it: the server half (RemoteClient) and the daemon half
// (handleFSReadBinaryFile) of one round trip, with the NATS hop between them
// reduced to handing over the payload bytes.
type handlerSender map[string]CommandHandler

func (s handlerSender) SendDaemonCommand(ctx context.Context, _ string, commandType string, payload []byte, _ int32) ([]byte, error) {
	h, ok := s[commandType]
	if !ok {
		return nil, fmt.Errorf("no handler for %s", commandType)
	}
	return h(ctx, payload)
}

// An image the view tool reads on a remote (cloud) daemon reaches the server
// byte for byte. Pinned because the reported "view returns only a caption"
// failure was suspected here; the caption's size (241.0KB) was computed from
// these bytes, so this hop was never what dropped them — the loss was later,
// at the workflow boundary — and this keeps it that way.
func TestRemoteReadBinaryFileCarriesARealPNGWhole(t *testing.T) {
	t.Parallel()

	// A real PNG of noise, ~240KB like the Playwright screenshot in the
	// report: noise does not compress, so the encoder cannot shrink it.
	img := image.NewNRGBA(image.Rect(0, 0, 280, 220))
	seed := uint32(1)
	for i := range img.Pix {
		seed = seed*1664525 + 1013904223
		img.Pix[i] = uint8(seed >> 24)
	}
	img.Set(0, 0, color.NRGBA{A: 255})
	var want bytes.Buffer
	if err := png.Encode(&want, img); err != nil {
		t.Fatal(err)
	}
	if want.Len() < 200_000 {
		t.Fatalf("fixture PNG is %d bytes, want a screenshot-sized one", want.Len())
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "dashboard-end.png")
	if err := os.WriteFile(path, want.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	client := daemon.NewRemoteClient(handlerSender{"fs.read_binary_file": handleFSReadBinaryFile}, "user-1")
	got, err := client.ReadBinaryFile(context.Background(), path, 5*1024*1024)
	if err != nil {
		t.Fatalf("ReadBinaryFile: %v", err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("read %d bytes over the remote path, want the file's %d unchanged", len(got), want.Len())
	}
	if _, err := png.DecodeConfig(bytes.NewReader(got)); err != nil {
		t.Fatalf("bytes read over the remote path are not a PNG: %v", err)
	}

	// The size cap is enforced daemon-side, before any bytes cross.
	if _, err := client.ReadBinaryFile(context.Background(), path, int64(want.Len()-1)); err == nil {
		t.Fatal("ReadBinaryFile under the file's size succeeded, want the daemon to refuse it")
	}
}
