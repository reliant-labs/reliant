package cluster

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Preflight is the deploy-time "deployability contract": before a single
// manifest is applied, it checks that the rendered bundle's external
// dependencies actually exist on the LIVE target — every Secret KEY and
// ConfigMap KEY a container references is present in the target cluster, every
// container image: is resolvable in its registry, and every NON-CORE resource
// KIND the bundle renders has a CRD installed on the cluster (so a rendered
// GRPCRoute can't fail `no matches for kind` mid-rollout). When something is
// missing it returns a single grouped error naming EVERYTHING missing at
// once, so the user fixes it all in one pass instead of discovering each
// gap one-at-a-time as pods crash (CreateContainerConfigError /
// ImagePullBackOff) — or kubectl apply errors `no matches for kind` — over a
// live rollout.
//
// The checks are injected (SecretGetter / ConfigMapGetter / ImageChecker) so
// the orchestration is unit-testable without a live cluster or registry; the
// deploy path wires the real kubectl-/docker-backed implementations (see
// KubectlSecretGetter / KubectlConfigMapGetter / RegistryImageChecker).

// ManifestGVK is the GroupVersionKind of a rendered manifest document — the
// (apiVersion, kind) pair `kubectl apply` keys a resource on — paired with the
// document's name so the preflight report can point at WHICH manifest needs a
// missing CRD. APIVersion is the raw `apiVersion:` value ("apps/v1", "v1",
// "gateway.networking.k8s.io/v1"); Kind is the raw `kind:` value.
type ManifestGVK struct {
	APIVersion string
	Kind       string
	// Name is metadata.name of the document (best-effort, "" when absent) —
	// used only to make the missing-CRD report actionable.
	Name string
}

// group returns the API group of the GVK — the part of apiVersion before the
// "/", or "" for a core-group resource (apiVersion "v1"). Used to decide
// whether a kind is a CORE kind (group "") that every cluster serves, so the
// CRD gate never false-positives on Deployment/Service/ConfigMap/etc.
func (g ManifestGVK) group() string {
	grp, _, found := strings.Cut(g.APIVersion, "/")
	if !found {
		// No "/" → core group (apiVersion is a bare version like "v1").
		return ""
	}
	return grp
}

// ResourceRef locates one Secret or ConfigMap a rendered object depends on.
//
// A pod resolves every Secret and ConfigMap it names in its OWN namespace, on
// the cluster it runs on, so a reference is only meaningful with both. Keying
// references by bare name, and checking them all in the deploy namespace on
// the primary cluster, refused control-plane's prod release for
// observability-data/obs-credentials and observability/otel-agent-token —
// Secrets that existed exactly where their workloads use them, reported as
// missing from control-plane-prod.
type ResourceRef struct {
	// Context is the kubectl context of the cluster the referencing object is
	// applied to. Empty when the collection was not placed on a cluster.
	Context string
	// Namespace is the referencing object's metadata.namespace, or the deploy
	// namespace when it declares none.
	Namespace string
	// Name is the referenced Secret / ConfigMap name.
	Name string
}

// subject renders the reference for a report: "<namespace>/<name>", qualified
// as "<context>:<namespace>/<name>" when the run spans several clusters and an
// unqualified name could be on any of them.
func (r ResourceRef) subject(multiCluster bool) string {
	s := r.Namespace + "/" + r.Name
	if multiCluster && r.Context != "" {
		s = r.Context + ":" + s
	}
	return s
}

// RefPlacement says where a rendered document's references must resolve.
type RefPlacement struct {
	// Namespace is the namespace of a document that declares none — the
	// deploy namespace `kubectl apply -n` puts it in.
	Namespace string
	// Context is the cluster every document lands on when Contexts is nil.
	Context string
	// Contexts attributes one document to the kubectl contexts it is applied
	// to, for an env that spans several clusters. It must be the deploy's own
	// router (ScopeManifestsToGroup), never a second model of it. Nil means
	// every document lands on Context. A document it attributes to no cluster
	// is applied nowhere, so it demands nothing.
	Contexts func(doc string) []string
}

// place resolves one document's namespace and clusters.
func (p RefPlacement) place(doc string, node any) (namespace string, contexts []string) {
	namespace = p.Namespace
	if root, ok := node.(map[string]any); ok {
		if md, ok := mapAt(root, "metadata"); ok {
			if ns := strings.TrimSpace(stringAt(md, "namespace")); ns != "" {
				namespace = ns
			}
		}
	}
	if p.Contexts == nil {
		return namespace, []string{p.Context}
	}
	return namespace, p.Contexts(doc)
}

// ManifestRefs is the set of external references a rendered manifest bundle
// depends on at schedule time: the Secret / ConfigMap (name, key) pairs its
// containers project into env or mount, and the distinct container images it
// runs.
type ManifestRefs struct {
	// Secrets maps a Secret, located where the referencing pod resolves it,
	// to the set of keys referenced from it. A key of "" (present in the set)
	// means a whole-Secret reference (envFrom secretRef, a volume mount, an
	// imagePullSecret) — verify existence only.
	//
	// Only REQUIRED references are here: every consumer that reads Secrets
	// as demand (the live check, the render-time supply gate, the secrets
	// lint) reads this map. A reference marked `optional: true` is in
	// OptionalSecrets instead.
	Secrets map[ResourceRef]map[string]struct{}
	// OptionalSecrets is Secrets for references marked `optional: true`.
	// Such a reference has two modes. While the Secret is ABSENT it demands
	// nothing: kubelet starts the pod without it, so no gate may report it.
	// Once the Secret is PRESENT it is a real input of the pod: the pod
	// template is pinned to it like any other (secret_pins.go), and the live
	// check reads the Secret and warns about a referenced key it lacks.
	OptionalSecrets map[ResourceRef]map[string]struct{}
	// ConfigMaps maps a ConfigMap, located like Secrets, to the set of keys
	// referenced from it. A key of "" means a whole-ConfigMap reference
	// (envFrom configMapRef or a whole-ConfigMap volume mount) — verify
	// existence only. A missing ConfigMap key fails a pod identically to a
	// missing Secret key (CreateContainerConfigError). Required references
	// only, like Secrets.
	ConfigMaps map[ResourceRef]map[string]struct{}
	// OptionalConfigMaps is ConfigMaps for `optional: true` references, with
	// OptionalSecrets' two modes (a ConfigMap pins nothing).
	OptionalConfigMaps map[ResourceRef]map[string]struct{}
	// Images is the set of distinct container image refs in the bundle.
	Images map[string]struct{}
	// ImagePullSecrets is the set of distinct Secrets referenced via a pod
	// spec's imagePullSecrets[].name. These are the credentials the CLUSTER
	// uses to pull private images — the image-verification path resolves
	// their .dockerconfigjson from the target cluster and uses it to
	// authenticate an otherwise auth-denied registry lookup, so a present
	// private image isn't false-flagged just because the LOCAL docker daemon
	// lacks creds for that registry. A subset of Secrets (which records every
	// Secret reference shape); kept distinct here because only
	// imagePullSecrets carry registry credentials.
	ImagePullSecrets map[ResourceRef]struct{}
}

func newManifestRefs() ManifestRefs {
	return ManifestRefs{
		Secrets:            map[ResourceRef]map[string]struct{}{},
		OptionalSecrets:    map[ResourceRef]map[string]struct{}{},
		ConfigMaps:         map[ResourceRef]map[string]struct{}{},
		OptionalConfigMaps: map[ResourceRef]map[string]struct{}{},
		Images:             map[string]struct{}{},
		ImagePullSecrets:   map[ResourceRef]struct{}{},
	}
}

// secretNames is the distinct set of referenced Secret names, for the
// name-based render-time supply gate.
func (r ManifestRefs) secretNames() map[string]struct{} {
	out := make(map[string]struct{}, len(r.Secrets))
	for ref := range r.Secrets {
		out[ref.Name] = struct{}{}
	}
	return out
}

// CollectManifestRefs walks a `---`-separated multi-doc YAML manifest stream
// and collects every Secret reference (secretKeyRef, envFrom secretRef,
// secret-backed volumes, projected secret sources, imagePullSecrets), every
// ConfigMap reference (configMapKeyRef, envFrom configMapRef, configMap-backed
// volumes, projected configMap sources), and every container image. It
// recurses the whole document tree rather than hard-coding pod-spec paths, so
// it picks references up uniformly across Deployments, StatefulSets,
// DaemonSets, Jobs, CronJobs, Pods, and any nested template — including init
// containers and CronJob/Job pod templates. Malformed documents are skipped
// (best-effort, mirroring the other manifest scanners in this package).
//
// Each Secret / ConfigMap reference is recorded where it resolves: the
// referencing document's namespace (place.Namespace when it declares none),
// on every cluster place attributes the document to.
func CollectManifestRefs(manifests string, place RefPlacement) ManifestRefs {
	refs := newManifestRefs()
	for _, doc := range splitDocs(manifests) {
		var node any
		if err := yaml.Unmarshal([]byte(doc), &node); err != nil {
			continue
		}
		namespace, contexts := place.place(doc, node)
		collectRefs(node, refSink{refs: &refs, namespace: namespace, contexts: contexts})
	}
	dropSelfProvided(manifests, place, &refs)
	return refs
}

// refSink records one document's references at every place the document
// lands: its namespace, on each of its clusters.
type refSink struct {
	refs      *ManifestRefs
	namespace string
	contexts  []string
}

func (s refSink) secret(name, key string, optional bool) {
	into := s.refs.Secrets
	if optional {
		into = s.refs.OptionalSecrets
	}
	for _, c := range s.contexts {
		addRef(into, ResourceRef{Context: c, Namespace: s.namespace, Name: name}, key)
	}
}

func (s refSink) configMap(name, key string, optional bool) {
	into := s.refs.ConfigMaps
	if optional {
		into = s.refs.OptionalConfigMaps
	}
	for _, c := range s.contexts {
		addRef(into, ResourceRef{Context: c, Namespace: s.namespace, Name: name}, key)
	}
}

func (s refSink) pullSecret(name string) {
	s.secret(name, "", false)
	for _, c := range s.contexts {
		s.refs.ImagePullSecrets[ResourceRef{Context: c, Namespace: s.namespace, Name: name}] = struct{}{}
	}
}

// dropSelfProvided removes references the SAME manifest stream satisfies.
//
// The preflight's job is to catch dependencies the live target is missing
// BEFORE the apply — a Secret or ConfigMap that must already exist because
// nothing in this deploy creates it. A resource the bundle itself renders is
// not that: `kubectl apply` creates it in the same pass, ordered ahead of the
// pods that consume it. Demanding it exist beforehand makes a first deploy
// unsatisfiable by construction — the user is told to provision something
// they cannot provision, because the thing that provisions it is the very
// deploy being blocked.
//
// This surfaced on a bundle carrying an in-cluster IdP: the bundle renders
// its own `zitadel-steps` bootstrap ConfigMap two documents above the
// Deployment that mounts it, and the preflight refused the deploy for a
// ConfigMap sitting in the stream it was reading.
//
// Only Secrets and ConfigMaps are subtracted. Images are deliberately NOT:
// an image reference is satisfied by a registry, never by another document
// in this stream, so a "self-provided" image is meaningless.
//
// A rendered Secret satisfies only the references that resolve to it: the
// same name, in its namespace, on a cluster it is applied to. Matching on the
// bare name let a Secret in one namespace hide a reference from another that
// nothing provides.
//
// A cert-manager Certificate and an external-secrets.io ExternalSecret
// PROVIDE their Secret the same way (operatorProvidedSecret): their operator
// writes it when it reconciles the object this apply creates. The no-cluster
// gate (CheckSecretSupply) already counted them; this live path did not, so a
// release that added a Certificate and the pod mounting its Secret was refused
// with "the Secret does not exist" and could ship only with --skip-preflight,
// which disables every other check too.
func dropSelfProvided(manifests string, place RefPlacement, refs *ManifestRefs) {
	for _, doc := range splitDocs(manifests) {
		var node any
		if err := yaml.Unmarshal([]byte(doc), &node); err != nil {
			continue
		}
		root, ok := node.(map[string]any)
		if !ok {
			continue
		}
		md, _ := mapAt(root, "metadata")
		name := stringAt(md, "name")
		var into []map[ResourceRef]map[string]struct{}
		switch stringAt(root, "kind") {
		case "Secret":
			into = []map[ResourceRef]map[string]struct{}{refs.Secrets, refs.OptionalSecrets}
		case "ConfigMap":
			into = []map[ResourceRef]map[string]struct{}{refs.ConfigMaps, refs.OptionalConfigMaps}
		default:
			provided, ok := operatorProvidedSecret(root)
			if !ok {
				continue
			}
			name = provided
			into = []map[ResourceRef]map[string]struct{}{refs.Secrets, refs.OptionalSecrets}
		}
		if name == "" {
			continue
		}
		namespace, contexts := place.place(doc, node)
		for _, c := range contexts {
			for _, m := range into {
				delete(m, ResourceRef{Context: c, Namespace: namespace, Name: name})
			}
		}
	}
}

// operatorProvidedSecret is the Secret an operator writes for doc, the
// document's root: a cert-manager.io Certificate's spec.secretName, or an
// external-secrets.io ExternalSecret's spec.target.name, else its own name
// (ESO names the Secret after the ExternalSecret when no target name is
// given). The Secret lands in the document's own namespace. The same rules
// as CollectCertManagerSecretNames and CollectExternalSecretsNames, which the
// no-cluster gate reads.
func operatorProvidedSecret(root map[string]any) (string, bool) {
	group, _, _ := strings.Cut(strings.TrimSpace(stringAt(root, "apiVersion")), "/")
	spec, _ := mapAt(root, "spec")
	switch kind := stringAt(root, "kind"); {
	case kind == "Certificate" && strings.EqualFold(group, "cert-manager.io"):
		return stringAt(spec, "secretName"), true
	case kind == "ExternalSecret" && strings.EqualFold(group, "external-secrets.io"):
		target, _ := mapAt(spec, "target")
		if name := stringAt(target, "name"); name != "" {
			return name, true
		}
		md, _ := mapAt(root, "metadata")
		return stringAt(md, "name"), true
	}
	return "", false
}

// CollectManifestGVKs walks a `---`-separated multi-doc YAML manifest stream
// and returns the GroupVersionKind of every TOP-LEVEL document — the
// (apiVersion, kind, name) `kubectl apply` will create a resource for. Only the
// document's OWN apiVersion/kind is collected (not nested template kinds): the
// cluster must serve the resource type forge actually applies, and a pod
// template's `kind` is an embedded field, never an applied object. Documents
// missing apiVersion or kind (a List wrapper, a malformed doc, a YAML comment-
// only chunk) are skipped — there's nothing to gate. The result preserves
// document order and may contain duplicate GVKs (the served-kind check
// de-dupes).
func CollectManifestGVKs(manifests string) []ManifestGVK {
	var out []ManifestGVK
	for _, doc := range splitDocs(manifests) {
		var head struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &head); err != nil {
			continue
		}
		av := strings.TrimSpace(head.APIVersion)
		kind := strings.TrimSpace(head.Kind)
		if av == "" || kind == "" {
			continue
		}
		out = append(out, ManifestGVK{APIVersion: av, Kind: kind, Name: strings.TrimSpace(head.Metadata.Name)})
	}
	return out
}

