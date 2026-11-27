package cluster

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// crdOwnedStream is a render that still declares ONE CRD (widgets.acme.dev).
const crdOwnedStream = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.acme.dev
spec:
  group: acme.dev
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: acme-dev
`

// TestStampCRDOwnership: every CRD in the applied stream is stamped with the
// owner label (and managed-by, unless the author set it). That stamp is what
// makes a later prune able to tell a CRD this env rendered from one it did not.
func TestStampCRDOwnership(t *testing.T) {
	out := StampCRDOwnership(crdOwnedStream, "acme.dev-env")
	docs := splitDocs(out)
	crd, ok := parseDoc(docs[0])
	if !ok || crd.Kind != "CustomResourceDefinition" {
		t.Fatalf("first doc = %+v", crd)
	}
	if got := crd.Metadata.Labels[CRDOwnerLabel]; got != "acme.dev-env" {
		t.Errorf("owner label = %q, want acme.dev-env", got)
	}
	if got := crd.Metadata.Labels[AppManagedByLabel]; got != "forge" {
		t.Errorf("managed-by = %q, want forge", got)
	}
	dep, _ := parseDoc(docs[1])
	if _, stamped := dep.Metadata.Labels[CRDOwnerLabel]; stamped {
		t.Error("a Deployment was stamped with the CRD owner label; only CRDs are")
	}
}

// fakeCRDKubectl installs a kubectl that answers the two reads the prune
// makes and records every delete:
//
//   - `get crd -l <selector> -o jsonpath=...` prints the given owned CRDs;
//   - `get <plural>.<group> -A -o name` prints instances for CRDs in inUse.
func fakeCRDKubectl(t *testing.T, owned []string, inUse map[string]bool) func() []string {
	t.Helper()
	requirePOSIXFake(t, "kubectl")
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	var inUseCase strings.Builder
	for crd := range inUse {
		inUseCase.WriteString("  *' get " + crd + " '*) echo " + crd + "/one ;;\n")
	}
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + log + "\n" +
		"case \" $* \" in\n" +
		"  *' get crd '*) printf '" + strings.Join(owned, `\n`) + `\n` + "' ;;\n" +
		inUseCase.String() +
		"  *) ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

func deletes(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.Contains(c, " delete ") || strings.HasPrefix(c, "delete ") {
			out = append(out, c)
		}
	}
	return out
}

// TestPruneCRDs_DeletesOnlyOwnedUnrenderedUnused is the defect: `forge env
// up` stopped rendering a CRD and never removed it. The prune deletes a CRD
// only when ALL of these hold:
//
//   - it carries THIS env's owner label (selected server-side — a CRD forge
//     did not stamp is never even listed);
//   - it is absent from the stream just applied;
//   - no custom resource of it exists anywhere (deleting a CRD deletes every
//     instance cluster-wide; another env on the same cluster may still use it).
func TestPruneCRDs_DeletesOnlyOwnedUnrenderedUnused(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	calls := fakeCRDKubectl(t,
		[]string{"widgets.acme.dev", "gadgets.acme.dev", "sprockets.acme.dev"},
		map[string]bool{"sprockets.acme.dev": true},
	)
	pruned, kept, err := PruneCRDs(context.Background(), "k3d-acme", crdOwnedStream, "acme.dev-env")
	if err != nil {
		t.Fatalf("PruneCRDs: %v", err)
	}
	all := calls()
	if len(pruned) != 1 || pruned[0] != "gadgets.acme.dev" {
		t.Errorf("pruned = %v, want [gadgets.acme.dev] (widgets is still rendered, sprockets still has instances)", pruned)
	}
	if len(kept) != 1 || kept[0] != "sprockets.acme.dev" {
		t.Errorf("kept-in-use = %v, want [sprockets.acme.dev]", kept)
	}
	d := deletes(all)
	if len(d) != 1 || !strings.Contains(d[0], "delete crd gadgets.acme.dev") || !strings.Contains(d[0], "--context k3d-acme") {
		t.Errorf("delete calls = %v, want exactly one context-scoped delete of gadgets.acme.dev", d)
	}
	// The list MUST be label-selected on this owner: that selector is the
	// "never touch a CRD forge did not create" guarantee.
	var listed bool
	for _, c := range all {
		if strings.Contains(c, "get crd") && strings.Contains(c, CRDOwnerLabel+"=acme.dev-env") && strings.Contains(c, AppManagedByLabel+"=forge") {
			listed = true
		}
	}
	if !listed {
		t.Errorf("no label-selected CRD list among calls: %v", all)
	}
}

