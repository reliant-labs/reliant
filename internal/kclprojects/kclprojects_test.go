package kclprojects

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/forgecompat"
)

const (
	producerMod = "example.com/producer"
	pinA        = "v0.1.47-0.20261023173404-df09b730b6b6"
	pinB        = "v0.1.47-0.20261022211527-ce1870c66476"
)

// write lays files (slash paths relative to root) out under root.
func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const kclModHead = "[package]\nname = \"deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n"

// producerFiles is a forge project published as module, pinning forge pin.
func producerFiles(module, pin string) map[string]string {
	return map[string]string{
		"go.mod":                  "module " + module + "\n\ngo 1.22\n",
		"forge.yaml":              "name: producer\nforge_version: " + pin + "\n",
		"deploy/kcl/kcl.mod":      kclModHead,
		"deploy/kcl/workloads.k":  "import config_gen\nname = config_gen.name\n",
		"deploy/kcl/config_gen.k": "name = \"producer\"\n",
		"deploy/kcl/dev/main.k":   "x = 1\n",
	}
}

// importer lays out a consumer project at root/consumer whose go.mod has the
// given extra lines, importing producerMod as `producer`.
func importer(t *testing.T, root, goModExtra, pin string) string {
	t.Helper()
	dir := filepath.Join(root, "consumer")
	write(t, dir, map[string]string{
		"go.mod":             "module example.com/consumer\n\ngo 1.22\n\n" + goModExtra,
		"forge.yaml":         "name: consumer\nforge_version: " + pin + "\n",
		"deploy/kcl/kcl.mod": kclModHead + "\n[forge_projects]\nproducer = { module = \"" + producerMod + "\" }\n",
	})
	return filepath.Join(dir, "deploy", "kcl")
}

// fakeCache stands a module cache up at a temp dir holding module@version
// with the given zip hash, and points resolution at it.
func fakeCache(t *testing.T, module, version, hash string, files map[string]string) string {
	t.Helper()
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, module+"@"+version), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cache, module+"@"+version), files)
	write(t, cache, map[string]string{"cache/download/" + module + "/@v/" + version + ".ziphash": hash + "\n"})
	prevCache, prevFetch := moduleCacheDir, fetchModule
	moduleCacheDir = func() (string, error) { return cache, nil }
	fetchModule = func(string, string) (string, error) {
		return "", errors.New("fetch must not run: the module is cached")
	}
	t.Cleanup(func() { moduleCacheDir, fetchModule = prevCache, prevFetch })
	return cache
}

func mustResolveOne(t *testing.T, kclRoot string) Resolved {
	t.Helper()
	rs, err := Resolve(kclRoot)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(rs) != 1 {
		t.Fatalf("want 1 import, got %+v", rs)
	}
	return rs[0]
}

func wantErr(t *testing.T, err error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want an error containing %q, got nil", fragments)
	}
	for _, f := range fragments {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error does not mention %q:\n%v", f, err)
		}
	}
}

func TestResolveNothingDeclared(t *testing.T) {
	dir := t.TempDir()
	if rs, err := Resolve(dir); err != nil || rs != nil {
		t.Fatalf("no kcl.mod: got %v, %v", rs, err)
	}
	write(t, dir, map[string]string{"kcl.mod": kclModHead})
	if rs, err := Resolve(dir); err != nil || rs != nil {
		t.Fatalf("no [forge_projects]: got %v, %v", rs, err)
	}
	// No go.mod anywhere is fine too while nothing is imported.
	if rs, err := Resolve(""); err != nil || rs != nil {
		t.Fatalf("empty root: got %v, %v", rs, err)
	}
}

func TestResolveDirectoryReplaceReadsTheSiblingCheckout(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "producer"), producerFiles(producerMod, pinA))
	kclRoot := importer(t, root, "require "+producerMod+" v1.2.3\n\nreplace "+producerMod+" => ../producer\n", pinA)

	r := mustResolveOne(t, kclRoot)
	wantDir := filepath.Join(root, "producer", "deploy", "kcl")
	if r.Dir != wantDir || r.Version != "" || !strings.Contains(r.Via, "replace "+producerMod+" => ../producer") {
		t.Fatalf("got %+v, want Dir %s via the replace", r, wantDir)
	}
	if args := ExternalPkgArgs([]Resolved{r}); len(args) != 1 || args[0] != "producer="+wantDir {
		t.Fatalf("ExternalPkgArgs = %v", args)
	}
}

