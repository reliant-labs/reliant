package sbom

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const platformSecret = "rlat_s3cr3t-control-plane-token"

// platformAuth is forge's own identity for one registry, the way the CLI
// wires the platform registry: a credential for that host, nothing for any
// other.
func platformAuth(host string) RegistryAuth {
	return func(_ context.Context, h string) (RegistryCredential, bool, error) {
		if h != host {
			return RegistryCredential{}, false, nil
		}
		return RegistryCredential{Username: "forge", Password: platformSecret}, true, nil
	}
}

func TestGenerate_RegistryCredentialReachesOnlyItsHostsRegistryReads(t *testing.T) {
	t.Parallel()
	bin := writeFile(t, t.TempDir(), "cli", "a binary")
	fake := &fakeSyft{fail: map[string]error{"docker": errors.New("No such image")}}
	g := &Generator{Tool: pinnedTool, Runner: fake, RegistryAuth: platformAuth("registry.example.com")}
	results := g.Generate(context.Background(), []Target{
		{Name: "hosted", Kind: KindRegistry, Ref: "registry.example.com/org/proj/api@" + imageDigest, Subject: imageDigest},
		{Name: "built", Kind: KindImage, Ref: "registry.example.com/org/proj/web@" + imageDigest, Subject: imageDigest},
		{Name: "ghcr", Kind: KindRegistry, Ref: "ghcr.io/acme/api@" + imageDigest, Subject: imageDigest},
		{Name: "hub", Kind: KindRegistry, Ref: "library/redis@" + imageDigest, Subject: imageDigest},
		{Name: "cli", Kind: KindBinary, Ref: bin},
	})
	for _, r := range results {
		if !r.Ref.HasSBOM() {
			t.Fatalf("%s: %+v", r.Target.Name, r.Ref)
		}
	}

	wantAuth := []string{
		"SYFT_REGISTRY_AUTH_AUTHORITY=registry.example.com",
		"SYFT_REGISTRY_AUTH_USERNAME=forge",
		"SYFT_REGISTRY_AUTH_PASSWORD=" + platformSecret,
	}
	authed := 0
	for i, call := range fake.calls {
		src, env := call[2], fake.envs[i]
		platformRead := strings.HasPrefix(src, "registry:registry.example.com/")
		if platformRead {
			authed++
			if strings.Join(env, "\n") != strings.Join(wantAuth, "\n") {
				t.Errorf("%s: env %q, want exactly %q", src, env, wantAuth)
			}
		} else if len(env) != 0 {
			// The docker-daemon attempt, other registries and file reads
			// get nothing: the credential is for its own authority alone.
			t.Errorf("%s: forge's registry credential leaked into a read that is not the platform registry's: %q", src, env)
		}
		for _, arg := range call {
			if strings.Contains(arg, platformSecret) {
				t.Errorf("%s: the credential is on syft's command line: %q", src, call)
			}
		}
	}
	if authed != 2 {
		t.Errorf("%d authenticated reads, want the hosted read and the built image's registry fallback", authed)
	}
}

func TestGenerate_UnresolvableRegistryCredentialFallsBackAndSaysSo(t *testing.T) {
	t.Parallel()
	fake := &fakeSyft{fail: map[string]error{"registry": errors.New("exit status 1: 401 Unauthorized")}}
	g := &Generator{Tool: pinnedTool, Runner: fake, RegistryAuth: func(context.Context, string) (RegistryCredential, bool, error) {
		return RegistryCredential{}, false, errors.New("no control-plane credential: run forge login")
	}}
	r := g.Generate(context.Background(), []Target{{Name: "hosted", Kind: KindRegistry, Ref: "registry.example.com/o/api@" + imageDigest, Subject: imageDigest}})[0]
	if r.Ref.HasSBOM() || !strings.Contains(r.Ref.Reason, "401") ||
		!strings.Contains(r.Ref.Reason, "forge's own credential for registry.example.com could not be resolved: no control-plane credential") {
		t.Errorf("reason %q should carry syft's refusal and why forge had no credential", r.Ref.Reason)
	}
	if len(fake.envs) != 1 || len(fake.envs[0]) != 0 {
		t.Errorf("syft should still try the ambient store, with no forge credential: %q", fake.envs)
	}
}

// The credential is on the child's environment only: never on its argv, and
// never in forge's own environment, which every later child would inherit.
func TestSyftCommand_CredentialOnlyInTheChildEnvironment(t *testing.T) {
	t.Parallel()
	env := []string{"SYFT_REGISTRY_AUTH_AUTHORITY=registry.example.com", "SYFT_REGISTRY_AUTH_PASSWORD=" + platformSecret}
	cmd := syftCommand(context.Background(), filepath.Join(t.TempDir(), "syft"), syftArgs(Target{Name: "api"}, "registry:registry.example.com/o/api@"+imageDigest), env)
	if got := cmd.Env[len(cmd.Env)-2:]; strings.Join(got, "\n") != strings.Join(env, "\n") {
		t.Errorf("child env ends %q, want %q last so it wins over an inherited value", got, env)
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, platformSecret) {
			t.Errorf("credential on the command line: %q", cmd.Args)
		}
	}
	if os.Getenv("SYFT_REGISTRY_AUTH_PASSWORD") == platformSecret {
		t.Error("the credential was set in forge's own environment")
	}
}

func TestRegistryHost(t *testing.T) {
	t.Parallel()
	for ref, want := range map[string]string{
		"registry.reliantapi.com/org/p/api@sha256:x": "registry.reliantapi.com",
		"Registry.Example.com:5000/a@sha256:x":       "registry.example.com:5000",
		"localhost/a@sha256:x":                       "localhost",
		"library/redis@sha256:x":                     "docker.io",
		"redis@sha256:x":                             "docker.io",
	} {
		if got := registryHost(ref); got != want {
			t.Errorf("registryHost(%q) = %q, want %q", ref, got, want)
		}
	}
}
