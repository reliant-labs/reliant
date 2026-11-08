package bundle

import (
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestBundleExternalSecretHealth(t *testing.T) {
	for _, sync := range []string{"", "shadow", "deferred"} {
		t.Run("sync="+sync, func(t *testing.T) {
			annotations := ""
			if sync != "" {
				annotations = "    forge.dev/secret-sync: " + sync + "\n"
			}
			docs := []tdoc{
				{cluster: tHub, kind: "ExternalSecret", name: "credentials", apiVersion: "external-secrets.io/v1", annotations: annotations},
				{cluster: tHub, kind: "Deployment", name: "consumer", apiVersion: "apps/v1"},
			}
			in := prodInput(docs)
			in.Units = nil
			built := mustBuild(t, in)
			extracted := fluxExtract(t, built, release.BundleManifestsLayer)
			var pre, rollout release.BundleTree
			for _, tree := range built.Doc.Trees {
				if tree.Phase == release.PhasePre {
					pre = tree
				} else if tree.Phase == release.PhaseRollout {
					rollout = tree
				}
			}
			wantHealth := 0
			if sync == "" {
				wantHealth = 1
			}
			if len(pre.Health) != wantHealth {
				t.Fatalf("sync %q health=%+v", sync, pre.Health)
			}
			if wantHealth == 1 && pre.Health[0].Kind != "ExternalSecret" {
				t.Fatalf("wrong health kind: %+v", pre.Health)
			}
			if !reflect.DeepEqual(rollout.DependsOn, []string{pre.Path}) {
				t.Fatalf("rollout must wait for secret tree: %+v", rollout)
			}
			if names, _ := filesUnder(extracted, pre.Path); len(names) != 1 {
				t.Fatalf("secret not packed under gated path %s: %v", pre.Path, names)
			}
		})
	}
}
