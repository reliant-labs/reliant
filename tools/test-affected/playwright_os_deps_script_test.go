// Copyright (c) 2025 Reliant Labs
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The behaviour of scripts/playwright-os-deps.sh (ci.yml's playwright job),
// run for real against stub package managers. The stubs keep a fake dpkg
// database in a temp dir: <state>/installed/<pkg> is an installed package,
// <state>/half/<pkg> one dpkg left unconfigured. MIRROR=down makes every
// mirror access (apt-get update, apt-get -f install, playwright install-deps)
// fail, which is how a test proves a run never touched it.

const pwStubSudo = `#!/usr/bin/env bash
exec "$@"
`

// npx playwright install-deps [--dry-run] <browser>...
const pwStubNpx = `#!/usr/bin/env bash
set -euo pipefail
[[ $1 == playwright && $2 == install-deps ]] || { echo "unexpected npx $*" >&2; exit 64; }
if [[ ${3:-} == --dry-run ]]; then
  if [[ -n ${REQUIRED_UNREADABLE:-} ]]; then echo "Playwright changed its output"; exit 0; fi
  echo "sudo -- sh -c \"apt-get update&& apt-get install -y --no-install-recommends $REQUIRED\""
  exit 0
fi
shift 2
echo "install-deps $*" >>"$STATE/calls"
[[ ${MIRROR:-up} == up ]] || { echo "mirror unreachable" >&2; exit 100; }
[[ -z ${FAIL_INSTALL:-} ]] || exit 1
# apt-get downloads into the archive dir the script's apt.conf names, when
# it wrote one (a recording install); a repair leaves apt's defaults.
archives=""
if [[ -e $PLAYWRIGHT_OS_DEPS_APT_CONF ]]; then
  archives=$(sed -n 's/^Dir::Cache::Archives "\(.*\)\/";$/\1/p' "$PLAYWRIGHT_OS_DEPS_APT_CONF")
  [[ -d $archives/partial ]] || { echo "no archive dir configured" >&2; exit 65; }
  grep -q 'Keep-Downloaded-Packages "true"' "$PLAYWRIGHT_OS_DEPS_APT_CONF" || { echo "downloads not kept" >&2; exit 65; }
fi
for p in $REQUIRED; do
  [[ " ${SKIP_INSTALL:-} " == *" $p "* ]] && continue
  rm -f "$STATE/half/$p"
  [[ -e $STATE/installed/$p ]] && continue
  touch "$STATE/installed/$p"
  [[ -z $archives ]] || echo "$p" >"$archives/${p}_1.0_amd64.deb"
done
`

// dpkg -i --skip-same-version <file>...
const pwStubDpkg = `#!/usr/bin/env bash
set -euo pipefail
[[ $1 == -i && $2 == --skip-same-version ]] || { echo "unexpected dpkg $*" >&2; exit 64; }
shift 2
echo "dpkg -i $#" >>"$STATE/calls"
for f in "$@"; do
  p=$(basename "$f"); p=${p%%_*}
  if [[ -n ${DPKG_FAIL:-} ]]; then touch "$STATE/half/$p"; else touch "$STATE/installed/$p"; fi
done
[[ -z ${DPKG_FAIL:-} ]]
`

const pwStubDpkgQuery = `#!/usr/bin/env bash
set -uo pipefail
shift 2 # -W -f=<format>
rc=0
for p in "$@"; do
  if [[ -e $STATE/installed/$p && ! -e $STATE/half/$p ]]; then echo "ii  $p"
  elif [[ -e $STATE/half/$p ]]; then echo "iU  $p"
  else echo "dpkg-query: no packages found matching $p" >&2; rc=1; fi
done
exit $rc
`

const pwStubAptGet = `#!/usr/bin/env bash
set -euo pipefail
echo "apt-get $*" >>"$STATE/calls"
[[ ${MIRROR:-up} == up ]] || { echo "mirror unreachable" >&2; exit 100; }
if [[ $1 == -f ]]; then
  for h in "$STATE"/half/*; do [[ -e $h ]] && { touch "$STATE/installed/$(basename "$h")"; rm "$h"; }; done
fi
exit 0
`

type pwDepsFixture struct {
	t                        *testing.T
	root, state, cache, debs string
	conf, archives, stubs    string
	env                      map[string]string
}

func newPWDepsFixture(t *testing.T, required string) *pwDepsFixture {
	t.Helper()
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "playwright-os-deps.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f := &pwDepsFixture{
		t:        t,
		root:     filepath.Join(dir, "repo"),
		state:    filepath.Join(dir, "state"),
		cache:    filepath.Join(dir, "cache"),
		conf:     filepath.Join(dir, "etc", "99playwright-os-deps"),
		archives: filepath.Join(dir, "var", "playwright-os-deps"),
		stubs:    filepath.Join(dir, "bin"),
		env:      map[string]string{"REQUIRED": required, "MIRROR": "up"},
	}
	f.debs = filepath.Join(f.cache, "debs")
	for _, d := range []string{
		filepath.Join(f.root, "scripts"), filepath.Join(f.root, "frontends", "web"),
		filepath.Join(f.state, "installed"), filepath.Join(f.state, "half"),
		filepath.Dir(f.conf), f.stubs,
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(f.root, "scripts", "playwright-os-deps.sh"), string(script))
	for name, body := range map[string]string{
		"sudo": pwStubSudo, "npx": pwStubNpx, "dpkg": pwStubDpkg,
		"dpkg-query": pwStubDpkgQuery, "apt-get": pwStubAptGet,
	} {
		write(filepath.Join(f.stubs, name), body)
	}
	return f
}

