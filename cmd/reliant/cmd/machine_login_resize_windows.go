// Copyright (c) 2026 Reliant Labs
//go:build windows

package cmd

// onTerminalResize: Windows consoles send no SIGWINCH, so the remote session
// keeps the size it started at.
func onTerminalResize(func()) (stop func()) { return func() {} }
