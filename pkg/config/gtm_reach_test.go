package config_test

import (
	"strings"
	"testing"

	forgeconfig "github.com/reliant-labs/forge/pkg/config"

	"github.com/reliant-labs/reliant/pkg/config"
)

// gtmEnvPrefix names every GTM setting: GTM_ALLOWED_ORG_IDS,
// GTM_ANALYTICS_TOKEN, the GTM_ASSETS_* bucket and whatever GTM adds later.
const gtmEnvPrefix = "GTM_"

// root is the root named name.
func root(t *testing.T, name string) forgeconfig.Root {
	t.Helper()
	for _, r := range config.Roots() {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no root %q in config.Roots()", name)
	return forgeconfig.Root{}
}

// The daemon gateway is handed no GTM setting and no GTM secret: the GTM
// blocks sit on ServerProcessConfig, and only the roots whose graph reads
// them (the API server and the worker) reach them. Roots() is the reach
// deploy/kcl/config_gen.k's ROOTS projects into every rendered workload's
// env, so a GTM_ name here is a GTM credential on every gateway pod.
func TestGatewayReachesNoGTMConfig(t *testing.T) {
	gw := root(t, "gateway")
	for _, v := range gw.Vars {
		if strings.HasPrefix(v.Name, gtmEnvPrefix) {
			t.Errorf("the gateway root reaches %s (%s %s)", v.Name, v.Config, v.Path)
		}
	}
	for _, f := range gw.Families {
		if strings.HasPrefix(f.Prefix, gtmEnvPrefix) {
			t.Errorf("the gateway root reaches the env family %s<KEY>%s (%s %s)", f.Prefix, f.Suffix, f.Config, f.Path)
		}
	}
	for _, c := range gw.Configs {
		for _, path := range c.Reach {
			if path == "gtm" || strings.HasPrefix(path, "gtm.") || strings.HasPrefix(path, "gtm_") {
				t.Errorf("the gateway root reaches %s of its config %T", path, c.Message)
			}
		}
	}
}
