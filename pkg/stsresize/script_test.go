package stsresize

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// fakeKubectl is a POSIX stand-in for kubectl over a directory of JSON files:
// sts/<name>.json are the live StatefulSets, pvcs.json the claim list, and
// noexpand lists the claims whose server-side dry-run patch the "API server"
// refuses (a StorageClass without allowVolumeExpansion). Every call is logged
// to calls.log, one line per call.
const fakeKubectl = `#!/bin/sh
d="$FAKE_KUBECTL_DIR"
[ "$1" = "-n" ] && shift 2
printf '%s\n' "$*" >> "$d/calls.log"
verb=$1; shift
case "$verb $1" in
"get persistentvolumeclaims") cat "$d/pvcs.json"; exit 0 ;;
"get statefulset")
  name=$2
  case " $* " in
    *" -o name "*) [ -f "$d/sts/$name.json" ] && echo "statefulset.apps/$name" ;;
    *) [ -f "$d/sts/$name.json" ] && cat "$d/sts/$name.json" ;;
  esac
  exit 0 ;;
"patch persistentvolumeclaim")
  claim=$2
  case " $* " in
    *" --dry-run=server "*) if grep -qx "$claim" "$d/noexpand" 2>/dev/null; then
        echo "persistentvolumeclaims \"$claim\" is forbidden: only dynamically provisioned pvc can be resized and the storageclass that provisions the pvc must support resize" >&2
        exit 1
      fi ;;
  esac
  exit 0 ;;
"delete statefulset") rm -f "$d/sts/$2.json"; exit 0 ;;
esac
echo "fake kubectl: unexpected call: $verb $*" >&2
exit 64
`

func requireScriptTools(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the Job script against a fake kubectl, one sh and about a dozen jq processes per case; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the Job script is POSIX sh and runs in a Linux container; covered on Linux")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed; the Job's image ships it and CI runners do")
	}
}

type scriptRun struct {
	exit  int
	out   string
	calls []string
}

// runScript runs the Job script as the pod would, against a fake kubectl
// serving live and claims.
func runScript(t *testing.T, declared []*appsv1.StatefulSet, live []*appsv1.StatefulSet, claims []corev1.PersistentVolumeClaim, noExpand ...string) scriptRun {
	t.Helper()
	requireScriptTools(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sts"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range live {
		write(filepath.Join(dir, "sts", s.Name+".json"), s)
	}
	write(filepath.Join(dir, "pvcs.json"), corev1.PersistentVolumeClaimList{Items: claims})
	if err := os.WriteFile(filepath.Join(dir, "noexpand"), []byte(strings.Join(noExpand, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(fakeKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "resize.sh")
	if err := os.WriteFile(script, []byte(Script), 0o644); err != nil {
		t.Fatal(err)
	}
	declaredJSON, err := DeclaredJSON(declared...)
	if err != nil {
		t.Fatal(err)
	}

	// An explicit environment, as the Job's container has: the fake kubectl
	// first, then the directory jq was found in and the system ones.
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script)
	cmd.Env = []string{
		"PATH=" + bin + ":" + filepath.Dir(jq) + ":/usr/bin:/bin",
		"FAKE_KUBECTL_DIR=" + dir,
		"RESIZE_NAMESPACE=observability-data",
		"RESIZE_DECLARED=" + declaredJSON,
	}
	out, err := cmd.CombinedOutput()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run script: %v", err)
		}
		exit = ee.ExitCode()
	}
	log, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	return scriptRun{exit: exit, out: string(out), calls: strings.Split(strings.TrimSpace(string(log)), "\n")}
}

func (r scriptRun) dump() string { return strings.Join(r.calls, "\n") }

func (r scriptRun) wrote() []string {
	var w []string
	for _, c := range r.calls {
		if strings.HasPrefix(c, "patch ") && !strings.Contains(c, "--dry-run") || strings.HasPrefix(c, "delete ") {
			w = append(w, c)
		}
	}
	return w
}

