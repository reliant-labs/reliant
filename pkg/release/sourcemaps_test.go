package release

import (
	"strings"
	"testing"
)

func TestSourcemapsArtifactName(t *testing.T) {
	if got := SourcemapsArtifactName("reliant-web"); got != "reliant-web.sourcemaps" {
		t.Fatalf("name = %q", got)
	}
	for name, want := range map[string]struct {
		frontend string
		ok       bool
	}{
		"reliant-web.sourcemaps": {"reliant-web", true},
		"web.sourcemaps":         {"web", true},
		".sourcemaps":            {"", false},
		"web":                    {"", false},
		"registry.example/org/proj/web/static.v1": {"", false},
		"web.sourcemaps.bak":                      {"", false},
	} {
		frontend, ok := SourcemapsFrontend(name)
		if frontend != want.frontend || ok != want.ok {
			t.Errorf("SourcemapsFrontend(%q) = %q, %v; want %q, %v", name, frontend, ok, want.frontend, want.ok)
		}
	}
}

func TestSourcemapsRepository(t *testing.T) {
	got, err := SourcemapsRepository("registry.example/org-1/acme/", "reliant-web")
	if err != nil || got != "registry.example/org-1/acme/reliant-web-sourcemaps" {
		t.Fatalf("repository = %q, %v", got, err)
	}
	for _, bad := range []string{"Web", "web_app", "-web", "web-", "", strings.Repeat("a", 53)} {
		if _, err := SourcemapsRepository("registry.example/o/p", bad); err == nil {
			t.Errorf("frontend %q named a repository", bad)
		}
	}
	if _, err := SourcemapsRepository("  ", "web"); err == nil {
		t.Error("an empty registry base named a repository")
	}
	// The longest admissible frontend still yields a legal 63-character segment.
	longest := strings.Repeat("a", 52)
	repo, err := SourcemapsRepository("r/o/p", longest)
	if err != nil || len(repo[strings.LastIndex(repo, "/")+1:]) != 63 {
		t.Errorf("longest frontend: %q, %v", repo, err)
	}
}

func TestSourcemapsTag(t *testing.T) {
	for in, want := range map[string]string{
		"3f9c0e1a2b4d":    "3f9c0e1a2b4d",
		"1.4.2":           "1.4.2",
		"1.4.2+build.5":   "1.4.2_build.5",
		"v2024-10-19_rc1": "v2024-10-19_rc1",
	} {
		got, err := SourcemapsTag(in)
		if err != nil || got != want {
			t.Errorf("SourcemapsTag(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", ".hidden", "-lead", "a b", "a/b", "a:b", strings.Repeat("a", 129)} {
		if _, err := SourcemapsTag(bad); err == nil {
			t.Errorf("SourcemapsTag(%q) accepted", bad)
		}
	}
}

// The recorded shape must be a valid release artifact: a shared OCI digest with
// a version (the build id) and the repository as URI.
func TestSourcemapsArtifactIsAValidReleaseArtifact(t *testing.T) {
	a := Artifact{
		Kind:    KindOCI,
		Mode:    ModeShared,
		Digests: map[string]string{SharedVariant: "sha256:" + strings.Repeat("ab12cd34", 8)},
		Version: "3f9c0e1a2b4d",
		URI:     "registry.example/org/proj/web-sourcemaps",
	}
	if err := a.Validate(SourcemapsArtifactName("web")); err != nil {
		t.Fatalf("a sourcemaps artifact must validate: %v", err)
	}
	if d, ok := a.SharedDigest(); !ok || d != a.Digests[SharedVariant] {
		t.Fatalf("SharedDigest = %q, %v", d, ok)
	}
}
