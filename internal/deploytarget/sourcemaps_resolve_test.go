package deploytarget

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// decodeMappings resolves a generated (line, col) in a version-3 source map to
// the original (source, line, col), all zero-based. It is the smallest decoder
// that answers "does this map still point at the right code", so the proof
// does not depend on a library the product does not use.
func decodeMappings(t *testing.T, mapJSON []byte, line, col int) (source string, srcLine, srcCol int) {
	t.Helper()
	var m struct {
		Sources  []string `json:"sources"`
		Mappings string   `json:"mappings"`
	}
	if err := json.Unmarshal(mapJSON, &m); err != nil {
		t.Fatal(err)
	}
	const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	vlq := func(seg string) []int {
		var out []int
		shift, val := 0, 0
		for _, c := range seg {
			d := strings.IndexRune(b64, c)
			val += (d & 31) << shift
			if d&32 != 0 {
				shift += 5
				continue
			}
			n := val >> 1
			if val&1 == 1 {
				n = -n
			}
			out = append(out, n)
			shift, val = 0, 0
		}
		return out
	}
	srcIdx, sl, sc := 0, 0, 0
	found := false
	for gl, lineMap := range strings.Split(m.Mappings, ";") {
		gc := 0
		for _, seg := range strings.Split(lineMap, ",") {
			if seg == "" {
				continue
			}
			f := vlq(seg)
			gc += f[0]
			if len(f) >= 4 {
				srcIdx += f[1]
				sl += f[2]
				sc += f[3]
			}
			if gl == line && gc <= col && len(f) >= 4 {
				source, srcLine, srcCol, found = m.Sources[srcIdx], sl, sc, true
			}
		}
	}
	if !found {
		t.Fatalf("no mapping at %d:%d", line, col)
	}
	return source, srcLine, srcCol
}

// TestExtractedMapResolvesAKnownFrame stages a real Vite build (testdata/
// vite-build, produced by `vite build` with sourcemap on) and proves the whole
// promise of the extraction: the served bundle has no map and no trailer, and
// the map in the sidecar still resolves a position in that SERVED bundle to
// the original source line. Stripping the trailer must shift nothing.
func TestExtractedMapResolvesAKnownFrame(t *testing.T) {
	projectDir := t.TempDir()
	if err := copyDir(filepath.Join("testdata", "vite-build"), filepath.Join(projectDir, "web", "dist")); err != nil {
		t.Fatal(err)
	}
	plan := fixturePlan(t, projectDir, "")
	plan.Copies[0].DestRel = "."
	if _, err := runStagePlan(context.Background(), &fakeRunner{}, plan); err != nil {
		t.Fatal(err)
	}

	bundle, err := os.ReadFile(filepath.Join(plan.StagingDir, "assets", "index-CNKW1jnY.js"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bundle), "sourceMappingURL") {
		t.Fatal("the served bundle still names its map")
	}
	if _, err := os.Stat(filepath.Join(plan.StagingDir, "assets", "index-CNKW1jnY.js.map")); !os.IsNotExist(err) {
		t.Fatal("the map is still in the served tree")
	}

	text := string(bundle)
	idx := strings.Index(text, `new Error("known frame")`)
	if idx < 0 {
		t.Fatal("fixture bundle does not contain the throw")
	}
	line := strings.Count(text[:idx], "\n")
	col := idx - (strings.LastIndex(text[:idx], "\n") + 1)

	mapJSON, err := os.ReadFile(filepath.Join(SourcemapsDir(plan.StagingDir), "assets", "index-CNKW1jnY.js.map"))
	if err != nil {
		t.Fatal(err)
	}
	source, srcLine, _ := decodeMappings(t, mapJSON, line, col)
	if !strings.HasSuffix(source, "src/boom.js") || srcLine != 1 {
		t.Errorf("served position %d:%d resolves to %s line %d, want src/boom.js line 2 (zero-based 1)", line, col, source, srcLine)
	}

	var m SourcemapManifest
	b, _ := os.ReadFile(filepath.Join(SourcemapsDir(plan.StagingDir), SourcemapManifestName))
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 1 || m.Files[0].Path != "assets/index-CNKW1jnY.js" || m.Files[0].Map != "assets/index-CNKW1jnY.js.map" {
		t.Errorf("manifest files = %+v", m.Files)
	}
}
