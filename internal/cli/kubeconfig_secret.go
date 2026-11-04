// Package cli — cross-cluster kubeconfig minting (forge.KubeconfigSecret).
//
// In a multi-k3d-cluster dev/e2e env, a workload in cluster A may need to
// talk to cluster B's API server. A kubeconfig with B's serverlb IP baked
// in goes stale every time B is recreated (k3d hands the container a fresh
// docker-network IP). This file mints that kubeconfig FRESH each `forge
// up` — resolving the endpoint at mint time, never persisting the IP —
// and stores it as a k8s Secret the workload mounts.
//
// The "in-network" reachability seam is the dev/e2e path: the target's
// API server is only reachable by its serverlb container IP on the shared
// docker network. That IP isn't in the serving cert's SANs, but the
// container's NAME is, so the minted kubeconfig dials https://<ip>:6443 and
// verifies the certificate against the cluster CA as tls-server-name
// k3d-<name>-serverlb (see rewriteInNetworkKubeconfig — verification is
// never skipped). "endpoint" (prod) uses the kubeconfig's own endpoint
// verbatim — a stable reachable address with a valid cert.
package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/reliant-labs/forge/internal/cluster"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// mintKubeconfigSecrets mints every declared cross-cluster kubeconfig and
// applies it as a Secret. Runs at the cluster→deploy boundary — AFTER the
// clusters exist (so `k3d kubeconfig get` + the serverlb container are
// available) and BEFORE workloads mount the Secret. A nil/empty list is a
// no-op.
//
// `ownerNetwork` is the docker network the in-network IP is resolved on —
// the network the clusters share (k3d names a cluster's auto-created
// network `k3d-<owner>`). Derived by the caller from the env's declared
// clusters.
func mintKubeconfigSecrets(ctx context.Context, secrets []KubeconfigSecretEntity, ownerNetwork, defaultNamespace string) error {
	return mintKubeconfigSecretsMode(ctx, secrets, ownerNetwork, defaultNamespace, false)
}

// mintKubeconfigSecretsMode is mintKubeconfigSecrets with an explicit
// dry-run. Under dryRun the phase contacts NO cluster and prints the
// objects a real run would create — the same convention the rest of the
// deploy path follows, so `--dry-run` is a safe way to review a credential
// declaration before it is granted.
func mintKubeconfigSecretsMode(ctx context.Context, secrets []KubeconfigSecretEntity, ownerNetwork, defaultNamespace string, dryRun bool) error {
	if len(secrets) == 0 {
		return nil
	}
	verb := "minting"
	if dryRun {
		verb = "would mint"
	}
	fmt.Printf("\n[up] kubeconfig phase — %s %d cross-cluster kubeconfig(s)\n", verb, len(secrets))
	for i := range secrets {
		if dryRun {
			if secrets[i].ServiceAccount != nil {
				fmt.Print(describeServiceAccountMint(secrets[i], defaultNamespace))
				continue
			}
			fmt.Printf("  [dry-run] kubeconfig Secret %s (target=%s, reachability=%s)\n",
				secrets[i].Name, secrets[i].TargetCluster, secrets[i].Reachability)
			continue
		}
		if err := mintOneKubeconfigSecret(ctx, secrets[i], ownerNetwork, defaultNamespace); err != nil {
			return fmt.Errorf("mint kubeconfig secret %q: %w", secrets[i].Name, err)
		}
	}
	return nil
}

// mintDeployKubeconfigSecrets runs the mint on the DEPLOY path, for the
// declarations that belong there: the ones that mint their own credential.
//
// `forge env deploy` is how a cloud env ships — its clusters already exist,
// so it never runs `env up`'s cluster phase. A k3d in-network declaration is
// deliberately NOT handled here: it resolves a docker container address on
// the operator's machine, which is an `env up` concern and would fail (or,
// worse, resolve something unrelated) during a cloud deploy.
func mintDeployKubeconfigSecrets(ctx context.Context, entities *KCLEntities, defaultNamespace string, dryRun bool) error {
	return mintKubeconfigSecretsMode(ctx, deployMintedKubeconfigSecrets(entities), "", defaultNamespace, dryRun)
}

