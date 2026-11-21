package fluxdelivery

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/reliant-labs/forge/pkg/release"
)

// The refusals, moved from control-plane's fluxtenant.BuildBundle and
// validateBundleTrees. Each is a fact about the Flux objects the template
// emits, so a writer refuses before it writes anything — a refusal never
// leaves a half-applied environment.
var (
	// ErrInvalidInput is an input whose shape is wrong: schema, a required
	// field, a retire value, a duration.
	ErrInvalidInput = errors.New("fluxdelivery: invalid input")
	// ErrBindingUnsupported is a non-empty binding: the ledger has no
	// per-binding promotion yet, so nothing may write one.
	ErrBindingUnsupported = errors.New("fluxdelivery: the ledger has no per-binding promotion yet; binding must be \"\"")
	// ErrInvalidName is a name, label or annotation Kubernetes would refuse,
	// or one that disagrees with the function that composes it.
	ErrInvalidName = errors.New("fluxdelivery: invalid name")
	// ErrNotDigestPinned is a source not pinned by a sha256 manifest digest.
	ErrNotDigestPinned = errors.New("fluxdelivery: the source must be pinned by its sha256 manifest digest, never a tag")
	// ErrUnknownProvider is a pull provider outside Flux's enum.
	ErrUnknownProvider = errors.New("fluxdelivery: source provider must be one of generic, aws, azure, gcp")
	// ErrNoAppliers is an input with no applier. A source alone fetches
	// bytes, applies nothing and reports Ready: an environment built that
	// way looks healthy forever and never deploys.
	ErrNoAppliers = errors.New("fluxdelivery: the input has no applier, so nothing would apply the bundle")
	// ErrPathMismatch is an applier whose path is not release's answer for
	// its coordinates — the green-over-nothing guard: Flux reconciles a path
	// the artifact does not hold as "no resources" and reports Ready.
	ErrPathMismatch = errors.New("fluxdelivery: applier path disagrees with release.BundleTreePath / BundleClusterPath")
	// ErrNoClusterCredential is an applier with neither a kubeconfig nor an
	// explicit in-cluster identity, or with both. An absent kubeConfig makes
	// kustomize-controller apply to its OWN cluster.
	ErrNoClusterCredential = errors.New("fluxdelivery: an applier needs exactly one of kubeConfigSecret and inCluster")
	// ErrServiceAccountOnConnected is a serviceAccountName beside a
	// kubeconfig. The connected shard applies as the kubeconfig's identity
	// and never as a service account; the hub's admission policy refuses it.
	ErrServiceAccountOnConnected = errors.New("fluxdelivery: a kubeconfig applier must not name a serviceAccountName")
	// ErrDuplicateApplier is two appliers with one cluster or one name: the
	// second Kustomization would overwrite the first.
	ErrDuplicateApplier = errors.New("fluxdelivery: two appliers collide")
	// ErrInvalidTree is a dependency outside the input, an unknown health
	// kind, a flat cluster given tree coordinates or the reverse.
	ErrInvalidTree = errors.New("fluxdelivery: invalid applier tree")
	// ErrInvalidVerify is a verify block enforce mode cannot render.
	ErrInvalidVerify = errors.New("fluxdelivery: invalid verify settings")
)

// The pull providers Flux's OCIRepository accepts.
const (
	ProviderGeneric = "generic"
	ProviderAWS     = "aws"
	ProviderAzure   = "azure"
	ProviderGCP     = "gcp"
)

var (
	dnsLabel      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	dnsSubdomain  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	fencePattern  = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
)

// maxSubdomainLen is Kubernetes' bound on an object name.
const maxSubdomainLen = 253

// templateOwnedPrefixes are label and annotation key prefixes the writer may
// not set: the template stamps them (the shard, the prune opt-out) or
// flux-operator does (its owner labels), and a writer's copy would either
// collide in the rendered YAML or override a safety stamp.
var templateOwnedPrefixes = []string{ShardLabel, "fluxcd.controlplane.io/", "resourceset.fluxcd.controlplane.io/"}

