package deploytarget

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
	"github.com/reliant-labs/forge/pkg/release"
)

// autoscaleCP is fakeCP that also answers GetCapabilities with features.
type autoscaleCP struct {
	*fakeCP
	features []string
}

func (c *autoscaleCP) Call(ctx context.Context, proc string, req, out any) error {
	if proc == ProcDeployCapabilities {
		c.mu.Lock()
		c.calls = append(c.calls, fakeCall{Proc: proc})
		c.mu.Unlock()
		b, _ := json.Marshal(map[string]any{"bundleDocumentFeatures": c.features})
		return json.Unmarshal(b, out)
	}
	return c.fakeCP.Call(ctx, proc, req, out)
}

func autoscaledGroup() ServiceGroup {
	g := hostedGroup("v1", map[string]string{"api": digestA}, v1alpha1.Resources{})
	g.Services[0].Hosted.Workload.Autoscale = &v1alpha1.Autoscale{MinReplicas: 2, MaxReplicas: 6}
	return g
}

// THE SAFETY PROPERTY. A control plane that predates autoscale decodes the
// spec, ignores the field and runs the workload at one replica, with the
// deploy green. forge refuses before ensuring the environment or recording
// the bundle, naming the workload and the capability.
//
// MUTATIONS VERIFIED RED:
//   - dropping the checkAutoscaleApplied call from Deploy → the deploy
//     ensures the environment and succeeds;
//   - moving it after ensureHostedEnvironment → EnsureEnvironment appears in
//     the call log.
func TestHostedDeployRefusesAutoscaleTheControlPlaneDoesNotApply(t *testing.T) {
	for name, features := range map[string][]string{
		"predates it":                      nil,
		"a pin bump: every decode feature": release.BundleDocFeatures(),
	} {
		t.Run(name, func(t *testing.T) {
			cp := &autoscaleCP{fakeCP: &fakeCP{status: readyStatus(digestA)}, features: features}
			recorded := false
			p := HostedProvider{Client: cp, PollInterval: time.Millisecond,
				RecordBundle: func(context.Context, string) (string, error) { recorded = true; return "", nil }}
			err := p.Deploy(context.Background(), autoscaledGroup())
			want := `workload "api" declares autoscale, but this control plane does not apply HorizontalPodAutoscalers yet (it does not advertise "autoscales-workloads"); set replicas instead until it is upgraded`
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Deploy = %v, want it to contain %q", err, want)
			}
			if got := cp.procs(); !slices.Equal(got, []string{"GetCapabilities"}) {
				t.Errorf("refused, but the calls were %v; want only GetCapabilities", got)
			}
			if recorded {
				t.Error("refused, but the bundle was recorded")
			}
		})
	}
}

// A control plane that advertises the capability takes the deploy as usual.
func TestHostedDeployAcceptsAutoscaleTheControlPlaneApplies(t *testing.T) {
	cp := &autoscaleCP{fakeCP: &fakeCP{status: readyStatus(digestA)}, features: []string{release.CapabilityAutoscalesWorkloads}}
	p := HostedProvider{Client: cp, PollInterval: time.Millisecond,
		RecordBundle: func(context.Context, string) (string, error) { return "", nil }}
	if err := p.Deploy(context.Background(), autoscaledGroup()); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if got, want := cp.procs(), []string{"GetCapabilities", "EnsureEnvironment", "GetStatus"}; !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}
