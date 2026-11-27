package release

import (
	"slices"
	"strings"
	"testing"
)

// An autoscaled hosted workload is refused to a control plane that does not
// apply HorizontalPodAutoscalers, never sent: it would ignore the field and
// run the workload at one replica.
func TestBundleReader_CheckAutoscaledWorkloads(t *testing.T) {
	workloads := []string{"api", "indexer"}
	for name, r := range map[string]*BundleReader{
		"machine ledger (nil reader)": nil,
		"applies them":                {Features: []string{CapabilityAutoscalesWorkloads}},
	} {
		if err := r.CheckAutoscaledWorkloads(workloads); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, r := range map[string]*BundleReader{
		"predates it":                      {},
		"a pin bump: every decode feature": {Features: BundleDocFeatures()},
	} {
		err := r.CheckAutoscaledWorkloads(workloads)
		if err == nil {
			t.Fatalf("%s: CheckAutoscaledWorkloads = nil, want a refusal", name)
		}
		for _, w := range workloads {
			want := `workload "` + w + `" declares autoscale, but this control plane does not apply HorizontalPodAutoscalers yet (it does not advertise "autoscales-workloads"); set replicas instead until it is upgraded`
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: CheckAutoscaledWorkloads = %v, want it to contain %q", name, err, want)
			}
		}
	}
	if err := (&BundleReader{}).CheckAutoscaledWorkloads(nil); err != nil {
		t.Errorf("no workload autoscales, yet refused: %v", err)
	}
	if CapabilityAutoscalesWorkloads != "autoscales-workloads" {
		t.Errorf("CapabilityAutoscalesWorkloads = %q; the wire string is frozen", CapabilityAutoscalesWorkloads)
	}
	// Applying is added by hand, never claimed by a pin bump.
	if slices.Contains(BundleDocFeatures(), CapabilityAutoscalesWorkloads) {
		t.Errorf("BundleDocFeatures claims %s", CapabilityAutoscalesWorkloads)
	}
}
