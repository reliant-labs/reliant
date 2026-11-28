// Copyright (c) 2026 Reliant Labs

//go:build !linux

package openat2test

import "testing"

// Missing is Linux-only: elsewhere the safe inspection refuses by design,
// and the tests that need it skip on that refusal.
func Missing() error { return nil }

// Require is Linux-only, as Missing is.
func Require(testing.TB) {}
