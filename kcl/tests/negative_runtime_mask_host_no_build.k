"""
Negative test — a go-run/binary/delve host workload with no GoBuild and no
explicit `command`.

The host argv is DERIVED from build + args (ADR 0002 §2; `go run <cmd>
<args>`). With neither a GoBuild nor an explicit command there is nothing to
launch, and the old fallback (`go run ./cmd/<name> server <name>`) guessed a
package path. Refused at render, naming the workload.
"""

# expect: worker

import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_w | {runtime = forge.OnHost {runner = "go-run"}} if not _w.runtime else _w for _w in [fw.Workload {name = "worker", kind = "worker", args = ["worker"]}]]
})
