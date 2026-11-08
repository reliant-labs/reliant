//go:build !windows

// install-release-tool runs only on Linux runners (it pins linux-amd64
// assets) and these tests run its bash scripts, so the file builds for
// every OS but Windows rather than skipping there at run time.

package ciguard

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// .github/actions/install-release-tool installs Task, buf and KCL for every
// gate job, so a broken branch of it fails CI for a reason unrelated to the
// commit. These tests run the action's own `run:` scripts and its
// fetch-release-asset.sh under bash, against a fake curl that serves fixture
// assets: no network, and every branch (cache hit, cache miss, a cache entry
// without its binary, the API fallback, checksum refusal) is exercised.
//
// The child reads only copies of files this process opened, so the short
// tier's cache sees an edit to the action or the script.

var installReleaseToolDir = filepath.Join("..", "..", ".github", "actions", "install-release-tool")

type compositeAction struct {
	Runs struct {
		Steps []struct {
			ID   string            `yaml:"id"`
			Name string            `yaml:"name"`
			Run  string            `yaml:"run"`
			Uses string            `yaml:"uses"`
			Env  map[string]string `yaml:"env"`
		} `yaml:"steps"`
	} `yaml:"runs"`
}

// fakeCurl stands in for curl. It honours -o and the URL and ignores every
// other flag; it logs each URL to $CURL_LOG. FAKE_DIRECT / FAKE_API set to
// "fail" make the github.com download / the API's asset download fail;
// FAKE_DIGEST is the digest the release metadata records ("" for none).
const fakeCurl = `#!/usr/bin/env bash
out="" url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    https://*) url="$1" ;;
  esac
  shift
done
echo "$url" >> "$CURL_LOG"
emit() { if [ -n "$out" ]; then cp "$1" "$out"; else cat "$1"; fi; }
case "$url" in
  https://github.com/*/releases/download/*)
    [ "${FAKE_DIRECT:-ok}" = ok ] || { echo "curl: (22) The requested URL returned error: 504" >&2; exit 22; }
    emit "$FAKE_ASSETS/${url##*/}" ;;
  https://api.github.com/repos/*/releases/tags/*)
    name="$(ls "$FAKE_ASSETS")"
    if [ -n "${FAKE_DIGEST:-}" ]; then d="\"sha256:$FAKE_DIGEST\""; else d=null; fi
    printf '{"assets":[{"id":4242,"name":"%s","digest":%s}]}' "$name" "$d" ;;
  https://api.github.com/repos/*/releases/assets/4242)
    [ "${FAKE_API:-ok}" = ok ] || { echo "curl: (22) The requested URL returned error: 500" >&2; exit 22; }
    emit "$FAKE_ASSETS/$(ls "$FAKE_ASSETS")" ;;
  *) echo "fake curl: unexpected URL $url" >&2; exit 2 ;;
esac
`

type releaseToolFixture struct {
	t      *testing.T
	root   string
	action compositeAction
	env    []string
}