// TestPruneCRDs_RefusesWithoutOwnerOrContext: an unscoped prune is not a
// prune, it is a guess. No owner or no context means nothing is deleted.
func TestPruneCRDs_RefusesWithoutOwnerOrContext(t *testing.T) {
	calls := fakeCRDKubectl(t, []string{"gadgets.acme.dev"}, nil)
	if _, _, err := PruneCRDs(context.Background(), "", crdOwnedStream, "acme.dev-env"); err == nil {
		t.Error("no context: want an error")
	}
	if _, _, err := PruneCRDs(context.Background(), "k3d-acme", crdOwnedStream, ""); err == nil {
		t.Error("no owner: want an error")
	}
	if d := deletes(calls()); len(d) != 0 {
		t.Errorf("deleted with no scope: %v", d)
	}
}

// TestCRDOwner: the owner is (project, env) — two envs sharing one cluster
// own disjoint CRD sets, and neither can prune the other's.
func TestCRDOwner(t *testing.T) {
	if got := CRDOwner("acme", "dev"); got != "acme.dev" {
		t.Errorf("CRDOwner = %q", got)
	}
	if got := CRDOwner("", "dev"); got != "" {
		t.Errorf("CRDOwner without project = %q, want empty (no prune)", got)
	}
}

// TestApply_PrunesCRDsOnlyOnAFullApply wires the prune into the apply path:
// the applied CRD carries the owner stamp, a full apply with PruneCRDs lists
// this owner's CRDs, and a --target apply (a SUBSET of the env) never does —
// a CRD missing from a subset proves nothing.
func TestApply_PrunesCRDsOnlyOnAFullApply(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake kubectl through many shell subprocesses; runs in task test")
	}
	stream := crdOwnedStream
	opts := gateOpts(RolloutSkip)
	opts.Project, opts.Env, opts.PruneCRDs = "acme", "dev", true

	calls := fakeGateKubectl(t, jobCompletes)
	if err := applyRendered(context.Background(), opts, stream); err != nil {
		t.Fatalf("applyRendered: %v", err)
	}
	var listed, stamped bool
	for _, c := range calls() {
		if strings.Contains(c.Args, "get crd") && strings.Contains(c.Args, CRDOwnerLabel+"=acme.dev") {
			listed = true
		}
		if c.IsApply() && strings.Contains(c.Stdin, "kind: CustomResourceDefinition") && strings.Contains(c.Stdin, CRDOwnerLabel+": acme.dev") {
			stamped = true
		}
	}
	if !stamped {
		t.Errorf("the applied CRD carries no %s stamp: calls:\n%s", CRDOwnerLabel, describeCalls(calls()))
	}
	if !listed {
		t.Errorf("full apply with PruneCRDs listed no owned CRDs: calls:\n%s", describeCalls(calls()))
	}

	calls = fakeGateKubectl(t, jobCompletes)
	opts.Targets = []string{"api"}
	if err := applyRendered(context.Background(), opts, stream); err != nil {
		t.Fatalf("applyRendered (--target): %v", err)
	}
	for _, c := range calls() {
		if strings.Contains(c.Args, "get crd") || strings.Contains(c.Args, "delete crd") {
			t.Errorf("a --target apply touched CRDs: %s", c.Args)
		}
	}
}
