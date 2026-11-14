package cli

import (
	"context"
	"io"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

type externalResolver struct {
	workloads []WorkloadEntity
}

func (externalResolver) Resolve(context.Context, string, string) envTarget { return envTarget{} }

func (r externalResolver) ExternalWorkloads(context.Context, string, string) []WorkloadEntity {
	return r.workloads
}

func TestEnvStatus_ExternalImagesAreReadThroughTheStatusHook(t *testing.T) {
	resolved := map[string]string{
		"ghcr.io/acme/api":    vDigestA,
		"ghcr.io/acme/worker": vDigestA,
		"ghcr.io/acme/web":    vDigestB,
	}
	hooks := &verifyHooks{status: map[string]string{"api": statusDoc(vDigestA), "worker": statusDoc(vDigestB)}}
	opts := envStatusOptions{
		Resolver: externalResolver{workloads: []WorkloadEntity{
			externalWorkload("api", "ghcr.io/acme/api:v1", fullHooks),
			externalWorkload("worker", "ghcr.io/acme/worker", fullHooks),
		}},
		ExternalHooks: deploytarget.ExternalHooks{Runner: hooks, Stderr: io.Discard},
	}
	external, rest := splitExternalImages(context.Background(), ".", "prod", resolved, opts)
	if len(rest) != 1 || rest["ghcr.io/acme/web"] != vDigestB {
		t.Errorf("only the cluster image is left for the cluster read, got %v", rest)
	}
	got := map[string]imageState{}
	for _, v := range external {
		got[v.Image] = v.State
	}
	if got["ghcr.io/acme/api"] != imageMatch || got["ghcr.io/acme/worker"] != imageDrift || len(got) != 2 {
		t.Errorf("external verdicts = %v", got)
	}
	for _, ran := range hooks.ran {
		if ran != "api:status" && ran != "worker:status" {
			t.Errorf("env status ran %s; it may only read", ran)
		}
	}
}

func TestEnvStatus_ExternalWithoutStatusHookIsUnreachable(t *testing.T) {
	opts := envStatusOptions{Resolver: externalResolver{workloads: []WorkloadEntity{
		externalWorkload("api", "ghcr.io/acme/api", deploytarget.ExternalRuntime{}),
	}}}
	external, rest := splitExternalImages(context.Background(), ".", "prod", map[string]string{"ghcr.io/acme/api": vDigestA}, opts)
	if len(rest) != 0 || len(external) != 1 || external[0].State != imageUnreachable || external[0].Detail == "" {
		t.Errorf("a record-only binding cannot be read: want UNREACHABLE with a reason, got %+v rest %v", external, rest)
	}
	if tally := tallyEnvStatusRelease(external); tally.Unreachable != 1 {
		t.Errorf("tally = %+v", tally)
	}
}

func TestEnvStatus_ResolverWithoutExternalSeamIsUnchanged(t *testing.T) {
	resolved := map[string]string{"ghcr.io/acme/api": vDigestA}
	external, rest := splitExternalImages(context.Background(), ".", "prod", resolved, envStatusOptions{Resolver: kclTargetResolverStub{}})
	if external != nil || len(rest) != 1 {
		t.Errorf("external %v rest %v", external, rest)
	}
}

type kclTargetResolverStub struct{}

func (kclTargetResolverStub) Resolve(context.Context, string, string) envTarget { return envTarget{} }
