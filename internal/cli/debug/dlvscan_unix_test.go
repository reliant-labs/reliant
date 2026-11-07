//go:build !windows

package debug

import (
	"context"
	"os"
	"testing"
)

func TestDlvPSLineMatches(t *testing.T) {
	listen := "--listen=127.0.0.1:2345"
	cases := []struct {
		line string
		pid  int
		ok   bool
	}{
		{"  123 /usr/bin/dlv exec x --listen=127.0.0.1:2345 --headless", 123, true},
		{"123 sleep 100 --listen=127.0.0.1:2345", 0, false},
		{"123 dlv exec x --listen=127.0.0.1:9999", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		pid, ok := dlvPSLineMatches(c.line, listen)
		if ok != c.ok || (ok && pid != c.pid) {
			t.Errorf("%q: got %d,%v", c.line, pid, ok)
		}
	}
}

func TestIsDlvForAddrRejectsNonDlv(t *testing.T) {
	if isDlvForAddr(context.Background(), os.Getpid(), "127.0.0.1:2345") {
		t.Fatal("test binary is not dlv")
	}
	if isDlvForAddr(context.Background(), 0, "x") || isDlvForAddr(context.Background(), os.Getpid(), "") {
		t.Fatal("invalid input must be false")
	}
}