func newReleaseToolFixture(t *testing.T) *releaseToolFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(installReleaseToolDir, "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	f := &releaseToolFixture{t: t, root: t.TempDir()}
	if err := yaml.Unmarshal(raw, &f.action); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(installReleaseToolDir, "fetch-release-asset.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"action", "bin", "home", "tmp", "assets"} {
		if err := os.Mkdir(filepath.Join(f.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.write("action/fetch-release-asset.sh", script, 0o755)
	f.write("bin/curl", []byte(fakeCurl), 0o755)
	f.env = append(os.Environ(),
		"PATH="+filepath.Join(f.root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+filepath.Join(f.root, "home"),
		"RUNNER_TEMP="+filepath.Join(f.root, "tmp"),
		"RUNNER_OS=Linux",
		"RUNNER_ARCH=X64",
		"GITHUB_ACTION_PATH="+filepath.Join(f.root, "action"),
		"GITHUB_OUTPUT="+filepath.Join(f.root, "output"),
		"GITHUB_PATH="+filepath.Join(f.root, "path"),
		"CURL_LOG="+filepath.Join(f.root, "curl.log"),
		"FAKE_ASSETS="+filepath.Join(f.root, "assets"),
	)
	return f
}

func (f *releaseToolFixture) write(name string, body []byte, mode os.FileMode) {
	f.t.Helper()
	p := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, body, mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f *releaseToolFixture) step(prefix string) (run string, env map[string]string) {
	f.t.Helper()
	for _, s := range f.action.Runs.Steps {
		if strings.HasPrefix(s.Name, prefix) && s.Run != "" {
			return s.Run, s.Env
		}
	}
	f.t.Fatalf("install-release-tool has no run step named %q...", prefix)
	return "", nil
}

func (f *releaseToolFixture) bash(script string, extra ...string) (string, error) {
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = f.root
	cmd.Env = append(append([]string{}, f.env...), extra...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// pin runs the action's Pin step for tool and returns its outputs.
func (f *releaseToolFixture) pin(tool string, extra ...string) (map[string]string, string, error) {
	f.t.Helper()
	run, _ := f.step("Pin ")
	_ = os.Remove(filepath.Join(f.root, "output"))
	out, err := f.bash(run, append([]string{"TOOL=" + tool}, extra...)...)
	outputs := map[string]string{}
	raw, _ := os.ReadFile(filepath.Join(f.root, "output"))
	for line := range strings.SplitSeq(string(raw), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			outputs[k] = v
		}
	}
	return outputs, out, err
}

var actionExpr = regexp.MustCompile(`\$\{\{\s*([^}]+?)\s*\}\}`)

// install runs the action's Install step with the env block it declares,
// resolved against the Pin step's outputs, with the asset's pinned sha256
// replaced by the fixture's (sha256 "" keeps the pin).
func (f *releaseToolFixture) install(tool, sha256 string, extra ...string) (string, error) {
	f.t.Helper()
	outputs, out, err := f.pin(tool)
	if err != nil {
		f.t.Fatalf("pin %s: %v\n%s", tool, err, out)
	}
	if sha256 != "" {
		outputs["sha256"] = sha256
	}
	run, decl := f.step("Install ")
	env := []string{}
	for k, v := range decl {
		v = actionExpr.ReplaceAllStringFunc(v, func(m string) string {
			expr := actionExpr.FindStringSubmatch(m)[1]
			switch {
			case expr == "inputs.tool":
				return tool
			case expr == "github.token":
				return "fixture-token"
			case strings.HasPrefix(expr, "steps.pin.outputs."):
				name := strings.TrimPrefix(expr, "steps.pin.outputs.")
				if _, ok := outputs[name]; !ok {
					f.t.Fatalf("Install reads steps.pin.outputs.%s, which Pin does not write", name)
				}
				return outputs[name]
			}
			f.t.Fatalf("Install env %s=%s: expression the fixture cannot resolve", k, v)
			return ""
		})
		env = append(env, k+"="+v)
	}
	_ = os.Remove(filepath.Join(f.root, "curl.log"))
	return f.bash(run, append(env, extra...)...)
}

func (f *releaseToolFixture) dir(tool string) string {
	f.t.Helper()
	outputs, out, err := f.pin(tool)
	if err != nil {
		f.t.Fatalf("pin %s: %v\n%s", tool, err, out)
	}
	return outputs["dir"]
}

func (f *releaseToolFixture) curlLog() string {
	raw, _ := os.ReadFile(filepath.Join(f.root, "curl.log"))
	return string(raw)
}

// fixtureBinary is a stand-in tool: `<tool> --version` prints a marker.
func fixtureBinary(tool string) []byte {
	return fmt.Appendf(nil, "#!/usr/bin/env bash\necho \"fixture %s $*\"\n", tool)
}

// fixtureAsset writes the release asset tool's Pin step names, as the real
// release ships it (a tar.gz holding the member, or the bare binary), and
// returns its sha256.
func (f *releaseToolFixture) fixtureAsset(tool string) string {
	f.t.Helper()
	outputs, out, err := f.pin(tool)
	if err != nil {
		f.t.Fatalf("pin %s: %v\n%s", tool, err, out)
	}
	body := fixtureBinary(tool)
	if member := outputs["member"]; member != "-" {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, e := range []struct {
			name string
			body []byte
		}{{"LICENSE", []byte("fixture\n")}, {member, body}} {
			if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
				f.t.Fatal(err)
			}
			if _, err := tw.Write(e.body); err != nil {
				f.t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			f.t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			f.t.Fatal(err)
		}
		body = buf.Bytes()
	}
	f.write(filepath.Join("assets", outputs["asset"]), body, 0o644)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestInstallReleaseToolDownloadsAndInstallsEachTool(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"task", "buf", "kcl", "golangci-lint"} {
		t.Run(tool, func(t *testing.T) {
			t.Parallel()
			f := newReleaseToolFixture(t)
			sum := f.fixtureAsset(tool)
			out, err := f.install(tool, sum)
			if err != nil {
				t.Fatalf("install %s on a cache miss: %v\n%s", tool, err, out)
			}
			dir := f.dir(tool)
			if !strings.Contains(out, "fixture "+tool+" --version") {
				t.Errorf("install did not run the installed %s --version:\n%s", tool, out)
			}
			if st, err := os.Stat(filepath.Join(dir, tool)); err != nil || st.Mode()&0o111 == 0 {
				t.Errorf("%s is not an executable at %s: %v", tool, dir, err)
			}
			if path, _ := os.ReadFile(filepath.Join(f.root, "path")); strings.TrimSpace(string(path)) != dir {
				t.Errorf("GITHUB_PATH = %q, want %q", path, dir)
			}
			// Only the binary is cached, never the archive's other members.
			if _, err := os.Stat(filepath.Join(dir, "LICENSE")); err == nil {
				t.Errorf("the cache dir holds archive members other than %s", tool)
			}
			if log := f.curlLog(); strings.Count(log, "\n") != 1 || !strings.Contains(log, "https://github.com/") {
				t.Errorf("want one direct github.com download, curl saw:\n%s", log)
			}
		})
	}
}

func TestInstallReleaseToolFallsBackToTheAPI(t *testing.T) {
	t.Parallel()
	f := newReleaseToolFixture(t)
	sum := f.fixtureAsset("kcl")
	out, err := f.install("kcl", sum, "FAKE_DIRECT=fail")
	if err != nil {
		t.Fatalf("install with github.com failing must fall back to the API: %v\n%s", err, out)
	}
	if !strings.Contains(out, "fetching it through the API") {
		t.Errorf("no fallback warning:\n%s", out)
	}
	log := f.curlLog()
	for _, want := range []string{"https://github.com/", "/releases/tags/", "/releases/assets/4242"} {
		if !strings.Contains(log, want) {
			t.Errorf("fallback never requested %s; curl saw:\n%s", want, log)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dir("kcl"), "kcl")); err != nil {
		t.Errorf("kcl not installed after the fallback: %v", err)
	}
}

func TestInstallReleaseToolFailsWhenEverySourceFails(t *testing.T) {
	t.Parallel()
	f := newReleaseToolFixture(t)
	sum := f.fixtureAsset("task")
	if out, err := f.install("task", sum, "FAKE_DIRECT=fail", "FAKE_API=fail"); err == nil {
		t.Fatalf("install must fail when both sources fail:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.dir("task"), "task")); err == nil {
		t.Error("a binary was installed although no download succeeded")
	}
}

func TestInstallReleaseToolRefusesAChecksumMismatch(t *testing.T) {
	t.Parallel()
	f := newReleaseToolFixture(t)
	f.fixtureAsset("buf")
	// The pinned sha256 of the real release, which the fixture asset is not.
	out, err := f.install("buf", "")
	if err == nil {
		t.Fatalf("install must refuse an asset whose sha256 is not the pin:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.dir("buf"), "buf")); err == nil {
		t.Error("a binary failing its checksum was installed")
	}
	// A rerun (the next job) must not trust what the failed one left.
	if out, err := f.install("buf", ""); err == nil {
		t.Fatalf("second install after a refused checksum must fail too:\n%s", out)
	}
}

func TestFetchReleaseAssetChecksTheAPIDigestWithoutAPin(t *testing.T) {
	t.Parallel()
	f := newReleaseToolFixture(t)
	sum := f.fixtureAsset("buf")
	outputs, _, _ := f.pin("buf")
	fetch := `"$GITHUB_ACTION_PATH/fetch-release-asset.sh" bufbuild/buf ` + outputs["tag"] + ` ` + outputs["asset"] + ` "$RUNNER_TEMP/dl"`

	if out, err := f.bash(fetch, "FAKE_DIGEST="+sum); err != nil {
		t.Errorf("an asset matching the API's digest must be accepted: %v\n%s", err, out)
	}
	out, err := f.bash(fetch, "FAKE_DIGEST="+strings.Repeat("0", 64))
	if err == nil {
		t.Errorf("an asset not matching the API's digest must be refused:\n%s", out)
	}
	out, err = f.bash(fetch, "FAKE_DIGEST=")
	if err == nil || !strings.Contains(out, "no pinned sha256 and the API records no digest") {
		t.Errorf("an asset with no pin and no digest must be refused (err %v):\n%s", err, out)
	}
}

func TestInstallReleaseToolCacheHitNeverTouchesTheNetwork(t *testing.T) {
	t.Parallel()
	f := newReleaseToolFixture(t)
	dir := f.dir("task")
	f.write(filepath.Join(strings.TrimPrefix(dir, f.root+string(os.PathSeparator)), "task"), fixtureBinary("task"), 0o755)
	// No fixture asset: any download would fail.
	out, err := f.install("task", "", "FAKE_DIRECT=fail", "FAKE_API=fail")
	if err != nil {
		t.Fatalf("install from a populated cache: %v\n%s", err, out)
	}
	if log := f.curlLog(); log != "" {
		t.Errorf("a cache hit made network calls:\n%s", log)
	}
	if !strings.Contains(out, "fixture task --version") {
		t.Errorf("cached task was not run:\n%s", out)
	}
}

func TestInstallReleaseToolReinstallsACacheEntryWithoutItsBinary(t *testing.T) {
	t.Parallel()
	f := newReleaseToolFixture(t)
	sum := f.fixtureAsset("kcl")
	dir := f.dir("kcl")
	// A restored entry holding something, but not an executable kcl.
	rel := strings.TrimPrefix(dir, f.root+string(os.PathSeparator))
	f.write(filepath.Join(rel, "stale"), []byte("x"), 0o644)
	f.write(filepath.Join(rel, "kcl"), []byte("truncated"), 0o644)
	out, err := f.install("kcl", sum)
	if err != nil {
		t.Fatalf("install over a cache entry without its binary: %v\n%s", err, out)
	}
	if !strings.Contains(f.curlLog(), "https://github.com/") {
		t.Errorf("the broken cache entry was trusted, nothing downloaded")
	}
	if !strings.Contains(out, "fixture kcl --version") {
		t.Errorf("kcl was not reinstalled:\n%s", out)
	}
}

func TestInstallReleaseToolPinRefusesUnknownToolsAndRunners(t *testing.T) {
	t.Parallel()
	f := newReleaseToolFixture(t)
	if out, err := f.pinErr("rg"); err == nil {
		t.Errorf("an unknown tool must fail the step:\n%s", out)
	}
	if out, err := f.pinErr("task", "RUNNER_ARCH=ARM64"); err == nil {
		t.Errorf("a non-amd64 runner must fail the step (assets are linux-amd64):\n%s", out)
	}
	outputs, out, err := f.pin("kcl")
	if err != nil {
		t.Fatalf("pin kcl: %v\n%s", err, out)
	}
	for _, k := range []string{"repo", "tag", "asset", "sha256", "member", "dir", "key"} {
		if outputs[k] == "" {
			t.Errorf("Pin wrote no %s output", k)
		}
	}
	if !strings.Contains(outputs["key"], outputs["tag"]) {
		t.Errorf("cache key %q does not carry the version %q", outputs["key"], outputs["tag"])
	}
}

func (f *releaseToolFixture) pinErr(tool string, extra ...string) (string, error) {
	_, out, err := f.pin(tool, extra...)
	return out, err
}
