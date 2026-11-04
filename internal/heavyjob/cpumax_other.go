// Copyright (c) 2026 Reliant Labs
//go:build !linux

package heavyjob

// limitCPU has no cgroup to cap outside Linux: the share is GOMAXPROCS alone.
func limitCPU(int) string { return "no cgroup v2" }
