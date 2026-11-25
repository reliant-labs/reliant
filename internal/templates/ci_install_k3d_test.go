package templates

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// installK3dSteps returns the `run:` body of every "Install k3d" step in a
// rendered workflow.
func installK3dSteps(t *testing.T, workflow []byte) []string {
	t.Helper()
	var parsed struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(workflow, &parsed); err != nil {
		t.Fatalf("workflow is not valid YAML: %v\n%s", err, workflow)
	}
	var runs []string
	for _, job := range parsed.Jobs {
		for _, step := range job.Steps {
			if step.Name == "Install k3d" {
				runs = append(runs, step.Run)
			}
		}
	}
	return runs
}

// Every scaffolded workflow that needs k3d installs the pinned release binary
// through installK3dScript, byte for byte: one recipe, so a pin bump or a fix
// cannot land in one workflow and miss the other.
func TestCIWorkflows_InstallPinnedK3d(t *testing.T) {
	render := func(name string, data any) []byte {
		t.Helper()
		out, err := CITemplates("github").Render(name, data)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		return out
	}
	for name, wf := range map[string][]byte{
		"ci.yml":  render("ci.yml.tmpl", fullCIData()),
		"e2e.yml": render("e2e.yml.tmpl", E2EWorkflowData{ProjectName: "demo", Runtime: "k3d"}),
	} {
		steps := installK3dSteps(t, wf)
		if len(steps) != 1 {
			t.Errorf("%s has %d Install k3d steps, want 1", name, len(steps))
		}
		for _, s := range steps {
			if s != installK3dScript() {
				t.Errorf("%s Install k3d step is not installK3dScript:\n%s", name, s)
			}
		}
		if strings.Contains(string(wf), "install.sh") {
			t.Errorf("%s still runs k3d's install.sh", name)
		}
	}

	script := installK3dScript()
	for _, want := range []string{
		"releases/download/" + K3dVersion + "/k3d-linux-${arch}",
		k3dLinuxSHA256["amd64"], k3dLinuxSHA256["arm64"],
		`sha256sum -c -`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("installK3dScript lacks %q:\n%s", want, script)
		}
	}
	// The checksum is checked BEFORE the binary is installed.
	if strings.Index(script, "sha256sum -c") > strings.Index(script, "install -m 0755") {
		t.Errorf("installK3dScript installs k3d before verifying it:\n%s", script)
	}
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Each pinned checksum is a sha256, and forge's own e2e suite installs the
// same k3d release with the same amd64 checksum, so the k3d a generated
// pipeline runs is the one forge was tested with.
func TestK3dPinMatchesForgesOwnCI(t *testing.T) {
	for arch, sum := range k3dLinuxSHA256 {
		if !sha256Hex.MatchString(sum) {
			t.Errorf("k3dLinuxSHA256[%s] = %q is not a sha256", arch, sum)
		}
	}
	suite, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "e2e-suite.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"K3D_VERSION: " + K3dVersion, "K3D_SHA256: " + k3dLinuxSHA256["amd64"]} {
		if !strings.Contains(string(suite), want) {
			t.Errorf(".github/workflows/e2e-suite.yml lacks %q: move forge's own k3d pin with K3dVersion", want)
		}
	}
}

// Exercise both architecture branches without network or root privileges. Only
// the expected digest is replaced with a fixture digest; verification still
// uses real sha256sum, and sudo redirects the real install into the sandbox.
func TestInstallK3dScript_InstallsVerifiedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux runner script requires POSIX paths and executable shell stubs; Git Bash is not a POSIX host")
	}
	for _, runner := range []struct {
		machine string
		arch    string
	}{
		{"x86_64", "amd64"},
		{"aarch64", "arm64"},
		{"arm64", "arm64"},
	} {
		t.Run(runner.machine, func(t *testing.T) {
			bash, err := exec.LookPath("bash")
			if err != nil {
				t.Skip("bash not on PATH")
			}
			for _, tool := range []string{"sha256sum", "install"} {
				if _, err := exec.LookPath(tool); err != nil {
					t.Skipf("%s not on PATH (coreutils)", tool)
				}
			}
			bin := t.TempDir()
			tmp := t.TempDir()
			fixture := []byte("#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = version ] || exit 1\necho verified-k3d-version\n")
			fixturePath := filepath.Join(tmp, "fixture")
			if err := os.WriteFile(fixturePath, fixture, 0o600); err != nil {
				t.Fatal(err)
			}
			stubs := map[string]string{
				"uname": "#!/bin/sh\necho " + runner.machine + "\n",
				"curl": `#!/bin/sh
set -eu
[ "$#" -eq 3 ] && [ "$1" = -fsSLo ] && [ "$3" = "$EXPECTED_URL" ]
cp "$FIXTURE" "$2"
`,
				"sudo": `#!/bin/sh
set -eu
[ "$#" -eq 5 ] && [ "$1" = install ] && [ "$2" = -m ] && [ "$3" = 0755 ] && [ "$5" = /usr/local/bin/k3d ]
install -m 0755 "$4" "$INSTALL_BIN/k3d"
`,
			}
			for name, body := range stubs {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			script := strings.ReplaceAll(installK3dScript(), k3dLinuxSHA256[runner.arch], fmt.Sprintf("%x", sha256.Sum256(fixture)))
			cmd := exec.Command(bash, "-c", script)
			cmd.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+"/usr/bin:/bin",
				"RUNNER_TEMP="+tmp, "FIXTURE="+fixturePath, "INSTALL_BIN="+bin,
				"EXPECTED_URL="+k3dReleaseURL(runner.arch))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("verified installation failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), ": OK") || !strings.Contains(string(out), "verified-k3d-version") {
				t.Fatalf("expected real checksum verification and installed binary execution:\n%s", out)
			}
			installed, err := os.ReadFile(filepath.Join(bin, "k3d"))
			if err != nil || string(installed) != string(fixture) {
				t.Fatalf("installed bytes differ from verified fixture: %v", err)
			}
		})
	}
}

// The script refuses a download whose sha256 is not the pinned one, and never
// installs it. Run against stub curl/uname/sudo on PATH; sha256sum is real.
func TestInstallK3dScript_RefusesAMismatchedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux runner script requires POSIX paths and executable shell stubs; Git Bash is not a POSIX host")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	sha, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Skip("sha256sum not on PATH (coreutils); the runners the script targets have it")
	}
	bin := t.TempDir()
	tmp := t.TempDir()
	installed := filepath.Join(tmp, "sudo-ran")
	stubs := map[string]string{
		"uname": "#!/bin/sh\necho x86_64\n",
		// curl -fsSLo <file> <url>: write something that is not k3d.
		"curl": "#!/bin/sh\nwhile [ $# -gt 0 ]; do case \"$1\" in -fsSLo) shift; out=$1 ;; esac; shift; done\necho not-k3d > \"$out\"\n",
		"sudo": "#!/bin/sh\ntouch " + installed + "\n",
		"k3d":  "#!/bin/sh\necho k3d\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(sha, filepath.Join(bin, "sha256sum")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, "-c", installK3dScript())
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+"/usr/bin:/bin", "RUNNER_TEMP="+tmp)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the script accepted a binary whose sha256 is not the pinned one:\n%s", out)
	}
	if !strings.Contains(string(out), "FAILED") {
		t.Errorf("the script failed, but not at the checksum (sha256sum -c reports FAILED):\n%s", out)
	}
	if _, statErr := os.Stat(installed); statErr == nil {
		t.Errorf("the script installed the unverified binary:\n%s", out)
	}
}
