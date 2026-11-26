package templates

import (
	"fmt"
	"strings"
)

// K3dVersion is the k3d release forge's scaffolded workflows and `forge
// doctor` install: the release forge's own e2e suite runs on
// (.github/workflows/e2e-suite.yml), so the generated pipeline runs the k3d
// forge was tested with.
//
// It is installed as the release's binary, verified against the sha256 the
// release publishes (its checksums.txt), never by piping k3d's install.sh from
// its main branch into bash: that runs whatever main holds on the day, and the
// k3d it installs moves under the project when k3d releases.
//
// To move the pin, set K3dVersion and replace k3dLinuxSHA256 with the new
// release's checksums.txt entries for the same architectures.
const K3dVersion = "v5.8.3"

// k3dLinuxSHA256 is the sha256 of each linux binary of K3dVersion, keyed by
// Go architecture name, from the release's checksums.txt.
var k3dLinuxSHA256 = map[string]string{
	"amd64": "dbaa79a76ace7f4ca230a1ff41dc7d8a5036a8ad0309e9c54f9bf3836dbe853e",
	"arm64": "0b8110f2229631af7402fb828259330985918b08fefd38b7f1b788a1c8687216",
}

// k3dReleaseURL is where K3dVersion's binary for linux/arch is published.
func k3dReleaseURL(arch string) string {
	return fmt.Sprintf("https://github.com/k3d-io/k3d/releases/download/%s/k3d-linux-%s", K3dVersion, arch)
}

// installK3dScript is the step body every scaffolded workflow that needs k3d
// runs (ci.yml's and e2e.yml's k3d e2e jobs), through the `installK3dRun`
// template func. It picks the runner's architecture, downloads that release
// binary, and refuses it unless its sha256 is the pinned one.
func installK3dScript() string {
	return fmt.Sprintf(`set -euo pipefail
case "$(uname -m)" in
  x86_64) arch=amd64 sum=%s ;;
  aarch64|arm64) arch=arm64 sum=%s ;;
  *) echo "::error::no pinned k3d %s binary for $(uname -m)"; exit 1 ;;
esac
bin="${RUNNER_TEMP:-/tmp}/k3d"
curl -fsSLo "$bin" "https://github.com/k3d-io/k3d/releases/download/%s/k3d-linux-${arch}"
echo "${sum}  ${bin}" | sha256sum -c -
sudo install -m 0755 "$bin" /usr/local/bin/k3d
k3d version
`, k3dLinuxSHA256["amd64"], k3dLinuxSHA256["arm64"], K3dVersion, K3dVersion)
}

// installK3dRun renders installK3dScript as a YAML `run: |` block whose key
// sits at `indent` spaces (the step's key column), like installForgeRun.
func installK3dRun(indent int) string {
	key := strings.Repeat(" ", indent)
	body := key + "  "
	var b strings.Builder
	b.WriteString(key + "run: |\n")
	lines := strings.Split(strings.TrimRight(installK3dScript(), "\n"), "\n")
	for i, line := range lines {
		b.WriteString(body + line)
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// K3dLinuxInstallHint is the one-line install `forge doctor` prints for k3d
// on linux/arch: the pinned release binary, checked against its sha256, then
// installed. "" for an architecture forge pins no checksum for (the caller
// falls back to the upstream installation page).
func K3dLinuxInstallHint(arch string) string {
	sum, ok := k3dLinuxSHA256[arch]
	if !ok {
		return ""
	}
	return fmt.Sprintf(`curl -fsSLo k3d %s && echo "%s  k3d" | sha256sum -c - && sudo install -m 0755 k3d /usr/local/bin/k3d && rm k3d`,
		k3dReleaseURL(arch), sum)
}
