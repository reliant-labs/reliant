// Copyright (c) 2026 Reliant Labs
//go:build !linux

package heavyjob

// cgroupBounds has no cgroup to lower outside Linux: a slotted command runs
// at the weight and with the memory every other command has.
type cgroupBounds struct{}

func lowerBounds(bounds) *cgroupBounds { return &cgroupBounds{} }

func (*cgroupBounds) release() {}
