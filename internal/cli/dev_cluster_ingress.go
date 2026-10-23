// Package cli — `forge cluster up` ingress install plumbing.
//
// forge is OPINIONATED on ONE Gateway API controller everywhere: Envoy
// Gateway (controllerName gateway.envoyproxy.io/gatewayclass-controller,
// GatewayClass `eg`). The local k3d bring-up installs the SAME helm chart
// the cloud envs (staging/preprod/prod) run, so a Gateway that renders
// `gatewayClassName: eg` behaves identically on a laptop and in the cloud.
// Envoy Gateway serves HTTPRoute AND GRPCRoute natively, so one controller
// covers every forge route shape — no second controller for gRPC.
//
// Three pieces, run in order after the k3d cluster is created and
// kubectl context pinned:
//
//  1. Fetch + apply the upstream Gateway API standard-channel CRDs
//     (version pinned via internal/templates/ingress/envoy/VERSION).
//     Cached under ~/.cache/forge/ingress/ so subsequent cluster-up runs
//     are offline-capable. The gateway-helm chart bundles the CRDs it
//     needs, but we apply the pinned standard channel explicitly first
//     (server-side) so the CRD surface matches the cloud install and so
//     `kubectl wait` on Established gates the GatewayClass apply.
//  2. `helm upgrade --install --skip-crds` the Envoy Gateway controller
//     from the pinned gateway-helm chart version into envoy-gateway-system,
//     the SAME release the cloud envs run. `--skip-crds` is required: the
//     chart bundles an OLDER, experimental-channel copy of the Gateway API
//     CRDs that the safe-upgrades ValidatingAdmissionPolicy (shipped by the
//     standard CRDs applied in step 1) would DENY — see helmInstallEnvoyGateway.
//     Envoy Gateway provisions a managed
//     Envoy proxy per Gateway with listener sockets derived dynamically
//     from Gateway.spec.listeners — no static per-listener entrypoint
//     config to template (unlike the old Traefik install). The proxy's
//     LoadBalancer Service ports follow the Gateway listeners; on k3d the
//     bundled klipper servicelb binds those node ports and the k3d
//     serverlb forwards the host ports mapped from deploy/k3d-ports.yaml.
//  3. Apply the vendored `eg` GatewayClass (idempotent).
//
// Idempotency comes from `helm upgrade --install` + `kubectl apply`
// semantics — re-running against a cluster that already has them noops.
// We block on CRD establishment between (1) and (3) so the GatewayClass
// apply doesn't race the CRD install.
//
// Also: k3d config merging — `forge generate` writes
// `deploy/k3d-ports.yaml` derived from the dev env's KCL gateway
// listeners. At cluster-up time we read deploy/k3d.yaml +
// deploy/k3d-ports.yaml, merge their ports blocks in memory, and
// hand a temp file to `k3d cluster create`. Keeps deploy/k3d.yaml
// user-owned while keeping host ports in lockstep with the project's
// declared Gateway listeners.
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/templates"
)

// ingressPinnedVersions reads internal/templates/ingress/envoy/VERSION
// and returns (envoyGatewayChartVersion, gatewayAPIVersion). Format:
//
//	envoy_gateway=v1.7.2
//	gateway_api=v1.5.1
func ingressPinnedVersions() (envoyGatewayVer, gatewayAPIVer string, err error) {
	b, err := templates.IngressTemplates().Get("envoy/VERSION")
	if err != nil {
		return "", "", fmt.Errorf("read pinned VERSION: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "envoy_gateway":
			envoyGatewayVer = strings.TrimSpace(v)
		case "gateway_api":
			gatewayAPIVer = strings.TrimSpace(v)
		}
	}
	if envoyGatewayVer == "" || gatewayAPIVer == "" {
		return "", "", fmt.Errorf("VERSION missing one of envoy_gateway=/gateway_api= keys")
	}
	return envoyGatewayVer, gatewayAPIVer, nil
}

// gatewayAPICRDsURL builds the upstream release URL for the standard
// channel CRDs. Pinned to the version from VERSION.
func gatewayAPICRDsURL(version string) string {
	return "https://github.com/kubernetes-sigs/gateway-api/releases/download/" + version + "/standard-install.yaml"
}

