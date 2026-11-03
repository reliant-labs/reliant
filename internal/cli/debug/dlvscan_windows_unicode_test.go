//go:build windows

package debug

import (
	"context"
	"os"
	"testing"
)

func TestUnicodeStringAtBounds(t *testing.T) {
	buf := []byte{'a', 0, 'b', 0, 'c', 0}
	if s, err := unicodeStringAt(buf, 2, 4); err != nil || s != "bc" {
		t.Fatalf("got %q %v", s, err)
	}
	for _, c := range []struct {
		off uintptr
		n   uint16
	}{{4, 4}, {7, 0}, {0, 3}, {^uintptr(0), 4}} {
		if _, err := unicodeStringAt(buf, c.off, c.n); err == nil {
			t.Errorf("off=%d n=%d accepted", c.off, c.n)
		}
	}
}

func TestIsDlvForAddrRejectsSelfWindows(t *testing.T) {
	if isDlvForAddr(context.Background(), os.Getpid(), "127.0.0.1:2345") {
		t.Fatal("test binary is not dlv")
	}
}
