// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"gopkg.in/yaml.v3"
)

// The Node and pnpm pins a dev or CI install of frontends/web depends on:
// engines.node (enforced by pnpm-workspace.yaml's engineStrict), and the one
// pnpm version named by the packageManager fields, the web lockfile and the
// CI setup. A drift here fails a fresh install, or a release build, before any
// application test runs.

// nodeManifests are the package.json files whose engines.node a dev install
// is held to: the root (an npm project whose postinstall runs pnpm in
// frontends/web) and frontends/web itself.
var nodeManifests = []string{"package.json", "frontends/web/package.json"}

type nodeManifest struct {
	PackageManager string `json:"packageManager"`
	Engines        struct {
		Node string `json:"node"`
		Pnpm string `json:"pnpm"`
	} `json:"engines"`
}

func readManifest(t *testing.T, rel string) nodeManifest {
	t.Helper()
	var m nodeManifest
	if err := json.Unmarshal(readRepo(t, rel), &m); err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return m
}

// semverAdmits reports whether version v satisfies an npm range written as
// comparator sets joined by "||", each a space-separated list of
// [>=|>|<=|<|=]MAJOR[.MINOR[.PATCH]]. That is every form these manifests use;
// anything else (^, ~, x, hyphen ranges) is an error, so a new spelling makes
// this test fail loudly instead of reading as "admitted".
func semverAdmits(rng, v string) (bool, error) {
	want, err := parseVersion(v)
	if err != nil {
		return false, err
	}
	for _, set := range strings.Split(rng, "||") {
		fields := strings.Fields(set)
		if len(fields) == 0 {
			return false, fmt.Errorf("range %q has an empty comparator set", rng)
		}
		ok := true
		for _, c := range fields {
			op := strings.TrimRight(c[:min(2, len(c))], "0123456789")
			ver, err := parseVersion(c[len(op):])
			if err != nil {
				return false, fmt.Errorf("range %q: %w", rng, err)
			}
			cmp := compareVersions(want, ver)
			switch op {
			case ">=":
				ok = ok && cmp >= 0
			case ">":
				ok = ok && cmp > 0
			case "<=":
				ok = ok && cmp <= 0
			case "<":
				ok = ok && cmp < 0
			case "=", "":
				ok = ok && cmp == 0
			default:
				return false, fmt.Errorf("range %q: comparator %q is not one this test understands", rng, c)
			}
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func parseVersion(s string) ([3]int, error) {
	var out [3]int
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return out, fmt.Errorf("version %q has more than three parts", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, fmt.Errorf("version %q: %q is not a number", s, p)
		}
		out[i] = n
	}
	return out, nil
}

func compareVersions(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return 0
}

func TestSemverAdmits(t *testing.T) {
	for _, c := range []struct {
		rng, v string
		want   bool
	}{
		{">=22.12.0 <25", "22.12.0", true},
		{">=22.12.0 <25", "24.13.0", true},
		{">=22.12.0 <25", "22.11.9", false},
		{">=22.12.0 <25", "25.0.0", false},
		{">=12.15.0 <13", "12.15.0", true},
		{"<22 || >=24", "23.1.0", false},
		{"<22 || >=24", "24.0.0", true},
		{"20.19.0", "20.19.0", true},
	} {
		got, err := semverAdmits(c.rng, c.v)
		if err != nil || got != c.want {
			t.Errorf("semverAdmits(%q, %q) = %v, %v; want %v", c.rng, c.v, got, err, c.want)
		}
	}
	for _, bad := range []string{"^22.12.0", "~24", "22.x", "22 - 24", ""} {
		if _, err := semverAdmits(bad, "24.0.0"); err == nil {
			t.Errorf("semverAdmits(%q) accepted a range spelling it cannot evaluate", bad)
		}
	}
}

// ciNodeMajors returns every Node major CI or a contributor's nvm runs a web
// install on: the setup action's and release.yml's actions/setup-node
// node-version, and both .nvmrc files.
func ciNodeMajors(t *testing.T) map[string]string {
	t.Helper()
	majors := map[string]string{}
	add := func(where, v string) {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		if _, err := strconv.Atoi(v); err != nil {
			t.Fatalf("%s names Node %q, which is not a bare major this test can check", where, v)
		}
		majors[v] = where
	}
	for _, rel := range []string{".nvmrc", "frontends/web/.nvmrc"} {
		add(rel, string(readRepo(t, rel)))
	}
	nodeSteps := func(where string, steps []ciStep) {
		for _, s := range steps {
			if strings.HasPrefix(s.Uses, "actions/setup-node@") {
				v, _ := s.With["node-version"].(string)
				add(where+" step "+strconv.Quote(s.Name), v)
			}
		}
	}
	nodeSteps(setupDir+"/action.yml", setupActionSteps(t))
	for job, steps := range releaseSteps(t) {
		nodeSteps("release.yml job "+job, steps)
	}
	return majors
}

func setupActionSteps(t *testing.T) []ciStep {
	t.Helper()
	var action struct {
		Runs struct {
			Steps []ciStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(readRepo(t, filepath.Join(setupDir, "action.yml")), &action); err != nil {
		t.Fatal(err)
	}
	return action.Runs.Steps
}

func releaseSteps(t *testing.T) map[string][]ciStep {
	t.Helper()
	var wf ciWorkflow
	if err := yaml.Unmarshal(readRepo(t, ".github/workflows/release.yml"), &wf); err != nil {
		t.Fatal(err)
	}
	out := map[string][]ciStep{}
	for name, job := range wf.Jobs {
		out[name] = job.Steps
	}
	return out
}

// TestNodeEnginesAdmitSupportedNodes pins engines.node against the Node
// releases installs actually run on. engineStrict turns that range into a
// refusal, so a range that drifts away from CI's Node fails every CI install.
func TestNodeEnginesAdmitSupportedNodes(t *testing.T) {
	// 22.12.0 is the oldest supported release (vite 7's floor on the 22 line);
	// 24 is CI's, .nvmrc's and Electron 41's bundled Node. Each CI major is
	// checked at a late patch, the release setup-node resolves "24" to.
	admit := map[string]string{"22.12.0": "the oldest supported Node 22", "24.0.0": "the first Node 24"}
	for major, where := range ciNodeMajors(t) {
		admit[major+".999.0"] = where
	}
	// 26 has no better-sqlite3 prebuild and fails in node-gyp; 25 is an
	// unsupported odd release; 22.11 is below vite 7's floor.
	reject := []string{"22.11.0", "25.0.0", "26.0.0"}

	for _, rel := range nodeManifests {
		rng := readManifest(t, rel).Engines.Node
		if rng == "" {
			t.Errorf("%s declares no engines.node", rel)
			continue
		}
		for v, why := range admit {
			if ok, err := semverAdmits(rng, v); err != nil {
				t.Errorf("%s: %v", rel, err)
			} else if !ok {
				t.Errorf("%s engines.node %q refuses Node %s (%s): engineStrict makes that install fail", rel, rng, v, why)
			}
		}
		for _, v := range reject {
			if ok, err := semverAdmits(rng, v); err != nil {
				t.Errorf("%s: %v", rel, err)
			} else if ok {
				t.Errorf("%s engines.node %q admits Node %s, which the web install does not support", rel, rng, v)
			}
		}
	}

	var ws struct {
		EngineStrict bool `yaml:"engineStrict"`
	}
	if err := yaml.Unmarshal(readRepo(t, "frontends/web/pnpm-workspace.yaml"), &ws); err != nil {
		t.Fatal(err)
	}
	if !ws.EngineStrict {
		t.Error("frontends/web/pnpm-workspace.yaml must set engineStrict: true; without it pnpm only warns, and Node 26 fails a minute later in node-gyp")
	}
}

// pnpmPin is "pnpm@<version>+sha512.<hex>", the form Corepack writes.
var pnpmPin = regexp.MustCompile(`^pnpm@(\d+\.\d+\.\d+)\+sha512\.[0-9a-f]{128}$`)

// rootPnpmVersion returns the pnpm version the root packageManager pins,
// failing unless it is in pnpmPin's form.
func rootPnpmVersion(t *testing.T) string {
	t.Helper()
	pm := readManifest(t, "package.json").PackageManager
	m := pnpmPin.FindStringSubmatch(pm)
	if m == nil {
		t.Fatalf("package.json packageManager = %q, want pnpm@<version>+sha512.<hash>: CI's pnpm/action-setup reads it, and without it Corepack writes one on a contributor's first install", pm)
	}
	return m[1]
}

// TestPnpmPinAgrees holds every place that names the pnpm a web install uses
// to one version.
//
// CI's pnpm/action-setup (v4.3.0, the setup action and release.yml) takes no
// `version:` input: it reads the root package.json's packageManager instead.
// With both present it compares the input against the field minus "pnpm@" —
// the "+sha512…" suffix included — and fails the step with "Multiple versions
// of pnpm specified". Without the input it installs the field's spec, which
// resolves pnpm@12.15.0+sha512.<hash> to 12.15.0 (checked by running the
// action's bundled pnpm against that spec). So no input, and a root field.
func TestPnpmPinAgrees(t *testing.T) {
	version := rootPnpmVersion(t)
	rootPM := readManifest(t, "package.json").PackageManager

	web := readManifest(t, "frontends/web/package.json")
	if web.PackageManager != rootPM {
		t.Errorf("frontends/web/package.json packageManager = %q, want the root's %q: CI installs the root's pin, the dev install runs the web one", web.PackageManager, rootPM)
	}
	if ok, err := semverAdmits(web.Engines.Pnpm, version); err != nil || !ok {
		t.Errorf("frontends/web/package.json engines.pnpm %q does not admit the pinned pnpm %s (%v)", web.Engines.Pnpm, version, err)
	}

	// pnpm 12 records the pinned pnpm in a leading lockfile document once
	// package.json pins it; a frozen install fails when the two disagree.
	if got := lockfilePnpm(t); got != version {
		t.Errorf("frontends/web/pnpm-lock.yaml records packageManagerDependencies pnpm %q, want %s: run `pnpm install --lockfile-only` in frontends/web", got, version)
	}

	setups := map[string][]ciStep{setupDir + "/action.yml": setupActionSteps(t)}
	for job, steps := range releaseSteps(t) {
		setups["release.yml job "+job] = steps
	}
	var found int
	for where, steps := range setups {
		for _, s := range steps {
			if !strings.HasPrefix(s.Uses, "pnpm/action-setup@") {
				continue
			}
			found++
			for _, key := range []string{"version", "package_json_file"} {
				if v, ok := s.With[key]; ok {
					t.Errorf("%s step %q sets %s: %v; pnpm/action-setup must read the root package.json's packageManager (a version beside that field fails the action)", where, s.Name, key, v)
				}
			}
		}
	}
	if found < 2 {
		t.Errorf("found %d pnpm/action-setup steps across the setup action and release.yml, want one in each", found)
	}

	if hint := "pnpm@" + version; !strings.Contains(string(readRepo(t, "scripts/install.sh")), hint) {
		t.Errorf("scripts/install.sh does not tell a missing-pnpm user to install %s", hint)
	}
}

// lockfilePnpm returns the pnpm version frontends/web/pnpm-lock.yaml's
// packageManagerDependencies records, or "" when it records none.
func lockfilePnpm(t *testing.T) string {
	t.Helper()
	type pmDoc struct {
		Importers map[string]struct {
			PackageManagerDependencies map[string]struct {
				Specifier string `yaml:"specifier"`
				Version   string `yaml:"version"`
			} `yaml:"packageManagerDependencies"`
		} `yaml:"importers"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(readRepo(t, "frontends/web/pnpm-lock.yaml")))
	for {
		var d pmDoc
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			return ""
		}
		if err != nil {
			t.Fatalf("parse frontends/web/pnpm-lock.yaml: %v", err)
		}
		if p, ok := d.Importers["."].PackageManagerDependencies["pnpm"]; ok {
			if p.Specifier != p.Version {
				t.Errorf("pnpm-lock.yaml pins pnpm specifier %q but resolved %q", p.Specifier, p.Version)
			}
			return p.Version
		}
	}
}

// webInstallInputs are the files a frozen web install reads: the workspace
// root's manifests and lockfile, and its workspace member sdk/ts.
var webInstallInputs = []string{
	"frontends/web/package.json", "frontends/web/pnpm-lock.yaml", "frontends/web/pnpm-workspace.yaml",
	"sdk/ts/package.json",
}

// requireTool skips the test when name is not on PATH, naming what is missing,
// except in CI: every CI job that runs these tests installs Node and pnpm
// (the setup action's node input), so there a missing tool means the setup
// broke, and skipping would hide exactly that.
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err == nil {
		return
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("%s is not on PATH in CI: the setup action's pnpm/Node steps did not install it", name)
	}
	t.Skipf("%s not on PATH (CI installs it; this host lacks it)", name)
}

// copyWebInstallInputs copies webInstallInputs into a temp tree and returns
// its root, the bytes copied (to prove an install leaves them alone), and a
// runner for pnpm in its frontends/web. It also checks that the pnpm which
// runs there is the one packageManager pins, whatever pnpm or Corepack shim
// is on PATH: in CI that is the pnpm pnpm/action-setup installed from the
// root packageManager field alone, so this proves the action's bootstrap.
func copyWebInstallInputs(t *testing.T) (string, map[string][]byte, func(args ...string) (string, error)) {
	t.Helper()
	version := rootPnpmVersion(t)
	root := t.TempDir()
	want := map[string][]byte{}
	for _, rel := range webInstallInputs {
		b := readRepo(t, rel)
		want[rel] = b
		dst := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	web := filepath.Join(root, "frontends", "web")
	pnpm := func(args ...string) (string, error) {
		cmd := exec.Command("pnpm", args...)
		cmd.Dir = web
		cmd.Env = append(os.Environ(), "COREPACK_ENABLE_AUTO_PIN=0")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := pnpm("--version")
	if err != nil {
		t.Fatalf("pnpm --version in frontends/web: %v\n%s", err, out)
	}
	if lines := strings.Split(strings.TrimSpace(out), "\n"); lines[len(lines)-1] != version {
		t.Fatalf("pnpm --version in frontends/web printed %q, want the pinned %s", out, version)
	}
	return root, want, pnpm
}

// TestPnpmFrozenInstallRunsItsBuildsOnThisNode is the install a contributor or
// CI actually does: a frozen install of the committed web inputs with
// lifecycle scripts on, under the Node running the test (24 in CI, the
// release gate's setup action). It must admit that Node, run the allowed
// builds (better-sqlite3's native addon among them, which is what failed on
// Node 26), leave every committed input byte-identical, and produce an addon
// that loads.
//
// The other supported Node lines are held by TestNodeEnginesAdmitSupportedNodes
// and the engine checks above; a real install needs that Node installed, so
// it runs only on the one this host has.
func TestPnpmFrozenInstallRunsItsBuildsOnThisNode(t *testing.T) {
	testkit.Slow(t, "a full pnpm install from the registry, with native builds",
		testkit.Inputs(append([]string{"package.json"}, webInstallInputs...)...))
	requireTool(t, "pnpm")
	requireTool(t, "node")
	nodeOut, err := exec.Command("node", "--version").Output()
	if err != nil {
		t.Fatalf("node --version: %v", err)
	}
	node := strings.TrimPrefix(strings.TrimSpace(string(nodeOut)), "v")
	rng := readManifest(t, "frontends/web/package.json").Engines.Node
	if ok, err := semverAdmits(rng, node); err != nil {
		t.Fatal(err)
	} else if !ok {
		if os.Getenv("CI") != "" {
			t.Fatalf("CI runs Node %s, which frontends/web engines.node %q refuses", node, rng)
		}
		t.Skipf("this host's Node %s is outside engines.node %q (the refusal is TestPnpmAdmitsSupportedNodeAndRefusesOthers'); a supported Node runs it", node, rng)
	}

	root, want, pnpm := copyWebInstallInputs(t)
	web := filepath.Join(root, "frontends", "web")
	if out, err := pnpm("install", "--frozen-lockfile", "--config.confirmModulesPurge=false"); err != nil {
		t.Fatalf("frozen install on Node %s failed: %v\n%s", node, err, out)
	}
	for rel, b := range want {
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || !bytes.Equal(got, b) {
			t.Errorf("the install changed %s (err %v): a dev install must leave the tree clean", rel, err)
		}
	}

	load := exec.Command("node", "-e", `const db = require("better-sqlite3")(":memory:");
if (db.prepare("select 1 as one").get().one !== 1) process.exit(1);`)
	load.Dir = web
	if out, err := load.CombinedOutput(); err != nil {
		t.Errorf("better-sqlite3 built by the install does not load on Node %s: %v\n%s", node, err, out)
	}
}

// TestPnpmAdmitsSupportedNodeAndRefusesOthers runs the real pnpm over a copy
// of the committed web install inputs. pnpm's node-version setting stands in
// for the Node it checks engines against, so one machine proves every
// version without installing them.
func TestPnpmAdmitsSupportedNodeAndRefusesOthers(t *testing.T) {
	testkit.Slow(t, "runs pnpm (and, through Corepack, may fetch the pinned release) over a copy of frontends/web's install inputs",
		testkit.Inputs(append([]string{"package.json"}, webInstallInputs...)...))
	requireTool(t, "pnpm")
	root, want, pnpm := copyWebInstallInputs(t)
	web := filepath.Join(root, "frontends", "web")

	// Supported Node: the frozen install's check passes (offline, lockfile
	// only, so it needs no store) and writes nothing.
	for _, node := range []string{"22.12.0", "24.0.0"} {
		out, err := pnpm("install", "--frozen-lockfile", "--lockfile-only", "--ignore-scripts", "--offline", "--config.node-version="+node)
		if err != nil {
			t.Errorf("frozen install on Node %s refused: %v\n%s", node, err, out)
		}
	}
	for rel, b := range want {
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil || !bytes.Equal(got, b) {
			t.Errorf("a supported install changed %s (err %v)", rel, err)
		}
	}

	// Unsupported Node: a full install (lifecycle scripts enabled) stops at
	// the engine check, before anything is installed or any script runs.
	for _, node := range []string{"22.11.0", "26.0.0"} {
		out, err := pnpm("install", "--frozen-lockfile", "--offline", "--config.node-version="+node)
		if err == nil || !strings.Contains(out, "ERR_PNPM_UNSUPPORTED_ENGINE") {
			t.Errorf("install on Node %s: err %v, want ERR_PNPM_UNSUPPORTED_ENGINE\n%s", node, err, out)
		}
		if _, err := os.Stat(filepath.Join(web, "node_modules")); err == nil {
			t.Errorf("install on Node %s created node_modules before refusing", node)
		}
	}
}