// Validate refuses an input the template must not render. It is pure, and
// it runs before anything is written: the hub converger and `forge env
// deploy` call the same function.
//
// It does NOT judge the input against the LIVE ResourceSet (the shrink,
// path-invariance and fence refusals): those compare two inputs of one
// binding and belong to the writer.
func Validate(in BindingInput) error {
	return validate(in, nil)
}

// validate is Validate, and ValidateSelfHosted's structural half when
// selfHosted is set: there an in-cluster tree is the norm, and a delivery
// without identities names none on its trees.
func validate(in BindingInput, selfHosted *SelfHosted) error {
	if in.Schema != Schema {
		return fmt.Errorf("%w: schema %q, want %q", ErrInvalidInput, in.Schema, Schema)
	}
	for field, value := range map[string]string{"org": in.Org, "env": in.Env, "namespace": in.Namespace} {
		if err := validateLabel(field, value); err != nil {
			return err
		}
	}
	if in.Binding != "" {
		return fmt.Errorf("%w (got %q)", ErrBindingUnsupported, in.Binding)
	}
	if !fencePattern.MatchString(in.Fence) {
		return fmt.Errorf("%w: fence %q is not a non-negative decimal integer", ErrInvalidInput, in.Fence)
	}
	if in.Promotion == "" {
		return fmt.Errorf("%w: promotion is required: an input projects one ledger row", ErrInvalidInput)
	}
	switch in.Retire {
	case "", RetireOrphan, RetireTeardown:
	default:
		return fmt.Errorf("%w: retire %q, want \"\", %q or %q", ErrInvalidInput, in.Retire, RetireOrphan, RetireTeardown)
	}
	if err := validateMetadata("labels", in.Labels); err != nil {
		return err
	}
	if err := validateSource(in); err != nil {
		return err
	}
	return validateAppliers(in, selfHosted)
}

func validateSource(in BindingInput) error {
	s := in.Source
	if want := SourceName(in.Org, in.Env, in.Binding); s.Name != want {
		return fmt.Errorf("%w: source name %q, want SourceName = %q", ErrInvalidName, s.Name, want)
	}
	if err := validateSubdomain("source name", s.Name); err != nil {
		return err
	}
	rest, ok := strings.CutPrefix(s.URL, "oci://")
	if !ok || rest == "" || strings.Contains(rest, "://") {
		return fmt.Errorf("%w: source url %q must be oci://<repository>, with exactly one scheme", ErrInvalidInput, s.URL)
	}
	if !digestPattern.MatchString(s.Digest) {
		return fmt.Errorf("%w: got %q", ErrNotDigestPinned, s.Digest)
	}
	if err := validateDuration("source interval", s.Interval, false); err != nil {
		return err
	}
	switch s.Provider {
	case "", ProviderGeneric, ProviderAWS, ProviderAzure, ProviderGCP:
	default:
		return fmt.Errorf("%w: got %q", ErrUnknownProvider, s.Provider)
	}
	if s.PullSecret != "" {
		if err := validateSubdomain("source pullSecret", s.PullSecret); err != nil {
			return err
		}
	}
	return validateVerify(s.Verify)
}

