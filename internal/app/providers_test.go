// Copyright (c) 2025 Reliant Labs
package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// As a node, the diagnostics listener serves until its context ends and is
// gone after; PPROF_PORT=0 is no node at all.
func TestDebugServerRunsUntilItsContextEnds(t *testing.T) {
	off, err := NewDebugServer(DebugServerDeps{Port: 0})
	require.NoError(t, err)
	require.Nil(t, off, "PPROF_PORT=0 built a listener")
	_, err = NewDebugServer(DebugServerDeps{Port: 70000})
	require.ErrorContains(t, err, "PPROF_PORT")

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	s, err := NewDebugServer(DebugServerDeps{Port: int32(port)})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	url := fmt.Sprintf("http://127.0.0.1:%d/debug/db", port)
	require.Eventually(t, func() bool {
		res, err := http.Get(url)
		if err != nil {
			return false
		}
		_ = res.Body.Close()
		return res.StatusCode == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond, "the debug server never answered")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	if res, err := http.Get(url); err == nil {
		_ = res.Body.Close()
		t.Fatal("the listener still answers after Run returned")
	}
}
