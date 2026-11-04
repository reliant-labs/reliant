package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func convergePolicyPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "storage.json")
}

func completeFacts() Facts {
	return Facts{
		Project:      "/work/proj-a",
		Contexts:     []string{"k3d-control-plane"},
		Registry:     "k3d-cp-registry",
		Aliases:      []string{"localhost:5051", "k3d-cp-registry:5000"},
		Repositories: []string{"localhost:5051/admin-server"},
		Pins:         []string{"sha256:" + repeat64('a')},
	}
}

func repeat64(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

// machinePolicyStandIn redirects os.UserConfigDir to a temp HOME, so the path
// DefaultPath resolves to — the developer's real machine policy outside a test
// — is a file this test owns and can inspect.
func machinePolicyStandIn(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// os.UserConfigDir reads %AppData% on Windows, not HOME.
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("FORGE_STORAGE_POLICY", "")
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, home) {
		t.Fatalf("DefaultPath() = %s; the stand-in HOME %s did not take effect", path, home)
	}
	return path
}

// TestMachinePolicyIsNeverWrittenUnderTest pins the guard on the accident the
// zero-touch activation would otherwise cause. Converge runs as a side effect
// of `forge build`, a release cut and the cluster phase, so every cli test
// reaching one of those wrote the developer's REAL storage.json. Measured on
// one machine: 65 `TestBuildTag_*/001` temp projects and fake pins
// (sha256:1111…, sha256:2222…) in ~/Library/Application Support/forge/
// storage.json — and Policy.Projects is the set the Logs layer expires files
// under.
//
// A test cannot be expected to remember to scope the policy, so the refusal is
// at the write: the machine-default path is not writable under `go test`
// unless FORGE_STORAGE_POLICY names it explicitly.
func TestMachinePolicyIsNeverWrittenUnderTest(t *testing.T) {
	path := machinePolicyStandIn(t)

	// Every path that mutates the policy or creates files beside it: the
	// converge touch points, the manual register commands, the GC stamp, and
	// the maintenance lock (which would otherwise mkdir the directory and
	// leave a .lock file behind even when nothing else is written).
	writers := map[string]func() error{
		"Converge":        func() error { return Converge(path, completeFacts()) },
		"Save":            func() error { return Save(path, DefaultPolicy()) },
		"RecordFullGC":    func() error { return RecordFullGC(path, NewGCResult(time.Now(), nil)) },
		"RecordAutoGC":    func() error { return RecordAutoGC(path, NewGCResult(time.Now(), nil)) },
		"RegisterProject": func() error { return RegisterProject(path, t.TempDir()) },
		"WithLock":        func() error { return WithLock(path, func() error { return nil }) },
	}
	for name, write := range writers {
		err := write()
		if !errors.Is(err, ErrMachinePolicyUnderTest) {
			t.Errorf("%s on the machine-default policy under test: err = %v, want ErrMachinePolicyUnderTest", name, err)
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) > 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("a test wrote the machine policy directory %s: %v", filepath.Dir(path), names)
	}
}

// TestMachinePolicyGuardHonorsAnExplicitPath is the other half: the guard must
// not disable the layer. A test that sets FORGE_STORAGE_POLICY — even to the
// very path the default would resolve to — has scoped it, and writes land.
func TestMachinePolicyGuardHonorsAnExplicitPath(t *testing.T) {
	path := machinePolicyStandIn(t)
	t.Setenv("FORGE_STORAGE_POLICY", path)
	if err := Converge(path, completeFacts()); err != nil {
		t.Fatalf("Converge on an explicitly named policy: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("explicitly named policy was not written: %v", err)
	}

	other := convergePolicyPath(t) // any non-default path is always writable
	if err := Converge(other, completeFacts()); err != nil {
		t.Fatalf("Converge on a non-default path: %v", err)
	}
}

// TestConvergeRecordsCompleteRegistry is the activation claim: a command that
// knows all the facts registers the registry with no manual step.
func TestConvergeRecordsCompleteRegistry(t *testing.T) {
	path := convergePolicyPath(t)
	if err := Converge(path, completeFacts()); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Registries) != 1 {
		t.Fatalf("registries = %+v; want exactly one", p.Registries)
	}
	reg := p.Registries[0]
	if reg.Container != "k3d-cp-registry" {
		t.Errorf("container = %q; want k3d-cp-registry", reg.Container)
	}
	// The host is stripped: retention works in repository paths.
	if len(reg.Repositories) != 1 || reg.Repositories[0] != "admin-server" {
		t.Errorf("repositories = %v; want [admin-server] (alias host stripped)", reg.Repositories)
	}
	if !contains(reg.Contexts, "k3d-control-plane") {
		t.Errorf("contexts = %v; want the declared k3d context", reg.Contexts)
	}
	if !contains(p.Clusters, "k3d-control-plane") || len(p.Pins) != 1 {
		t.Errorf("clusters = %v pins = %v; want both converged", p.Clusters, p.Pins)
	}
	if want, _ := filepath.Abs("/work/proj-a"); !contains(p.Projects, want) {
		t.Errorf("projects = %v; want the absolute project dir", p.Projects)
	}
}

