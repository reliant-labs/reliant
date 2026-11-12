package goexec

import (
	"reflect"
	"testing"
)

func TestEnvFrom_ScrubsOnlyModMod(t *testing.T) {
	for _, tc := range []struct {
		name  string
		base  []string
		extra []string
		want  []string
	}{
		{
			name: "a lone -mod=mod drops GOFLAGS entirely",
			base: []string{"HOME=/h", "GOFLAGS=-mod=mod", "PATH=/bin"},
			want: []string{"HOME=/h", "PATH=/bin"},
		},
		{
			name: "every other flag survives, in order",
			base: []string{"GOFLAGS=-tags=integration -mod=mod -trimpath"},
			want: []string{"GOFLAGS=-tags=integration -trimpath"},
		},
		{
			name: "the double-dash spelling cmd/go also accepts",
			base: []string{"GOFLAGS=--mod=mod -buildvcs=false"},
			want: []string{"GOFLAGS=-buildvcs=false"},
		},
		{
			// vendor and readonly never write go.mod or go.sum.
			name: "-mod values that cannot edit the module files are kept",
			base: []string{"GOFLAGS=-mod=vendor"},
			want: []string{"GOFLAGS=-mod=vendor"},
		},
		{
			name: "no GOFLAGS, nothing changes",
			base: []string{"HOME=/h", "GOWORK=off"},
			want: []string{"HOME=/h", "GOWORK=off"},
		},
		{
			// What forge or a project's declared build env asks for is a
			// decision, not a leak: it is appended after the scrub and wins.
			name:  "extra is appended verbatim and is not scrubbed",
			base:  []string{"GOFLAGS=-mod=mod"},
			extra: []string{"GOWORK=off", "GOFLAGS=-mod=mod"},
			want:  []string{"GOWORK=off", "GOFLAGS=-mod=mod"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := envFrom(tc.base, tc.extra...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("envFrom(%q, %q) = %q, want %q", tc.base, tc.extra, got, tc.want)
			}
		})
	}
}

// Env must not reorder or edit the caller's slice in place: exec.Cmd.Env
// values are routinely built by appending to a shared base.
func TestEnvFrom_DoesNotMutateBase(t *testing.T) {
	base := []string{"GOFLAGS=-mod=mod -tags=x", "A=1"}
	_ = envFrom(base, "B=2")
	if base[0] != "GOFLAGS=-mod=mod -tags=x" || base[1] != "A=1" {
		t.Errorf("base was mutated: %q", base)
	}
}