// collectRefs recursively descends a decoded YAML value, recording any
// Secret / ConfigMap reference and container image it finds into the sink. The
// recursion is shape-driven (it matches on the well-known key names wherever
// they appear), so it covers every pod-template site uniformly.
func collectRefs(node any, sink refSink) {
	switch v := node.(type) {
	case map[string]any:
		collectSecretRefs(v, sink)
		collectConfigMapRefs(v, sink)
		collectImageRef(v, sink.refs)
		for _, child := range v {
			collectRefs(child, sink)
		}
	case []any:
		for _, child := range v {
			collectRefs(child, sink)
		}
	}
}

// collectSecretRefs records every Secret reference shape rooted at this map:
// a keyed projection (secretKeyRef), a whole-Secret env projection (envFrom
// secretRef), a secret-backed volume (volumes[].secret.secretName), a
// projected secret source (sources[].secret.name), and an image-pull Secret
// (imagePullSecrets[].name). The volume / projected / imagePullSecret shapes
// are existence-only (key ""): forge can't know which keys the runtime reads,
// and an imagePullSecret is consumed whole. These cover the
// `ClusterClient external=True` out-of-band kubeconfig Secret that forge does
// NOT mint — mounted via a secret volume — so the gate catches it instead of
// letting the pod crash on first schedule.
//
// A reference marked `optional: true` is recorded apart, in OptionalSecrets,
// never as demand. It is not a schedule-time dependency: kubelet starts the
// container without it (an optional secretKeyRef yields no env var, an
// optional envFrom or volume projects nothing). Recording it as demand made a
// deliberately-absent Secret — a break-glass token that exists for minutes,
// once, ever — block every ordinary deploy in the live preflight and report
// as a pod that "will FailedMount" in the render-time supply gate, both false.
// Dropping it outright went too far the other way: a PRESENT optional Secret
// is still read by the pod, so its version pin must still roll the pod and a
// key it lacks is still worth saying. imagePullSecrets carry no `optional`
// field and are unaffected.
func collectSecretRefs(v map[string]any, sink refSink) {
	// secretKeyRef: {name, key} — a single (Secret, key) projection.
	if skr, ok := mapAt(v, "secretKeyRef"); ok {
		if name := stringAt(skr, "name"); name != "" {
			sink.secret(name, stringAt(skr, "key"), isOptional(skr))
		}
	}
	// envFrom secretRef: {name} — projects the WHOLE Secret. Existence only.
	if sr, ok := mapAt(v, "secretRef"); ok {
		if name := stringAt(sr, "name"); name != "" {
			sink.secret(name, "", isOptional(sr))
		}
	}
	// volumes[].secret.secretName — a secret-backed volume mount.
	if sv, ok := mapAt(v, "secret"); ok {
		// A pod-volume secret source keys the name as `secretName`; a
		// projected source keys it as `name`. Accept either so both the
		// `volumes[].secret` and `sources[].secret` shapes are covered.
		if name := stringAt(sv, "secretName"); name != "" {
			sink.secret(name, "", isOptional(sv))
		}
		if name := stringAt(sv, "name"); name != "" {
			sink.secret(name, "", isOptional(sv))
		}
	}
	// imagePullSecrets[].name — a list of {name} Secret references. Recorded
	// in BOTH Secrets (existence check) and ImagePullSecrets (the
	// registry-credential source the image-verification path authenticates
	// with). These are the cluster's registry pull credentials; the
	// image-verification path resolves their .dockerconfigjson and uses it to
	// authenticate registry lookups the LOCAL docker daemon would be denied.
	if list, ok := v["imagePullSecrets"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				if name := stringAt(m, "name"); name != "" {
					sink.pullSecret(name)
				}
			}
		}
	}
}

// collectConfigMapRefs records every ConfigMap reference shape rooted at this
// map, mirroring collectSecretRefs: a keyed projection (configMapKeyRef), a
// whole-ConfigMap env projection (envFrom configMapRef), a configMap-backed
// volume (volumes[].configMap.name), and a projected configMap source
// (sources[].configMap.name). control-plane projects non-sensitive config to
// ConfigMaps, so a missing ConfigMap key is a real CreateContainerConfigError
// class the gate must catch.
//
// `optional: true` references go to OptionalConfigMaps for the reason
// collectSecretRefs gives: kubelet schedules the pod without them.
func collectConfigMapRefs(v map[string]any, sink refSink) {
	// configMapKeyRef: {name, key} — a single (ConfigMap, key) projection.
	if ckr, ok := mapAt(v, "configMapKeyRef"); ok {
		if name := stringAt(ckr, "name"); name != "" {
			sink.configMap(name, stringAt(ckr, "key"), isOptional(ckr))
		}
	}
	// envFrom configMapRef / volumes[].configMap / sources[].configMap:
	// each is {name} — existence only. All three use the `configMap` key
	// EXCEPT envFrom, which uses `configMapRef`; handle both.
	if cmr, ok := mapAt(v, "configMapRef"); ok {
		if name := stringAt(cmr, "name"); name != "" {
			sink.configMap(name, "", isOptional(cmr))
		}
	}
	if cm, ok := mapAt(v, "configMap"); ok {
		if name := stringAt(cm, "name"); name != "" {
			sink.configMap(name, "", isOptional(cm))
		}
	}
}

// collectImageRef records a container image ref. Only a STRING image is a
// container ref; objects keyed "image" elsewhere (rare) are ignored.
func collectImageRef(v map[string]any, refs *ManifestRefs) {
	if img, ok := v["image"]; ok {
		if s, ok := img.(string); ok && strings.TrimSpace(s) != "" {
			refs.Images[strings.TrimSpace(s)] = struct{}{}
		}
	}
}

// addRef records (ref, key) into a ref→keyset map (Secrets or ConfigMaps),
// lazily allocating the inner set.
func addRef(into map[ResourceRef]map[string]struct{}, ref ResourceRef, key string) {
	if into[ref] == nil {
		into[ref] = map[string]struct{}{}
	}
	into[ref][key] = struct{}{}
}

// mapAt returns m[key] as a map[string]any when present and of that type.
func mapAt(m map[string]any, key string) (map[string]any, bool) {
	raw, ok := m[key]
	if !ok {
		return nil, false
	}
	sub, ok := raw.(map[string]any)
	return sub, ok
}

// isOptional reports whether a Secret/ConfigMap reference carries
// `optional: true` — the Kubernetes marker that the pod runs without it.
func isOptional(m map[string]any) bool {
	b, ok := m["optional"].(bool)
	return ok && b
}

// stringAt returns m[key] as a string when present and of that type.
func stringAt(m map[string]any, key string) string {
	if raw, ok := m[key]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return ""
}

// SecretGetter resolves which keys exist on a Secret in a target cluster.
// GetSecretKeys returns the set of keys present in the named Secret (the
// keys of its `.data`). exists=false means the Secret itself is absent in
// the namespace — every referenced key is then reported missing. An error
// is a genuine lookup failure (kubectl not configured, RBAC denial) and
// aborts the preflight rather than being treated as "missing".
type SecretGetter interface {
	GetSecretKeys(ctx context.Context, kctx, namespace, name string) (keys map[string]struct{}, exists bool, err error)
}

// ConfigMapGetter resolves which keys exist on a ConfigMap in a target
// cluster, mirroring SecretGetter. exists=false means the ConfigMap is
// absent; an error is a genuine lookup failure that aborts the preflight.
type ConfigMapGetter interface {
	GetConfigMapKeys(ctx context.Context, kctx, namespace, name string) (keys map[string]struct{}, exists bool, err error)
}

// ServedKindChecker reports which (group, kind) resource types a target
// cluster's API server actually SERVES — its discovery surface
// (`kubectl api-resources`). The CRD preflight uses it to verify, BEFORE a
// single manifest is applied, that every NON-CORE kind the bundle renders has a
// CRD installed on the cluster. The motivating footgun: a rendered GRPCRoute
// (apiVersion gateway.networking.k8s.io/v1) applied to a cluster that never
// installed the Gateway API channel fails mid-rollout with `no matches for kind
// "GRPCRoute"` — AFTER other resources already applied. The check turns that
// deploy-time partial failure into one fail-fast block.
//
// The contract mirrors the image checker's "don't block on a blind spot, don't
// silently pass a confirmed problem" discipline:
//
//   - (served, nil) — discovery succeeded; served is the set of (group/kind)
//     the cluster serves. The gate compares the bundle's kinds against it.
//   - (_, err) — discovery itself FAILED (kubectl not configured, RBAC denial,
//     unreachable apiserver). The cluster's served set is UNKNOWN, so the gate
//     cannot assert a kind is missing — it surfaces the error and aborts the
//     preflight rather than blocking on (or silently passing) every kind.
//
// Served keys are normalized to "<group>/<kind>" with a lowercased group and
// the kind verbatim (CRD kinds are case-sensitive); a core-group kind keys as
// "/<kind>". ServesKind does the comparison so callers never reimplement the
// key shape.
type ServedKindChecker interface {
	// ServedKinds returns the set of resource types the cluster serves, keyed
	// by servedKindKey(group, kind).
	ServedKinds(ctx context.Context, kctx string) (served map[string]struct{}, err error)
}

// servedKindKey normalizes a (group, kind) pair to the key shape ServedKinds
// returns and ServesKind compares against: "<lowercased-group>/<kind>". The
// group is lowercased (API groups are DNS-style, case-insensitive); the kind is
// kept verbatim (Kubernetes kinds are PascalCase and case-sensitive). A core-
// group kind (group "") keys as "/<kind>".
func servedKindKey(group, kind string) string {
	return strings.ToLower(strings.TrimSpace(group)) + "/" + strings.TrimSpace(kind)
}

// ServesKind reports whether served (from a ServedKindChecker) contains the
// (group, kind). Centralizes the key shape so call sites and the live checker
// agree.
func ServesKind(served map[string]struct{}, group, kind string) bool {
	_, ok := served[servedKindKey(group, kind)]
	return ok
}

// ImageChecker reports whether an image ref is resolvable in its registry.
//
// The outcomes are deliberately distinct, because a deploy GATE must not block
// on its own blind spots — but it equally must not SILENTLY PASS an image it
// could not confirm is present:
//
//   - (true, nil)   — the image is present. Proceed.
//   - (false, err) where errors.Is(err, ErrImageNotFound) — the image is
//     CONFIRMED absent (the registry answered 404 / MANIFEST_UNKNOWN). err
//     carries the registry's answer, which the report prints: a block that
//     names only the ref leaves nobody able to tell what the registry said.
//     (false, nil) means the same thing from a checker with no answer to
//     carry. BLOCK the deploy — after one recheck (see checkOneImage).
//   - (false, err) where errors.Is(err, ErrImageCheckAuthDenied) — the
//     registry refused the lookup with an auth-class denial (denied /
//     unauthorized / 403 forbidden). The image's presence is UNKNOWN, and on
//     a private prod registry an auth-denied manifest lookup is exactly what a
//     genuinely-missing image looks like. BLOCK (cannot confirm), with an
//     actionable message — but the operator can --skip-preflight after
//     verifying. Failing open here is what produces ImagePullBackOff in prod.
//   - (false, err) where errors.Is(err, ErrImageCheckInconclusive) — the
//     check could not reach the registry AT ALL (DNS / connection refused /
//     i/o timeout / docker daemon down). That is a transport problem, not a
//     statement about the image, and the CLUSTER may still pull fine, so the
//     preflight WARNS and PROCEEDS rather than false-failing a present image.
//   - (_, err) for any other error — a genuine checker failure the caller
//     surfaces (aborts the preflight).
type ImageChecker interface {
	ImageExists(ctx context.Context, ref string) (exists bool, err error)
}

// ImageArchChecker reports the architecture(s) an image ref advertises — the
// `architecture` field of a single-platform manifest, or every platform's
// architecture in a multi-arch manifest index. A deploy GATE compares this to
// the TARGET cluster's declared node arch and BLOCKS on mismatch, turning the
// runtime `exec format error` (an amd64 node trying to run an arm64 binary —
// the 2026-06-24 cross-env incident) into a pre-apply failure.
//
// Contract mirrors ImageChecker's "don't block on a blind spot, don't pass on
// a confirmed problem" discipline:
//
//   - (archs, nil)  — the image's architectures, resolved. Compare + gate.
//   - (nil, err) where errors.Is(err, ErrImageCheckInconclusive) — the arch
//     could not be read (transport failure, image absent — already reported by
//     the existence check, daemon down). The arch is UNKNOWN, so the gate
//     WARNS and proceeds rather than false-failing. Mismatch can only be
//     asserted on a KNOWN arch.
//   - (nil, err) for anything else — a genuine checker failure surfaced by the
//     caller.
type ImageArchChecker interface {
	ImageArchitectures(ctx context.Context, ref string) (archs []string, err error)
}

// PullCredsResolver fetches the CLUSTER's registry pull credentials — the
// `.dockerconfigjson` of a kubernetes.io/dockerconfigjson Secret — from the
// target cluster, so the image-verification path can authenticate a registry
// lookup the LOCAL docker daemon would be denied.
//
// The deploy already collects the bundle's imagePullSecret names for the
// existence preflight (ManifestRefs.ImagePullSecrets); this resolves those
// names to the credential blob the cluster would itself use to pull. The
// returned bytes are a docker config.json `{"auths":{...}}` document (the
// decoded `.dockerconfigjson`). A resolver MAY merge several pull Secrets into
// one config (multiple registries). It returns (nil, nil) when no creds are
// available (no imagePullSecrets, or none readable) — the caller then keeps
// today's local-daemon behaviour rather than regressing. An error is a genuine
// lookup failure (kubectl misconfigured) the caller surfaces.
type PullCredsResolver interface {
	ResolveDockerConfig(ctx context.Context, kctx, namespace string, secretNames []string) (dockerConfigJSON []byte, err error)
}

// CredentialedImageChecker is an OPTIONAL capability an ImageChecker /
// ImageArchChecker may implement: given a docker config dir (a directory
// holding a config.json with the cluster's pull creds), it returns a variant
// of itself that authenticates registry lookups with those creds. The
// preflight uses it to RETRY an auth-denied lookup from the CLUSTER's
// perspective — turning a local-daemon "auth denied" (a false negative for an
// image the cluster can pull) into a TRUE existence/arch verdict. A checker
// that does not implement this is used as-is (no credentialed retry).
type CredentialedImageChecker interface {
	// WithDockerConfigDir returns an ImageChecker that runs `docker` with
	// DOCKER_CONFIG=dir, so private-registry lookups authenticate with the
	// cluster's pull creds materialised there.
	WithDockerConfigDir(dir string) ImageChecker
}

// CredentialedImageArchChecker is the ImageArchChecker analogue of
// CredentialedImageChecker.
type CredentialedImageArchChecker interface {
	WithDockerConfigDir(dir string) ImageArchChecker
}

// ErrImageCheckInconclusive marks an image check that could not reach the
// registry at all — a transport-class failure (DNS, connection refused, i/o
// timeout, docker daemon down). That says nothing about the image: the CLUSTER
// may still pull it fine, so the preflight treats it as a non-blocking warning
// ("couldn't verify image X: <reason>; proceeding") rather than a confirmed
// miss. Wrap it with %w to carry the underlying reason.
var ErrImageCheckInconclusive = errors.New("image existence could not be verified")

// ErrImageNotFound marks a CONFIRMED miss: the registry answered, and its
// answer was "no such manifest". Wrap it with %w to carry what the registry
// said, so a blocked deploy shows the evidence rather than just the ref.
var ErrImageNotFound = errors.New("the registry has no such image")

