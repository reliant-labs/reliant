package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/cluster"
)

// Bootstrap has only the typed entities, not the apply stream. Render through
// the same manifest pipeline as deploy so labels and overrides remain canonical.
var renderPlatformPrerequisitesFn = func(ctx context.Context, env string, entities *KCLEntities) (string, error) {
	store, err := loadProjectStore()
	if err != nil {
		return "", err
	}
	// -C changes config resolution, not the process working directory. Resolve
	// the entrypoint against that same project root before checking/rendering it.
	projectDir := projectDirForKCL()
	kclDir := store.K8s().KCLDir
	if kclDir == "" {
		kclDir = "deploy/kcl"
	}
	if !filepath.IsAbs(kclDir) {
		kclDir = filepath.Join(projectDir, kclDir)
	}
	mainK := filepath.Join(kclDir, env, "main.k")
	if _, err := os.Stat(mainK); err != nil {
		return "", fmt.Errorf("environment %q entrypoint %s: %w", env, mainK, err)
	}
	namespace := k8sClusterFieldFromEntities(entities, "namespace")
	if namespace == "" {
		namespace = store.Meta().Name + "-" + env
	}
	return renderPlatformPrerequisiteManifestsFn(ctx, mainK, entities.ImageTag, namespace, env, loadDeployEnvConfigKV(projectDir, env), nil)
}

var renderPlatformPrerequisiteManifestsFn = cluster.RenderManifests

var applyPlatformPrerequisitesFn = cluster.ApplyClusterPrerequisites

// Bootstrap only touches contexts with selected managed-cluster charts. All
// prerequisites (cluster.ChartClusterPrerequisites: PriorityClasses,
// StorageClasses, CRDs, ...) land before the first chart's readiness wait,
// including those on secondary clusters. Routing uses the same group scope as
// deploy: explicitly routed objects stay on their cluster; genuinely shared
// ones are replicated.
func applyPlatformPrerequisites(ctx context.Context, env, project string, entities *KCLEntities, installs []platformChartInstall) error {
	manifests, err := renderPlatformPrerequisitesFn(ctx, env, entities)
	if err != nil {
		return fmt.Errorf("render chart cluster prerequisites: %w", err)
	}
	manifests = cluster.StampOwnership(manifests, cluster.ObjectOwner(project, env))
	groups, err := buildDeployGroupsForEnv(env, entities, k8sClusterFieldFromEntities(entities, "namespace"), entities.ImageTag, false)
	if err != nil {
		return err
	}
	scopeFor := clusterScopeForGroups(groups, entities)
	seen := map[string]bool{}
	for _, in := range installs {
		if seen[in.Context] {
			continue
		}
		seen[in.Context] = true
		scope := cluster.GroupScope{Cluster: in.Context}
		for _, g := range groups {
			if g.ProviderID == "k8s-cluster" && resolveGroupContext(g) == in.Context {
				if s := scopeFor(g); s != nil {
					scope = *s
				}
				break
			}
		}
		pre := cluster.ChartClusterPrerequisites(cluster.ScopeManifestsToGroup(manifests, scope))
		if strings.TrimSpace(pre) == "" {
			continue
		}
		if err := applyPlatformPrerequisitesFn(ctx, in.Context, pre); err != nil {
			return fmt.Errorf("apply chart cluster prerequisites to %q: %w", in.Context, err)
		}
	}
	return nil
}
