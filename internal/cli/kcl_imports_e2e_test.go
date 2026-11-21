//go:build e2e

package cli

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
	modzip "golang.org/x/mod/zip"

	"github.com/reliant-labs/forge/internal/goexec"
)

// TestE2EKCLImportsForgeProjectAtGoModPin is F6 end to end: a
// control-plane-shaped project imports a sibling forge project's scaffolded
// deploy/kcl/workloads.k through [forge_projects] in its kcl.mod, and forge
// reads that KCL from wherever go.mod resolves the module —
//
//  1. a directory `replace` (the sibling checkout, as in dev): the live
//     checkout, edits included, through `kcl eval`, `kcl imports` and an
//     env render whose main.k imports it;
//  2. no replace: the module cache at the REQUIRED version (served here by a
//     file:// GOPROXY), downloaded on demand and refused until go.sum holds
//     its hash — and its content is the pinned version's, not the sibling's;
//  3. a producer pinning a different forge: refused as version skew.
func TestE2EKCLImportsForgeProjectAtGoModPin(t *testing.T) {
	t.Parallel() // independent projects in their own t.TempDir; binary shared via sync.Once

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, forgeBin, "project", "new", "producer", "--mod", "example.com/producer", "--service", "item")
	runCmd(t, dir, forgeBin, "project", "new", "consumer", "--mod", "example.com/consumer")
	producer, consumer := filepath.Join(dir, "producer"), filepath.Join(dir, "consumer")

	// A marker the probe reads back, so each scenario proves WHICH copy of
	// the producer's KCL it evaluated.
	appendFileE2E(t, filepath.Join(producer, "deploy", "kcl", "workloads.k"), "\nF6_MARKER = \"sibling\"\n")

	appendFileE2E(t, filepath.Join(consumer, "deploy", "kcl", "kcl.mod"),
		"\n[forge_projects]\nproducer = { module = \"example.com/producer\" }\n")
	writeFileE2E(t, filepath.Join(consumer, "deploy", "kcl", "f6_probe.k"),
		"import producer.workloads as pwl\n\nmarker = pwl.F6_MARKER\nnames = [w.name for w in pwl.ALL]\n")
	// The env render path too: option discovery and the render both have to
	// supply the import.
	appendFileE2E(t, filepath.Join(consumer, "deploy", "kcl", "dev", "main.k"),
		"\nimport producer.workloads as _f6_pwl\n_f6_names = [w.name for w in _f6_pwl.ALL]\n")

	goMod := filepath.Join(consumer, "go.mod")
	pristineGoMod, err := os.ReadFile(goMod)
	if err != nil {
		t.Fatal(err)
	}

	// ── 1. directory replace: the sibling checkout ──────────────────────
	addModuleLines(t, goMod, "require example.com/producer v0.1.0", "replace example.com/producer => ../producer")

	if got := strings.TrimSpace(runCmdStdout(t, consumer, forgeBin, "kcl", "eval", "deploy/kcl/f6_probe.k", "-S", "marker", "--format", "raw")); got != "sibling" {
		t.Fatalf("replace: marker = %q, want the sibling checkout's", got)
	}
	var names []string
	if err := json.Unmarshal([]byte(runCmdStdout(t, consumer, forgeBin, "kcl", "eval", "deploy/kcl/f6_probe.k", "-S", "names", "--format", "json")), &names); err != nil || len(names) == 0 {
		t.Fatalf("replace: the scaffolded producer's ALL must evaluate to its workloads; got %v (%v)", names, err)
	}
	var resolved []struct {
		Name, Module, Version, Via, Dir string
	}
	if err := json.Unmarshal([]byte(runCmdStdout(t, consumer, forgeBin, "kcl", "imports", "--json")), &resolved); err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Dir != filepath.Join(producer, "deploy", "kcl") || resolved[0].Version != "" ||
		!strings.Contains(resolved[0].Via, "replace example.com/producer => ../producer") {
		t.Fatalf("replace: kcl imports = %+v", resolved)
	}
	if out := runCmdOutput(t, consumer, forgeBin, "env", "render", "dev"); !strings.Contains(out, "consumer-dev") {
		t.Fatalf("replace: env render dev did not render the consumer's namespace:\n%s", out)
	}

	// ── 2. the module cache at the required version ─────────────────────
	// Publish the producer as v0.1.0 with a DIFFERENT marker than the
	// checkout now holds, then drop the replace.
	proxy, modCache := t.TempDir(), t.TempDir()
	zipHash := publishModuleE2E(t, producer, "example.com/producer", "v0.1.0", proxy, func(rel string, data []byte) []byte {
		if rel != "deploy/kcl/workloads.k" {
			return data
		}
		return []byte(strings.Replace(string(data), `F6_MARKER = "sibling"`, `F6_MARKER = "v0.1.0"`, 1))
	})
	if err := os.WriteFile(goMod, pristineGoMod, 0o644); err != nil {
		t.Fatal(err)
	}
	addModuleLines(t, goMod, "require example.com/producer v0.1.0")
	env := goexec.WithTrimpath([]string{
		"GOMODCACHE=" + modCache,
		"GOPROXY=file://" + filepath.ToSlash(proxy),
		"GOSUMDB=off",
		"GOFLAGS=-modcacherw", // so t.TempDir can remove the cache
	})

	out, err := runForgeEnvE2E(consumer, env, forgeBin, "kcl", "eval", "deploy/kcl/f6_probe.k", "-S", "marker", "--format", "raw")
	if err == nil || !strings.Contains(out, "go.sum has no hash") {
		t.Fatalf("module cache: an import go.sum cannot verify must be refused; err=%v\n%s", err, out)
	}
	goSum, _ := os.ReadFile(filepath.Join(consumer, "go.sum")) // absent is fine: the line is the whole file then
	writeFileE2E(t, filepath.Join(consumer, "go.sum"), string(goSum)+"example.com/producer v0.1.0 "+zipHash+"\n")
	out, err = runForgeEnvE2E(consumer, env, forgeBin, "kcl", "eval", "deploy/kcl/f6_probe.k", "-S", "marker", "--format", "raw")
	if err != nil || strings.TrimSpace(lastLineE2E(out)) != "v0.1.0" {
		t.Fatalf("module cache: marker must be the pinned version's (v0.1.0), not the sibling's; err=%v\n%s", err, out)
	}
	wantDir := filepath.Join(modCache, "example.com", "producer@v0.1.0", "deploy", "kcl")
	if _, err := os.Stat(filepath.Join(wantDir, "workloads.k")); err != nil {
		t.Fatalf("module cache: the import was not downloaded into GOMODCACHE: %v", err)
	}

	// ── 3. version skew is refused ──────────────────────────────────────
	if err := os.WriteFile(goMod, pristineGoMod, 0o644); err != nil {
		t.Fatal(err)
	}
	addModuleLines(t, goMod, "require example.com/producer v0.1.0", "replace example.com/producer => ../producer")
	setForgeVersionE2E(t, filepath.Join(producer, "forge.yaml"), "v0.0.1-f6-skew")
	out, err = runForgeEnvE2E(consumer, []string{"FORGE_ALLOW_VERSION_SKEW="}, forgeBin, "kcl", "eval", "deploy/kcl/f6_probe.k", "-S", "marker", "--format", "raw")
	if err == nil || !strings.Contains(out, "version skew") || !strings.Contains(out, "v0.0.1-f6-skew") {
		t.Fatalf("skew: a producer pinning another forge must be refused; err=%v\n%s", err, out)
	}
}