// ingressCacheDir is the local cache root for downloaded ingress
// assets. Falls back to a tempdir if $HOME isn't available — that's
// fine, the URL gets re-fetched on next cluster-up.
func ingressCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir(), nil
	}
	return filepath.Join(home, ".cache", "forge", "ingress"), nil
}

// fetchGatewayAPICRDs ensures the pinned-version CRD YAML is on disk
// at the cache path and returns the path. Re-downloads when the file
// is missing; trusts the version-pinned filename for cache busting
// (a forge project upgrade changes the VERSION file, the new release URL
// hashes to a different filename, the old cache file stays around).
func fetchGatewayAPICRDs(ctx context.Context, version string) (string, error) {
	return fetchCachedCRDYAML(ctx, "gateway-api-crds-"+version+".yaml",
		gatewayAPICRDsURL(version), fmt.Sprintf("Gateway API CRDs %s", version))
}

// certManagerCRDsURL builds the upstream release URL for cert-manager's
// CRDs at a chart version (the chart and its CRDs move in lockstep, so the
// CRD bundle is fetched at the chart's own version).
func certManagerCRDsURL(version string) string {
	return "https://github.com/cert-manager/cert-manager/releases/download/" + version + "/cert-manager.crds.yaml"
}

// fetchCertManagerCRDs ensures cert-manager's CRD YAML for the chart
// version is on disk and returns the path. Version-pinned cache filename,
// same scheme as fetchGatewayAPICRDs. Used by the helm-as-a-RENDERER apply
// path (deploy_helm.go) so a `--target=cert-manager` apply lands the CRDs
// (Established-gated) before the chart's --skip-crds controllers.
func fetchCertManagerCRDs(ctx context.Context, version string) (string, error) {
	return fetchCachedCRDYAML(ctx, "cert-manager-crds-"+version+".yaml",
		certManagerCRDsURL(version), fmt.Sprintf("cert-manager CRDs %s", version))
}

// fetchCachedCRDYAML downloads a CRD YAML bundle from url into the ingress
// cache under cacheName, returning the on-disk path. A present cache file
// is a no-op (the version-pinned filename is the cache key). label is the
// human name printed on a cold fetch. The atomic temp-write-then-rename
// keeps a concurrent reader from seeing a half-written file. Shared by the
// Gateway API + cert-manager CRD fetchers.
func fetchCachedCRDYAML(ctx context.Context, cacheName, url, label string) (string, error) {
	dir, err := ingressCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}
	cachePath := filepath.Join(dir, cacheName)
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	}

	fmt.Printf("Fetching %s from upstream...\n", label)
	req, err := http.NewRequestWithContext(ctx, "GET", url, http.NoBody)
	if err != nil {
		return "", err
	}
	httpClient := &http.Client{Timeout: 60 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, cacheName+".*.tmp")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // best-effort cleanup if rename below fails
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), cachePath); err != nil {
		return "", fmt.Errorf("install cache: %w", err)
	}
	return cachePath, nil
}

// kubeContextArgs returns the `--context <kctx>` prefix for a kubectl
// invocation, or an empty slice when kctx is "". An empty context means
// "use whatever context the caller pinned via pinKubectlContext" — the
// dev `forge cluster up` path. The declared-cluster path
// (reconcileDeclaredClusters) passes an explicit `k3d-<name>` context so
// the install targets THAT cluster regardless of the active context.
func kubeContextArgs(kctx string) []string {
	if kctx == "" {
		return nil
	}
	return []string{"--context", kctx}
}

// kubectlApplyBytes runs `kubectl apply -f -` with the given YAML
// piped in via stdin. When kctx is non-empty it targets that context
// explicitly (`--context <kctx>`); kctx == "" inherits the current
// pinned context (caller pins it via pinKubectlContext first).
func kubectlApplyBytes(ctx context.Context, kctx string, yamlBytes []byte) error {
	args := append(kubeContextArgs(kctx), "apply", "-f", "-")
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdin = strings.NewReader(string(yamlBytes))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply: %w", err)
	}
	return nil
}

