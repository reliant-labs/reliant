// Copyright (c) 2026 Reliant Labs

//go:build !linux

package daemon

// waitExitNoReap is never reached off Linux: hot swap is Linux only, so no
// command runs the swappable way. Returning lets cmd.Wait do the waiting.
func waitExitNoReap(int) error { return nil }
