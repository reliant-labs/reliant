// Copyright (c) 2025 Reliant Labs
//go:build !linux

package osutil

// AdjustChildOOMScore is a no-op on non-Linux platforms: oom_score_adj is a
// Linux /proc interface and macOS/Windows have no equivalent OOM-victim
// steering knob. The daemon also runs on developer Macs, where memory
// pressure is handled by the OS and no cgroup limit applies.
func AdjustChildOOMScore(pid int) error {
	return nil
}