// NOTE: the Gateway API controller (Envoy Gateway) is no longer installed
// imperatively. Like cert-manager (below), it is a DECLARATIVE platform
// dependency — a forge.HelmChart in the env Bundle's `helm_charts`, rendered
// (`helm template --skip-crds`) with forge-supplied pinned standard-channel
// Gateway API CRDs (crds="gateway-api") and the `eg` GatewayClass riding the
// chart's `manifests`, applied CRD-first via `forge env deploy <env>
// --target=envoy-gateway` (helm-as-a-RENDERER, internal/cluster). The pinned
// CRD bundle is still fetched here (fetchGatewayAPICRDs, reused by
// deploy_helm.go's fetchHelmChartCRDs). The former imperative
// installIngressBundle / helmInstallEnvoyGateway machinery — and the vendored
// `eg` GatewayClass — were removed: dev/e2e now bring up ingress the SAME
// declarative way the cloud envs do. ONE model everywhere.

// NOTE: cert-manager is no longer installed imperatively here. It is a
// declarative platform dependency — a forge.HelmChart in the env Bundle,
// rendered (`helm template --skip-crds`) and applied via `forge env deploy
// <env> --target=cert-manager` (helm-as-a-RENDERER, internal/cluster).
// The former `helm upgrade --install` cert-manager machinery (chart
// coordinates, repo-add, install fn) was removed with `forge cluster-setup`.

// k3dConfigPath holds the path to the (possibly merged) k3d config
// passed to `k3d cluster create`. Callers that don't need the merge
// pass the raw configPath; the merge path writes a tempfile and
// returns its path.
type k3dConfigPath struct {
	path      string
	temporary bool // true when we own cleanup
}

// mergeK3dConfig reads deploy/k3d.yaml + deploy/k3d-ports.yaml from
// the project root, splices the ports[] array from the fragment into
// the user config, and returns a path to a temp file holding the
// merged YAML. When the fragment is missing the user config is
// passed through unchanged. Caller invokes Close() to clean up.
//
// Merge policy: fragment entries WIN over scaffolded entries on the
// same host port — the fragment is derived from the current KCL
// truth, the scaffolded deploy/k3d.yaml is a one-shot from `forge
// new`. Entries that don't parse as the canonical `<host>:<cluster>`
// shorthand are passed through unchanged (best-effort: a warning is
// printed and both entries survive into the merged config — k3d may
// then reject the config, but the warning gives the user a starting
// point).
// k3dConfigOverlay is the set of DECLARED Cluster facts that must be projected
// onto a config-file cluster, because `k3d cluster create --config` accepts no
// per-flag settings and would otherwise drop them silently.
//
// Grouped into a struct rather than passed as positional parameters: they are
// all "the declared cluster, projected onto its config file", and the flag
// path's counterpart (clusterCreateFlags) likewise takes the whole entity. It
// also keeps the next projected field from being a fifth bool nobody can read
// at the call site.
type k3dConfigOverlay struct {
	HostPorts   bool
	APIPort     int
	ClusterCIDR string
	ServiceCIDR string
	// PublicIssuer projects the service-account issuer a forge-created cluster
	// signs with (k3sPublicIssuer) as k3s server args. A config that already
	// sets its own service-account-issuer is left alone.
	PublicIssuer bool
}

// empty reports whether there is nothing to project, in which case the user's
// own config file is handed back untouched (no temp file, formatting and
// comments preserved).
func (o k3dConfigOverlay) empty() bool {
	return !o.HostPorts && o.APIPort <= 0 && o.ClusterCIDR == "" && o.ServiceCIDR == "" && !o.PublicIssuer
}

