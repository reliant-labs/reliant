//go:build e2e

package cli

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// TestE2EFreshSiteFirstCutRecordsAnSBOMPerArtifact is SBOMs on by default
// (design doc §6, phase 4b, D0), end to end: a project `forge project new`
// just wrote, with nothing turned on, cuts its first release and every
// artifact that ships carries an SBOM, in the release and in the ledger.
//
// The smallest project with an artifact and no docker is a static site
// (`--kind site`). Its releasable envs are hosted, so its cut records on a
// control plane and pushes the site to the platform registry: the only edit
// to the scaffold points prod's `forge.ControlPlane` at an httptest control
// plane (the fake the hosted ledger tests use, plus the vulnerability
// service's upload) and an in-process OCI registry. Everything else is real —
// the Next.js build, forge's pinned syft (downloaded into forge's tool cache
// on first use) reading the site's lockfile, the hosted ledger, the upload.
func TestE2EFreshSiteFirstCutRecordsAnSBOMPerArtifact(t *testing.T) {
	t.Parallel() // its own project, ledger and control plane; binary shared via sync.Once
	requireTool(t, "git", "node", "npm")
	forgeBin := buildforgeBinary(t)

	cp := newSBOME2EControlPlane()
	cpSrv := httptest.NewServer(cp)
	t.Cleanup(cpSrv.Close)
	regSrv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(regSrv.Close)
	// After the cut the build records the env's bundle, which the control
	// plane reads back from the registry the build pushed it to.
	cp.deploy.layerFetch = func(repository, manifestDigest string) ([]byte, error) {
		repo, err := bundle.NewRepositoryWithCredential(repository, func(context.Context, string) (auth.Credential, error) {
			return auth.EmptyCredential, nil
		})
		if err != nil {
			return nil, err
		}
		fetched, err := bundle.Fetch(context.Background(), repo, manifestDigest)
		if err != nil {
			return nil, err
		}
		return fetched.Manifests, nil
	}
	regURL, err := url.Parse(regSrv.URL)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	state := t.TempDir()
	ledgerHome := filepath.Join(state, "ledger")
	policy := filepath.Join(state, "storage.json")
	// The host disk reserve guards a developer machine's disk, not this
	// test's subject; a shared e2e runner may be below the 20 GiB default.
	if err := os.WriteFile(policy, []byte(`{"host_reserve_gib": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"SBOME2E_CP_TOKEN=rlat_e2e",
		"FORGE_HOME=" + filepath.Join(state, "home"),
		"FORGE_LEDGER_HOME=" + ledgerHome,
		// No vulnerability database here: the cut records SBOMs and says
		// "not scanned"; it never downloads the database itself.
		"FORGE_VULN_DB_DIR=" + filepath.Join(state, "vulndb"),
		"FORGE_SBOM_CACHE=" + filepath.Join(state, "sbom-cache"),
		"FORGE_STORAGE_POLICY=" + policy,
		"GIT_AUTHOR_NAME=forge e2e", "GIT_AUTHOR_EMAIL=e2e@example.com",
		"GIT_COMMITTER_NAME=forge e2e", "GIT_COMMITTER_EMAIL=e2e@example.com",
	}
	run := func(dir string, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s\ncontrol plane calls: %s", name, strings.Join(args, " "), err, out, cp.callList())
		}
		return string(out)
	}

	run(root, forgeBin, "project", "new", "sbomsite", "--kind", "site")
	project := filepath.Join(root, "sbomsite")

	mainK := filepath.Join(project, "deploy", "kcl", "prod", "main.k")
	src, err := os.ReadFile(mainK)
	if err != nil {
		t.Fatal(err)
	}
	const scaffolded = "control_plane = forge.ControlPlane {}"
	if !bytes.Contains(src, []byte(scaffolded)) {
		t.Fatalf("prod/main.k no longer declares %q; update this test's one edit:\n%s", scaffolded, src)
	}
	src = bytes.Replace(src, []byte(scaffolded), []byte(fmt.Sprintf(
		`control_plane = forge.ControlPlane {endpoint = %q, token_env = "SBOME2E_CP_TOKEN", registry_host = %q}`,
		cpSrv.URL, regURL.Host)), 1)
	if err := os.WriteFile(mainK, src, 0o644); err != nil {
		t.Fatal(err)
	}
	run(project, "git", "add", "-A")
	run(project, "git", "commit", "--quiet", "--no-verify", "-m", "a fresh site, pointed at a test control plane")

	out := run(project, forgeBin, "env", "build", "prod", "--release", "v0.1.0")

	rel, ok := cp.release("v0.1.0")
	if !ok {
		t.Fatalf("the cut recorded no release v0.1.0 on the control plane\n%s\ncalls: %s", out, cp.callList())
	}
	var shipping []string
	for _, a := range rel.Artifacts {
		if _, maps := release.SourcemapsFrontend(a.Name); !maps {
			shipping = append(shipping, a.Name)
		}
	}
	if len(shipping) == 0 {
		t.Fatalf("release v0.1.0 ships no artifact: %+v\n%s", rel.Artifacts, out)
	}
	uploads := cp.uploads()
	for _, name := range shipping {
		up, ok := uploads[name]
		if !ok {
			t.Errorf("artifact %s: no SBOM was recorded for it (uploads: %v)\n%s", name, sortedKeys(uploads), out)
			continue
		}
		if up.version != "v0.1.0" || up.source != release.SBOMSourceLockfile {
			t.Errorf("artifact %s: SBOM recorded for %s from %q, want v0.1.0 from %q", name, up.version, up.source, release.SBOMSourceLockfile)
		}
		var doc struct {
			BOMFormat  string            `json:"bomFormat"`
			Components []json.RawMessage `json:"components"`
		}
		if err := json.Unmarshal(up.data, &doc); err != nil || doc.BOMFormat != "CycloneDX" || len(doc.Components) == 0 {
			t.Errorf("artifact %s: the SBOM is not a CycloneDX document listing the site's dependencies (err %v, format %q, %d components)",
				name, err, doc.BOMFormat, len(doc.Components))
		}
		// The bytes are in this machine's ledger too, by the same digest.
		if !ledgerHoldsSBOM(t, ledgerHome, up.digest) {
			t.Errorf("artifact %s: the machine ledger under %s holds no SBOM %s", name, ledgerHome, up.digest)
		}
	}
	if !strings.Contains(out, "SBOMs: ") || strings.Contains(out, "recorded without an SBOM") {
		t.Errorf("the cut did not report every SBOM recorded:\n%s", out)
	}
}

// ledgerHoldsSBOM reports whether the machine ledger has the SBOM with this
// digest: <ledger>/<project-id>/sboms/sha256-<hex>.cdx.json.
func ledgerHoldsSBOM(t *testing.T, ledgerHome, digest string) bool {
	t.Helper()
	want := strings.Replace(digest, ":", "-", 1) + ".cdx.json"
	found := false
	_ = filepath.WalkDir(ledgerHome, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == want && filepath.Base(filepath.Dir(p)) == "sboms" {
			found = true
		}
		return nil
	})
	return found
}

// sbomE2EUpload is one RecordReleaseSBOM the control plane received.
type sbomE2EUpload struct {
	version, source, digest string
	data                    []byte
}

// sbomE2EControlPlane is the hosted ledger tests' fake DeployService, plus the
// organization lookup, the capability that asks for SBOM uploads, and the
// upload itself.
type sbomE2EControlPlane struct {
	deploy *fakeDeployService

	mu    sync.Mutex
	calls []string
	ups   map[string]sbomE2EUpload
}

const sbomE2EOrg = "4f3c2b1a-0000-4000-8000-0000000000e2"

func newSBOME2EControlPlane() *sbomE2EControlPlane {
	return &sbomE2EControlPlane{deploy: newFakeDeployService(map[string]string{}), ups: map[string]sbomE2EUpload{}}
}

func (c *sbomE2EControlPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.calls = append(c.calls, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/" + cloud.ProcGetOrganization:
		_ = json.NewEncoder(w).Encode(cloud.OrganizationReply(sbomE2EOrg))
		return
	case "/" + procDeployCapabilities:
		_ = json.NewEncoder(w).Encode(map[string]any{"vulnerabilityFeatures": []string{hostedVulnFeatureSBOMUpload}})
		return
	case "/" + procRecordReleaseSBOM:
		c.recordSBOM(w, raw)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	c.deploy.ServeHTTP(w, r)
}

func (c *sbomE2EControlPlane) recordSBOM(w http.ResponseWriter, raw []byte) {
	var req struct {
		Version      string `json:"version"`
		ArtifactName string `json:"artifactName"`
		SBOM         string `json:"sbom"`
		Encoding     string `json:"encoding"`
		Source       string `json:"source"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		connectErr(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	gz, err := base64.StdEncoding.DecodeString(req.SBOM)
	if err != nil || req.Encoding != sbomEncodingGzip {
		connectErr(w, http.StatusBadRequest, "invalid_argument", fmt.Sprintf("sbom: encoding %q: %v", req.Encoding, err))
		return
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		connectErr(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		connectErr(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	c.mu.Lock()
	c.ups[req.ArtifactName] = sbomE2EUpload{version: req.Version, source: req.Source, digest: digest, data: data}
	c.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"digest": digest, "created": true, "scanState": "VULNERABILITY_SCAN_STATE_PENDING"})
}

func (c *sbomE2EControlPlane) release(version string) (wireRelease, bool) {
	c.deploy.mu.Lock()
	defer c.deploy.mu.Unlock()
	rel, ok := c.deploy.releases[version]
	return rel, ok
}

func (c *sbomE2EControlPlane) uploads() map[string]sbomE2EUpload {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]sbomE2EUpload, len(c.ups))
	for k, v := range c.ups {
		out[k] = v
	}
	return out
}

func (c *sbomE2EControlPlane) callList() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.calls, ",")
}
