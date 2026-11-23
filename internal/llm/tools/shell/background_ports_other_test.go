// Copyright (c) 2025 Reliant Labs
//go:build !windows && !linux

package shell

// wantPortScanForks is how many `ps` and `lsof` processes one port scan
// starts: one of each off Linux.
const wantPortScanForks = 1
