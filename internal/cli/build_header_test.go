package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestPrintBuildHeader pins the conditional lines of the block runBuild opens
// with. The tag line is the one that matters: it names the tag an image is
// pushed as and why, so it must appear exactly when an image is built with a
// resolved tag, and never as an empty "Tag:" on a binary-only build.
func TestPrintBuildHeader(t *testing.T) {
	push := pushPlan{push: true, destinations: []imageDestination{{repository: "ghcr.io/acme/shop", workload: "shop"}}}
	entities := &KCLEntities{Frontends: []FrontendEntity{{Name: "web"}}}

	tests := []struct {
		name        string
		opts        buildOptions
		resolvedTag string
		push        pushPlan
		entities    *KCLEntities
		want        []string
		wantAbsent  []string
	}{
		{
			name:        "docker build in an env prints env, tag, push target and KCL plan",
			opts:        buildOptions{outputDir: "bin", buildTarget: "all", buildDocker: true, env: "staging"},
			resolvedTag: "v1.2.3",
			push:        push,
			entities:    entities,
			want: []string{
				"[build] Building project: shop\n",
				"[build]   Output:   bin\n",
				"[build]   Target:   all\n",
				"[build]   Docker:   true\n",
				"[build]   Env:      staging\n",
				"[build]   Tag:      v1.2.3 (explicit --tag flag)\n",
				`[build]   Image:    ghcr.io/acme/shop (declared by workload "shop"; pushed)`,
				"Frontends (skip docker):",
			},
		},
		{
			name:        "binary-only build prints no tag even when one resolved, and no env line without an env",
			opts:        buildOptions{outputDir: "bin", buildTarget: "all"},
			resolvedTag: "v1.2.3",
			want:        []string{"[build]   Docker:   false\n"},
			wantAbsent:  []string{"Tag:", "Env:", "Image:", "Frontends"},
		},
		{
			name:       "docker build with no resolved tag prints no empty tag line",
			opts:       buildOptions{outputDir: "bin", buildTarget: "all", buildDocker: true},
			want:       []string{"[build]   Docker:   true\n"},
			wantAbsent: []string{"Tag:"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() {
				printBuildHeader("shop", tc.opts, tc.resolvedTag, "explicit --tag flag", tc.push, tc.entities)
			})
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("header missing %q\n--- got ---\n%s", w, out)
				}
			}
			for _, a := range tc.wantAbsent {
				if strings.Contains(out, a) {
					t.Errorf("header must not contain %q\n--- got ---\n%s", a, out)
				}
			}
			if !strings.HasSuffix(out, "\n\n") {
				t.Errorf("header must end with a blank separator line\n--- got ---\n%q", out)
			}
		})
	}
}

// TestPartitionBuildResults pins that the split keeps each side in build
// order: the summary prints it, and the build-state writers read succeeded.
func TestPartitionBuildResults(t *testing.T) {
	boom := errors.New("boom")
	results := []buildResult{
		{name: "api", kind: "service"},
		{name: "web", kind: "frontend", err: boom},
		{name: "shop", kind: "docker"},
		{name: "worker", kind: "service", err: boom},
	}

	succeeded, failed := partitionBuildResults(results)

	names := func(rs []buildResult) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.name)
		}
		return out
	}
	if got, want := names(succeeded), []string{"api", "shop"}; !reflect.DeepEqual(got, want) {
		t.Errorf("succeeded = %v, want %v", got, want)
	}
	if got, want := names(failed), []string{"web", "worker"}; !reflect.DeepEqual(got, want) {
		t.Errorf("failed = %v, want %v", got, want)
	}

	if s, f := partitionBuildResults(nil); s != nil || f != nil {
		t.Errorf("partitionBuildResults(nil) = %v, %v; want nil, nil", s, f)
	}
}
