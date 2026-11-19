package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func writeModuleTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, src := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// publishModule lays out version v of a module in a file:// GOPROXY.
func publishModule(t *testing.T, proxy, path, version string, files map[string]string) {
	t.Helper()
	src := t.TempDir()
	writeModuleTree(t, src, files)
	dir := filepath.Join(proxy, filepath.FromSlash(path), "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := modzip.CreateFromDir(&buf, module.Version{Path: path, Version: version}, src); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		version + ".zip":  buf.Bytes(),
		version + ".mod":  []byte(files["go.mod"]),
		version + ".info": []byte(`{"Version":"` + version + `"}`),
		"list":            []byte(version + "\n"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// forge#2236: an additive producer change (a field the in-tree SDK now
// declares and the root's code uses) fails the root's build only because the
// root pins the published SDK. That is a pending pin bump, proven by the
// producer compiling alone and the root compiling against it as generated —
// and anything else still fails.
func TestProducerPinLag(t *testing.T) {
	if testing.Short() {
		t.Skip("execs go list over temporary modules served by a file GOPROXY")
	}
	proxy := t.TempDir()
	const sdkMod = "module example.com/r/sdk\n\ngo 1.22\n"
	publishModule(t, proxy, "example.com/r/sdk", "v0.1.0", map[string]string{"go.mod": sdkMod, "sdk.go": "package sdk\n\ntype Msg struct{ A string }\n"})
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOWORK", "off")

	project := func(t *testing.T, sdk, main string) string {
		dir := t.TempDir()
		writeModuleTree(t, dir, map[string]string{
			"go.mod":     "module example.com/r\n\ngo 1.22\n\nrequire example.com/r/sdk v0.1.0\n",
			"sdk/go.mod": sdkMod,
			"sdk/sdk.go": sdk,
			"main.go":    main,
		})
		cmd := exec.Command("go", "mod", "tidy")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go mod tidy: %v\n%s", err, out)
		}
		return dir
	}
	const added = "package sdk\n\ntype Msg struct{ A, B string }\n"
	const usesB = "package main\n\nimport \"example.com/r/sdk\"\n\nfunc main() { _ = sdk.Msg{A: \"a\", B: \"b\"} }\n"

	t.Run("an additive producer change is a pending pin bump", func(t *testing.T) {
		dir := project(t, added, usesB)
		if ps := pinnedProducers(dir); len(ps) != 1 || ps[0].Path != "example.com/r/sdk" || ps[0].Version != "v0.1.0" {
			t.Fatalf("pinnedProducers = %+v", ps)
		}
		if rep, err := compileTree(dir, false); err != nil || rep.Build == "" {
			t.Fatalf("the fixture must fail against the pinned SDK: %+v, %v", rep, err)
		}
		msg, _ := producerPinLag(dir)
		if !strings.Contains(msg, "pending pin bump") || !strings.Contains(msg, "example.com/r/sdk v0.1.0") {
			t.Fatalf("producerPinLag = %q", msg)
		}
		if err := runGoBuildValidate(dir); err != nil {
			t.Fatalf("validate failed on a pin lag: %v", err)
		}
		raw, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
		if strings.Contains(string(raw), "replace") {
			t.Error("the check wrote the project's go.mod")
		}
	})
	t.Run("a producer that does not compile is a failure", func(t *testing.T) {
		dir := project(t, "package sdk\n\ntype Msg struct{ A, B string }\n\nvar _ = undefinedThing\n", usesB)
		if msg, _ := producerPinLag(dir); msg != "" {
			t.Fatalf("a broken producer read as a pin lag: %s", msg)
		}
		if err := runGoBuildValidate(dir); err == nil {
			t.Fatal("validate passed a broken producer")
		}
	})
	t.Run("a root error the producer does not explain is a failure", func(t *testing.T) {
		dir := project(t, added, "package main\n\nimport \"example.com/r/sdk\"\n\nfunc main() { _ = sdk.Msg{A: \"a\", B: \"b\"}; undefinedThing() }\n")
		if msg, _ := producerPinLag(dir); msg != "" {
			t.Fatalf("an unrelated error read as a pin lag: %s", msg)
		}
	})
}
