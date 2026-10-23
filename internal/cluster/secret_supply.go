package cluster

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// secret_supply.go implements the RENDER-TIME secret back-propagation gate:
// the complement to the live-cluster Secret preflight. Where the live check
// verifies a referenced Secret KEY actually exists in the target cluster, this
// check needs NO cluster at all — it asks, purely from the rendered bundle,
// "does anything in this bundle PROVIDE every Secret a workload mounts or
// reads?". A workload that mounts a secret-backed volume (or reads a
// secretKeyRef) for a Secret NOTHING declares will stick forever on
// `MountVolume.SetUp failed … secret "X" not found` / CreateContainerConfigError
// — with zero error at deploy time. This gate turns that silent pod-rot into a
// fail-fast render-time block.
//
// The motivating bug: a control-plane operator MOUNTED Secret
// `cp-daemon-kubeconfig` (a secret volume) but never declared the
// forge.KubeconfigSecret that mints it. forge minted nothing, the pod stuck
// ContainerCreating for 15+ minutes, and `forge env up` was green. This gate
// fails that bundle at render time with a back-propagated, actionable error.
//
// DEMAND (what must be provided): every Secret the rendered workloads mount as
// a volume + every Secret an env var reads via secretKeyRef / envFrom
// secretRef — exactly what CollectManifestRefs already collects into
// ManifestRefs.Secrets.
//
// SUPPLY (what provides a Secret in this bundle):
//   - a Secret rendered into the manifest stream (kind: Secret),
//   - a cert-manager Certificate in the stream (materialises the Secret named
//     by its spec.secretName when cert-manager reconciles it),
//   - an external-secrets.io ExternalSecret in the stream (materialises the
//     Secret named by its spec.target.name when the operator reconciles it),
//   - a forge.KubeconfigSecret (mints its named Secret),
//   - a forge.ExternalSecret (the author's explicit out-of-band promise that
//     the Secret exists — counts as SATISFIED; the LIVE preflight separately
//     verifies it's actually provisioned),
//   - any other generated/known Secret forge produces (passed in as supply by
//     the caller, e.g. ExternalSecrets-operator-provided Secrets).
//
// AVOIDING FALSE POSITIVES is the explicit priority: a demanded Secret is
// SATISFIED when ANY supply source provides a Secret of that NAME. Matching is
// name-based (namespace-permissive) on purpose — a rendered Secret, a
// KubeconfigSecret, and an ExternalSecret each carry their own namespace
// (cert-manager's token lives in cert-manager, not the deploy namespace), and
// a forge bundle renders into a single deploy namespace anyway. Insisting on an
// exact namespace match would risk BLOCKING a perfectly valid render (a far
// worse failure than missing one truly-undeclared mount). Only a Secret whose
// name NOTHING in the bundle supplies fails.

// SecretSupplyKind labels WHERE a supplied Secret comes from, so the gate can
// report what already provides a name (and so a future report can explain why a
// near-miss didn't satisfy a demand). Purely descriptive — the match itself is
// name-based.
type SecretSupplyKind string

const (
	// SupplyRenderedManifest — a `kind: Secret` document in the rendered stream.
	SupplyRenderedManifest SecretSupplyKind = "rendered Secret"
	// SupplyKubeconfigSecret — a forge.KubeconfigSecret mint.
	SupplyKubeconfigSecret SecretSupplyKind = "KubeconfigSecret"
	// SupplyExternalSecret — a forge.ExternalSecret out-of-band promise.
	SupplyExternalSecret SecretSupplyKind = "ExternalSecret"
	// SupplyGenerated — any other Secret forge is known to produce (operator-
	// provisioned, generated). The catch-all the caller uses for supply that
	// isn't one of the first-class kinds.
	SupplyGenerated SecretSupplyKind = "generated/known Secret"
	// SupplyControlPlaneSecret — a forge.ControlPlaneSecret: the env's control
	// plane writes and renews it AFTER the deploy, so it is promised, never
	// preconditioned.
	SupplyControlPlaneSecret SecretSupplyKind = "ControlPlaneSecret"
	// SupplyCertManagerCertificate — a cert-manager Certificate in the stream,
	// which materialises the Secret its spec.secretName names.
	SupplyCertManagerCertificate SecretSupplyKind = "cert-manager Certificate"
)

