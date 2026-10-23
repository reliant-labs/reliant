// Package cli — `forge cluster up` mkcert TLS provisioning.
//
// Why this exists: in dev, cert-manager's only stand-alone option is
// a self-signed Issuer whose CA isn't in the host trust store, so
// every browser shows a warning and every curl needs `-k`. mkcert
// installs a CA into the OS trust store once (`mkcert -install`) and
// signs leaf certs the host already trusts. The cluster-side shape
// stays identical to prod (Gateway carries `tls:`, listener is HTTPS,
// Secret is referenced normally); only the cert origin differs.
//
// This file walks the dev env's rendered KCL, finds Gateways whose
// tls.mode == "mkcert", and provisions a kubernetes.io/tls Secret per
// (hostname, secret_name) pair. Re-running just refreshes the Secret.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// mkcertSecretSpec is the projection of one mkcert-mode Gateway into
// the (hostname, secret_name, namespace) tuple we hand mkcert + kubectl.
// Exposed as its own type so the projection helper is testable without
// the mkcert binary on PATH.
type mkcertSecretSpec struct {
	Hostname   string
	SecretName string
	Namespace  string
}

// projectMkcertSecrets walks the rendered KCL entities and returns one
// mkcertSecretSpec per Gateway with tls.mode == "mkcert". An empty
// hostname is an error — mkcert needs a name to sign for, and an empty
// host on an mkcert Gateway is almost certainly a misconfiguration.
//
// Pure-logic helper — no side effects, no binary invocation. The
// caller (provisionMkcertSecrets) drives mkcert + kubectl from the
// returned specs.
func projectMkcertSecrets(entities *KCLEntities, namespace string) ([]mkcertSecretSpec, error) {
	if entities == nil {
		return nil, nil
	}
	var out []mkcertSecretSpec
	for _, gw := range entities.Gateways {
		if gw.TLS == nil || gw.TLS.Mode != "mkcert" {
			continue
		}
		if gw.Host == "" {
			return nil, fmt.Errorf("gateway %q: tls.mode == \"mkcert\" requires a non-empty host (mkcert signs for a specific hostname)", gw.Name)
		}
		if gw.TLS.SecretName == "" {
			return nil, fmt.Errorf("gateway %q: tls.secret_name is required", gw.Name)
		}
		out = append(out, mkcertSecretSpec{
			Hostname:   gw.Host,
			SecretName: gw.TLS.SecretName,
			Namespace:  namespace,
		})
	}
	return out, nil
}

// mkcertOnPath reports whether the mkcert binary is on PATH. Cheap
// presence check — does NOT verify the CA is installed.
func mkcertOnPath() bool {
	_, err := exec.LookPath("mkcert")
	return err == nil
}

// mkcertCARootInstalled returns true when `mkcert -CAROOT` points at
// a directory that exists. False means the user hasn't run
// `mkcert -install` (or did and the CA was removed). Best-effort — we
// only use this to print a one-time hint, never to fail.
func mkcertCARootInstalled(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "mkcert", "-CAROOT").Output()
	if err != nil {
		return false
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(root, "rootCA.pem")); err != nil {
		return false
	}
	return true
}

// runMkcert invokes `mkcert -cert-file <crt> -key-file <key> <host>`
// against the given temp dir and returns the cert+key bytes.
func runMkcert(ctx context.Context, tmpDir, hostname string) (certPEM, keyPEM []byte, err error) {
	certPath := filepath.Join(tmpDir, "tls.crt")
	keyPath := filepath.Join(tmpDir, "tls.key")
	cmd := exec.CommandContext(ctx, "mkcert",
		"-cert-file", certPath,
		"-key-file", keyPath,
		hostname,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("mkcert %s: %w", hostname, err)
	}
	certPEM, err = os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read mkcert cert: %w", err)
	}
	keyPEM, err = os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read mkcert key: %w", err)
	}
	return certPEM, keyPEM, nil
}

// renderTLSSecretYAML produces the kubernetes.io/tls Secret manifest
// for the given (name, namespace, cert, key) tuple. The data fields
// are base64-encoded via yaml.Marshal's []byte handling so the output
// matches what `kubectl create secret tls` would produce.
func renderTLSSecretYAML(name, namespace string, certPEM, keyPEM []byte) ([]byte, error) {
	manifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       "kubernetes.io/tls",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "forge",
				"app.kubernetes.io/part-of":    "forge-ingress",
				"forge.dev/tls-source":         "mkcert",
			},
		},
		"data": map[string]any{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}
	return yaml.Marshal(manifest)
}

