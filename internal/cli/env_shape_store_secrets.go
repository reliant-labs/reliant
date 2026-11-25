package cli

import (
	"fmt"
	"sort"

	"github.com/reliant-labs/forge/pkg/release"
)

// shapeStoreSecretsOf projects the env's store-backed secrets and the
// components that serve them (OpenBao, the External Secrets Operators) into the
// shape's store_secrets, generated_secrets, openbao and
// external_secrets_operators fields.
//
// THE CLUSTER ATTRIBUTION is the one thing the render cannot say for itself, so
// it is resolved here with the same answer the rest of the CLI uses:
//
//   - a Secret lands on its source's `cluster` when it names one, else on the
//     env's primary cluster target (the placement lib/external_secret.k renders
//     its ExternalSecret into);
//   - a cluster's connected name is declaredBindings' (primary target, each
//     cluster workload, Bundle.connected_clusters, delivered Secrets), the same
//     map the bundle's cluster bindings use, so a shape and a bundle cannot
//     disagree about which cluster a role is derived for.
//
// A store-backed Secret that lands on no cluster is an error from
// release.ProjectStoreSecrets (there is no role to derive for it); KCL has
// already refused that case at render, so reaching it here means the render and
// this projection disagree, and the message says so.
func shapeStoreSecretsOf(e *KCLEntities) (release.StoreProjection, error) {
	if e == nil {
		return release.StoreProjection{}, nil
	}
	bound, _ := declaredBindings(e)
	connected := func(context string) string {
		if names := bound[context]; len(names) > 0 {
			return names[0]
		}
		return ""
	}
	primary := e.ClusterTarget.field("cluster")

	in := release.StoreRender{ExternalSecretsOperators: e.ExternalSecretsOperators}
	for i := range in.ExternalSecretsOperators {
		if in.ExternalSecretsOperators[i].ConnectedCluster == "" {
			in.ExternalSecretsOperators[i].ConnectedCluster = connected(in.ExternalSecretsOperators[i].Cluster)
		}
	}
	if o := e.OpenBao; o != nil {
		in.OpenBao = &release.RenderedOpenBao{
			Name: o.Name, Namespace: o.Namespace, Image: o.Image, Cluster: o.Cluster,
			ConnectedCluster: o.ConnectedCluster,
		}
		if in.OpenBao.ConnectedCluster == "" {
			in.OpenBao.ConnectedCluster = connected(o.Cluster)
		}
	}

	for _, s := range e.RequiredSecrets {
		if s.Source == nil {
			continue
		}
		context := s.Source.Cluster
		if context == "" {
			context = primary
		}
		rs := release.RenderedStoreSecret{Name: s.Name, Namespace: s.Namespace, Source: &s.Source.RenderedStoreSource}
		if context != "" {
			rs.Clusters = []release.StoreCluster{{Cluster: context, ConnectedCluster: connected(context)}}
		}
		in.Secrets = append(in.Secrets, rs)
	}
	// A control-plane Secret delivered through the store reads its value from
	// the namespace's role like any other source; it lands on its own cluster,
	// through the connected cluster it was declared with.
	for _, c := range e.ControlPlaneSecrets {
		if c.Source == nil {
			continue
		}
		in.Secrets = append(in.Secrets, release.RenderedStoreSecret{
			Name: c.Name, Namespace: c.Namespace, Source: &c.Source.RenderedStoreSource,
			Clusters: []release.StoreCluster{{Cluster: c.Cluster, ConnectedCluster: firstNonEmpty(c.ConnectedCluster, connected(c.Cluster))}},
		})
	}
	sort.SliceStable(in.Secrets, func(i, j int) bool {
		a, b := in.Secrets[i], in.Secrets[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})

	out, err := release.ProjectStoreSecrets(in)
	if err != nil {
		return release.StoreProjection{}, fmt.Errorf("project store-backed secrets: %w", err)
	}
	return out, nil
}
