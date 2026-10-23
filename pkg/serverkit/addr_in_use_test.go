package serverkit

import (
	"context"
	"net"
	"testing"
	"time"
)

// A takeover from the boot probes retries while the old socket still holds
// the address, which it can for a moment after the boot server's Close (on
// Windows CI it did in 3 of 5 main runs). The bind error is spelled the OS's way
// (addrInUseErrnos), so this runs the real bind on whatever OS runs it.
func TestListenAfterHandoff_RetriesUntilTheAddressIsFree(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := held.Addr().String()

	// Not a handoff: an in-use address is an immediate error, and it is
	// recognized as one.
	if ln, err := listenAfterHandoff(context.Background(), addr, false); err == nil {
		_ = ln.Close()
		t.Fatal("a first-time bind to a held address succeeded")
	} else if !addrInUse(err) {
		t.Fatalf("addrInUse does not recognize this OS's bind error: %v", err)
	}

	// A handoff: the bind succeeds once the holder lets go.
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = held.Close()
	}()
	ln, err := listenAfterHandoff(context.Background(), addr, true)
	if err != nil {
		t.Fatalf("the takeover did not retry until the address was free: %v", err)
	}
	_ = ln.Close()
}
