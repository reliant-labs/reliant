package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/reliant-labs/forge/pkg/stsresize"
)

// clickhouseManifest is the StatefulSet as a render declares it after the
// env's disk size moved from 50Gi to 100Gi.
const clickhouseManifest = `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: clickhouse
  namespace: observability-data
spec:
  replicas: 1
  serviceName: clickhouse
  selector:
    matchLabels:
      app: clickhouse
  template:
    metadata:
      labels:
        app: clickhouse
    spec:
      containers:
        - name: clickhouse
          image: clickhouse/clickhouse-server:26.8
  volumeClaimTemplates:
    - metadata:
        name: data
      spec:
        accessModes: [ReadWriteOnce]
        storageClassName: standard-rwo
        resources:
          requests:
            storage: 100Gi
`

// The rejection an API server without per-field StatefulSet validation
// returns (GKE 1.33), interleaved with the batch's other results the way
// kubectl prints it.
const stsForbiddenStderr = `service/clickhouse serverside-applied
The StatefulSet "clickhouse" is invalid: spec: Forbidden: updates to statefulset spec for fields other than 'replicas', 'ordinals', 'template', 'updateStrategy', 'persistentVolumeClaimRetentionPolicy' and 'minReadySeconds' are forbidden
`

// The per-field spelling newer API servers return.
const stsImmutableStderr = `Error from server (Invalid): error when applying patch: StatefulSet.apps "clickhouse" is invalid: spec.volumeClaimTemplates: Invalid value: []core.PersistentVolumeClaim{...}: field is immutable
`

func TestStatefulSetSpecConflicts_MatchesBothSpellings(t *testing.T) {
	for name, stderr := range map[string]string{"forbidden": stsForbiddenStderr, "immutable": stsImmutableStderr} {
		t.Run(name, func(t *testing.T) {
			got := statefulSetSpecConflicts(stderr, clickhouseManifest, "control-plane-prod")
			if len(got) != 1 || got[0].Name != "clickhouse" || got[0].Namespace != "observability-data" {
				t.Fatalf("targets = %+v, want StatefulSet observability-data/clickhouse", got)
			}
			q := got[0].Spec.VolumeClaimTemplates[0].Spec.Resources.Requests["storage"]
			if q.String() != "100Gi" {
				t.Errorf("decoded template size = %s, want the declared 100Gi", q.String())
			}
		})
	}
}

func TestStatefulSetSpecConflicts_DefaultsTheNamespace(t *testing.T) {
	manifest := strings.Replace(clickhouseManifest, "  namespace: observability-data\n", "", 1)
	got := statefulSetSpecConflicts(stsForbiddenStderr, manifest, "obs")
	if len(got) != 1 || got[0].Namespace != "obs" {
		t.Fatalf("targets = %+v, want the apply's namespace", got)
	}
}

func TestStatefulSetSpecConflicts_IgnoresOtherFailures(t *testing.T) {
	for name, stderr := range map[string]string{
		"deployment": `The Deployment "clickhouse" is invalid: spec.selector: Invalid value: field is immutable` + "\n",
		"job":        `The Job "migrate" is invalid: spec.template: Invalid value: field is immutable` + "\n",
		"undeclared": strings.ReplaceAll(stsForbiddenStderr, `"clickhouse"`, `"elsewhere"`),
		"unrelated":  "error: unable to recognize \"STDIN\": no matches for kind\n",
	} {
		if got := statefulSetSpecConflicts(stderr, clickhouseManifest, ""); len(got) != 0 {
			t.Errorf("%s: targets = %+v, want none", name, got)
		}
	}
}

func TestApplyWithVolumeResizeRecovery_ConvergesThenReapplies(t *testing.T) {
	applies := 0
	apply := func() (string, string, error) {
		applies++
		if applies == 1 {
			return "service/clickhouse serverside-applied\n", stsForbiddenStderr, errors.New("exit status 1")
		}
		return "statefulset.apps/clickhouse serverside-applied\n", "", nil
	}
	var converged []string
	converge := func(d *appsv1.StatefulSet) (stsresize.Plan, error) {
		converged = append(converged, d.Namespace+"/"+d.Name)
		return stsresize.Plan{Namespace: d.Namespace, Name: d.Name, Recreate: true}, nil
	}
	stdout, _, err := applyWithVolumeResizeRecovery(clickhouseManifest, "", apply, converge)
	if err != nil {
		t.Fatal(err)
	}
	if applies != 2 || len(converged) != 1 || converged[0] != "observability-data/clickhouse" {
		t.Fatalf("applies = %d, converged = %v", applies, converged)
	}
	if !strings.Contains(stdout, "statefulset.apps/clickhouse") {
		t.Errorf("stdout = %q, want the winning re-apply's", stdout)
	}
}

func TestApplyWithVolumeResizeRecovery_RefusalSurfacesTheReason(t *testing.T) {
	apply := func() (string, string, error) { return "", stsForbiddenStderr, errors.New("exit status 1") }
	converge := func(*appsv1.StatefulSet) (stsresize.Plan, error) {
		return stsresize.Plan{}, stsresize.ErrShrink
	}
	_, _, err := applyWithVolumeResizeRecovery(clickhouseManifest, "", apply, converge)
	if !errors.Is(err, stsresize.ErrShrink) || !strings.Contains(err.Error(), "observability-data/clickhouse") {
		t.Fatalf("err = %v, want the shrink refusal naming the StatefulSet", err)
	}
}

