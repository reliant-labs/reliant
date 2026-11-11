package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The setup action's tool installs (.github/actions/setup): the downloader
// fetch-release-asset.sh against a fake curl, and the Install Task / buf /
// ripgrep steps against a stub downloader. Both scripts are read in-process
// and run from t.TempDir(), so Go's test cache sees every input.

const setupDir = ".github/actions/setup"

// fakeCurl serves $FAKE_WEB/<url with / and : as _> and exits 22 (curl -f's
// HTTP error) for any other URL. Every call's URL and headers go to $CURL_LOG.
const fakeCurl = `#!/usr/bin/env bash
out="" url="" hdrs=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift ;;
    -H) hdrs="$hdrs|$2"; shift ;;
    --retry|--retry-delay|--connect-timeout|--max-time) shift ;;
    https://*) url="$1" ;;
  esac
  shift
done
echo "$url$hdrs" >>"$CURL_LOG"
f="$FAKE_WEB/$(printf '%s' "$url" | tr '/:' '__')"
[ -f "$f" ] || exit 22
if [ -n "$out" ]; then cat "$f" >"$out"; else cat "$f"; fi
`

func webKey(url string) string { return strings.NewReplacer("/", "_", ":", "_").Replace(url) }

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readRepo(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFetchReleaseAsset(t *testing.T) {
	for _, tool := range []string{"bash", "jq", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	script := readRepo(t, filepath.Join(setupDir, "fetch-release-asset.sh"))

	const (
		repo, tag, asset = "acme/tool", "v1.2.3", "tool-linux"
		direct           = "https://github.com/acme/tool/releases/download/v1.2.3/tool-linux"
		meta             = "https://api.github.com/repos/acme/tool/releases/tags/v1.2.3"
		apiAsset         = "https://api.github.com/repos/acme/tool/releases/assets/42"
	)
	good := []byte("the real tool bytes\n")
	goodSum := sha256Hex(good)
	release := func(digest string) []byte {
		a := map[string]any{"name": asset, "id": 42}
		if digest != "" {
			a["digest"] = digest
		}
		b, _ := json.Marshal(map[string]any{"assets": []any{map[string]any{"name": "other", "id": 7}, a}})
		return b
	}

	for _, tc := range []struct {
		name    string
		web     map[string][]byte // url -> body; any other url fails
		pin     string
		wantErr string   // substring of the output when the script must fail
		wantGET []string // the urls curl was asked for, in order
	}{
		{
			name:    "direct download with a pinned sha",
			web:     map[string][]byte{direct: good},
			pin:     goodSum,
			wantGET: []string{direct},
		},
		{
			name:    "direct download fails, the API serves the asset",
			web:     map[string][]byte{meta: release(""), apiAsset: good},
			pin:     goodSum,
			wantGET: []string{direct, meta, apiAsset},
		},
		{
			name:    "pinned sha mismatch is refused",
			web:     map[string][]byte{direct: []byte("corrupt")},
			pin:     goodSum,
			wantErr: "FAILED",
			wantGET: []string{direct},
		},
		{
			name:    "no pin: the API's recorded digest verifies the asset",
			web:     map[string][]byte{direct: good, meta: release("sha256:" + goodSum)},
			wantGET: []string{direct, meta},
		},
		{
			name:    "no pin: corrupt bytes fail the API's digest",
			web:     map[string][]byte{direct: []byte("corrupt"), meta: release("sha256:" + goodSum)},
			wantErr: "FAILED",
			wantGET: []string{direct, meta},
		},
		{
			name:    "no pin and no recorded digest is refused",
			web:     map[string][]byte{direct: good, meta: release("")},
			wantErr: "no pinned sha256 and the API records no digest",
			wantGET: []string{direct, meta},
		},
		{
			name:    "direct and API both fail",
			web:     map[string][]byte{},
			pin:     goodSum,
			wantGET: []string{direct, meta},
			wantErr: "fetching it through the API",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			bin, web := filepath.Join(dir, "bin"), filepath.Join(dir, "web")
			for _, d := range []string{bin, web} {
				if err := os.Mkdir(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeExec(t, filepath.Join(bin, "curl"), fakeCurl)
			writeExec(t, filepath.Join(dir, "fetch.sh"), string(script))
			for url, body := range tc.web {
				if err := os.WriteFile(filepath.Join(web, webKey(url)), body, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			out, log := filepath.Join(dir, "out"), filepath.Join(dir, "curl.log")
			args := []string{filepath.Join(dir, "fetch.sh"), repo, tag, asset, out}
			if tc.pin != "" {
				args = append(args, tc.pin)
			}
			cmd := exec.CommandContext(t.Context(), "bash", args...)
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"),
				"FAKE_WEB="+web, "CURL_LOG="+log, "GH_TOKEN=tok")
			output, err := cmd.CombinedOutput()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("fetch failed: %v\n%s", err, output)
				}
				if got, _ := os.ReadFile(out); !bytes.Equal(got, good) {
					t.Errorf("out = %q, want %q", got, good)
				}
			} else {
				if err == nil {
					t.Fatalf("fetch succeeded, want a failure mentioning %q\n%s", tc.wantErr, output)
				}
				if !strings.Contains(string(output), tc.wantErr) {
					t.Errorf("output lacks %q:\n%s", tc.wantErr, output)
				}
			}

			raw, _ := os.ReadFile(log)
			var got []string
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				url, hdrs, _ := strings.Cut(line, "|")
				// meta is fetched once per release_meta call; compare distinct urls.
				if len(got) == 0 || got[len(got)-1] != url {
					got = append(got, url)
				}
				if strings.HasPrefix(url, "https://api.github.com/") && !strings.Contains(hdrs, "Authorization: Bearer tok") {
					t.Errorf("API call %s sent no GH_TOKEN: %q", url, hdrs)
				}
				if strings.HasPrefix(url, "https://github.com/") && strings.Contains(hdrs, "Authorization") {
					t.Errorf("github.com download %s carries the token", url)
				}
			}
			if strings.Join(got, "\n") != strings.Join(tc.wantGET, "\n") {
				t.Errorf("curl fetched\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(tc.wantGET, "\n  "))
			}
		})
	}
}

func tgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestSetupInstallsCachedTools runs each Install step of the setup action on a
// cache miss (the downloader is called once, with the pinned sha where there is
// one, and the binary lands in the directory the cache steps save) and on a
// cache hit (nothing is downloaded).
func TestSetupInstallsCachedTools(t *testing.T) {
	for _, tool := range []string{"bash", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	var action struct {
		Runs struct {
			Steps []ciStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(readRepo(t, filepath.Join(setupDir, "action.yml")), &action); err != nil {
		t.Fatal(err)
	}
	step := func(name string) ciStep {
		for _, s := range action.Runs.Steps {
			if s.Name == name {
				return s
			}
		}
		t.Fatalf("setup action has no step %q", name)
		return ciStep{}
	}
	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)
	const rgDir = "ripgrep-14.1.1-x86_64-unknown-linux-musl"
	const glDir = "golangci-lint-2.14.0-linux-amd64"

	for _, tc := range []struct {
		install, restore, save string
		cacheDir, binary       string // under $HOME/.cache/ci-tools
		asset                  string
		fixture                []byte
		wantFetch              []string // repo, tag, asset (the out path is not compared)
		pinned                 bool
		onPath                 bool   // exported through $GITHUB_PATH
		sudo                   string // the sudo command it must run, if any
	}{
		{
			install: "Install Task", restore: "Restore Task", save: "Save Task (main only)",
			cacheDir: "task-3.54.0", binary: "task",
			asset: "task_linux_amd64.tar.gz", fixture: tgz(t, map[string]string{"task": "#!/bin/sh\necho task\n", "LICENSE": "x"}),
			wantFetch: []string{"go-task/task", "v3.54.0", "task_linux_amd64.tar.gz"}, pinned: true, onPath: true,
		},
		{
			install: "Install buf", restore: "Restore buf", save: "Save buf (main only)",
			cacheDir: "buf-1.50.0", binary: "buf",
			asset: "buf-Linux-x86_64", fixture: []byte("#!/bin/sh\necho buf\n"),
			wantFetch: []string{"bufbuild/buf", "v1.50.0", "buf-Linux-x86_64"}, onPath: true,
		},
		{
			install: "Install ripgrep", restore: "Restore ripgrep", save: "Save ripgrep (main only)",
			cacheDir: "rg-14.1.1", binary: "rg",
			asset: rgDir + ".tar.gz", fixture: tgz(t, map[string]string{rgDir + "/rg": "#!/bin/sh\necho rg\n", rgDir + "/README": "x"}),
			wantFetch: []string{"BurntSushi/ripgrep", "14.1.1", rgDir + ".tar.gz"}, pinned: true,
			sudo: "install -m 0755 {dir}/rg /usr/local/bin/rg",
		},
		{
			install: "Install golangci-lint", restore: "Restore golangci-lint", save: "Save golangci-lint (main only)",
			cacheDir: "golangci-lint-2.14.0", binary: "golangci-lint",
			asset: glDir + ".tar.gz", fixture: tgz(t, map[string]string{glDir + "/golangci-lint": "#!/bin/sh\necho golangci-lint\n", glDir + "/LICENSE": "x"}),
			wantFetch: []string{"golangci/golangci-lint", "v2.14.0", glDir + ".tar.gz"}, onPath: true,
		},
	} {
		run := step(tc.install).Run
		// The restore and save steps must cache the directory the install writes.
		wantPath := "~/.cache/ci-tools/" + strings.NewReplacer(
			"buf-1.50.0", "buf-${{ steps.buf.outputs.version }}",
			"golangci-lint-2.14.0", "golangci-lint-${{ steps.golangci.outputs.version }}",
		).Replace(tc.cacheDir)
		for _, name := range []string{tc.restore, tc.save} {
			if got := step(name).With["path"]; got != wantPath {
				t.Errorf("%s caches %v, want %s", name, got, wantPath)
			}
		}
		if step(tc.restore).With["key"] != step(tc.save).With["key"] {
			t.Errorf("%s and %s use different keys", tc.restore, tc.save)
		}
		if !strings.Contains(step(tc.save).If, "github.ref == 'refs/heads/main'") {
			t.Errorf("%s must save on main only, if: %s", tc.save, step(tc.save).If)
		}

		for _, hit := range []bool{false, true} {
			name := tc.install + "/miss"
			if hit {
				name = tc.install + "/hit"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				home, tmp, bin, actionDir := filepath.Join(dir, "home"), filepath.Join(dir, "tmp"), filepath.Join(dir, "bin"), filepath.Join(dir, "action")
				for _, d := range []string{home, tmp, bin, actionDir} {
					if err := os.Mkdir(d, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				fixture := filepath.Join(dir, "fixture")
				if err := os.WriteFile(fixture, tc.fixture, 0o644); err != nil {
					t.Fatal(err)
				}
				fetchLog, sudoLog, ghPath := filepath.Join(dir, "fetch.log"), filepath.Join(dir, "sudo.log"), filepath.Join(dir, "github_path")
				// The stub downloader records its arguments and writes the fixture.
				writeExec(t, filepath.Join(actionDir, "fetch-release-asset.sh"),
					"#!/usr/bin/env bash\necho \"$*\" >>"+fetchLog+"\ncp "+fixture+" \"$4\"\n")
				writeExec(t, filepath.Join(bin, "sudo"), "#!/usr/bin/env bash\necho \"$*\" >>"+sudoLog+"\n")
				for _, net := range []string{"curl", "wget"} {
					writeExec(t, filepath.Join(bin, net), "#!/bin/sh\necho unexpected network call >&2\nexit 99\n")
				}

				cache := filepath.Join(home, ".cache", "ci-tools", tc.cacheDir)
				cached := "#!/bin/sh\necho restored\n"
				if hit {
					if err := os.MkdirAll(cache, 0o755); err != nil {
						t.Fatal(err)
					}
					writeExec(t, filepath.Join(cache, tc.binary), cached)
				}

				script := strings.ReplaceAll(run, "${{ github.action_path }}", actionDir)
				if strings.Contains(script, "${{") {
					t.Fatalf("%s has an unsubstituted expression:\n%s", tc.install, script)
				}
				cmd := exec.CommandContext(t.Context(), "bash", "-e", "-c", script)
				cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "HOME="+home,
					"RUNNER_TEMP="+tmp, "GITHUB_PATH="+ghPath, "GH_TOKEN=tok", "BUF_VERSION=1.50.0", "V=2.14.0")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s: %v\n%s", tc.install, err, out)
				}

				fetched, _ := os.ReadFile(fetchLog)
				if hit {
					if len(fetched) != 0 {
						t.Errorf("cache hit downloaded: %s", fetched)
					}
				} else {
					lines := strings.Split(strings.TrimSpace(string(fetched)), "\n")
					if len(lines) != 1 {
						t.Fatalf("downloader called %d times, want 1: %q", len(lines), fetched)
					}
					args := strings.Fields(lines[0])
					if len(args) < 4 || strings.Join(args[:3], " ") != strings.Join(tc.wantFetch, " ") {
						t.Errorf("downloader args %q, want %q first", args, tc.wantFetch)
					}
					if tc.pinned && (len(args) != 5 || !sha.MatchString(args[4])) {
						t.Errorf("downloader args %q: want a pinned sha256 as the 5th", args)
					}
					if !tc.pinned && len(args) != 4 {
						t.Errorf("downloader args %q: want no pin (the API's digest checks it)", args)
					}
				}

				got, err := os.ReadFile(filepath.Join(cache, tc.binary))
				if err != nil {
					t.Fatalf("no cached binary: %v", err)
				}
				if info, _ := os.Stat(filepath.Join(cache, tc.binary)); info.Mode()&0o111 == 0 {
					t.Errorf("cached %s is not executable", tc.binary)
				}
				want := cached
				if !hit {
					want = map[string]string{"task": "#!/bin/sh\necho task\n", "buf": "#!/bin/sh\necho buf\n", "rg": "#!/bin/sh\necho rg\n", "golangci-lint": "#!/bin/sh\necho golangci-lint\n"}[tc.binary]
				}
				if string(got) != want {
					t.Errorf("cached %s = %q, want %q", tc.binary, got, want)
				}
				entries, _ := os.ReadDir(cache)
				if len(entries) != 1 {
					var names []string
					for _, e := range entries {
						names = append(names, e.Name())
					}
					t.Errorf("cache dir holds %v, want only the binary", names)
				}

				paths, _ := os.ReadFile(ghPath)
				if onPath := strings.TrimSpace(string(paths)) == cache; onPath != tc.onPath {
					t.Errorf("GITHUB_PATH = %q, want %s exported: %v", paths, cache, tc.onPath)
				}
				sudo, _ := os.ReadFile(sudoLog)
				if wantSudo := strings.ReplaceAll(tc.sudo, "{dir}", cache); strings.TrimSpace(string(sudo)) != wantSudo {
					t.Errorf("sudo ran %q, want %q", sudo, wantSudo)
				}
			})
		}
	}
}