func TestResolveGoWorkUseWins(t *testing.T) {
	t.Setenv("GOWORK", "")
	root := t.TempDir()
	write(t, filepath.Join(root, "producer"), producerFiles(producerMod, pinA))
	kclRoot := importer(t, root, "require "+producerMod+" v1.2.3\n", pinA)
	write(t, root, map[string]string{"go.work": "go 1.22\n\nuse (\n\t./consumer\n\t./producer\n)\n"})
	fakeCache(t, producerMod, "v1.2.3", "h1:x", nil) // must not be consulted

	r := mustResolveOne(t, kclRoot)
	if r.Dir != filepath.Join(root, "producer", "deploy", "kcl") || !strings.Contains(r.Via, "go.work use ./producer") {
		t.Fatalf("got %+v, want the go.work use", r)
	}

	t.Setenv("GOWORK", "off")
	if _, err := Resolve(kclRoot); err == nil || !strings.Contains(err.Error(), "go.sum has no hash") {
		t.Fatalf("GOWORK=off must fall back to the module cache (and its go.sum check); got %v", err)
	}
}

func TestResolveModuleCacheAtTheRequiredVersion(t *testing.T) {
	root := t.TempDir()
	cache := fakeCache(t, producerMod, "v1.2.3", "h1:good", producerFiles(producerMod, pinA))
	kclRoot := importer(t, root, "require "+producerMod+" v1.2.3\n", pinA)
	write(t, filepath.Join(root, "consumer"), map[string]string{"go.sum": producerMod + " v1.2.3 h1:good\n" + producerMod + " v1.2.3/go.mod h1:mod\n"})

	r := mustResolveOne(t, kclRoot)
	if r.Dir != filepath.Join(cache, producerMod+"@v1.2.3", "deploy", "kcl") || r.Version != "v1.2.3" || r.ForgePin != pinA {
		t.Fatalf("got %+v", r)
	}
	if !strings.Contains(r.Via, "go.mod require "+producerMod+" v1.2.3") {
		t.Fatalf("Via = %q", r.Via)
	}
}

func TestResolveModuleCacheVerifiesGoSum(t *testing.T) {
	root := t.TempDir()
	fakeCache(t, producerMod, "v1.2.3", "h1:tampered", producerFiles(producerMod, pinA))
	kclRoot := importer(t, root, "require "+producerMod+" v1.2.3\n", pinA)

	_, err := Resolve(kclRoot)
	wantErr(t, err, "go.sum has no hash", "go mod download "+producerMod)

	write(t, filepath.Join(root, "consumer"), map[string]string{"go.sum": producerMod + " v1.2.3 h1:good\n"})
	_, err = Resolve(kclRoot)
	wantErr(t, err, "does not match go.sum", "h1:good", "h1:tampered")
}

func TestResolveDownloadsAnUncachedModule(t *testing.T) {
	root := t.TempDir()
	cache := t.TempDir()
	fetched := ""
	prevCache, prevFetch := moduleCacheDir, fetchModule
	moduleCacheDir = func() (string, error) { return cache, nil }
	fetchModule = func(mod, ver string) (string, error) {
		fetched = mod + "@" + ver
		dir := filepath.Join(cache, mod+"@"+ver)
		write(t, dir, producerFiles(mod, pinA))
		write(t, cache, map[string]string{"cache/download/" + mod + "/@v/" + ver + ".ziphash": "h1:good"})
		return dir, nil
	}
	t.Cleanup(func() { moduleCacheDir, fetchModule = prevCache, prevFetch })

	kclRoot := importer(t, root, "require "+producerMod+" v1.2.3\n", pinA)
	write(t, filepath.Join(root, "consumer"), map[string]string{"go.sum": producerMod + " v1.2.3 h1:good\n"})
	r := mustResolveOne(t, kclRoot)
	if fetched != producerMod+"@v1.2.3" || r.Version != "v1.2.3" {
		t.Fatalf("fetched %q, resolved %+v", fetched, r)
	}
}

func TestResolveVersionedReplacePrecedence(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "producer"), producerFiles(producerMod, pinA))
	cache := fakeCache(t, "example.com/fork", "v9.0.0", "h1:fork", producerFiles("example.com/fork", pinA))
	kclRoot := importer(t, root, "require "+producerMod+" v1.2.3\n\n"+
		"replace "+producerMod+" => ../producer\n"+
		"replace "+producerMod+" v1.2.3 => example.com/fork v9.0.0\n", pinA)
	write(t, filepath.Join(root, "consumer"), map[string]string{"go.sum": "example.com/fork v9.0.0 h1:fork\n"})

	r := mustResolveOne(t, kclRoot)
	if r.Dir != filepath.Join(cache, "example.com/fork@v9.0.0", "deploy", "kcl") || r.Version != "v9.0.0" {
		t.Fatalf("a replace naming the exact version must win over a version-less one; got %+v", r)
	}
}

