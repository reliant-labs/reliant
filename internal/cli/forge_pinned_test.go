package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

// pinnedResolver arms the hand-off and makes its resolver report version for
// every forge it is asked about, with the real comparison. A build writes a
// stand-in when buildable, and fails the test otherwise.
func pinnedResolver(t *testing.T, version string, buildable bool) (*handoff, *[]string) {
	t.Helper()
	return pinnedResolverProbing(t, func(string, []string) string { return version }, buildable, nil)
}

// pinnedResolverProbing is pinnedResolver with the version each probed
// command reports decided by versionOf; probed, when set, records each
// command (`path args...`) the resolver was asked to run.
func pinnedResolverProbing(t *testing.T, versionOf func(path string, args []string) string, buildable bool, probed *[]string) (*handoff, *[]string) {
	t.Helper()
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, installs := armDelegation(t)
	pinDelegateResolver = func(w io.Writer) *forgedelegate.Resolver {
		return &forgedelegate.Resolver{
			Log:     w,
			Version: func(_ context.Context, path string) (string, error) { return versionOf(path, nil), nil },
			CommandVersion: func(_ context.Context, path string, args []string) (string, error) {
				if probed != nil {
					*probed = append(*probed, strings.Join(append([]string{path}, args...), " "))
				}
				return versionOf(path, args), nil
			},
			Same: func(want, got string) bool {
				skewed, ok := pinSkew(want, got, "")
				return ok && !skewed
			},
			Install: func(_ context.Context, pkg, gobin string) error {
				*installs = append(*installs, pkg)
				if !buildable {
					return errors.New("dial tcp: lookup proxy.golang.org: no such host")
				}
				return atomicfile.WriteFile(filepath.Join(gobin, forgeExeName()), []byte("#!/bin/sh\n"), 0o755)
			},
		}
	}
	return h, installs
}

// versionPinned runs `forge <args>` through the real root, from wherever the
// test stands, and decodes the --json document.
func versionPinned(t *testing.T, args ...string) (pinnedForgeDocument, string, error) {
	t.Helper()
	root := newTestRootCmd(t)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err := root.Execute()
	var doc pinnedForgeDocument
	if err == nil && slices.Contains(args, "--json") {
		if jerr := json.Unmarshal(out.Bytes(), &doc); jerr != nil {
			t.Fatalf("forge %v: not one JSON document (%v):\n%s", args, jerr, out.String())
		}
	}
	return doc, out.String() + errOut.String(), err
}

// cachedPinnedForge puts a stand-in for skewTestPin where the resolver keeps
// its builds, and returns its path.
func cachedPinnedForge(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(os.Getenv(forgedelegate.CacheDirEnv), skewTestPin, runtime.GOOS+"-"+runtime.GOARCH, forgeExeName())
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// The running forge is the pin: the answer is this forge's own executable,
// nothing built and nothing exec'd.
func TestVersionPinned_TheRunningForgeIsThePinIsSelf(t *testing.T) {
	skewedProject(t)
	buildinfo.Set(skewTestPin, "", "377b61532c0e")
	var probed []string
	h, installs := pinnedResolverProbing(t, func(string, []string) string { return skewTestPin }, false, &probed)

	doc, out, err := versionPinned(t, "version", "--pinned", "--json")
	if err != nil {
		t.Fatalf("version --pinned: %v\n%s", err, out)
	}
	exe, _ := os.Executable()
	if doc.Schema != pinnedForgeSchema || doc.Source != PinnedSourceSelf || doc.Path != exe || doc.Version != skewTestPin || doc.Pin != skewTestPin {
		t.Errorf("document %+v, want source self, path %s, version %s", doc, exe, skewTestPin)
	}
	if !slices.Equal(doc.Command, []string{exe}) {
		t.Errorf("command %v, want [%s] for a standalone forge", doc.Command, exe)
	}
	if h.path != "" || len(*installs) != 0 {
		t.Errorf("exec'd %q / built %v for the forge already running", h.path, *installs)
	}
	// The executable is asked, not this process's build metadata.
	if !slices.Equal(probed, []string{exe}) {
		t.Errorf("probed %v, want [%s]: the executable it names must confirm the pin", probed, exe)
	}
}

// embeddedVersionPinned runs `reliant forge version --pinned --json` through
// a host root that mounts forge.
func embeddedVersionPinned(t *testing.T) (pinnedForgeDocument, error) {
	t.Helper()
	host := &cobra.Command{Use: "reliant"}
	host.AddCommand(newTestRootCmd(t))
	var out bytes.Buffer
	host.SetOut(&out)
	host.SetErr(io.Discard)
	host.SetArgs([]string{"forge", "version", "--pinned", "--json"})
	if err := host.Execute(); err != nil {
		return pinnedForgeDocument{}, err
	}
	var doc pinnedForgeDocument
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%v:\n%s", err, out.String())
	}
	return doc, nil
}

