// Copyright (c) 2026 Reliant Labs
//go:build !windows

package cmd

import (
	"os"
	"os/signal"
	"syscall"
)

// onTerminalResize calls fn on every SIGWINCH until stop is called.
func onTerminalResize(fn func()) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ch:
				fn()
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