// TestConvergeIsIdempotent pins that a warm run neither duplicates entries nor
// rewrites the file — Converge runs on every `forge env up` / `forge build`,
// so a converge that churned the policy would be a converge nobody could call
// from a hot path.
func TestConvergeIsIdempotent(t *testing.T) {
	path := convergePolicyPath(t)
	if err := Converge(path, completeFacts()); err != nil {
		t.Fatalf("first Converge: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat policy: %v", err)
	}
	// A coarse-grained mtime would make "not rewritten" untestable, so age
	// the file and assert the timestamp is unchanged.
	old := info.ModTime().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := Converge(path, completeFacts()); err != nil {
			t.Fatalf("repeat Converge %d: %v", i, err)
		}
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read policy: %v", err)
	}
	if string(again) != string(first) {
		t.Fatalf("converge churned the policy:\nfirst:\n%s\nagain:\n%s", first, again)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("re-stat policy: %v", err)
	}
	if !info.ModTime().Equal(old) {
		t.Fatalf("unchanged converge rewrote the file (mtime moved to %v)", info.ModTime())
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Registries) != 1 || len(p.Registries[0].Repositories) != 1 ||
		len(p.Clusters) != 1 || len(p.Pins) != 1 || len(p.Projects) != 1 {
		t.Fatalf("repeat converge duplicated entries: %+v", p)
	}
}

// TestConvergeNeverRemovesAnotherProjectsEntries is the multi-project
// invariant. Several checkouts on one machine converge into one policy; a
// project that no longer uses a context must not un-protect it for the
// project that still does.
func TestConvergeNeverRemovesAnotherProjectsEntries(t *testing.T) {
	path := convergePolicyPath(t)
	if err := Converge(path, completeFacts()); err != nil {
		t.Fatalf("project A Converge: %v", err)
	}
	// A different project, a different cluster, a different repository in the
	// SAME registry, and no overlap in pins.
	other := Facts{
		Project:      "/work/proj-b",
		Contexts:     []string{"k3d-cp-daemon"},
		Registry:     "k3d-cp-registry",
		Aliases:      []string{"localhost:5051"},
		Repositories: []string{"localhost:5051/reliant-api-server"},
		Pins:         []string{"sha256:" + repeat64('b')},
	}
	if err := Converge(path, other); err != nil {
		t.Fatalf("project B Converge: %v", err)
	}

	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Registries) != 1 {
		t.Fatalf("registries = %+v; want one merged entry", p.Registries)
	}
	reg := p.Registries[0]
	for _, repo := range []string{"admin-server", "reliant-api-server"} {
		if !contains(reg.Repositories, repo) {
			t.Errorf("repositories = %v; lost %q", reg.Repositories, repo)
		}
	}
	for _, c := range []string{"k3d-control-plane", "k3d-cp-daemon"} {
		if !contains(reg.Contexts, c) {
			t.Errorf("contexts = %v; lost %q — deleting images a registered cluster still pulls", reg.Contexts, c)
		}
		if !contains(p.Clusters, c) {
			t.Errorf("clusters = %v; lost %q", p.Clusters, c)
		}
	}
	if !contains(reg.Aliases, "k3d-cp-registry:5000") {
		t.Errorf("aliases = %v; project B's narrower alias set clobbered A's", reg.Aliases)
	}
	if len(p.Pins) != 2 || len(p.Projects) != 2 {
		t.Errorf("pins = %v projects = %v; want both projects' entries", p.Pins, p.Projects)
	}
}

// TestConvergeWithholdsIncompleteRegistry is the safety gate. A registry whose
// protecting contexts (or repositories, or aliases) are not yet known must not
// be recorded: retention would then delete against an unknown protected set.
// The other half of the facts still converges.
func TestConvergeWithholdsIncompleteRegistry(t *testing.T) {
	for name, f := range map[string]Facts{
		"no contexts": {
			Registry: "k3d-cp-registry", Aliases: []string{"localhost:5051"},
			Repositories: []string{"localhost:5051/admin-server"},
		},
		"no repositories": {
			Registry: "k3d-cp-registry", Aliases: []string{"localhost:5051"},
			Contexts: []string{"k3d-control-plane"},
		},
		"no aliases": {
			Registry: "k3d-cp-registry", Contexts: []string{"k3d-control-plane"},
			Repositories: []string{"localhost:5051/admin-server"},
		},
		"repository host is not an alias of this registry": {
			Registry: "k3d-cp-registry", Aliases: []string{"localhost:5051"},
			Contexts: []string{"k3d-control-plane"}, Repositories: []string{"ghcr.io/acme/api"},
		},
		"registry is not a local k3d container": {
			Registry: "ghcr.io", Aliases: []string{"ghcr.io"},
			Contexts: []string{"k3d-control-plane"}, Repositories: []string{"ghcr.io/acme/api"},
		},
		"context is not local": {
			Registry: "k3d-cp-registry", Aliases: []string{"localhost:5051"},
			Contexts: []string{"gke_proj_us-central1_prod"}, Repositories: []string{"localhost:5051/admin-server"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := convergePolicyPath(t)
			if err := Converge(path, f); err != nil {
				t.Fatalf("Converge: %v", err)
			}
			p, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(p.Registries) != 0 {
				t.Fatalf("recorded an incomplete registry: %+v", p.Registries)
			}
		})
	}
}

