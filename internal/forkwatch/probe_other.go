// Copyright (c) 2026 Reliant Labs
//go:build !unix

package forkwatch

import "context"

// refused: there is no process table to fill here that EAGAIN reports.
func refused(error) bool { return false }

// defaultProbe: none, so the watchdog is off.
func defaultProbe() func(ctx context.Context) error { return nil }
