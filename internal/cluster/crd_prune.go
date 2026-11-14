package cluster

// CRD prune — removing CustomResourceDefinitions an env STOPPED rendering.
//
// `forge env up` / `forge env deploy` is a declarative reconcile, but until
// this file existed it only ever ADDED CRDs: a CRD dropped from the KCL (an
// operator retired, a kind renamed — SimpleBackend → Workload is the case that
// surfaced it) stayed in the cluster forever, still served, still admitting
// objects nobody reconciles. The Deployment prune (Prune) never covered it,
// because a CRD is cluster-scoped and deleting one is far more destructive
// than deleting a Deployment: it deletes EVERY custom resource of that kind,
// in every namespace.
//
// So the prune is deliberately narrow. A CRD is deleted only when ALL hold:
//
//  1. forge stamped it as THIS env's — `forge.dev/crd-owner=<project>.<env>`
//     plus `app.kubernetes.io/managed-by=forge`. The stamp is written by
//     StampCRDOwnership on the apply path and nowhere else, and the listing
//     is SERVER-SIDE label-selected on it, so a CRD forge did not create (a
//     helm chart's, cert-manager's, another env's, a hand-applied one) is
//     never even a candidate.
//  2. it is absent from the stream this apply just sent.
//  3. no custom resource of it exists anywhere in the cluster. A CRD that
//     still has instances is REPORTED and kept: deleting it would destroy
//     data, and on a shared cluster the instances may belong to a workload
//     this render does not know about. Removing them is a human decision.
//
// It runs only on a FULL apply (see Apply): a --target apply renders a subset
// of the env, so a CRD missing from it proves nothing.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// CRDOwnerLabel names the (project, env) that rendered a CRD. It is the
	// prune's ownership proof; see the file comment.
	CRDOwnerLabel = "forge.dev/crd-owner"
	// AppManagedByLabel is the standard managed-by key. forge's own value is
	// "forge".
	AppManagedByLabel = "app.kubernetes.io/managed-by"
)

var labelValueUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// CRDOwner is the CRDOwnerLabel value for a (project, env): "<project>.<env>",
// sanitized to a legal label value. Empty when either half is unknown, which
// disables both the stamp and the prune — an owner that cannot be named
// cannot be proven.
func CRDOwner(project, env string) string {
	if strings.TrimSpace(project) == "" || strings.TrimSpace(env) == "" {
		return ""
	}
	v := labelValueUnsafe.ReplaceAllString(project+"."+env, "-")
	if len(v) > 63 {
		v = v[:63]
	}
	return strings.Trim(v, "._-")
}

// StampCRDOwnership labels every CustomResourceDefinition in the stream with
// CRDOwnerLabel=owner, and with managed-by=forge unless the author set one.
// Other documents pass through byte-identical. An empty owner is a no-op.
func StampCRDOwnership(manifests, owner string) string {
	if owner == "" {
		return manifests
	}
	docs := splitDocs(manifests)
	changed := false
	for i, doc := range docs {
		if m, ok := parseDoc(doc); !ok || m.Kind != "CustomResourceDefinition" {
			continue
		}
		b, ok := stampCRDLabels(doc, owner)
		if !ok {
			continue
		}
		docs[i] = strings.TrimRight(string(b), "\n")
		changed = true
	}
	if !changed {
		return manifests
	}
	return strings.Join(docs, docDelimiter)
}

// stampCRDLabels sets the ownership labels on one CRD document by editing its
// yaml.Node tree, so every key keeps its position and the document diffs as
// the added label lines only.
func stampCRDLabels(doc, owner string) ([]byte, bool) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil || len(root.Content) == 0 {
		return nil, false
	}
	obj := root.Content[0]
	if obj.Kind != yaml.MappingNode {
		return nil, false
	}
	meta := mappingChild(obj, "metadata")
	labels := mappingChild(meta, "labels")
	setScalar(labels, CRDOwnerLabel, owner, true)
	setScalar(labels, AppManagedByLabel, "forge", false)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// mappingChild returns key's mapping value under m, appending an empty one
// when absent.
func mappingChild(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key && m.Content[i+1].Kind == yaml.MappingNode {
			return m.Content[i+1]
		}
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
	return child
}

// setScalar sets key=value in mapping m; with overwrite false an existing key
// is left as the author wrote it.
func setScalar(m *yaml.Node, key, value string, overwrite bool) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			if overwrite {
				m.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			}
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// PruneCRDs deletes this owner's CRDs that the stream no longer renders and
// that have no remaining instances. It returns the CRDs it deleted and the
// ones it kept because they still have instances. Delete failures are
// reported on stdout and do not stop the rest; a failed LIST is an error.
func PruneCRDs(ctx context.Context, kctx, manifests, owner string) (pruned, keptInUse []string, err error) {
	if strings.TrimSpace(kctx) == "" {
		return nil, nil, errors.New("crd prune: no kubectl context — a cluster-scoped delete is never aimed at the current context")
	}
	if owner == "" {
		return nil, nil, errors.New("crd prune: no owner (project and env) — without one, forge cannot prove a CRD is its own")
	}
	rendered := map[string]bool{}
	for _, n := range renderedNamesByKind(manifests, "CustomResourceDefinition") {
		rendered[n] = true
	}
	selector := fmt.Sprintf("%s=%s,%s=forge", CRDOwnerLabel, owner, AppManagedByLabel)
	out, lerr := kubectlCmd(ctx, kctx, "get", "crd", "-l", selector,
		"-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`).Output()
	if lerr != nil {
		return nil, nil, fmt.Errorf("crd prune: list CRDs owned by %s: %w", owner, lerr)
	}
	var orphans []string
	for _, name := range strings.Split(string(out), "\n") {
		name = strings.TrimSpace(name)
		if name != "" && !rendered[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	for _, name := range orphans {
		inst, gerr := kubectlCmd(ctx, kctx, "get", name, "-A", "-o", "name", "--ignore-not-found").Output()
		if gerr != nil {
			fmt.Printf("  Warning: crd prune: cannot list instances of %s (%v); keeping it\n", name, gerr)
			keptInUse = append(keptInUse, name)
			continue
		}
		if n := countLines(string(inst)); n > 0 {
			fmt.Printf("  crd %s is no longer rendered but still has %d instance(s); keeping it. Delete them first if it should go.\n", name, n)
			keptInUse = append(keptInUse, name)
			continue
		}
		fmt.Printf("Pruning CRD %s (no longer rendered by %s, no instances)\n", name, owner)
		del := kubectlCmd(ctx, kctx, "delete", "crd", name, "--ignore-not-found=true")
		del.Stdout = os.Stdout
		del.Stderr = os.Stderr
		if derr := del.Run(); derr != nil {
			fmt.Printf("  Warning: delete crd %s: %v\n", name, derr)
			continue
		}
		pruned = append(pruned, name)
	}
	return pruned, keptInUse, nil
}

func countLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}
