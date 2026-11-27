package config_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
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

// The GTM blocks are on ServerProcessConfig, never the root config: gtm is
// field 18 and gtm_assets field 19, and the root config reserves the
// numbers and names both once had there.
func TestGTMBlocksAreServerProcessFields(t *testing.T) {
	server := (&configv1.ServerProcessConfig{}).ProtoReflect().Descriptor().Fields()
	for name, number := range map[string]protoreflect.FieldNumber{"gtm": 18, "gtm_assets": 19} {
		f := server.ByName(protoreflect.Name(name))
		if f == nil {
			t.Errorf("ServerProcessConfig has no field %s", name)
			continue
		}
		if f.Number() != number {
			t.Errorf("ServerProcessConfig.%s is field %d, want %d", name, f.Number(), number)
		}
	}
	root := (&configv1.ReliantConfig{}).ProtoReflect().Descriptor()
	for _, name := range []string{"gtm", "gtm_assets"} {
		if root.Fields().ByName(protoreflect.Name(name)) != nil {
			t.Errorf("ReliantConfig composes %s: every root, the gateway included, would reach it", name)
		}
		if !root.ReservedNames().Has(protoreflect.Name(name)) {
			t.Errorf("ReliantConfig does not reserve the name %s", name)
		}
	}
	for _, number := range []protoreflect.FieldNumber{8, 9} {
		if !root.ReservedRanges().Has(number) {
			t.Errorf("ReliantConfig does not reserve field number %d", number)
		}
	}
}
