// Copyright (c) 2025 Reliant Labs
package daemonpolicy

import (
	"context"
	"slices"
	"testing"
)

// The daemon exports forge's credential helper (pinned to its own session) so
// the user's agent can deploy without a second login. A CONFINED caller — a
// third-party connector — must never inherit it: through it, anything that
// can run a program could mint deploy authority as the user. First-party
// children must.
func TestChildEnv_ForgeCredentialHelperIsFirstPartyOnly(t *testing.T) {
	const helper = `["/opt/reliant/bin/reliant","auth","forge-credential","--daemon-server","https://api.example.com"]`
	t.Setenv("FORGE_CREDENTIAL_HELPER", helper)

	confined := ChildEnv(NewContext(context.Background(), &Policy{GrantID: "grant-1"}), nil)
	if slices.ContainsFunc(confined, func(kv string) bool { return len(kv) >= 24 && kv[:24] == "FORGE_CREDENTIAL_HELPER=" }) {
		t.Fatal("a confined (connector) child inherited forge's credential helper")
	}
	if !slices.Contains(ChildEnv(context.Background(), nil), "FORGE_CREDENTIAL_HELPER="+helper) {
		t.Fatal("a first-party child (the user's agent) must inherit forge's credential helper")
	}
}

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
