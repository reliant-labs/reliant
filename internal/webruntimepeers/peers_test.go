package webruntimepeers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestEmbeddedPeersMatchWebRuntime is the mechanism that replaces four
// "keep this list in step with…" comments.
//
// Because go:embed cannot reach outside its own package directory, peers.json
// is a copy of web-runtime/package.json's peerDependencies. This test keeps
// the copy honest — the same pattern internal/buildinfo uses for VERSION.
//
// If this fails, regenerate rather than hand-editing:
//
//	python3 -c 'import json;s=json.load(open("web-runtime/package.json"));f=open("internal/webruntimepeers/peers.json","w");json.dump({"peerDependencies":s["peerDependencies"]},f,indent=2,sort_keys=True);f.write("\n")'
//
// The trailing newline is not cosmetic: peers.json matches the prettier
// pre-commit hook's file filter, and json.dump writes no final newline, so a
// regeneration that omits it takes the pre-commit workflow red on the next
// push with a diff that looks like nothing changed.
func TestEmbeddedPeersMatchWebRuntime(t *testing.T) {
	// t.Fatal, NOT t.Skip. web-runtime/package.json is on every checkout, so
	// a skip here would be unfalsifiable — and the day the layout moves it
	// would silently delete the only thing keeping the embedded copy honest,
	// which is precisely the drift this package exists to end. A missing
	// fixture is a broken test, not an inapplicable one.
	raw, err := os.ReadFile(filepath.Join("..", "..", "web-runtime", "package.json"))
	if err != nil {
		t.Fatalf("read web-runtime/package.json (the source of truth this embedded copy mirrors): %v", err)
	}
	var src struct {
		PeerDependencies map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(raw, &src); err != nil {
		t.Fatalf("parse web-runtime package.json: %v", err)
	}

	var embedded struct {
		PeerDependencies map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(peersJSON, &embedded); err != nil {
		t.Fatalf("parse embedded peers.json: %v", err)
	}

	if len(src.PeerDependencies) == 0 {
		t.Fatal("web-runtime declares no peerDependencies — the derivation has no source")
	}
	for name, want := range src.PeerDependencies {
		got, ok := embedded.PeerDependencies[name]
		if !ok {
			t.Errorf("peer %q is declared by web-runtime but missing from the embedded copy — "+
				"a project linking the dev runtime will resolve TWO copies of it", name)
			continue
		}
		if got != want {
			t.Errorf("peer %q range = %q embedded, %q in web-runtime", name, got, want)
		}
	}
	for name := range embedded.PeerDependencies {
		if _, ok := src.PeerDependencies[name]; !ok {
			t.Errorf("embedded copy carries %q, which web-runtime no longer declares as a peer", name)
		}
	}
}

// TestTypePinsCoverEveryPeer is the regression guard for the drift this
// package was created to end: before it, the hand-written tsconfig list was
// missing NINE real peers (every @opentelemetry/* except api, plus react).
func TestTypePinsCoverEveryPeer(t *testing.T) {
	pins := map[string]bool{}
	for _, p := range TypePins() {
		pins[p] = true
	}
	for _, peer := range decodePeers() {
		if !pins[peer] {
			t.Errorf("peer %q is not in TypePins() — tsc would see two copies of its types", peer)
		}
	}
	for _, extra := range extraTypeOnlyPins {
		if !pins[extra] {
			t.Errorf("type-only pin %q missing from TypePins()", extra)
		}
	}
}

// TestBundlerDedupeCoverEveryPeer is the same guard for the bundler list.
func TestBundlerDedupeCoverEveryPeer(t *testing.T) {
	ded := map[string]bool{}
	for _, p := range BundlerDedupe() {
		ded[p] = true
	}
	for _, peer := range decodePeers() {
		if !ded[peer] {
			t.Errorf("peer %q is not in BundlerDedupe() — it would ship twice", peer)
		}
	}
	for _, extra := range extraBundlerDedupe {
		if !ded[extra] {
			t.Errorf("bundler-only dedupe %q missing from BundlerDedupe()", extra)
		}
	}
}

// TestListsAreSortedAndDeduped keeps generated output stable: an unstable
// order would rewrite a user's tsconfig on every `forge generate`.
func TestListsAreSortedAndDeduped(t *testing.T) {
	for name, list := range map[string][]string{
		"TypePins":      TypePins(),
		"BundlerDedupe": BundlerDedupe(),
	} {
		if !sort.StringsAreSorted(list) {
			t.Errorf("%s() is not sorted: %v", name, list)
		}
		seen := map[string]bool{}
		for _, v := range list {
			if seen[v] {
				t.Errorf("%s() contains %q twice", name, v)
			}
			seen[v] = true
		}
	}
}