func TestResolveRefusesAnUnrequiredModule(t *testing.T) {
	root := t.TempDir()
	kclRoot := importer(t, root, "", pinA)
	_, err := Resolve(kclRoot)
	wantErr(t, err, "does not require", "go get "+producerMod+"@<version>", "kpm git dependency")
}

func TestResolveRefusesForgeVersionSkew(t *testing.T) {
	t.Setenv(forgecompat.SkewOverrideEnv, "")
	root := t.TempDir()
	write(t, filepath.Join(root, "producer"), producerFiles(producerMod, pinB))
	kclRoot := importer(t, root, "require "+producerMod+" v1.2.3\n\nreplace "+producerMod+" => ../producer\n", pinA)

	_, err := Resolve(kclRoot)
	wantErr(t, err, "version skew", pinA, pinB, producerMod)

	t.Setenv(forgecompat.SkewOverrideEnv, "1")
	if _, err := Resolve(kclRoot); err != nil {
		t.Fatalf("%s must let a skewed import through: %v", forgecompat.SkewOverrideEnv, err)
	}
}

func TestResolveRefusesNonForgeProjectsAndMissingPackages(t *testing.T) {
	root := t.TempDir()
	files := producerFiles(producerMod, pinA)
	delete(files, "forge.yaml")
	write(t, filepath.Join(root, "producer"), files)
	kclRoot := importer(t, root, "require "+producerMod+" v1.2.3\n\nreplace "+producerMod+" => ../producer\n", pinA)
	_, err := Resolve(kclRoot)
	wantErr(t, err, "not a forge project")

	write(t, filepath.Join(root, "producer"), map[string]string{"forge.yaml": "forge_version: " + pinA + "\n"})
	if err := os.Remove(filepath.Join(root, "producer", "deploy", "kcl", "kcl.mod")); err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(kclRoot)
	wantErr(t, err, "no KCL package at deploy/kcl", "subdir")
}

func TestResolveRefusesAMissingOrMismatchedReplaceDir(t *testing.T) {
	root := t.TempDir()
	kclRoot := importer(t, root, "require "+producerMod+" v1.2.3\n\nreplace "+producerMod+" => ../producer\n", pinA)
	_, err := Resolve(kclRoot)
	wantErr(t, err, "does not exist")

	write(t, filepath.Join(root, "producer"), producerFiles("example.com/other", pinA))
	_, err = Resolve(kclRoot)
	wantErr(t, err, `declares module "example.com/other"`)
}

func TestResolveRefusesShadowingNames(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "producer"), producerFiles(producerMod, pinA))
	kclRoot := importer(t, root, "require "+producerMod+" v1.2.3\n\nreplace "+producerMod+" => ../producer\n", pinA)

	write(t, kclRoot, map[string]string{"producer.k": "x = 1\n"})
	_, err := Resolve(kclRoot)
	wantErr(t, err, "ambiguous")
	_ = os.Remove(filepath.Join(kclRoot, "producer.k"))

	for _, name := range []string{"forge", "yaml", "kcl_plugin"} {
		write(t, kclRoot, map[string]string{"kcl.mod": kclModHead + "\n[forge_projects]\n" + name + " = { module = \"" + producerMod + "\" }\n"})
		_, err = Resolve(kclRoot)
		wantErr(t, err, "shadows the built-in KCL package")
	}

	write(t, kclRoot, map[string]string{"kcl.mod": "[dependencies]\nproducer = { git = \"https://example.com/p\", tag = \"v1\" }\n\n[forge_projects]\nproducer = { module = \"" + producerMod + "\" }\n"})
	_, err = Resolve(kclRoot)
	wantErr(t, err, "same name as a kpm dependency")

	write(t, kclRoot, map[string]string{"kcl.mod": kclModHead + "\n[forge_projects]\nself = { module = \"example.com/consumer\" }\n"})
	_, err = Resolve(kclRoot)
	wantErr(t, err, "the importing project itself")
}

func TestReadKCLModRejectsMisspelledDeclarations(t *testing.T) {
	cases := map[string][]string{
		"[dependencies]\nreliant = { forge_project = \"github.com/x/reliant\", subdir = \"deploy/kcl\" }\n": {"under [dependencies]", "[forge_projects]"},
		"[forge_projects]\nr = { module = \"m\", version = \"v1\" }\n":                                      {`"version" is not allowed`, "go.mod"},
		"[forge_projects]\nr = { module = \"m\", ref = \"main\" }\n":                                        {`"ref" is not allowed`},
		"[forge_projects]\nr = { module = \"m\", sub = \"x\" }\n":                                           {`unknown key "sub"`},
		"[forge_projects]\nr = \"m\"\n":                                                                     {"expected a table"},
		"[forge_projects]\nr = { subdir = \"x\" }\n":                                                        {"module is required"},
		"[forge_projects]\nr = { module = \"m\", subdir = \"../x\" }\n":                                     {"relative path inside the module"},
		"[forge_projects]\n\"my-pkg\" = { module = \"m\" }\n":                                               {"not a valid KCL package name"},
	}
	for body, frags := range cases {
		p := filepath.Join(t.TempDir(), "kcl.mod")
		write(t, filepath.Dir(p), map[string]string{"kcl.mod": body})
		_, err := readKCLMod(p)
		wantErr(t, err, frags...)
	}

	p := filepath.Join(t.TempDir(), "kcl.mod")
	write(t, filepath.Dir(p), map[string]string{"kcl.mod": "[forge_projects]\nr = { module = \"m\", subdir = \"kcl/./pkg/\" }\n"})
	m, err := readKCLMod(p)
	if err != nil || len(m.imports) != 1 || m.imports[0].Subdir != "kcl/pkg" {
		t.Fatalf("subdir must be cleaned: %+v, %v", m, err)
	}
}

