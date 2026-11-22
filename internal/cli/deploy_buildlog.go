package cli

import (
	"io"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/buildlog"
)

// beginDeployBuildLog starts a writing deploy's quiet build session
// (buildlog): one line per build step, the full output in
// .forge/logs/<env>/deploy.log beside the env's other logs. --verbose, and
// the previews that build nothing, keep streaming. A log file that cannot be
// created is no reason to fail a deploy: it streams instead.
//
// Under --json stdout carries the one document and nothing else, so the
// step lines go to stderr outright, not to whatever os.Stdout is when a step
// ends.
func beginDeployBuildLog(env string, f deployCmdFlags) *buildlog.Session {
	if f.verbose || f.explain || f.dryRun || f.promote.plan || f.promote.planOnly {
		return nil
	}
	out := func() io.Writer { return os.Stdout }
	if f.jsonOut {
		out = func() io.Writer { return os.Stderr }
	}
	session, err := buildlog.Begin(deployLogPath(env), out)
	if err != nil {
		return nil
	}
	return session
}

// deployLogPath is where a deploy's full build output goes.
func deployLogPath(env string) string {
	return filepath.Join(projectDirForKCL(), ".forge", "logs", env, "deploy.log")
}