func runForgeEnvE2E(dir string, env []string, bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func lastLineE2E(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func setForgeVersionE2E(t *testing.T, path, version string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "forge_version:") {
			line = "forge_version: " + version
		}
		out = append(out, line)
	}
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// publishModuleE2E writes mod@version into a file:// GOPROXY at proxy: the
// project's go.mod, forge.yaml and deploy/kcl tree (what an import reads),
// each file passed through edit. It returns the zip's h1 hash —
// the go.sum line's value.
func publishModuleE2E(t *testing.T, src, mod, version, proxy string, edit func(rel string, data []byte) []byte) string {
	t.Helper()
	staging := t.TempDir()
	keep := func(rel string) bool {
		return rel == "go.mod" || rel == "forge.yaml" || strings.HasPrefix(rel, "deploy/kcl/")
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		rel = filepath.ToSlash(rel)
		if !keep(rel) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		data = edit(rel, data)
		dst := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	escPath, err := module.EscapePath(mod)
	if err != nil {
		t.Fatal(err)
	}
	vdir := filepath.Join(proxy, filepath.FromSlash(escPath), "@v")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(vdir, version+".zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := modzip.CreateFromDir(f, module.Version{Path: mod, Version: version}, staging); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	goModData, err := os.ReadFile(filepath.Join(staging, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	writeFileE2E(t, filepath.Join(vdir, version+".mod"), string(goModData))
	writeFileE2E(t, filepath.Join(vdir, version+".info"), `{"Version":"`+version+`","Time":"2026-10-24T00:00:00Z"}`)
	writeFileE2E(t, filepath.Join(vdir, "list"), version+"\n")
	h, err := dirhash.HashZip(zipPath, dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