// deployMintedKubeconfigSecrets is the declarations the DEPLOY path mints:
// those that mint their own credential (a declared service_account). A
// declaration WITHOUT one copies a k3d kubeconfig, which is an `env up`
// concern — see mintDeployKubeconfigSecrets.
//
// It exists as a named function, rather than a loop inside the mint, because
// the deployability preflight needs the SAME set: these Secrets are created by
// this deploy AFTER the preflight runs, so demanding them up-front blocks the
// deploy on its own output. Deriving both from one filter is what keeps the
// gate and the provisioning step from drifting — a new kind of minted
// declaration becomes exempt automatically, and a declaration forge does not
// mint stays gated.
//
// nil entities / no declarations => nil (both callers no-op).
func deployMintedKubeconfigSecrets(entities *KCLEntities) []KubeconfigSecretEntity {
	if entities == nil {
		return nil
	}
	var minted []KubeconfigSecretEntity
	for _, k := range entities.KubeconfigSecrets {
		if k.ServiceAccount != nil {
			minted = append(minted, k)
		}
	}
	return minted
}

// mintOneKubeconfigSecret mints one declared kubeconfig and applies it as a
// Secret on its consumer cluster.
func mintOneKubeconfigSecret(ctx context.Context, k KubeconfigSecretEntity, ownerNetwork, defaultNamespace string) error {
	// A declared ServiceAccount switches the whole credential model: forge
	// MINTS a token on the target instead of copying the operator's
	// credential out of their kubeconfig. That is the only path that works
	// for a cluster whose kubeconfig authenticates with an exec plugin
	// (GKE/EKS/AKS) — see kubeconfig_serviceaccount.go.
	if k.ServiceAccount != nil {
		return mintServiceAccountKubeconfig(ctx, k, defaultNamespace)
	}
	// Without one, the mint below COPIES the credential from `k3d
	// kubeconfig get`, so it can only address a k3d cluster. Naming a
	// target_context means the target is something else, and copying that
	// context's credential would produce a kubeconfig no pod can use (an
	// exec plugin, or a cert file that exists only on this machine).
	// Refuse with the fix rather than mint something inert.
	if strings.TrimSpace(k.TargetContext) != "" {
		return fmt.Errorf(
			"KubeconfigSecret %q sets target_context %q but declares no service_account: "+
				"forge can only COPY a credential for a k3d cluster. For any other cluster it must MINT one — "+
				"declare service_account (a ServiceAccount + rules forge creates on the target) so the minted "+
				"kubeconfig carries an inline bearer token a pod can actually present",
			k.Name, k.TargetContext)
	}
	key := k.Key
	if key == "" {
		key = "kubeconfig"
	}
	ns := k.Namespace
	if ns == "" {
		ns = defaultNamespace
	}
	if ns == "" {
		return fmt.Errorf("no namespace: set KubeconfigSecret.namespace or ensure the env declares one")
	}

	// 1. Get the target cluster's kubeconfig into a temp file we can
	//    rewrite without touching the user's ~/.kube/config.
	rawKubeconfig, err := k3dKubeconfigGet(ctx, k.TargetCluster)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "forge-kubeconfig-*.yaml")
	if err != nil {
		return fmt.Errorf("temp kubeconfig: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, werr := tmp.Write(rawKubeconfig); werr != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp kubeconfig: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("close temp kubeconfig: %w", cerr)
	}

	// k3d names the kubeconfig context `k3d-<target>`; this is the name we
	// rewrite cluster/server on and rename to the stable context_name.
	k3dContext := "k3d-" + k.TargetCluster
	// The cluster ENTRY name inside a k3d kubeconfig is also `k3d-<target>`.
	clusterEntry := "k3d-" + k.TargetCluster

	if k.Reachability == "in-network" {
		// 2a. Resolve the target's API endpoint FRESH from docker — the
		//     serverlb container's IP on the owner network (with a
		//     server-0 fallback). Never persisted to a committed file.
		_, container, ierr := resolveInNetworkAPIServerIP(ctx, k.TargetCluster, ownerNetwork)
		if ierr != nil {
			return ierr
		}
		// Dial the container by its NAME, never its IP. Docker reassigns
		// container IPs whenever the network is re-established (a host
		// sleep did it here), and a minted IP then silently points at a
		// DIFFERENT cluster's API server — live, the "hub" kubeconfig was
		// reaching cp-daemon. The name is stable, resolves from any pod
		// on the shared network (docker DNS via CoreDNS's forward), and is
		// in the serving cert's SANs, so the cert verifies against the
		// kubeconfig's own CA with nothing overridden.
		rewritten, rerr := rewriteInNetworkKubeconfig(rawKubeconfig, clusterEntry,
			inNetworkServer(container), container)
		if rerr != nil {
			return rerr
		}
		if err := atomicfile.WriteFile(tmpPath, rewritten, 0o600); err != nil {
			return fmt.Errorf("write rewritten kubeconfig: %w", err)
		}
	}
	// 2b. "endpoint": leave the kubeconfig's own server/CA verbatim.

	// 3. Rename the context to the stable context_name the workload selects.
	if k.ContextName != k3dContext {
		if err := kubectlConfigRenameContext(ctx, tmpPath, k3dContext, k.ContextName); err != nil {
			return err
		}
	}

	// 4. Read the rewritten kubeconfig and apply it as a Secret in
	//    in_cluster. EnsureNamespace first (the namespace may not exist
	//    yet on a fresh cluster — same ordering fix the dotenv Secret
	//    apply uses).
	final, rerr := os.ReadFile(tmpPath)
	if rerr != nil {
		return fmt.Errorf("read minted kubeconfig: %w", rerr)
	}
	if err := cluster.EnsureNamespace(ctx, k.InCluster, ns); err != nil {
		return fmt.Errorf("ensure namespace %q in %q: %w", ns, k.InCluster, err)
	}
	secretYAML := kubeconfigSecretYAML(k.Name, ns, key, final)
	fmt.Printf("  applying kubeconfig Secret %s/%s into %s (target=%s, reachability=%s)\n",
		ns, k.Name, k.InCluster, k.TargetCluster, k.Reachability)
	if err := cluster.KubectlApply(ctx, k.InCluster, secretYAML); err != nil {
		return fmt.Errorf("apply kubeconfig Secret: %w", err)
	}
	return nil
}

