// Package cluster — helm-as-a-RENDERER for declarative platform deps.
//
// THE MODEL: helm is a RENDERER, not an installer. A forge.HelmChart is a
// platform dependency declared in the SAME env Bundle as the app. forge
// runs `helm template <chart> --version <v> --values <vals> -n <ns>
// --skip-crds` to expand the chart to a manifest list, stamps every
// rendered manifest with `app.kubernetes.io/name = <name>`, and folds
// those manifests into the SAME render → kubectl-apply → wait pipeline
// every other forge manifest flows through (Apply, below). There is NO
// `helm install`, NO helm-managed release, NO imperative installer.
//
// SELECTION IS `--target`. Because each chart's manifests carry the
// chart's `name` as their `app.kubernetes.io/name` GROUP, the SAME
// exclusive `--target` axis (SelectManifestsByGroup) selects them with no
// new tag — a chart is just another manifest group:
//
//	forge env deploy <env> --target <name>   # render+apply ONLY this group
//	forge env deploy <env>                   # apply EVERYTHING (every group +
//	                                     # every declared platform dep)
//
// APPLY ORDERING (the one real problem). A chart's controllers reference
// CRDs that must exist + be Established first. The chart's bundled
// STANDARD Gateway-API CRDs (group gateway.networking.k8s.io) are excluded
// from the render (`--skip-crds`) because the chart's copy is often older /
// experimental-channel and trips the `safe-upgrades`
// ValidatingAdmissionPolicy, making the install self-deny. forge SUPPLIES
// that group itself at a pinned version (the caller fetches the pinned
// standard Gateway API CRDs / cert-manager's CRDs at the matching chart
// version and hands them in as HelmChartSpec.CRDs).
//
// `--skip-crds` is necessary but NOT sufficient. Envoy Gateway 1.9 moved the
// safe-upgrades ValidatingAdmissionPolicy itself from the CRD bundle into the
// chart's TEMPLATES, which --skip-crds does not touch — so the chart would
// render a second copy of a resource forge already applies from its pinned
// bundle. gatewayAPIChartValues turns that template off for any chart whose
// Gateway API CRDs forge supplies, keeping ONE source for the whole group.
//
// But a chart ALSO ships its OWN, non-Gateway-API CRDs the controller
// needs — envoy-gateway's eight `gateway.envoyproxy.io` CRDs the controller
// starts informers on; `--skip-crds` would drop those too and the controller
// crashloops on cache-sync. So forge ADDITIONALLY renders the chart
// `--include-crds` (chartOwnCRDs) and supplies the chart's NON-standard-
// Gateway-API CRDs alongside the pinned bundle. Net CRD set = pinned standard
// Gateway-API + the chart's own CRDs.
//
// Apply applies that combined CRD set (+ the chart's synthesized Namespace,
// which `helm template` never emits) FIRST and waits until the CRDs are
// Established before the chart's controller manifests; then waits for the
// chart's Deployments to be Available before the chart's riding manifests
// (the cert-manager webhook would reject ClusterIssuers until then). See
// applyCRDsThenRest, RenderHelmChart, waitChartDeploymentsAvailable.
package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// HelmChartSpec is one declared platform dependency, resolved from the
// KCL `output.helm_charts` projection plus the caller-fetched CRD bundle.
// The CLI (internal/cli) builds these from KCLEntities and fetches CRDs
// via the existing pinned-CRD machinery (internal/cli/dev_cluster_ingress.go),
// keeping this package free of the templates/cache dependency.
type HelmChartSpec struct {
	// Name is the platform dep's NAME — the `--target` selector and the
	// `app.kubernetes.io/name` stamped on every rendered manifest.
	Name string
	// Chart is the chart name for a repo chart (e.g. "cert-manager").
	// Empty for an OCI chart (OCI carries the full ref).
	Chart string
	// Repo is the chart-repo URL (e.g. "https://charts.jetstack.io").
	// Mutually exclusive with OCI.
	Repo string
	// OCI is the OCI chart ref (e.g.
	// "oci://docker.io/envoyproxy/gateway-helm"). Mutually exclusive with Repo.
	OCI string
	// Version is the pinned chart version (e.g. "v1.20.1").
	Version string
	// Namespace is the namespace to render the chart into
	// (`helm template -n <namespace>`).
	Namespace string
	// Values is the helm values overlay (the `--values` file content),
	// passed through verbatim from the KCL `values` dict.
	Values map[string]any
	// CRDs is the forge-supplied CRD manifest YAML applied FIRST (before
	// the chart's controllers) and waited until Established. The caller
	// fetches it (pinned standard Gateway API CRDs / cert-manager CRDs at
	// the chart version). Empty when the chart needs no forge CRDs.
	CRDs string
	// CRDBundle NAMES the forge-supplied bundle CRDs holds the content of
	// ("gateway-api", "cert-manager", or empty). CRDs alone cannot answer
	// "which bundle is this?" — it is megabytes of YAML — and the render
	// has to know, because a chart whose Gateway API CRDs forge supplies
	// must also be told not to render the resources that ship ALONGSIDE
	// those CRDs upstream. See gatewayAPIChartValues.
	CRDBundle string
	// Manifests is the consumer-declared raw manifest YAML (a `---`-joined
	// stream) that rides this chart's `--target`: the cluster-scoped
	// instances a chart's controller reconciles but the chart doesn't ship
	// (the `eg` GatewayClass, cert-manager ClusterIssuers). Applied AFTER
	// the chart's controllers (so the controller is up before its
	// instances), stamped with the chart's app-label like the chart's own
	// output, so they ride the chart's GROUP under the exclusive `--target`
	// filter (selected iff no targets, or the chart's Name ∈ targets — the
	// same rule as the chart itself). Empty when the chart carries none.
	Manifests string
	// Cluster, when non-empty, is the kubectl CONTEXT this chart installs
	// into, overriding the env's primary cluster for this chart alone. It
	// is the denormalized `.context` of the `forge.Cluster` the chart's KCL
	// `cluster` field references (the CLI resolves the reference and
	// validates it against the env's declared clusters before building this
	// spec — see cli.helmChartSpecsFromEntities).
	//
	// THE GAP THIS CLOSES. An operator must be installed where the custom
	// resources it reconciles land, and in a multi-cluster env that is not
	// the primary cluster. control-plane's dev installs CNPG while its
	// managed-database tier renders `postgresql.cnpg.io/v1 Cluster` objects
	// into the cp-daemon cluster; with the operator on the control-plane
	// cluster the CR apply fails `no matches for kind "Cluster" in version
	// "postgresql.cnpg.io/v1"`.
	//
	// Empty => the env's primary cluster, so every pre-existing chart
	// declaration applies through the identical argv it did before.
	Cluster string

	// NamespaceLabels and NamespaceAnnotations are written onto the Namespace
	// forge creates for the chart (helm template never emits it). The
	// Namespace rides the chart's server-side apply, so a re-apply sets
	// exactly the declared keys, drops a key that is no longer declared (the
	// same field manager owned it), and leaves every key another manager set.
	//
	// PRECEDENCE: the labels forge stamps on that Namespace are forge's, and
	// a declaration may not set them (forgeOwnedNamespaceLabel). That is
	// enforced by REFUSING the spec in RenderHelmChart, as the KCL schema
	// refuses the declaration, rather than by picking a winner. Either
	// silent outcome is wrong: if forge's value won, the declared one would
	// vanish, and if the declared one won, forge's routing and ownership
	// stamps would change without anyone deciding to change them.
	NamespaceLabels      map[string]string
	NamespaceAnnotations map[string]string
	// Owner is the (project, env) that applies this chart — ChartOwner's
	// value. Every object the chart renders is stamped with it
	// (ChartOwnerLabel), and the chart's inventory is recorded per owner. The
	// prune after a successful apply deletes only objects that an earlier
	// apply recorded in that inventory and that still carry the stamp
	// (chart_prune.go). Empty disables the stamp, the inventory and the prune:
	// an owner that cannot be named cannot be proven.
	Owner string
	// Prune enables that prune for this chart. The zero value is OFF so a
	// spec built by hand never deletes anything by accident; the KCL
	// declaration defaults it to on.
	Prune bool
	// Patches are merge patches applied to the objects `helm template`
	// rendered, keyed `Kind/name` (the Bundle.overrides grammar, minus the
	// cluster qualifier). They reach what a chart's values cannot — a field
	// the chart does not template, such as a pod's runtimeClassName. Every
	// key must match exactly one rendered object.
	Patches map[string]any
}

