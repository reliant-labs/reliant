package cli

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// ONE TOKEN (ADR-0003 F3). These pin the property the whole slice exists for:
// a push to the platform registry authenticates with the SAME `rlat_` forge
// reaches the control plane with, from whichever of its four sources holds
// one, and the author passes nothing.
//
// Every test here drives the FAKE dockerLogin seam. A real `docker login`
// against a real registry would be a network call in a unit test and a
// credential write into the developer's own keychain.

const platformHost = "registry.reliantapi.com"

// hostedPlatformFixture is a hosted env whose control plane declares an
// organization, so a BARE hosted image resolves under the platform's push
// base — which is the shape every hosted project has after `forge env new
// --bind <x>=hosted`. registryHost overrides forge's default when non-empty.
func hostedPlatformFixture(registryHost, image string) string {
	host := ""
	if registryHost != "" {
		host += `,
      "registry_host": "` + registryHost + `"`
	}
	return `{
  "output": {
    "control_plane": {
      "type": "control_plane",
      "endpoint": "https://cp.example.com",
      "token_env": "FORGE_CONTROL_PLANE_TOKEN"` + host + `
    },
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "image": "` + image + `",
        "build": {
          "type": "go",
          "cmd": "./cmd/pt",
          "output_name": "pt"
        },
        "runtime": {
          "type": "hosted"
        },
        "spec": {
          "kind": "service",
          "image": "` + image + `"
        }
      }
    ]
  }
}`
}

// loginAttempt is one recorded call at the dockerLogin seam: the ARGV forge
// would have handed the subprocess, and what it wrote to stdin.
type loginAttempt struct {
	args     []string
	password string
}

// stubDockerLoginArgs records the full argv, which is what the no-token-in-
// argv assertion needs. stubDockerLogin (registry_cmd_test.go) keeps the
// host/username shape the older tests read.
func stubDockerLoginArgs(t *testing.T) *[]loginAttempt {
	t.Helper()
	resetPlatformLogins(t)
	var calls []loginAttempt
	prev := dockerLogin
	dockerLogin = func(_ context.Context, args []string, password io.Reader) error {
		b, _ := io.ReadAll(password)
		calls = append(calls, loginAttempt{args: args, password: string(b)})
		return nil
	}
	t.Cleanup(func() { dockerLogin = prev })
	return &calls
}

// resetPlatformLogins clears the per-process memo. Tests share one process,
// so without this the SECOND test to push to a host would observe the first
// test's login and assert on nothing.
func resetPlatformLogins(t *testing.T) {
	t.Helper()
	platformLogins.Range(func(k, _ any) bool { platformLogins.Delete(k); return true })
	t.Cleanup(func() {
		platformLogins.Range(func(k, _ any) bool { platformLogins.Delete(k); return true })
	})
}

