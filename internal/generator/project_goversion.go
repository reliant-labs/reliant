package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/goexec"
)

const defaultGoVersion = "1.27.0"

// detectGoVersion returns the host Go version from `go env GOVERSION`
// (for example, "1.26.1"). It trusts the installed toolchain and only falls
// back to defaultGoVersion when the local version cannot be detected.
func detectGoVersion() string {
	cmd := exec.Command("go", "env", "GOVERSION")
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	if err != nil {
		return defaultGoVersion
	}

	v := strings.TrimSpace(string(out))
	v = strings.TrimPrefix(v, "go")
	if v == "" || strings.HasPrefix(v, "devel") {
		return defaultGoVersion
	}

	return strings.TrimRight(v, ".")
}

// parseGoVersion extracts major, minor, and patch from a version string.
// Accepts "1.24", "1.24.3". Returns ok=false if the format is invalid.
func parseGoVersion(v string) (major, minor, patch int, ok bool) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, 0, false
	}

	n, err := fmt.Sscanf(parts[0], "%d", &major)
	if err != nil || n != 1 {
		return 0, 0, 0, false
	}

	n, err = fmt.Sscanf(parts[1], "%d", &minor)
	if err != nil || n != 1 {
		return 0, 0, 0, false
	}

	if len(parts) == 3 {
		n, err = fmt.Sscanf(parts[2], "%d", &patch)
		if err != nil || n != 1 {
			return 0, 0, 0, false
		}
	}

	return major, minor, patch, true
}

// goVersionMinor returns the major.minor portion (e.g. "1.25.0" -> "1.25").
func goVersionMinor(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return v
}

// latestDockerHubGoMinor is the newest major.minor Go tag we expect to exist
// on Docker Hub as `golang:<minor>-alpine`. Docker Hub tends to lag upstream
// Go releases by days to weeks; pinning the scaffolded Dockerfile base to
// this value avoids `manifest unknown` errors while Go's toolchain manager
// (GOTOOLCHAIN=auto) transparently fetches any newer toolchain required by
// `go.mod`.
//
// Bump this constant when a newer Go minor is published on Docker Hub.
const latestDockerHubGoMinor = "1.27"

// dockerBuilderGoVersion returns the Go version tag to use as the builder
// image base. It prefers the project's declared Go minor version when that
// image is already known to exist on Docker Hub, otherwise it falls back to
// the latest publicly available minor. Go's toolchain manager will upgrade
// the compiler inside the image to match go.mod's `go <version>` directive
// at build time when needed.
func dockerBuilderGoVersion(projectGoVersion string) string {
	minor := goVersionMinor(projectGoVersion)
	if !dockerHubHasGoMinor(minor) {
		return latestDockerHubGoMinor
	}
	return minor
}

// dockerHubHasGoMinor reports whether a `golang:<minor>-alpine` image is
// expected to exist on Docker Hub. It compares the minor against the
// latest known-good minor (latestDockerHubGoMinor).
func dockerHubHasGoMinor(minor string) bool {
	wantMajor, wantMinor, _, ok := parseGoVersion(minor)
	if !ok {
		return false
	}
	maxMajor, maxMinor, _, ok := parseGoVersion(latestDockerHubGoMinor)
	if !ok {
		return false
	}
	if wantMajor != maxMajor {
		return wantMajor < maxMajor
	}
	return wantMinor <= maxMinor
}

// goVersionFromGoMod reads the Go version from <projectDir>/go.mod's `go`
// directive. Returns "" when projectDir is empty or the file can't be read
// or parsed. The returned value matches go.mod syntax (e.g. "1.26.1").
func goVersionFromGoMod(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "go ") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "go "))
		if _, _, _, ok := parseGoVersion(v); ok {
			return v
		}
	}
	return ""
}

// resolveGoVersion returns the Go version to use, preferring the override if set.
// The returned value is clamped to defaultGoVersion when lower, since forge's
// generated code (pkg/orm and friends) requires that minimum.
func (g *ProjectGenerator) resolveGoVersion() string {
	v := detectGoVersion()
	if g.GoVersionOverride != "" {
		ov := g.GoVersionOverride
		parts := strings.SplitN(ov, ".", 3)
		if len(parts) == 2 {
			ov += ".0"
		}
		if _, _, _, ok := parseGoVersion(ov); ok {
			v = ov
		} else {
			fmt.Fprintf(os.Stderr, "⚠️  Invalid --go-version %q. Using detected version instead.\n", g.GoVersionOverride)
		}
	}
	if compareGoVersion(v, defaultGoVersion) < 0 {
		fmt.Fprintf(os.Stderr, "ℹ️  Go version %s is below the forge minimum %s; pinning go.work/go.mod to %s. The Go toolchain (GOTOOLCHAIN=auto) will fetch the required compiler at build time.\n", v, defaultGoVersion, defaultGoVersion)
		return defaultGoVersion
	}
	return v
}

// compareGoVersion returns -1, 0, or 1 if a is older than, equal to, or newer
// than b. Unparseable inputs are treated as 0.0.0.
func compareGoVersion(a, b string) int {
	aMaj, aMin, aPatch, _ := parseGoVersion(a)
	bMaj, bMin, bPatch, _ := parseGoVersion(b)
	switch {
	case aMaj != bMaj:
		if aMaj < bMaj {
			return -1
		}
		return 1
	case aMin != bMin:
		if aMin < bMin {
			return -1
		}
		return 1
	case aPatch != bPatch:
		if aPatch < bPatch {
			return -1
		}
		return 1
	}
	return 0
}
