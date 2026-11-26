// Copyright (c) 2025 Reliant Labs

//go:build !unix

package daemonpolicy

// Without Unix ownership and mode checks we cannot establish a trusted chain.
// Keep GH_CONFIG_DIR neutral rather than trusting a shared writable directory.
func trustedGHConfigParent(string) bool { return false }