// ErrImageCheckAuthDenied marks an image check the registry refused with an
// auth-class denial (denied / unauthorized / 403 forbidden). Unlike a
// transport error, this DID reach the registry — it just wouldn't answer
// whether the manifest exists. On a private registry (ghcr.io private
// packages, GCP Artifact Registry) a genuinely-MISSING image returns exactly
// this denial, so treating it as inconclusive would let the gate silently pass
// a missing image and ImagePullBackOff in prod. The preflight therefore BLOCKS
// on it (cannot confirm the image is present) with a message naming both
// causes — image not pushed, OR the deploy host lacks pull creds — and the
// --skip-preflight escape. Wrap it with %w to carry the underlying reason.
var ErrImageCheckAuthDenied = errors.New("image existence could not be confirmed (registry denied the lookup)")

// PreflightOpts bundles everything Preflight needs: the rendered manifests
// to scan, the target cluster context + namespace the Secret checks run
// against, and the injected getter/checker.
type PreflightOpts struct {
	// Manifests is the rendered `---`-separated YAML stream about to be
	// applied. Refs are collected from it.
	Manifests string

	// Context is the DECLARED kubectl context (forge.K8sCluster.cluster)
	// the Secret checks run against — the SAME context the apply targets,
	// never the ambient one. Empty disables the Secret check (host-only /
	// compose env with nothing to verify against a cluster).
	Context string

	// Namespace is the deploy's target namespace: where a rendered object
	// that declares no metadata.namespace is applied, and so where ITS
	// Secret / ConfigMap references resolve. An object that declares a
	// namespace resolves its references there instead.
	Namespace string

	// DocumentContexts attributes each rendered document to the kubectl
	// contexts it is applied to, for an env that spans several clusters, so
	// every reference is checked on the cluster its pod actually runs on. It
	// must be the deploy's own router (ScopeManifestsToGroup), not a second
	// model of it. Nil — a single-cluster env — checks every reference on
	// Context. See RefPlacement.Contexts.
	DocumentContexts func(doc string) []string

	// Secrets resolves Secret keys against the target cluster.
	Secrets SecretGetter

	// ConfigMaps resolves ConfigMap keys against the target cluster. Like
	// Secrets, the check runs only when this and Context are both set.
	ConfigMaps ConfigMapGetter

	// ServedKinds resolves which resource types the target cluster serves, so
	// the CRD preflight can BLOCK a deploy that renders a kind (e.g. GRPCRoute)
	// whose CRD / Gateway API channel isn't installed — before the partial
	// apply that otherwise errors `no matches for kind` mid-rollout. The check
	// runs only when this and Context are both set, and only gates NON-CORE
	// kinds (core kinds like Deployment/Service are served by every cluster, so
	// gating them would false-positive on a discovery blind spot). Nil disables
	// the CRD gate (local dev / nothing to verify).
	ServedKinds ServedKindChecker

	// VolumeResize judges every StatefulSet the bundle renders against the
	// live one, so a change to its volumeClaimTemplates that no resize can
	// converge (a shrink, a changed storage class, a claim whose class cannot
	// expand) BLOCKS before anything is applied. Runs only with a Context.
	// Nil disables the check. See VolumeResizeChecker.
	VolumeResize VolumeResizeChecker

	// Images checks image existence against the registry.
	Images ImageChecker

	// ImageRecheckDelay is how long to wait before the one recheck a miss or
	// an inconclusive lookup gets (see checkOneImage). A registry having a bad
	// minute answers wrongly in bursts, so a little space between the looks
	// is what makes the second one independent. Zero rechecks immediately.
	ImageRecheckDelay time.Duration

	// RunningImages lists the images the target cluster is RUNNING right
	// now. An image a live container is running cannot be "missing": the
	// registry's not-found (or refusal) for it is downgraded to a warning
	// that names both facts, instead of blocking a deploy whose image is
	// demonstrably pullable. Consulted only when an image would otherwise
	// block, and only with a Context. Nil disables the downgrade.
	RunningImages RunningImageLister

	// ImageArch reports an image's advertised architecture(s) for the arch
	// gate. Nil disables the gate entirely (the existence check still runs).
	ImageArch ImageArchChecker

	// PullCreds resolves the CLUSTER's registry pull credentials (the
	// .dockerconfigjson of the bundle's imagePullSecrets) from the target
	// cluster. When set AND the image checker implements
	// CredentialedImageChecker AND the bundle declares imagePullSecrets, an
	// AUTH-DENIED registry lookup from the LOCAL docker daemon is RETRIED with
	// those creds, so a present private image the cluster can pull yields a TRUE
	// verdict instead of a false BLOCK. Nil (or no imagePullSecrets, or creds
	// not resolvable) leaves the existing local-daemon behaviour unchanged — the
	// auth-denied lookup still BLOCKS (fail-safe, no regression). Each pull
	// Secret is resolved where the pod that names it reads it — its
	// namespace, on its cluster — and the results are merged.
	PullCreds PullCredsResolver

	// TargetArch is the DECLARED node architecture of the target cluster
	// (GOARCH form: "amd64" / "arm64"), resolved from the env's KCL
	// `deploy.Cluster.platform`. When set AND ImageArch is configured, each
	// checked image's architectures are compared to it and a mismatch BLOCKS
	// the deploy (the exec-format-error gate). EMPTY means the env hasn't
	// declared a platform yet — the gate is INERT (WARN-don't-block) so envs
	// that predate the platform field (incl. the local e2e path) are never
	// false-failed.
	TargetArch string

	// SkipImageRef, when non-nil, returns true for image refs that should
	// NOT be checked (e.g. local k3d / registry.localhost refs in a dev
	// loop). A nil func checks every image (both existence AND arch).
	SkipImageRef func(ref string) bool

	// RequiredSecrets are the env's DECLARED external Secret prerequisites
	// (forge.ExternalSecret) — out-of-band Secrets the deploy depends on but
	// forge does NOT create. UNLIKE the secretKeyRef check above (which is
	// driven by what the rendered manifests reference, each in its
	// referencing object's namespace), a declared prereq names no consumer
	// and carries its OWN namespace (cert-manager's `cloudflare-api-token`
	// lives in the `cert-manager` namespace, not the deploy namespace), so
	// each is checked in its declared namespace. A
	// declared-required-but-absent Secret/key BLOCKS — this is the whole
	// point: it converts "render green, then ACME hangs silently" into a
	// fail-fast pre-apply block. Verified only when opts.Secrets is configured
	// (a SecretGetter against the live target). Empty => no declared prereqs.
	RequiredSecrets []RequiredSecret

	// RequiredSecretContexts are the kubectl contexts in which the declared
	// prerequisites must ALL exist. An environment can deploy one render to
	// several clusters, and a workload scheduled onto the secondary needs its
	// Secret THERE — checking only the primary passes a deploy that then dies
	// at rollout with CreateContainerConfigError, which is the same class of
	// late, opaque failure this preflight exists to prevent.
	//
	// Unlike the manifest-driven secretKeyRef check, this one is NOT gated to
	// remote clusters. That gate exists because forge applies its own projected
	// Secrets moments after the preflight, so checking them locally would
	// false-fail the inner loop. A forge.ExternalSecret is BY DEFINITION one
	// forge does not create — the provisioning is out-of-band — so the
	// rationale does not apply, and skipping it locally removed the check
	// exactly where the local dev loop needed it.
	//
	// Empty falls back to Context, preserving single-cluster behaviour.
	RequiredSecretContexts []string

	// SecretValues, when set, resolves a Secret's full .data value bytes
	// (base64-decoded) for the cross-secret BYTE-MATCH check: ExternalSecrets
	// sharing a `value_group` must carry IDENTICAL bytes under their keys. A
	// drifted copy (same group, different bytes — a half-rotated credential)
	// is caught here before it ships. Nil => the byte-match compare is skipped
	// (the KCL schema still enforces that a group's members declare the same
	// KEY SET; only the live byte equality needs cluster reads).
	SecretValues SecretValueGetter

	// ProvisionedByDeploy are the Secrets THIS deploy creates AFTER the
	// preflight runs, so the live existence check must not demand them
	// up-front. Today that is exactly the forge.KubeconfigSecret declarations
	// forge MINTS on the deploy path (a declared service_account).
	//
	// Without this the gate blocks on its own output: forge refuses to deploy
	// because a Secret is absent, and the deploy it refuses is the one that
	// would have created it. There is no order of operations that fixes it —
	// minting first would grant a credential on a deploy the gate then
	// rejects, and under --dry-run the mint writes nothing at all, so the
	// Secret is absent by construction.
	//
	// This is the same reasoning that already skips the local-cluster Secret
	// check: a Secret forge itself applies moments later is not a missing
	// dependency, it is a not-yet-executed step of this deploy.
	//
	// The set must be DERIVED from the provisioning step rather than
	// re-described beside it, or the two drift and the gate silently stops
	// covering a Secret nobody creates. The caller builds it from the same
	// filter the mint phase iterates.
	//
	// Scope is deliberately narrow: a Secret in here is exempt from the live
	// existence check ONLY. It still counts as DEMAND for every other gate,
	// and a Secret forge does not provision — including a KubeconfigSecret
	// that COPIES a kubeconfig rather than minting one — is not in this set
	// and still BLOCKS when absent.
	ProvisionedByDeploy []ProvisionedSecret

	// SecretSupply is the env's bundle-internal Secret SUPPLY for the
	// RENDER-TIME back-propagation gate (CheckSecretSupply): the Secrets the
	// bundle PROVIDES via a forge.KubeconfigSecret mint, a forge.ExternalSecret
	// out-of-band promise, or any other generated/known Secret forge produces.
	// Rendered-stream Secrets (kind: Secret) are collected from Manifests
	// directly, so this need only carry the entity-derived supply. The gate is
	// pure (NO cluster) and ALWAYS runs — it converts a workload mounting a
	// Secret nothing declares (the silent FailedMount / 15-min ContainerCreating
	// rot) into a fail-fast render-time BLOCK. Empty => only rendered-stream
	// Secrets count as supply. See SecretSupply / CheckSecretSupply.
	SecretSupply []SecretSupply

	// StoreExpect and StoreProbe drive the store-source preflight (SF9): the
	// CRDs, the External Secrets Operator's owner, the store's readiness and
	// whether the store holds the names the store-backed ExternalSecrets read.
	// Each expectation names its OWN cluster, so the check runs whatever
	// opts.Context is. Both must be set for it to run. When it does, the
	// generic served-kinds gate leaves the external-secrets.io group to it,
	// because only the store check knows whether THIS deploy installs the
	// operator that serves those kinds. See CheckStore.
	StoreExpect []StoreExpectation
	StoreProbe  StoreProbe
}

// RequiredSecret is one declared external Secret prerequisite the preflight
// verifies against the live target. It mirrors the cli ExternalSecretEntity
// but stays a plain cluster-package struct so this package never depends on
// the cli entity types.
type RequiredSecret struct {
	// Name / Namespace identify the out-of-band Secret. Namespace is the
	// declared namespace (often NOT the deploy namespace).
	Name      string
	Namespace string
	// Keys are the data keys the consumer reads; each must exist.
	Keys []string
	// ValueGroup ties this Secret to others that must carry identical bytes
	// (the cross-secret byte-match group). Empty => standalone.
	ValueGroup string
	// Contexts are the kubectl contexts THIS Secret must exist in: the
	// clusters that receive its consumer. A multi-cluster env does not need
	// every Secret everywhere — control-plane's prod daemon cluster receives
	// only PriorityClasses and a pre-pull DaemonSet, and requiring the app's
	// Secrets there blocked every prod deploy. Empty => every context the
	// prerequisites are checked in (RequiredSecretContexts, else Context), the
	// conservative choice for a Secret nothing attributes to a cluster.
	Contexts []string
}

// ProvisionedSecret is one Secret THIS deploy creates after the preflight, so
// its absence up-front is not a missing dependency. See
// PreflightOpts.ProvisionedByDeploy for why the gate needs to know.
//
// The match is on namespace AND name, because a reference is: the Secret
// exempts exactly the references that resolve to it, from pods in the
// namespace it is created in. A same-named Secret provisioned elsewhere does
// nothing for a pod in another namespace, which would fail at rollout. The
// cluster is not matched — the provisioning step does not record one, and
// exempting on every cluster errs toward the deploy proceeding on a Secret
// forge does create, rather than blocking on its own output.
type ProvisionedSecret struct {
	// Name is the k8s Secret name this deploy will create.
	Name string
	// Namespace is the declared namespace (empty means the deploy namespace).
	Namespace string
	// By names the provisioning step, for the report.
	By SecretSupplyKind
}

// provisionedSecretRefs is the (namespace, name) set of the Secrets this
// deploy creates after the preflight, with an unset namespace resolved to the
// deploy namespace. Context is left empty: the match ignores the cluster.
// Empty input yields an empty set, which exempts nothing.
func provisionedSecretRefs(provisioned []ProvisionedSecret, deployNamespace string) map[ResourceRef]struct{} {
	out := make(map[ResourceRef]struct{}, len(provisioned))
	for _, p := range provisioned {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue
		}
		namespace := strings.TrimSpace(p.Namespace)
		if namespace == "" {
			namespace = deployNamespace
		}
		out[ResourceRef{Namespace: namespace, Name: name}] = struct{}{}
	}
	return out
}

// withoutProvisionedSecrets drops from the manifest-driven Secret references
// the ones this deploy is itself about to create, so the live existence check
// does not demand a Secret up-front that the same command provisions a few
// phases later.
//
// It filters the REFERENCE set rather than suppressing the finding afterwards
// on purpose: an exempt Secret produces no lookup at all, so the gate never
// reads a cluster to ask a question whose answer it would discard.
//
// Returns refs unchanged when nothing is exempt, so the overwhelmingly common
// case allocates nothing.
func withoutProvisionedSecrets(
	refs map[ResourceRef]map[string]struct{},
	provisioned []ProvisionedSecret,
	deployNamespace string,
) map[ResourceRef]map[string]struct{} {
	exempt := provisionedSecretRefs(provisioned, deployNamespace)
	if len(exempt) == 0 || len(refs) == 0 {
		return refs
	}
	out := make(map[ResourceRef]map[string]struct{}, len(refs))
	for ref, keys := range refs {
		if _, skip := exempt[ResourceRef{Namespace: ref.Namespace, Name: ref.Name}]; skip {
			continue
		}
		out[ref] = keys
	}
	return out
}

// SecretValueGetter resolves a Secret's decoded .data values for the
// cross-secret byte-match check. exists=false means the Secret is absent
// (already reported by the existence check); an error is a genuine lookup
// failure that aborts the preflight.
type SecretValueGetter interface {
	GetSecretValues(ctx context.Context, kctx, namespace, name string) (values map[string][]byte, exists bool, err error)
}

