// Copyright (c) 2025 Reliant Labs

//go:build !linux && !darwin

package memtemp

// No tmpfs to find: Dir falls back to t.TempDir().
func usable(string) bool { return false }

// alive is never asked: usable is false, so Dir never reaps.
func alive(int) bool { return true }
