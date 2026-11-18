// Copyright (c) 2026 Reliant Labs
//go:build !linux

package credsandbox

import (
	"errors"
	"os"
)

// No credentialed command gets SignalEnv outside Linux, so none gets a scratch
// directory of its own either (stepscratch.go); these are never reached.

func makeStepScratch(string) (string, error) {
	return "", errors.New("the credentials sandbox is implemented for Linux only")
}

// SweepStepScratch does nothing outside Linux: no command has a scratch
// directory of its own to leave behind.
func SweepStepScratch() {}

func stepScratchRefs(string) (map[string]bool, error) {
	return nil, errors.New("no /proc to prove a scratch directory unheld")
}

func removeTree(dir string) error { return os.RemoveAll(dir) }