func TestApplyWithVolumeResizeRecovery_NothingToRecreateSurfacesTheOriginal(t *testing.T) {
	applies := 0
	orig := errors.New("exit status 1")
	apply := func() (string, string, error) { applies++; return "", stsForbiddenStderr, orig }
	converge := func(d *appsv1.StatefulSet) (stsresize.Plan, error) {
		return stsresize.Plan{Namespace: d.Namespace, Name: d.Name}, nil
	}
	if _, _, err := applyWithVolumeResizeRecovery(clickhouseManifest, "", apply, converge); err != orig || applies != 1 {
		t.Fatalf("err = %v, applies = %d; want the original failure and no re-apply", err, applies)
	}
}

func TestApplyWithVolumeResizeRecovery_UnrelatedFailureIsUntouched(t *testing.T) {
	orig := errors.New("exit status 1")
	apply := func() (string, string, error) { return "", "error: connection refused\n", orig }
	converge := func(*appsv1.StatefulSet) (stsresize.Plan, error) {
		t.Fatal("converge must not run for an unrelated failure")
		return stsresize.Plan{}, nil
	}
	if _, _, err := applyWithVolumeResizeRecovery(clickhouseManifest, "", apply, converge); err != orig {
		t.Fatalf("err = %v", err)
	}
}

// fakeResizeKubectl is a kubectl holding one live StatefulSet with a 50Gi
// template, its 50Gi claim and an expandable StorageClass. Its first real
// apply is rejected the way the API server rejects a template change; after
// the StatefulSet is deleted the next apply succeeds. Every argv is logged.
func fakeResizeKubectl(t *testing.T) func() []string {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	dir := t.TempDir()
	files := map[string]string{
		"sts.json": `{"apiVersion":"apps/v1","kind":"StatefulSet","metadata":{"name":"clickhouse","namespace":"observability-data"},
"spec":{"replicas":1,"serviceName":"clickhouse","selector":{"matchLabels":{"app":"clickhouse"}},
"volumeClaimTemplates":[{"metadata":{"name":"data","creationTimestamp":null},"spec":{"accessModes":["ReadWriteOnce"],
"storageClassName":"standard-rwo","volumeMode":"Filesystem","resources":{"requests":{"storage":"50Gi"}}},"status":{"phase":"Pending"}}]}}`,
		"pvcs.json": `{"apiVersion":"v1","kind":"List","items":[{"metadata":{"name":"data-clickhouse-0","namespace":"observability-data"},
"spec":{"storageClassName":"standard-rwo","resources":{"requests":{"storage":"50Gi"}}}}]}`,
		"scs.json": `{"apiVersion":"v1","kind":"List","items":[{"metadata":{"name":"standard-rwo"},"provisioner":"pd.csi.storage.gke.io","allowVolumeExpansion":true}]}`,
		"stderr":   stsForbiddenStderr,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"d=" + dir + "\n" +
		"printf '%s\\n' \"$*\" >> \"$d/calls.log\"\n" +
		"case \" $* \" in\n" +
		"  *' apply '*' --dry-run=server '*) cat > /dev/null; exit 0 ;;\n" +
		"  *' apply '*) cat > /dev/null\n" +
		"    if [ ! -f \"$d/deleted\" ]; then cat \"$d/stderr\" >&2; exit 1; fi\n" +
		"    echo 'statefulset/clickhouse serverside-applied'; exit 0 ;;\n" +
		"  *' get statefulset '*) [ -f \"$d/deleted\" ] && exit 0; case \" $* \" in *' -o json '*) cat \"$d/sts.json\" ;; *) echo statefulset.apps/clickhouse ;; esac ;;\n" +
		"  *' get persistentvolumeclaims '*) cat \"$d/pvcs.json\" ;;\n" +
		"  *' get storageclasses '*) cat \"$d/scs.json\" ;;\n" +
		"  *' patch persistentvolumeclaim '*) echo 'persistentvolumeclaim/data-clickhouse-0 patched' ;;\n" +
		"  *' delete statefulset '*) touch \"$d/deleted\"; echo 'statefulset.apps \"clickhouse\" deleted' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		raw, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
}

// The whole direct-apply path, through the real kubectl argv: the rejected
// apply leads to the claim growing BEFORE the StatefulSet is deleted, the
// delete orphans (never cascades to the pods), and the re-apply lands.
func TestApplyStream_ConvergesAGrownStatefulSetVolume(t *testing.T) {
	calls := fakeResizeKubectl(t)
	if err := applyStreamInNamespace(context.Background(), "prod-ctx", "", clickhouseManifest); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var order []string
	for _, c := range calls() {
		if !strings.HasPrefix(c, "--context prod-ctx ") {
			t.Errorf("kubectl call without the apply's context: %q", c)
		}
		switch {
		case strings.Contains(c, " patch persistentvolumeclaim data-clickhouse-0 -n observability-data "):
			if !strings.Contains(c, `{"spec":{"resources":{"requests":{"storage":"100Gi"}}}}`) {
				t.Errorf("claim patch = %q, want storage 100Gi", c)
			}
			order = append(order, "patch")
		case strings.Contains(c, " delete statefulset clickhouse "):
			if !strings.Contains(c, "--cascade=orphan") {
				t.Fatalf("the StatefulSet delete must orphan its pods: %q", c)
			}
			order = append(order, "orphan")
		case strings.Contains(c, " apply ") && strings.Contains(c, "--dry-run=server"):
			order = append(order, "dry-run")
		case strings.Contains(c, " apply "):
			order = append(order, "apply")
		}
	}
	want := []string{"apply", "dry-run", "patch", "orphan", "apply"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v\ncalls:\n%s", order, want, strings.Join(calls(), "\n"))
	}
}
