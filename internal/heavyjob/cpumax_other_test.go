// Copyright (c) 2026 Reliant Labs
//go:build !linux

package heavyjob

// inheritedShare is share: outside Linux there is no cap to inherit.
func inheritedShare(share int) int { return share }
