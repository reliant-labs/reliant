// Copyright (c) 2026 Reliant Labs
//go:build !linux

package procguard

// sample cannot count processes without /proc: the guard stays off.
func (g *Guard) sample() (Sample, bool) { return Sample{}, false }
