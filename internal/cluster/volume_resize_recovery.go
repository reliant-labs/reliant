package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/stsresize"
)

// The StatefulSet volume-resize recovery: converging a declared volume size
// that grew.
//
// A StatefulSet's volumeClaimTemplates are immutable, so raising the size an
// env declares for one (a `clickhouse_disk = "100Gi"` that was "50Gi") makes
// the apply fail on every deploy, and StatefulSet is deliberately not a kind
// the immutable recovery may delete (pkg/release recreatableKinds): a
// background or foreground delete takes the pods with it.
//
// This recovery runs the production procedure instead, through
// pkg/stsresize: grow each existing claim (grow-only, and only when its
// StorageClass allows expansion), orphan-delete the StatefulSet object so its
// pods and claims keep running, and re-apply, which recreates the object with
// the new templates and adopts the pods. Everything other than a larger
// storage request — a shrink, a changed class, a changed selector — is refused
// with the reason, and nothing is written. See pkg/stsresize for the rules.
//
// It is keyed off the API server's own rejection, like the strategy and
// immutable recoveries beside it, so the happy path costs nothing: nothing
// runs unless an apply has already failed on a StatefulSet's spec.

// statefulSetSpecMarkers are the API server's two spellings of "this
// StatefulSet update touches an immutable spec field": the single Forbidden
// on `spec` that API servers before the per-field validation return, and the
// per-field `field is immutable` on volumeClaimTemplates that newer ones do.
// Either one only names a candidate; pkg/stsresize decides whether the change
// is a volume growth it may converge.
var statefulSetSpecMarkers = []string{
	"updates to statefulset spec for fields other than",
	"spec.volumeClaimTemplates",
}

// statefulSetSpecConflicts returns every StatefulSet in one apply whose error
// body carries a spec-immutability marker, decoded from the manifest that
// declares it, with its namespace defaulted the way the apply's was. Like
// strategyConflicts it scans every `is invalid:` segment, because forge
// applies a whole batch in one call and one apply can reject several objects.
func statefulSetSpecConflicts(stderr, manifests, namespace string) []*appsv1.StatefulSet {
	const inv = " is invalid:"
	var out []*appsv1.StatefulSet
	seen := map[string]bool{}
	rest := stderr
	for {
		idx := strings.Index(rest, inv)
		if idx < 0 {
			break
		}
		head := rest[:idx+len(inv)]
		rest = rest[idx+len(inv):]
		body := rest
		if next := strings.Index(rest, inv); next >= 0 {
			body = rest[:next]
		}
		if !containsAny(body, statefulSetSpecMarkers) {
			continue
		}
		kind, name, ok := parseInvalidResource(head)
		if !ok || kind != "StatefulSet" {
			continue
		}
		desired, ok := statefulSetForResource(manifests, name)
		if !ok {
			continue
		}
		if desired.Namespace == "" {
			desired.Namespace = namespace
		}
		key := desired.Namespace + "/" + desired.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, desired)
	}
	return out
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// statefulSetForResource decodes the manifest document declaring StatefulSet
// name. ok=false when none does or it does not decode.
func statefulSetForResource(manifests, name string) (*appsv1.StatefulSet, bool) {
	for _, doc := range splitDocs(manifests) {
		var sts appsv1.StatefulSet
		if err := sigsyaml.Unmarshal([]byte(doc), &sts); err != nil {
			continue
		}
		if sts.Kind == "StatefulSet" && sts.Name == name {
			return &sts, true
		}
	}
	return nil, false
}

// applyWithVolumeResizeRecovery runs apply and, when it failed on a
// StatefulSet's immutable spec, converges each such StatefulSet's volumes and
// re-applies once. The cluster-touching step is a closure so the sequencing is
// unit-testable; applyStreamInNamespace wires stsresize.Converge over kubectl.
//
// Outcomes, in order of precedence:
//   - the apply succeeded, or failed for another reason: returned unchanged;
//   - a converge refused or failed: that error, which names the StatefulSet
//     and the reason (the original rejection is already on the terminal);
//   - no StatefulSet needed recreating (its templates already match): the
//     original failure, unchanged — the rejection was about something else;
//   - otherwise the re-apply's result, whose stdout is the one the
//     apply-completeness check must see.
func applyWithVolumeResizeRecovery(
	manifests, namespace string,
	apply func() (stdout, stderr string, err error),
	converge func(*appsv1.StatefulSet) (stsresize.Plan, error),
) (stdout, stderr string, err error) {
	stdout, stderr, err = apply()
	if err == nil {
		return stdout, stderr, nil
	}
	targets := statefulSetSpecConflicts(stderr, manifests, namespace)
	if len(targets) == 0 {
		return stdout, stderr, err
	}
	recreated := false
	for _, desired := range targets {
		plan, cErr := converge(desired)
		if cErr != nil {
			return stdout, stderr, fmt.Errorf("converging the volumes of StatefulSet %s/%s: %w", desired.Namespace, desired.Name, cErr)
		}
		fmt.Printf("[deploy]   %s\n", plan)
		recreated = recreated || plan.Recreate
	}
	if !recreated {
		return stdout, stderr, err
	}
	return apply()
}