func mergeK3dConfig(userPath string, overlay k3dConfigOverlay) (k3dConfigPath, func(), error) {
	cleanup := func() {}
	projectDir := filepath.Dir(userPath) // userPath is typically deploy/k3d.yaml; sibling is deploy/k3d-ports.yaml
	fragPath := filepath.Join(projectDir, "k3d-ports.yaml")
	spliceFragment := overlay.HostPorts
	if spliceFragment {
		if _, err := os.Stat(fragPath); errors.Is(err, os.ErrNotExist) {
			spliceFragment = false
		}
	}
	if !spliceFragment && overlay.APIPort <= 0 && overlay.ClusterCIDR == "" && overlay.ServiceCIDR == "" && !overlay.PublicIssuer {
		return k3dConfigPath{path: userPath}, cleanup, nil
	}

	userBytes, err := os.ReadFile(userPath)
	if err != nil {
		return k3dConfigPath{}, cleanup, fmt.Errorf("read %s: %w", userPath, err)
	}

	merged := userBytes
	if spliceFragment {
		fragBytes, err := os.ReadFile(fragPath)
		if err != nil {
			return k3dConfigPath{}, cleanup, fmt.Errorf("read %s: %w", fragPath, err)
		}
		if merged, err = spliceK3dPorts(merged, fragBytes); err != nil {
			return k3dConfigPath{}, cleanup, fmt.Errorf("merge k3d config: %w", err)
		}
	}
	if merged, err = spliceK3dAPIPort(merged, overlay.APIPort); err != nil {
		return k3dConfigPath{}, cleanup, err
	}
	if merged, err = spliceK3dCIDRs(merged, overlay.ClusterCIDR, overlay.ServiceCIDR); err != nil {
		return k3dConfigPath{}, cleanup, err
	}
	if overlay.PublicIssuer {
		if merged, err = spliceK3dIssuer(merged); err != nil {
			return k3dConfigPath{}, cleanup, err
		}
	}
	// Nothing to project: hand back the user's own file so its formatting and
	// comments are untouched and no temp file is created.
	if bytes.Equal(merged, userBytes) {
		return k3dConfigPath{path: userPath}, cleanup, nil
	}

	tmp, err := os.CreateTemp("", "forge-k3d-config-*.yaml")
	if err != nil {
		return k3dConfigPath{}, cleanup, err
	}
	if _, err := tmp.Write(merged); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return k3dConfigPath{}, cleanup, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return k3dConfigPath{}, cleanup, err
	}
	cleanup = func() { _ = os.Remove(tmp.Name()) }
	return k3dConfigPath{path: tmp.Name(), temporary: true}, cleanup, nil
}