// renderedChart pairs a chart spec with its rendered (stamped,
// --skip-crds) manifest stream so Apply can apply each chart's
// forge-supplied CRDs first and its controllers after.
type renderedChart struct {
	spec      HelmChartSpec
	manifests string
	// crds is the FULL CRD set applied (Established-gated) before this chart's
	// controllers: forge's pinned forge-supplied bundle (spec.CRDs — e.g. the
	// standard Gateway API v1.5.1) PLUS the chart's OWN CRDs that forge does
	// not own (the chart's `gateway.envoyproxy.io` / x-k8s.io CRDs, rendered
	// via `helm template --include-crds` and filtered to exclude the standard
	// Gateway API group forge pins). See RenderHelmChart / chartOwnCRDs.
	crds string
	// extra is the chart's consumer-declared raw manifests (HelmChartSpec.
	// Manifests), stamped with the chart's app-label, applied AFTER the
	// chart's controllers. Empty when the chart carries none.
	extra string
}

// chartContext resolves which kubectl context ONE chart's whole apply
// sequence runs against: the chart's own declared cluster when it names one
// (HelmChartSpec.Cluster, the denormalized `.context` of the referenced
// forge.Cluster), else the env's primary context the caller passed in.
//
// This is the single resolution point on purpose. The chart's sequence makes
// four kinds of kubectl call — the CRD/Namespace apply, the
// `wait --for=condition=Established` on those CRDs, the
// `wait --for=condition=Available` on the controllers, and the riding-manifest
// applies — and a context threaded into some but not all of them is worse than
// no re-targeting at all: an Established wait against the primary apiserver
// reports a CRD the TARGET cluster does not have, so the deploy would install
// the operator nowhere useful and still report success.
//
// Empty Cluster returns envContext unchanged, which is what keeps every
// existing chart declaration on byte-identical argv.
func chartContext(envContext string, spec HelmChartSpec) string {
	if c := strings.TrimSpace(spec.Cluster); c != "" {
		return c
	}
	return envContext
}

// selectHelmChartsByGroup applies the ONE uniform exclusive `--target`
// rule to the platform deps: a chart's NAME is its GROUP, so a chart is
// rendered iff (no targets) OR (its Name ∈ targets) — the IDENTICAL rule
// SelectManifestsByGroup applies to every other manifest. There is NO
// chart opt-in/opt-out asymmetry: a bare `forge env deploy <env>` (empty
// targets) reconciles every declared platform dep, exactly as it
// reconciles every app manifest.
//
// So `--target=cert-manager` renders ONLY the cert-manager chart; an empty
// target renders ALL charts; a target naming no chart renders no chart
// (its manifests, if any, are selected by SelectManifestsByGroup instead).
func selectHelmChartsByGroup(charts []HelmChartSpec, targets []string) []HelmChartSpec {
	if len(targets) == 0 {
		return charts
	}
	want := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		want[t] = struct{}{}
	}
	var selected []HelmChartSpec
	for _, c := range charts {
		if _, ok := want[c.Name]; ok {
			selected = append(selected, c)
		}
	}
	return selected
}

