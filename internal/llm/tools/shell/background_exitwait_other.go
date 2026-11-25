// Copyright (c) 2026 Reliant Labs
//go:build !linux && !darwin

package shell

import "errors"

// fileOutputSupported is false: there is no in-place restart to outlive here
// (Windows replaces the daemon with a new process), so background processes
// keep their output pipes.
const fileOutputSupported = false

func waitExited(int) error { return errors.ErrUnsupported }
