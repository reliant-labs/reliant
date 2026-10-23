package cluster

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// foreignClusterListCap bounds how many offending objects a refusal names. The
// count is always exact; past the cap the list says how many it left out, so a
// whole misrouted cluster does not bury the fix under hundreds of lines.
const foreignClusterListCap = 12

// ErrNothingApplied marks a refusal that was decided before any object was
// sent to a cluster. A deploy that fails with it changed nothing, which is a
// different fact from "failed part-way; some of it may be live".
var ErrNothingApplied = errors.New("nothing was applied")

// refuseForeignClusterDocs refuses a manifest stream that carries an object
// stamped for a DIFFERENT cluster than the kubectl context it is about to be
// written to.
//
// ClusterRoutingLabel is the render's statement of which context an object
// belongs on. A scoped apply honours it (ScopeManifestsToGroup drops every
// object stamped for another cluster), but an apply that reached kubectl
// UNSCOPED used to write the whole stream to the one context it was handed,
// whatever cluster each object named. That happened on 2026-10-09: a prod
// deploy whose entity read failed fell through to the env-wide direct apply,
// and every object stamped for prod's daemon cluster — a CNPG operator, its
// CRDs and webhooks, the kata pre-pull DaemonSet, a StorageClass, a
// RuntimeClass and RBAC — landed on the main cluster, where the operator
// crash-looped for a day.
//
// The label is the declaration, so a write that contradicts it is a routing
// bug upstream of this call, and the only safe answer is to write nothing and
// name what was misrouted. Every document is checked before anything is sent,
// so a refusal is never a partial apply.
//
// A document with no routing label (an env-shared object the render
// attributes to no cluster) and one that does not parse make no claim to
// contradict, so they pass. An empty kctx passes too: refusing a write with no
// context at all is KubectlApplyNamespaced's own check, and a dry run with no
// context writes nothing.
func refuseForeignClusterDocs(kctx, manifests string) error {
	kctx = strings.TrimSpace(kctx)
	if kctx == "" {
		return nil
	}
	var foreign []string
	for _, doc := range splitDocs(manifests) {
		m, ok := parseDoc(doc)
		if !ok {
			continue
		}
		stamped := strings.TrimSpace(m.Metadata.Labels[ClusterRoutingLabel])
		if stamped == "" || stamped == kctx {
			continue
		}
		foreign = append(foreign, describeForeignDoc(m, stamped))
	}
	if len(foreign) == 0 {
		return nil
	}
	sort.Strings(foreign)
	shown := foreign
	more := ""
	if len(shown) > foreignClusterListCap {
		shown = shown[:foreignClusterListCap]
		more = fmt.Sprintf("\n    … and %d more", len(foreign)-foreignClusterListCap)
	}
	return fmt.Errorf("refusing to apply to kubectl context %q: %d object(s) in this stream are stamped %s for a different cluster, and %w:\n    %s%s\n"+
		"  Each object lands only on the cluster its %s label names. A stream that still carries another cluster's objects at this point means the "+
		"deploy lost its multi-cluster routing — a forge bug, not something to work around. `forge env render <env> --list` shows where each object should go",
		kctx, len(foreign), ClusterRoutingLabel, ErrNothingApplied, strings.Join(shown, "\n    "), more, ClusterRoutingLabel)
}

// describeForeignDoc names one misrouted object as `Kind ns/name → context`.
func describeForeignDoc(m parsedDoc, stamped string) string {
	name := m.Metadata.Name
	if m.Metadata.Namespace != "" {
		name = m.Metadata.Namespace + "/" + name
	}
	return fmt.Sprintf("%s %s → %s", m.Kind, name, stamped)
}
