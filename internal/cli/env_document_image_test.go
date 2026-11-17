package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/envdoc"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
)

// TestEnvDocumentImages_KCLMatchesGo keeps KCL the source of a built
// workload's image pinning (internal/envdoc image.go). Each render-contract
// fixture with a workload forge builds for a cluster is rendered by KCL under
// a deploy's bindings — a tag, the digests a push captured, a release
// snapshot's slot pin — and its golden (the same fixture rendered with no
// bindings), saved as an env document, is loaded under the same bindings.
// The two must agree on every workload and on the applied stream: a render
// saved as a document deploys exactly what its KCL would.
func TestEnvDocumentImages_KCLMatchesGo(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in task test")
	}
	kclplugin.Register()
	argsRE := regexp.MustCompile(`(?m)^#\s*kcl-args:\s*(.+?)\s*$`)
	kclMod := "[package]\nname = \"envdocimages\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n"
	checked := 0
	for _, name := range []string{"cluster", "flux-delivery", "mixed", "unit-split"} {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("testdata", "render_contract", name+".k"))
			if err != nil {
				t.Fatal(err)
			}
			golden, err := os.ReadFile(filepath.Join("testdata", "render_contract", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			unbound, err := parseKCLEntities(golden)
			if err != nil {
				t.Fatal(err)
			}

			// The bindings a deploy of release v1.2.3 makes: a digest for
			// every image forge builds, and a snapshot pin for the first.
			digests := map[string]string{}
			pins := map[string]map[string]string{}
			for _, w := range unbound.Workloads {
				if w.Build.Type == "" || !(w.OnRuntime(RuntimeCluster) || w.OnRuntime(RuntimeExternal)) {
					continue
				}
				sum := sha256.Sum256([]byte(w.BuildImage))
				digests[w.BuildImage] = "sha256:" + hex.EncodeToString(sum[:])
				if len(pins) == 0 {
					pin := sha256.Sum256([]byte("pin:" + w.Name))
					pins[w.Name] = map[string]string{"container:" + w.Name: w.Image + "@sha256:" + hex.EncodeToString(pin[:])}
				}
			}
			if len(digests) == 0 {
				t.Fatalf("%s.json has no built cluster workload: the fixture list is stale", name)
			}
			const tag = "v1.2.3"
			dj, _ := json.Marshal(digests)
			pj, _ := json.Marshal(pins)
			var dArgs []string
			if m := argsRE.FindSubmatch(src); m != nil {
				dArgs = strings.Fields(string(m[1]))
			}
			dArgs = append(dArgs, "image_tag="+strconv.Quote(tag), "image_digests="+strconv.Quote(string(dj)),
				"forge_workload_image_digests="+strconv.Quote(string(pj)))

			kdir := t.TempDir()
			for f, c := range map[string]string{"kcl.mod": kclMod, "main.k": string(src)} {
				if err := os.WriteFile(filepath.Join(kdir, f), []byte(c), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			kclOut, err := kclrender.Run(kdir, kdir, dArgs)
			if err != nil {
				t.Fatalf("render %s.k: %v", name, err)
			}

			var wrapper struct {
				Output map[string]any `json:"output"`
			}
			if err := json.Unmarshal(golden, &wrapper); err != nil {
				t.Fatal(err)
			}
			wrapper.Output["apiVersion"] = envdoc.APIVersion
			body, _ := json.MarshalIndent(wrapper.Output, "", "  ")
			ddir := t.TempDir()
			writeEnvDocument(t, ddir, unbound.Env+".json", string(body))
			docOut, ok, err := envdoc.Load(ddir, unbound.Env, envdoc.Options{ImageTag: tag, ImageDigests: digests, WorkloadPins: pins})
			if err != nil || !ok {
				t.Fatalf("load the golden as a document: ok %v, %v", ok, err)
			}

			fromKCL, err := parseKCLEntities(kclOut)
			if err != nil {
				t.Fatal(err)
			}
			fromDoc, err := parseKCLEntities(docOut)
			if err != nil {
				t.Fatal(err)
			}
			if fromDoc.ImageTag != fromKCL.ImageTag {
				t.Errorf("image_tag: document %q, KCL %q", fromDoc.ImageTag, fromKCL.ImageTag)
			}
			kw := map[string]string{}
			for _, w := range fromKCL.Workloads {
				kw[w.Name] = mustJSON(t, w)
			}
			for _, w := range fromDoc.Workloads {
				if g, k := mustJSON(t, w), kw[w.Name]; g != k {
					t.Errorf("workload %s\n document %s\n KCL      %s", w.Name, g, k)
				}
			}
			gs, gerr := cluster.ExtractManifests(docOut)
			ks, kerr := cluster.ExtractManifests(kclOut)
			if gerr != nil || kerr != nil {
				t.Fatalf("extract: %v / %v", gerr, kerr)
			}
			if gs != ks {
				t.Errorf("applied streams differ:\n%s", firstLineDiff(gs, ks))
			}
			for w, slots := range pins {
				for _, ref := range slots {
					if !strings.Contains(gs, ref) {
						t.Errorf("the applied stream lacks %s's pinned reference %s", w, ref)
					}
				}
			}
			checked++
		})
	}
	if checked == 0 {
		t.Fatal("no fixture checked")
	}
}

// firstLineDiff names the first line two streams differ on.
func firstLineDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			return "line " + strconv.Itoa(i+1) + ":\n document: " + gl + "\n KCL:      " + wl
		}
	}
	return ""
}