// helmTemplate runs `helm template <release> <chart-ref> --version <v>
// --namespace <ns> --skip-crds [--repo <url>] [--values <file>]` and
// returns the rendered `---`-separated manifest stream.
//
// `--skip-crds` is LOAD-BEARING: the chart's bundled CRDs never enter the
// stream (see the package doc). forge supplies the correct CRDs out of
// band (HelmChartSpec.CRDs, applied first by Apply).
//
// Repo vs OCI: a repo chart passes `--repo <url> <chart-name>`; an OCI
// chart passes the OCI ref as the chart argument with no `--repo`. Both
// are fully version-pinned and offline-capable once helm has cached them.
func helmTemplate(ctx context.Context, spec HelmChartSpec) (string, error) {
	args := []string{
		"template", spec.Name,
		"--version", spec.Version,
		"--namespace", spec.Namespace,
		"--skip-crds",
		// include-crds is OFF by default; --skip-crds is explicit + future-proof.
	}
	args = append(args, gatewayAPIChartValues(spec)...)

	chartRef := spec.OCI
	if chartRef == "" {
		// Repo chart: `helm template <name> <chart> --repo <url>`.
		chartRef = spec.Chart
		args = append(args, "--repo", spec.Repo)
	}

	var valuesFile string
	if len(spec.Values) > 0 {
		f, err := writeValuesFile(spec.Values)
		if err != nil {
			return "", fmt.Errorf("helm chart %q: write values: %w", spec.Name, err)
		}
		defer func() { _ = os.Remove(f) }()
		valuesFile = f
		args = append(args, "--values", valuesFile)
	}

	// The chart ref is the positional argument; append last.
	args = append(args, chartRef)

	var lastErr error
	var lastStderr string
	for attempt := 1; attempt <= helmTemplateAttempts; attempt++ {
		cmd := exec.CommandContext(ctx, "helm", args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			return stripHelmOCIStatus(stdout.String()), nil
		}
		lastErr, lastStderr = err, stderr.String()
		if !transientChartFetchFailure(lastStderr) || attempt == helmTemplateAttempts {
			break
		}
		// The chart is REMOTE, so rendering it is a network operation, and a
		// truncated blob is a fault in the transfer rather than in the chart.
		// Measured against a local inspection proxy, the same pull failed about
		// one attempt in three and succeeded on the next — without a retry that
		// makes an unrelated deploy fail at random.
		fmt.Printf("  helm %s: chart fetch failed mid-transfer (%s) — retrying (%d/%d)\n",
			spec.Name, firstLine(lastStderr), attempt, helmTemplateAttempts)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(helmTemplateRetryDelay):
		}
	}
	return "", fmt.Errorf("helm template %s (%s@%s): %w\n%s",
		spec.Name, chartRef, spec.Version, lastErr, lastStderr)
}

// helmTemplateAttempts / helmTemplateRetryDelay bound the retry for a chart
// pull that failed in TRANSFER. Vars so tests can shorten them.
var (
	helmTemplateAttempts   = 3
	helmTemplateRetryDelay = 750 * time.Millisecond
)

// transportFailureSignatures name failures that can ONLY come from the network
// layer, so they identify a transient transfer on their own.
var transportFailureSignatures = []string{
	"connection reset by peer",
	"TLS handshake timeout",
	"i/o timeout",
	"unexpected status from GET request",
	"proxyconnect",
}

// truncatedTransferSignatures are ambiguous on their own — a chart whose
// TEMPLATE is malformed can also report "unexpected EOF" — so they count only
// alongside evidence that the failure happened while FETCHING. Without that
// pairing a genuine template syntax error would be retried twice before
// reporting the same verdict.
var (
	truncatedTransferSignatures = []string{"unexpected EOF", "EOF"}
	fetchContextSignatures      = []string{`"Fetch" on source`, "FetchReference", "failed to perform", "https://", "http://"}
)

// transientChartFetchFailure reports whether a chart pull died in TRANSFER
// rather than being a chart that is wrong. A bad version, a missing repo, an
// auth failure or a broken template must still fail on the first attempt.
func transientChartFetchFailure(stderr string) bool {
	for _, sig := range transportFailureSignatures {
		if strings.Contains(stderr, sig) {
			return true
		}
	}
	fetching := false
	for _, sig := range fetchContextSignatures {
		if strings.Contains(stderr, sig) {
			fetching = true
			break
		}
	}
	if !fetching {
		return false
	}
	for _, sig := range truncatedTransferSignatures {
		if strings.Contains(stderr, sig) {
			return true
		}
	}
	return false
}

// firstLine renders the leading line of a multi-line stderr for a one-line
// progress message.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 100
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// stripHelmOCIStatus removes Helm 4's OCI pull receipt from stdout. Helm 3
// wrote these lines to stderr, but Helm 4 prefixes successful `helm template
// oci://...` output with a standalone YAML-looking document:
//
//	Pulled: <ref>
//	Digest: sha256:...
//
// Forge stamps every rendered document before applying it. Without this
// boundary cleanup, the receipt gains metadata but still has no apiVersion or
// kind, and kubectl rejects the whole stream. Only an initial document made
// entirely of the two known receipt keys is removed; arbitrary chart output is
// preserved so malformed manifests still fail loudly at apply time.
func stripHelmOCIStatus(manifests string) string {
	docs := splitDocs(manifests)
	if len(docs) == 0 {
		return ""
	}
	lines := strings.Split(docs[0], "\n")
	isReceipt := len(lines) > 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "Pulled:") && !strings.HasPrefix(trimmed, "Digest:") {
			isReceipt = false
			break
		}
	}
	if isReceipt {
		docs = docs[1:]
	}
	return strings.Join(docs, docDelimiter)
}

