// Copyright (c) 2025 Reliant Labs
//go:build !linux

package pid1

// Init is a no-op off Linux: PID namespaces, and so a reliant that is PID 1,
// exist only there.
func Init() {}