// kubectlStatefulSets is stsresize.Cluster over kubectl, scoped to one
// context — the same context the failed apply used, never a fallback.
type kubectlStatefulSets struct {
	kctx string
}

var _ stsresize.Cluster = kubectlStatefulSets{}

// getJSON runs `kubectl get <args> -o json` and decodes stdout into out.
// Empty stdout (an --ignore-not-found miss) leaves out untouched and reports
// found=false.
func (k kubectlStatefulSets) getJSON(ctx context.Context, out any, args ...string) (found bool, err error) {
	cmd := kubectlCmd(ctx, k.kctx, append(append([]string{"get"}, args...), "-o", "json")...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	raw, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("kubectl get %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return false, fmt.Errorf("decode kubectl get %s: %w", strings.Join(args, " "), err)
	}
	return true, nil
}

func (k kubectlStatefulSets) StatefulSet(ctx context.Context, namespace, name string) (*appsv1.StatefulSet, error) {
	var sts appsv1.StatefulSet
	found, err := k.getJSON(ctx, &sts, "statefulset", name, "-n", namespace, "--ignore-not-found=true")
	if err != nil || !found {
		return nil, err
	}
	return &sts, nil
}

func (k kubectlStatefulSets) PersistentVolumeClaims(ctx context.Context, namespace string) ([]corev1.PersistentVolumeClaim, error) {
	var list corev1.PersistentVolumeClaimList
	if _, err := k.getJSON(ctx, &list, "persistentvolumeclaims", "-n", namespace); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (k kubectlStatefulSets) StorageClasses(ctx context.Context) ([]storagev1.StorageClass, error) {
	var list storagev1.StorageClassList
	if _, err := k.getJSON(ctx, &list, "storageclasses"); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// DryRunApply is the real apply's argv plus --dry-run=server, so the server
// judges the candidate under the same field manager and conflict policy.
func (k kubectlStatefulSets) DryRunApply(ctx context.Context, sts *appsv1.StatefulSet) error {
	body, err := json.Marshal(sts)
	if err != nil {
		return fmt.Errorf("marshal StatefulSet %s/%s: %w", sts.Namespace, sts.Name, err)
	}
	cmd := kubectlCmd(ctx, k.kctx, append(kubectlApplyArgs(sts.Namespace), "--dry-run=server")...)
	cmd.Stdin = bytes.NewReader(body)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// ResizeClaim merge-patches the one field: a claim's storage request is
// mutable (grow-only) where its template's is not.
func (k kubectlStatefulSets) ResizeClaim(ctx context.Context, namespace, name string, size resource.Quantity) error {
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"resources": map[string]any{
		"requests": map[string]string{string(corev1.ResourceStorage): size.String()},
	}}})
	if err != nil {
		return err
	}
	cmd := kubectlCmd(ctx, k.kctx, "patch", "persistentvolumeclaim", name, "-n", namespace, "--type=merge", "-p", string(patch))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// OrphanStatefulSet deletes with --cascade=orphan — the pods, claims and
// ControllerRevisions lose their owner reference and keep running — then
// polls until the object is gone, because the re-apply must CREATE it: an
// apply that lands on the terminating object fails.
func (k kubectlStatefulSets) OrphanStatefulSet(ctx context.Context, namespace, name string) error {
	cmd := kubectlCmd(ctx, k.kctx, "delete", "statefulset", name, "-n", namespace,
		"--cascade=orphan", "--ignore-not-found=true", "--wait=true")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	return kubectlWaitResourceGone(ctx, k.kctx, immutableTarget{Kind: "StatefulSet", Name: name, Namespace: namespace})
}
