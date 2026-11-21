// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseLabels(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		want    map[string]string
		wantErr string
	}{
		{name: "none", args: nil, want: nil},
		{name: "repeated", args: []string{"pool=pipeline", "gate=yes"}, want: map[string]string{"pool": "pipeline", "gate": "yes"}},
		{name: "comma-separated", args: []string{"pool=pipeline,gate=yes"}, want: map[string]string{"pool": "pipeline", "gate": "yes"}},
		{name: "trailing comma and spaces", args: []string{" pool = pipeline ,"}, want: map[string]string{"pool": "pipeline"}},
		{name: "last value wins", args: []string{"pool=a", "pool=b"}, want: map[string]string{"pool": "b"}},
		{name: "a bare key is not a label", args: []string{"pool=pipeline,gate"}, wantErr: `"gate" is not key=value`},
		{name: "uppercase", args: []string{"Pool=pipeline"}, wantErr: "lowercase slugs"},
		{name: "empty value", args: []string{"pool="}, wantErr: "lowercase slugs"},
		{name: "too long", args: []string{"pool=" + strings.Repeat("a", 64)}, wantErr: "at most 63 characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLabels(tc.args, "--label")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.HasPrefix(err.Error(), "--label: ") {
					t.Fatalf("ParseLabels(%q) error = %v, want one naming --label and containing %q", tc.args, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLabels(%q): %v", tc.args, err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) || (got == nil) != (tc.want == nil) {
				t.Fatalf("ParseLabels(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestValidateLabelsBoundsTheCount(t *testing.T) {
	labels := map[string]string{}
	for i := 0; i < MaxMachineLabels; i++ {
		labels[fmt.Sprintf("k%d", i)] = "v"
	}
	if err := ValidateLabels(labels, "labels"); err != nil {
		t.Fatalf("%d labels refused: %v", MaxMachineLabels, err)
	}
	labels["one-more"] = "v"
	if err := ValidateLabels(labels, "labels"); err == nil {
		t.Fatalf("%d labels accepted", len(labels))
	}
}