// TestScript_MatchesDecide runs the Job script and [Decide] over one table, so
// the jq restatement cannot drift from the Go original.
func TestScript_MatchesDecide(t *testing.T) {
	requireScriptTools(t)
	live := func(size string) *appsv1.StatefulSet { return sts(storedTemplate("data", size)) }
	declared := func(size string) *appsv1.StatefulSet { return sts(template("data", size)) }
	claims := func(sizes ...string) []corev1.PersistentVolumeClaim {
		var out []corev1.PersistentVolumeClaim
		for i, s := range sizes {
			out = append(out, claim("data-clickhouse-"+string(rune('0'+i)), s, ptr("standard-rwo")))
		}
		return out
	}

	withClass := func(s *appsv1.StatefulSet, c string) *appsv1.StatefulSet {
		s.Spec.VolumeClaimTemplates[0].Spec.StorageClassName = ptr(c)
		return s
	}
	withLabel := func(s *appsv1.StatefulSet) *appsv1.StatefulSet {
		s.Spec.VolumeClaimTemplates[0].Labels = map[string]string{"extra": "1"}
		return s
	}
	twoTemplates := sts(template("data", "100Gi"), template("logs", "10Gi"))

	cases := []struct {
		name     string
		declared *appsv1.StatefulSet
		live     *appsv1.StatefulSet
		claims   []corev1.PersistentVolumeClaim
		// wantRefused: Decide must return an error and the script exit 2.
		wantRefused bool
		// wantWrites: the writes the script makes when it converges.
		wantWrites int
	}{
		{name: "grow 50Gi to 100Gi", declared: declared("100Gi"), live: live("50Gi"), claims: claims("50Gi"), wantWrites: 2},
		{name: "grow, claim already expanded by hand", declared: declared("100Gi"), live: live("50Gi"), claims: claims("100Gi"), wantWrites: 1},
		{name: "grow across ordinals", declared: declared("100Gi"), live: live("50Gi"), claims: claims("50Gi", "50Gi", "100Gi"), wantWrites: 3},
		{name: "grow, claim larger than declared is kept", declared: declared("100Gi"), live: live("50Gi"), claims: claims("200Gi"), wantWrites: 1},
		{name: "unchanged", declared: declared("50Gi"), live: live("50Gi"), claims: claims("50Gi")},
		{name: "unchanged in other units (51200Mi is 50Gi)", declared: declared("51200Mi"), live: live("50Gi"), claims: claims("50Gi")},
		{name: "shrink", declared: declared("20Gi"), live: live("50Gi"), claims: claims("50Gi"), wantRefused: true},
		{name: "changed storage class", declared: withClass(declared("100Gi"), "premium"), live: live("50Gi"), claims: claims("50Gi"), wantRefused: true},
		{name: "changed template label", declared: withLabel(declared("100Gi")), live: live("50Gi"), claims: claims("50Gi"), wantRefused: true},
		{name: "added template", declared: twoTemplates, live: live("50Gi"), claims: claims("50Gi"), wantRefused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan, decideErr := Decide(tc.declared, tc.live, tc.claims, expandable)
			run := runScript(t, []*appsv1.StatefulSet{tc.declared}, []*appsv1.StatefulSet{tc.live}, tc.claims)

			if tc.wantRefused {
				if decideErr == nil {
					t.Fatal("Decide accepted a change the table says must be refused")
				}
				if run.exit != 2 {
					t.Fatalf("script exit = %d, want 2 (refused)\n%s", run.exit, run.out)
				}
				if got := run.wrote(); len(got) != 0 {
					t.Fatalf("a refusal wrote: %v", got)
				}
				if !strings.Contains(run.out, "REFUSED") {
					t.Fatalf("a refusal must say so:\n%s", run.out)
				}
				return
			}
			if decideErr != nil {
				t.Fatalf("Decide refused: %v", decideErr)
			}
			if run.exit != 0 {
				t.Fatalf("script exit = %d, want 0\n%s", run.exit, run.out)
			}
			// The script patches exactly the claims Decide resizes, then
			// orphan-deletes exactly when Decide says Recreate.
			var patched []string
			for _, w := range run.wrote() {
				if strings.HasPrefix(w, "patch ") {
					fields := strings.Fields(w)
					patched = append(patched, fields[2])
				}
			}
			if want := claimNames(plan); strings.Join(patched, ",") != strings.Join(want, ",") {
				t.Fatalf("script patched %v, Decide resizes %v", patched, want)
			}
			deleted := false
			for _, w := range run.wrote() {
				deleted = deleted || strings.HasPrefix(w, "delete statefulset clickhouse --cascade=orphan")
			}
			if deleted != plan.Recreate {
				t.Fatalf("script orphan-deleted = %v, Decide Recreate = %v", deleted, plan.Recreate)
			}
		})
	}
}

