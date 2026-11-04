// Copyright (c) 2025 Reliant Labs
package handlers

import "strings"

// daemonRuntimeProfile describes what a given daemon runtime/sandbox type can
// and cannot do. Both lists are surfaced to the model as a heads-up; Can
// entries may carry a short usage hint.
type daemonRuntimeProfile struct {
	Can    []string
	Cannot []string
}

// daemonRuntimeProfiles is the extensible, type-keyed capability/limitation map.
//
// Extending it is deliberately trivial:
//   - add a new runtime by adding a map entry;
//   - relax a limitation by moving its line from Cannot to Can.
//
// Runtime types absent from this map produce no note.
var daemonRuntimeProfiles = map[string]daemonRuntimeProfile{
	"kata": {
		Can: []string{
			"docker build", "docker run", "docker compose",
			"k3d (single- and multi-node clusters; networking and NetworkPolicy work). " +
				"On a workspace without /dev/kmsg, kubelet dies and `k3d cluster create` hangs: " +
				"run `sudo mknod /tmp/kmsg c 1 11` first and add " +
				"`--volume '/tmp/kmsg:/dev/kmsg@server:*'` (`@all` for agents too) to `k3d cluster create`. " +
				"Clusters created by forge handle this automatically",
		},
		Cannot: []string{
			"nested VMs / KVM (no /dev/kvm)",
		},
	},
	"gvisor": {
		Cannot: []string{
			"docker / dockerd / k3d (sandboxed runtime, no container runtime available)",
		},
	},
}

// daemonRuntimeLimitationNote returns a short heads-up describing what the
// model can and cannot do on a daemon of the given runtime type. It returns ""
// for empty, unknown, or unconstrained runtime types (no Cannot entries) so
// callers can inject unconditionally without emitting noise.
func daemonRuntimeLimitationNote(runtimeType string) string {
	rt := strings.ToLower(strings.TrimSpace(runtimeType))
	if rt == "" {
		return ""
	}
	profile, ok := daemonRuntimeProfiles[rt]
	if !ok || len(profile.Cannot) == 0 {
		return ""
	}
	note := "NOTE: this daemon runs in a " + rt + " sandbox;"
	if len(profile.Can) > 0 {
		note += " you can: " + strings.Join(profile.Can, "; ") + ";"
	}
	return note + " you cannot: " + strings.Join(profile.Cannot, "; ") + "."
}