// isolatedCredentials points forge's credentials file and the declared token
// env at this test's own state, so a developer's real `forge login` neither
// leaks into an assertion nor is read by one.
func isolatedCredentials(t *testing.T) string {
	t.Helper()
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(cloud.DefaultTokenEnv, "")
	path, err := cloud.CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRegistryLogin_PlatformHostUsesTheControlPlaneCredential is the headline:
// our host takes username `forge` and the control-plane `rlat_` on stdin, from
// EVERY source cloud.ResolveCredential honours — including the credential
// helper, which is how a Reliant session (laptop or managed daemon) reaches a
// push with no `forge login` and no CI secret (gap G7).
func TestRegistryLogin_PlatformHostUsesTheControlPlaneCredential(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
		// arrange installs the credential wherever this source lives.
		arrange func(t *testing.T, credPath string)
		// extra args the invocation carries (the --token case).
		args []string
	}{
		{
			name:    "--token flag",
			token:   "rlat_fromflag",
			arrange: func(*testing.T, string) {},
			args:    []string{"--token", "rlat_fromflag"},
		},
		{
			name:    "the declared token_env",
			token:   "rlat_fromenv",
			arrange: func(t *testing.T, _ string) { t.Setenv("FORGE_CONTROL_PLANE_TOKEN", "rlat_fromenv") },
		},
		{
			name:  "the forge login credentials file",
			token: "rlat_fromlogin",
			arrange: func(t *testing.T, credPath string) {
				if err := credentials.Store(credPath, "https://cp.example.com", cloud.ClientID,
					credentials.Credential{Token: "rlat_fromlogin", CreatedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			// THE HOST-SESSION PATH (Reliant, laptop or cloud daemon). The
			// user is signed in to the host, which names a credential helper;
			// it has no way to be handed a registry password, so the helper's
			// token resolving here is what makes a push work with no `forge
			// login` and no CI secret.
			name:  "a host application's session via the credential helper",
			token: "rlat_fromhelper",
			arrange: func(t *testing.T, _ string) {
				useFakeCredentialHelper(t, "rlat_fromhelper")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planProject(t, hostedPlatformFixture("", "api"))
			credPath := isolatedCredentials(t)
			tc.arrange(t, credPath)
			calls := stubDockerLoginArgs(t)

			args := append([]string{"login", "prod"}, tc.args...)
			if _, err := runRegistryCommand(t, "", args...); err != nil {
				t.Fatalf("forge registry login prod: %v", err)
			}
			if len(*calls) != 1 {
				t.Fatalf("docker login calls = %+v, want exactly one to the platform registry", *calls)
			}
			got := (*calls)[0]
			if strings.TrimSpace(got.password) != tc.token {
				t.Errorf("stdin carried %q, want the %s credential %q", got.password, tc.name, tc.token)
			}
			if !strings.Contains(strings.Join(got.args, " "), platformHost) {
				t.Errorf("argv = %q, want it to name %s", got.args, platformHost)
			}
			if !containsArg(got.args, platformRegistryUsername) {
				t.Errorf("argv = %q, want --username %s (the realm authenticates on the password alone)",
					got.args, platformRegistryUsername)
			}
		})
	}
}

// TestRegistryLogin_TokenNeverReachesArgv is the SABOTAGE-verified security
// property: the credential goes on stdin and nowhere else.
//
// `--password <value>` would work — docker accepts it — and would also publish
// the secret to every process on the box through `ps` for the life of the
// call, plus any CI trace that echoed the command. Switching dockerLoginArgs
// to emit `--password <token>` turns this test red; see the PR body for the
// recorded run.
func TestRegistryLogin_TokenNeverReachesArgv(t *testing.T) {
	planProject(t, hostedPlatformFixture("", "api"))
	isolatedCredentials(t)
	t.Setenv("FORGE_CONTROL_PLANE_TOKEN", "rlat_supersecret")
	calls := stubDockerLoginArgs(t)

	if _, err := runRegistryCommand(t, "", "login", "prod"); err != nil {
		t.Fatalf("forge registry login prod: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("docker login calls = %+v, want one", *calls)
	}
	got := (*calls)[0]
	for _, arg := range got.args {
		if strings.Contains(arg, "rlat_supersecret") {
			t.Fatalf("the credential is in argv (%q) — it is world-readable through `ps` there; it belongs on stdin only", got.args)
		}
	}
	if !containsArg(got.args, "--password-stdin") {
		t.Errorf("argv = %q, want --password-stdin — the only form that cannot leak the credential", got.args)
	}
	if strings.TrimSpace(got.password) != "rlat_supersecret" {
		t.Errorf("stdin carried %q, want the credential", got.password)
	}
}

// TestRegistryLogin_PlatformHostRefusesExplicitFlags: honouring a hand-passed
// credential for our own host would teach the author that it has a password of
// its own, and the next CI job they wrote would carry one.
func TestRegistryLogin_PlatformHostRefusesExplicitFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"--password-stdin":       {"login", "prod", "--username", "u", "--password-stdin"},
		"--password-env":         {"login", "prod", "--username", "u", "--password-env", "SOME_TOKEN"},
		"--username on its own":  {"login", "prod", "--username", "u"},
		"--password-stdin alone": {"login", "prod", "--password-stdin"},
	} {
		t.Run(name, func(t *testing.T) {
			planProject(t, hostedPlatformFixture("", "api"))
			isolatedCredentials(t)
			t.Setenv("FORGE_CONTROL_PLANE_TOKEN", "rlat_x")
			calls := stubDockerLoginArgs(t)

			_, err := runRegistryCommand(t, "whatever", args...)
			if err == nil {
				t.Fatalf("forge registry %s: want a refusal — our registry takes the control-plane credential", strings.Join(args, " "))
			}
			if !strings.Contains(err.Error(), "control-plane credential") || !strings.Contains(err.Error(), platformHost) {
				t.Errorf("the refusal must name the host and say what it takes instead; got:\n%v", err)
			}
			if len(*calls) != 0 {
				t.Errorf("a refused invocation must not log in; calls = %+v", *calls)
			}
		})
	}
}

// TestRegistryLogin_ForeignHostStillRequiresItsFlags: nothing changed for a
// registry the AUTHOR chose. forge holds no credential for it and must not
// invent one.
func TestRegistryLogin_ForeignHostStillRequiresItsFlags(t *testing.T) {
	planProject(t, declaredRegistryFixture)
	isolatedCredentials(t)
	calls := stubDockerLoginArgs(t)

	_, err := runRegistryCommand(t, "", "login", "prod")
	if err == nil {
		t.Fatal("login to a foreign registry with no credential: want a usage error")
	}
	if !strings.Contains(err.Error(), "--password-stdin") {
		t.Errorf("the error must name how to pass the credential; got:\n%v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("no credential: must not log in; calls = %+v", *calls)
	}

	// With the flags it logs in exactly as it always did.
	if _, err := runRegistryCommand(t, "s3cret", "login", "prod", "--username", "ci-bot", "--password-stdin"); err != nil {
		t.Fatalf("login to a foreign registry with its flags: %v", err)
	}
	if len(*calls) != 1 || !containsArg((*calls)[0].args, "registry.example") || !containsArg((*calls)[0].args, "ci-bot") {
		t.Fatalf("calls = %+v, want one login to registry.example as ci-bot", *calls)
	}
}

// TestRegistryLogin_MixedEnvLogsInToBoth: an env that pushes to our registry
// AND somebody else's authenticates each with the credential that is its own,
// in one run. Two logins, two credentials, one command.
func TestRegistryLogin_MixedEnvLogsInToBoth(t *testing.T) {
	planProject(t, mixedRegistryFixture())
	isolatedCredentials(t)
	t.Setenv("FORGE_CONTROL_PLANE_TOKEN", "rlat_platform")
	calls := stubDockerLoginArgs(t)

	if _, err := runRegistryCommand(t, "ghcr-pat", "login", "prod", "--username", "ci-bot", "--password-stdin"); err != nil {
		t.Fatalf("forge registry login prod (mixed): %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("docker login calls = %+v, want two — one per host", *calls)
	}
	byHost := map[string]loginAttempt{}
	for _, c := range *calls {
		switch {
		case containsArg(c.args, platformHost):
			byHost[platformHost] = c
		case containsArg(c.args, "ghcr.io"):
			byHost["ghcr.io"] = c
		}
	}
	ours, ok := byHost[platformHost]
	if !ok {
		t.Fatalf("no login to %s; calls = %+v", platformHost, *calls)
	}
	if strings.TrimSpace(ours.password) != "rlat_platform" || !containsArg(ours.args, platformRegistryUsername) {
		t.Errorf("the platform login must use the control-plane credential as %q; got %+v", platformRegistryUsername, ours)
	}
	theirs, ok := byHost["ghcr.io"]
	if !ok {
		t.Fatalf("no login to ghcr.io; calls = %+v", *calls)
	}
	if strings.TrimSpace(theirs.password) != "ghcr-pat" || !containsArg(theirs.args, "ci-bot") {
		t.Errorf("the foreign login must use the passed credential as ci-bot; got %+v", theirs)
	}
}

// TestRegistryLogin_MixedEnvMissingForeignFlagsNamesTheHost: the author ran a
// command that is correct for our half and incomplete for the other, so the
// refusal names WHICH registry still needs a credential. A bare "--username
// is required" would be read as applying to the host they just logged in to
// without one.
func TestRegistryLogin_MixedEnvMissingForeignFlagsNamesTheHost(t *testing.T) {
	planProject(t, mixedRegistryFixture())
	isolatedCredentials(t)
	t.Setenv("FORGE_CONTROL_PLANE_TOKEN", "rlat_platform")
	calls := stubDockerLoginArgs(t)

	_, err := runRegistryCommand(t, "", "login", "prod")
	if err == nil {
		t.Fatal("a mixed env with no foreign credential: want a refusal naming the foreign host")
	}
	if !strings.Contains(err.Error(), "ghcr.io") {
		t.Errorf("the refusal must name the registry whose credential is missing; got:\n%v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("a refused invocation must log in to neither host; calls = %+v", *calls)
	}
}

// mixedRegistryFixture is a hosted env with one workload under the platform's
// push base (bare image) and one on the author's own registry.
func mixedRegistryFixture() string {
	return `{
  "output": {
    "control_plane": {
      "type": "control_plane",
      "endpoint": "https://cp.example.com",
      "token_env": "FORGE_CONTROL_PLANE_TOKEN"
    },
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "image": "api",
        "build": {"type": "go", "cmd": "./cmd/pt", "output_name": "pt"},
        "runtime": {"type": "hosted"},
        "spec": {"kind": "service", "image": "api"}
      },
      {
        "name": "worker",
        "kind": "service",
        "image": "ghcr.io/acme/worker",
        "build": {"type": "go", "cmd": "./cmd/pt", "output_name": "pt"},
        "runtime": {"type": "hosted"},
        "spec": {"kind": "service", "image": "ghcr.io/acme/worker"}
      }
    ]
  }
}`
}

// TestPlatformRegistryHost: the host comes from the DECLARATION, defaulted the
// same way PushBase defaults it, and is "" for an env with no control plane —
// which is what keeps a cluster-only env from ever resolving a credential.
func TestPlatformRegistryHost(t *testing.T) {
	for name, tc := range map[string]struct {
		entities *KCLEntities
		want     string
	}{
		"no entities":         {nil, ""},
		"no control plane":    {&KCLEntities{}, ""},
		"declared host":       {&KCLEntities{ControlPlane: &ControlPlaneEntity{RegistryHost: "reg.test"}}, "reg.test"},
		"trailing slash":      {&KCLEntities{ControlPlane: &ControlPlaneEntity{RegistryHost: "reg.test/"}}, "reg.test"},
		"defaulted host":      {&KCLEntities{ControlPlane: &ControlPlaneEntity{}}, platformHost},
		"no org still a host": {&KCLEntities{ControlPlane: &ControlPlaneEntity{RegistryHost: "reg.test"}}, "reg.test"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := platformRegistryHost(tc.entities); got != tc.want {
				t.Errorf("platformRegistryHost = %q, want %q", got, tc.want)
			}
		})
	}
}
