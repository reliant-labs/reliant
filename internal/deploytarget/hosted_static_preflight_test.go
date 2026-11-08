package deploytarget

import (
	"strings"
	"testing"

	v1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

func TestHostedStaticPreflightAcceptsUnresolvedPlatformAddress(t *testing.T) {
	group := ServiceGroup{Env: "prod", ProviderID: HostedProviderID, Services: []ResolvedService{{Name: "web", Hosted: &HostedWorkload{Tier: HostedTierStatic, Artifact: "web/static.v1", Static: &v1alpha1.StaticSiteSpec{}}}}}
	items, err := PreflightHosted(group)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !strings.HasPrefix(items[0].Static.ReleaseRepository, preflightRegistry+"/") {
		t.Fatalf("not an obvious display placeholder: %+v", items)
	}
	records, err := PreflightHostedRecords(group)
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %+v %v", records, err)
	}
	if group.Services[0].Hosted.Artifact != "web/static.v1" {
		t.Fatal("preflight mutated declaration")
	}
	group.Hosted = &HostedTarget{Release: "v1", Digests: map[string]string{"web/static.v1": digestB}}
	if _, err := HostedRecords(group); err == nil {
		t.Fatal("live release accepted unresolved address")
	}
}
