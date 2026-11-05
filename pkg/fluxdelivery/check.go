package fluxdelivery

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/reliant-labs/forge/pkg/release"
)

// ErrUnsafeRender is a render that breaks one of the template's safety
// properties. Reaching it means the template or the renderer is wrong, never
// the input: [Validate] has already accepted the input.
var ErrUnsafeRender = errors.New("fluxdelivery: unsafe render")

// CheckRendered asserts the safety properties on objs, the render of in
// ([Render]). A writer runs it on the would-be ResourceSet before every write,
// so a template or renderer bug is caught in the writer, attributed to the
// binding, before flux-operator ever sees it. Every golden in this package is
// held to it.
//
// The properties:
//   - exactly one OCIRepository (the input's source) and one Kustomization
//     per applier, in order, and nothing else — no Secret, no RBAC, no
//     Namespace: the template renders only objects one binding owns;
//   - every object in the input's namespace, carrying the input's labels;
//   - the prune opt-out on every object the input does not retire;
//   - deletionPolicy Orphan on every Kustomization unless the binding is
//     torn down (then Delete), and suspend exactly when retired as orphan or
//     carried suspended;
//   - the shard label and kubeConfig agree, every Kustomization applies as
//     the input's ServiceAccount, and one without a kubeConfig impersonates a
//     named one;
//   - the source is digest-pinned to the input's digest, selects the
//     manifests layer, carries the fence, and verifies exactly when the
//     input enforces;
//   - every Kustomization reads the input's source at the applier's path.
func CheckRendered(in BindingInput, objs []*unstructured.Unstructured) error {
	return checkRendered(in, objs, nil)
}

// CheckRenderedSelfHosted is [CheckRendered] for a self-hosted binding
// ([ValidateSelfHosted]): a delivery without identities names no
// ServiceAccount, because kustomize-controller applies as itself there.
func CheckRenderedSelfHosted(in BindingInput, objs []*unstructured.Unstructured, mode SelfHosted) error {
	return checkRendered(in, objs, &mode)
}

func checkRendered(in BindingInput, objs []*unstructured.Unstructured, selfHosted *SelfHosted) error {
	if len(objs) != 1+len(in.Appliers) {
		return fmt.Errorf("%w: %d objects for 1 source and %d appliers (a document with no name is silently dropped)",
			ErrUnsafeRender, len(objs), len(in.Appliers))
	}
	for _, o := range objs {
		if err := checkCommon(in, o); err != nil {
			return err
		}
	}
	if err := checkSource(in, objs[0]); err != nil {
		return err
	}
	for i, a := range in.Appliers {
		if err := checkKustomization(in, a, objs[i+1], selfHosted); err != nil {
			return err
		}
	}
	return nil
}

func checkCommon(in BindingInput, o *unstructured.Unstructured) error {
	if o.GetNamespace() != in.Namespace {
		return fmt.Errorf("%w: %s %s is in namespace %q, not %q", ErrUnsafeRender, o.GetKind(), o.GetName(), o.GetNamespace(), in.Namespace)
	}
	labels := o.GetLabels()
	for k, v := range in.Labels {
		if labels[k] != v {
			return fmt.Errorf("%w: %s %s lacks label %s=%s", ErrUnsafeRender, o.GetKind(), o.GetName(), k, v)
		}
	}
	return nil
}

func wantPruneOptOut(o *unstructured.Unstructured, want bool) error {
	_, has := o.GetAnnotations()[PruneAnnotation]
	if got := o.GetAnnotations()[PruneAnnotation] == PruneDisabled; got != want || (!want && has) {
		return fmt.Errorf("%w: %s %s: prune opt-out present=%t, want %t", ErrUnsafeRender, o.GetKind(), o.GetName(), has, want)
	}
	return nil
}