func TestScript_AbsentStatefulSetIsANoop(t *testing.T) {
	t.Parallel()
	run := runScript(t, []*appsv1.StatefulSet{sts(template("data", "100Gi"))}, nil, nil)
	if run.exit != 0 || len(run.wrote()) != 0 {
		t.Fatalf("exit %d, wrote %v\n%s", run.exit, run.wrote(), run.out)
	}
}

func TestScript_RunTwiceIsIdempotent(t *testing.T) {
	t.Parallel()
	// The second run sees what the first left: the StatefulSet gone, which is
	// the Job's whole job done, and so nothing to do.
	run := runScript(t, []*appsv1.StatefulSet{sts(template("data", "100Gi"))}, nil, []corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "100Gi", ptr("standard-rwo"))})
	if run.exit != 0 || len(run.wrote()) != 0 {
		t.Fatalf("exit %d, wrote %v\n%s", run.exit, run.wrote(), run.out)
	}
}

func TestScript_RefusesAClaimThatCannotExpandBeforeWritingAnything(t *testing.T) {
	t.Parallel()
	d := sts(template("data", "100Gi"))
	l := sts(storedTemplate("data", "50Gi"))
	claims := []corev1.PersistentVolumeClaim{
		claim("data-clickhouse-0", "50Gi", ptr("standard-rwo")),
		claim("data-clickhouse-1", "50Gi", ptr("standard-rwo")),
	}
	run := runScript(t, []*appsv1.StatefulSet{d}, []*appsv1.StatefulSet{l}, claims, "data-clickhouse-1")
	if run.exit != 2 {
		t.Fatalf("exit = %d, want 2\n%s", run.exit, run.out)
	}
	if got := run.wrote(); len(got) != 0 {
		t.Fatalf("claim 0 must not be grown when claim 1 cannot be: %v", got)
	}
	if !strings.Contains(run.out, "allow volume expansion") {
		t.Fatalf("the refusal must name the cause:\n%s", run.out)
	}
}

func TestScript_RefusalOnOneStatefulSetWritesNothingForAnother(t *testing.T) {
	t.Parallel()
	ok := sts(template("data", "100Gi"))
	bad := sts(template("data", "10Gi"))
	bad.Name = "other"
	okLive := sts(storedTemplate("data", "50Gi"))
	badLive := sts(storedTemplate("data", "50Gi"))
	badLive.Name = "other"
	claims := []corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "50Gi", ptr("standard-rwo")), claim("data-other-0", "50Gi", ptr("standard-rwo"))}
	run := runScript(t, []*appsv1.StatefulSet{ok, bad}, []*appsv1.StatefulSet{okLive, badLive}, claims)
	if run.exit != 2 || len(run.wrote()) != 0 {
		t.Fatalf("exit %d, wrote %v\n%s", run.exit, run.wrote(), run.out)
	}
}

