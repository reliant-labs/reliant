// Copyright (c) 2025 Reliant Labs

// The package's surface is Check and Validate, which take their collaborators
// per call: the run's machine and the run owner's credentials. There is no
// long-lived component to construct, so this contract declares those
// collaborators.
//
//forge:not-a-component: functions only — the preflight-requirements activity calls Check with a per-run Machine and Credentials; nothing constructs it
package requirements

import "context"

// Machine runs one shell command on the run's machine, in the project root.
// err is a transport failure (the command may not have run), never a
// non-zero exit.
type Machine interface {
	Run(ctx context.Context, command string) (stdout string, exitCode int, err error)
}

// Credentials says whether an integration's connection resolves, authorized,
// for this run: nil when it does (or the integration needs none), else an
// error whose text the owner is shown.
type Credentials interface {
	Check(ctx context.Context, integration string) error
}
