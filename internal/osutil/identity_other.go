// Copyright (c) 2026 Reliant Labs
//go:build !linux && !darwin

package osutil

// IdentifyProcess cannot read a process's identity without a process-table
// API: ok is false, and callers treat the process as unknown.
func IdentifyProcess(int) (ProcessIdentity, bool) { return ProcessIdentity{}, false }