// k3dKubeconfigGet returns the target cluster's kubeconfig YAML.
func k3dKubeconfigGet(ctx context.Context, target string) ([]byte, error) {
	getCmd := exec.CommandContext(ctx, "k3d", "kubeconfig", "get", target)
	scrubSubprocessLogEnv(getCmd)
	out, err := getCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("k3d kubeconfig get %s: %w", target, err)
	}
	return out, nil
}

// resolveInNetworkAPIServerIP returns the target cluster's API-server
// container IP on the shared owner network. Prefers the serverlb
// (load-balancer) container; falls back to server-0 when the cluster was
// created with --no-lb (no serverlb). The IP is read from docker every
// run and never written to a committed file — that's the whole point
// (the IP drifts each `k3d cluster create`).
//
// It also returns the NAME of the container it matched: k3s puts
// k3d-<name>-serverlb and k3d-<name>-server-0 into the API server
// certificate's SANs (and not the container IP), so the name is what the
// minted kubeconfig verifies the certificate against.
func resolveInNetworkAPIServerIP(ctx context.Context, target, ownerNetwork string) (ip, container string, err error) {
	if ownerNetwork == "" {
		return "", "", fmt.Errorf(
			"cannot resolve in-network IP for %q: no owner network "+
				"(the clusters must share a docker network — declare Cluster.network)", target)
	}
	format := fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).IPAddress}}`, ownerNetwork)
	// serverlb first, then server-0 (--no-lb clusters).
	for _, name := range []string{"k3d-" + target + "-serverlb", "k3d-" + target + "-server-0"} {
		if ip := dockerInspectIP(ctx, name, format); ip != "" {
			return ip, name, nil
		}
	}
	return "", "", fmt.Errorf(
		"could not resolve API-server IP for cluster %q on network %q "+
			"(tried k3d-%s-serverlb and k3d-%s-server-0)", target, ownerNetwork, target, target)
}

// inNetworkServer is the API server URL an in-network kubeconfig dials: the
// k3d container's NAME on the shared docker network. See the call site for
// why never the IP.
func inNetworkServer(container string) string {
	return "https://" + container + ":6443"
}

// rewriteInNetworkKubeconfig points clusterEntry at server (the target's
// container address on the shared docker network) and VERIFIES the API
// server's certificate against the kubeconfig's own CA under serverName.
//
// NEVER insecure-skip-tls-verify. That was the previous policy, on the
// premise that the serving certificate does not cover the container IP. The
// premise holds for the IP and not for the NAME — k3s stamps
// k3d-<name>-serverlb and k3d-<name>-server-0 into the SANs — and the policy
// was not merely weaker, it was BROKEN for the most important consumer: Flux's
// kustomize-controller ignores insecure-skip-tls-verify in a Kustomization's
// spec.kubeConfig Secret by design, so every apply through a minted hub edge
// failed with "x509: certificate signed by unknown authority".
// tls-server-name is the standard kubeconfig field for exactly this shape:
// dial one address, verify another name. Every client-go consumer honours it.
func rewriteInNetworkKubeconfig(raw []byte, clusterEntry, server, serverName string) ([]byte, error) {
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("parse kubeconfig: %w", err)
	}
	c, ok := cfg.Clusters[clusterEntry]
	if !ok || c == nil {
		return nil, fmt.Errorf("kubeconfig has no cluster entry %q", clusterEntry)
	}
	if len(c.CertificateAuthorityData) == 0 && c.CertificateAuthority == "" {
		return nil, fmt.Errorf("kubeconfig cluster %q carries no CA: the in-network mint verifies the API server "+
			"by name against the cluster CA, and will not fall back to skipping verification", clusterEntry)
	}
	c.Server = server
	c.TLSServerName = serverName
	c.InsecureSkipTLSVerify = false
	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("write kubeconfig: %w", err)
	}
	return out, nil
}

// dockerInspectIP returns the inspected IP for a container, or "" when
// the container doesn't exist / isn't on the network (so the caller can
// fall through to the next candidate).
func dockerInspectIP(ctx context.Context, container, format string) string {
	out, err := exec.CommandContext(ctx, "docker", "inspect", container, "--format", format).Output()
	if err != nil {
		return ""
	}
	ip := strings.TrimSpace(string(out))
	// A container that exists but isn't on the queried network renders the
	// template to "<no value>" or empty — treat both as a miss.
	if ip == "" || ip == "<no value>" {
		return ""
	}
	return ip
}

// kubectlConfig* helpers operate on an explicit kubeconfig file (never
// the user's default config) via `kubectl config --kubeconfig=<path>`.

func kubectlConfigRenameContext(ctx context.Context, kubeconfigPath, from, to string) error {
	return runKubectlConfig(ctx, kubeconfigPath, "rename-context", from, to)
}

// runKubectlConfig runs a `kubectl config` subcommand against an explicit
// kubeconfig file. Stderr is surfaced on failure (kubectl's config errors
// are actionable: "no cluster named X").
func runKubectlConfig(ctx context.Context, kubeconfigPath string, args ...string) error {
	all := append([]string{"config", "--kubeconfig", kubeconfigPath}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", all...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl config %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// kubeconfigSecretYAML builds the Opaque Secret manifest carrying the
// minted kubeconfig under `key`. Uses base64 `data` (not stringData) so
// the YAML stays safe regardless of the kubeconfig's contents.
func kubeconfigSecretYAML(name, namespace, key string, kubeconfig []byte) string {
	enc := base64.StdEncoding.EncodeToString(kubeconfig)
	var b strings.Builder
	b.WriteString("apiVersion: v1\n")
	b.WriteString("kind: Secret\n")
	b.WriteString("metadata:\n")
	b.WriteString("  name: " + name + "\n")
	b.WriteString("  namespace: " + namespace + "\n")
	b.WriteString("  labels:\n")
	b.WriteString("    app.kubernetes.io/managed-by: forge\n")
	b.WriteString("type: Opaque\n")
	b.WriteString("data:\n")
	b.WriteString("  " + key + ": " + enc + "\n")
	return b.String()
}

// ownerNetworkFromClusters derives the docker network the in-network IP
// resolution runs on. A secondary cluster names its owner's network via
// Cluster.network; the owner cluster gets its own auto-created network
// `k3d-<owner>`. The owner network is the value the secondaries point at —
// the first non-empty Cluster.network. When no cluster declares a
// network, fall back to the single declared cluster's own network
// (`k3d-<name>`), which is what a one-cluster env's serverlb sits on.
//
// There is NO "primary" notion: this just reads the network the env's
// clusters explicitly share (the secondaries' `network`), or — absent any
// cross-cluster wiring — the lone cluster's own k3d network.
func ownerNetworkFromClusters(clusters []ClusterEntity) string {
	for _, c := range clusters {
		if c.Network != "" {
			return c.Network
		}
	}
	if len(clusters) == 1 {
		return "k3d-" + clusters[0].Name
	}
	return ""
}
