package cli

// env_status_checks.go — the ENV-RUNTIME half of the doctor split.
//
// `forge doctor` used to answer "is some environment running?" alongside "is
// this project well-formed", and it answered the first one badly: it guessed
// the app was on :8080, missed a project serving on :3091, and rendered the
// miss as a gray dash that looked exactly like "not applicable". The whole
// time, `forge env status <env>` sat next to it resolving the ports
// `forge env up` actually bound.
//
// So the runtime checks moved to the command that already owns the resolver.
// Nothing here discovers a port: runtimeTargetFor reads the SAME rendered
// entities + live-port overlay + probe results the status table was built
// from, and hands doctor a finished address. When it cannot resolve one, the
// dependent check reports UNDETERMINED — never a pass, never a skip.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/doctor"
)

// doctorCompose is how the runtime checks reach `docker compose`. A seam so the
// package's tests never ask the host's docker about the developer's own compose
// stacks; TestMain installs the answer a host with no daemon gives.
var doctorCompose doctor.ComposeRunner = doctor.DockerCompose

// envStatusCheckTimeout bounds the whole runtime-check phase. `env status`
// is a snapshot command a human waits on, so the probes get a short leash;
// an unreachable backend surfaces as a failed check, not a hung terminal.
const envStatusCheckTimeout = 15 * time.Second

// runtimeTargetFor derives the addresses the env-runtime checks probe from
// the rows the status table already resolved.
//
//   - HTTP is the first LISTENING host service in KCL declaration order.
//     "First declared and answering" is a fact about this env, not a guess:
//     the port came from the render + the live-port overlay and was then
//     probed. Service names it, so the report can never be ambiguous about
//     which one replied.
//   - Pprof comes from that same service's inline PPROF_ADDR — serverkit
//     binds pprof on its own listener, so it is never the HTTP port.
//
// Both fields stay empty when nothing is up, which is the honest answer.
//
// RemoteOnly is a fact about the render, not the probe: nothing the env
// declares runs on this machine — the SAME predicate that decides whether it
// gets a port block (entitiesTargetThisMachine), so the two cannot disagree
// about which envs are local. A frontend the developer may still dev-serve
// (`forge env up prod --target reliant-web`) does not make an env local: that
// preview is not the stack the machine-local checks probe, and its own row in
// the status table already reports whether it is listening.
func runtimeTargetFor(e *KCLEntities, rows []upServiceRow) doctor.RuntimeTarget {
	t := doctor.RuntimeTarget{RemoteOnly: e != nil && !entitiesTargetThisMachine(e)}
	for _, r := range rows {
		if r.Kind != "host" || !r.Listening || r.Port <= 0 {
			continue
		}
		t.Service = r.Name
		t.HTTP = "localhost:" + strconv.Itoa(r.Port)
		break
	}
	if t.Service == "" || e == nil {
		return t
	}
	if w := e.FindWorkload(t.Service); w != nil && w.OnRuntime(RuntimeHost) {
		if p := pprofPortFromAddr(w.HostEnv()["PPROF_ADDR"]); p != "" {
			t.Pprof = "localhost:" + p
		}
	}
	return t
}

// pprofPortFromAddr extracts the port from a PPROF_ADDR value, which is a Go
// listen address (":6060", "0.0.0.0:6060", "localhost:6060"). Empty when the
// service declares none or the value carries no port.
func pprofPortFromAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return ""
	}
	port := addr[idx+1:]
	if n, err := strconv.Atoi(port); err != nil || n <= 0 {
		return ""
	}
	return port
}

// runEnvRuntimeChecks runs the env-runtime check set and prints it under the
// status table. Returns the report so the --json path can embed it.
//
// Failures here do NOT fail `forge env status`: the command's contract is to
// REPORT the state of a stack, and "the app is down" is a state it must be
// able to print. Signal validation is the one exception — a mistyped
// --signal is a usage error and must not be swallowed.
func runEnvRuntimeChecks(ctx context.Context, projectName, projectDir, env string, target doctor.RuntimeTarget, signal string, jsonOut, verbose bool) (doctor.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, envStatusCheckTimeout)
	defer cancel()

	d := doctor.New(doctor.Deps{Compose: doctorCompose})
	report, err := d.RunRuntime(ctx, doctor.RuntimeInput{
		ProjectName: projectName,
		ProjectDir:  projectDir,
		Env:         env,
		Target:      target,
		Signal:      signal,
	})
	if err != nil {
		return doctor.Report{}, err
	}
	if jsonOut {
		return report, nil
	}
	fmt.Printf("\n  Runtime checks · env=%s\n\n", env)
	d.PrintReport(os.Stdout, report, verbose)
	return report, nil
}
