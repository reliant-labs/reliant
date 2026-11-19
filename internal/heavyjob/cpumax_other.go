// Copyright (c) 2026 Reliant Labs
//go:build !linux

package heavyjob

import "os/exec"

// cpuCap has no cgroup to cap outside Linux: the share is GOMAXPROCS alone.
type cpuCap struct{ share int }

func limitCPU(share int) *cpuCap { return &cpuCap{share: share} }

func (*cpuCap) into(*exec.Cmd) bool { return false }

func (*cpuCap) release() {}
