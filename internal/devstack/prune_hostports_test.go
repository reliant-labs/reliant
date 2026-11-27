package devstack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestBlockReservationMatchesExactlyItsKey(t *testing.T) {
	cases := []struct {
		name, key string
		want      bool
	}{
		{"block:wt-dead:21000", "wt-dead", true},
		{"block:wt-dead:3000", "wt-dead", true},
		{"block:wt-dead-2:21000", "wt-dead", false}, // a longer key sharing the prefix
		{"block:prod-wt-dead:21000", "wt-dead", false},
		{"block:wt-dead:web", "wt-dead", false}, // not a base port
		{"/src/wt-dead:dev:web", "wt-dead", false},
	}
	for _, tc := range cases {
		if got := blockReservation(tc.name, tc.key); got != tc.want {
			t.Errorf("blockReservation(%q, %q) = %v, want %v", tc.name, tc.key, got, tc.want)
		}
	}
}

func writeHostPorts(t *testing.T, projectDir string, ports map[string]int) {
	t.Helper()
	path := HostPortsPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(ports)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readHostPorts(t *testing.T, projectDir string) map[string]int {
	t.Helper()
	data, err := os.ReadFile(HostPortsPath(projectDir))
	if err != nil {
		t.Fatal(err)
	}
	ports := map[string]int{}
	if err := json.Unmarshal(data, &ports); err != nil {
		t.Fatal(err)
	}
	return ports
}

// TestPruneDropsTheFreedBlocksHostPortReservations is the regression lock for
// the reported failure: prune freed a dead worktree's block from blocks.json
// but left "block:<key>:<base>" in host-ports.json, so the next key handed that
// block failed `allocate_port(...): port N reserved by block:<dead key>:<base>`.
func TestPruneDropsTheFreedBlocksHostPortReservations(t *testing.T) {
	testkit.Slow(t, "builds real git repositories and worktrees")
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	deadDir := filepath.Join(t.TempDir(), "wt-dead")
	pruneGit(t, primary, "worktree", "add", "-q", "-b", "dead", deadDir)
	deadPort := registerStackKey(t, primary, "wt-dead", 21000)
	prodPort := registerPlainKey(t, primary, "prod", 3000)
	unrelatedDir := filepath.Join(t.TempDir(), "unavailable-checkout")

	writeHostPorts(t, primary, map[string]int{
		"block:wt-dead:21000":     deadPort,
		"block:wt-dead:5432":      deadPort - 21000 + 5432,
		"block:prod:3000":         prodPort,
		deadDir + ":dev:web":      40001,
		primary + ":dev:web":      40002,
		unrelatedDir + ":dev:web": 40003, // missing, but no reclaimed block owns it
		"block:wt-dead-2:21000":   21300, // a different key sharing the prefix
	})
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	wantDropped := []string{"block:wt-dead:21000", "block:wt-dead:5432"}
	// Only reservations explicitly owned by reclaimed blocks are dropped.
	sort.Strings(wantDropped)

	dry, err := PruneAll(primary, true)
	if err != nil {
		t.Fatalf("PruneAll dry-run: %v", err)
	}
	if !reflect.DeepEqual(dry.Reservations, wantDropped) {
		t.Fatalf("dry-run reservations = %v, want %v", dry.Reservations, wantDropped)
	}
	if got := readHostPorts(t, primary); len(got) != 7 {
		t.Fatalf("dry-run rewrote host-ports.json: %v", got)
	}

	res, err := PruneAll(primary, false)
	if err != nil {
		t.Fatalf("PruneAll: %v", err)
	}
	if len(res.Blocks) != 1 || res.Blocks[0].Key != "wt-dead" {
		t.Fatalf("pruned blocks = %+v, want [wt-dead]", res.Blocks)
	}
	if !reflect.DeepEqual(res.Reservations, wantDropped) {
		t.Fatalf("dropped reservations = %v, want %v", res.Reservations, wantDropped)
	}
	want := map[string]int{
		"block:prod:3000":         prodPort,
		deadDir + ":dev:web":      40001,
		primary + ":dev:web":      40002,
		unrelatedDir + ":dev:web": 40003,
		"block:wt-dead-2:21000":   21300,
	}
	if got := readHostPorts(t, primary); !reflect.DeepEqual(got, want) {
		t.Fatalf("host-ports.json after prune = %v, want %v", got, want)
	}
}

// TestReleaseKeyDropsItsHostPortReservations: the manual release frees the
// block's reservations the same way, and only that key's.
func TestReleaseKeyDropsItsHostPortReservations(t *testing.T) {
	testkit.Slow(t, "builds a real git repository")
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	port := registerPlainKey(t, primary, "prod-cp-obs", 3000)
	other := registerPlainKey(t, primary, "prod", 3000)
	writeHostPorts(t, primary, map[string]int{
		"block:prod-cp-obs:3000": port,
		"block:prod:3000":        other,
	})

	listed, err := BlockReservations(primary, "prod-cp-obs")
	if err != nil {
		t.Fatalf("BlockReservations: %v", err)
	}
	if !reflect.DeepEqual(listed, []string{"block:prod-cp-obs:3000"}) {
		t.Fatalf("BlockReservations = %v", listed)
	}

	_, dropped, err := ReleaseKey(primary, "prod-cp-obs")
	if err != nil {
		t.Fatalf("ReleaseKey: %v", err)
	}
	if !reflect.DeepEqual(dropped, []string{"block:prod-cp-obs:3000"}) {
		t.Fatalf("ReleaseKey dropped %v", dropped)
	}
	if got := readHostPorts(t, primary); !reflect.DeepEqual(got, map[string]int{"block:prod:3000": other}) {
		t.Fatalf("host-ports.json after release = %v", got)
	}
}