func validateVerify(v Verify) error {
	switch v.Mode {
	case VerifyOff, VerifyAudit:
		return nil
	case VerifyEnforce:
	default:
		return fmt.Errorf("%w: mode %q, want %q, %q or %q", ErrInvalidVerify, v.Mode, VerifyOff, VerifyAudit, VerifyEnforce)
	}
	if v.Provider != "cosign" && v.Provider != "notation" {
		return fmt.Errorf("%w: enforce needs provider cosign or notation, got %q", ErrInvalidVerify, v.Provider)
	}
	switch {
	case v.SecretRef != "" && len(v.OIDC) > 0:
		return fmt.Errorf("%w: secretRef (key-based) and oidc (keyless) are exclusive", ErrInvalidVerify)
	case v.SecretRef == "" && len(v.OIDC) == 0:
		return fmt.Errorf("%w: enforce needs a secretRef of trusted keys or at least one oidc identity; "+
			"with neither nothing could pass", ErrInvalidVerify)
	case v.SecretRef != "":
		return validateSubdomain("verify secretRef", v.SecretRef)
	}
	for _, id := range v.OIDC {
		if id.Issuer == "" || id.Subject == "" {
			return fmt.Errorf("%w: an oidc identity needs both issuer and subject", ErrInvalidVerify)
		}
		for _, pattern := range []string{id.Issuer, id.Subject} {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("%w: oidc pattern %q: %v", ErrInvalidVerify, pattern, err)
			}
		}
	}
	return nil
}

func validateAppliers(in BindingInput, selfHosted *SelfHosted) error {
	if len(in.Appliers) == 0 {
		return fmt.Errorf("%w: org %s env %s", ErrNoAppliers, in.Org, in.Env)
	}
	names := make(map[string]bool, len(in.Appliers))
	flat := map[string]bool{}
	tree := map[string]bool{}
	for _, a := range in.Appliers {
		if err := validateApplier(in, a, selfHosted); err != nil {
			return err
		}
		if names[a.Name] {
			return fmt.Errorf("%w: name %q is produced twice; the second Kustomization would overwrite the first",
				ErrDuplicateApplier, a.Name)
		}
		names[a.Name] = true
		if a.Phase == "" {
			if flat[a.Cluster] {
				return fmt.Errorf("%w: cluster %q is named twice", ErrDuplicateApplier, a.Cluster)
			}
			flat[a.Cluster] = true
		} else {
			tree[a.Cluster] = true
		}
	}
	for cluster := range flat {
		if tree[cluster] {
			return fmt.Errorf("%w: cluster %q is both a flat cluster and a tree cluster", ErrDuplicateApplier, cluster)
		}
	}
	// Dependencies last: they may name an applier declared after them.
	for _, a := range in.Appliers {
		for _, dep := range a.DependsOn {
			if !names[dep] || dep == a.Name {
				return fmt.Errorf("%w: applier %s depends on %q, which is not another applier of this binding "+
					"(a dependency outside it belongs in externalDependsOn)", ErrInvalidTree, a.Name, dep)
			}
		}
		for _, dep := range a.ExternalDependsOn {
			if err := validateSubdomain("externalDependsOn of "+a.Name, dep); err != nil {
				return err
			}
			if names[dep] {
				return fmt.Errorf("%w: applier %s lists %q in externalDependsOn, but it is in this binding",
					ErrInvalidTree, a.Name, dep)
			}
		}
	}
	return nil
}

