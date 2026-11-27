package obsbackend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/profilequery"
)

// flamegraphReply is a GetFlamegraph answer as protojson writes it: the
// speedscope document as a string, the int64 total as a JSON string.
func flamegraphReply(t *testing.T, extra string) string {
	t.Helper()
	var b bytes.Buffer
	p := profilequery.Profile{Service: "api", SampleType: "cpu", Unit: "nanoseconds", Stacks: []profilequery.Stack{
		{Frames: []string{"main.main", "main.idle"}, Value: 3000},
		{Frames: []string{"main.main", "main.work", "crypto/sha256.Sum256"}, Value: 7000},
	}}
	if err := p.Speedscope(&b); err != nil {
		t.Fatal(err)
	}
	doc, _ := json.Marshal(b.String())
	return `{"speedscopeJson":` + string(doc) + `,"totalValue":"10000","unit":"nanoseconds","stackCount":2,` +
		`"availableProfileTypes":["cpu","inuse_space"]` + extra + `}`
}

// The request carries the env's scope with the window and no instance
// narrowing, the service, type, trace and env name; the speedscope answer is
// read back into stacks, heaviest first.
func TestCloudFlamegraph(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procGetFlamegraph: flamegraphReply(t, `,"truncated":true,"env":"prod"`)}}
	c := cloudWith(f)
	c.Instances = Instances{IDs: []string{"inst-1"}}
	rng := Range{From: t0c.Add(-time.Hour), To: t0c}
	res, err := c.Flamegraph(context.Background(), FlamegraphRequest{Service: "api", SampleType: "cpu", TraceID: "0af7651916cd43dd8448eb211c80319c", Env: "prod", Range: rng})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Profile
	if p.Service != "api" || p.SampleType != "cpu" || p.Unit != "nanoseconds" || !p.From.Equal(rng.From) || !p.To.Equal(rng.To) {
		t.Errorf("profile = %+v", p)
	}
	if len(p.Stacks) != 2 || p.Stacks[0].Value != 7000 || p.Stacks[0].Frames[2] != "crypto/sha256.Sum256" || p.Total() != 10000 {
		t.Errorf("stacks = %v, want two, the heaviest first, root to leaf", p.Stacks)
	}
	if res.TotalValue != 10000 || !res.Truncated || !res.EnvApplied || fmt.Sprint(res.AvailableTypes) != "[cpu inuse_space]" {
		t.Errorf("result = %+v", res)
	}
	req := f.reqs[procGetFlamegraph]
	scope := req["scope"].(map[string]any)
	tr := scope["timeRange"].(map[string]any)
	if scope["orgId"] != "org-1" || fmt.Sprint(scope["envIds"]) != "[env-1]" || tr["start"] != rng.From.Format(time.RFC3339) {
		t.Errorf("scope = %v", scope)
	}
	if _, narrowed := scope["instanceIds"]; narrowed {
		t.Errorf("profiles keep no dev instance, but the instance narrowing was sent: %v", scope)
	}
	if req["service"] != "api" || req["profileType"] != "cpu" || req["traceId"] != "0af7651916cd43dd8448eb211c80319c" || req["env"] != "prod" {
		t.Errorf("request = %v", req)
	}
}

// A control plane that predates the env filter answers without echoing it:
// the stacks are every environment's, and the result says so.
func TestCloudFlamegraph_EnvNotApplied(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procGetFlamegraph: flamegraphReply(t, "")}}
	res, err := cloudWith(f).Flamegraph(context.Background(), FlamegraphRequest{Service: "api", SampleType: "cpu", Env: "prod", Range: Range{From: t0c.Add(-time.Hour), To: t0c}})
	if err != nil || res.EnvApplied || len(res.Profile.Stacks) != 2 {
		t.Fatalf("res = %+v, %v; want the stacks, EnvApplied false", res, err)
	}
	// No env, no field: nothing to apply.
	if _, err := cloudWith(f).Flamegraph(context.Background(), FlamegraphRequest{Service: "api", SampleType: "cpu", Range: Range{From: t0c.Add(-time.Hour), To: t0c}}); err != nil {
		t.Fatal(err)
	}
	if _, sent := f.reqs[procGetFlamegraph]["env"]; sent {
		t.Errorf("an empty env was sent: %v", f.reqs[procGetFlamegraph])
	}
}

// NotFound is "no profile in the range", an answer; Unimplemented is an older
// control plane, could-not-look; a service is required.
func TestCloudFlamegraph_Errors(t *testing.T) {
	rng := Range{From: t0c.Add(-time.Hour), To: t0c}
	nf := &fakeCaller{errs: map[string]error{procGetFlamegraph: &cloud.Error{Code: cloud.CodeNotFound, Message: "profile not found"}}}
	res, err := cloudWith(nf).Flamegraph(context.Background(), FlamegraphRequest{Service: "api", SampleType: "cpu", Range: rng})
	if err != nil || len(res.Profile.Stacks) != 0 || res.Profile.Service != "api" {
		t.Errorf("not found = %+v, %v; want an empty profile", res, err)
	}

	un := &fakeCaller{errs: map[string]error{procGetFlamegraph: &cloud.Error{Code: cloud.CodeUnimplemented}}}
	_, err = cloudWith(un).Flamegraph(context.Background(), FlamegraphRequest{Service: "api", SampleType: "cpu", Range: rng})
	var be *Error
	if !errors.As(err, &be) || be.ExitCode() != ExitUndetermined {
		t.Errorf("unimplemented = %v, want could-not-look", err)
	}

	bad := &fakeCaller{replies: map[string]string{procGetFlamegraph: `{"speedscopeJson":"{\"profiles\":[{\"type\":\"evented\"}]}"}`}}
	if _, err := cloudWith(bad).Flamegraph(context.Background(), FlamegraphRequest{Service: "api", SampleType: "cpu", Range: rng}); !errors.As(err, &be) || be.ExitCode() != ExitUndetermined {
		t.Errorf("unreadable answer = %v, want could-not-look", err)
	}

	if _, err := cloudWith(&fakeCaller{}).Flamegraph(context.Background(), FlamegraphRequest{SampleType: "cpu", Range: rng}); err == nil || !strings.Contains(err.Error(), "--service") {
		t.Errorf("no service = %v, want a refusal naming --service", err)
	}
}
