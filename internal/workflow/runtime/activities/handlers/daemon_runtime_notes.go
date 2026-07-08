// Copyright (c) 2025 Reliant Labs
package handlers

import "strings"

// daemonRuntimeProfile describes what a given daemon runtime/sandbox type can
// and cannot do. Only the Cannot lines are surfaced to the model today (as a
// limitation heads-up), but Can is kept so the map stays a single, honest
// source of truth as runtimes evolve.
type daemonRuntimeProfile struct {
	Can    []string
	Cannot []string
}

// daemonRuntimeProfiles is the extensible, type-keyed capability/limitation map.
//
// Extending it is deliberately trivial:
//   - add a new runtime by adding a map entry;
//   - relax a limitation (e.g. kata gaining k3d) by dropping a Cannot line.
//
// Runtime types absent from this map produce no note.
var daemonRuntimeProfiles = map[string]daemonRuntimeProfile{
	"kata": {
		Can: []string{"docker build", "docker run", "docker compose"},
		Cannot: []string{
			"k3d / kind / nested Kubernetes",
			"nested VMs / KVM",
		},
	},
	"gvisor": {
		Cannot: []string{
			"docker / dockerd / k3d (sandboxed runtime, no container runtime available)",
		},
	},
}

// daemonRuntimeLimitationNote returns a short heads-up describing what the
// model cannot do on a daemon of the given runtime type. It returns "" for
// empty, unknown, or unconstrained runtime types so callers can inject
// unconditionally without emitting noise.
func daemonRuntimeLimitationNote(runtimeType string) string {
	rt := strings.ToLower(strings.TrimSpace(runtimeType))
	if rt == "" {
		return ""
	}
	profile, ok := daemonRuntimeProfiles[rt]
	if !ok || len(profile.Cannot) == 0 {
		return ""
	}
	return "NOTE: this daemon runs in a " + rt + " sandbox; you cannot: " +
		strings.Join(profile.Cannot, "; ") + "."
}