// An embedded forge that is the pin names its host, and the route into forge
// — and it is the host's `forge version` that is asked, not `<host> version`.
func TestVersionPinned_AnEmbeddedSelfCarriesItsRoute(t *testing.T) {
	skewedProject(t)
	buildinfo.Set(skewTestPin, "", "377b61532c0e")
	var probed []string
	pinnedResolverProbing(t, func(string, []string) string { return skewTestPin }, false, &probed)

	doc, err := embeddedVersionPinned(t)
	if err != nil {
		t.Fatalf("reliant forge version --pinned: %v", err)
	}
	exe, _ := os.Executable()
	if !slices.Equal(doc.Command, []string{exe, "forge"}) || doc.Source != PinnedSourceSelf || doc.Version != skewTestPin {
		t.Errorf("command %v source %q version %q, want [%s forge] self %s", doc.Command, doc.Source, doc.Version, exe, skewTestPin)
	}
	if !slices.Equal(probed, []string{exe + " forge"}) {
		t.Errorf("probed %v, want [%s forge]", probed, exe)
	}
}

// Review of #2401: an embedded host whose executable reports another forge
// than this process's build metadata (a host replaced on disk since it
// started, a host stamped differently) is not offered as the pin on the word
// of buildinfo. The pin is obtained as for any other forge: here, the cached
// build.
func TestVersionPinned_AnEmbeddedSelfThatReportsAnotherVersionFallsBackToTheCache(t *testing.T) {
	skewedProject(t)
	buildinfo.Set(skewTestPin, "", "377b61532c0e")
	h, installs := pinnedResolverProbing(t, hostReportsAnotherForge, false, nil)
	bin := cachedPinnedForge(t)

	doc, err := embeddedVersionPinned(t)
	if err != nil {
		t.Fatalf("reliant forge version --pinned: %v", err)
	}
	if doc.Source != forgedelegate.KindCache || doc.Path != bin || doc.Version != skewTestPin {
		t.Errorf("document %+v, want the cached build %s at %s", doc, bin, skewTestPin)
	}
	if !slices.Equal(doc.Command, []string{bin}) {
		t.Errorf("command %v, want [%s]: the host route is this forge's, not the pin's", doc.Command, bin)
	}
	if h.path != "" || len(*installs) != 0 {
		t.Errorf("exec'd %q / built %v: the cached build was the answer", h.path, *installs)
	}
}

