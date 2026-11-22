package serverkit

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// The first abort's cause wins: a stream the drain ended keeps reading as
// drained when its handler aborts too, so StreamDrainInterceptor still
// reports the drain. Close stays idempotent after an abort closed the body.
func TestAbortableBody_FirstCauseWins(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	b := &abortableBody{body: pr, http2: true}

	readDone := make(chan error, 1)
	go func() {
		_, err := b.Read(make([]byte, 1))
		readDone <- err
	}()
	b.abort(svcerr.ErrServerDraining)
	select {
	case err := <-readDone:
		if !errors.Is(err, svcerr.ErrServerDraining) {
			t.Fatalf("woken read = %v, want the drain's cause", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("abort did not wake the pending read")
	}

	b.abort(ErrReceiveAborted)
	if _, err := b.Read(make([]byte, 1)); !errors.Is(err, svcerr.ErrServerDraining) {
		t.Fatalf("read after a second abort = %v, want the first cause", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close after abort = %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}

// A request with no body has nothing to wake: abort and Close are no-ops.
func TestAbortableBody_NoBody(t *testing.T) {
	t.Parallel()
	b := &abortableBody{}
	b.abort(ErrReceiveAborted)
	if err := b.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
}
