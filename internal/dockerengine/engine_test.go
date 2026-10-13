// Copyright (c) 2025 Reliant Labs
package dockerengine

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stubEngine serves handler on a unix socket, standing in for dockerd. The
// directory comes from os.MkdirTemp rather than t.TempDir because a unix
// socket path is limited to ~104 bytes, which a long test name can exceed.
func stubEngine(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "de")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestDiskUsage_UsesTheCLIAccounting(t *testing.T) {
	raw := `{"LayersSize": 9783382589,
	  "Images": [{"Containers": 1, "Size": 400, "SharedSize": 100}, {"Containers": 0, "Size": 900, "SharedSize": 0}],
	  "Containers": [{"SizeRw": 10, "State": "running"}, {"SizeRw": 5, "State": "exited"}],
	  "Volumes": [{"UsageData": {"Size": 73886027, "RefCount": 0}}, {"UsageData": {"Size": 100, "RefCount": 1}}, {"UsageData": {"Size": -1, "RefCount": 0}}],
	  "BuildCache": [{"Size": 308856101, "InUse": false, "Shared": true}, {"Size": 50, "InUse": false, "Shared": false}]}`
	sock := stubEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/system/df" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, raw)
	})

	u, err := New(Deps{SocketPath: sock}).DiskUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Images in use by a container (400 - 100 shared) are not reclaimable.
	if want := (Category{Bytes: 9783382589, Reclaimable: 9783382589 - 300}); u.Images != want {
		t.Errorf("images: got %+v, want %+v", u.Images, want)
	}
	// Only a stopped container's writable layer is reclaimable.
	if want := (Category{Bytes: 15, Reclaimable: 5}); u.Containers != want {
		t.Errorf("containers: got %+v, want %+v", u.Containers, want)
	}
	// A volume of unknown size (-1) counts as nothing.
	if want := (Category{Bytes: 73886127, Reclaimable: 73886027}); u.Volumes != want {
		t.Errorf("volumes: got %+v, want %+v", u.Volumes, want)
	}
	// A record shared with an image frees nothing when pruned.
	if want := (Category{Bytes: 308856151, Reclaimable: 50}); u.BuildCache != want {
		t.Errorf("build cache: got %+v, want %+v", u.BuildCache, want)
	}
}

func TestDiskUsage_NoSocketIsAnError(t *testing.T) {
	_, err := New(Deps{SocketPath: filepath.Join(t.TempDir(), "absent.sock")}).DiskUsage(context.Background())
	if err == nil {
		t.Fatal("want an error when nothing listens on the socket")
	}
}

func TestDiskUsage_EngineRefusalIsAnError(t *testing.T) {
	sock := stubEngine(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	if _, err := New(Deps{SocketPath: sock}).DiskUsage(context.Background()); err == nil {
		t.Fatal("want an error for a non-200 /system/df")
	}
}

func TestPruneBuildCache_SendsTheAgeFilter(t *testing.T) {
	filters := make(chan string, 1)
	sock := stubEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/build/prune" {
			http.NotFound(w, r)
			return
		}
		filters <- r.URL.Query().Get("filters")
		_, _ = io.WriteString(w, `{"SpaceReclaimed": 1234}`)
	})

	freed, err := New(Deps{SocketPath: sock}).PruneBuildCache(context.Background(), 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if freed != 1234 {
		t.Errorf("freed = %d, want 1234", freed)
	}
	if got, want := <-filters, `{"until":["2h0m0s"]}`; got != want {
		t.Errorf("filters = %s, want %s", got, want)
	}
}
