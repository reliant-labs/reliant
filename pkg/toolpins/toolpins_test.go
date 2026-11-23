package toolpins

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every platform forge supports has an archive for each pinned tool, so a
// version bump without its sums fails here.
func TestPinsCoverEveryPlatform(t *testing.T) {
	t.Parallel()
	hexSum := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, tool := range All() {
		for _, p := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
			if s, ok := tool.SHA256(p[0], p[1]); !ok || !hexSum.MatchString(s) {
				t.Errorf("%s %s has no sha256 for %s/%s", tool.Name, tool.Version, p[0], p[1])
			}
		}
		if !sort.SliceIsSorted(tool.Archives, func(i, j int) bool {
			a, b := tool.Archives[i], tool.Archives[j]
			return a.GOOS < b.GOOS || (a.GOOS == b.GOOS && a.GOARCH < b.GOARCH)
		}) {
			t.Errorf("%s: archives are not sorted by GOOS, GOARCH", tool.Name)
		}
		seen := map[string]string{}
		for _, a := range tool.Archives {
			if !hexSum.MatchString(a.SHA256) {
				t.Errorf("%s: %s sha256 %q is not lower-case hex", tool.Name, a.Name, a.SHA256)
			}
			// Upstream publishes no executable sums, so these are held to
			// their shape only; internal/toolfetch's installer and its
			// opt-in live test hold them to the archives' contents.
			if !hexSum.MatchString(a.BinarySHA256) {
				t.Errorf("%s: %s binary_sha256 %q is not lower-case hex", tool.Name, a.Name, a.BinarySHA256)
			}
			if a.BinarySHA256 == a.SHA256 {
				t.Errorf("%s: %s binary_sha256 is the archive's sum, copied into the wrong column", tool.Name, a.Name)
			}
			if other, dup := seen[a.BinarySHA256]; dup {
				t.Errorf("%s: %s and %s share a binary_sha256; each platform's executable differs", tool.Name, a.Name, other)
			}
			seen[a.BinarySHA256] = a.Name
		}
	}
}

// Every pinned sum is the release's own: each archive's name and sha256 is a
// line of the release's checksums file (testdata, copied verbatim from the
// release), and every archive in that file is pinned. A bump copies the new
// checksums file in beside the new version.
func TestPinsMatchTheUpstreamChecksumsFiles(t *testing.T) {
	t.Parallel()
	for _, tool := range All() {
		file := filepath.Join("testdata", tool.Name+"_"+strings.TrimPrefix(tool.Version, "v")+"_checksums.txt")
		upstream := readChecksums(t, file)
		pinned := map[string]string{}
		for _, a := range tool.Archives {
			pinned[a.Name] = a.SHA256
			if got, ok := upstream[a.Name]; !ok || got != a.SHA256 {
				t.Errorf("%s: %s pinned as %s, but %s says %q", tool.Name, a.Name, a.SHA256, file, got)
			}
		}
		for name := range upstream {
			if (strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip")) && pinned[name] == "" {
				t.Errorf("%s: %s publishes %s, which is not pinned", tool.Name, file, name)
			}
		}
	}
}

func readChecksums(t *testing.T, file string) map[string]string {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatalf("%v (a version bump must add the release's checksums file to testdata)", err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) == 2 {
			out[fields[1]] = fields[0]
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAccessors(t *testing.T) {
	t.Parallel()
	g := Grype()
	if g.Name != "grype" || g.Version != GrypeVersion || g.Repo != "anchore/grype" || g.GoPackage != "github.com/anchore/grype/cmd/grype" {
		t.Errorf("Grype() = %+v", g)
	}
	if Syft().Version != SyftVersion {
		t.Errorf("Syft().Version = %s, want %s", Syft().Version, SyftVersion)
	}
	a, ok := g.Archive("linux", "amd64")
	want := Archive{GOOS: "linux", GOARCH: "amd64", Name: "grype_0.122.1_linux_amd64.tar.gz", Format: FormatTarGz,
		SHA256:       "f1ba7e2917ee1395618de20a98da0aa7c0fb1788927797651047c50502a37538",
		BinarySHA256: "322a7839c1c9f6c1b6ac0aa6b61a7d94139b7f24780b82a05b63caab56d4286b", Binary: "grype"}
	if !ok || a != want {
		t.Errorf("Archive(linux, amd64) = %+v, %v; want %+v", a, ok, want)
	}
	if bin, ok := g.BinarySHA256("linux", "amd64"); !ok || bin != want.BinarySHA256 {
		t.Errorf("BinarySHA256(linux, amd64) = %s, %v", bin, ok)
	}
	if a, _ := g.Archive("windows", "amd64"); a.Format != FormatZip || a.Binary != "grype.exe" || a.Name != "grype_0.122.1_windows_amd64.zip" {
		t.Errorf("windows archive = %+v", a)
	}
	if url, _ := g.ReleaseURL("linux", "amd64"); url != "https://github.com/anchore/grype/releases/download/v0.122.1/grype_0.122.1_linux_amd64.tar.gz" {
		t.Errorf("ReleaseURL = %s", url)
	}
	if url, _ := g.ReleaseURLFrom("https://mirror.example/gh/", "linux", "amd64"); url != "https://mirror.example/gh/anchore/grype/releases/download/v0.122.1/grype_0.122.1_linux_amd64.tar.gz" {
		t.Errorf("ReleaseURLFrom = %s", url)
	}
	for _, missing := range [][2]string{{"windows", "arm64"}, {"linux", "riscv64"}, {"plan9", "amd64"}} {
		if _, ok := g.SHA256(missing[0], missing[1]); ok {
			t.Errorf("grype pins %s/%s, which upstream does not publish", missing[0], missing[1])
		}
		if _, ok := g.BinarySHA256(missing[0], missing[1]); ok {
			t.Errorf("grype pins an executable for %s/%s, which upstream does not publish", missing[0], missing[1])
		}
		if _, ok := g.ReleaseURL(missing[0], missing[1]); ok {
			t.Errorf("ReleaseURL(%s/%s) reported a URL with no archive", missing[0], missing[1])
		}
	}
	if s, ok := Lookup("syft"); !ok || s.Name != "syft" {
		t.Errorf("Lookup(syft) = %+v, %v", s, ok)
	}
	if _, ok := Lookup("trivy"); ok {
		t.Error("Lookup found an unpinned tool")
	}
	if names := []string{All()[0].Name, All()[1].Name}; len(All()) != 2 || names[0] != "grype" || names[1] != "syft" {
		t.Errorf("All() = %v", names)
	}
}

// A consumer's edit to a returned Tool changes nothing anyone else sees.
func TestAccessorsReturnCopies(t *testing.T) {
	t.Parallel()
	g := Grype()
	g.Archives[0].SHA256 = "tampered"
	g.Version = "v0"
	if Grype().Archives[0].SHA256 == "tampered" || Grype().Version != GrypeVersion {
		t.Error("mutating a returned Tool changed the pins")
	}
}