// spliceK3dAPIPort projects a DECLARED api_port onto a cluster that also names
// a k3d config file.
//
// `k3d cluster create --config` takes none of the per-flag settings, so for a
// config-file cluster forge previously dropped a declared api_port on the
// floor: the field parsed, validated and then did nothing, and k3d picked a
// random host port instead. A declared value that is silently inert is worse
// than an unsupported one — nothing tells the author their declaration had no
// effect. This projects it the same way the Gateway host ports are projected,
// so `api_port` means the same thing however the cluster is declared.
//
// The config file stays authoritative where it actually speaks. If it already
// carries a kubeAPI.hostPort the file wins when the two agree and forge
// REFUSES when they disagree, rather than picking a winner between two sources
// of truth that both claim to be one.
func spliceK3dAPIPort(userYAML []byte, apiPort int) ([]byte, error) {
	if apiPort <= 0 {
		return userYAML, nil
	}
	var doc map[string]any
	if err := yaml.Unmarshal(userYAML, &doc); err != nil {
		return nil, fmt.Errorf("parse k3d config to set the declared api_port: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	want := strconv.Itoa(apiPort)

	kubeAPI, _ := doc["kubeAPI"].(map[string]any)
	if kubeAPI == nil {
		doc["kubeAPI"] = map[string]any{"hostPort": want}
	} else if existing, ok := kubeAPI["hostPort"]; ok {
		if got := strings.TrimSpace(fmt.Sprintf("%v", existing)); got != want {
			return nil, fmt.Errorf(
				"cluster declares api_port = %d but its k3d config already sets kubeAPI.hostPort: %q — "+
					"remove one of them so the API port has a single source of truth", apiPort, got)
		}
		return userYAML, nil
	} else {
		kubeAPI["hostPort"] = want
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("re-encode k3d config with the declared api_port: %w", err)
	}
	return out, nil
}

// spliceK3dCIDRs projects the DECLARED pod / Service CIDRs onto a cluster that
// also names a k3d config file, as k3s server args under
// `options.k3s.extraArgs`.
//
// Same reasoning as spliceK3dAPIPort: `k3d cluster create --config` takes none
// of the per-flag settings, so without this a config-file cluster would parse
// and validate a `cluster_cidr` and then silently create a cluster on k3s's
// default block. A declaration that is inert is worse than an unsupported one
// — nothing tells the author it had no effect, and the symptom (two clusters
// that still cannot reach each other's pods) looks exactly like the problem
// they were trying to fix.
//
// The `extraArgs` shape is k3d's own: a list of `{arg, nodeFilters}` entries.
// This APPENDS to whatever the file already declares (a config commonly
// carries `--disable=traefik` there) rather than replacing the list.
//
// The config file stays authoritative where it actually speaks: if it already
// sets the same k3s arg, forge accepts a value that AGREES and REFUSES one
// that disagrees, rather than picking a winner between two sources of truth.
func spliceK3dCIDRs(userYAML []byte, clusterCIDR, serviceCIDR string) ([]byte, error) {
	if clusterCIDR == "" && serviceCIDR == "" {
		return userYAML, nil
	}
	var doc map[string]any
	if err := yaml.Unmarshal(userYAML, &doc); err != nil {
		return nil, fmt.Errorf("parse k3d config to set the declared CIDRs: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}

	options, _ := doc["options"].(map[string]any)
	if options == nil {
		options = map[string]any{}
		doc["options"] = options
	}
	k3s, _ := options["k3s"].(map[string]any)
	if k3s == nil {
		k3s = map[string]any{}
		options["k3s"] = k3s
	}
	existing, _ := k3s["extraArgs"].([]any)

	changed := false
	for _, want := range []struct{ flag, value string }{
		{"--cluster-cidr", clusterCIDR},
		{"--service-cidr", serviceCIDR},
	} {
		if want.value == "" {
			continue
		}
		declared, found := k3sExtraArgValue(existing, want.flag)
		if found {
			if declared != want.value {
				return nil, fmt.Errorf(
					"cluster declares %s = %s but its k3d config already sets %s=%s in "+
						"options.k3s.extraArgs — remove one of them so the CIDR has a single "+
						"source of truth",
					strings.TrimPrefix(want.flag, "--"), want.value, want.flag, declared)
			}
			continue // already says exactly this; nothing to add
		}
		existing = append(existing, map[string]any{
			"arg":         want.flag + "=" + want.value,
			"nodeFilters": []any{"server:*"},
		})
		changed = true
	}
	if !changed {
		// Every declared CIDR was already present and agreed. Hand back the
		// user's own bytes so no temp file is written and their formatting and
		// comments survive.
		return userYAML, nil
	}
	k3s["extraArgs"] = existing

	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("re-encode k3d config with the declared CIDRs: %w", err)
	}
	return out, nil
}

// spliceK3dIssuer appends the k3sIssuerArgs to a config's
// options.k3s.extraArgs so a config-file cluster signs with k3sPublicIssuer,
// exactly as the flag path does. The config stays authoritative where it
// speaks: one that already sets a service-account-issuer is returned
// untouched, never merged with a second source of truth.
func spliceK3dIssuer(userYAML []byte) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(userYAML, &doc); err != nil {
		return nil, fmt.Errorf("parse k3d config to set the service-account issuer: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	options, _ := doc["options"].(map[string]any)
	if options == nil {
		options = map[string]any{}
		doc["options"] = options
	}
	k3s, _ := options["k3s"].(map[string]any)
	if k3s == nil {
		k3s = map[string]any{}
		options["k3s"] = k3s
	}
	existing, _ := k3s["extraArgs"].([]any)
	for _, e := range existing {
		if m, ok := e.(map[string]any); ok {
			if arg, _ := m["arg"].(string); strings.HasPrefix(strings.TrimSpace(arg), k3sIssuerArgKey) {
				return userYAML, nil
			}
		}
	}
	for _, a := range k3sIssuerArgs() {
		existing = append(existing, map[string]any{"arg": a, "nodeFilters": []any{"server:*"}})
	}
	k3s["extraArgs"] = existing
	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("re-encode k3d config with the service-account issuer: %w", err)
	}
	return out, nil
}

// k3sExtraArgValue looks for `flag=<value>` among an options.k3s.extraArgs
// list and returns its value. The second result distinguishes "declared as
// empty" from "not declared", which the caller's agree/disagree check needs.
//
// Entries whose shape it does not recognise are skipped rather than treated as
// an error: extraArgs is a list of dicts with an `arg` key, and an entry that
// is not one is either a k3d form forge does not model or a user mistake k3d
// itself will report — neither is a reason to fail the merge here.
func k3sExtraArgValue(entries []any, flag string) (string, bool) {
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		arg, ok := m["arg"].(string)
		if !ok {
			continue
		}
		value, found := strings.CutPrefix(strings.TrimSpace(arg), flag+"=")
		if found {
			return value, true
		}
	}
	return "", false
}

// spliceK3dPorts is the pure YAML-merging half — exposed for tests
// so the merge policy is unit-testable without temp files. The user
// YAML's `ports:` list (if present) is merged with the fragment's
// entries: fragment entries WIN on host-port collisions. Other
// top-level keys pass through unchanged so we don't silently drop
// registries, agent counts, etc.
//
// Host-port extraction handles the canonical `port: <host>:<cluster>`
// shorthand. Entries we can't parse (alternative forms, missing port
// key) are passed through verbatim with a warning — k3d may then
// reject the config, but the warning gives the user a starting point.
func spliceK3dPorts(userYAML, fragmentYAML []byte) ([]byte, error) {
	var userDoc map[string]any
	if err := yaml.Unmarshal(userYAML, &userDoc); err != nil {
		return nil, fmt.Errorf("parse user k3d.yaml: %w", err)
	}
	if userDoc == nil {
		userDoc = map[string]any{}
	}
	var frag map[string]any
	if err := yaml.Unmarshal(fragmentYAML, &frag); err != nil {
		return nil, fmt.Errorf("parse k3d-ports.yaml: %w", err)
	}
	fragPorts, _ := frag["ports"].([]any)
	if len(fragPorts) == 0 {
		// Fragment has no ports — nothing to splice; return user
		// YAML verbatim so we don't disturb formatting/comments.
		return userYAML, nil
	}
	userPorts, _ := userDoc["ports"].([]any)

	// Collect host ports claimed by the fragment. Entries from the
	// user list that share a host port get dropped (fragment wins).
	// Entries we can't classify pass through unchanged — caller logs
	// a warning so the user can investigate if k3d then rejects.
	fragHosts := map[int]bool{}
	for _, e := range fragPorts {
		if host, ok := k3dPortHost(e); ok {
			fragHosts[host] = true
		}
	}

	merged := make([]any, 0, len(userPorts)+len(fragPorts))
	for _, e := range userPorts {
		host, ok := k3dPortHost(e)
		if !ok {
			// Unrecognized shape — keep it, warn the user. We
			// don't crash because the alternative forms (structured
			// `port:` int + hostIP/protocol/nodeFilters siblings) are
			// legitimate k3d config; we just can't dedupe them.
			fmt.Fprintf(os.Stderr, "warning: deploy/k3d.yaml ports[] entry not in canonical <host>:<cluster> shorthand; passing through without dedupe: %v\n", e)
			merged = append(merged, e)
			continue
		}
		if fragHosts[host] {
			// Fragment wins on this host port — drop the user entry.
			continue
		}
		merged = append(merged, e)
	}
	merged = append(merged, fragPorts...)
	userDoc["ports"] = merged

	out, err := yaml.Marshal(userDoc)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// k3dPortHost extracts the host-port integer from a k3d ports[] entry
// in the canonical `port: <host>:<cluster>` shorthand. Returns
// (port, true) on success; (0, false) for anything else (alternative
// structured forms, missing key, bare `port: <int>`). Callers treat
// the false case as "don't dedupe this entry".
func k3dPortHost(entry any) (int, bool) {
	m, ok := entry.(map[string]any)
	if !ok {
		return 0, false
	}
	raw, ok := m["port"].(string)
	if !ok {
		return 0, false
	}
	hostStr, _, ok := strings.Cut(raw, ":")
	if !ok {
		// Bare `port: 18080` (no cluster side) — possible in k3d but
		// not the shape we emit. Bail out of the dedupe; caller will
		// pass through with a warning.
		return 0, false
	}
	host, err := strconv.Atoi(strings.TrimSpace(hostStr))
	if err != nil {
		return 0, false
	}
	return host, true
}

// runningClusterHostPorts returns the SET of host ports the LIVE k3d
// cluster publishes on its serverlb (loadbalancer) container. k3d maps
// the config's ports[] onto the serverlb container at create time; those
// published ports ARE the host-port surface, and they are FIXED for the
// life of the cluster (k3d can't add a port map to a running cluster). So
// this is the authoritative "what the running cluster actually maps".
//
// The serverlb container is named `k3d-<cluster>-serverlb`. We read its
// published host ports via `docker port`, which prints lines like
//
//	28080/tcp -> 0.0.0.0:28080
//
// The host side (after the last colon) is the mapped host port. A cluster
// with no serverlb (e.g. --no-lb) yields an empty set, which the drift
// check treats as "can't verify" rather than "everything is missing".
var runningClusterHostPortsFn = runningClusterHostPorts

func runningClusterHostPorts(ctx context.Context, clusterName string) (map[int]bool, error) {
	container := "k3d-" + clusterName + "-serverlb"
	out, err := exec.CommandContext(ctx, "docker", "port", container).Output()
	if err != nil {
		// No serverlb / docker hiccup — return empty so the caller skips
		// the drift check rather than falsely flagging every port missing.
		return map[int]bool{}, nil
	}
	ports := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// "<cluster>/tcp -> 0.0.0.0:<host>" — take the field after the
		// last colon as the host port.
		idx := strings.LastIndex(line, ":")
		if idx < 0 {
			continue
		}
		if host, err := strconv.Atoi(strings.TrimSpace(line[idx+1:])); err == nil {
			ports[host] = true
		}
	}
	return ports, nil
}

// clusterPortDrift compares the host ports the env's RENDERED Gateway
// listeners REQUIRE (required) against the host ports the LIVE cluster
// actually publishes, and returns the required ports the running cluster is
// MISSING (sorted ascending). An empty result means the live cluster maps
// every listener the env needs — no drift.
//
// `required` is DERIVED from the SAME render `forge env up` deploys (the env's
// Gateway listeners — see renderedGatewayHostPorts), NOT from the static
// deploy/k3d.yaml port block. That's the single-source-of-truth contract:
// drift is "a listener this env renders has no host port on the running
// cluster", nothing more. It deliberately does NOT flag the pre-mapped
// parallel-worktree blocks a project may declare statically for FUTURE
// stacks — those aren't routes THIS bring-up needs, so missing them is not
// a reason to force a recreate.
//
// This catches the failure the derivation alone can't: k3d fixes port maps
// at create time, so a Gateway listener ADDED after the cluster exists
// (e.g. the dev controller listener) renders a host port the running
// cluster never mapped — the route is silently unreachable. The caller
// turns a non-empty result into a clear "recreate the cluster" error.
func clusterPortDrift(ctx context.Context, clusterName string, required map[int]bool) ([]int, error) {
	if len(required) == 0 {
		return nil, nil
	}
	running, err := runningClusterHostPortsFn(ctx, clusterName)
	if err != nil {
		return nil, err
	}
	// Empty running set = couldn't read the serverlb; don't flag drift on
	// no evidence (avoids a spurious "recreate" on a transient docker error).
	if len(running) == 0 {
		return nil, nil
	}
	var missing []int
	for p := range required {
		if !running[p] {
			missing = append(missing, p)
		}
	}
	sort.Ints(missing)
	return missing, nil
}

// renderedGatewayHostPorts returns the host-port set the env's RENDERED
// Gateway listeners require — the EXACT listeners `forge env up`/`forge env deploy`
// produce (transform-added listeners like the dev `controller` included).
// This is the single source of truth the drift check reasons about. A
// render failure or an env with no gateways yields an empty set (the caller
// treats that as "nothing to verify").
func renderedGatewayHostPorts(ctx context.Context, projectDir, env string) (map[int]bool, error) {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return nil, err
	}
	ports := map[int]bool{}
	if entities == nil {
		return ports, nil
	}
	for _, gw := range entities.Gateways {
		for _, l := range gw.Listeners {
			if l.Port > 0 {
				ports[l.Port] = true
			}
		}
	}
	return ports, nil
}