func validateApplier(in BindingInput, a Applier, selfHosted *SelfHosted) error {
	if a.Source != in.Source.Name {
		return fmt.Errorf("%w: applier %s reads source %q, but this binding's one source is %q",
			ErrInvalidInput, a.Name, a.Source, in.Source.Name)
	}
	if strings.TrimSpace(a.Cluster) == "" {
		// The unclustered tree holds documents attributed to no cluster,
		// which nothing applies; the writer filters it out.
		return fmt.Errorf("%w: applier %s names no cluster; the unclustered tree (%s) has nothing to apply to "+
			"and must be filtered out before building", ErrNoClusterCredential, a.Name, release.BundleClusterPath(""))
	}
	var wantName, wantPath string
	if a.Phase == "" {
		if a.Unit != "" {
			return fmt.Errorf("%w: applier %s names unit %q but no phase", ErrInvalidTree, a.Name, a.Unit)
		}
		wantName = KustomizationName(in.Org, in.Env, in.Binding, a.Cluster)
		wantPath = release.BundleClusterPath(a.Cluster)
	} else {
		switch {
		case !a.Phase.Valid():
			return fmt.Errorf("%w: applier %s has phase %q", ErrInvalidTree, a.Name, a.Phase)
		case release.FlatCluster(a.Cluster):
			return fmt.Errorf("%w: cluster %q is a flat cluster and has no trees", ErrInvalidTree, a.Cluster)
		}
		wantName = TreeKustomizationName(in.Org, in.Env, in.Binding, a.Cluster, a.Unit, a.Phase)
		wantPath = release.BundleTreePath(a.Cluster, a.Unit, a.Phase)
	}
	if a.Name != wantName {
		return fmt.Errorf("%w: applier name %q, want %q for cluster %q unit %q phase %q",
			ErrInvalidName, a.Name, wantName, a.Cluster, a.Unit, a.Phase)
	}
	if err := validateSubdomain("applier name", a.Name); err != nil {
		return err
	}
	if len(a.Name) > maxLabelLen {
		return fmt.Errorf("%w: applier name %q exceeds %d characters; kustomize-controller stamps it as a label value",
			ErrInvalidName, a.Name, maxLabelLen)
	}
	if a.Path != wantPath {
		return fmt.Errorf("%w: applier %s carries path %q, want %q. A Kustomization at a path the artifact does "+
			"not hold reconciles as \"no resources\" and reports Ready", ErrPathMismatch, a.Name, a.Path, wantPath)
	}
	switch a.Retire {
	case "", RetireOrphan:
	default:
		return fmt.Errorf("%w: applier %s retire %q, want \"\" or %q (teardown is binding-wide)",
			ErrInvalidInput, a.Name, a.Retire, RetireOrphan)
	}
	switch {
	case a.InCluster == (a.KubeConfigSecret != ""):
		return fmt.Errorf("%w: applier %s (inCluster %t, kubeConfigSecret %q)",
			ErrNoClusterCredential, a.Name, a.InCluster, a.KubeConfigSecret)
	case a.InCluster && a.ServiceAccountName == "" && (selfHosted == nil || selfHosted.ApplyIdentities):
		return fmt.Errorf("%w: applier %s is in-cluster with no serviceAccountName to impersonate; "+
			"the destination Role is the boundary there, so it is refused rather than defaulted", ErrNoClusterCredential, a.Name)
	case a.InCluster && a.Phase != "" && selfHosted == nil:
		return fmt.Errorf("%w: tree %s is in-cluster; every tree applies through a kubeconfig", ErrNoClusterCredential, a.Name)
	case !a.InCluster && a.ServiceAccountName != "":
		return fmt.Errorf("%w: applier %s names %q", ErrServiceAccountOnConnected, a.Name, a.ServiceAccountName)
	}
	if a.KubeConfigSecret != "" {
		if err := validateSubdomain("kubeConfigSecret of "+a.Name, a.KubeConfigSecret); err != nil {
			return err
		}
	}
	if a.ServiceAccountName != "" {
		if err := validateSubdomain("serviceAccountName of "+a.Name, a.ServiceAccountName); err != nil {
			return err
		}
	}
	if a.TargetNamespace != "" {
		if err := validateLabel("targetNamespace of "+a.Name, a.TargetNamespace); err != nil {
			return err
		}
	}
	if err := validateMetadata("labels of "+a.Name, a.Labels); err != nil {
		return err
	}
	if err := validateMetadata("commonLabels of "+a.Name, a.CommonLabels); err != nil {
		return err
	}
	if err := validateDuration("timeout of "+a.Name, a.Timeout, false); err != nil {
		return err
	}
	if err := validateDuration("retryInterval of "+a.Name, a.RetryInterval, true); err != nil {
		return err
	}
	if a.Phase != "" && a.RetryInterval == "" {
		return fmt.Errorf("%w: tree %s has no retry interval; a tree's per-attempt wait is short and must retry",
			ErrInvalidTree, a.Name)
	}
	for _, h := range a.Health {
		want, ok := HealthAPIVersion(h.Kind)
		if !ok || h.APIVersion != want || h.Name == "" {
			return fmt.Errorf("%w: applier %s is judged on %s %s/%s; known kinds need their apiVersion and a name",
				ErrInvalidTree, a.Name, h.APIVersion, h.Kind, h.Name)
		}
	}
	for _, e := range a.HealthExprs {
		want, ok := HealthAPIVersion(e.Kind)
		if !ok || e.APIVersion != want || strings.TrimSpace(e.Current) == "" {
			return fmt.Errorf("%w: applier %s has a health expression for %s %s with current %q",
				ErrInvalidTree, a.Name, e.APIVersion, e.Kind, e.Current)
		}
	}
	for _, p := range a.Patches {
		if strings.TrimSpace(p.Patch) == "" || p.Target.Kind == "" || p.Target.Name == "" {
			return fmt.Errorf("%w: applier %s has a patch without a body, a target kind or a target name",
				ErrInvalidInput, a.Name)
		}
	}
	return nil
}

