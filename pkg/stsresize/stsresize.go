// Package stsresize converges a StatefulSet whose declared volume size grew.
//
// # The problem
//
// A StatefulSet's volumeClaimTemplates are immutable. Raising the size an env
// declares for a StatefulSet's volume therefore makes every apply of the new
// render fail:
//
//	The StatefulSet "clickhouse" is invalid: spec: Forbidden: updates to
//	statefulset spec for fields other than 'replicas', 'ordinals', 'template',
//	'updateStrategy', 'persistentVolumeClaimRetentionPolicy' and
//	'minReadySeconds' are forbidden
//
// (newer API servers say `spec.volumeClaimTemplates: Invalid value: ...: field
// is immutable`). The template is not even what decides the size of a running
// volume: it is read once, when the StatefulSet controller creates a claim, and
// never again. Growing a live volume means growing its PersistentVolumeClaim,
// which IS mutable, grow-only, when the claim's StorageClass allows expansion.
//
// Without a converging step a declared size can never change after the first
// deploy: either the template is left at the old value and live and declared
// disagree for ever, or the apply wedges. Under a reconciler the wedge is
// total — Flux dry-runs every object of a Kustomization before applying any of
// them, so one rejected StatefulSet stops the whole unit from converging.
//
// # What this package does
//
// The production procedure, made a function:
//
//  1. Decide whether the ONLY difference the API server refuses is a growth of
//     a volumeClaimTemplate's storage request. Anything else — a shrink, a
//     changed storage class, access mode, selector or serviceName, an added or
//     removed template — is refused with a reason, and nothing is written.
//  2. Grow every existing claim of each grown template that is smaller than the
//     declared size, after checking its StorageClass has allowVolumeExpansion.
//     Claims already at or above the size are left alone (an operator may have
//     expanded one ahead of the declaration; that is the declaration catching
//     up, not drift). Claims of ordinals the StatefulSet has scaled away from
//     are grown too, so a later scale-up does not bring back a small volume.
//  3. Delete the StatefulSet object with propagationPolicy=Orphan and wait for
//     it to be gone. Its pods, claims and ControllerRevisions keep running and
//     lose only their owner reference.
//
// The caller then re-applies the declared StatefulSet. The new object adopts
// the orphaned pods (they match its selector) and its revisions; when the pod
// template is unchanged the pods are not restarted, and when it changed the
// normal rolling update follows.
//
// Every step is idempotent, so a converge interrupted at any point is finished
// by running it again: claims already grown are skipped, and a StatefulSet
// already orphan-deleted is simply created by the re-apply.
//
// # Who calls it
//
// Two callers run the procedure, by two routes that share one decision:
//
//   - forge's direct apply runs [Converge] when an apply fails on a
//     StatefulSet's immutable spec (internal/cluster). It holds a cluster
//     client, so it uses the [Cluster] interface.
//   - A reconciled bundle has no such caller: Flux dry-runs every object of a
//     Kustomization before applying any, so one rejected StatefulSet stops the
//     whole tree, and nothing of forge's runs before it. The render therefore
//     carries the procedure itself: [Build] makes a pre-rollout Job (with the
//     namespace-scoped identity it runs as) whose container runs [Script], the
//     same steps in shell and jq. The bundle orders a pre tree ahead of the
//     rollout tree, so the Job runs first and every write stays inside an
//     identity the bundle declares.
//
// The decision is pure ([Decide]); the cluster access is the [Cluster]
// interface. script_test.go runs [Script] and [Decide] over one table so the
// two cannot drift.
package stsresize

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Refusals. Each wraps a detail naming the template or claim; match with
// errors.Is. Nothing has been written to the cluster when one is returned.
var (
	// ErrShrink is a declared volume size below the live template's. A
	// PersistentVolumeClaim cannot shrink, so the only honest outcome is to
	// refuse: lowering the template alone would describe volumes that do not
	// exist.
	ErrShrink = errors.New("stsresize: a volume cannot shrink")
	// ErrNotExpandable is a claim that must grow but whose StorageClass does
	// not set allowVolumeExpansion (or which has no class at all).
	ErrNotExpandable = errors.New("stsresize: the claim's storage class does not allow volume expansion")
	// ErrTemplateChanged is a volumeClaimTemplate change other than a larger
	// storage request: a template added, removed or renamed, or a changed
	// storage class, access mode, selector, volume mode, label or annotation.
	// Those describe different volumes, not bigger ones, and recreating the
	// StatefulSet would leave every existing claim contradicting the template.
	ErrTemplateChanged = errors.New("stsresize: a volumeClaimTemplate changed in a way other than a larger storage request")
	// ErrOtherImmutable is a change to another immutable StatefulSet field
	// (selector, serviceName, podManagementPolicy) alongside the volume
	// growth, as judged by the API server's own dry run. Orphan-recreating the
	// object then would create a StatefulSet that does not adopt the running
	// pods, so it is refused.
	ErrOtherImmutable = errors.New("stsresize: the StatefulSet changes an immutable field other than its volume sizes")
)

