// Copyright (c) 2025 Reliant Labs
package gate

import (
	"fmt"
	"time"
)

// SysboxImageContract is what a gate image for the sysbox backend must hold.
// This package builds and publishes none; a repository names its image in its
// gate configuration (a pinned digest is best).
//
//   - Linux, with a POSIX /bin/sh, mkdir, sleep and tail, run as root (USER 0
//     or unset): under sysbox that root is an unprivileged user on the host.
//   - dockerd, containerd and runc on PATH, with iptables (the nested engine
//     programs its own NAT), and the docker CLI. The docker:<version>-dind
//     images satisfy this.
//   - for k3d: the k3d binary; it runs its nodes on the nested engine.
//   - no ENTRYPOINT the gate relies on: the backend replaces it with the
//     bootstrap below, which starts the engine and then execs the gate's
//     command in the tree.
//   - nothing secret in the image or its ENV: the image's environment is the
//     only environment the gate sees beside GateEnv.
const SysboxImageContract = "sh, dockerd, containerd, runc, iptables, docker CLI; root user; no secret ENV"

// bootstrapScript starts the nested engine with its data-root on the engine
// volume, waits up to engineWait for it to answer, and execs the gate's
// command in the tree. An engine that does not start is the gate's
// infrastructure failing, so it exits 2: unproven.
func bootstrapScript(engineWait time.Duration) string {
	secs := int(engineWait / time.Second)
	if secs < 1 {
		secs = 1
	}
	return fmt.Sprintf(`set -eu
mkdir -p %[1]s %[2]s %[3]s /var/log
log=/var/log/reliant-gate-engine.log
dockerd --data-root=%[3]s --host=unix:///var/run/docker.sock >"$log" 2>&1 &
waited=0
until docker version >/dev/null 2>&1; do
	if [ "$waited" -ge %[4]d ]; then
		echo "reliant gate: the nested container engine did not start within %[4]ds; the end of its log:" >&2
		tail -n 40 "$log" >&2 || true
		exit 2
	fi
	waited=$((waited + 1))
	sleep 1
done
cd %[5]s
exec "$@"
`, HomeDir, TmpDir, EngineDataRoot, secs, TreeDir)
}
