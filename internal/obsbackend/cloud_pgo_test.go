package obsbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

// The request carries the env-bound scope with the window in timeRange (as
// GetFlamegraph's), the services and the env name; the response's int64 is a
// JSON string and its bytes base64, as protojson writes them.
func TestCloudExportPGOProfile(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procExportPGOProfile: `{"pprof":"AQID","edgeCount":2,"totalWeight":"12000000000",` +
		`"oldest":"2026-10-21T10:00:00Z","newest":"2026-10-22T10:00:00Z"}`}}
	c := cloudWith(f)
	c.Instances = Instances{IDs: []string{"inst-1"}}
	res, err := c.ExportPGOProfile(context.Background(), ExportPGORequest{
		Services: []string{"api", "worker"}, Namespace: "shop", Env: "prod", Range: Range{From: t0c.Add(-168 * time.Hour), To: t0c},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Pprof) != "\x01\x02\x03" || res.EdgeCount != 2 || res.TotalWeight != 12e9 ||
		!res.Newest.Equal(time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)) || res.Oldest.IsZero() {
		t.Errorf("result = %+v", res)
	}
	req := f.reqs[procExportPGOProfile]
	scope := req["scope"].(map[string]any)
	tr := scope["timeRange"].(map[string]any)
	if scope["orgId"] != "org-1" || fmt.Sprint(scope["envIds"]) != "[env-1]" ||
		tr["start"] != t0c.Add(-168*time.Hour).Format(time.RFC3339) || tr["end"] != t0c.Format(time.RFC3339) {
		t.Errorf("scope = %v", scope)
	}
	if _, narrowed := scope["instanceIds"]; narrowed {
		t.Errorf("a PGO export is a whole-env read, but the instance narrowing was sent: %v", scope)
	}
	if fmt.Sprint(req["services"]) != "[api worker]" || req["env"] != "prod" || req["serviceNamespace"] != "shop" {
		t.Errorf("request = %v", req)
	}
}

// The answer is read only up to MaxPGOResponseBytes: one over it is refused,
// and so is one the client cut off at that limit, each naming the fix.
func TestCloudExportPGOProfile_ResponseBound(t *testing.T) {
	rng := Range{From: t0c.Add(-time.Hour), To: t0c}
	huge := `{"pprof":"` + strings.Repeat("A", MaxPGOResponseBytes) + `","edgeCount":1}`
	cut := fmt.Errorf("decode %s response: %w", procExportPGOProfile, &json.SyntaxError{Offset: MaxPGOResponseBytes})
	for name, f := range map[string]*fakeCaller{
		"over the limit": {replies: map[string]string{procExportPGOProfile: huge}},
		"cut off":        {errs: map[string]error{procExportPGOProfile: cut}},
	} {
		_, err := cloudWith(f).ExportPGOProfile(context.Background(), ExportPGORequest{Services: []string{"api"}, Range: rng})
		var be *Error
		if !errors.As(err, &be) || be.ExitCode() != ExitUndetermined || !strings.Contains(err.Error(), "--since") || !strings.Contains(err.Error(), "--binary") {
			t.Errorf("%s: %v, want an undetermined error naming --since and --binary", name, err)
		}
	}
	// No namespace: the field is left out, for a control plane that predates it.
	f := &fakeCaller{replies: map[string]string{procExportPGOProfile: `{}`}}
	if _, err := cloudWith(f).ExportPGOProfile(context.Background(), ExportPGORequest{Services: []string{"api"}, Range: rng}); err != nil {
		t.Fatal(err)
	}
	if _, sent := f.reqs[procExportPGOProfile]["serviceNamespace"]; sent {
		t.Errorf("an empty namespace was sent: %v", f.reqs[procExportPGOProfile])
	}
}

// An empty window is an answer, not an error.
func TestCloudExportPGOProfile_Empty(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procExportPGOProfile: `{}`}}
	res, err := cloudWith(f).ExportPGOProfile(context.Background(), ExportPGORequest{Services: []string{"api"}, Range: Range{From: t0c.Add(-time.Hour), To: t0c}})
	if err != nil || res.EdgeCount != 0 || res.TotalWeight != 0 || !res.Newest.IsZero() {
		t.Fatalf("empty = %+v, %v", res, err)
	}
}

func TestCloudExportPGOProfile_Refusals(t *testing.T) {
	rng := Range{From: t0c.Add(-time.Hour), To: t0c}
	many := make([]string, MaxPGOServices+1)
	for i := range many {
		many[i] = fmt.Sprintf("svc-%d", i)
	}
	for _, tc := range []struct {
		name     string
		services []string
		err      error
		want     string
		code     int
	}{
		{name: "older control plane", services: []string{"api"}, err: &cloud.Error{HTTPStatus: 404, Code: cloud.CodeUnimplemented},
			want: "does not serve PGO profiles yet (ExportPGOProfile is unimplemented)", code: ExitUndetermined},
		{name: "unreachable", services: []string{"api"}, err: errors.New("dial tcp: connection refused"),
			want: "could not be reached", code: ExitUndetermined},
		{name: "too many services", services: many, want: "merges at most 50", code: ExitWrong},
		{name: "no services", want: "no services named", code: ExitWrong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeCaller{errs: map[string]error{procExportPGOProfile: tc.err}}
			_, err := cloudWith(f).ExportPGOProfile(context.Background(), ExportPGORequest{Services: tc.services, Range: rng})
			var be *Error
			if !errors.As(err, &be) || be.ExitCode() != tc.code || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v (code %v), want code %d containing %q", err, be, tc.code, tc.want)
			}
		})
	}
}
