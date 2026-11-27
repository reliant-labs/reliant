// Copyright (c) 2026 Reliant Labs
package cliupdate

import (
	"os"
	"testing"

	"github.com/reliant-labs/forge/pkg/selfupdate"
)

// containerMarkers are the files forge's selfupdate container probe stats:
// Docker's and Podman's. Any of them makes selfupdate refuse every binary as
// the container's (Managed), before its path heuristics run, in
// selfupdate.Detect and in the Updater's own detection alike.
var containerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

// plainHost makes selfupdate's container probe see a machine that is not a
// container, so a test of what it decides elsewhere is hermetic: it hides
// the kubelet's KUBERNETES_SERVICE_HOST from this test. A container engine's
// marker file is a real file selfupdate stats at a fixed path, and no
// selfupdate option replaces the probe, so on such a host the test skips,
// naming the file, rather than failing on where it ran (a merge queue gate
// runs in a container).
func plainHost(t *testing.T) {
	t.Helper()
	t.Setenv(selfupdate.KubernetesServiceHostEnv, "")
	if m, ok := containerMarker(); ok {
		t.Skipf("skip: %s exists: forge's selfupdate refuses every binary in a container before the rule this test checks, and has no seam to replace its container probe", m)
	}
}

func containerMarker() (string, bool) {
	for _, m := range containerMarkers {
		if _, err := os.Stat(m); err == nil {
			return m, true
		}
	}
	return "", false
}
