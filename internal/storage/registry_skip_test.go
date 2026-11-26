package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// clusterStateRunner fakes kubectl failing to reach the cluster and docker
// reporting its server containers in the given states.
func clusterStateRunner(states ...string) Runner {
	return Runner{Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "kubectl" {
			return nil, fmt.Errorf("The connection to the server 0.0.0.0:6445 was refused")
		}
		if contains(args, "ps") && contains(args, "label=k3d.cluster=other") && contains(args, "label=k3d.role=server") {
			return []byte(strings.Join(states, "\n")), nil
		}
		return nil, fmt.Errorf("unexpected docker args %v", args)
	}}
}

// A laptop's other project has its cluster stopped. Its images must survive
// (nothing is deleted), but that is an ordinary state, not a failed pass: it
// used to be recorded as "failed in registry …" every day, and `env up` in an
// unrelated project repeated it.
func TestProtectedSetOfAStoppedClusterIsASkipNotAFailure(t *testing.T) {
	reg := Registry{Container: "k3d-shared-registry", Contexts: []string{"k3d-other"}, Aliases: []string{"reg:5000"}}
	for name, states := range map[string][]string{
		"stopped":           {"exited"},
		"deleted in docker": nil,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := clusterStateRunner(states...).protected(context.Background(), reg)
			var skip *SkippedError
			if !errors.As(err, &skip) {
				t.Fatalf("err = %v, want a SkippedError (it must still delete nothing)", err)
			}
			if !strings.Contains(skip.Reason, "k3d-other") || !strings.Contains(skip.Reason, "not running") {
				t.Fatalf("reason does not say which cluster is not running: %q", skip.Reason)
			}
			r := NewGCResult(time.Now(), layerErr("registry "+reg.Container, fmt.Errorf("registry %s: %w", reg.Container, err)))
			if !r.OK || len(r.FailedLayers) != 0 || len(r.Skipped) != 1 || !strings.HasPrefix(r.Skipped[0], "registry k3d-shared-registry: ") {
				t.Fatalf("result = %+v; want OK with one skip naming the registry", r)
			}
			if s := r.Summary(); !strings.Contains(s, "skipped registry k3d-shared-registry") {
				t.Fatalf("summary does not name the skip: %s", s)
			}
			for _, e := range []error{err, layerErr("registry "+reg.Container, err)} {
				if RealFailure(e) != nil {
					t.Fatalf("a skip failed the pass's exit status: %v", e)
				}
			}
		})
	}
}

// A cluster that is RUNNING and does not answer is a real failure: something
// is wrong and nobody would otherwise look.
func TestProtectedSetOfARunningUnreachableClusterStillFails(t *testing.T) {
	reg := Registry{Container: "k3d-shared-registry", Contexts: []string{"k3d-other"}, Aliases: []string{"reg:5000"}}
	_, err := clusterStateRunner("running").protected(context.Background(), reg)
	var skip *SkippedError
	if err == nil || errors.As(err, &skip) {
		t.Fatalf("err = %v, want a failure", err)
	}
	if r := NewGCResult(time.Now(), layerErr("registry "+reg.Container, err)); r.OK {
		t.Fatalf("a running, unreachable cluster was recorded as OK: %+v", r)
	}
}

// Docker that cannot answer is "cannot tell", never "stopped".
func TestProtectedSetFailsWhenDockerCannotSayTheClusterIsStopped(t *testing.T) {
	r := Runner{Command: func(context.Context, string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("Cannot connect to the Docker daemon")
	}}
	_, err := r.protected(context.Background(), Registry{Contexts: []string{"k3d-other"}})
	var skip *SkippedError
	if err == nil || errors.As(err, &skip) {
		t.Fatalf("err = %v, want a failure", err)
	}
}

func TestRegistryGCProblemIsScopedToOneRegistry(t *testing.T) {
	now := time.Now()
	p := DefaultPolicy()
	p.Registries = []Registry{{Container: "k3d-mine"}, {Container: "k3d-theirs"}}
	theirsFailed := GCResult{At: now.Add(-10 * time.Hour), FailedLayers: []string{"registry k3d-theirs"},
		Error: "registry k3d-theirs: refusing registry cleanup: protected set for k3d-x unavailable: exit status 1\nThe connection to the server was refused"}
	mineFailed := GCResult{At: now.Add(-10 * time.Hour), FailedLayers: []string{"logs", "registry k3d-mine"},
		Error: "logs: permission denied\nregistry k3d-mine: registry k3d-mine has no published port"}
	for _, tc := range []struct {
		name      string
		container string
		last      GCResult
		ok        bool
		expect    string
	}{
		{"not registered", "k3d-unknown", mineFailed, true, ""},
		{"another registry failed", "k3d-mine", theirsFailed, true, ""},
		{"another layer failed", "k3d-theirs", mineFailed, true, ""},
		{"this registry failed, with the reason", "k3d-mine", mineFailed, true, "the last cleanup of registry k3d-mine (10h ago) failed: registry k3d-mine has no published port"},
		{"failure with no layer attribution", "k3d-mine", GCResult{At: now.Add(-time.Hour)}, true, "the last cleanup of registry k3d-mine (1h ago) failed"},
		{"never ran", "k3d-mine", GCResult{}, false, "no cleanup of registry k3d-mine has completed yet"},
		{"stale", "k3d-mine", GCResult{At: now.Add(-72 * time.Hour), OK: true}, true, "the last cleanup of registry k3d-mine was 3d ago; the daily pass is not running"},
		{"skipped is not a problem", "k3d-mine", GCResult{At: now.Add(-time.Hour), OK: true, Skipped: []string{"registry k3d-mine: cluster k3d-x is not running"}}, true, ""},
		{"healthy", "k3d-mine", GCResult{At: now.Add(-time.Hour), OK: true}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RegistryGCProblem(p, tc.container, tc.last, tc.ok, now)
			if tc.expect == "" && got != "" || tc.expect != "" && got != tc.expect {
				t.Fatalf("RegistryGCProblem = %q, want %q", got, tc.expect)
			}
		})
	}
}

func TestLayerErrorQuotesOneBoundedLine(t *testing.T) {
	long := strings.Repeat("é", layerErrorMax+50)
	r := GCResult{Error: "logs: denied\nregistry k3d-a: " + long + "\nsecond line of stderr"}
	got := r.LayerError("registry k3d-a")
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != layerErrorMax+1 || strings.Contains(got, "\n") {
		t.Fatalf("LayerError = %q (%d runes); want %d runes, one line, truncated", got, len([]rune(got)), layerErrorMax+1)
	}
	if got := r.LayerError("registry k3d-b"); got != "" {
		t.Fatalf("LayerError for a layer with no line = %q", got)
	}
}
