// Package nodeengine refuses to run npm in a frontend on a Node the
// frontend's package.json says it does not support.
//
// npm treats `engines.node` as advice: it prints EBADENGINE and installs
// anyway. On a Node below the scaffold's floor that install does not fail
// cleanly. npm 10.8 on Node 20 crashes resolving the scaffolded frontend's
// tree (vite 8, vitest 5):
//
//	npm error Cannot read properties of null (reading 'edgesOut')
//
// which names neither Node nor the floor, and sends the reader to npm bug
// trackers. Checking the declared range first turns that into one line that
// names the two versions and the fix.
package nodeengine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// nodeVersion reports the version of the node on PATH ("v22.12.0"). A var so
// tests can stand in a version without installing one.
var nodeVersion = func() (string, error) {
	out, err := exec.Command("node", "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Check returns an error when the node on PATH does not satisfy the
// `engines.node` range that dir/package.json declares.
//
// It returns nil whenever it cannot judge: no node on PATH (npm's own error
// is clearer then), no package.json, no `engines.node`, or a range in a form
// Check does not read. Refusing on a range it misread would block a working
// install, which is worse than the crash it exists to explain.
func Check(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if json.Unmarshal(raw, &pkg) != nil || strings.TrimSpace(pkg.Engines.Node) == "" {
		return nil
	}
	have, err := nodeVersion()
	if err != nil {
		return nil
	}
	ok, judged := Satisfies(have, pkg.Engines.Node)
	if !judged || ok {
		return nil
	}
	return fmt.Errorf("%s requires Node %s (engines.node in its package.json), and the node on PATH is %s. "+
		"npm on an older Node fails installing this tree, often as \"Cannot read properties of null (reading 'edgesOut')\". "+
		"Install a Node that satisfies %s (for example `nvm install 22`), then re-run",
		dir, pkg.Engines.Node, have, pkg.Engines.Node)
}

// Satisfies reports whether version ("v22.12.0" or "22.12.0") satisfies the
// npm range rng, and whether it could judge at all. It reads the forms forge
// scaffolds and the toolchains it pins declare: alternatives joined by "||",
// each ">=X[.Y[.Z]]", "^X[.Y[.Z]]" or a bare "X[.Y[.Z]]" (the same major and
// at least that version). Any other form makes the whole range unjudged.
func Satisfies(version, rng string) (ok, judged bool) {
	have, valid := parseVersion(strings.TrimPrefix(strings.TrimSpace(version), "v"))
	if !valid {
		return false, false
	}
	for _, alt := range strings.Split(rng, "||") {
		alt = strings.TrimSpace(alt)
		var sameMajor bool
		switch {
		case strings.HasPrefix(alt, ">="):
			alt = strings.TrimSpace(strings.TrimPrefix(alt, ">="))
		case strings.HasPrefix(alt, "^"):
			alt, sameMajor = strings.TrimPrefix(alt, "^"), true
		default:
			sameMajor = true
		}
		floor, valid := parseVersion(alt)
		if !valid {
			return false, false
		}
		if compare(have, floor) >= 0 && (!sameMajor || have[0] == floor[0]) {
			ok = true
		}
	}
	return ok, true
}

// parseVersion reads "X", "X.Y" or "X.Y.Z" (missing parts are 0).
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(s, ".")
	if s == "" || len(parts) > 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func compare(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
