// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"strings"
	"testing"
)

func TestDaemonRuntimeLimitationNote_Kata(t *testing.T) {
	note := daemonRuntimeLimitationNote(" Kata ")

	can, cannot, ok := strings.Cut(note, "you cannot:")
	if !ok {
		t.Fatalf("note missing %q section: %q", "you cannot:", note)
	}
	if !strings.Contains(can, "k3d") {
		t.Errorf("k3d must be listed under can: %q", note)
	}
	for _, hint := range []string{"mknod /tmp/kmsg c 1 11", "/tmp/kmsg:/dev/kmsg@server:*", "forge"} {
		if !strings.Contains(can, hint) {
			t.Errorf("k3d hint missing %q: %q", hint, can)
		}
	}
	if !strings.Contains(cannot, "KVM") {
		t.Errorf("nested VMs / KVM must stay in cannot: %q", cannot)
	}
	for _, s := range []string{"k3d", "kind", "Kubernetes"} {
		if strings.Contains(cannot, s) {
			t.Errorf("cannot section must not mention %q: %q", s, cannot)
		}
	}
	if strings.Contains(note, "kind") {
		t.Errorf("kind is unverified and must appear in neither list: %q", note)
	}
}

func TestDaemonRuntimeLimitationNote_UnconstrainedRuntimes(t *testing.T) {
	for _, rt := range []string{"", "  ", "local", "unknown-runtime"} {
		if got := daemonRuntimeLimitationNote(rt); got != "" {
			t.Errorf("runtime %q: want empty note, got %q", rt, got)
		}
	}
}

func TestDaemonRuntimeLimitationNote_Gvisor(t *testing.T) {
	note := daemonRuntimeLimitationNote("gvisor")
	if !strings.HasPrefix(note, "NOTE: this daemon runs in a gvisor sandbox; you cannot:") {
		t.Errorf("gvisor has no Can entries, note should not claim any: %q", note)
	}
}
