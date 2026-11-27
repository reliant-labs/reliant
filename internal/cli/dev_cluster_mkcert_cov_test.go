package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const covCMkcertBundle = `{"project":"mk","env":"dev","gateways":[
  {"name":"public","host":"app.localtest.me","tls":{"mode":"mkcert","secret_name":"app-tls"}},
  {"name":"plain","host":"x.example.com","tls":{"mode":"cert_manager","secret_name":"x"}}]}`

// covCMkcertProject is a deploy-enabled project whose dev env declares one
// mkcert gateway.
func covCMkcertProject(t *testing.T) {
	t.Helper()
	deployEnabledProject(t)
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, covCMkcertBundle))
}

func TestCovC_MkcertNamespaceFollowsTheProjectName(t *testing.T) {
	deployEnabledProject(t)
	if got := mkcertDevNamespace(""); got != testProjectName+"-dev" {
		t.Errorf("namespace = %q", got)
	}
	t.Chdir(t.TempDir())
	if got := mkcertDevNamespace(""); got != "dev" {
		t.Errorf("a directory outside any project falls back to %q, got %q", "dev", got)
	}
}

func TestCovC_MkcertHelpers(t *testing.T) {
	t.Run("on PATH", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"mkcert": `exit 0`})
		if !mkcertOnPath() {
			t.Error("a mkcert on PATH is found")
		}
		t.Setenv("PATH", t.TempDir())
		if mkcertOnPath() {
			t.Error("no mkcert on PATH")
		}
	})
	t.Run("CA root installed", func(t *testing.T) {
		root := t.TempDir()
		covCInstallTools(t, map[string]string{"mkcert": `echo "$COVC_CAROOT"`})
		t.Setenv("COVC_CAROOT", root)
		if mkcertCARootInstalled(context.Background()) {
			t.Error("a CAROOT without rootCA.pem is not installed")
		}
		if err := os.WriteFile(filepath.Join(root, "rootCA.pem"), []byte("pem"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !mkcertCARootInstalled(context.Background()) {
			t.Error("rootCA.pem present means installed")
		}
		t.Setenv("COVC_CAROOT", "")
		if mkcertCARootInstalled(context.Background()) {
			t.Error("an empty CAROOT is not installed")
		}
		covCInstallTools(t, map[string]string{"mkcert": `exit 1`})
		if mkcertCARootInstalled(context.Background()) {
			t.Error("a failing mkcert is not installed")
		}
	})
	t.Run("runMkcert reads back the cert and key it asked for", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"mkcert": `cert=""; key=""
while [ $# -gt 0 ]; do case "$1" in -cert-file) cert="$2"; shift;; -key-file) key="$2"; shift;; esac; shift; done
[ -n "$COVC_NOKEY" ] && { echo CERT > "$cert"; exit 0; }
echo CERT > "$cert"; echo KEY > "$key"`})
		tmp := t.TempDir()
		cert, key, err := runMkcert(context.Background(), tmp, "app.localtest.me")
		if err != nil || string(cert) != "CERT\n" || string(key) != "KEY\n" {
			t.Fatalf("= %q %q %v", cert, key, err)
		}
		if !covCHasCall(covCCalls(t, logPath), "mkcert -cert-file "+filepath.Join(tmp, "tls.crt")+" -key-file "+filepath.Join(tmp, "tls.key")+" app.localtest.me") {
			t.Errorf("calls = %v", covCCalls(t, logPath))
		}
		t.Setenv("COVC_NOKEY", "1")
		if _, _, err := runMkcert(context.Background(), t.TempDir(), "h"); err == nil || !strings.Contains(err.Error(), "read mkcert key") {
			t.Errorf("err = %v", err)
		}
		covCInstallTools(t, map[string]string{"mkcert": `exit 1`})
		if _, _, err := runMkcert(context.Background(), t.TempDir(), "h"); err == nil || !strings.Contains(err.Error(), "mkcert h") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("ensureNamespace applies a forge-managed Namespace", func(t *testing.T) {
		logPath := covCInstallTools(t, map[string]string{"kubectl": `cat > "$COVC_LOG.stdin"`})
		if err := ensureNamespace(context.Background(), "mk-dev"); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(logPath + ".stdin")
		for _, want := range []string{"kind: Namespace", "name: mk-dev", "app.kubernetes.io/managed-by: forge"} {
			if !strings.Contains(string(b), want) {
				t.Errorf("manifest lacks %q:\n%s", want, b)
			}
		}
	})
}

