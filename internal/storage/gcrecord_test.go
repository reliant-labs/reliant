package storage

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGCResultNamesFailedLayers(t *testing.T) {
	err := errors.Join(
		layerErr("logs", fmt.Errorf("permission denied")),
		fmt.Errorf("wrapped: %w", layerErr("registry k3d-reg", fmt.Errorf("protected set unavailable"))),
	)
	r := NewGCResult(time.Now(), err)
	if r.OK || strings.Join(r.FailedLayers, ",") != "logs,registry k3d-reg" {
		t.Fatalf("result = %+v", r)
	}
	if !strings.Contains(r.Summary(), "registry k3d-reg") {
		t.Fatalf("summary does not name the failed layer: %s", r.Summary())
	}
	if ok := NewGCResult(time.Now(), nil); !ok.OK || ok.Summary() != "succeeded" {
		t.Fatalf("success = %+v", ok)
	}
}

func TestGCRecordsRoundTripSeparately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	if _, ok := LastFullGC(path); ok {
		t.Fatal("a record exists before any pass")
	}
	at := time.Now().Truncate(time.Second)
	if err := RecordAutoGC(path, NewGCResult(at, nil)); err != nil {
		t.Fatal(err)
	}
	if _, ok := LastFullGC(path); ok {
		t.Fatal("an opportunistic pass produced a full-GC record")
	}
	if err := RecordFullGC(path, NewGCResult(at, layerErr("registry k3d-reg", fmt.Errorf("x")))); err != nil {
		t.Fatal(err)
	}
	got, ok := LastFullGC(path)
	if !ok || got.OK || !got.At.Equal(at) {
		t.Fatalf("full record = %+v ok=%v", got, ok)
	}
}

func TestFullGCProblem(t *testing.T) {
	now := time.Now()
	withRegistry := DefaultPolicy()
	withRegistry.Registries = []Registry{{Container: "k3d-reg"}}
	for _, tc := range []struct {
		name   string
		p      Policy
		last   GCResult
		ok     bool
		expect string
	}{
		{"no registries: never a problem", DefaultPolicy(), GCResult{}, false, ""},
		{"never ran", withRegistry, GCResult{}, false, "no full storage GC has ever completed"},
		{"last pass failed", withRegistry, GCResult{At: now.Add(-2 * time.Hour), FailedLayers: []string{"registry k3d-reg"}}, true, "failed in registry k3d-reg"},
		{"stale success", withRegistry, GCResult{At: now.Add(-72 * time.Hour), OK: true}, true, "3d ago"},
		{"recent success", withRegistry, GCResult{At: now.Add(-2 * time.Hour), OK: true}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FullGCProblem(tc.p, tc.last, tc.ok, now)
			if tc.expect == "" && got != "" || tc.expect != "" && !strings.Contains(got, tc.expect) {
				t.Fatalf("FullGCProblem = %q, want %q", got, tc.expect)
			}
		})
	}
}