// Plan is what converging one StatefulSet takes. The zero Plan (Recreate
// false) means there is nothing to converge: the live templates already
// declare the declared sizes.
type Plan struct {
	Namespace string
	Name      string
	// Grown lists each volumeClaimTemplate whose storage request grows.
	Grown []TemplateGrowth
	// Resize lists each existing claim that must be grown, in name order.
	Resize []ClaimResize
	// Recreate reports that the StatefulSet object must be orphan-deleted and
	// re-applied, because its templates differ from the declared ones.
	Recreate bool
}

// TemplateGrowth is one volumeClaimTemplate's storage request, live and
// declared.
type TemplateGrowth struct {
	Template string
	From, To resource.Quantity
}

// ClaimResize is one PersistentVolumeClaim to grow to To.
type ClaimResize struct {
	Name         string
	Template     string
	StorageClass string
	From, To     resource.Quantity
}

// String is a one-line account of the plan, for an applier's log.
func (p Plan) String() string {
	if !p.Recreate {
		return fmt.Sprintf("StatefulSet %s/%s: volume sizes already converged", p.Namespace, p.Name)
	}
	var grown []string
	for _, g := range p.Grown {
		grown = append(grown, fmt.Sprintf("%s %s→%s", g.Template, g.From.String(), g.To.String()))
	}
	claims := "no claim needs to grow"
	if len(p.Resize) > 0 {
		var names []string
		for _, r := range p.Resize {
			names = append(names, r.Name)
		}
		claims = "grow " + strings.Join(names, ", ")
	}
	return fmt.Sprintf("StatefulSet %s/%s: volumeClaimTemplates %s; %s; orphan-recreate the StatefulSet (pods keep running)",
		p.Namespace, p.Name, strings.Join(grown, ", "), claims)
}

// Decide computes the plan for converging live to desired, given the live
// claims in the StatefulSet's namespace and the cluster's StorageClasses. It
// is pure.
//
// It judges the volumeClaimTemplates only. Whether another immutable field
// also changed is the API server's call, which [Converge] asks it with a dry
// run; a caller using Decide alone must make that check itself before acting
// on a Recreate plan.
//
// A nil live StatefulSet yields the zero Plan: creating one has no immutable
// field to collide with.
func Decide(desired, live *appsv1.StatefulSet, claims []corev1.PersistentVolumeClaim, classes []storagev1.StorageClass) (Plan, error) {
	if desired == nil {
		return Plan{}, fmt.Errorf("stsresize: no desired StatefulSet")
	}
	plan := Plan{Namespace: desired.Namespace, Name: desired.Name}
	if live == nil {
		return plan, nil
	}
	if plan.Namespace == "" {
		plan.Namespace = live.Namespace
	}

	want := desired.Spec.VolumeClaimTemplates
	have := live.Spec.VolumeClaimTemplates
	if names(want) != names(have) {
		return Plan{}, fmt.Errorf("%w: StatefulSet %s/%s declares templates [%s], the live object has [%s]",
			ErrTemplateChanged, plan.Namespace, plan.Name, names(want), names(have))
	}

	byClass := map[string]storagev1.StorageClass{}
	defaultClass := ""
	for _, sc := range classes {
		byClass[sc.Name] = sc
		if isDefaultClass(sc) {
			defaultClass = sc.Name
		}
	}

	for i := range want {
		w, h := want[i], have[i]
		if !sameExceptStorage(w, h) {
			return Plan{}, fmt.Errorf("%w: StatefulSet %s/%s template %q", ErrTemplateChanged, plan.Namespace, plan.Name, w.Name)
		}
		to, from := storageRequest(w), storageRequest(h)
		switch to.Cmp(from) {
		case 0:
			continue
		case -1:
			return Plan{}, fmt.Errorf("%w: StatefulSet %s/%s template %q declares %s, the live template has %s",
				ErrShrink, plan.Namespace, plan.Name, w.Name, to.String(), from.String())
		}
		plan.Grown = append(plan.Grown, TemplateGrowth{Template: w.Name, From: from, To: to})

		for _, pvc := range claimsOf(claims, w.Name, plan.Name) {
			size := storageRequest(pvc)
			if size.Cmp(to) >= 0 {
				continue
			}
			class := claimClass(pvc, defaultClass)
			sc, known := byClass[class]
			if class == "" || !known || sc.AllowVolumeExpansion == nil || !*sc.AllowVolumeExpansion {
				return Plan{}, fmt.Errorf("%w: claim %s/%s (storage class %q) must grow from %s to %s",
					ErrNotExpandable, plan.Namespace, pvc.Name, class, size.String(), to.String())
			}
			plan.Resize = append(plan.Resize, ClaimResize{
				Name: pvc.Name, Template: w.Name, StorageClass: class, From: size, To: to,
			})
		}
	}
	sort.Slice(plan.Resize, func(i, j int) bool { return plan.Resize[i].Name < plan.Resize[j].Name })
	plan.Recreate = len(plan.Grown) > 0
	return plan, nil
}

