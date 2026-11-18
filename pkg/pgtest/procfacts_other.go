//go:build !windows && !darwin && !linux

package pgtest

// readProcFacts: no supported way to read process identity here, so every
// fact is unreadable and callers fail closed (never signal).
func readProcFacts(int) procFacts { return procFacts{} }