func checkSource(in BindingInput, o *unstructured.Unstructured) error {
	if o.GetAPIVersion() != SourceAPIVersion || o.GetKind() != KindOCIRepository || o.GetName() != in.Source.Name {
		return fmt.Errorf("%w: first object is %s %s %s, want the source %s", ErrUnsafeRender, o.GetAPIVersion(), o.GetKind(), o.GetName(), in.Source.Name)
	}
	if err := wantPruneOptOut(o, in.Retire == ""); err != nil {
		return err
	}
	if o.GetLabels()[ShardLabel] != ShardMain {
		return fmt.Errorf("%w: the source is not on shard %s", ErrUnsafeRender, ShardMain)
	}
	if o.GetAnnotations()[FenceAnnotation] != in.Fence {
		return fmt.Errorf("%w: the source's %s is %q, want the input's fence %q", ErrUnsafeRender, FenceAnnotation, o.GetAnnotations()[FenceAnnotation], in.Fence)
	}
	for path, want := range map[string]string{
		"spec.url":                     in.Source.URL,
		"spec.ref.digest":              in.Source.Digest,
		"spec.layerSelector.mediaType": release.BundleManifestsLayer,
		"spec.layerSelector.operation": "extract",
	} {
		if got := nestedString(o, path); got != want {
			return fmt.Errorf("%w: source %s is %q, want %q", ErrUnsafeRender, path, got, want)
		}
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(o.Object, "spec", "ref", "tag"); found {
		return fmt.Errorf("%w: the source carries a tag", ErrUnsafeRender)
	}
	_, verifies, _ := unstructured.NestedMap(o.Object, "spec", "verify")
	if want := in.Source.Verify.Mode == VerifyEnforce; verifies != want {
		return fmt.Errorf("%w: source spec.verify present=%t, but verify mode is %q", ErrUnsafeRender, verifies, in.Source.Verify.Mode)
	}
	return nil
}

func checkKustomization(in BindingInput, a Applier, o *unstructured.Unstructured, selfHosted *SelfHosted) error {
	if o.GetAPIVersion() != KustomizeAPIVersion || o.GetKind() != KindKustomization || o.GetName() != a.Name {
		return fmt.Errorf("%w: got %s %s %s, want Kustomization %s", ErrUnsafeRender, o.GetAPIVersion(), o.GetKind(), o.GetName(), a.Name)
	}
	retire := in.Retire
	if retire == "" {
		retire = a.Retire
	}
	if err := wantPruneOptOut(o, retire == ""); err != nil {
		return err
	}
	for k, v := range a.Labels {
		if o.GetLabels()[k] != v {
			return fmt.Errorf("%w: Kustomization %s lacks label %s=%s", ErrUnsafeRender, a.Name, k, v)
		}
	}
	wantPolicy := "Orphan"
	if retire == RetireTeardown {
		wantPolicy = "Delete"
	}
	if got := nestedString(o, "spec.deletionPolicy"); got != wantPolicy {
		return fmt.Errorf("%w: Kustomization %s deletionPolicy %q, want %q", ErrUnsafeRender, a.Name, got, wantPolicy)
	}
	if prune, _, _ := unstructured.NestedBool(o.Object, "spec", "prune"); !prune {
		return fmt.Errorf("%w: Kustomization %s does not prune", ErrUnsafeRender, a.Name)
	}
	suspend, _, _ := unstructured.NestedBool(o.Object, "spec", "suspend")
	if want := retire == RetireOrphan || (retire == "" && a.Suspended); suspend != want {
		return fmt.Errorf("%w: Kustomization %s suspend=%t, want %t", ErrUnsafeRender, a.Name, suspend, want)
	}
	kubeConfig := nestedString(o, "spec.kubeConfig.secretRef.name")
	shard := o.GetLabels()[ShardLabel]
	sa := nestedString(o, "spec.serviceAccountName")
	// Only a self-hosted delivery without identities applies with no identity.
	unnamedAllowed := selfHosted != nil && !selfHosted.ApplyIdentities
	switch {
	case sa != a.ServiceAccountName:
		return fmt.Errorf("%w: Kustomization %s serviceAccountName %q, want %q", ErrUnsafeRender, a.Name, sa, a.ServiceAccountName)
	case kubeConfig != a.KubeConfigSecret:
		return fmt.Errorf("%w: Kustomization %s kubeConfig %q, want %q", ErrUnsafeRender, a.Name, kubeConfig, a.KubeConfigSecret)
	case kubeConfig != "" && (shard != ShardConnected || nestedString(o, "spec.kubeConfig.secretRef.key") != KubeConfigKey):
		return fmt.Errorf("%w: Kustomization %s has a kubeConfig but shard %q", ErrUnsafeRender, a.Name, shard)
	case kubeConfig != "" && nestedString(o, "spec.serviceAccountName") != "":
		return fmt.Errorf("%w: Kustomization %s names a serviceAccountName beside a kubeConfig", ErrUnsafeRender, a.Name)
	case kubeConfig == "" && (shard != ShardMain || (sa == "" && !unnamedAllowed)):
		return fmt.Errorf("%w: Kustomization %s has no kubeConfig, shard %q and serviceAccountName %q",
			ErrUnsafeRender, a.Name, shard, nestedString(o, "spec.serviceAccountName"))
	}
	if got := nestedString(o, "spec.path"); got != a.Path {
		return fmt.Errorf("%w: Kustomization %s path %q, want %q", ErrUnsafeRender, a.Name, got, a.Path)
	}
	wantRef := map[string]any{"apiVersion": SourceAPIVersion, "kind": KindOCIRepository, "name": in.Source.Name}
	if ref, _, _ := unstructured.NestedMap(o.Object, "spec", "sourceRef"); !reflect.DeepEqual(ref, wantRef) {
		return fmt.Errorf("%w: Kustomization %s reads %v, want the binding's source %s", ErrUnsafeRender, a.Name, ref, in.Source.Name)
	}
	return nil
}

func nestedString(o *unstructured.Unstructured, dotted string) string {
	s, _, _ := unstructured.NestedString(o.Object, strings.Split(dotted, ".")...)
	return s
}
