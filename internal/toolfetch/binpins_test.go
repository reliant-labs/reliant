package toolfetch

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// Every pinned archive has a compiled-in executable pin, and nothing else
// does: a release forge can install is one whose binary it can check
// without asking the cache.
func TestEveryArchivePinHasABinaryPin(t *testing.T) {
	t.Parallel()
	hexSum := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, tool := range Tools() {
		if err := New().validate(tool); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
		}
		if len(tool.BinaryChecksums) != len(tool.Checksums) {
			t.Errorf("%s: %d archive pins, %d executable pins", tool.Name, len(tool.Checksums), len(tool.BinaryChecksums))
		}
		seen := map[string]string{}
		for asset, bin := range tool.BinaryChecksums {
			if !hexSum.MatchString(bin) {
				t.Errorf("%s: executable pin for %s is %q", tool.Name, asset, bin)
			}
			if bin == tool.Checksums[asset] {
				t.Errorf("%s: executable pin for %s is the archive's sum, copied into the wrong table", tool.Name, asset)
			}
			if other, dup := seen[bin]; dup {
				t.Errorf("%s: %s and %s share an executable pin; each platform's binary differs", tool.Name, asset, other)
			}
			seen[bin] = asset
		}
	}
	missing := Syft
	missing.BinaryChecksums = map[string]string{}
	if err := New().validate(missing); err == nil || !strings.Contains(err.Error(), "executable checksum") {
		t.Errorf("a release with no executable pin validated: %v", err)
	}
}

// An archive that matches its pin but holds an executable that does not
// match ITS pin installs nothing: the two pins disagree, and forge trusts
// neither alone.
func TestInstallRefusesAnExecutableThatMissesItsPin(t *testing.T) {
	t.Parallel()
	archive := tarGz(t, "faketool", fakeBinary)
	tool := fakeTool(archive, nil)
	tool.BinaryChecksums[tool.Asset("linux", "amd64")] = sum([]byte("some other build"))
	rel := newRelease(t)
	rel.serve(tool, "linux", "amd64", archive)
	f := rel.fetcher(t, "linux")
	_, err := f.Ensure(context.Background(), tool)
	if !errors.Is(err, ErrChecksum) || !strings.Contains(err.Error(), "executable") {
		t.Fatalf("err = %v, want ErrChecksum for the executable", err)
	}
	if _, ok := f.Find(tool); ok {
		t.Error("an executable that missed its pin was installed")
	}
	assertNoStaging(t, f.CacheDir)
}

// TestPinnedBinaryChecksumsMatchTheirArchives re-derives every executable
// pin from the real release: download each pinned archive, verify it
// against its archive pin, and hash its executable. It needs the network
// and ~500 MB, so it runs only on request — after a version bump:
//
//	FORGE_TEST_TOOLFETCH_LIVE=1 go test -run TestPinnedBinaryChecksumsMatchTheirArchives ./internal/toolfetch/
func TestPinnedBinaryChecksumsMatchTheirArchives(t *testing.T) {
	testkit.Slow(t, "downloads every pinned syft and grype archive")
	if os.Getenv("FORGE_TEST_TOOLFETCH_LIVE") != "1" {
		t.Skip("set FORGE_TEST_TOOLFETCH_LIVE=1 to download every pinned archive and re-derive the executable pins")
	}
	f := New()
	f.Client = http.DefaultClient
	for _, tool := range Tools() {
		for asset, want := range tool.Checksums {
			t.Run(asset, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, asset)
				url := f.BaseURL + "/" + tool.Repo + "/releases/download/" + tool.Version + "/" + asset
				got, err := f.download(context.Background(), url, path)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("archive sha256 %s, pinned %s", got, want)
				}
				bin := tool.Name
				if strings.HasSuffix(asset, ".zip") {
					bin += ".exe"
				}
				out := filepath.Join(dir, "bin")
				if err := extractMember(path, asset, bin, out, 0o755); err != nil {
					t.Fatal(err)
				}
				if got, err := hashFile(out); err != nil || got != tool.BinaryChecksums[asset] {
					t.Errorf("executable sha256 %s (%v), pinned %s", got, err, tool.BinaryChecksums[asset])
				}
			})
		}
	}
}
