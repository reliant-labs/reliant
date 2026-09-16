// Copyright (c) 2025 Reliant Labs

package daemoninstance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realOrigins are the entries actually present in ~/.reliant/daemon.json on a
// developer machine. They are the inputs that must not collide.
var realOrigins = []string{
	"http://127.0.0.1:8123",
	"http://localhost:3090",
	"http://localhost:3091",
	"http://localhost:3123",
	"http://localhost:3691",
	"http://localhost:8090",
	"http://localhost:8690",
	"https://api.reliantapi.com",
	"https://preprod.reliantapi.com",
}

func TestSlugIsStableAcrossCalls(t *testing.T) {
	key := Key{
		Origin:    "http://localhost:8090",
		Sub:       "5f4d3c2b-1a09-4e8d-9c7b-6a5f4e3d2c1b",
		Workspace: "/Users/dev/src/reliant-labs/reliant",
	}

	want := key.Slug()
	for i := 0; i < 100; i++ {
		if got := key.Slug(); got != want {
			t.Fatalf("slug changed between calls: iteration %d gave %q, first call gave %q", i, got, want)
		}
	}

	// An independently constructed, equal key must agree — the slug is a pure
	// function of the key, not of anything the first call cached.
	twin := Key{
		Origin:    "http://localhost:8090",
		Sub:       "5f4d3c2b-1a09-4e8d-9c7b-6a5f4e3d2c1b",
		Workspace: "/Users/dev/src/reliant-labs/reliant",
	}
	if got := twin.Slug(); got != want {
		t.Fatalf("equal keys produced different slugs: %q vs %q", got, want)
	}
}

func TestOriginSlugsDoNotCollide(t *testing.T) {
	// The two pairs the ports and schemes make dangerous. 8090/8690 differ by
	// one character inside a segment a lossy slugger could truncate away, and
	// the https/http pair differs only in the part that a naive "strip the
	// scheme punctuation" rule erases.
	pairs := [][2]string{
		{"http://localhost:8090", "http://localhost:8690"},
		{"https://x.com", "http://x.com"},
	}
	for _, pair := range pairs {
		a := Key{Origin: OriginKey(pair[0])}.OriginSlug()
		b := Key{Origin: OriginKey(pair[1])}.OriginSlug()
		if a == b {
			t.Errorf("origins %q and %q collided on slug %q", pair[0], pair[1], a)
		}
	}

	// Every real entry in the credentials store must land somewhere distinct.
	seen := make(map[string]string, len(realOrigins))
	for _, origin := range realOrigins {
		slug := Key{Origin: OriginKey(origin)}.OriginSlug()
		if prior, dup := seen[slug]; dup {
			t.Errorf("origins %q and %q collided on slug %q", prior, origin, slug)
		}
		seen[slug] = origin
	}
}