// validateLabel checks an RFC-1123 label (an org, env or namespace).
func validateLabel(field, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("%w: %s is required", ErrInvalidName, field)
	case len(value) > maxLabelLen:
		return fmt.Errorf("%w: %s %q exceeds %d characters", ErrInvalidName, field, value, maxLabelLen)
	case !dnsLabel.MatchString(value):
		return fmt.Errorf("%w: %s %q is not an RFC-1123 label", ErrInvalidName, field, value)
	}
	return nil
}

// validateSubdomain checks an object name, including the per-label bound a
// plain length check would miss.
func validateSubdomain(field, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidName, field)
	}
	if len(value) > maxSubdomainLen {
		return fmt.Errorf("%w: %s %q is %d characters, over the %d-character bound for an object name",
			ErrInvalidName, field, value, len(value), maxSubdomainLen)
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) > maxLabelLen {
			return fmt.Errorf("%w: %s %q has a %d-character segment (%q), over the %d-character bound",
				ErrInvalidName, field, value, len(label), label, maxLabelLen)
		}
	}
	if !dnsSubdomain.MatchString(value) {
		return fmt.Errorf("%w: %s %q is not an RFC-1123 subdomain", ErrInvalidName, field, value)
	}
	return nil
}

// validateMetadata checks label keys and values, and refuses the keys the
// template or flux-operator own.
func validateMetadata(field string, m map[string]string) error {
	for k, v := range m {
		if errs := validation.IsQualifiedName(k); len(errs) > 0 {
			return fmt.Errorf("%w: %s key %q: %s", ErrInvalidName, field, k, strings.Join(errs, "; "))
		}
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			return fmt.Errorf("%w: %s %q value %q: %s", ErrInvalidName, field, k, v, strings.Join(errs, "; "))
		}
		for _, owned := range templateOwnedPrefixes {
			if strings.HasPrefix(k, owned) {
				return fmt.Errorf("%w: %s sets %q, which the template or flux-operator owns", ErrInvalidName, field, k)
			}
		}
	}
	return nil
}

// validateDuration requires a positive duration in its canonical Go form.
//
// CANONICAL, because the API server stores what it is given: "5m" and "5m0s"
// are one duration and two specs, and a render that spells a live object's
// duration differently bumps its generation and requeues Flux for nothing.
func validateDuration(field, value string, optional bool) error {
	if value == "" && optional {
		return nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return fmt.Errorf("%w: %s %q is not a positive duration", ErrInvalidInput, field, value)
	}
	if d.String() != value {
		return fmt.Errorf("%w: %s %q is not in canonical form %q; a differently spelled duration is a different spec",
			ErrInvalidInput, field, value, d.String())
	}
	return nil
}
