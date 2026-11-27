// Copyright (c) 2025 Reliant Labs
package gate

import (
	"fmt"
	"regexp"
	"sort"
)

// The gate's filesystem layout. The work volume is mounted at WorkMount and
// holds the tree, HOME and TMPDIR, so everything the suite writes outside the
// nested engine is on one per-gate volume (and under its disk limit) and the
// tree itself stays clean. The engine volume is the nested engine's data-root.
const (
	WorkMount      = "/gate"
	TreeDir        = WorkMount + "/src"
	HomeDir        = WorkMount + "/home"
	TmpDir         = WorkMount + "/tmp"
	EngineDataRoot = "/var/lib/docker"
)

// BaseEnv is the whole environment a gate starts from, before the
// repository's gate.env. Nothing else reaches it: GateEnv builds the list from
// these constants and the Spec, never from this process's environment, and the
// sysbox backend checks the created container holds nothing more
// (ErrEnvInjected).
var BaseEnv = map[string]string{
	"PATH":        "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"HOME":        HomeDir,
	"TMPDIR":      TmpDir,
	"LANG":        "C.UTF-8",
	"TZ":          "UTC",
	"CI":          "true",
	"DOCKER_HOST": "unix:///var/run/docker.sock",
}

// ReservedEnv are the names gate.env may not set: where the gate's files live,
// which engine it talks to, and what identifies the run. PATH, LANG, TZ and CI
// are base values a repository may override.
var ReservedEnv = map[string]bool{
	"HOME":           true,
	"TMPDIR":         true,
	"DOCKER_HOST":    true,
	"RELIANT_GATE":   true,
	"RELIANT_RUN_ID": true,
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// GateEnv is the gate's environment as KEY=VALUE entries, sorted: BaseEnv, the
// run's identity (RELIANT_GATE=1, RELIANT_RUN_ID), then gateEnv on top. Every
// entry carries a value — a bare KEY would let the docker CLI copy this
// process's value in.
func GateEnv(runID string, gateEnv map[string]string) ([]string, error) {
	merged := make(map[string]string, len(BaseEnv)+len(gateEnv)+2)
	for k, v := range BaseEnv {
		merged[k] = v
	}
	merged["RELIANT_GATE"] = "1"
	merged["RELIANT_RUN_ID"] = runID
	for k, v := range gateEnv {
		switch {
		case !envNamePattern.MatchString(k):
			return nil, fmt.Errorf("%w: gate.env name %q is not a valid variable name", ErrInvalidSpec, k)
		case ReservedEnv[k]:
			return nil, fmt.Errorf("%w: gate.env may not set %s", ErrInvalidSpec, k)
		}
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out, nil
}