// PreflightResult is the structured outcome of a preflight run — the
// grouped missing-by-secret / missing-by-configmap / missing-images sets,
// plus non-blocking image warnings. OK reports whether the deploy may
// proceed (warnings do NOT block).
type PreflightResult struct {
	// MissingSecretKeys maps "<namespace>/<secret>" to the sorted list of
	// referenced keys that are absent (or the whole Secret, rendered as a
	// single marker when the Secret doesn't exist). The namespace is the
	// referencing object's. When the run spans more than one cluster the
	// key is "<context>:<namespace>/<secret>".
	MissingSecretKeys map[string][]string
	// MissingConfigMapKeys maps "<namespace>/<configmap>" (cluster-qualified
	// like MissingSecretKeys) to the sorted list of referenced keys that are
	// absent (or the whole-ConfigMap marker when the ConfigMap doesn't
	// exist).
	MissingConfigMapKeys map[string][]string
	// MissingImages is the sorted list of image refs CONFIRMED absent.
	MissingImages []string
	// MissingImageEvidence maps each MissingImages ref to what the registry
	// answered, verbatim, when the checker carried it (ErrImageNotFound).
	MissingImageEvidence map[string]string
	// UnverifiableImages is the sorted list of "<ref>: <reason>" entries for
	// images whose registry lookup was AUTH-DENIED (denied / unauthorized /
	// 403). Their presence is unknown and a deploy gate must not silently
	// pass an image it cannot confirm — these BLOCK, with a message naming
	// both possible causes (image not pushed, OR the deploy host lacks pull
	// creds for the registry).
	UnverifiableImages []string
	// ImageWarnings is the sorted list of "could not verify" notes for
	// images whose existence check was inconclusive — a TRANSPORT failure
	// (DNS, connection refused, i/o timeout, docker daemon down) that says
	// nothing about the image. These do NOT block the deploy — they are
	// printed so the user knows the gate couldn't vouch for those images.
	ImageWarnings []string
	// ArchMismatchImages is the sorted list of "<ref>: image is <archs>,
	// cluster nodes are <target>" entries for images whose advertised
	// architecture does NOT include the target cluster's declared arch. These
	// BLOCK — running an arm64 image on amd64 nodes is the exec-format-error
	// crash, caught here before a single pod schedules. Only populated when a
	// target arch is DECLARED (an undeclared target arch can't assert a
	// mismatch).
	ArchMismatchImages []string
	// ArchWarnings is the sorted list of advisory notes for images whose arch
	// could not be read (transport failure / inconclusive). These do NOT
	// block — a mismatch can only be asserted on a known arch.
	ArchWarnings []string
	// MissingCRDs is the sorted list of "<Kind> (<apiVersion>) — required by
	// <manifest-name>" entries for NON-CORE kinds the bundle renders that the
	// target cluster's API server does NOT serve (no installed CRD / Gateway
	// API channel). These BLOCK — applying such a manifest fails `no matches
	// for kind` mid-rollout, AFTER other resources already applied. Only
	// populated when a ServedKindChecker is configured (an undeclared discovery
	// surface can't assert a kind is missing).
	MissingCRDs []string
	// VolumeResizeRefusals is the sorted list of "<namespace>/<name>: <reason>"
	// entries for StatefulSets whose declared volumeClaimTemplates differ from
	// the live ones in a way the pre-rollout resize Job (and the direct-apply
	// recovery) REFUSES: a shrink, a changed storage class or access mode, or a
	// claim that must grow whose StorageClass does not allow expansion. These
	// BLOCK: the apply would fail on the immutable field, and a Flux-applied
	// copy would wedge its whole tree.
	VolumeResizeRefusals []string
	// VolumeResizeWarnings are the StatefulSets whose live volumes could not
	// be read (a transport or permission failure). They do NOT block, and are
	// printed so a clean-looking run is not mistaken for a checked one.
	VolumeResizeWarnings []string
	// OptionalKeyWarnings are the keys an `optional: true` reference reads
	// from a Secret or ConfigMap that EXISTS on the live target but lacks
	// them, one "<Kind> <subject>: <key>" entry each, plus optional
	// resources that could not be read. They do NOT block: the reference is
	// optional down to the key (kubelet starts the pod with the variable
	// unset), and a key absent by design — an optional integration's
	// credential in a shared Secret — is exactly what `optional` is for. A
	// key the Secret must carry blocks through a declaration instead: a
	// required reference, or a forge.ExternalSecret's `keys`. An ABSENT
	// optional resource is not listed at all.
	OptionalKeyWarnings []string
	// MissingRequiredSecretKeys maps "<namespace>/<secret>" (as
	// "<context>:<namespace>/<secret>" when the run spans more than one
	// cluster) to the sorted list
	// of declared-required keys absent on the live target (or the whole-Secret
	// marker when the Secret itself is absent), for the DECLARED external
	// Secret prerequisites (forge.ExternalSecret). These BLOCK — the deploy
	// renders a consumer (e.g. cert-manager's DNS-01 ClusterIssuer) that reads
	// the Secret out-of-band, so its absence hangs ACME/DNS silently after a
	// green apply. Only populated when a SecretGetter is configured.
	MissingRequiredSecretKeys map[string][]string
	// ByteMatchMismatches is the sorted list of advisory notes for
	// cross-secret byte-match groups whose live Secret values are NOT
	// identical across the group (a half-rotated / drifted shared credential).
	// These BLOCK — a value_group declares that the same logical secret is
	// projected to N refs, so a divergence means one consumer has the stale
	// value. Only populated when a SecretValueGetter is configured AND every
	// member Secret exists (a missing member is reported by the existence
	// check, not here).
	ByteMatchMismatches []string
	// UndeclaredSecretMounts is the RENDER-TIME back-propagation result: every
	// Secret a workload mounts/references that NOTHING in the rendered bundle
	// provides (no rendered Secret, no KubeconfigSecret, no ExternalSecret, no
	// generated/known Secret). These BLOCK — the pod would stick on
	// MountVolume.SetUp failed / CreateContainerConfigError forever, with zero
	// error at deploy time. Unlike every other field here this is a PURE,
	// no-cluster check that ALWAYS runs (incl. dry-run / local clusters) — it's
	// the complement to the live Secret preflight. See CheckSecretSupply.
	UndeclaredSecretMounts []UndeclaredSecretMount
	// Store is the store-source preflight: gaps in how store-backed Secrets
	// would be delivered (CRDs, operator owner, store readiness, values the
	// store lacks). Its blocking findings BLOCK; Store.Warnings do not.
	Store StoreFindings
}

// OK reports whether nothing was found missing. Inconclusive image warnings
// are advisory and do NOT make the result fail.
func (r PreflightResult) OK() bool {
	return len(r.MissingSecretKeys) == 0 &&
		len(r.MissingConfigMapKeys) == 0 &&
		len(r.MissingImages) == 0 &&
		len(r.UnverifiableImages) == 0 &&
		len(r.ArchMismatchImages) == 0 &&
		len(r.MissingCRDs) == 0 &&
		len(r.VolumeResizeRefusals) == 0 &&
		len(r.MissingRequiredSecretKeys) == 0 &&
		len(r.ByteMatchMismatches) == 0 &&
		len(r.UndeclaredSecretMounts) == 0 &&
		r.Store.Empty()
}

// wholeSecretMarker is the placeholder listed under a Secret that doesn't
// exist at all (so every referenced key is unsatisfiable). Distinct from a
// real key name so the report reads clearly.
const wholeSecretMarker = "(the Secret does not exist)"

// Preflight runs the deployability checks and returns a grouped, actionable
// error when anything is missing — or nil to proceed. It is safe under
// --dry-run (a pure read-only check that applies nothing). When there is
// nothing to check (no Secret refs and no checkable images) it is a no-op.
//
// Checks run concurrently: one GetSecretKeys per DISTINCT Secret and one
// ImageExists per DISTINCT image, in parallel, so the happy path adds one
// round-trip's latency rather than one-per-ref.
func Preflight(ctx context.Context, opts PreflightOpts) error {
	_, err := PreflightReport(ctx, opts)
	return err
}

// PreflightReport is Preflight that also returns the STRUCTURED result.
//
// WHY BOTH EXIST. The findings are computed structurally and then, on a
// failure, formatted into one error string — so from outside this package the
// only machine-readable thing about a blocked deploy was "it blocked". A
// consumer that wants to render "these two Secret keys are missing" as a list
// would have to parse FormatPreflightReport's prose, and the moment that prose
// is reworded the consumer silently starts reporting nothing.
//
// The error is byte-identical to Preflight's — it is the SAME value, from the
// same run — so a caller can report the structure and still fail exactly as
// before. Preflight now delegates here, which is what keeps the two from
// disagreeing: there is one implementation, and the error-only signature is a
// projection of it.
//
// The result is populated whether or not the run blocks. A clean preflight
// returns a zero-finding result and a nil error, and that is deliberately
// distinguishable from a preflight that never ran (which produces no result at
// all) — "nothing wrong" and "nobody looked" must not present identically.
func PreflightReport(ctx context.Context, opts PreflightOpts) (PreflightResult, error) {
	result, err := runPreflightChecks(ctx, opts)
	if err != nil {
		return result, err
	}
	// Inconclusive image checks are advisory — surface them whether the
	// run blocks or proceeds, so the user knows the gate couldn't vouch
	// for those images (and isn't surprised by a later ImagePullBackOff).
	for _, w := range result.ImageWarnings {
		fmt.Printf("preflight: %s\n", w)
	}
	// Arch checks that couldn't read an image's architecture are advisory
	// too (an undeclared target arch, or an unreadable manifest) — surface
	// them so a later exec-format-error isn't a surprise.
	for _, w := range result.ArchWarnings {
		fmt.Printf("preflight: %s\n", w)
	}
	for _, w := range result.VolumeResizeWarnings {
		fmt.Printf("preflight: %s\n", w)
	}
	for _, w := range result.OptionalKeyWarnings {
		fmt.Printf("preflight: %s\n", w)
	}
	for _, w := range result.Store.Warnings {
		fmt.Printf("preflight: %s\n", w)
	}
	for _, w := range result.Store.Adoptions {
		fmt.Printf("preflight: WARNING: %s\n", w)
	}
	if result.OK() {
		return result, nil
	}
	return result, fmt.Errorf("%s", FormatPreflightReport(result))
}

// refPlacement is where the rendered documents' references resolve: each
// document's own namespace (else the deploy namespace), on the clusters
// DocumentContexts routes it to (else the deploy target).
func (opts PreflightOpts) refPlacement() RefPlacement {
	return RefPlacement{Namespace: opts.Namespace, Context: opts.Context, Contexts: opts.DocumentContexts}
}

// spansClusters reports whether this run checks anything on more than one
// cluster. One rule for every finding, so a report never qualifies some
// subjects by cluster and leaves others for the reader to place.
func spansClusters(opts PreflightOpts, refs ManifestRefs) bool {
	spanned := map[string]struct{}{}
	add := func(c string) {
		if c = strings.TrimSpace(c); c != "" {
			spanned[c] = struct{}{}
		}
	}
	add(opts.Context)
	for _, c := range requiredSecretContexts(opts) {
		add(c)
	}
	for _, rs := range opts.RequiredSecrets {
		for _, c := range rs.Contexts {
			add(c)
		}
	}
	for _, m := range []map[ResourceRef]map[string]struct{}{refs.Secrets, refs.OptionalSecrets, refs.ConfigMaps, refs.OptionalConfigMaps} {
		for ref := range m {
			add(ref.Context)
		}
	}
	return len(spanned) > 1
}

// runPreflightChecks performs the secret + image lookups concurrently and
// assembles the PreflightResult. Split out from Preflight so tests can
// assert on the structured result directly.
func runPreflightChecks(ctx context.Context, opts PreflightOpts) (PreflightResult, error) {
	refs := CollectManifestRefs(opts.Manifests, opts.refPlacement())
	result := PreflightResult{
		MissingSecretKeys:         map[string][]string{},
		MissingConfigMapKeys:      map[string][]string{},
		MissingRequiredSecretKeys: map[string][]string{},
	}

	hasContext := strings.TrimSpace(opts.Context) != ""

	// RENDER-TIME secret back-propagation gate — PURE, no cluster. This is the
	// COMPLEMENT to the live Secret check below: it flags a workload mounting a
	// Secret that NOTHING in the rendered bundle provides (no rendered Secret,
	// KubeconfigSecret, or ExternalSecret), turning a silent 15-minute
	// FailedMount rot into a fail-fast block on the no-cluster path
	// (forge env up dev / --dry-run / host-only).
	//
	// It runs ONLY when the LIVE Secret check is NOT active (no SecretGetter or
	// no context). When a live check IS configured, that check is authoritative
	// for the deploy-namespace Secrets: a cluster-provisioned Secret (out-of-band,
	// like an ExternalSecret) is reported PRESENT, and a genuinely-absent one is
	// already reported by the live MissingSecretKeys path — so running the
	// back-prop gate too would double-report (and false-flag a legitimately
	// cluster-provided Secret the bundle deliberately doesn't render). Gating it
	// to the no-cluster path keeps the two checks non-overlapping: live check
	// when a cluster is present, render-time back-prop when it isn't.
	if opts.Secrets == nil || !hasContext {
		if misses := CheckSecretSupply(opts.Manifests, opts.SecretSupply); len(misses) > 0 {
			result.UndeclaredSecretMounts = misses
		}
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	recordErr := func(e error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		mu.Unlock()
	}
	// sink is the shared write side every fanned-out check records into (result
	// under mu, plus the first-error sink), mirroring keyedResourceCheck's
	// bundled coordination primitives so the extracted per-check helpers stay
	// under the argument limit.
	sink := preflightSink{mu: &mu, result: &result, recordErr: recordErr, multiCluster: spansClusters(opts, refs)}

	// Secret checks — one lookup per distinct Secret, each in the namespace
	// and on the cluster of the object that references it, only when a
	// getter and a target context are configured.
	// The Secrets THIS deploy mints after the preflight are dropped from the
	// reference set first — see PreflightOpts.ProvisionedByDeploy. Everything
	// else is checked exactly as before, including a KubeconfigSecret forge
	// does NOT mint.
	if opts.Secrets != nil && hasContext {
		checkKeyedResources(ctx, keyedResourceCheck{
			wg:       &wg,
			refs:     withoutProvisionedSecrets(refs.Secrets, opts.ProvisionedByDeploy, opts.Namespace),
			optional: withoutProvisionedSecrets(refs.OptionalSecrets, opts.ProvisionedByDeploy, opts.Namespace),
			kind:     "Secret",
			getKeys: func(ctx context.Context, ref ResourceRef) (map[string]struct{}, bool, error) {
				return opts.Secrets.GetSecretKeys(ctx, ref.Context, ref.Namespace, ref.Name)
			},
			out: result.MissingSecretKeys, sink: sink,
		})
	}

	// ConfigMap checks — symmetric with Secrets. A missing ConfigMap key
	// fails a pod identically (CreateContainerConfigError).
	if opts.ConfigMaps != nil && hasContext {
		checkKeyedResources(ctx, keyedResourceCheck{
			wg: &wg, refs: refs.ConfigMaps, optional: refs.OptionalConfigMaps, kind: "ConfigMap",
			getKeys: func(ctx context.Context, ref ResourceRef) (map[string]struct{}, bool, error) {
				return opts.ConfigMaps.GetConfigMapKeys(ctx, ref.Context, ref.Namespace, ref.Name)
			},
			out: result.MissingConfigMapKeys, sink: sink,
		})
	}

	// CRD / served-kind check — ONE discovery lookup against the target
	// cluster, then every NON-CORE kind the bundle renders is verified to be in
	// the served set. A kind whose CRD isn't installed (the GRPCRoute / Gateway
	// API channel footgun) BLOCKS here instead of failing `no matches for kind`
	// mid-rollout. Core kinds (group "") are skipped — every cluster serves
	// them, so gating them would only risk a false-positive on a discovery blind
	// spot. Runs only when a checker and a target context are configured. A
	// discovery FAILURE aborts the preflight (the served set is unknown — we
	// can't assert a kind is missing) rather than blocking on every kind.
	if opts.ServedKinds != nil && hasContext {
		checkServedKinds(ctx, opts, &wg, sink)
	}

	// StatefulSet volume check — one read of each live StatefulSet that has
	// volumeClaimTemplates, judged by the same pkg/stsresize.Decide the resize
	// Job and the direct-apply recovery use.
	if opts.VolumeResize != nil && hasContext {
		checkVolumeResize(ctx, opts, &wg, sink)
	}

	// Cluster-credentialed image checker — resolve the bundle's
	// imagePullSecrets from the TARGET cluster into a temp DOCKER_CONFIG so an
	// AUTH-DENIED local-daemon lookup can be RETRIED with the credentials the
	// cluster would itself pull with. Built ONCE (not per-image). When creds
	// can't be resolved, the bundle has no imagePullSecrets, or the checker
	// isn't credentialed, credImages/credArch stay nil and the auth-denied
	// path keeps today's block-on-can't-confirm behaviour (no regression).
	credImages, credArch, credCleanup := prepareCredentialedCheckers(ctx, opts, refs)
	defer credCleanup()

	// recheckWithClusterCreds re-runs an auth-denied existence lookup with the
	// cluster's pull creds. Returns (exists, retried): retried=false when no
	// credentialed checker is available (caller keeps the auth-denied block).
	recheckExistsWithCreds := func(ref string) (exists bool, retried bool) {
		if credImages == nil {
			return false, false
		}
		ok, err := credImages.ImageExists(ctx, ref)
		if err != nil {
			// Still denied (the cluster creds also lack access — or the secret
			// was the wrong one) or now inconclusive: not a confirmed verdict,
			// so leave it to the caller's existing block/warn handling.
			return false, false
		}
		return ok, true
	}

	// Image checks — one lookup per distinct image, skipping refs the
	// caller marks (local registries) when an image checker is configured.
	if opts.Images != nil {
		checkImages(ctx, opts, refs, &wg, sink, recheckExistsWithCreds)
	}

	// Arch gate — one manifest-inspect per distinct image, comparing the
	// image's advertised architecture(s) to the TARGET cluster's declared
	// arch. Only runs when BOTH an arch checker and a declared target arch
	// are configured: an undeclared target arch can't assert a mismatch
	// (WARN-don't-block), so the gate is inert for envs that predate the
	// platform field (incl. the local e2e path). Skips the same local-
	// registry refs as the existence check.
	target := strings.TrimSpace(opts.TargetArch)
	if opts.ImageArch != nil && target != "" {
		checkImageArchs(ctx, opts, refs, &wg, sink, credArch, target)
	}

	// Declared external Secret prerequisites (forge.ExternalSecret) — each in
	// its OWN declared namespace (often NOT opts.Namespace). One GetSecretKeys
	// per declared Secret; a missing key/Secret BLOCKS. Verified only against a
	// live target (a SecretGetter + a context), like the secretKeyRef check.
	if opts.Secrets != nil && len(opts.RequiredSecrets) > 0 && len(requiredSecretContexts(opts)) > 0 {
		checkRequiredSecrets(ctx, opts, refs, &wg, sink)
	}

	// Store-backed Secrets (forge.StoreSource): the operator and its CRDs, the
	// store's readiness and whether it holds the names read. Each expectation
	// carries its own cluster, so this does not wait on opts.Context.
	if opts.StoreProbe != nil && len(opts.StoreExpect) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			found, err := CheckStore(ctx, opts.StoreProbe, opts.StoreExpect)
			if err != nil {
				sink.recordErr(err)
				return
			}
			sink.mu.Lock()
			sink.result.Store = found
			sink.mu.Unlock()
		}()
	}

	// Cross-secret BYTE-MATCH — for each value_group, read every member's live
	// values and assert byte-identity under the shared keys. A divergence (a
	// half-rotated shared credential) BLOCKS. Runs serially (groups are tiny
	// and rare); gated on a SecretValueGetter being configured.
	if opts.SecretValues != nil && hasContext && len(opts.RequiredSecrets) > 0 {
		checkByteMatchGroupsAsync(ctx, opts, &wg, sink)
	}

	wg.Wait()
	if firstErr != nil {
		return PreflightResult{}, firstErr
	}
	for _, v := range result.MissingRequiredSecretKeys {
		sort.Strings(v)
	}
	sort.Strings(result.ByteMatchMismatches)
	sort.Strings(result.MissingImages)
	sort.Strings(result.UnverifiableImages)
	sort.Strings(result.ImageWarnings)
	sort.Strings(result.ArchMismatchImages)
	sort.Strings(result.ArchWarnings)
	sort.Strings(result.MissingCRDs)
	sort.Strings(result.VolumeResizeRefusals)
	sort.Strings(result.VolumeResizeWarnings)
	sort.Strings(result.OptionalKeyWarnings)
	return result, nil
}

