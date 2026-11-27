package config_test

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/pkg/config"
)

// The daemon gateway is handed none of the GTM assets bucket's settings or
// credentials: gtm_assets sits on ServerProcessConfig, and only the roots
// whose graph reads it (the API server and the worker) reach it. Roots() is
// the reach deploy/kcl/config_gen.k's ROOTS projects into every rendered
// workload's env. (TestGatewayReachesNoGTMConfig, from the feature-gate
// branch, holds every GTM_ name to the same rule.)
func TestGatewayReachesNoGTMAssetsConfig(t *testing.T) {
	found := false
	for _, r := range config.Roots() {
		if r.Name != "gateway" {
			continue
		}
		found = true
		for _, v := range r.Vars {
			if strings.HasPrefix(v.Name, "GTM_ASSETS_") {
				t.Errorf("the gateway root reaches %s (%s %s)", v.Name, v.Config, v.Path)
			}
		}
		for _, f := range r.Families {
			if strings.HasPrefix(f.Prefix, "GTM_ASSETS_") {
				t.Errorf("the gateway root reaches the env family %s<KEY>%s", f.Prefix, f.Suffix)
			}
		}
		for _, c := range r.Configs {
			for _, path := range c.Reach {
				if path == "gtm_assets" || strings.HasPrefix(path, "gtm_assets.") {
					t.Errorf("the gateway root reaches %s of its config %T", path, c.Message)
				}
			}
		}
	}
	if !found {
		t.Fatal("no gateway root in config.Roots()")
	}
}
