// Copyright (c) 2025 Reliant Labs
package repo

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestCheckoutWaves(t *testing.T) {
	repos := func(paths ...string) []*core.Repo {
		out := make([]*core.Repo, len(paths))
		for i, p := range paths {
			out[i] = &core.Repo{RelativePath: p}
		}
		return out
	}

	for _, tc := range []struct {
		name  string
		paths []string
		want  [][]int
	}{
		{"single root repo", []string{""}, [][]int{{0}}},
		{"siblings run together", []string{"control-plane", "forge", "reliant"}, [][]int{{0, 1, 2}}},
		{"root before nested", []string{"", "a", "b"}, [][]int{{0}, {1, 2}}},
		{"dot is the root too", []string{".", "a"}, [][]int{{0}, {1}}},
		{"chain", []string{"", "a", "a/b"}, [][]int{{0}, {1}, {2}}},
		{"prefix is not containment", []string{"app", "app-web"}, [][]int{{0, 1}}},
		{"deepest container wins", []string{"a/b", "", "a"}, [][]int{{1}, {2}, {0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, CheckoutWaves(repos(tc.paths...)))
		})
	}
}