// preflightSink is the shared WRITE side of a preflight run: the mutex guarding
// result, the result being assembled under it, and the first-error sink. Every
// fanned-out check records into it, mirroring keyedResourceCheck's bundled
// coordination primitives so the extracted per-check helpers stay small and
// under the argument limit. It is passed by value; the pointers inside are
// shared, so a goroutine writing through sink.result mutates the one result.
type preflightSink struct {
	mu        *sync.Mutex
	result    *PreflightResult
	recordErr func(error)
	// multiCluster qualifies every finding's subject with its cluster — see
	// spansClusters.
	multiCluster bool
}

// checkImages fans out one existence lookup per distinct image, skipping refs
// the caller marks (local registries). Each goroutine records its outcome into
// the sink under the mutex; a genuine checker failure aborts via sink.recordErr.
// Mirrors the checkKeyedResources fan-out (wg.Add per goroutine).
func checkImages(
	ctx context.Context,
	opts PreflightOpts,
	refs ManifestRefs,
	wg *sync.WaitGroup,
	sink preflightSink,
	recheckExistsWithCreds func(ref string) (exists bool, retried bool),
) {
	live := newLiveImages(ctx, opts)
	for ref := range refs.Images {
		ref := ref
		if opts.SkipImageRef != nil && opts.SkipImageRef(ref) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkOneImage(ctx, opts, ref, recheckExistsWithCreds, live, sink)
		}()
	}
}

// checkOneImage runs one image existence lookup and records its outcome into the
// sink, early-returning on each terminal case so the branching stays flat:
//
//   - present → nothing to record.
//   - confirmed absent → MissingImages (block), with the registry's answer in
//     MissingImageEvidence. "Confirmed" means the RECHECK agreed (lookImage).
//   - auth-denied → RETRY with the CLUSTER's pull creds
//     (recheckExistsWithCreds): the local daemon may simply lack creds the
//     cluster has, so an auth-denied here is a FALSE negative for an image the
//     cluster can pull. A credentialed retry that resolves the lookup is
//     AUTHORITATIVE — present passes, confirmed miss blocks. No creds to retry
//     with (or still denied) → UnverifiableImages (conservative block; the gate
//     must not silently pass an image it can't confirm).
//   - inconclusive transport failure → ImageWarnings (warn and proceed; the
//     CLUSTER may still pull fine).
//   - any other error → recordErr (aborts the preflight).
//
// Either blocking verdict is downgraded to a warning when the live target is
// RUNNING the image (live): a container running it is proof the image can be
// pulled, whatever the registry says this minute.
func checkOneImage(
	ctx context.Context,
	opts PreflightOpts,
	ref string,
	recheckExistsWithCreds func(ref string) (exists bool, retried bool),
	live *liveImages,
	sink preflightSink,
) {
	exists, err := lookImage(ctx, opts, ref)
	switch {
	case err == nil && exists:
		return
	case imageLookIsMiss(exists, err):
		sink.recordMissing(ref, missEvidence(err), live)
	case errors.Is(err, ErrImageCheckAuthDenied):
		credExists, retried := recheckExistsWithCreds(ref)
		if !retried {
			sink.recordUnverifiable(ref, err, live)
			return
		}
		if credExists {
			return // cluster can pull it — TRUE existence verdict
		}
		sink.recordMissing(ref, "not found when checked with the cluster's own pull credentials", live)
	case errors.Is(err, ErrImageCheckInconclusive):
		sink.mu.Lock()
		sink.result.ImageWarnings = append(sink.result.ImageWarnings,
			fmt.Sprintf("couldn't verify image %q: %v; proceeding", ref, err))
		sink.mu.Unlock()
	default:
		sink.recordErr(fmt.Errorf("preflight: check image %q: %w", ref, err))
	}
}

// lookImage asks the registry about ref, and asks AGAIN when the first answer
// is a miss or inconclusive.
//
// One answer is not a verdict. A registry having a bad minute answers in
// bursts — the 2026-10-07 prod release got HTTP 500s for three images and a
// "not found" for a fourth that existed and that prod was running, and that
// single answer blocked the release. So:
//
//   - present on EITHER look is present;
//   - otherwise the second look's answer stands, so a miss blocks only when
//     the recheck is also a miss, and a miss followed by a transport failure
//     is reported as the inconclusive lookup it is (with the first answer
//     kept in the reason).
//
// Auth-denied answers are not rechecked here: they have their own,
// stronger retry with the cluster's pull credentials.
func lookImage(ctx context.Context, opts PreflightOpts, ref string) (bool, error) {
	exists, err := opts.Images.ImageExists(ctx, ref)
	firstMiss := imageLookIsMiss(exists, err)
	if !firstMiss && !errors.Is(err, ErrImageCheckInconclusive) {
		return exists, err
	}
	if opts.ImageRecheckDelay > 0 {
		select {
		case <-ctx.Done():
			return exists, err
		case <-time.After(opts.ImageRecheckDelay):
		}
	}
	again, againErr := opts.Images.ImageExists(ctx, ref)
	switch {
	case againErr == nil && again:
		return true, nil
	case firstMiss && againErr != nil && !imageLookIsMiss(again, againErr):
		return again, fmt.Errorf("%w (the first look answered: %s)", againErr, missEvidence(err))
	}
	return again, againErr
}

// imageLookIsMiss reports whether one ImageExists answer says "absent":
// (false, nil) or an ErrImageNotFound carrying the registry's answer.
func imageLookIsMiss(exists bool, err error) bool {
	return (err == nil && !exists) || errors.Is(err, ErrImageNotFound)
}

// missEvidence is what the registry answered for a miss, as the report
// prints it.
func missEvidence(err error) string {
	if err == nil {
		return "not found (the checker gave no detail)"
	}
	return err.Error()
}