func TestScript_ClaimsOfAnotherStatefulSetAreNotTouched(t *testing.T) {
	t.Parallel()
	// "data-clickhouse-keeper-0" must not be mistaken for an ordinal of "clickhouse".
	d, l := sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi"))
	claims := []corev1.PersistentVolumeClaim{
		claim("data-clickhouse-0", "50Gi", ptr("standard-rwo")),
		claim("data-clickhouse-keeper-0", "50Gi", ptr("standard-rwo")),
		claim("data-clickhouse-x", "50Gi", ptr("standard-rwo")),
	}
	run := runScript(t, []*appsv1.StatefulSet{d}, []*appsv1.StatefulSet{l}, claims)
	if run.exit != 0 {
		t.Fatalf("exit %d\n%s", run.exit, run.out)
	}
	for _, w := range run.wrote() {
		if strings.HasPrefix(w, "patch ") && !strings.Contains(w, "data-clickhouse-0 ") {
			t.Fatalf("patched a claim that is not clickhouse's: %s", w)
		}
	}
}

// Decide judges the templates only; the API server's dry run judges the other
// immutable fields in Converge. The script has no manifest to dry-run, so it
// judges them itself, and these are the cases only it handles.
func TestScript_RefusesAChangeToAnotherImmutableField(t *testing.T) {
	for name, mutate := range map[string]func(*appsv1.StatefulSet){
		"selector":            func(s *appsv1.StatefulSet) { s.Spec.Selector.MatchLabels = map[string]string{"app": "other"} },
		"serviceName":         func(s *appsv1.StatefulSet) { s.Spec.ServiceName = "other" },
		"podManagementPolicy": func(s *appsv1.StatefulSet) { s.Spec.PodManagementPolicy = appsv1.ParallelPodManagement },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d, l := sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi"))
			mutate(d)
			run := runScript(t, []*appsv1.StatefulSet{d}, []*appsv1.StatefulSet{l}, []corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "50Gi", ptr("standard-rwo"))})
			if run.exit != 2 || len(run.wrote()) != 0 || !strings.Contains(run.out, name) {
				t.Fatalf("exit %d, wrote %v\n%s", run.exit, run.wrote(), run.out)
			}
		})
	}
}

func TestScript_ServerDefaultsAreNotChanges(t *testing.T) {
	t.Parallel()
	// The live object carries the defaults the API server filled in (an
	// OrderedReady policy, a Filesystem volume mode); the render states none.
	d, l := sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi"))
	l.Spec.PodManagementPolicy = appsv1.OrderedReadyPodManagement
	run := runScript(t, []*appsv1.StatefulSet{d}, []*appsv1.StatefulSet{l}, []corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "50Gi", ptr("standard-rwo"))})
	if run.exit != 0 || len(run.wrote()) != 2 {
		t.Fatalf("exit %d, wrote %v\ncalls:\n%s\n%s", run.exit, run.wrote(), run.dump(), run.out)
	}
}

// A real API server returns kubectl.kubernetes.io/last-applied-configuration,
// a JSON document inside a JSON string, so the live object is full of \" and \n.
// dash's echo interprets those, which turned the object into invalid JSON and
// failed the Job on the first real cluster; a fake that returned a clean object
// never showed it.
func TestScript_LiveObjectWithEscapedJSONAnnotation(t *testing.T) {
	t.Parallel()
	d, l := sts(template("data", "100Gi")), sts(storedTemplate("data", "50Gi"))
	l.Annotations = map[string]string{
		"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"apps/v1","kind":"StatefulSet","metadata":{"annotations":{"note":"line1\nline2 \"quoted\" back\\slash"}}}` + "\n",
	}
	run := runScript(t, []*appsv1.StatefulSet{d}, []*appsv1.StatefulSet{l}, []corev1.PersistentVolumeClaim{claim("data-clickhouse-0", "50Gi", ptr("standard-rwo"))})
	if run.exit != 0 || len(run.wrote()) != 2 {
		t.Fatalf("exit %d, wrote %v\n%s", run.exit, run.wrote(), run.out)
	}
}