// SecretSupply is one Secret the bundle PROVIDES, used to satisfy a demand. The
// caller (cli layer) projects the env's KubeconfigSecrets / ExternalSecrets /
// generated Secrets onto this shape so the cluster package stays decoupled from
// the cli entity types — the same pattern RequiredSecret uses. Rendered-stream
// Secrets are collected by the gate itself (CollectRenderedSecretNames), so the
// caller need not enumerate those.
type SecretSupply struct {
	// Name is the k8s Secret name this source provides. The match key.
	Name string
	// Namespace is the source's declared namespace (may differ from the deploy
	// namespace; empty means "the deploy namespace"). Recorded for the report
	// only — the match is name-based to avoid false positives.
	Namespace string
	// Kind labels the source for the report.
	Kind SecretSupplyKind
}

// CollectRenderedSecretNames returns the set of Secret NAMES rendered as
// `kind: Secret` documents in the manifest stream — the in-stream half of the
// supply. These satisfy a demand directly: forge applies them in the same
// deploy, so a mount of one resolves on first schedule. Malformed documents are
// skipped (best-effort, mirroring the other manifest scanners in this package).
func CollectRenderedSecretNames(manifests string) map[string]struct{} {
	names := map[string]struct{}{}
	for _, doc := range splitDocs(manifests) {
		var head struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &head); err != nil {
			continue
		}
		if strings.TrimSpace(head.Kind) != "Secret" {
			continue
		}
		if name := strings.TrimSpace(head.Metadata.Name); name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

// CollectCertManagerSecretNames returns the set of Secret NAMES that
// cert-manager Certificates in the stream will materialise — each
// Certificate's `spec.secretName`.
//
// A Certificate is a PROVIDER of its Secret, exactly as a `kind: Secret`
// document is. cert-manager issues the certificate and writes the Secret when
// it reconciles the Certificate, in the same apply pass that creates the pods
// mounting it, so demanding the Secret exist beforehand makes a first deploy
// of any cert-manager-backed workload unsatisfiable — the thing that
// provisions it is the very deploy being blocked.
//
// This surfaced on the vendored cloudnative-pg barman-cloud plugin, whose
// bundle renders two Certificates and the Deployment mounting their
// `barman-cloud-{client,server}-tls` Secrets. The gate reported both as
// undeclared mounts and told the operator to declare a KubeconfigSecret or
// ExternalSecret — advice that is wrong for a cert-manager Secret, whose
// bytes cert-manager owns and would overwrite. `forge env deploy e2e` was
// unrunnable without --skip-preflight, which disables every other check too.
//
// Matched on the cert-manager.io API GROUP (any version), so a bundle on
// v1alpha2 or a future v2 is recognised without a forge change, while an
// unrelated CRD that happens to be called Certificate is not.
func CollectCertManagerSecretNames(manifests string) map[string]struct{} {
	names := map[string]struct{}{}
	for _, doc := range splitDocs(manifests) {
		var head struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Spec       struct {
				SecretName string `yaml:"secretName"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &head); err != nil {
			continue
		}
		if strings.TrimSpace(head.Kind) != "Certificate" {
			continue
		}
		// apiVersion is "<group>/<version>"; compare the group alone so every
		// cert-manager.io version qualifies.
		group, _, _ := strings.Cut(strings.TrimSpace(head.APIVersion), "/")
		if !strings.EqualFold(group, "cert-manager.io") {
			continue
		}
		if name := strings.TrimSpace(head.Spec.SecretName); name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

// CollectExternalSecretsNames returns the set of Secret NAMES that
// external-secrets.io ExternalSecrets in the stream will materialise: each one's
// `spec.target.name`, else its own name (ESO writes the Secret named like the
// ExternalSecret when no target name is given).
//
// An ExternalSecret is a PROVIDER of its Secret exactly as a cert-manager
// Certificate is: the operator writes the Secret when it reconciles the object,
// in the same apply pass that creates the pods mounting it, so demanding the
// Secret exist beforehand makes a first deploy unsatisfiable. This is what a
// bootstrap source needs: the OpenBao component renders the ExternalSecrets that
// deliver its own seal key and database DSN, and a store that mounts them would
// otherwise be refused by its own render.
//
// Matched on the external-secrets.io API GROUP (any version).
func CollectExternalSecretsNames(manifests string) map[string]struct{} {
	names := map[string]struct{}{}
	for _, doc := range splitDocs(manifests) {
		var head struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Target struct {
					Name string `yaml:"name"`
				} `yaml:"target"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &head); err != nil {
			continue
		}
		if strings.TrimSpace(head.Kind) != "ExternalSecret" {
			continue
		}
		group, _, _ := strings.Cut(strings.TrimSpace(head.APIVersion), "/")
		if !strings.EqualFold(group, "external-secrets.io") {
			continue
		}
		name := strings.TrimSpace(head.Spec.Target.Name)
		if name == "" {
			name = strings.TrimSpace(head.Metadata.Name)
		}
		if name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

// UndeclaredSecretMount is one demanded Secret that NO supply in the bundle
// provides — a back-propagated render-time failure. Workloads carries the
// names of the workload manifests that mount/reference it (so the error points
// at WHO will FailedMount), and Refs records the reference shapes (volume mount
// vs env secretKeyRef) for the message.
type UndeclaredSecretMount struct {
	// Secret is the demanded Secret name nothing supplies.
	Secret string
	// Workloads are the names of the manifests that mount/reference it,
	// deduped + sorted. Best-effort: a workload whose metadata.name couldn't be
	// read contributes nothing.
	Workloads []string
}

// CheckSecretSupply is the render-time back-propagation gate. It collects the
// DEMAND from the rendered manifests (every mounted/referenced Secret) and the
// SUPPLY (rendered Secrets + the caller-supplied KubeconfigSecret /
// ExternalSecret / generated Secrets) and returns one UndeclaredSecretMount per
// demanded Secret NAME that no supply provides — sorted by Secret name for a
// deterministic report. A nil/empty return means every mount/ref is satisfied.
//
// No cluster is touched. The match is name-based (namespace-permissive) so a
// Secret promised by an ExternalSecret in another namespace, or rendered
// in-stream, or minted by a KubeconfigSecret, always PASSES — only a truly
// undeclared name fails.
func CheckSecretSupply(manifests string, supplied []SecretSupply) []UndeclaredSecretMount {
	// Unplaced: this gate is name-based, so where a reference resolves does
	// not enter into it.
	refs := CollectManifestRefs(manifests, RefPlacement{})
	if len(refs.Secrets) == 0 {
		return nil
	}

	// SUPPLY set, name-keyed. Rendered-stream Secrets first, then the Secrets
	// cert-manager Certificates in the same stream materialise, then the
	// caller-supplied sources.
	supply := CollectRenderedSecretNames(manifests)
	for name := range CollectCertManagerSecretNames(manifests) {
		supply[name] = struct{}{}
	}
	for name := range CollectExternalSecretsNames(manifests) {
		supply[name] = struct{}{}
	}
	for _, s := range supplied {
		if n := strings.TrimSpace(s.Name); n != "" {
			supply[n] = struct{}{}
		}
	}

	// imagePullSecrets are a DEMAND shape CollectManifestRefs records too, but
	// they are cluster registry-pull credentials provisioned out-of-band (the
	// live preflight checks them) — not an application Secret a forge bundle is
	// expected to render. Excluding them here keeps this render-time gate from
	// false-failing on a pull secret that legitimately lives only in the
	// cluster. The live preflight remains the backstop for a genuinely-missing
	// pull secret.
	pullSecrets := map[string]struct{}{}
	for ref := range refs.ImagePullSecrets {
		pullSecrets[ref.Name] = struct{}{}
	}

	// DEMAND→supply match. Record which workloads mount each unsatisfied name.
	workloadsBySecret := secretMountWorkloads(manifests)
	var out []UndeclaredSecretMount
	for name := range refs.secretNames() {
		if name == "" {
			continue // defensive: an unnamed reference can't be matched
		}
		if _, ok := pullSecrets[name]; ok {
			continue
		}
		if _, ok := supply[name]; ok {
			continue
		}
		miss := UndeclaredSecretMount{Secret: name}
		if ws := workloadsBySecret[name]; len(ws) > 0 {
			miss.Workloads = ws
		}
		out = append(out, miss)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Secret < out[j].Secret })
	return out
}

// secretMountWorkloads maps each demanded Secret name to the sorted set of
// workload manifest names that mount or reference it, so the failure report can
// name WHO will FailedMount. It re-walks the stream per document (rather than
// reusing CollectManifestRefs, which flattens away the owning document) so each
// reference is attributed to its top-level manifest's metadata.name. A document
// whose name can't be read still contributes its refs under "" (dropped by the
// caller), so the gate never loses a demand just because attribution failed.
func secretMountWorkloads(manifests string) map[string][]string {
	bySecret := map[string]map[string]struct{}{}
	for _, doc := range splitDocs(manifests) {
		var node any
		if err := yaml.Unmarshal([]byte(doc), &node); err != nil {
			continue
		}
		// Per-document refs, unplaced: only the names matter here.
		docRefs := newManifestRefs()
		collectRefs(node, refSink{refs: &docRefs, contexts: []string{""}})
		if len(docRefs.Secrets) == 0 {
			continue
		}
		name := docName(node)
		if name == "" {
			continue
		}
		for ref := range docRefs.Secrets {
			secret := ref.Name
			if secret == "" {
				continue
			}
			if _, ok := docRefs.ImagePullSecrets[ref]; ok {
				continue
			}
			if bySecret[secret] == nil {
				bySecret[secret] = map[string]struct{}{}
			}
			bySecret[secret][name] = struct{}{}
		}
	}
	out := make(map[string][]string, len(bySecret))
	for secret, set := range bySecret {
		ws := make([]string, 0, len(set))
		for w := range set {
			ws = append(ws, w)
		}
		sort.Strings(ws)
		out[secret] = ws
	}
	return out
}

// docName reads metadata.name off a decoded top-level YAML document, returning
// "" when absent. Used to attribute a Secret demand to its owning manifest.
func docName(node any) string {
	m, ok := node.(map[string]any)
	if !ok {
		return ""
	}
	meta, ok := mapAt(m, "metadata")
	if !ok {
		return ""
	}
	return strings.TrimSpace(stringAt(meta, "name"))
}

// FormatUndeclaredSecretMounts renders the back-propagated, actionable failure
// for one or more undeclared Secret mounts. The message names WHO mounts the
// Secret and what the author must declare to provide it — the exact shape the
// task specifies:
//
//	service "workspace-controller" mounts Secret "cp-daemon-kubeconfig" but
//	nothing declares it (no rendered Secret, KubeconfigSecret, or
//	ExternalSecret) — it will FailedMount. Declare a
//	forge.KubeconfigSecret/ExternalSecret or remove the mount.
func FormatUndeclaredSecretMounts(misses []UndeclaredSecretMount) string {
	var b strings.Builder
	b.WriteString("render failed — a workload mounts/references a Secret that NOTHING in the rendered bundle provides (it would FailedMount / CreateContainerConfigError at schedule time):\n")
	for _, m := range misses {
		who := "a workload"
		if len(m.Workloads) > 0 {
			who = fmt.Sprintf("%s %s", pluralWorkloadNoun(m.Workloads), quoteJoin(m.Workloads))
		}
		fmt.Fprintf(&b, "\n  %s mounts Secret %q but nothing declares it (no rendered Secret, KubeconfigSecret, or ExternalSecret) — it will FailedMount.\n",
			who, m.Secret)
	}
	b.WriteString("\nFix one of:\n")
	b.WriteString("  - declare a forge.KubeconfigSecret to MINT the Secret (cross-cluster kubeconfig), or\n")
	b.WriteString("  - declare a forge.ExternalSecret to promise it exists out-of-band (then provision it), or\n")
	b.WriteString("  - render the Secret in the bundle (a secret_provider rendered Secret), or\n")
	b.WriteString("  - remove the mount/reference if the workload doesn't actually need it.")
	return b.String()
}

// pluralWorkloadNoun returns "workload" / "workloads" to match the count of
// attributed manifests, so the message reads naturally for one or many.
func pluralWorkloadNoun(workloads []string) string {
	if len(workloads) == 1 {
		return "workload"
	}
	return "workloads"
}

// quoteJoin renders a sorted, quoted, comma-separated list of workload names.
func quoteJoin(items []string) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = fmt.Sprintf("%q", it)
	}
	return strings.Join(parts, ", ")
}