// preinstall marks packages as already on the image.
func (f *pwDepsFixture) preinstall(pkgs ...string) {
	for _, p := range pkgs {
		if err := os.WriteFile(filepath.Join(f.state, "installed", p), nil, 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

// seedCache writes a cache as a main run would have recorded it.
func (f *pwDepsFixture) seedCache(browsers string, pkgs ...string) {
	if err := os.MkdirAll(f.debs, 0o755); err != nil {
		f.t.Fatal(err)
	}
	for _, p := range pkgs {
		if err := os.WriteFile(filepath.Join(f.debs, p+"_1.0_amd64.deb"), []byte(p), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.cache, "browsers"), []byte(strings.ReplaceAll(browsers, " ", "\n")+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *pwDepsFixture) run(browsers ...string) (string, error) {
	f.t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(f.root, "scripts", "playwright-os-deps.sh"), f.cache}, browsers...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+f.stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STATE="+f.state,
		"PLAYWRIGHT_OS_DEPS_APT_CONF="+f.conf,
		"PLAYWRIGHT_OS_DEPS_ARCHIVES="+f.archives,
	)
	for k, v := range f.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *pwDepsFixture) calls() string {
	b, _ := os.ReadFile(filepath.Join(f.state, "calls"))
	return string(b)
}

// cachedDebs lists the packages recorded in the cache, sorted.
func (f *pwDepsFixture) cachedDebs() []string {
	entries, _ := os.ReadDir(f.debs)
	var out []string
	for _, e := range entries {
		out = append(out, strings.SplitN(e.Name(), "_", 2)[0])
	}
	slices.Sort(out)
	return out
}

func (f *pwDepsFixture) recorded() bool {
	_, err := os.Stat(filepath.Join(f.cache, "browsers"))
	return err == nil
}

func (f *pwDepsFixture) assertInstalled(pkgs ...string) {
	f.t.Helper()
	for _, p := range pkgs {
		if _, err := os.Stat(filepath.Join(f.state, "installed", p)); err != nil {
			f.t.Errorf("package %s is not installed", p)
		}
		if _, err := os.Stat(filepath.Join(f.state, "half", p)); err == nil {
			f.t.Errorf("package %s is left half-configured", p)
		}
	}
}

func (f *pwDepsFixture) assertConfRemoved() {
	f.t.Helper()
	if _, err := os.Stat(f.conf); err == nil {
		f.t.Error("the script left its apt.conf behind: later apt-get calls would use its archive dir")
	}
}

func TestPlaywrightOSDepsMissRecordsAfterSuccess(t *testing.T) {
	f := newPWDepsFixture(t, "liba libb")
	if out, err := f.run("chromium"); err != nil {
		t.Fatalf("miss failed: %v\n%s", err, out)
	}
	f.assertInstalled("liba", "libb")
	if got := f.cachedDebs(); !slices.Equal(got, []string{"liba", "libb"}) {
		t.Errorf("cached packages = %v, want [liba libb]", got)
	}
	if b, _ := os.ReadFile(filepath.Join(f.cache, "browsers")); string(b) != "chromium\n" {
		t.Errorf("recorded browsers = %q, want %q", b, "chromium\n")
	}
	f.assertConfRemoved()
}

func TestPlaywrightOSDepsFailedMissRecordsNothing(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"install fails":            {"FAIL_INSTALL": "1"},
		"install leaves a package": {"SKIP_INSTALL": "libb"},
		"mirror unreachable":       {"MIRROR": "down"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPWDepsFixture(t, "liba libb")
			for k, v := range env {
				f.env[k] = v
			}
			if out, err := f.run("chromium"); err == nil {
				t.Fatalf("a failed install exited 0:\n%s", out)
			}
			if f.recorded() || len(f.cachedDebs()) > 0 {
				t.Error("a failed install recorded a cache: main would save a partial set")
			}
			f.assertConfRemoved()
		})
	}
}

func TestPlaywrightOSDepsHitNeverTouchesTheMirror(t *testing.T) {
	f := newPWDepsFixture(t, "liba libb")
	f.seedCache("chromium webkit", "liba", "libb")
	f.env["MIRROR"] = "down"
	out, err := f.run("chromium", "webkit")
	if err != nil {
		t.Fatalf("offline hit failed: %v\n%s", err, out)
	}
	f.assertInstalled("liba", "libb")
	if c := f.calls(); strings.Contains(c, "apt-get") || strings.Contains(c, "install-deps") {
		t.Errorf("a cache hit called the mirror:\n%s", c)
	}
	if !strings.Contains(f.calls(), "dpkg -i 2") {
		t.Errorf("the hit did not dpkg -i the two cached packages:\n%s", f.calls())
	}
}

// An image that already has every package records an empty set, and the
// next run is a hit too.
func TestPlaywrightOSDepsCompleteImageRecordsAnEmptySet(t *testing.T) {
	f := newPWDepsFixture(t, "liba libb")
	f.preinstall("liba", "libb")
	if out, err := f.run("chromium"); err != nil {
		t.Fatalf("miss failed: %v\n%s", err, out)
	}
	if !f.recorded() || len(f.cachedDebs()) != 0 {
		t.Fatalf("recorded=%v debs=%v, want an empty recorded set", f.recorded(), f.cachedDebs())
	}
	f.env["MIRROR"] = "down"
	if out, err := f.run("chromium"); err != nil {
		t.Fatalf("hit on an empty set failed: %v\n%s", err, out)
	}
}

func TestPlaywrightOSDepsIncompleteCacheIsRepairedNotRerecorded(t *testing.T) {
	for name, cached := range map[string][]string{"truncated": {"liba"}, "empty": nil} {
		t.Run(name, func(t *testing.T) {
			f := newPWDepsFixture(t, "liba libb")
			f.seedCache("chromium", cached...)
			out, err := f.run("chromium")
			if err != nil {
				t.Fatalf("repair failed: %v\n%s", err, out)
			}
			f.assertInstalled("liba", "libb")
			if got := f.cachedDebs(); !slices.Equal(got, cached) {
				t.Errorf("the repair re-recorded the cache: %v, want %v", got, cached)
			}
			if !strings.Contains(f.calls(), "install-deps chromium") {
				t.Errorf("the repair did not install from the mirror:\n%s", f.calls())
			}
		})
	}
}

func TestPlaywrightOSDepsIncompleteCacheOfflineFails(t *testing.T) {
	f := newPWDepsFixture(t, "liba libb")
	f.seedCache("chromium", "liba")
	f.env["MIRROR"] = "down"
	if out, err := f.run("chromium"); err == nil {
		t.Fatalf("an incomplete cache passed with the mirror down:\n%s", out)
	}
}

// A failed dpkg -i leaves packages half-configured: apt-get -f install must
// run before the mirror install, and the run ends with everything installed.
func TestPlaywrightOSDepsFailedDpkgIsRepairedFirst(t *testing.T) {
	f := newPWDepsFixture(t, "liba libb")
	f.seedCache("chromium", "liba", "libb")
	f.env["DPKG_FAIL"] = "1"
	out, err := f.run("chromium")
	if err != nil {
		t.Fatalf("repair after a failed dpkg failed: %v\n%s", err, out)
	}
	f.assertInstalled("liba", "libb")
	c := f.calls()
	fix, mirror := strings.Index(c, "apt-get -f install"), strings.Index(c, "install-deps chromium")
	if fix < 0 || mirror < 0 || fix > mirror {
		t.Errorf("want apt-get -f install before the mirror install, calls:\n%s", c)
	}
}

func TestPlaywrightOSDepsOtherBrowsersAreAMiss(t *testing.T) {
	f := newPWDepsFixture(t, "liba libb")
	f.seedCache("chromium", "liba")
	if out, err := f.run("chromium", "webkit"); err != nil {
		t.Fatalf("miss failed: %v\n%s", err, out)
	}
	if strings.Contains(f.calls(), "dpkg -i") {
		t.Error("a cache recorded for other browsers was installed")
	}
	if b, _ := os.ReadFile(filepath.Join(f.cache, "browsers")); string(b) != "chromium\nwebkit\n" {
		t.Errorf("recorded browsers = %q, want chromium and webkit", b)
	}
}

// If Playwright's package list cannot be read, nothing can prove the cache
// complete: the run must fail rather than pass on an unchecked set.
func TestPlaywrightOSDepsUnreadablePackageListFails(t *testing.T) {
	f := newPWDepsFixture(t, "liba libb")
	f.seedCache("chromium", "liba", "libb")
	f.env["REQUIRED_UNREADABLE"] = "1"
	f.env["MIRROR"] = "down"
	if out, err := f.run("chromium"); err == nil {
		t.Fatalf("an unreadable package list passed:\n%s", out)
	}
}
