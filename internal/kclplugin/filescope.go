package kclplugin

import (
	"fmt"
	"os"
)

// fileScopeEnv is the KCL runtime's switch for confining the path-taking
// `file.*` builtins (read, readbase64, glob, exists, abs, write, …). The
// runtime reads it from the process environment on every call.
const fileScopeEnv = "KCL_FILE_SCOPE"

// unscopeFileBuiltins turns the KCL runtime's file scoping off for this
// process, unless the user set KCL_FILE_SCOPE themselves.
//
// # Why forge opts out
//
// KCL v0.13.1 (kcl-lang/kcl#2189) resolves every relative `file.*` path
// against the directory of the .k file making the call, and refuses any path
// outside that directory. Forge's contract is the opposite: a render runs with
// the PROJECT ROOT as its working directory, so `file.read("deploy/...")`
// means the same file from every .k in the project (kcleval.Eval,
// kclrender.RunInWorkDir). Real projects depend on it — control-plane's
// deploy/kcl/lib reads deploy/cnpg, deploy/openbao and internal/obstenant by
// project-relative path, and its dev env reaches a sibling checkout with
// file.abs("../reliant"). Under the new default every one of those reads
// resolves under deploy/kcl/lib/ instead and fails on a missing file.
//
// Upstream offers no way to move the scope root to the project, only to turn
// scoping off, which restores exactly the semantics every KCL release before
// v0.13.1 had. TestEvalReadsFilesRelativeToTheProjectRoot
// (internal/kcleval) fails if a KCL upgrade breaks this again.
//
// An explicit KCL_FILE_SCOPE is left alone: a project that wants upstream's
// file-relative, confined reads can export KCL_FILE_SCOPE=on.
//
// It runs before the native client exists, so no KCL evaluation can be
// reading the environment while it is written. The variable is therefore in
// forge's environment for the rest of the process, and child processes
// inherit it; only a `kcl` binary reads it.
func unscopeFileBuiltins() error {
	if _, set := os.LookupEnv(fileScopeEnv); set {
		return nil
	}
	if err := os.Setenv(fileScopeEnv, "off"); err != nil {
		return fmt.Errorf("set %s=off for the KCL runtime: %w", fileScopeEnv, err)
	}
	return nil
}
