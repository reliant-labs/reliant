// Copyright (c) 2025 Reliant Labs
package daemonpolicy

import (
	"context"
	"slices"
	"testing"

	"github.com/reliant-labs/forge/pkg/forgepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// The daemon exports forge's credential helper (pinned to its own session) so
// the user's agent can deploy without a second login. A CONFINED caller — a
// third-party connector — must never inherit it: through it, anything that
// can run a program could mint deploy authority as the user. First-party
// children must.
func TestChildEnv_ForgeCredentialHelperIsFirstPartyOnly(t *testing.T) {
	const helper = `["/opt/reliant/bin/reliant","auth","forge-credential","--daemon-server","https://api.example.com"]`
	t.Setenv("FORGE_CREDENTIAL_HELPER", helper)

	confined := ChildEnv(NewContext(context.Background(), &Policy{GrantID: "grant-1"}), nil, CredentialsInherit)
	if slices.ContainsFunc(confined, func(kv string) bool { return len(kv) >= 24 && kv[:24] == "FORGE_CREDENTIAL_HELPER=" }) {
		t.Fatal("a confined (connector) child inherited forge's credential helper")
	}
	if !slices.Contains(ChildEnv(context.Background(), nil, CredentialsInherit), "FORGE_CREDENTIAL_HELPER="+helper) {
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

	env := ChildEnv(context.Background(), map[string]string{"RELIANT_SERVER_URL": "https://explicit.example.com"}, CredentialsInherit)

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

// The incident (#2823): a cloud machine's daemon carries DAEMON_DATA_DIR,
// DAEMON_GRPC_URL and RELIANT_DAEMON_TYPE, and a `reliant daemon start` an
// agent ran in its shell inherited them and wrote the host daemon's log and
// runtime record. A command gets none of the daemon's identity; it keeps
// RELIANT_DAEMON_TYPE, which the CLI reads to recognise a cloud machine.
func TestChildEnv_FirstPartyOmitsDaemonIdentity(t *testing.T) {
	for name := range daemonIdentityEnvVars {
		t.Setenv(name, "host-daemon")
	}
	t.Setenv("RELIANT_DAEMON_TYPE", "managed")

	env := ChildEnv(context.Background(), map[string]string{"DAEMON_DATA_DIR": "/tmp/explicit"}, CredentialsInherit)

	for name := range daemonIdentityEnvVars {
		if slices.Contains(env, name+"=host-daemon") {
			t.Errorf("the daemon's own %s leaked into a first-party child", name)
		}
	}
	if !slices.Contains(env, "DAEMON_DATA_DIR=/tmp/explicit") {
		t.Error("a request-supplied DAEMON_DATA_DIR must still apply")
	}
	if !slices.Contains(env, "RELIANT_DAEMON_TYPE=managed") {
		t.Error("RELIANT_DAEMON_TYPE must reach a first-party child: the CLI reads it to recognise a cloud machine")
	}
}

// daemonConfigEnvPassedThrough are the DaemonConfig variables a command still
// inherits, each for a reason. Anything else DaemonConfig reads is the
// daemon's identity and belongs in daemonIdentityEnvVars.
var daemonConfigEnvPassedThrough = map[string]string{
	"RELIANT_DAEMON_TYPE": "the CLI reads it to recognise a cloud machine (isManagedMachine, loopbackUnreachable)",
	// Generic names: a user's own programs read these too, and the daemon
	// does not own them.
	"TLS_CERT_FILE": "generic name a user's programs use",
	"TLS_KEY_FILE":  "generic name a user's programs use",
	"PROJECT_PATH":  "generic name a user's programs use",
	// Machine-wide policy, not which daemon this is: a daemon started on the
	// same machine is meant to apply the same limits.
	"RELIANT_CHILD_PRIORITY":                    "machine policy",
	"RELIANT_WORKTREE_DIR":                      "machine policy",
	"RELIANT_CHROME_BROWSER_URL":                "machine policy",
	"RELIANT_DISK_RECLAIM_AT":                   "machine policy",
	"RELIANT_DISK_REFUSE_AT":                    "machine policy",
	"RELIANT_HEAVY_COMMANDS":                    "machine policy",
	"RELIANT_HEAVY_SHARE":                       "machine policy",
	"RELIANT_HEAVY_CPU_WEIGHT":                  "machine policy",
	"RELIANT_HEAVY_MEMORY_HIGH":                 "machine policy",
	"RELIANT_COMMAND_THREADS":                   "machine policy",
	"RELIANT_BACKGROUND_IDLE_REAP":              "machine policy",
	"RELIANT_FETCH_ALLOW_PRIVATE_NETWORK":       "machine policy",
	"RELIANT_DAEMON_EGRESS_USER_PER_MINUTE":     "machine policy",
	"RELIANT_DAEMON_EGRESS_USER_BURST":          "machine policy",
	"RELIANT_DAEMON_EGRESS_USER_MAX_CONCURRENT": "machine policy",
	"RELIANT_DAEMON_EGRESS_HOST_MAX_CONCURRENT": "machine policy",
}

// A variable added to `daemon start`'s config must be decided: is it the
// daemon's identity, which a command must not inherit, or something a command
// may share? Without this, a new DAEMON_* setting would leak into every
// agent's shell by default.
func TestDaemonIdentityEnvCoversDaemonConfig(t *testing.T) {
	read := map[string]bool{}
	var walk func(protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			f := fields.Get(i)
			if opts, _ := proto.GetExtension(f.Options(), forgepb.E_Config).(*forgepb.ConfigFieldOptions); opts != nil {
				names := []string{opts.GetEnvVar()}
				for _, alias := range opts.GetEnvVarAliases() {
					names = append(names, alias.GetName())
				}
				for _, name := range names {
					if name != "" {
						read[name] = true
					}
				}
			}
			if f.Kind() == protoreflect.MessageKind && f.Message().FullName().Parent() == md.FullName().Parent() {
				walk(f.Message())
			}
		}
	}
	walk((&configv1.DaemonConfig{}).ProtoReflect().Descriptor())
	if !read["DAEMON_DATA_DIR"] {
		t.Fatal("walked DaemonConfig and found no DAEMON_DATA_DIR: the walk is broken")
	}

	for name := range read {
		_, passed := daemonConfigEnvPassedThrough[name]
		switch {
		case daemonIdentityEnvVars[name] && passed:
			t.Errorf("%s is both stripped and passed through", name)
		case !daemonIdentityEnvVars[name] && !passed:
			t.Errorf("DaemonConfig reads %s: add it to daemonIdentityEnvVars (env.go) if it says which daemon this is, else to daemonConfigEnvPassedThrough with the reason", name)
		}
	}
	for name := range daemonIdentityEnvVars {
		if !read[name] {
			t.Errorf("daemonIdentityEnvVars lists %s, which DaemonConfig no longer reads", name)
		}
	}
}

// Every process an agent's shell starts carries the marker the `reliant` CLI
// reads to refuse enabling automations, whether the caller is first-party or
// confined, and a request cannot clear it.
func TestChildEnv_MarksAgentShells(t *testing.T) {
	confined := NewContext(context.Background(), &Policy{GrantID: "grant-1"})
	for name, ctx := range map[string]context.Context{"first-party": context.Background(), "confined": confined} {
		if !slices.Contains(ChildEnv(ctx, nil, CredentialsInherit), AgentShellEnv+"=1") {
			t.Errorf("%s: child env lacks %s", name, AgentShellEnv)
		}
		if !slices.Contains(ChildEnv(ctx, map[string]string{AgentShellEnv: ""}, CredentialsInherit), AgentShellEnv+"=1") {
			t.Errorf("%s: a request cleared %s", name, AgentShellEnv)
		}
	}
}