// ... and when the pin cannot be had either, nothing is answered: the
// refusal says both why this forge was not it and why the pin was not found.
func TestVersionPinned_AnEmbeddedSelfThatReportsAnotherVersionIsRefusedWhenThePinCannotBeHad(t *testing.T) {
	skewedProject(t)
	buildinfo.Set(skewTestPin, "", "377b61532c0e")
	pinnedResolverProbing(t, hostReportsAnotherForge, false, nil)

	_, err := embeddedVersionPinned(t)
	if err == nil {
		t.Fatal("an embedded host reporting another forge was answered as the pin")
	}
	for _, want := range []string{"reports forge " + skewTestBinary, "not the pinned " + skewTestPin, "forge.yaml", "no such host"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

// hostReportsAnotherForge is a probe under which the host's `forge version`
// reports skewTestBinary, and every other forge the pin.
func hostReportsAnotherForge(_ string, args []string) string {
	if slices.Equal(args, []string{"forge"}) {
		return skewTestBinary // what the host on disk actually runs
	}
	return skewTestPin
}

// A different pin, already built: its cached path, version checked by running
// it — asked from OUTSIDE the project, through -C, which is the whole point.
func TestVersionPinned_ADifferentPinIsTheCachedBuild(t *testing.T) {
	skewedProject(t)
	project, _ := os.Getwd()
	h, installs := pinnedResolver(t, skewTestPin, false)
	bin := cachedPinnedForge(t)
	t.Chdir(t.TempDir())

	doc, out, err := versionPinned(t, "-C", project, "version", "--pinned", "--json")
	if err != nil {
		t.Fatalf("version --pinned: %v\n%s", err, out)
	}
	if doc.Source != forgedelegate.KindCache || doc.Path != bin || doc.Version != skewTestPin || doc.Project != project {
		t.Errorf("document %+v, want the cached build %s at %s", doc, bin, skewTestPin)
	}
	if !slices.Equal(doc.Command, []string{bin}) {
		t.Errorf("command %v, want [%s]", doc.Command, bin)
	}
	if h.path != "" || len(*installs) != 0 {
		t.Errorf("exec'd %q / built %v: the cached build was the answer", h.path, *installs)
	}

	t.Run("a build that reports another version is refused", func(t *testing.T) {
		pinnedResolver(t, skewTestBinary, false)
		cachedPinnedForge(t)
		_, _, err := versionPinned(t, "-C", project, "version", "--pinned")
		if err == nil || !strings.Contains(err.Error(), "reports forge "+skewTestBinary) {
			t.Fatalf("a cached forge reporting the wrong version was accepted: %v", err)
		}
	})
}

// Nothing cached: the pinned forge is built once, by the hand-off's resolver.
func TestVersionPinned_AnUncachedPinIsBuilt(t *testing.T) {
	skewedProject(t)
	h, installs := pinnedResolver(t, skewTestPin, true)

	doc, out, err := versionPinned(t, "version", "--pinned", "--json")
	if err != nil {
		t.Fatalf("version --pinned: %v\n%s", err, out)
	}
	if doc.Source != forgedelegate.KindBuilt || doc.Version != skewTestPin {
		t.Errorf("document %+v, want a fresh build of %s", doc, skewTestPin)
	}
	if want := "github.com/reliant-labs/forge/cmd/forge@" + skewTestPin; !slices.Equal(*installs, []string{want}) {
		t.Errorf("built %v, want [%s]", *installs, want)
	}
	if h.path != "" {
		t.Errorf("exec'd %q: the command prints the forge, it never runs it", h.path)
	}

	t.Run("an unobtainable pin fails", func(t *testing.T) {
		t.Setenv(forgedelegate.CacheDirEnv, t.TempDir())
		pinnedResolver(t, skewTestPin, false)
		_, _, err := versionPinned(t, "version", "--pinned")
		if err == nil || !strings.Contains(err.Error(), "no such host") || !strings.Contains(err.Error(), skewTestPin) {
			t.Fatalf("want the reason the pin could not be had, got %v", err)
		}
	})
}

// No project, and a project with no pin, are errors, not this forge's version.
func TestVersionPinned_NoProjectOrNoPinFails(t *testing.T) {
	pinnedResolver(t, skewTestPin, false)
	t.Run("no project", func(t *testing.T) {
		t.Chdir(t.TempDir())
		_, _, err := versionPinned(t, "version", "--pinned", "--json")
		if err == nil || !strings.Contains(err.Error(), "not inside a forge project") {
			t.Fatalf("outside every project: %v", err)
		}
		if _, err := ResolvePinnedForgePath(context.Background(), "", io.Discard); !errors.Is(err, errNotInProject) {
			t.Errorf("ResolvePinnedForgePath outside every project: %v", err)
		}
	})
	t.Run("no pin", func(t *testing.T) {
		dir := t.TempDir()
		writeForgeYAML(t, dir, "name: demo\nmodule_path: github.com/example/demo\n")
		t.Chdir(dir)
		_, _, err := versionPinned(t, "version", "--pinned")
		if err == nil || !strings.Contains(err.Error(), "pins no forge_version") {
			t.Fatalf("a project with no pin: %v", err)
		}
	})
}

// Under skew, `version --pinned` is answered by the forge in hand: it is
// never handed to the pinned forge, by the entrypoint or by the root's check,
// and the override that says "run this forge" does not change the answer.
func TestVersionPinned_SkewNeverHandsOff(t *testing.T) {
	skewedProject(t)
	project, _ := os.Getwd()
	h, _ := pinnedResolver(t, skewTestPin, false)
	bin := cachedPinnedForge(t)

	for _, args := range [][]string{
		{"version", "--pinned"},
		{"version", "--pinned", "--json"},
		{"-C", project, "version", "--pinned"},
	} {
		if handed, out, err := handOffForTest(t, args...); handed || err != nil {
			t.Errorf("forge %v was handed over: %v, %v\n%s", args, handed, err, out)
		}
	}

	t.Setenv(allowVersionSkewEnv, "1")
	doc, out, err := versionPinned(t, "version", "--pinned", "--json")
	if err != nil {
		t.Fatalf("version --pinned: %v\n%s", err, out)
	}
	if h.path != "" {
		t.Errorf("the root check exec'd %q", h.path)
	}
	if doc.Path != bin || doc.Source != forgedelegate.KindCache {
		t.Errorf("%s=1 changed the answer: %+v, want the pinned %s", allowVersionSkewEnv, doc, bin)
	}
	if strings.Contains(out, "forge skew") || strings.Contains(out, "running") {
		t.Errorf("the answer came with a skew notice it already states:\n%s", out)
	}
}

// --json is --pinned's document; `forge version` alone keeps its first line,
// which every forge's RunVersion parses.
func TestVersionPinned_PlainVersionIsUnchanged(t *testing.T) {
	_, out, err := versionPinned(t, "version")
	if err != nil || !strings.Contains(strings.SplitN(out, "\n", 2)[0], " version ") {
		t.Fatalf("forge version: %v\n%s", err, out)
	}
	if _, _, err := versionPinned(t, "version", "--json"); err == nil || !strings.Contains(err.Error(), "--pinned") {
		t.Errorf("version --json without --pinned: %v", err)
	}
}