// recordMissing records a confirmed miss — or, when the live target is
// running the image, the warning that says both things.
func (s preflightSink) recordMissing(ref, evidence string, live *liveImages) {
	if where, ok := live.running(ref); ok {
		s.mu.Lock()
		s.result.ImageWarnings = append(s.result.ImageWarnings, fmt.Sprintf(
			"the registry says image %q does not exist (%s), but the live target is running it (%s); proceeding",
			ref, evidence, where))
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result.MissingImages = append(s.result.MissingImages, ref)
	if s.result.MissingImageEvidence == nil {
		s.result.MissingImageEvidence = map[string]string{}
	}
	s.result.MissingImageEvidence[ref] = evidence
}

// recordUnverifiable records an auth-denied lookup nothing could resolve — or,
// when the live target is running the image, a warning instead: the cluster
// demonstrably pulls it, so this machine's missing credentials say nothing
// about the deploy.
func (s preflightSink) recordUnverifiable(ref string, err error, live *liveImages) {
	where, ok := live.running(ref) // before the lock: it may ask the cluster
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok {
		s.result.ImageWarnings = append(s.result.ImageWarnings, fmt.Sprintf(
			"the registry refused to confirm image %q (%v), but the live target is running it (%s); proceeding",
			ref, err, where))
		return
	}
	s.result.UnverifiableImages = append(s.result.UnverifiableImages, fmt.Sprintf("%s (%v)", ref, err))
}

// checkServedKinds runs ONE discovery lookup against the target cluster, then
// BLOCKS on every NON-CORE kind the bundle renders whose CRD / Gateway API
// channel the cluster doesn't serve (the GRPCRoute footgun) instead of failing
// `no matches for kind` mid-rollout. A discovery FAILURE aborts the preflight
// (the served set is unknown — a missing kind can't be asserted) via
// sink.recordErr. Only called when a checker and a target context are set.
func checkServedKinds(ctx context.Context, opts PreflightOpts, wg *sync.WaitGroup, sink preflightSink) {
	gvks := CollectManifestGVKs(opts.Manifests)
	if opts.StoreProbe != nil && len(opts.StoreExpect) > 0 {
		gvks = withoutGroup(gvks, esoAPIGroup)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		served, derr := opts.ServedKinds.ServedKinds(ctx, opts.Context)
		if derr != nil {
			sink.recordErr(fmt.Errorf("preflight: discover served resource kinds on %q: %w", opts.Context, derr))
			return
		}
		missing := missingCRDs(gvks, withBundleCRDs(served, opts.Manifests))
		if len(missing) == 0 {
			return
		}
		sink.mu.Lock()
		sink.result.MissingCRDs = missing
		sink.mu.Unlock()
	}()
}

// checkRequiredSecrets verifies each DECLARED external Secret prerequisite
// (forge.ExternalSecret) against the live target in its OWN declared namespace
// (often NOT opts.Namespace). One GetSecretKeys per declared Secret; a missing
// key/Secret BLOCKS via MissingRequiredSecretKeys. Only called when a
// SecretGetter and a target context are set and prerequisites are declared.
// requiredSecretKeys is the key set a declared prerequisite must carry on
// cluster kctx: the keys it DECLARES, unioned with the keys the rendered
// manifests actually reference from it there.
//
// The declared list is hand-maintained and drifts. A workload that starts
// reading a new key is a change to what the Secret must contain, but nothing
// makes the author also add it to the ExternalSecret's `keys` — so the
// preflight passed on a Secret that was, for the workload's purposes,
// incomplete, and the pod failed at rollout with CreateContainerConfigError
// naming a key the preflight had never been told to look for. Deriving the
// requirement from the reference removes the second place to keep in sync.
//
// A whole-Secret reference (key "") asserts existence only and adds no key.
//
// A reference counts only where it resolves to this Secret: from a pod in the
// prerequisite's namespace, on kctx. A reference collected with no cluster
// (the deploy target was never named — a local env) is placed nowhere in
// particular, so it counts on every cluster, as before references carried one.
func requiredSecretKeys(rs RequiredSecret, refs ManifestRefs, kctx string) []string {
	want := map[string]struct{}{}
	for _, k := range rs.Keys {
		want[k] = struct{}{}
	}
	for _, c := range []string{kctx, ""} {
		for k := range refs.Secrets[ResourceRef{Context: c, Namespace: rs.Namespace, Name: rs.Name}] {
			if k != "" {
				want[k] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(want))
	for k := range want {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func checkRequiredSecrets(ctx context.Context, opts PreflightOpts, refs ManifestRefs, wg *sync.WaitGroup, sink preflightSink) {
	fallback := requiredSecretContexts(opts)
	contextsFor := func(rs RequiredSecret) []string {
		if own := dedupeContexts(rs.Contexts); len(own) > 0 {
			return own
		}
		return fallback
	}
	for _, rs := range opts.RequiredSecrets {
		for _, kctx := range contextsFor(rs) {
			rs, kctx := rs, kctx
			wg.Add(1)
			go func() {
				defer wg.Done()
				present, exists, err := opts.Secrets.GetSecretKeys(ctx, kctx, rs.Namespace, rs.Name)
				if err != nil {
					sink.recordErr(fmt.Errorf("preflight: read required Secret %s/%s in %s: %w", rs.Namespace, rs.Name, kctx, err))
					return
				}
				want := map[string]struct{}{}
				for _, k := range requiredSecretKeys(rs, refs, kctx) {
					want[k] = struct{}{}
				}
				missing := missingSecretKeys(want, present, exists)
				if len(missing) == 0 {
					return
				}
				sort.Strings(missing)
				// The cluster qualifies the key ONLY when the run spans more
				// than one, even if this Secret is checked in only one of
				// them: once there are two, the reader cannot tell which
				// cluster an unqualified miss is in, and omitting it keeps the
				// single-cluster message (by far the common case) unchanged.
				key := ResourceRef{Context: kctx, Namespace: rs.Namespace, Name: rs.Name}.subject(sink.multiCluster)
				sink.mu.Lock()
				sink.result.MissingRequiredSecretKeys[key] = missing
				sink.mu.Unlock()
			}()
		}
	}
}

// requiredSecretContexts resolves the clusters the declared prerequisites are
// verified in, falling back to the single deploy target.
func requiredSecretContexts(opts PreflightOpts) []string {
	if out := dedupeContexts(opts.RequiredSecretContexts); len(out) > 0 {
		return out
	}
	if c := strings.TrimSpace(opts.Context); c != "" {
		return []string{c}
	}
	return nil
}

// dedupeContexts trims, drops empties and de-duplicates, preserving order.
func dedupeContexts(in []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, c := range in {
		if c = strings.TrimSpace(c); c != "" {
			if _, dup := seen[c]; !dup {
				seen[c] = struct{}{}
				out = append(out, c)
			}
		}
	}
	return out
}

// checkByteMatchGroupsAsync runs the cross-secret BYTE-MATCH check on its own
// goroutine (the compare itself is serial — groups are tiny and rare) and
// records any divergence (a half-rotated shared credential) into the sink as a
// BLOCK. Only called when a SecretValueGetter and a target context are set and
// prerequisites are declared.
func checkByteMatchGroupsAsync(ctx context.Context, opts PreflightOpts, wg *sync.WaitGroup, sink preflightSink) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		mismatches := checkByteMatchGroups(ctx, opts.SecretValues, opts.Context, opts.RequiredSecrets, sink.recordErr)
		if len(mismatches) == 0 {
			return
		}
		sink.mu.Lock()
		sink.result.ByteMatchMismatches = mismatches
		sink.mu.Unlock()
	}()
}

// checkImageArchs fans out one manifest-inspect per distinct image, comparing
// the image's advertised architecture(s) to the TARGET cluster's declared arch
// and recording a mismatch (BLOCK) into the sink. Skips the same local-registry
// refs as the existence check. Only called when an arch checker AND a declared
// target arch are configured.
func checkImageArchs(
	ctx context.Context,
	opts PreflightOpts,
	refs ManifestRefs,
	wg *sync.WaitGroup,
	sink preflightSink,
	credArch ImageArchChecker,
	target string,
) {
	for ref := range refs.Images {
		ref := ref
		if opts.SkipImageRef != nil && opts.SkipImageRef(ref) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			checkOneImageArch(ctx, opts.ImageArch, credArch, ref, target, sink)
		}()
	}
}

// checkOneImageArch resolves one image's architecture(s) and BLOCKS when they
// don't include the target cluster's arch (the exec-format-error gate). An
// unreadable arch is a non-blocking warning / genuine error, both handled by
// resolveImageArchs — so when it reports the gate should be skipped, this
// returns without comparing.
func checkOneImageArch(
	ctx context.Context,
	checker ImageArchChecker,
	credArch ImageArchChecker,
	ref, target string,
	sink preflightSink,
) {
	archs, ok := resolveImageArchs(ctx, checker, credArch, ref, sink)
	if !ok {
		return
	}
	if archMatchesTarget(archs, target) {
		return
	}
	sink.mu.Lock()
	sink.result.ArchMismatchImages = append(sink.result.ArchMismatchImages,
		fmt.Sprintf("%s: image is %s, cluster nodes are %s — rebuild for the target platform (exec-format-error class)",
			ref, archList(archs), target))
	sink.mu.Unlock()
}

// resolveImageArchs reads an image's advertised architectures for the arch gate.
// It returns (archs, true) when the arch is KNOWN and the gate should compare;
// (nil, false) when the gate must be SKIPPED for this ref — either an
// inconclusive read (transport / image-absent / daemon down / auth-denied on a
// private registry the LOCAL daemon lacks creds for), recorded as a non-blocking
// ArchWarning, or a genuine checker failure surfaced via sink.recordErr.
//
// An inconclusive read is RETRIED with the CLUSTER's pull creds before warning:
// a private image's manifest the local daemon can't read is exactly the
// false-negative the credentialed path targets, letting the arch gate enforce on
// private images instead of silently skipping them.
func resolveImageArchs(
	ctx context.Context,
	checker ImageArchChecker,
	credArch ImageArchChecker,
	ref string,
	sink preflightSink,
) ([]string, bool) {
	archs, aerr := checker.ImageArchitectures(ctx, ref)
	if aerr == nil {
		return archs, true
	}
	if !errors.Is(aerr, ErrImageCheckInconclusive) {
		sink.recordErr(fmt.Errorf("preflight: read image arch %q: %w", ref, aerr))
		return nil, false
	}
	if credArch != nil {
		if credArchs, cerr := credArch.ImageArchitectures(ctx, ref); cerr == nil {
			return credArchs, true
		}
	}
	sink.mu.Lock()
	sink.result.ArchWarnings = append(sink.result.ArchWarnings,
		fmt.Sprintf("couldn't read architecture of image %q: %v; skipping arch gate for it", ref, aerr))
	sink.mu.Unlock()
	return nil, false
}

// checkByteMatchGroups reads the live values of every ExternalSecret that
// carries a value_group and reports one line per group whose members do NOT
// agree byte-for-byte under their shared keys. A group is checked only when
// EVERY member Secret exists (a missing member is the existence check's job,
// not a byte-match miss); a group whose members declare the same key set
// (enforced at KCL load) but differ in bytes is a half-rotated shared
// credential. Returns nil when every group is consistent (or there are no
// groups). A lookup failure is surfaced via recordErr and the group skipped.
func checkByteMatchGroups(
	ctx context.Context,
	getter SecretValueGetter,
	kctx string,
	required []RequiredSecret,
	recordErr func(error),
) []string {
	// Bucket members by value_group.
	groups := map[string][]RequiredSecret{}
	for _, rs := range required {
		if rs.ValueGroup == "" {
			continue
		}
		groups[rs.ValueGroup] = append(groups[rs.ValueGroup], rs)
	}
	var out []string
	for groupID, members := range groups {
		if len(members) < 2 {
			// A single-member group matches nothing — surfaced as an audit
			// note, not a preflight block.
			continue
		}
		// Resolve every member's values; the shared keys are the same across
		// the group (KCL guarantees the key set matches), so compare the first
		// member to each subsequent one under those keys.
		type resolved struct {
			rs     RequiredSecret
			values map[string][]byte
		}
		var got []resolved
		ok := true
		for _, rs := range members {
			values, exists, err := getter.GetSecretValues(ctx, kctx, rs.Namespace, rs.Name)
			if err != nil {
				recordErr(fmt.Errorf("preflight: read value-group Secret %s/%s: %w", rs.Namespace, rs.Name, err))
				ok = false
				break
			}
			if !exists {
				// A missing member is reported by the existence check; skip the
				// byte compare for this group (can't compare against absent).
				ok = false
				break
			}
			got = append(got, resolved{rs: rs, values: values})
		}
		if !ok || len(got) < 2 {
			continue
		}
		base := got[0]
		for _, other := range got[1:] {
			for _, key := range base.rs.Keys {
				if !bytes.Equal(base.values[key], other.values[key]) {
					out = append(out, fmt.Sprintf(
						"value-group %q: key %q differs between %s/%s and %s/%s — the same logical value is projected to both, but their live bytes don't match (a half-rotated credential)",
						groupID, key, base.rs.Namespace, base.rs.Name, other.rs.Namespace, other.rs.Name))
				}
			}
		}
	}
	return out
}

// missingCRDs returns one report line per DISTINCT non-core (group, kind) the
// bundle renders that the cluster does NOT serve. Core-group kinds (group "")
// are skipped — every cluster serves Deployment/Service/ConfigMap/etc, so
// gating them would only produce a false-positive on a discovery blind spot.
// The first manifest name seen for a missing kind is named in the line so the
// report points the author at WHAT to install the CRD for. Output order is the
// caller's responsibility (the result is sorted before formatting).
// withBundleCRDs returns served plus every (group, kind) a
// CustomResourceDefinition in manifests defines. The apply sends a stream's
// CRDs in their own early batch before anything else (partitionEarlyBatch /
// applyEarlyBatch), so a kind whose CRD ships in the same bundle is served by
// the time its objects are applied. Without this, a release that introduces
// a CRD together with its first object (control-plane's DaemonRollout) was
// blocked on "CRD not served" by the very deploy that installs it. served is
// not modified.
func withBundleCRDs(served map[string]struct{}, manifests string) map[string]struct{} {
	var out map[string]struct{}
	for _, doc := range splitDocs(manifests) {
		if !strings.Contains(doc, "CustomResourceDefinition") {
			continue
		}
		var crd struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Spec       struct {
				Group string `yaml:"group"`
				Names struct {
					Kind string `yaml:"kind"`
				} `yaml:"names"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &crd); err != nil || crd.Kind != "CustomResourceDefinition" ||
			!strings.HasPrefix(crd.APIVersion, "apiextensions.k8s.io/") || crd.Spec.Group == "" || crd.Spec.Names.Kind == "" {
			continue
		}
		if out == nil {
			out = make(map[string]struct{}, len(served)+1)
			for k := range served {
				out[k] = struct{}{}
			}
		}
		out[servedKindKey(crd.Spec.Group, crd.Spec.Names.Kind)] = struct{}{}
	}
	if out == nil {
		return served
	}
	return out
}

func missingCRDs(gvks []ManifestGVK, served map[string]struct{}) []string {
	// De-dupe by (group, kind): a bundle renders many GRPCRoutes but the CRD is
	// either installed or not — report it once. Keep the first name seen so the
	// message stays actionable without listing every offending document.
	type miss struct {
		apiVersion string
		kind       string
		name       string
	}
	seen := map[string]struct{}{}
	var misses []miss
	for _, g := range gvks {
		group := g.group()
		if group == "" {
			// Core kind — served by every cluster. Don't gate it.
			continue
		}
		key := servedKindKey(group, g.Kind)
		if _, dup := seen[key]; dup {
			continue
		}
		if ServesKind(served, group, g.Kind) {
			continue
		}
		seen[key] = struct{}{}
		misses = append(misses, miss{apiVersion: g.APIVersion, kind: g.Kind, name: g.Name})
	}
	if len(misses) == 0 {
		return nil
	}
	out := make([]string, 0, len(misses))
	for _, m := range misses {
		req := m.name
		if req == "" {
			req = "(unnamed manifest)"
		}
		out = append(out, fmt.Sprintf("%s (%s) — required by %s", m.kind, m.apiVersion, req))
	}
	return out
}

// prepareCredentialedCheckers resolves the bundle's imagePullSecrets from the
// TARGET cluster into a temp DOCKER_CONFIG dir and returns credentialed
// variants of the existence + arch checkers that authenticate with them. It is
// the seam that makes the image-verification path use the CLUSTER's pull creds
// rather than the local docker daemon's: an auth-denied local lookup can then
// be retried from the cluster's perspective for a TRUE verdict.
//
// It returns (nil, nil, no-op cleanup) — disabling the credentialed retry —
// when ANY precondition is unmet, so the caller transparently keeps today's
// local-daemon behaviour and never regresses:
//   - no PullCreds resolver, or no target context, or
//   - the bundle declares no imagePullSecrets, or
//   - the configured checker doesn't implement the credentialed capability, or
//   - the creds can't be resolved / materialised.
//
// The returned cleanup removes the temp dir; callers MUST defer it.
func prepareCredentialedCheckers(ctx context.Context, opts PreflightOpts, refs ManifestRefs) (ImageChecker, ImageArchChecker, func()) {
	noop := func() {}
	if opts.PullCreds == nil || strings.TrimSpace(opts.Context) == "" || len(refs.ImagePullSecrets) == 0 {
		return nil, nil, noop
	}
	// At least one of the checkers must be credentialed for the retry to mean
	// anything.
	credImagesCap, imgOK := opts.Images.(CredentialedImageChecker)
	credArchCap, archOK := opts.ImageArch.(CredentialedImageArchChecker)
	if !imgOK && !archOK {
		return nil, nil, noop
	}

	dockerConfig := resolvePullCreds(ctx, opts, refs)
	if len(dockerConfig) == 0 {
		// No usable creds (none of the imagePullSecrets resolved to a
		// dockerconfigjson, or a lookup failed). Fall back — don't regress.
		return nil, nil, noop
	}

	dir, err := os.MkdirTemp("", "forge-preflight-dockercfg-")
	if err != nil {
		return nil, nil, noop
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	// docker reads $DOCKER_CONFIG/config.json. The .dockerconfigjson blob IS a
	// docker config.json ({"auths":{...}}), so it drops straight in. 0600: it
	// carries registry credentials.
	if werr := atomicfile.WriteFile(filepath.Join(dir, "config.json"), dockerConfig, 0o600); werr != nil {
		cleanup()
		return nil, nil, noop
	}

	var credImages ImageChecker
	if imgOK {
		credImages = credImagesCap.WithDockerConfigDir(dir)
	}
	var credArch ImageArchChecker
	if archOK {
		credArch = credArchCap.WithDockerConfigDir(dir)
	}
	return credImages, credArch, cleanup
}

// resolvePullCreds reads the bundle's imagePullSecrets where each pod reads
// them — its own namespace, on its own cluster — one resolver call per
// (cluster, namespace), and merges the results into one docker config. A
// registry configured by several Secrets keeps the first, in sorted order, so
// the choice is deterministic. A location whose lookup fails contributes
// nothing rather than discarding the creds the others resolved. Nil when
// nothing resolved.
func resolvePullCreds(ctx context.Context, opts PreflightOpts, refs ManifestRefs) []byte {
	type location struct{ context, namespace string }
	byLocation := map[location][]string{}
	for ref := range refs.ImagePullSecrets {
		at := location{context: ref.Context, namespace: ref.Namespace}
		if at.context == "" {
			at.context = opts.Context
		}
		byLocation[at] = append(byLocation[at], ref.Name)
	}
	locations := make([]location, 0, len(byLocation))
	for at := range byLocation {
		locations = append(locations, at)
	}
	sort.Slice(locations, func(i, j int) bool {
		if locations[i].context != locations[j].context {
			return locations[i].context < locations[j].context
		}
		return locations[i].namespace < locations[j].namespace
	})

	var configs [][]byte
	for _, at := range locations {
		names := byLocation[at]
		sort.Strings(names)
		cfg, err := opts.PullCreds.ResolveDockerConfig(ctx, at.context, at.namespace, names)
		if err != nil || len(cfg) == 0 {
			continue
		}
		configs = append(configs, cfg)
	}
	return mergeDockerConfigs(configs)
}

// mergeDockerConfigs unions the `auths` of several docker config.json
// documents, first writer wins per registry. A single config passes through
// byte-for-byte; one that does not parse contributes nothing.
func mergeDockerConfigs(configs [][]byte) []byte {
	switch len(configs) {
	case 0:
		return nil
	case 1:
		return configs[0]
	}
	merged := map[string]json.RawMessage{}
	for _, cfg := range configs {
		var parsed struct {
			Auths map[string]json.RawMessage `json:"auths"`
		}
		if json.Unmarshal(cfg, &parsed) != nil {
			continue
		}
		for registry, entry := range parsed.Auths {
			if _, seen := merged[registry]; !seen {
				merged[registry] = entry
			}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	out, err := json.Marshal(map[string]any{"auths": merged})
	if err != nil {
		return nil
	}
	return out
}

// archMatchesTarget reports whether the target cluster arch is among the
// image's advertised architectures. A multi-arch index that includes the
// target (e.g. ["amd64","arm64"] for an amd64 target) MATCHES — the kubelet
// selects the right manifest. An empty arch set is treated as "unknown" →
// matches (don't block on a blind spot; the warn path handles unreadable
// arch). Comparison is case-insensitive and trims whitespace.
func archMatchesTarget(archs []string, target string) bool {
	if len(archs) == 0 {
		return true
	}
	t := strings.ToLower(strings.TrimSpace(target))
	for _, a := range archs {
		if strings.ToLower(strings.TrimSpace(a)) == t {
			return true
		}
	}
	return false
}

// archList renders an image's architecture set for a report message:
// "arm64", or "[amd64 arm64]" for a multi-arch image, "unknown" when empty.
func archList(archs []string) string {
	switch len(archs) {
	case 0:
		return "unknown"
	case 1:
		return archs[0]
	default:
		return "[" + strings.Join(archs, " ") + "]"
	}
}

// keyedResourceCheck carries the inputs for one keyed-resource preflight
// fan-out: the references to verify (each located on its cluster, in its
// namespace), the resource kind, the getter that reads present keys, the
// result map findings land in, and the shared sink (mutex / error / subject
// qualification) the goroutines report into.
type keyedResourceCheck struct {
	wg   *sync.WaitGroup
	refs map[ResourceRef]map[string]struct{}
	// optional are the `optional: true` references (ManifestRefs.Optional*).
	optional map[ResourceRef]map[string]struct{}
	kind     string
	getKeys  func(ctx context.Context, ref ResourceRef) (map[string]struct{}, bool, error)
	out      map[string][]string
	sink     preflightSink
}

// checkKeyedResources fans out one existence/key lookup per distinct
// referenced resource (Secret or ConfigMap), where the referencing pod
// resolves it, and records the missing keys into c.out under the ref's
// subject ("<namespace>/<name>", cluster-qualified on a multi-cluster run).
// c.getKeys returns (presentKeys, exists, err); a lookup error aborts the
// preflight via the sink. Shared by the Secret and ConfigMap checks so both
// behave identically.
//
// An optional reference is checked in its two modes (ManifestRefs.
// OptionalSecrets): an absent resource is no finding at all, and a present
// one that lacks a key the reference reads is an OptionalKeyWarning. A
// whole-resource optional reference has no key to miss, so it costs no
// lookup; nor does a failed lookup of an optional-only resource abort the
// run — it is reported as a warning, because what it would have found does
// not block either.
func checkKeyedResources(ctx context.Context, c keyedResourceCheck) {
	for _, ref := range keyedLookups(c.refs, c.optional) {
		required, isRequired := c.refs[ref]
		optional := optionalOnlyKeys(c.optional[ref], required)
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			present, exists, err := c.getKeys(ctx, ref)
			if err != nil {
				if isRequired {
					c.sink.recordErr(fmt.Errorf("preflight: read %s %s/%s in %s: %w", c.kind, ref.Namespace, ref.Name, ref.Context, err))
					return
				}
				c.sink.mu.Lock()
				c.sink.result.OptionalKeyWarnings = append(c.sink.result.OptionalKeyWarnings, fmt.Sprintf(
					"%s %s: could not be read (%v), so the optional keys %s were not checked",
					c.kind, ref.subject(c.sink.multiCluster), err, strings.Join(optional, ", ")))
				c.sink.mu.Unlock()
				return
			}
			var missing []string
			if isRequired {
				missing = []string{absentMarker(c.kind)}
				if exists {
					missing = missingSecretKeys(required, present, true)
				}
				sort.Strings(missing)
			}
			var warnings []string
			if exists {
				for _, k := range optional {
					if _, ok := present[k]; !ok {
						warnings = append(warnings, fmt.Sprintf(
							"%s %s: optional key %q is absent; the pods that read it start without it (an `optional: true` reference)",
							c.kind, ref.subject(c.sink.multiCluster), k))
					}
				}
			}
			if len(missing) == 0 && len(warnings) == 0 {
				return
			}
			c.sink.mu.Lock()
			if len(missing) > 0 {
				c.out[ref.subject(c.sink.multiCluster)] = missing
			}
			c.sink.result.OptionalKeyWarnings = append(c.sink.result.OptionalKeyWarnings, warnings...)
			c.sink.mu.Unlock()
		}()
	}
}

// keyedLookups is every resource checkKeyedResources reads, in a stable
// order: each required reference, and each optional one that names a key
// (a whole-resource optional reference has nothing to check once present).
func keyedLookups(required, optional map[ResourceRef]map[string]struct{}) []ResourceRef {
	var out []ResourceRef
	for ref := range required {
		out = append(out, ref)
	}
	for ref, keys := range optional {
		if _, dup := required[ref]; dup {
			continue
		}
		for k := range keys {
			if k != "" {
				out = append(out, ref)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].subject(true) < out[j].subject(true) })
	return out
}

// optionalOnlyKeys is the sorted named keys in optional that no required
// reference also reads: a key a required reference reads is already demand.
func optionalOnlyKeys(optional, required map[string]struct{}) []string {
	var out []string
	for k := range optional {
		if _, req := required[k]; k != "" && !req {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// absentMarker is the placeholder listed under a referenced resource that
// does not exist at all. A ConfigMap used to be reported as "(the Secret does
// not exist)".
func absentMarker(kind string) string {
	return "(the " + kind + " does not exist)"
}

// missingSecretKeys returns the referenced keys not satisfied by the
// target Secret. When the Secret doesn't exist, the whole-Secret marker is
// returned (every key is unsatisfiable). A referenced key of "" (whole-
// Secret envFrom reference) is satisfied as long as the Secret exists.
func missingSecretKeys(referenced, present map[string]struct{}, exists bool) []string {
	if !exists {
		return []string{wholeSecretMarker}
	}
	var missing []string
	for key := range referenced {
		if key == "" {
			// Whole-Secret reference — satisfied by the Secret existing.
			continue
		}
		if _, ok := present[key]; !ok {
			missing = append(missing, key)
		}
	}
	return missing
}

// FormatPreflightReport renders the grouped, actionable failure report. The
// shape is deliberately scannable: one block per missing Secret, then the
// missing-images block, then the remediation footer.
func FormatPreflightReport(r PreflightResult) string {
	var b strings.Builder
	b.WriteString("deploy preflight failed — the live target is missing dependencies the rendered manifests require:\n")

	writeMissingKeyBlock(&b, "Secret", r.MissingSecretKeys)
	writeMissingKeyBlock(&b, "ConfigMap", r.MissingConfigMapKeys)

	if len(r.MissingImages) > 0 {
		b.WriteString("\n  Images not found:\n")
		for _, img := range r.MissingImages {
			fmt.Fprintf(&b, "    - %s\n", img)
			if ev := r.MissingImageEvidence[img]; ev != "" {
				fmt.Fprintf(&b, "        registry: %s\n", ev)
			}
		}
	}

	if len(r.UnverifiableImages) > 0 {
		b.WriteString("\n  Images that could not be confirmed (registry denied the lookup):\n")
		for _, img := range r.UnverifiableImages {
			fmt.Fprintf(&b, "    - %s\n", img)
		}
	}

	if len(r.ArchMismatchImages) > 0 {
		b.WriteString("\n  Images built for the WRONG architecture (would crash with exec format error):\n")
		for _, img := range r.ArchMismatchImages {
			fmt.Fprintf(&b, "    - %s\n", img)
		}
	}

	if len(r.MissingCRDs) > 0 {
		b.WriteString("\n  Resource kinds the cluster does NOT serve (missing CRD / Gateway API channel):\n")
		for _, c := range r.MissingCRDs {
			fmt.Fprintf(&b, "    - %s\n", c)
		}
	}

	writeVolumeResizeBlock(&b, r)

	writeStoreBlock(&b, r)

	if len(r.MissingRequiredSecretKeys) > 0 {
		b.WriteString("\n  DECLARED external Secret prerequisites missing (forge.ExternalSecret — provision out-of-band):\n")
		writeMissingKeyBlock(&b, "Secret", r.MissingRequiredSecretKeys)
	}

	if len(r.ByteMatchMismatches) > 0 {
		b.WriteString("\n  Cross-secret byte-match mismatches (a value_group's members carry different bytes):\n")
		for _, m := range r.ByteMatchMismatches {
			fmt.Fprintf(&b, "    - %s\n", m)
		}
	}

	if len(r.UndeclaredSecretMounts) > 0 {
		b.WriteString("\n  Secrets a workload mounts/references but NOTHING in the bundle provides (would FailedMount at schedule time):\n")
		for _, m := range r.UndeclaredSecretMounts {
			who := "a workload"
			if len(m.Workloads) > 0 {
				who = quoteJoin(m.Workloads)
			}
			fmt.Fprintf(&b, "    - Secret %q mounted by %s — no rendered Secret, KubeconfigSecret, or ExternalSecret declares it\n", m.Secret, who)
		}
	}

	b.WriteString("\nNothing was applied. Fix the gaps, then re-run the deploy:\n")
	if len(r.MissingSecretKeys) > 0 {
		b.WriteString("  - provision the missing Secret key(s) in the target cluster/namespace\n")
	}
	if len(r.MissingConfigMapKeys) > 0 {
		b.WriteString("  - provision the missing ConfigMap key(s) in the target cluster/namespace\n")
	}
	if len(r.MissingImages) > 0 {
		b.WriteString("  - build + push the missing image(s) to the registry\n")
	}
	if len(r.UnverifiableImages) > 0 {
		b.WriteString("  - the registry refused to confirm the image(s) above — this means ONE of:\n")
		b.WriteString("      (a) the image was never pushed (build + push it), OR\n")
		b.WriteString("      (b) the deploy host lacks pull credentials for that registry\n")
		b.WriteString("          (e.g. `docker login ghcr.io`, `gcloud auth configure-docker`).\n")
		b.WriteString("    Verify the image really exists, then re-run; only --skip-preflight once you've confirmed it.\n")
	}
	if len(r.ArchMismatchImages) > 0 {
		b.WriteString("  - rebuild the wrong-arch image(s) for the target cluster's node architecture\n")
		b.WriteString("      (forge build --target-arch <arch>, or set deploy.target_arch / the env's K8sCluster.platform).\n")
	}
	if len(r.MissingCRDs) > 0 {
		b.WriteString("  - install the CRD for the kind(s) above on the target cluster.\n")
		b.WriteString("      For Gateway API kinds, prefer the DECLARATIVE path: give the env's\n")
		b.WriteString("      Gateway API controller chart `crds = \"gateway-api\"` (a forge.HelmChart\n")
		b.WriteString("      in the Bundle's helm_charts) and forge installs the version it pins,\n")
		b.WriteString("      Established-gated, before anything that uses it — no kubectl apply.\n")
		b.WriteString("      Out-of-band, the STANDARD channel is the one to install\n")
		b.WriteString("      (kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/<version>/standard-install.yaml):\n")
		b.WriteString("      it carries GA Gateway, HTTPRoute, GRPCRoute and ListenerSet. Use the\n")
		b.WriteString("      experimental channel ONLY for an x-k8s.io alpha kind, and never on a\n")
		b.WriteString("      local cluster alone — it would diverge dev from the cloud.\n")
	}
	writeVolumeResizeFix(&b, r)
	if len(r.MissingRequiredSecretKeys) > 0 {
		b.WriteString("  - provision the DECLARED external Secret(s) above out-of-band in their\n")
		b.WriteString("      namespace (e.g. the cert-manager `cloudflare-api-token`); forge does\n")
		b.WriteString("      not create them, and their absence hangs the consumer (ACME/DNS) silently.\n")
	}
	if len(r.ByteMatchMismatches) > 0 {
		b.WriteString("  - re-sync the value-group Secret(s) above so every ref carries identical bytes\n")
		b.WriteString("      (a divergence means a half-rotated shared credential).\n")
	}
	if len(r.UndeclaredSecretMounts) > 0 {
		b.WriteString("  - declare a forge.KubeconfigSecret (to MINT it) or forge.ExternalSecret (to\n")
		b.WriteString("      promise it out-of-band), render it via a secret_provider, or remove the\n")
		b.WriteString("      mount/reference if the workload doesn't need it.\n")
	}
	b.WriteString("  - or pass --skip-preflight to bypass this check (you accept the risk of a crash-on-apply).")
	return b.String()
}

// writeMissingKeyBlock renders one "<kind> <ns/name> missing keys: [...]"
// line per entry, in deterministic name order. No-op for an empty map.
func writeMissingKeyBlock(b *strings.Builder, kind string, missing map[string][]string) {
	if len(missing) == 0 {
		return
	}
	b.WriteString("\n")
	names := make([]string, 0, len(missing))
	for k := range missing {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(b, "  %s %s missing keys: [%s]\n",
			kind, name, strings.Join(missing[name], ", "))
	}
}

// KubectlSecretGetter is the live SecretGetter: it reads a Secret's `.data`
// KEY NAMES from the target cluster, threading the DECLARED context per
// command (the same per-command --context discipline the rest of the apply
// path uses, so the check never trusts the ambient context). A not-found
// Secret returns exists=false rather than an error so the preflight reports
// it cleanly.
//
// KEYS ONLY, NEVER VALUES. The output is a go-template that prints each key
// and nothing else (secretKeysTemplate), so a value never reaches this
// process — not in a buffer, not in an error string, not in a log. `-o json`
// (and `-o jsonpath={.data}`) would hand forge every value just to read the
// map's keys; a presence check has no business holding them.
type KubectlSecretGetter struct{}

// secretKeysTemplate prints a Secret's `.data` keys, one per line. The value
// is bound to `$_` and never printed.
const secretKeysTemplate = `{{range $k, $_ := .data}}{{$k}}{{"\n"}}{{end}}`

// GetSecretKeys returns the keys of the named Secret's `.data` in namespace.
func (KubectlSecretGetter) GetSecretKeys(ctx context.Context, kctx, namespace, name string) (map[string]struct{}, bool, error) {
	args := []string{"get", "secret", name, "-o", "go-template=" + secretKeysTemplate}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	cmd := kubectlCmd(ctx, kctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "NotFound") || strings.Contains(msg, "not found") {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("kubectl get secret: %v: %s", err, msg)
	}
	keys := map[string]struct{}{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if k := strings.TrimSpace(line); k != "" {
			keys[k] = struct{}{}
		}
	}
	return keys, true, nil
}

// KubectlSecretValueGetter is the live SecretValueGetter: it reads a
// Secret's `.data` and base64-decodes each value, for the cross-secret
// byte-match check. Same per-command --context discipline and not-found →
// exists=false handling as KubectlSecretGetter.
type KubectlSecretValueGetter struct{}

// GetSecretValues returns the decoded `.data` values of the named Secret.
func (KubectlSecretValueGetter) GetSecretValues(ctx context.Context, kctx, namespace, name string) (map[string][]byte, bool, error) {
	args := []string{"get", "secret", name, "-o", "json"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	cmd := kubectlCmd(ctx, kctx, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "NotFound") || strings.Contains(string(out), "not found") {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("kubectl get secret: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var parsed struct {
		Data map[string]string `json:"data"`
	}
	if jerr := json.Unmarshal(out, &parsed); jerr != nil {
		return nil, false, fmt.Errorf("parse secret json: %w", jerr)
	}
	values := make(map[string][]byte, len(parsed.Data))
	for k, v := range parsed.Data {
		dec, derr := base64.StdEncoding.DecodeString(v)
		if derr != nil {
			return nil, false, fmt.Errorf("decode secret key %q: %w", k, derr)
		}
		values[k] = dec
	}
	return values, true, nil
}

// KubectlConfigMapGetter is the live ConfigMapGetter: it reads a ConfigMap's
// `.data` keys from the target cluster via `kubectl --context <ctx> get
// configmap <name> -n <ns> -o json`, mirroring KubectlSecretGetter (same
// declared-context discipline, same not-found → exists=false handling).
type KubectlConfigMapGetter struct{}

// GetConfigMapKeys returns the keys of the named ConfigMap's `.data`.
func (KubectlConfigMapGetter) GetConfigMapKeys(ctx context.Context, kctx, namespace, name string) (map[string]struct{}, bool, error) {
	return kubectlDataKeys(ctx, kctx, namespace, "configmap", name)
}

// KubectlServedKinds is the live ServedKindChecker: it enumerates the resource
// types the target cluster's API server serves via `kubectl --context <ctx>
// api-resources --no-headers -o wide` and keys each by servedKindKey(group,
// kind). This is the cluster's discovery surface — a kind absent from it has no
// installed CRD (the GRPCRoute / Gateway API channel footgun), so the preflight
// can block before the apply that would otherwise fail `no matches for kind`.
//
// The DECLARED context is threaded per command (the same --context discipline
// the rest of the apply path uses). A non-zero exit is a genuine discovery
// failure (kubectl not configured, RBAC denial, unreachable apiserver) returned
// as an error so the gate aborts rather than asserting a kind is missing
// against an unknown served set.
type KubectlServedKinds struct{}

// ServedKinds implements ServedKindChecker. It parses `kubectl api-resources`
// output, whose columns are NAME [SHORTNAMES] APIVERSION NAMESPACED KIND. The
// APIVERSION column is the group/version ("apps/v1", "gateway.networking.k8s.io/
// v1") or a bare version ("v1") for core; the KIND column is the last field.
// We split the group off APIVERSION and key on (group, kind).
func (KubectlServedKinds) ServedKinds(ctx context.Context, kctx string) (map[string]struct{}, error) {
	// --no-headers keeps parsing simple; -o wide guarantees the APIVERSION
	// column is present (some kubectl versions omit it without -o wide).
	cmd := kubectlCmd(ctx, kctx, "api-resources", "--no-headers", "-o", "wide")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("kubectl api-resources: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return parseServedKinds(string(out)), nil
}

// parseServedKinds turns `kubectl api-resources --no-headers -o wide` output
// into the served-kind key set. Each non-blank line is whitespace-split; the
// APIVERSION column (a group/version or bare version) and KIND column are
// located positionally. kubectl's columns are NAME [SHORTNAMES] APIVERSION
// NAMESPACED KIND VERBS [CATEGORIES] — variable because SHORTNAMES/CATEGORIES
// are optional. We anchor on the APIVERSION token (the field containing a "/"
// for grouped resources, or a bare version like "v1" for core) and read KIND as
// the token two positions after it (APIVERSION, NAMESPACED, KIND). A line we
// can't confidently parse is skipped — discovery over-reporting a served kind
// would only relax the gate, and the apply itself remains the backstop.
func parseServedKinds(out string) map[string]struct{} {
	served := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// Find the APIVERSION column: the first field that looks like an API
		// group/version. A grouped resource has a "/" ("apps/v1"); a core
		// resource has a bare version ("v1", "v2beta1"). NAME (and SHORTNAMES)
		// never contain a "/", and NAME is a plural lowercase resource name —
		// so the first "/"-bearing token, or a bare version token, is the
		// APIVERSION. KIND is two tokens after it (APIVERSION NAMESPACED KIND).
		avIdx := -1
		for i, f := range fields {
			if i == 0 {
				continue // NAME is never the apiVersion
			}
			if strings.Contains(f, "/") || looksLikeBareVersion(f) {
				avIdx = i
				break
			}
		}
		if avIdx < 0 || avIdx+2 >= len(fields) {
			continue
		}
		apiVersion := fields[avIdx]
		kind := fields[avIdx+2]
		group, _, _ := strings.Cut(apiVersion, "/")
		if !strings.Contains(apiVersion, "/") {
			group = "" // bare version → core group
		}
		served[servedKindKey(group, kind)] = struct{}{}
	}
	return served
}

// looksLikeBareVersion reports whether s is a Kubernetes API version token with
// no group (the core-group APIVERSION column, e.g. "v1", "v2", "v1beta1",
// "v2beta3"). Used to locate the APIVERSION column for core resources, whose
// token carries no "/". Matches `v<digits>` optionally followed by an
// alpha/beta qualifier.
func looksLikeBareVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	rest := s[1:]
	// Must start with at least one digit.
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	// Remainder (if any) is an alpha/beta qualifier like "beta1" / "alpha2".
	suffix := rest[i:]
	return suffix == "" || strings.HasPrefix(suffix, "alpha") || strings.HasPrefix(suffix, "beta")
}

// kubectlDataKeys reads the `.data` keys of a `kubectl get <kind> <name>`
// object in namespace, threading the DECLARED context per command. A
// not-found object returns exists=false (a clean "missing", not a lookup
// failure); any other non-zero exit (RBAC denial, unreachable apiserver,
// kubectl misconfig) is a real error that aborts the preflight. Shared by the
// Secret and ConfigMap getters — both project `.data` keys identically.
func kubectlDataKeys(ctx context.Context, kctx, namespace, kind, name string) (map[string]struct{}, bool, error) {
	args := []string{"get", kind, name, "-o", "json"}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	cmd := kubectlCmd(ctx, kctx, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "NotFound") || strings.Contains(string(out), "not found") {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("kubectl get %s: %v: %s", kind, err, strings.TrimSpace(string(out)))
	}
	var parsed struct {
		Data map[string]string `json:"data"`
	}
	if jerr := json.Unmarshal(out, &parsed); jerr != nil {
		return nil, false, fmt.Errorf("parse %s json: %w", kind, jerr)
	}
	keys := make(map[string]struct{}, len(parsed.Data))
	for k := range parsed.Data {
		keys[k] = struct{}{}
	}
	return keys, true, nil
}

// DockerImageArchChecker is the live ImageArchChecker: it reads an image's
// advertised architecture(s) via `docker manifest inspect <ref>` against the
// registry. It parses BOTH shapes the command can return:
//
//   - a manifest LIST / OCI index — `.manifests[].platform.architecture`,
//     one entry per platform (a multi-arch image). "unknown" attestation
//     entries (buildx provenance/SBOM) are dropped — they aren't runnable.
//   - a single image manifest — the top-level `.architecture` field.
//
// A lookup that can't reach the registry (transport failure, image absent,
// daemon down) is returned as ErrImageCheckInconclusive so the gate WARNS
// rather than blocking — a mismatch can only be asserted on a known arch.
//
// DockerConfigDir mirrors RegistryImageChecker: set it (via WithDockerConfigDir)
// to read the manifest with the CLUSTER's pull creds when the local daemon
// lacks access to a private registry.
type DockerImageArchChecker struct {
	// DockerConfigDir overrides DOCKER_CONFIG for the docker invocation. Empty
	// inherits the process environment.
	DockerConfigDir string
}

// WithDockerConfigDir returns a copy that runs docker with DOCKER_CONFIG=dir.
// Implements CredentialedImageArchChecker.
func (c DockerImageArchChecker) WithDockerConfigDir(dir string) ImageArchChecker {
	c.DockerConfigDir = dir
	return c
}

// ImageArchitectures returns the distinct architectures ref advertises. See
// the type doc for the two manifest shapes handled.
func (c DockerImageArchChecker) ImageArchitectures(ctx context.Context, ref string) ([]string, error) {
	args := []string{"manifest", "inspect"}
	if registryRefIsInsecure(ref) {
		args = append(args, "--insecure")
	}
	args = append(args, ref)
	cmd := exec.CommandContext(ctx, "docker", args...)
	withDockerConfig(cmd, c.DockerConfigDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		reason := strings.TrimSpace(string(out))
		if reason == "" {
			reason = err.Error()
		}
		// Any non-zero exit leaves the arch UNKNOWN — existence is the
		// existence check's job; here we only fail open so the gate warns.
		return nil, fmt.Errorf("%w: %s", ErrImageCheckInconclusive, reason)
	}
	return parseManifestArchitectures(out)
}

// parseManifestArchitectures extracts the distinct architectures from a
// `docker manifest inspect` JSON blob — a manifest list (`.manifests[].
// platform.architecture`) or a single image (`.architecture`). "unknown"
// platforms (buildx attestation entries) are dropped. Order is stable
// (sorted) so the report message is deterministic.
func parseManifestArchitectures(raw []byte) ([]string, error) {
	var parsed struct {
		Architecture string `json:"architecture"`
		Manifests    []struct {
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if jerr := json.Unmarshal(raw, &parsed); jerr != nil {
		return nil, fmt.Errorf("%w: parse manifest json: %v", ErrImageCheckInconclusive, jerr)
	}
	seen := map[string]struct{}{}
	var archs []string
	add := func(a string) {
		a = strings.TrimSpace(a)
		if a == "" || a == "unknown" {
			return
		}
		if _, ok := seen[a]; !ok {
			seen[a] = struct{}{}
			archs = append(archs, a)
		}
	}
	for _, m := range parsed.Manifests {
		add(m.Platform.Architecture)
	}
	if len(archs) == 0 {
		add(parsed.Architecture)
	}
	sort.Strings(archs)
	return archs, nil
}

// registryRefIsInsecure reports whether an image ref points at a registry
// that should be treated as plain HTTP (the dev-cluster k3d registries
// forge stands up). Mirrors the cli package's isInsecureRegistry — kept
// here so the cluster package's checker is self-contained.
func registryRefIsInsecure(ref string) bool {
	host, _, _ := strings.Cut(ref, "/")
	hostOnly, _, _ := strings.Cut(host, ":")
	switch hostOnly {
	case "localhost", "127.0.0.1", "registry.localhost":
		return true
	}
	return false
}

// LocalImageRef reports whether an image ref targets a LOCAL registry
// (k3d / registry.localhost / localhost:<port> / 127.0.0.1). The deploy
// path uses it as the default SkipImageRef so a local dev loop isn't failed
// by an image that only exists in the in-cluster registry the manifest
// checker can't reach the same way. Best-effort: a missing local image
// still surfaces as an ImagePullBackOff, but local dev iterates fast and a
// hard preflight failure there is more friction than value.
func LocalImageRef(ref string) bool {
	return registryRefIsInsecure(ref)
}

// withDockerConfig points a docker *exec.Cmd at a DOCKER_CONFIG dir when dir
// is non-empty, so the lookup authenticates with the credentials materialised
// there (the cluster's pull creds) instead of the ambient docker login. A
// no-op for an empty dir (inherit the process environment). It seeds Env from
// os.Environ() once so the docker binary still finds PATH/HOME etc.
func withDockerConfig(cmd *exec.Cmd, dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	env := os.Environ()
	// Drop any inherited DOCKER_CONFIG so ours is authoritative, then set it.
	filtered := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "DOCKER_CONFIG=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	filtered = append(filtered, "DOCKER_CONFIG="+dir)
	cmd.Env = filtered
}

// KubectlPullCredsResolver is the live PullCredsResolver: it reads the
// `.dockerconfigjson` of each named imagePullSecret from the TARGET cluster
// (via `kubectl --context <ctx> get secret <name> -n <ns> -o json`) and MERGES
// their `auths` maps into a single docker config.json document. That document
// is what the image-verification path materialises into a temp DOCKER_CONFIG so
// `docker manifest inspect` authenticates with the cluster's pull creds.
//
// A secret that is absent, isn't a dockerconfigjson Secret, or has an
// unparseable blob is SKIPPED (best-effort) rather than failing the resolve —
// the goal is to recover creds when we can, never to turn a credential gap into
// a hard preflight error (that would regress envs that worked before). Only a
// genuine kubectl failure (misconfig / unreachable apiserver) surfaces as an
// error. Returns (nil, nil) when no secret yielded any auths.
type KubectlPullCredsResolver struct{}

// ResolveDockerConfig implements PullCredsResolver.
func (KubectlPullCredsResolver) ResolveDockerConfig(ctx context.Context, kctx, namespace string, secretNames []string) ([]byte, error) {
	merged := map[string]json.RawMessage{}
	for _, name := range secretNames {
		args := []string{"get", "secret", name, "-o", "json"}
		if namespace != "" {
			args = append(args, "-n", namespace)
		}
		cmd := kubectlCmd(ctx, kctx, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			// A not-found pull secret is a SKIP (the existence preflight already
			// reports a genuinely-missing imagePullSecret); any other non-zero
			// exit is a real kubectl failure worth surfacing.
			if strings.Contains(string(out), "NotFound") || strings.Contains(string(out), "not found") {
				continue
			}
			return nil, fmt.Errorf("kubectl get secret %s/%s: %v: %s", namespace, name, err, strings.TrimSpace(string(out)))
		}
		auths := dockerConfigAuthsFromSecret(out)
		for reg, entry := range auths {
			if _, exists := merged[reg]; !exists {
				merged[reg] = entry
			}
		}
	}
	if len(merged) == 0 {
		return nil, nil
	}
	doc := map[string]any{"auths": merged}
	return json.Marshal(doc)
}

// dockerConfigAuthsFromSecret extracts the `auths` map from a
// kubernetes.io/dockerconfigjson Secret's JSON (`kubectl get secret -o json`).
// The Secret stores the docker config under data[".dockerconfigjson"],
// base64-encoded (kubectl -o json reports `.data` already... no — kubectl
// reports `.data` values base64-encoded). Returns an empty map for any Secret
// that isn't a parseable dockerconfigjson (best-effort: a non-cred Secret named
// as a pull secret simply contributes nothing).
func dockerConfigAuthsFromSecret(secretJSON []byte) map[string]json.RawMessage {
	var parsed struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(secretJSON, &parsed); err != nil {
		return nil
	}
	b64, ok := parsed.Data[".dockerconfigjson"]
	if !ok || strings.TrimSpace(b64) == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	var cfg struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(decoded, &cfg); err != nil {
		return nil
	}
	return cfg.Auths
}

// writeVolumeResizeBlock lists the StatefulSet volumes no resize can converge.
// writeStoreBlock renders the store-source findings (no-op when none block).
func writeStoreBlock(b *strings.Builder, r PreflightResult) {
	if blk := FormatStoreFindings(r.Store); blk != "" {
		b.WriteString(blk)
		b.WriteString("\n")
	}
}

func writeVolumeResizeBlock(b *strings.Builder, r PreflightResult) {
	if len(r.VolumeResizeRefusals) == 0 {
		return
	}
	b.WriteString("\n  StatefulSet volumes that cannot be converged (volumeClaimTemplates are immutable; only a LARGER storage request is):\n")
	for _, m := range r.VolumeResizeRefusals {
		fmt.Fprintf(b, "    - %s\n", m)
	}
}

// writeVolumeResizeFix is the remedy for [writeVolumeResizeBlock].
func writeVolumeResizeFix(b *strings.Builder, r PreflightResult) {
	if len(r.VolumeResizeRefusals) == 0 {
		return
	}
	b.WriteString("  - declare the StatefulSet volume(s) above at the live size or larger, and leave their storage\n")
	b.WriteString("      class and access modes alone. A volume cannot shrink; to move data to a smaller or different\n")
	b.WriteString("      volume, restore into a new StatefulSet. A claim whose StorageClass lacks allowVolumeExpansion\n")
	b.WriteString("      must have expansion enabled on the class first.\n")
}
