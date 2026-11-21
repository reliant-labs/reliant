package deploytarget

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// hounders' shape: a 2-replica service, a migrate job, one database, one site.
func houndersItems() []HostedPreflightItem {
	return []HostedPreflightItem{
		{Name: "api", Tier: HostedTierWorkload, Workload: &v1alpha1.WorkloadSpec{
			Kind: v1alpha1.KindService, Replicas: 2,
			Resources: v1alpha1.Resources{CPURequestMillicores: 500, MemoryRequestBytes: 512 << 20},
		}},
		{Name: "migrate", Tier: HostedTierWorkload, Workload: &v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Replicas: 3}},
		{Name: "db", Tier: HostedTierDatabase, Database: &v1alpha1.ManagedDatabaseSpec{Instances: 2, StorageGiB: 5}},
		{Name: "web", Tier: HostedTierStatic, Static: &v1alpha1.StaticSiteSpec{}},
	}
}

func TestDemandOfHoundersShape(t *testing.T) {
	got := DemandOf(houndersItems(), 2)
	want := CapacityDemand{
		// api 2×500m + job (1 pod, default 250m)
		CPUMillicores: 1250,
		// api 2×512MiB + job default 1GiB
		MemoryBytes: 2<<30 + 0,
		StorageGiB:  10, // 2 instances × 5 GiB
		Workloads:   2, Databases: 1, Builds: 2, StaticSites: 1,
	}
	if got != want {
		t.Fatalf("demand = %+v\nwant     %+v", got, want)
	}
}

type capacityCaller struct {
	resp map[string]any
	err  error
	req  map[string]any
}

func (c *capacityCaller) Call(_ context.Context, proc string, req, out any) error {
	if proc != procCheckDeployCapacity {
		return errors.New("unexpected " + proc)
	}
	c.req = req.(map[string]any)
	if c.err != nil {
		return c.err
	}
	return decodeInto(c.resp, out)
}

type codedErr struct{ code string }

func (e codedErr) Error() string         { return "coded " + e.code }
func (e codedErr) HasCode(c string) bool { return c == e.code }

func TestCheckHostedCapacityRefusalRendersRunbook(t *testing.T) {
	c := &capacityCaller{resp: map[string]any{
		"allowed": false, "code": "DEPLOY_CAPACITY_CODE_NO_COMPUTE_PLAN",
		"reason": "this runs compute and the organization has no active compute plan",
		"fix":    "subscribe to a Reliant Compute plan in billing settings, then retry",
	}}
	d := DemandOf(houndersItems(), 2)
	v, err := CheckHostedCapacity(context.Background(), c, "env-1", d)
	if err != nil || v.Allowed || v.Code != "NO_COMPUTE_PLAN" {
		t.Fatalf("verdict = %+v, %v", v, err)
	}
	if c.req["environmentId"] != "env-1" {
		t.Errorf("request = %v", c.req)
	}
	msg := (&CapacityRefusedError{Env: "prod", Verdict: v, Demand: d}).Error()
	for _, s := range []string{"no active compute plan", "subscribe to a Reliant Compute plan", "expected:", "found:", "1250m CPU", "nothing was built"} {
		if !strings.Contains(msg, s) {
			t.Errorf("refusal missing %q:\n%s", s, msg)
		}
	}
}

// A billed org past its static-site limit is told the limit that refused it
// and how to ask for more, not its compute ceiling (which it fits).
func TestCheckHostedCapacityStaticSiteLimitNamesTheLimit(t *testing.T) {
	c := &capacityCaller{resp: map[string]any{
		"allowed": false, "code": "DEPLOY_CAPACITY_CODE_STATIC_SITE_LIMIT_REACHED", "hasComputePlan": true,
		"ceilingCpuMillicores": "4000", "ceilingMemoryBytes": "8589934592", "ceilingStorageGib": "50",
		"reason": "this would make 101 live static sites and your organization's limit is 100 (each environment's site counts as one)",
		"fix":    "delete a static site or environment you no longer need, or ask for a higher limit: email support@reliantlabs.io with your organization id",
	}}
	v, err := CheckHostedCapacity(context.Background(), c, "env-1", CapacityDemand{StaticSites: 1})
	if err != nil || v.Allowed || v.Code != CapacityCodeStaticSiteLimit || v.Queued() {
		t.Fatalf("verdict = %+v, %v", v, err)
	}
	msg := (&CapacityRefusedError{Env: "prod", Verdict: v, Demand: CapacityDemand{StaticSites: 1}}).Error()
	for _, s := range []string{"STATIC_SITE_LIMIT_REACHED", "limit is 100", "static-site limit", "support@reliantlabs.io"} {
		if !strings.Contains(msg, s) {
			t.Errorf("refusal missing %q:\n%s", s, msg)
		}
	}
	if strings.Contains(msg, "ceiling of 4000m CPU") {
		t.Errorf("a static-site limit refusal points at the compute ceiling:\n%s", msg)
	}
}

func TestCheckHostedCapacityUnimplementedIsUnchecked(t *testing.T) {
	v, err := CheckHostedCapacity(context.Background(), &capacityCaller{err: codedErr{"unimplemented"}}, "", CapacityDemand{})
	if err != nil || !v.Unchecked {
		t.Fatalf("verdict = %+v, %v", v, err)
	}
	if _, err := CheckHostedCapacity(context.Background(), &capacityCaller{err: codedErr{"unavailable"}}, "", CapacityDemand{}); err == nil {
		t.Fatal("a network failure must fail, not fall open")
	}
}

func decodeInto(reply map[string]any, out any) error {
	b, _ := json.Marshal(reply)
	return json.Unmarshal(b, out)
}