// Cluster is the cluster access [Converge] needs. Declared here, by the one
// consumer, so each applier supplies its own client (forge's direct apply
// shells out to kubectl; a reconciler's converger holds a controller-runtime
// client).
type Cluster interface {
	// StatefulSet returns the live object, or nil and no error when absent.
	StatefulSet(ctx context.Context, namespace, name string) (*appsv1.StatefulSet, error)
	// PersistentVolumeClaims lists the claims in namespace.
	PersistentVolumeClaims(ctx context.Context, namespace string) ([]corev1.PersistentVolumeClaim, error)
	// StorageClasses lists the cluster's StorageClasses.
	StorageClasses(ctx context.Context) ([]storagev1.StorageClass, error)
	// DryRunApply server-side applies sts with dryRun=All, under the same
	// field manager the real apply uses, and returns the server's verdict.
	DryRunApply(ctx context.Context, sts *appsv1.StatefulSet) error
	// ResizeClaim sets a claim's spec.resources.requests.storage.
	ResizeClaim(ctx context.Context, namespace, name string, size resource.Quantity) error
	// OrphanStatefulSet deletes the StatefulSet with propagationPolicy=Orphan
	// and returns once it is gone. Absent is success.
	OrphanStatefulSet(ctx context.Context, namespace, name string) error
}

// Converge makes the live cluster ready to accept desired: it plans with
// [Decide], proves with a server-side dry run that the volume templates are
// the only immutable difference, grows the claims, and orphan-deletes the
// StatefulSet. The caller applies desired afterwards.
//
// desired must carry its namespace. The returned Plan says what was done;
// when its Recreate is false nothing was written. On a refusal nothing was
// written either. An error after the claims were grown leaves them grown,
// which is correct on its own, and a re-run finishes the rest.
func Converge(ctx context.Context, c Cluster, desired *appsv1.StatefulSet) (Plan, error) {
	if desired == nil || desired.Name == "" || desired.Namespace == "" {
		return Plan{}, fmt.Errorf("stsresize: the desired StatefulSet needs a name and a namespace")
	}
	live, err := c.StatefulSet(ctx, desired.Namespace, desired.Name)
	if err != nil {
		return Plan{}, fmt.Errorf("stsresize: read StatefulSet %s/%s: %w", desired.Namespace, desired.Name, err)
	}
	if live == nil {
		return Plan{Namespace: desired.Namespace, Name: desired.Name}, nil
	}
	claims, err := c.PersistentVolumeClaims(ctx, desired.Namespace)
	if err != nil {
		return Plan{}, fmt.Errorf("stsresize: list claims in %s: %w", desired.Namespace, err)
	}
	classes, err := c.StorageClasses(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("stsresize: list storage classes: %w", err)
	}
	plan, err := Decide(desired, live, claims, classes)
	if err != nil || !plan.Recreate {
		return plan, err
	}

	// The server, not a hand-kept list, decides whether anything besides the
	// templates is immutable and changed: the declared object with the LIVE
	// templates swapped in must be an acceptable update. If it is not, the
	// recreated object would not adopt the running pods.
	candidate := desired.DeepCopy()
	candidate.Spec.VolumeClaimTemplates = live.DeepCopy().Spec.VolumeClaimTemplates
	if err := c.DryRunApply(ctx, candidate); err != nil {
		return Plan{}, fmt.Errorf("%w: StatefulSet %s/%s: with the live volumeClaimTemplates the API server still refuses it: %v",
			ErrOtherImmutable, desired.Namespace, desired.Name, err)
	}

	// Claims first: a failure here leaves the StatefulSet untouched, and a
	// grown claim is correct whatever happens next.
	for _, r := range plan.Resize {
		if err := c.ResizeClaim(ctx, plan.Namespace, r.Name, r.To); err != nil {
			return plan, fmt.Errorf("stsresize: grow claim %s/%s to %s: %w", plan.Namespace, r.Name, r.To.String(), err)
		}
	}
	if err := c.OrphanStatefulSet(ctx, plan.Namespace, plan.Name); err != nil {
		return plan, fmt.Errorf("stsresize: orphan-delete StatefulSet %s/%s: %w", plan.Namespace, plan.Name, err)
	}
	return plan, nil
}

