package kclplugin

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"testing"
)

func TestPortResolver_StablePerName(t *testing.T) {
	r := NewPortResolver()
	p1, err := r.Resolve("reliant-web", 0)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := r.Resolve("reliant-web", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Errorf("same name returned different ports: %d != %d", p1, p2)
	}
}

func TestPortResolver_DistinctNamesDistinctPorts(t *testing.T) {
	r := NewPortResolver()
	seen := map[int]string{}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		p, err := r.Resolve(name, 0)
		if err != nil {
			t.Fatal(err)
		}
		if prev, ok := seen[p]; ok {
			t.Errorf("port %d handed to both %q and %q", p, prev, name)
		}
		seen[p] = name
	}
}

func TestPortResolver_PrefersFreePreferred(t *testing.T) {
	r := NewPortResolver()
	// Find a currently-free port to use as the "preferred" value.
	free, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve("svc", free)
	if err != nil {
		t.Fatal(err)
	}
	if got != free {
		t.Errorf("expected preferred free port %d, got %d", free, got)
	}
}

func TestPortResolver_ScansNearPreferredWhenBusy(t *testing.T) {
	// Occupy a free port so `preferred` is taken; the resolver should land
	// just above it (human-friendly), not jump to a random OS port.
	base, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", fmt.Sprintf("localhost:%d", base))
	if err != nil {
		t.Skipf("could not occupy base port %d: %v", base, err)
	}
	defer ln.Close()

	r := NewPortResolver()
	got, err := r.Resolve("svc", base)
	if err != nil {
		t.Fatal(err)
	}
	if got <= base || got >= base+scanWindow {
		t.Errorf("expected a port just above busy %d (within scan window), got %d", base, got)
	}
}

func TestPortResolver_PersistsAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	r1 := NewPersistentPortResolver(path)
	p1, err := r1.Resolve("reliant-web", 0)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh resolver loading the same store should reuse the port.
	r2 := NewPersistentPortResolver(path)
	p2, err := r2.Resolve("reliant-web", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p2 != p1 {
		t.Errorf("expected reused port %d across runs, got %d", p1, p2)
	}
}

func TestPortResolver_PreferredAlreadyClaimedFallsBack(t *testing.T) {
	r := NewPortResolver()
	free, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.Resolve("a", free)
	if err != nil {
		t.Fatal(err)
	}
	// Second name asks for the same preferred — must NOT collide.
	second, err := r.Resolve("b", free)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Errorf("two names got the same port %d despite claim", first)
	}
}