func TestResolveFollowsNestedImportsAgainstTheRootGoMod(t *testing.T) {
	root := t.TempDir()
	prod := producerFiles(producerMod, pinA)
	prod["deploy/kcl/kcl.mod"] = kclModHead + "\n[forge_projects]\nlib = { module = \"example.com/lib\", subdir = \"kcl\" }\n"
	write(t, filepath.Join(root, "producer"), prod)
	write(t, filepath.Join(root, "lib"), map[string]string{
		"go.mod":      "module example.com/lib\n",
		"forge.yaml":  "forge_version: " + pinA + "\n",
		"kcl/kcl.mod": kclModHead,
	})
	// The producer's own go.mod says nothing about lib: the ROOT's does.
	kclRoot := importer(t, root, "require (\n\t"+producerMod+" v1.2.3\n\texample.com/lib v0.1.0\n)\n\n"+
		"replace "+producerMod+" => ../producer\nreplace example.com/lib => ../lib\n", pinA)

	rs, err := Resolve(kclRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].Name != "lib" || rs[0].Dir != filepath.Join(root, "lib", "kcl") || rs[1].Name != "producer" {
		t.Fatalf("got %+v", rs)
	}

	// The same name for a different module is refused: the root names its
	// own `lib`, and the producer's `lib` is another module.
	write(t, filepath.Join(root, "clib"), map[string]string{
		"go.mod":             "module example.com/consumer-lib\n",
		"forge.yaml":         "forge_version: " + pinA + "\n",
		"deploy/kcl/kcl.mod": kclModHead,
	})
	write(t, filepath.Join(root, "consumer"), map[string]string{"go.mod": "module example.com/consumer\n\nrequire (\n\t" + producerMod + " v1.2.3\n\texample.com/lib v0.1.0\n\texample.com/consumer-lib v0.1.0\n)\n\n" +
		"replace " + producerMod + " => ../producer\nreplace example.com/lib => ../lib\nreplace example.com/consumer-lib => ../clib\n"})
	write(t, kclRoot, map[string]string{"kcl.mod": kclModHead + "\n[forge_projects]\nproducer = { module = \"" + producerMod + "\" }\nlib = { module = \"example.com/consumer-lib\" }\n"})
	_, err = Resolve(kclRoot)
	wantErr(t, err, `two forge-project imports are named "lib"`)
}

func TestResolveRefusesKpmDependenciesInAnImportedPackage(t *testing.T) {
	root := t.TempDir()
	prod := producerFiles(producerMod, pinA)
	prod["deploy/kcl/kcl.mod"] = "[dependencies]\nk8s = \"1.28\"\n"
	write(t, filepath.Join(root, "producer"), prod)
	kclRoot := importer(t, root, "require "+producerMod+" v1.2.3\n\nreplace "+producerMod+" => ../producer\n", pinA)
	_, err := Resolve(kclRoot)
	wantErr(t, err, "declares kpm dependencies", "k8s")
}

func TestDownloadEnvLeavesTheModuleOut(t *testing.T) {
	got := downloadEnv([]string{"GOWORK=/x/go.work", "GOFLAGS=-mod=vendor -tags=x --mod=mod", "HOME=/h"})
	want := []string{"GOFLAGS=-tags=x", "HOME=/h", "GOWORK=off"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("downloadEnv = %v, want %v", got, want)
	}
}

func TestFindKCLRoot(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"deploy/kcl/kcl.mod": kclModHead, "deploy/kcl/dev/main.k": "x = 1\n"})
	want := filepath.Join(root, "deploy", "kcl")
	for _, p := range []string{filepath.Join(want, "dev"), filepath.Join(want, "dev", "main.k"), want} {
		if got := FindKCLRoot(p); got != want {
			t.Errorf("FindKCLRoot(%s) = %s, want %s", p, got, want)
		}
	}
}