// names is the templates' names in order, joined, for comparison and messages.
func names(ts []corev1.PersistentVolumeClaim) string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return strings.Join(out, ", ")
}

// storageRequest is a claim's (or template's) storage request; zero if unset.
func storageRequest(pvc corev1.PersistentVolumeClaim) resource.Quantity {
	if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		return q
	}
	return resource.Quantity{}
}

// sameExceptStorage reports whether two templates are the same claim apart
// from the storage request. It compares what the API server's immutability
// check compares, with the one default the server fills into a stored
// template (volumeMode Filesystem) applied to both sides, so an undefaulted
// render does not read as a change. Status and creationTimestamp are not part
// of what a template declares.
func sameExceptStorage(a, b corev1.PersistentVolumeClaim) bool {
	norm := func(p corev1.PersistentVolumeClaim) (map[string]string, map[string]string, corev1.PersistentVolumeClaimSpec) {
		spec := *p.Spec.DeepCopy()
		if spec.Resources.Requests != nil {
			delete(spec.Resources.Requests, corev1.ResourceStorage)
		}
		if spec.VolumeMode == nil {
			fs := corev1.PersistentVolumeFilesystem
			spec.VolumeMode = &fs
		}
		return p.Labels, p.Annotations, spec
	}
	al, aa, as := norm(a)
	bl, ba, bs := norm(b)
	return a.Name == b.Name &&
		apiequality.Semantic.DeepEqual(al, bl) &&
		apiequality.Semantic.DeepEqual(aa, ba) &&
		apiequality.Semantic.DeepEqual(as, bs)
}

// claimsOf returns the claims the StatefulSet controller created from
// template for sts: `<template>-<sts>-<ordinal>`, every ordinal, including
// those of replicas scaled away (their claims are retained by default).
func claimsOf(claims []corev1.PersistentVolumeClaim, template, sts string) []corev1.PersistentVolumeClaim {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(template+"-"+sts+"-") + `(\d+)$`)
	var out []corev1.PersistentVolumeClaim
	for _, pvc := range claims {
		m := re.FindStringSubmatch(pvc.Name)
		if m == nil {
			continue
		}
		if _, err := strconv.Atoi(m[1]); err != nil {
			continue
		}
		out = append(out, pvc)
	}
	return out
}

// claimClass is the StorageClass a claim was provisioned from. A claim that
// names none (nil) got the cluster default; an explicit "" means no class (a
// statically bound volume), which cannot be expanded.
func claimClass(pvc corev1.PersistentVolumeClaim, defaultClass string) string {
	if pvc.Spec.StorageClassName == nil {
		return defaultClass
	}
	return *pvc.Spec.StorageClassName
}

// isDefaultClass reports the cluster-default StorageClass annotation.
func isDefaultClass(sc storagev1.StorageClass) bool {
	return sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true"
}