// writeValuesFile marshals a helm values dict to a temp YAML file and
// returns its path. helm accepts JSON as a --values file (JSON is a YAML
// subset), so we marshal as JSON to avoid a YAML dependency on the
// arbitrary nested values shape.
func writeValuesFile(values map[string]any) (string, error) {
	b, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "forge-helm-values-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// RenderHelmChart expands one HelmChart to its manifest stream, with every
// rendered manifest stamped `app.kubernetes.io/name = spec.Name` so the
// SAME exclusive `--target` axis (SelectManifestsByGroup) selects it. This is the
// whole bridge from "a declared platform dep" to "manifests in the normal
// apply stream": the result joins render output and flows through the same
// filter → apply → wait pipeline as the app.
//
// The chart's CRDs (HelmChartSpec.CRDs) are NOT included here — they are
// applied first by Apply (CRDs → wait Established → rest); RenderHelmChart
// returns only the `--skip-crds` controller/RBAC/Service manifests.
func RenderHelmChart(ctx context.Context, spec HelmChartSpec) (string, error) {
	if err := validateNamespaceMetadata(spec); err != nil {
		return "", err
	}
	rendered, err := helmTemplate(ctx, spec)
	if err != nil {
		return "", err
	}
	// helm template does NOT emit the chart's target Namespace, and a chart's
	// rendered resources are namespaced into spec.Namespace (cert-manager →
	// cert-manager, envoy → envoy-gateway-system). On a cold cluster the
	// first config-pass apply then fails `namespaces "<ns>" not found`. So
	// forge EMITS the Namespace itself, stamped with the chart's group so it
	// rides the chart's --target, and applyCRDsThenRest promotes it into the
	// early (CRD) batch so it lands+waits BEFORE the namespaced resources.
	withNS := joinNonEmpty(namespaceManifest(spec.Namespace, spec.NamespaceLabels, spec.NamespaceAnnotations), rendered)
	// Drop helm POST-phase / test hooks (cert-manager-startupapicheck): a
	// `helm template` render carries `helm.sh/hook`-annotated docs that are
	// meaningless under a declarative reconcile and, being immutable Jobs,
	// force a delete/recreate on every warm redeploy. PRE-phase hooks
	// (envoy-gateway's certgen, which creates the controller's serving-cert
	// Secret) are KEPT — they produce prerequisite cluster state the
	// workloads mount, not a post-facto check. See dropPostHelmHooks.
	withNS = dropPostHelmHooks(withNS)
	withNS, err = applyChartPatches(spec.Name, withNS, spec.Patches)
	if err != nil {
		return "", err
	}
	return stampAppLabel(withNS, spec.Name, spec.Owner), nil
}

// applyChartPatches merges HelmChart.patches into the rendered stream. It
// reuses the Bundle.overrides engine, so a patch merges the way an override
// does (strategic for a built-in kind, RFC 7386 for a CRD, `null` deletes) and
// a key that matches nothing, or two objects, is an error naming the chart and
// the candidates rather than a silently ignored patch. A chart with no patches
// is returned byte for byte.
func applyChartPatches(chart, manifests string, patches map[string]any) (string, error) {
	if len(patches) == 0 {
		return manifests, nil
	}
	overrides := make(map[string]map[string]any, len(patches))
	for k, v := range patches {
		patch, ok := v.(map[string]any)
		if !ok {
			return "", fmt.Errorf("helm chart %q: patches[%q] must be a map of the fields to change, got %T", chart, k, v)
		}
		overrides[k] = patch
	}
	items, err := decodeObjectStream(manifests)
	if err != nil {
		return "", fmt.Errorf("helm chart %q: patches need a readable render: %w", chart, err)
	}
	patched, _, err := applyOverrides(items, overrides, nil)
	if err != nil {
		return "", fmt.Errorf("helm chart %q: patches: %w", chart, err)
	}
	docs := make([]string, 0, len(patched))
	for _, it := range patched {
		b, err := yaml.Marshal(it)
		if err != nil {
			return "", fmt.Errorf("helm chart %q: encode patched object: %w", chart, err)
		}
		docs = append(docs, strings.TrimRight(string(b), "\n"))
	}
	return strings.Join(docs, docDelimiter), nil
}

// forgeOwnedNamespaceLabel reports whether key is a label forge (or the
// apiserver) writes on the Namespace forge creates for a chart, so a chart's
// NamespaceLabels may not declare it:
//
//   - `forge.dev/*`: WorkloadLabel routes the Namespace on the chart's
//     `--target`, and ChartOwnerLabel is the ownership proof the chart prune
//     relies on.
//   - AppNameLabel and AppManagedByLabel: stampDocAppLabel only DEFAULTS
//     these, so that a chart's own `app.kubernetes.io/name` survives for the
//     selectors that match on it. On the Namespace no chart wrote them, so the
//     only competing value is a declared one, and a declared one would have
//     silently replaced forge's.
//   - `kubernetes.io/metadata.name`: the apiserver sets it to the Namespace's
//     name and refuses any other value.
//
// kcl/schema.k's HelmChart check refuses the same keys with the same reason.
func forgeOwnedNamespaceLabel(key string) bool {
	switch key {
	case AppNameLabel, AppManagedByLabel, "kubernetes.io/metadata.name":
		return true
	}
	return strings.HasPrefix(key, "forge.dev/")
}

// validateNamespaceMetadata refuses a chart spec whose Namespace metadata sets
// a key forge owns (forgeOwnedNamespaceLabel, and `forge.dev/*` annotations).
// The KCL schema refuses the declaration first. This repeats the check at the
// one seam every render passes through, so a spec built another way is refused
// too instead of being resolved in silence.
func validateNamespaceMetadata(spec HelmChartSpec) error {
	var bad []string
	for k := range spec.NamespaceLabels {
		if forgeOwnedNamespaceLabel(k) {
			bad = append(bad, "label "+k)
		}
	}
	for k := range spec.NamespaceAnnotations {
		if strings.HasPrefix(k, "forge.dev/") {
			bad = append(bad, "annotation "+k)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("helm chart %q: namespace metadata may not set %s: forge stamps those on the Namespace it creates "+
		"for the chart (forge.dev/* routes and proves ownership, app.kubernetes.io/name and app.kubernetes.io/managed-by "+
		"name the chart and forge) and the apiserver owns kubernetes.io/metadata.name - drop the key",
		spec.Name, strings.Join(bad, ", "))
}

// namespaceManifest renders the `kind: Namespace` document for ns, or "" when
// ns is empty (a chart with no namespace renders into whatever the apply
// default is — nothing to create). labels and annotations are the chart's
// declared NamespaceLabels / NamespaceAnnotations, which validateNamespaceMetadata
// has already checked hold none of forge's keys; stampAppLabel adds the chart
// group, owner, name and managed-by afterwards so it rides the chart's
// --target and applyRenderedCharts promotes it ahead of the namespaced
// resources. Idempotent at apply time (already-exists is fine under
// server-side apply).
func namespaceManifest(ns string, labels, annotations map[string]string) string {
	if strings.TrimSpace(ns) == "" {
		return ""
	}
	meta := map[string]any{"name": ns}
	if len(labels) > 0 {
		meta["labels"] = labels
	}
	if len(annotations) > 0 {
		meta["annotations"] = annotations
	}
	b, err := yaml.Marshal(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": meta})
	if err != nil {
		// A map[string]string and two strings always marshal; this is unreachable.
		return "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: " + ns
	}
	return strings.TrimRight(string(b), "\n")
}

// dropPostHelmHooks removes docs whose `helm.sh/hook` annotation is
// EXCLUSIVELY a post-phase or test hook (post-install / post-upgrade /
// post-rollback / post-delete / test) from a `---`-separated stream. Those
// hooks (cert-manager's cert-manager-startupapicheck Job + its RBAC) are
// post-facto CHECKS: under `helm template` they're inert, and left in the
// render the startupapicheck Job's immutable spec.template forces the
// immutable-Job delete/recreate recovery on EVERY warm redeploy. Dropping
// them keeps the declarative reconcile a true no-op when nothing changed.
//
// PRE-phase hooks are KEPT. The distinction is correctness, not cosmetics: a
// pre-install/pre-upgrade hook PRODUCES prerequisite cluster state the
// workloads depend on — envoy-gateway's `certgen` Job (pre-install,
// pre-upgrade) generates the `envoy-gateway` serving-cert Secret the
// controller pod mounts; drop it and the controller wedges forever on
// `MountVolume.SetUp failed ... secret "envoy-gateway" not found`. Helm's own
// `upgrade --install` runs that certgen on every install/upgrade, so keeping
// it (it reconciles as a plain Job, healed by the immutable-Job recovery on a
// warm redeploy) matches the imperative install's behaviour exactly.
//
// A doc with no hook annotation, or a mixed pre+post hook, is KEPT (only a
// purely post/test hook is dropped). Docs that don't parse pass through.
func dropPostHelmHooks(manifests string) string {
	var kept []string
	for _, doc := range splitDocs(manifests) {
		if isPostOnlyHelmHook(doc) {
			continue
		}
		kept = append(kept, doc)
	}
	return strings.Join(kept, docDelimiter)
}

// postPhaseHelmHooks is the set of `helm.sh/hook` values that mark a doc as a
// post-facto / test hook (vs a pre-phase hook that sets up prerequisites).
var postPhaseHelmHooks = map[string]struct{}{
	"post-install":  {},
	"post-upgrade":  {},
	"post-rollback": {},
	"post-delete":   {},
	"test":          {},
	"test-success":  {},
}

// isPostOnlyHelmHook reports whether a doc carries a `helm.sh/hook` annotation
// whose phases are ALL post-phase/test (so it's safe to drop under a
// declarative reconcile). A doc with no hook annotation returns false (kept);
// a doc with ANY pre-phase hook returns false (kept — it may set up
// prerequisite state). The annotation is a comma-separated list
// (`post-install,post-upgrade`).
func isPostOnlyHelmHook(doc string) bool {
	var m struct {
		Metadata struct {
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		return false
	}
	raw, ok := m.Metadata.Annotations["helm.sh/hook"]
	if !ok || strings.TrimSpace(raw) == "" {
		return false
	}
	for _, h := range strings.Split(raw, ",") {
		if _, post := postPhaseHelmHooks[strings.TrimSpace(h)]; !post {
			// A non-post (pre-phase) hook — keep the whole doc.
			return false
		}
	}
	return true
}

// gatewayAPIChartValues returns the `--set` arguments a chart needs when
// forge supplies its Gateway API CRDs out of band, or nil for every other
// chart.
//
// `--skip-crds` used to be enough. Envoy Gateway 1.9 moved the Gateway API
// safe-upgrades ValidatingAdmissionPolicy (and its binding) OUT of the CRD
// bundle and INTO the chart's templates, and --skip-crds does not skip a
// TEMPLATE. So the chart now renders a second copy of a resource that is
// already inside the standard-install bundle forge pins and applies itself —
// two sources, two versions, each overwriting the other on alternate deploys.
//
// The policy denies CRD writes that look like a downgrade or a channel
// switch, so the copy that wins decides whether forge's NEXT pinned bump is
// admitted at all. That makes a self-denying install the failure mode, which
// is exactly what forge supplying the bundle is meant to prevent.
//
// Upstream's answer for "the Gateway API resources are managed outside this
// chart" is this value, so forge sets it whenever it owns the bundle rather
// than naming envoy-gateway: any chart that delegates its Gateway API CRDs to
// forge has the same conflict, and keying off the declared bundle keeps the
// rule where the ownership decision already lives.
//
// Harmless on a chart with no such value — `--set` only defines a key, and a
// chart that reads nothing at that path renders identically.
func gatewayAPIChartValues(spec HelmChartSpec) []string {
	if spec.CRDBundle != "gateway-api" {
		return nil
	}
	return []string{"--set", "crds.gatewayAPI.safeUpgradePolicy.enabled=false"}
}

// standardGatewayAPIGroup is the CRD group forge OWNS at a pinned version
// (the standard-channel Gateway API CRDs). A chart that bundles its
// OWN copy of this group (envoy gateway-helm ships an older,
// experimental-channel copy) must NOT have it supplied from the chart render
// — forge pins it separately so the chart's copy never trips the
// safe-upgrades ValidatingAdmissionPolicy. chartOwnCRDs filters this group
// out of the `--include-crds` render.
const standardGatewayAPIGroup = "gateway.networking.k8s.io"

// envoyGatewayOwnGroup is Envoy Gateway's OWN CRD group — the eight kinds its
// controller starts informers on, and the group forge's rendered Backend /
// BackendTrafficPolicy live in. Named for the tests that assert the harvest
// yields this group and nothing else.
const envoyGatewayOwnGroup = "gateway.envoyproxy.io"

// experimentalChannelAnnotation is the annotation upstream stamps on every CRD
// in an EXPERIMENTAL-channel Gateway API bundle, and the one the safe-upgrades
// ValidatingAdmissionPolicy reads to deny an experimental CRD written over a
// standard one.
//
// It is the discriminator chartOwnCRDs filters on, because the GROUP NAME is
// not sufficient. gateway-helm's bundled gatewayapi-crds.yaml spans TWO groups
// — `gateway.networking.k8s.io` AND `gateway.networking.x-k8s.io` (xbackends,
// xbackendtrafficpolicies, xmeshes) — so excluding the standard group by name
// left the x-k8s.io three in the harvest and forge applied experimental CRDs
// out of the very file it means to exclude. Every CRD in that file carries this
// annotation; EG's own generated CRDs carry none. So the annotation separates
// the two sources exactly, and keeps doing so if upstream regroups the bundle.
const experimentalChannelAnnotation = "gateway.networking.k8s.io/channel: experimental"

// chartOwnCRDs renders a chart with `--include-crds` and returns the chart's
// OWN CRDs that forge does NOT already own — i.e. every CRD in the chart's
// crds/ EXCEPT the standard Gateway API group (standardGatewayAPIGroup),
// which forge supplies separately at its pinned version.
//
// THE BUG THIS FIXES (K3): the envoy gateway-helm chart's controller starts
// informers on its EIGHT `gateway.envoyproxy.io` CRDs (envoyproxies,
// clienttrafficpolicies, backendtrafficpolicies, securitypolicies,
// envoypatchpolicies, envoyextensionpolicies, httproutefilters, backends).
// With the main render `--skip-crds` those CRDs never land → the controller's
// cache-sync fails → it never goes Ready → ingress is dead. forge must supply
// the chart's OWN CRDs IN ADDITION to the standard Gateway API CRDs it pins.
//
// Why the chart's Gateway API bundle is filtered OUT: the chart bundles an
// EXPERIMENTAL-channel copy of the Gateway API CRDs that the safe-upgrades
// ValidatingAdmissionPolicy (activated by forge's pinned standard channel)
// DENIES. forge keeps owning that surface at its own pin; only the chart's own
// CRDs are taken from the chart render. A chart that bundles no CRDs
// (cert-manager, rendered --skip-crds and supplied separately) yields "" here.
//
// THE VERSION TIE. The CRDs are harvested from the SAME chart, resolved at the
// SAME version, as the controller manifests forge applies beside them — not
// from a vendored copy or a release URL. So a chart bump moves the CRDs and the
// controller together, and there is no second list that can silently disagree
// with the pin. That is the tie TestChartOwnCRDs_RealChartYieldsOnlyEnvoyGatewaysOwnCRDs
// asserts structurally.
func chartOwnCRDs(ctx context.Context, spec HelmChartSpec) (string, error) {
	rendered, err := helmTemplateIncludeCRDs(ctx, spec)
	if err != nil {
		return "", err
	}
	return keepChartOwnCRDs(rendered), nil
}

// keepChartOwnCRDs is the pure filter over an `--include-crds` render: it keeps
// the CRDs forge does NOT own and drops the chart's experimental Gateway API
// bundle whole. Split from chartOwnCRDs so the exclusion rule — the part that
// was wrong — is testable without a network fetch.
func keepChartOwnCRDs(rendered string) string {
	var kept []string
	for _, doc := range splitDocs(rendered) {
		m, ok := parseDoc(doc)
		if !ok || m.Kind != "CustomResourceDefinition" {
			continue
		}
		// forge owns the standard Gateway API channel at its pinned version —
		// never from the chart.
		if crdGroup(doc) == standardGatewayAPIGroup {
			continue
		}
		// The rest of the chart's Gateway API bundle. Excluding it by GROUP
		// NAME missed the `gateway.networking.x-k8s.io` CRDs that share the
		// file, so forge applied experimental CRDs the controller is not even
		// granted RBAC to watch. The channel annotation is what identifies the
		// bundle; EG's own generated CRDs carry none.
		if strings.Contains(doc, experimentalChannelAnnotation) {
			continue
		}
		kept = append(kept, doc)
	}
	return strings.Join(kept, docDelimiter)
}

// crdGroup extracts spec.group from a CustomResourceDefinition document
// (e.g. `gateway.envoyproxy.io`, `gateway.networking.k8s.io`). Empty when
// the doc has no readable group.
func crdGroup(doc string) string {
	var m struct {
		Spec struct {
			Group string `yaml:"group"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
		return ""
	}
	return m.Spec.Group
}

// helmTemplateIncludeCRDs runs the SAME `helm template` as helmTemplate but
// with `--include-crds` instead of `--skip-crds`, so the chart's bundled CRDs
// (its crds/ directory) appear in the output. chartOwnCRDs filters the result
// down to the chart's own non-standard-Gateway-API CRDs. Kept separate from
// helmTemplate (which stays --skip-crds for the controller render) so the two
// passes never share the CRD-channel decision.
func helmTemplateIncludeCRDs(ctx context.Context, spec HelmChartSpec) (string, error) {
	args := []string{
		"template", spec.Name,
		"--version", spec.Version,
		"--namespace", spec.Namespace,
		"--include-crds",
	}
	args = append(args, gatewayAPIChartValues(spec)...)
	chartRef := spec.OCI
	if chartRef == "" {
		chartRef = spec.Chart
		args = append(args, "--repo", spec.Repo)
	}
	if len(spec.Values) > 0 {
		f, err := writeValuesFile(spec.Values)
		if err != nil {
			return "", fmt.Errorf("helm chart %q: write values: %w", spec.Name, err)
		}
		defer func() { _ = os.Remove(f) }()
		args = append(args, "--values", f)
	}
	args = append(args, chartRef)

	cmd := exec.CommandContext(ctx, "helm", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("helm template --include-crds %s (%s@%s): %w\n%s",
			spec.Name, chartRef, spec.Version, err, stderr.String())
	}
	return stripHelmOCIStatus(stdout.String()), nil
}

// stampAppLabel stamps the chart's NAME onto every document in a
// `---`-separated manifest stream as its deploy GROUP, so the deploy layer's
// `--target` selection (SelectManifestsByGroup) treats the WHOLE chart as
// ONE named app. Documents that don't parse are passed through unchanged.
//
// The group rides WorkloadLabel (`forge.dev/workload`), which is FORCED: a
// chart's sub-components label themselves (cert-manager's webhook /
// cainjector are `app.kubernetes.io/name: webhook` / `cainjector`), and
// `--target=cert-manager` must select all of them. The chart's own
// `app.kubernetes.io/name` is left EXACTLY as the chart rendered it, because
// the chart's Service and Deployment selectors match on it — overwriting it
// (the old behaviour) rewrote the labels on the pod template's owner but not
// the selector, which is only safe by accident. It is defaulted to the chart
// name when the chart set none, so every document still carries the
// recommended label. See WorkloadLabel for why routing cannot share that key.
//
// A non-empty owner additionally stamps ChartOwnerLabel on every document. The
// prune after the apply (chart_prune.go) deletes an object the chart's
// inventory recorded only while it still carries this stamp for this
// (project, env).
func stampAppLabel(manifests, name, owner string) string {
	var out []string
	for _, doc := range splitDocs(manifests) {
		out = append(out, stampDocAppLabel(doc, name, owner))
	}
	return strings.Join(out, docDelimiter)
}

// stampDocAppLabel forces metadata.labels[WorkloadLabel] = name on a single
// YAML document and defaults AppNameLabel / managed-by when absent,
// preserving every other field and label (see stampAppLabel). Unparseable docs
// pass through verbatim.
func stampDocAppLabel(doc, name, owner string) string {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(doc), &m); err != nil || m == nil {
		return doc
	}
	meta, _ := m["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	labels, _ := meta["labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	// FORCE the group — the whole chart is one --target unit. The chart's
	// own name label is only defaulted: its selectors depend on it.
	labels[WorkloadLabel] = name
	if owner != "" {
		labels[ChartOwnerLabel] = owner
	}
	if _, ok := labels[AppNameLabel]; !ok {
		labels[AppNameLabel] = name
	}
	if _, ok := labels["app.kubernetes.io/managed-by"]; !ok {
		labels["app.kubernetes.io/managed-by"] = "forge"
	}
	meta["labels"] = labels
	m["metadata"] = meta
	b, err := yaml.Marshal(m)
	if err != nil {
		return doc
	}
	return strings.TrimRight(string(b), "\n")
}

// applyCRDsThenRest applies a manifest stream in CRD-FIRST order: every
// CustomResourceDefinition document is applied and WAITED to Established
// before the rest of the stream. This is the apply-ordering primitive the
// helm-as-renderer model needs — a `--target=<platform>` apply must leave
// the cluster with CRDs Established + controllers Deployed, so a later
// `forge env deploy` (the app) finds the CRDs present.
//
// extraCRDs are forge-supplied CRDs (the pinned standard Gateway API CRDs /
// cert-manager CRDs at the chart version) that must also land + Establish
// before the rest — the chart was rendered `--skip-crds`, so its own CRDs
// are absent and forge owns them. They are applied in the SAME first pass
// as any CRD found in the stream.
//
// Ordering, in one apply:
//  1. CRDs (extraCRDs ++ every `kind: CustomResourceDefinition` in
//     manifests) → kubectl apply → wait --for=condition=Established.
//  2. Everything else → kubectl apply.
//
// When there are no CRDs in either source this degenerates to a single
// apply of `rest`, byte-identical to a plain apply.
//
// namespace is the chart's DECLARED namespace (HelmChartSpec.Namespace),
// passed to kubectl as `-n` so a chart object that does not stamp
// `metadata.namespace` lands where the chart declared instead of in
// `default`. EVERY pass below takes it — the early CRD/Namespace batch and
// both halves of the config-then-rest second pass — because the objects are
// split across those passes by KIND, so threading one and not another
// reproduces the bug for whichever half was missed. See
// KubectlApplyNamespaced for the chart-by-chart evidence.
func applyCRDsThenRest(ctx context.Context, kctx, namespace, extraCRDs, manifests string) error {
	streamCRDs, streamNS, rest := partitionEarlyBatch(manifests)

	// Early batch: CRDs + Namespaces. The chart's namespaced resources target
	// a Namespace `helm template` never emits (RenderHelmChart synthesizes
	// it), so the Namespace must land BEFORE the rest pass or the config-first
	// apply fails `namespaces "<ns>" not found`. CRDs + Namespaces are both
	// pre-requisites of the rest, applied together; only the CRDs gate on
	// Established (a Namespace is ready the moment it exists).
	crds := joinNonEmpty(extraCRDs, streamCRDs)
	if err := applyEarlyBatch(ctx, kctx, namespace, crds, streamNS); err != nil {
		return err
	}

	if strings.TrimSpace(rest) != "" {
		// Reuse the standard two-pass (config-then-rest) apply for the
		// non-CRD remainder so a chart's namespaced Secrets/ConfigMaps land
		// before the controller pods that reference them.
		config, workloads := PartitionConfigManifests(rest)
		if strings.TrimSpace(config) != "" {
			if err := KubectlApplyNamespaced(ctx, kctx, namespace, config); err != nil {
				return fmt.Errorf("apply config: %w", err)
			}
		}
		if err := KubectlApplyNamespaced(ctx, kctx, namespace, workloads); err != nil {
			return fmt.Errorf("apply: %w", err)
		}
	}
	return nil
}

// crdEstablishedTimeout bounds the wait for a freshly applied CRD to be
// served. Establishing is the apiserver registering a new REST endpoint — it
// takes well under a second on a healthy cluster; this only guards a wedged one.
const crdEstablishedTimeout = 120 * time.Second

// applyEarlyBatch is the CRD-first primitive both apply paths share: apply the
// CRDs together with the early Namespaces, then block until every CRD reports
// Established. Nothing that instantiates one of those CRDs may be sent before
// this returns — an apply racing its own CRD fails `no matches for kind`
// (or, worse, succeeds against a stale discovery cache and fails half-way).
//
// Used by the chart path (applyCRDsThenRest, whose CRDs include forge's pinned
// bundle) and by the env's own manifest stream (applyRendered, whose CRDs come
// from RenderedWorkload.manifests / additional_manifests). One implementation
// on purpose: a second copy of "apply, then wait Established" is a second
// place for the wait to be forgotten.
//
// A no-op when both inputs are empty.
func applyEarlyBatch(ctx context.Context, kctx, namespace, crds, namespaces string) error {
	early := joinNonEmpty(crds, namespaces)
	if strings.TrimSpace(early) == "" {
		return nil
	}
	if err := KubectlApplyNamespaced(ctx, kctx, namespace, early); err != nil {
		return fmt.Errorf("apply CRDs/Namespaces: %w", err)
	}
	names := crdNames(crds)
	if len(names) == 0 {
		return nil
	}
	fmt.Printf("Waiting for %d CRD(s) to be Established...\n", len(names))
	if err := waitCRDsEstablished(ctx, kctx, names, crdEstablishedTimeout); err != nil {
		return fmt.Errorf("wait CRDs Established: %w", err)
	}
	return nil
}

// partitionEarlyBatch splits a `---`-separated stream into (crds, namespaces,
// rest): crds holds every `kind: CustomResourceDefinition` document (in
// order), namespaces holds every `kind: Namespace` document, rest holds
// everything else. CRDs and Namespaces are the early-batch prerequisites
// applyCRDsThenRest lands before the rest (CRDs additionally gate on
// Established). An unparseable doc goes to rest (it can't be confirmed a CRD
// or Namespace, and rest is the pass that always runs).
func partitionEarlyBatch(manifests string) (crds, namespaces, rest string) {
	var crdDocs, nsDocs, restDocs []string
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		switch {
		case ok && m.Kind == "CustomResourceDefinition":
			crdDocs = append(crdDocs, doc)
		case ok && m.Kind == "Namespace":
			nsDocs = append(nsDocs, doc)
		default:
			restDocs = append(restDocs, doc)
		}
	}
	return strings.Join(crdDocs, docDelimiter),
		strings.Join(nsDocs, docDelimiter),
		strings.Join(restDocs, docDelimiter)
}

// crdNames extracts the metadata.name of every CustomResourceDefinition in
// a `---`-separated stream — the names `kubectl wait --for=Established
// crd/<name>` blocks on.
func crdNames(manifests string) []string {
	var names []string
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if ok && m.Kind == "CustomResourceDefinition" && m.Metadata.Name != "" {
			names = append(names, m.Metadata.Name)
		}
	}
	return names
}

// waitCRDsEstablished blocks until every named CRD reports
// Established=True (or the timeout elapses) — the happens-before that lets
// the chart's controllers (and any later `forge env deploy` referencing the
// CRD's resources) apply against a CRD the apiserver already serves.
func waitCRDsEstablished(ctx context.Context, kctx string, names []string, timeout time.Duration) error {
	args := []string{"wait", "--for=condition=Established", "--timeout=" + timeout.String()}
	for _, n := range names {
		args = append(args, "crd/"+n)
	}
	cmd := kubectlCmd(ctx, kctx, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// waitChartDeploymentsAvailable blocks until every Deployment in the chart's
// namespace reports Available=True (or the timeout elapses). This is the
// happens-before the chart's RIDING manifests need: a chart that ships an
// admission webhook (cert-manager's `webhook.cert-manager.io`
// ValidatingWebhookConfiguration, failurePolicy: Fail) REJECTS the
// cluster-scoped instances its controller reconciles (ClusterIssuers) with
// `failed calling webhook ... no endpoints available` until the webhook
// Deployment is Ready and cainjector has injected the caBundle. Waiting for
// the chart's Deployments to be Available closes that race before the riding
// manifests apply. A namespace with no Deployments (kubectl wait --all over
// an empty set) returns immediately.
func waitChartDeploymentsAvailable(ctx context.Context, kctx, namespace string, timeout time.Duration) error {
	cmd := kubectlCmd(ctx, kctx,
		"wait", "--for=condition=Available",
		"deploy", "--all",
		"-n", namespace,
		"--timeout="+timeout.String(),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// applyRidingManifestsWithRetry applies a chart's riding manifests (the
// GatewayClass / ClusterIssuers stamped with the chart's group) with a
// bounded retry. Even after the chart's webhook Deployment reports Available,
// the webhook Service's endpoints can lag a few seconds (the readiness gate
// and Endpoints publication aren't perfectly simultaneous), so an apply that
// hits `no endpoints available` / `connection refused` for the webhook is
// retried rather than failing the deploy. A non-webhook error surfaces
// immediately (no point retrying a genuine manifest error).
func applyRidingManifestsWithRetry(ctx context.Context, kctx, namespace, manifests string) error {
	const attempts = 6
	const delay = 5 * time.Second
	var err error
	for i := 0; i < attempts; i++ {
		if err = applyCRDsThenRest(ctx, kctx, namespace, "", manifests); err == nil {
			return nil
		}
		if !isWebhookNotReadyError(err) {
			return err
		}
		if i < attempts-1 {
			fmt.Printf("Webhook endpoint not ready yet (attempt %d/%d); retrying in %s...\n",
				i+1, attempts, delay)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return err
}

// isWebhookNotReadyError reports whether an apply error is the transient
// "the admission webhook isn't serving yet" failure — the only error
// applyRidingManifestsWithRetry retries. The cert-manager validating webhook
// surfaces as `failed calling webhook ... no endpoints available for service`
// (or `connection refused` / `context deadline exceeded`) while its
// Deployment's endpoints are still publishing.
func isWebhookNotReadyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "failed calling webhook") &&
		(strings.Contains(msg, "no endpoints available") ||
			strings.Contains(msg, "connection refused") ||
			strings.Contains(msg, "context deadline exceeded") ||
			strings.Contains(msg, "EOF") ||
			strings.Contains(msg, "i/o timeout"))
}

// joinNonEmpty joins manifest streams with the doc delimiter, skipping
// empty ones so no spurious `---\n---` separators appear.
func joinNonEmpty(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, strings.TrimSpace(p))
		}
	}
	return strings.Join(kept, docDelimiter)
}