func TestCovC_ProvisionMkcertSecrets(t *testing.T) {
	ctx := context.Background()
	const mkcertOK = `cert=""; key=""
while [ $# -gt 0 ]; do case "$1" in -cert-file) cert="$2"; shift;; -key-file) key="$2"; shift;; esac; shift; done
[ "$1" = "-CAROOT" ] && { echo "$COVC_CAROOT"; exit 0; }
echo CERT > "$cert"; echo KEY > "$key"`
	t.Run("a render failure is not fatal to cluster up", func(t *testing.T) {
		deployEnabledProject(t)
		prev := renderKCLRawHook
		t.Cleanup(func() { renderKCLRawHook = prev })
		renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) { return nil, os.ErrInvalid }
		if err := provisionMkcertSecrets(ctx, "."); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no mkcert gateways means no work", func(t *testing.T) {
		deployEnabledProject(t)
		t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"project":"mk","env":"dev"}`))
		logPath := covCInstallTools(t, map[string]string{"mkcert": `exit 0`, "kubectl": `exit 0`})
		if err := provisionMkcertSecrets(ctx, "."); err != nil || len(covCCalls(t, logPath)) != 0 {
			t.Fatalf("err %v calls %v", err, covCCalls(t, logPath))
		}
	})
	t.Run("a gateway declared without a host is refused", func(t *testing.T) {
		deployEnabledProject(t)
		t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"project":"mk","env":"dev","gateways":[{"name":"g","tls":{"mode":"mkcert","secret_name":"s"}}]}`))
		if err := provisionMkcertSecrets(ctx, "."); err == nil || !strings.Contains(err.Error(), "requires a non-empty host") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("mkcert missing warns with install hints and leaves the cluster up", func(t *testing.T) {
		covCMkcertProject(t)
		t.Setenv("PATH", t.TempDir())
		var err error
		out := captureStdout(t, func() { err = provisionMkcertSecrets(ctx, ".") })
		if err != nil || !strings.Contains(out, "`mkcert` not on PATH") || !strings.Contains(out, "brew install mkcert") {
			t.Fatalf("err %v out %q", err, out)
		}
	})
	t.Run("signs the host, creates the namespace, then applies the TLS Secret", func(t *testing.T) {
		covCMkcertProject(t)
		logPath := covCInstallTools(t, map[string]string{"mkcert": mkcertOK, "kubectl": `cat >> "$COVC_LOG.stdin"`})
		t.Setenv("COVC_CAROOT", t.TempDir()) // no rootCA.pem
		var err error
		out := captureStdout(t, func() { err = provisionMkcertSecrets(ctx, ".") })
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		if !strings.Contains(out, "`mkcert -install` hasn't been run") {
			t.Errorf("a machine without the mkcert CA is told to install it:\n%s", out)
		}
		if !strings.Contains(out, `Provisioning mkcert TLS Secret "app-tls" (`+testProjectName+`-dev) for host app.localtest.me`) {
			t.Errorf("out = %s", out)
		}
		if calls := covCCalls(t, logPath); !covCHasCall(calls, "mkcert -cert-file") || !covCHasCall(calls, "app.localtest.me") || len(calls) < 4 {
			t.Errorf("calls = %v", calls)
		}
		b, _ := os.ReadFile(logPath + ".stdin")
		ns, secret := strings.Index(string(b), "kind: Namespace"), strings.Index(string(b), "kind: Secret")
		if ns < 0 || secret < ns || !strings.Contains(string(b), "type: kubernetes.io/tls") || !strings.Contains(string(b), "forge.dev/tls-source: mkcert") {
			t.Errorf("the Namespace must be applied before the Secret that lives in it:\n%s", b)
		}
	})
	t.Run("a failing apply names the Secret", func(t *testing.T) {
		covCMkcertProject(t)
		// The first apply (the Namespace) succeeds; the second (the Secret) fails.
		covCInstallTools(t, map[string]string{"mkcert": mkcertOK, "kubectl": `cat >/dev/null
n=$(cat "$COVC_LOG.count" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$COVC_LOG.count"
[ "$n" -ge 2 ] && exit 1; exit 0`})
		t.Setenv("COVC_CAROOT", t.TempDir())
		var err error
		captureStdout(t, func() { err = provisionMkcertSecrets(ctx, ".") })
		if err == nil || !strings.Contains(err.Error(), "apply mkcert Secret app-tls") {
			t.Fatalf("err = %v", err)
		}
	})
}