// provisionMkcertSecrets is the post-cluster-up TLS hook. It
// renders the project's dev KCL, projects mkcert-mode Gateways into
// the (host, secret, ns) tuple set, and provisions one TLS Secret per
// tuple via mkcert + kubectl apply.
//
// Behaviour matrix:
//
//   - No mkcert-mode gateways in dev KCL  → no-op success, no message.
//   - mkcert NOT on PATH but specs found  → WARN (with install hint)
//     and return nil — the cluster still comes up; the user just won't
//     have a valid Secret until they install mkcert and re-run.
//   - mkcert present but CA not installed → WARN (suggest `mkcert
//     -install`) but proceed; mkcert -cert-file will install the CA
//     into the local store on first run.
//   - mkcert succeeds                     → apply Secret(s); idempotent.
func provisionMkcertSecrets(ctx context.Context, projectDir string) error {
	entities, err := RenderKCL(ctx, projectDir, "dev")
	if err != nil {
		// Don't fail cluster-up over an absent dev KCL — this function
		// is opt-in. The user almost certainly has features.ingress on
		// but doesn't actually use mkcert; the rest of the cluster is
		// usable.
		return nil
	}

	namespace := mkcertDevNamespace(projectDir)
	specs, err := projectMkcertSecrets(entities, namespace)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return nil
	}

	if !mkcertOnPath() {
		fmt.Println("WARN: mkcert-mode Gateway(s) declared but `mkcert` not on PATH —")
		fmt.Println("      install it (brew install mkcert / choco install mkcert /")
		fmt.Println("      scoop install mkcert) and re-run `forge cluster up` to")
		fmt.Println("      provision the TLS Secret(s). Cluster is otherwise up.")
		return nil
	}

	if !mkcertCARootInstalled(ctx) {
		fmt.Println("Note: `mkcert -install` hasn't been run on this machine yet.")
		fmt.Println("      Run it once to add mkcert's CA to your OS trust store so")
		fmt.Println("      browsers + curl trust the certs forge provisions below.")
	}

	tmpRoot, err := os.MkdirTemp("", "forge-mkcert-*")
	if err != nil {
		return fmt.Errorf("create mkcert tmp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpRoot) }()

	for _, spec := range specs {
		fmt.Printf("Provisioning mkcert TLS Secret %q (%s) for host %s...\n",
			spec.SecretName, spec.Namespace, spec.Hostname)
		certPEM, keyPEM, err := runMkcert(ctx, tmpRoot, spec.Hostname)
		if err != nil {
			return err
		}
		secretYAML, err := renderTLSSecretYAML(spec.SecretName, spec.Namespace, certPEM, keyPEM)
		if err != nil {
			return fmt.Errorf("render Secret YAML for %s: %w", spec.SecretName, err)
		}
		// Ensure the namespace exists before applying the Secret —
		// the env's namespace is normally created by forge env deploy
		// dev, but cluster-up runs first so we have to bootstrap it.
		if err := ensureNamespace(ctx, spec.Namespace); err != nil {
			return err
		}
		if err := kubectlApplyBytes(ctx, "", secretYAML); err != nil {
			return fmt.Errorf("apply mkcert Secret %s: %w", spec.SecretName, err)
		}
	}
	return nil
}

// mkcertDevNamespace returns the namespace to apply mkcert Secrets
// into. Mirrors `forge cluster reload`'s resolution (cfg.Name +
// "-dev" fallback when forge.yaml has no explicit dev namespace).
// `projectDir` is currently informational — loadProjectConfig walks
// up from cwd which is the established convention.
func mkcertDevNamespace(_ string) string {
	if store, err := loadProjectStore(); err == nil && store.Meta().Name != "" {
		return store.Meta().Name + "-dev"
	}
	return "dev"
}

// ensureNamespace `kubectl apply`s a minimal Namespace manifest. No-op
// when the namespace already exists (apply is idempotent).
func ensureNamespace(ctx context.Context, name string) error {
	manifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name": name,
			"labels": map[string]string{
				"app.kubernetes.io/managed-by": "forge",
			},
		},
	}
	out, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("render namespace manifest: %w", err)
	}
	return kubectlApplyBytes(ctx, "", out)
}
