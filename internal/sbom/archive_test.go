package sbom

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/toolfetch"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// writeTar writes a tar of the named (empty-bodied unless given) entries:
// the shape of an image archive without any image in it.
func writeTar(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArchiveScheme(t *testing.T) {
	t.Parallel()
	docker := writeTar(t, map[string]string{"manifest.json": `[{"Config":"c.json","RepoTags":["x:1"],"Layers":["l.tar"]}]`, "c.json": "{}", "l.tar": ""})
	both := writeTar(t, map[string]string{"./manifest.json": "[]", "oci-layout": `{"imageLayoutVersion":"1.0.0"}`, "index.json": "{}"})
	oci := writeTar(t, map[string]string{"oci-layout": `{"imageLayoutVersion":"1.0.0"}`, "index.json": "{}"})
	plain := writeTar(t, map[string]string{"README": "not an image"})

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("x"))
	_ = zw.Close()
	gzPath := filepath.Join(t.TempDir(), "image.tar.gz")
	if err := os.WriteFile(gzPath, gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	notTar := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(notTar, []byte("\x7fELF..."), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, path, want, wantErr string
	}{
		{"docker save", docker, "docker-archive", ""},
		{"docker save with an OCI layout beside it", both, "docker-archive", ""},
		{"OCI layout archive", oci, "oci-archive", ""},
		{"a tar with no image", plain, "", "neither a docker manifest.json nor an oci-layout"},
		{"gzip", gzPath, "", "gzip-compressed"},
		{"not a tar", notTar, "", "not a tar file"},
	}
	for _, tc := range cases {
		got, err := ArchiveScheme(tc.path)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.wantErr != "" && (err == nil || !errors.Is(err, ErrNotArchive) || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: err = %v, want ErrNotArchive saying %q", tc.name, err, tc.wantErr)
		case got != tc.want:
			t.Errorf("%s: scheme = %q, want %q", tc.name, got, tc.want)
		}
	}
}

type recordingRunner struct {
	args [][]string
	out  []byte
}

func (r *recordingRunner) Run(_ context.Context, _ string, args, _ []string) ([]byte, error) {
	r.args = append(r.args, args)
	return r.out, nil
}

func TestGenerateArchiveReadsTheFile(t *testing.T) {
	t.Parallel()
	p := writeTar(t, map[string]string{"manifest.json": "[]"})
	r := &recordingRunner{out: []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`)}
	g := &Generator{Tool: func(context.Context) (string, string, error) { return "/fake/syft", "v1.57.2", nil }, Runner: r}
	res := g.Generate(context.Background(), []Target{{Name: "api", Kind: KindArchive, Ref: p}})[0]
	if !res.Ref.HasSBOM() || res.Ref.Source != "image" {
		t.Fatalf("result = %+v", res.Ref)
	}
	if len(r.args) != 1 || r.args[0][1] != "docker-archive:"+p {
		t.Fatalf("syft args = %v, want a docker-archive source", r.args)
	}
	// The streamed file digest is the digest of the bytes: the cache key a
	// cut and this agree on.
	if got, err := fileDigest(p); err != nil || got != Digest(mustRead(t, p)) {
		t.Fatalf("fileDigest = %q, %v; want %q", got, err, Digest(mustRead(t, p)))
	}

	bad := writeTar(t, map[string]string{"README": ""})
	res = g.Generate(context.Background(), []Target{{Name: "api", Kind: KindArchive, Ref: bad}})[0]
	if res.Ref.HasSBOM() || !strings.Contains(res.Ref.Reason, "neither a docker manifest.json") {
		t.Fatalf("a tar with no image = %+v, want unavailable saying why", res.Ref)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// dockerSaveTar writes a minimal but valid `docker save` archive: one layer
// holding an installed npm package, its config naming that layer's diff id.
func dockerSaveTar(t *testing.T) string {
	t.Helper()
	tarOf := func(files map[string]string) []byte {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for name, body := range files {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			_, _ = tw.Write([]byte(body))
		}
		_ = tw.Close()
		return buf.Bytes()
	}
	layer := tarOf(map[string]string{"usr/lib/node_modules/left-pad/package.json": `{"name":"left-pad","version":"1.3.0","license":"WTFPL"}`})
	layerID := strings.TrimPrefix(Digest(layer), "sha256:")
	config := `{"architecture":"amd64","os":"linux","config":{},"rootfs":{"type":"layers","diff_ids":["sha256:` + layerID + `"]}}`
	configName := strings.TrimPrefix(Digest([]byte(config)), "sha256:") + ".json"
	manifest := `[{"Config":"` + configName + `","RepoTags":["api:dev"],"Layers":["` + layerID + `/layer.tar"]}]`
	p := filepath.Join(t.TempDir(), "api.tar")
	if err := os.WriteFile(p, tarOf(map[string]string{"manifest.json": manifest, configName: config, layerID + "/layer.tar": string(layer)}), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The real pinned syft reads a `docker save` tarball by the scheme
// ArchiveScheme names, with no daemon and no registry.
func TestGenerateArchive_RealSyft(t *testing.T) {
	testkit.Slow(t, "execs the pinned syft binary on an image archive")
	installed, ok := toolfetch.Find(toolfetch.Syft)
	if !ok {
		t.Skip("forge's pinned syft is not installed on this machine; this test never downloads it")
	}
	p := dockerSaveTar(t)
	g := &Generator{Tool: func(context.Context) (string, string, error) { return installed.Path, installed.Version, nil }}
	res := g.Generate(context.Background(), []Target{{Name: "api", Kind: KindArchive, Ref: p}})[0]
	if !res.Ref.HasSBOM() {
		t.Fatalf("%+v", res.Ref)
	}
	if !bytes.Contains(res.Bytes, []byte(`"left-pad"`)) {
		t.Errorf("the image's npm package is not in its SBOM:\n%s", res.Bytes)
	}
}