func TestOriginKeyCollapsesToOrigin(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://staging.reliantapi.com/grpc", "https://staging.reliantapi.com"},
		{"https://staging.reliantapi.com/api?x=1#frag", "https://staging.reliantapi.com"},
		{"HTTPS://API.Example.COM/path", "https://api.example.com"},
		{"http://[::1]:8080", "http://[::1]:8080"},
		{"http://localhost:8090", "http://localhost:8090"},
	}
	for _, tc := range tests {
		if got := OriginKey(tc.in); got != tc.want {
			t.Errorf("OriginKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Unparseable input yields no key, so callers can refuse rather than
	// silently write under a default origin.
	for _, bad := range []string{"", "   ", "not-a-url", "https://"} {
		if got := OriginKey(bad); got != "" {
			t.Errorf("OriginKey(%q) = %q, want empty", bad, got)
		}
	}
}

func TestResolveRejectsUnparseableOrigin(t *testing.T) {
	if _, err := Resolve("not-a-url", "sub", "/tmp/ws"); !errors.Is(err, ErrInvalidOrigin) {
		t.Fatalf("Resolve with a bad origin: err = %v, want ErrInvalidOrigin", err)
	}
}

func TestResolveDefaultsOriginAndWorkspace(t *testing.T) {
	t.Setenv(EnvServerURL, "http://localhost:3123")
	t.Setenv(EnvWorkspace, "")

	dir := t.TempDir()
	key, err := Resolve("", "", dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if key.Origin != "http://localhost:3123" {
		t.Errorf("origin = %q, want the RELIANT_SERVER_URL value", key.Origin)
	}

	// An explicit workspace label wins over the env override.
	t.Setenv(EnvWorkspace, "/some/other/place")
	key, err = Resolve("http://localhost:8090", "", dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasSuffix(key.Workspace, filepath.Base(dir)) {
		t.Errorf("workspace = %q, want the explicit argument ending in %q", key.Workspace, filepath.Base(dir))
	}

	// With no explicit workspace, the env override is used.
	key, err = Resolve("http://localhost:8090", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if key.Workspace != "/some/other/place" {
		t.Errorf("workspace = %q, want the RELIANT_INSTANCE_WORKSPACE value", key.Workspace)
	}
}

func TestWorkspacePathBecomesOneSegment(t *testing.T) {
	key := Key{
		Origin:    "http://localhost:8090",
		Workspace: "/Users/dev/src/reliant-labs/reliant/electron",
	}

	slug := key.WorkspaceSlug()
	if strings.ContainsAny(slug, `/\`) {
		t.Fatalf("workspace slug %q leaked a path separator", slug)
	}
	if !strings.HasPrefix(slug, "electron-") {
		t.Errorf("workspace slug %q should lead with the readable base name", slug)
	}

	// Two worktrees sharing a base name must not merge.
	other := Key{Origin: key.Origin, Workspace: "/Users/dev/other/reliant/electron"}
	if other.WorkspaceSlug() == slug {
		t.Errorf("distinct workspaces sharing a base name collided on %q", slug)
	}

	// The whole instance path must be exactly three segments below the root.
	dataDir, err := key.DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	root, err := InstancesDir()
	if err != nil {
		t.Fatalf("InstancesDir: %v", err)
	}
	rel, err := filepath.Rel(root, dataDir)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if parts := strings.Split(rel, string(filepath.Separator)); len(parts) != 3 {
		t.Errorf("instance path %q has %d segments below the root, want 3", rel, len(parts))
	}
}

func TestEmptySubBecomesAnExplicitSegment(t *testing.T) {
	key := Key{Origin: "http://localhost:8090", Workspace: "/Users/dev/ws"}

	if got := key.SubSlug(); got != DefaultSubSegment {
		t.Errorf("empty Sub slug = %q, want %q", got, DefaultSubSegment)
	}

	dataDir, err := key.DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	// The path must not contain an empty component, which would silently
	// collapse the signed-out instance into its parent directory.
	for _, part := range strings.Split(dataDir, string(filepath.Separator))[1:] {
		if part == "" {
			t.Fatalf("data dir %q contains an empty path component", dataDir)
		}
	}
	if !strings.Contains(dataDir, string(filepath.Separator)+DefaultSubSegment+string(filepath.Separator)) {
		t.Errorf("data dir %q should carry the %q segment", dataDir, DefaultSubSegment)
	}

	// A real sub must not land in the default segment.
	signedIn := Key{Origin: key.Origin, Sub: "auth0|abc123", Workspace: key.Workspace}
	if signedIn.SubSlug() == DefaultSubSegment {
		t.Error("a non-empty Sub resolved to the default segment")
	}
}

func TestDataDirIsAbsoluteAndUnderInstances(t *testing.T) {
	key, err := Resolve("http://localhost:8090", "sub-1", "/Users/dev/src/reliant")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	dataDir, err := key.DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if !filepath.IsAbs(dataDir) {
		t.Fatalf("data dir %q is not absolute", dataDir)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory on this machine: %v", err)
	}
	want := filepath.Join(home, ".reliant", InstancesDirName)
	if !strings.HasPrefix(dataDir, want+string(filepath.Separator)) {
		t.Errorf("data dir %q is not under %q", dataDir, want)
	}
}

func TestSanitizeNeverProducesADangerousSegment(t *testing.T) {
	for _, in := range []string{"", "...", "///", "..", ".", "!!!", "  ", "a/../../etc/passwd"} {
		got := sanitize(in)
		if got == "" || got == "." || got == ".." {
			t.Errorf("sanitize(%q) = %q, which is not a usable path segment", in, got)
		}
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("sanitize(%q) = %q, which leaked a path separator", in, got)
		}
	}
}
