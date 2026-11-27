// Copyright (c) 2026 Reliant Labs

//go:build !linux

package idlereclaim

import "github.com/reliant-labs/reliant/internal/diskwatch"

// statVolume reads no volume off Linux: the regenerable inventory the strip
// needs is Linux-only too (worktreereclaim.InspectRegenerable), so a pressure
// pass there would hold everything anyway.
func statVolume(string) (diskwatch.Usage, bool) { return diskwatch.Usage{}, false }
