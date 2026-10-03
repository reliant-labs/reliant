// Copyright (c) 2025 Reliant Labs
package daemonpolicy

import (
	"context"
	"slices"
	"testing"
)

// An agent's shell runs on the daemon, and Electron sets the daemon's own
// connection settings in its environment — in cloud-dev, to the admin server's
// origin. A `reliant` CLI run from that shell inherited them and silently
// targeted the daemon's internal origin instead of its own default (prod).
func TestChildEnv_FirstPartyOmitsDaemonConnectionSettings(t *testing.T) {
	t.Setenv("RELIANT_SERVER_URL", "http://localhost:8090")
	t.Setenv("RELIANT_GATEWAY_URL", "http://localhost:39190")
	t.Setenv("RELIANT_AUTH_URL", "http://localhost:54321")
	t.Setenv("RELIANT_AUTH_KEY", "sb_publishable_dev")
	t.Setenv("RELIANT_TEST_KEEP", "kept")

	env := ChildEnv(context.Background(), map[string]string{"RELIANT_SERVER_URL": "https://explicit.example.com"})

	for _, name := range []string{"RELIANT_GATEWAY_URL", "RELIANT_AUTH_URL", "RELIANT_AUTH_KEY"} {
		if slices.ContainsFunc(env, func(kv string) bool { return len(kv) > len(name) && kv[:len(name)+1] == name+"=" }) {
			t.Errorf("%s leaked from the daemon into a first-party child", name)
		}
	}
	if slices.Contains(env, "RELIANT_SERVER_URL=http://localhost:8090") {
		t.Error("the daemon's own RELIANT_SERVER_URL leaked into a first-party child")
	}
	if !slices.Contains(env, "RELIANT_SERVER_URL=https://explicit.example.com") {
		t.Error("a request-supplied RELIANT_SERVER_URL must still apply")
	}
	if !slices.Contains(env, "RELIANT_TEST_KEEP=kept") {
		t.Error("first-party children must still inherit the rest of the daemon's environment")
	}
}
