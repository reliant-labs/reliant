package crud

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/orm"
)

func TestMaskTouches(t *testing.T) {
	cases := []struct {
		name   string
		paths  []string
		column string
		want   bool
	}{
		{"nil mask is a full replace", nil, "spec", true},
		{"empty mask is a full replace", []string{}, "spec", true},
		{"blank entries only is a full replace", []string{"", ""}, "spec", true},
		{"wildcard is a full replace", []string{"name", "*"}, "spec", true},
		{"exact column", []string{"name", "spec"}, "spec", true},
		{"nested path under the column", []string{"spec.task_ttl"}, "spec", true},
		{"deeply nested path", []string{"spec.limits.cpu"}, "spec", true},
		{"other column", []string{"name"}, "spec", false},
		{"shared prefix is not a touch", []string{"spec_version"}, "spec", false},
		{"column is a nested segment, not the root", []string{"meta.spec"}, "spec", false},
		{"blank entries beside a real path", []string{"", "name"}, "spec", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MaskTouches(tc.paths, tc.column); got != tc.want {
				t.Errorf("MaskTouches(%q, %q) = %v, want %v", tc.paths, tc.column, got, tc.want)
			}
		})
	}
}

// MaskTouches must agree with the write Update actually dispatches: for
// every mask, a column the update writes (every column on a full replace,
// the named ones on a masked write) is one MaskTouches reports as touched.
// A validation hook built on it then cannot skip a column the write lands.
func TestMaskTouches_AgreesWithUpdateDispatch(t *testing.T) {
	masks := [][]string{nil, {}, {""}, {"*"}, {"email"}, {"name", "email"}, {"name", "", "*"}}
	for _, mask := range masks {
		var full bool
		var masked []string
		h := HandleUpdate(UpdateOp[updateReq, updateResp, *user]{
			EntityLower:    "user",
			EntityFieldLow: "user",
			Entity:         func(_ context.Context, r *updateReq) (*user, error) { return r.User, entityOrRequired(r.User) },
			Mask:           func(r *updateReq) []string { return r.Mask },
			Persist: func(context.Context, *user, ...orm.QueryOption) error {
				full = true
				return nil
			},
			PersistMasked: func(_ context.Context, _ *user, fields []string, _ ...orm.QueryOption) error {
				masked = fields
				return nil
			},
			Pack: func(u *user) (*updateResp, error) { return &updateResp{User: u}, nil },
		})
		if _, err := h(context.Background(), connect.NewRequest(&updateReq{User: &user{ID: "u1"}, Mask: mask})); err != nil {
			t.Fatalf("mask %q: unexpected err: %v", mask, err)
		}
		for _, col := range []string{"name", "email"} {
			writes := full || slices.Contains(masked, col)
			if got := MaskTouches(mask, col); got != writes {
				t.Errorf("mask %q, column %q: MaskTouches = %v, but Update wrote it = %v (full=%v masked=%q)",
					mask, col, got, writes, full, masked)
			}
		}
	}
}
