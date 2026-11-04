package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClusterKubectl is a kubectl that answers the local stage's pre-apply
// calls and records, in order, what each apply wrote: the context, then each
// object's kind and namespace/name. Namespaces in existing answer `get
// namespace`; a token Secret is always populated.
func fakeClusterKubectl(t *testing.T, existing ...string) (log string) {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	bin := t.TempDir()
	log = filepath.Join(bin, "calls.log")
	script := `#!/bin/sh
LOG='` + log + `'
ctx=""; prev=""
for a in "$@"; do [ "$prev" = "--context" ] && ctx="$a"; prev="$a"; done
case " $* " in
  *" get namespace "*)
    for n in ` + strings.Join(existing, " ") + `; do
      case " $* " in *" $n "*) echo "namespace/$n"; exit 0;; esac
    done
    exit 0;;
  *" get secret "*"jsonpath={.data.token}"*) echo dG9rZW4=; exit 0;;
  *" get secret "*"jsonpath={.data.ca"*) echo Y2E=; exit 0;;
  *" get "*) exit 1;;
  *" apply "*)
    awk -v c="$ctx" -v lf="$LOG" '
      function flush() { if (k != "") { print "APPLY " c " " k " " ns "/" n >> lf; print tolower(k) "/" n " serverside-applied" } k=""; n=""; ns="" }
      /^kind: /{k=$2}
      /^ +name: /{if(n=="")n=$2}
      /^ +namespace: /{if(ns=="")ns=$2}
      /^---/{flush()}
      END{flush()}'
    exit 0;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// THE ORDER OF THE WRITES BEFORE THE APPLY, on control-plane prod's shape
// (forge bug ac69bea3): v1.7.89-rc.2 adds Namespace claimable-dns on the daemon
// cluster and grants the prerequisite observer a Role in it, and the mint ran
// before anything had created it. Driving the local stage's pre-apply writes
// against a recording kubectl, the Namespace is created first, exactly once,
// on the daemon cluster, and only then is a Role written into it; an existing
// rendered namespace (kata-prepull) is not re-applied.
func TestApplyLocalPrewritesCreatesTheNamespaceBeforeTheRoleInIt(t *testing.T) {
	log := fakeClusterKubectl(t, "kata-prepull", "kube-system", "control-plane-prod")
	daemon, hub := "gke_daemon", "gke_hub"
	e := &KCLEntities{
		ManifestNamespaces: []RenderedNamespaceEntity{
			{Cluster: daemon, Name: "claimable-dns", Labels: map[string]string{"forge.dev/cluster": daemon}},
			{Cluster: daemon, Name: "kata-prepull", Labels: map[string]string{"forge.dev/cluster": daemon}},
			{Cluster: hub, Name: "control-plane-prod", Labels: map[string]string{"forge.dev/cluster": hub}},
		},
		KubeconfigSecrets: []KubeconfigSecretEntity{{
			Name:          "prerequisite-observer-daemon",
			InCluster:     hub,
			TargetCluster: "prod-daemon",
			TargetContext: daemon,
			ContextName:   "daemon",
			Namespace:     "control-plane-prod",
			Reachability:  "in-cluster",
			ServiceAccount: &KubeconfigServiceAccountEntity{
				Name:       "forge-prerequisite-observer",
				Namespace:  "kube-system",
				Namespaces: []string{"claimable-dns", "kata-prepull", "kube-system"},
				Rules:      []KubeconfigPolicyRuleEntity{{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get"}}},
			},
		}},
	}
	captureStdout(t, func() {
		if err := applyLocalPrewrites(context.Background(), localPrewriteInput{
			entities: e, fullEntities: e, namespace: "control-plane-prod", deployContext: hub, envName: "prod",
		}); err != nil {
			t.Fatalf("pre-apply writes: %v", err)
		}
	})
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	nsAt, roleAt, nsCount := -1, -1, 0
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "APPLY "+daemon+" Namespace /claimable-dns"):
			nsCount++
			if nsAt < 0 {
				nsAt = i
			}
		case strings.HasPrefix(l, "APPLY "+daemon+" Role claimable-dns/") && roleAt < 0:
			roleAt = i
		case strings.Contains(l, " Namespace /kata-prepull"):
			t.Errorf("an existing rendered namespace was re-applied: %s", l)
		}
	}
	if nsAt < 0 || roleAt < 0 || nsCount != 1 {
		t.Fatalf("want one Namespace claimable-dns and a Role in it on %s; calls:\n%s", daemon, data)
	}
	if nsAt > roleAt {
		t.Errorf("the Role in claimable-dns was written before the Namespace was created; calls:\n%s", data)
	}
}
