package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const kmsgMountServer = "/cache/forge/k3d/kmsg:/dev/kmsg@server:*"

func stubKmsgArgs(t *testing.T, missing bool) {
	t.Helper()
	orig := addK3dKmsgArgsFn
	t.Cleanup(func() { addK3dKmsgArgsFn = orig })
	addK3dKmsgArgsFn = func(args []string) ([]string, error) {
		if !missing {
			return args, nil
		}
		return append(args, "--volume", kmsgMountServer), nil
	}
}

// Every forge path that creates a k3d cluster must route its argv through the
// /dev/kmsg seam: a host without the device boots a cluster whose k3s dies in
// two seconds, which --wait reports only as a timeout.
func TestCreateK3dCluster_MountsKmsgWhenHostLacksIt(t *testing.T) {
	for name, missing := range map[string]bool{"host without /dev/kmsg": true, "host with /dev/kmsg": false} {
		t.Run(name, func(t *testing.T) {
			s := stubCreateSeams(t, false, nil, nil)
			stubKmsgArgs(t, missing)
			if err := createK3dCluster(t.Context(), "x", []string{"cluster", "create", "x"}); err != nil {
				t.Fatalf("createK3dCluster: %v", err)
			}
			if got := slices.Contains(s.runs[0], kmsgMountServer); got != missing {
				t.Fatalf("argv %v: kmsg mount present=%v, want %v", s.runs[0], got, missing)
			}
		})
	}
}

func TestDevClusterCreateArgs_MountsKmsgWhenHostLacksIt(t *testing.T) {
	orig := addClusterStorageArgsFn
	t.Cleanup(func() { addClusterStorageArgsFn = orig })
	addClusterStorageArgsFn = func(args []string) ([]string, error) { return args, nil }
	stubKmsgArgs(t, true)

	for name, withConfig := range map[string]bool{"with deploy/k3d.yaml": true, "without deploy/k3d.yaml": false} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			if withConfig {
				if err := os.MkdirAll("deploy", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join("deploy", "k3d.yaml"), []byte("apiVersion: k3d.io/v1alpha5\nkind: Simple\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			args, cleanup, err := devClusterCreateArgs()
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if !slices.Contains(args, kmsgMountServer) {
				t.Fatalf("argv %v lacks the /dev/kmsg mount", args)
			}
		})
	}
}