// TestConvergeCompletesAcrossCalls is how activation actually happens: the
// cluster phase knows the contexts and aliases, the build later adds the
// repositories, and only then does the registry become a cleanup target.
func TestConvergeCompletesAcrossCalls(t *testing.T) {
	path := convergePolicyPath(t)
	clusterPhase := Facts{
		Project:  "/work/proj-a",
		Contexts: []string{"k3d-control-plane"},
		Registry: "k3d-cp-registry",
		Aliases:  []string{"localhost:5051", "k3d-cp-registry:5000"},
	}
	if err := Converge(path, clusterPhase); err != nil {
		t.Fatalf("cluster-phase Converge: %v", err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Registries) != 0 {
		t.Fatalf("registry recorded before any repository was known: %+v", p.Registries)
	}
	if !contains(p.Clusters, "k3d-control-plane") {
		t.Fatalf("clusters = %v; the known half should still converge", p.Clusters)
	}

	build := Facts{Registry: "k3d-cp-registry", Aliases: clusterPhase.Aliases,
		Repositories: []string{"localhost:5051/admin-server"}}
	if err := Converge(path, build); err != nil {
		t.Fatalf("build Converge: %v", err)
	}
	p, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Registries) != 1 || !contains(p.Registries[0].Contexts, "k3d-control-plane") {
		t.Fatalf("registries = %+v; want one complete entry carrying the earlier context", p.Registries)
	}
}

// TestConvergeKeepsPolicyValid guards the interaction between the converge
// path and Save's validation: anything Converge writes must round-trip.
func TestConvergeKeepsPolicyValid(t *testing.T) {
	path := convergePolicyPath(t)
	if err := Converge(path, completeFacts()); err != nil {
		t.Fatalf("Converge: %v", err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("converged policy does not validate: %v", err)
	}
}

// TestGCRecordsAreBesideThePolicyAndTolerateCorruption pins the opportunistic
// pass's gate: a missing record reads as "never attempted" (so the first run
// is eligible), and a corrupt one must never fail the caller's command.
func TestGCRecordsAreBesideThePolicyAndTolerateCorruption(t *testing.T) {
	path := convergePolicyPath(t)
	if _, ok := LastAutoGC(path); ok {
		t.Fatal("an attempt is recorded before any pass")
	}
	for _, record := range []string{AutoGCRecordPath(path), FullGCRecordPath(path)} {
		if filepath.Dir(record) != filepath.Dir(path) {
			t.Fatalf("record %q is not beside the policy", record)
		}
		if err := os.WriteFile(record, []byte("{not json"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := LastAutoGC(path); ok {
		t.Fatal("a corrupt attempt record was read as an attempt")
	}
	if _, ok := LastFullGC(path); ok {
		t.Fatal("a corrupt full-GC record was read as a pass")
	}
}

// TestRegistryAliasesCoverHostAndInNetworkNames pins the alias set retention
// matches repositories against. A name missing here makes images pushed under
// it look unowned.
func TestRegistryAliasesCoverHostAndInNetworkNames(t *testing.T) {
	got := RegistryAliases("k3d-cp-registry", "5051")
	for _, want := range []string{
		"localhost:5051", "127.0.0.1:5051", "k3d-cp-registry:5000",
		"cp-registry.localhost:5051", "registry.localhost:5000", "registry.localhost:5051",
	} {
		if !contains(got, want) {
			t.Errorf("aliases %v missing %q", got, want)
		}
	}
	// No host port known: the in-network name is still an alias.
	noPort := RegistryAliases("k3d-cp-registry", "")
	if !contains(noPort, "k3d-cp-registry:5000") {
		t.Errorf("aliases without a host port = %v; want the in-network name", noPort)
	}
	for _, unwanted := range noPort {
		if unwanted == "localhost:" {
			t.Errorf("aliases without a host port produced a malformed entry: %v", noPort)
		}
	}
}
