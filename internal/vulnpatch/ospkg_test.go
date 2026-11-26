package vulnpatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/vulnscan"
)

// fakeRegistry answers from maps: tags by repository (host/path), digests
// by repository:tag, and the platforms of a single-platform image by
// repository:reference (any other reference is an index of linux/amd64
// and linux/arm64). A repository in fail errors.
type fakeRegistry struct {
	mu      sync.Mutex
	tags    map[string][]string
	digests map[string]string
	single  map[string]string
	fail    map[string]bool
	calls   []string
}

func (r *fakeRegistry) Platforms(_ context.Context, repository, reference string) ([]string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "platforms "+repository+":"+reference)
	if r.fail[repository] {
		return nil, false, errors.New("connection refused")
	}
	if pl, ok := r.single[repository+":"+reference]; ok {
		return []string{pl}, false, nil
	}
	return []string{"linux/amd64", "linux/arm64"}, true, nil
}

func (r *fakeRegistry) Digest(_ context.Context, repository, tag string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "digest "+repository+":"+tag)
	if r.fail[repository] {
		return "", errors.New("connection refused")
	}
	d, ok := r.digests[repository+":"+tag]
	if !ok {
		return "", fmt.Errorf("%w: %s:%s", ErrImageNotFound, repository, tag)
	}
	return d, nil
}

func (r *fakeRegistry) Tags(_ context.Context, repository string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "tags "+repository)
	if r.fail[repository] {
		return nil, errors.New("connection refused")
	}
	return r.tags[repository], nil
}

// dg is a sha256 digest of one repeated hex digit.
func dg(c string) string { return "sha256:" + strings.Repeat(c, 64) }

const alpineRepo = "docker.io/library/alpine"

func alpineRegistry() *fakeRegistry {
	return &fakeRegistry{
		tags: map[string][]string{alpineRepo: {"3.18.6", "3.19", "3.19.1", "3.19.2", "3.19.4", "3.19.10-rc1", "3.20", "3.20.0", "3.20.3", "4.0.0", "edge", "latest"}},
		digests: map[string]string{
			alpineRepo + ":3.19":   dg("1"),
			alpineRepo + ":3.19.4": dg("4"),
			alpineRepo + ":3.20.3": dg("7"),
			alpineRepo + ":3.20":   dg("8"),
			alpineRepo + ":latest": dg("9"),
		},
	}
}

func apkFinding(id, name, version string, fixed ...string) vulnscan.Finding {
	return vulnscan.Finding{
		ID: id, Severity: vulnscan.SeverityHigh, FixState: vulnscan.FixStateFixed, FixedIn: fixed,
		Package: vulnscan.Package{Name: name, Version: version, Type: "apk", PURL: "pkg:apk/alpine/" + name + "@" + version + "?arch=x86_64&distro=alpine-3.19.1"},
	}
}

func debFinding(id, name, version string, fixed ...string) vulnscan.Finding {
	return vulnscan.Finding{
		ID: id, Severity: vulnscan.SeverityHigh, FixState: vulnscan.FixStateFixed, FixedIn: fixed,
		Package: vulnscan.Package{Name: name, Version: version, Type: "deb", PURL: "pkg:deb/debian/" + name + "@" + version + "?arch=amd64&distro=debian-12"},
	}
}

const multiStage = "FROM golang:1.22-alpine AS build\nRUN go build -o /app .\n\nFROM alpine:3.19.1\nCOPY --from=build /app /app\n"

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOSPackageMovesToTheNewestPatchTag(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{"Dockerfile": multiStage})
	reg := alpineRegistry()
	doc, fr := patch(t, root, []vulnscan.Finding{
		apkFinding("CVE-2026-1", "openssl", "3.1.4-r5", "3.1.4-r6"),
		apkFinding("CVE-2026-2", "busybox", "1.36.1-r15", "1.36.1-r19"),
	}, Options{Registry: reg}, nil, nil)
	wantCmds(t, fr)
	for _, r := range doc.Findings {
		if r.Status != StatusBumped || r.Target != "alpine:3.19.4" || r.Ecosystem != EcosystemDocker || strings.Join(r.Manifests, ",") != "Dockerfile" {
			t.Errorf("%s = %+v", r.ID, r)
		}
	}
	if len(doc.Bumps) != 1 {
		t.Fatalf("bumps = %+v, want the two findings merged into one", doc.Bumps)
	}
	b := doc.Bumps[0]
	if b.Ecosystem != EcosystemDocker || b.Manifest != "Dockerfile" || b.Package != "alpine" || b.From != "alpine:3.19.1" || b.To != "alpine:3.19.4" ||
		len(b.Commands) != 0 || b.Commands == nil || strings.Join(b.Edits, "") != "Dockerfile:4 FROM alpine:3.19.1 → alpine:3.19.4" {
		t.Errorf("bump = %+v", b)
	}
	if got := readFile(t, root, "Dockerfile"); got != strings.Replace(multiStage, "alpine:3.19.1", "alpine:3.19.4", 1) {
		t.Errorf("Dockerfile:\n%s", got)
	}
	if doc.ExitCode != ExitOK || !doc.Changed {
		t.Errorf("exit %d changed %v", doc.ExitCode, doc.Changed)
	}
	if strings.Join(reg.calls, "|") != "tags "+alpineRepo {
		t.Errorf("registry calls = %v, want the tag list read once", reg.calls)
	}
}

func TestOSPackageAllowMajorCrossesAMinor(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{"Dockerfile": "FROM alpine:3.19.1@" + dg("0") + "\n"})
	doc, _ := patch(t, root, []vulnscan.Finding{apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6")}, Options{Registry: alpineRegistry(), AllowMajor: true}, nil, nil)
	// 3.20.3, never 4.0.0, and the digest follows the new tag.
	if want := "alpine:3.20.3@" + dg("7"); doc.Findings[0].Target != want || readFile(t, root, "Dockerfile") != "FROM "+want+"\n" {
		t.Errorf("target %q, Dockerfile %q", doc.Findings[0].Target, readFile(t, root, "Dockerfile"))
	}
}

func TestOSPackageDigestRefresh(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		from, want, reason string
		allowMajor         bool
	}{
		"floating tag pinned":         {from: "alpine:3.19", want: "alpine:3.19@" + dg("1")},
		"stale digest moved":          {from: "alpine:3.19@" + dg("0"), want: "alpine:3.19@" + dg("1")},
		"no tag is latest":            {from: "alpine", want: "alpine:latest@" + dg("9")},
		"two-part tag, allow major":   {from: "alpine:3.19", want: "alpine:3.20", allowMajor: true},
		"newest patch tag, pinned":    {from: "alpine:3.19.4@" + dg("4"), reason: "already the newest image of its tag and no newer 3.19.x tag exists (--allow-major crosses a minor)"},
		"current digest":              {from: "alpine:3.19@" + dg("1"), reason: "already the newest image of its tag"},
		"digest alone":                {from: "alpine@" + dg("1"), reason: "pinned by digest alone"},
		"tag gone":                    {from: "alpine:3.17", reason: "not in its registry any more"},
		"semver tag with no newer":    {from: "alpine:3.20.3", want: "alpine:3.20.3@" + dg("7")},
		"newest major is not crossed": {from: "alpine:3.20.3@" + dg("7"), allowMajor: true, reason: "already the newest image"},
	} {
		root := writeTree(t, map[string]string{"Dockerfile": "FROM " + tc.from + "\n"})
		doc, _ := patch(t, root, []vulnscan.Finding{apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6")}, Options{Registry: alpineRegistry(), AllowMajor: tc.allowMajor}, nil, nil)
		r := doc.Findings[0]
		if tc.reason != "" {
			if r.Status != StatusSkipped || !strings.Contains(r.Reason, tc.reason) || doc.Changed {
				t.Errorf("%s: %s %q (changed %v), want skipped with %q", name, r.Status, r.Reason, doc.Changed, tc.reason)
			}
			continue
		}
		if r.Status != StatusBumped || r.Target != tc.want || readFile(t, root, "Dockerfile") != "FROM "+tc.want+"\n" {
			t.Errorf("%s: %s %q target %q, Dockerfile %q; want %s", name, r.Status, r.Reason, r.Target, readFile(t, root, "Dockerfile"), tc.want)
		}
	}
}

func TestOSPackageResolvesTheDockerfileConservatively(t *testing.T) {
	t.Parallel()
	reg := &fakeRegistry{digests: map[string]string{
		"docker.io/library/debian:12-slim":          dg("d"),
		"gcr.io/distroless/static-debian12:nonroot": dg("e"),
		"docker.io/library/nginx:1.27-alpine":       dg("f"),
		"docker.io/library/python:3.12-slim":        dg("b"),
		alpineRepo + ":3.19":                        dg("1"),
	}}
	scratchDF := "FROM golang:1.22 AS b\nFROM scratch\n"
	for name, tc := range map[string]struct {
		files  map[string]string
		f      vulnscan.Finding
		want   string // the Dockerfile picked
		reason string
	}{
		"the distro picks one of two": {
			files: map[string]string{"Dockerfile": "FROM debian:12-slim\n", "web/Dockerfile": "FROM node:20 AS b\nFROM nginx:1.27-alpine\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), want: "web/Dockerfile",
		},
		"distroless names debian": {
			files: map[string]string{"Dockerfile": "FROM golang:1.22 AS b\nFROM gcr.io/distroless/static-debian12:nonroot\n", "web/Dockerfile": "FROM nginx:1.27-alpine\n"},
			f:     debFinding("CVE-1", "libc6", "2.36-9", "2.36-9+deb12u7"), want: "Dockerfile",
		},
		"scratch carries no OS packages": {
			files: map[string]string{"Dockerfile": scratchDF, "worker/Dockerfile": "FROM python:3.12-slim\n"},
			f:     debFinding("CVE-1", "libc6", "2.36-9", "2.36-9+deb12u7"), want: "worker/Dockerfile",
		},
		"a base that names no distro could be anything": {
			files: map[string]string{"Dockerfile": "FROM debian:12-slim\n", "worker/Dockerfile": "FROM python:3.12-slim\n"},
			f:     debFinding("CVE-1", "libc6", "2.36-9", "2.36-9+deb12u7"), reason: "which of 2 Dockerfiles built the image is ambiguous (Dockerfile, worker/Dockerfile)",
		},
		"two of the same distro": {
			files: map[string]string{"a/Dockerfile": "FROM alpine:3.19\n", "b/Dockerfile": "FROM golang:1.22-alpine\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), reason: "ambiguous (a/Dockerfile, b/Dockerfile)",
		},
		"none of the distro": {
			files: map[string]string{"Dockerfile": "FROM debian:12-slim\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), reason: "no Dockerfile of the tree builds on a alpine base (Dockerfile (debian:12-slim))",
		},
		"only scratch": {
			files: map[string]string{"Dockerfile": scratchDF},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), reason: "every Dockerfile of the tree builds FROM scratch",
		},
		"an ARG names the base": {
			files: map[string]string{"Dockerfile": "ARG BASE=alpine:3.19\nFROM ${BASE}\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), reason: "builds its final stage FROM ${BASE}: a base named by a build argument is left to the agent",
		},
		"an ARG makes another ambiguous": {
			files: map[string]string{"Dockerfile": "FROM alpine:3.19\n", "b/Dockerfile": "ARG BASE\nFROM $BASE\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), reason: "ambiguous",
		},
		// The scanned image's platform: apkFinding's purl says x86_64.
		"$BUILDPLATFORM on the base is patched": {
			files: map[string]string{"Dockerfile": "FROM --platform=$BUILDPLATFORM alpine:3.19\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), want: "Dockerfile",
		},
		"$TARGETPLATFORM through a stage alias is patched": {
			files: map[string]string{"Dockerfile": "FROM --platform=$TARGETPLATFORM alpine:3.19 AS base\nFROM base\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), want: "Dockerfile",
		},
		"a variable in the image tag is skipped": {
			files: map[string]string{"Dockerfile": "ARG V=3.19.1\nFROM alpine:${V}\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), reason: "builds its final stage FROM alpine:${V}: a base named by a build argument is left to the agent",
		},
		"a variable in another FROM flag is skipped": {
			files: map[string]string{"Dockerfile": "FROM --future=$X alpine:3.19.1\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), reason: "a base whose FROM flag expands a build argument is left to the agent",
		},
		"a literal platform that matches": {
			files: map[string]string{"Dockerfile": "FROM --platform=linux/amd64 alpine:3.19\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), want: "Dockerfile",
		},
		"a literal mismatched platform is excluded": {
			files: map[string]string{"Dockerfile": "FROM --platform=linux/arm64 alpine:3.19\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), reason: "the image was scanned for amd64, and these Dockerfiles pin another platform (Dockerfile (alpine:3.19, pinned to linux/arm64))",
		},
		"a mismatched platform through an alias": {
			files: map[string]string{"Dockerfile": "FROM alpine:3.19 AS base\nFROM --platform=linux/arm64/v8 base\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), reason: "pinned to linux/arm64/v8",
		},
		"a mismatched platform leaves the other one": {
			files: map[string]string{"arm/Dockerfile": "FROM --platform=linux/arm64 alpine:3.19\n", "Dockerfile": "FROM alpine:3.19\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), want: "Dockerfile",
		},
		"a variable platform is picked over a mismatched literal one": {
			files: map[string]string{"arm/Dockerfile": "FROM --platform=linux/arm64 alpine:3.19\n", "multi/Dockerfile": "FROM --platform=$TARGETPLATFORM alpine:3.19\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), want: "multi/Dockerfile",
		},
		"no Dockerfile": {
			files: map[string]string{"go.mod": goModApp},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), reason: "the tree has no Dockerfile",
		},
		"testdata and node_modules are not the tree's": {
			files: map[string]string{"Dockerfile": "FROM alpine:3.19\n", "testdata/Dockerfile": "FROM alpine:3.18\n", "web/node_modules/x/Dockerfile": "FROM alpine:3.18\n"},
			f:     apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"), want: "Dockerfile",
		},
	} {
		root := writeTree(t, tc.files)
		doc, _ := patch(t, root, []vulnscan.Finding{tc.f}, Options{Registry: reg}, nil, nil)
		r := doc.Findings[0]
		if tc.reason != "" {
			if r.Status != StatusSkipped || !strings.Contains(r.Reason, tc.reason) {
				t.Errorf("%s: %s %q, want skipped with %q", name, r.Status, r.Reason, tc.reason)
			}
			continue
		}
		if r.Status != StatusBumped || strings.Join(r.Manifests, ",") != tc.want {
			t.Errorf("%s: %s %q manifests %v, want %s", name, r.Status, r.Reason, r.Manifests, tc.want)
		}
	}
}

// A --platform named by a build argument does not stop the refresh: the base
// is the literal image, and a refreshed tag moves every platform of its
// index. Only when the base cannot be shown to have the scanned
// architecture does the agent get a note.
func TestOSPackageVariablePlatformNotes(t *testing.T) {
	t.Parallel()
	df := "FROM --platform=$TARGETPLATFORM alpine:3.19.1\n"
	for name, tc := range map[string]struct {
		single map[string]string
		pkg    vulnscan.Finding
		note   string
	}{
		"an index with the scanned arch": {pkg: apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6")},
		"a single-platform base of the scanned arch": {
			single: map[string]string{alpineRepo + ":3.19.4": "linux/amd64"}, pkg: apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"),
		},
		"a single-platform base of another arch": {
			single: map[string]string{alpineRepo + ":3.19.4": "linux/arm64"}, pkg: apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6"),
			note: "CVE-1: Dockerfile builds FROM --platform=$TARGETPLATFORM alpine:3.19.1; alpine:3.19.4 is a single-platform image (linux/arm64): that it built the image scanned for linux/amd64 is not confirmed",
		},
		"a single-platform base and no arch in the purl": {
			single: map[string]string{alpineRepo + ":3.19.4": "linux/amd64"},
			pkg:    vulnscan.Finding{ID: "CVE-1", Severity: vulnscan.SeverityHigh, FixState: vulnscan.FixStateFixed, FixedIn: []string{"3.1.4-r6"}, Package: vulnscan.Package{Name: "openssl", Version: "3.1.4-r5", Type: "apk", PURL: "pkg:apk/alpine/openssl@3.1.4-r5?distro=alpine-3.19.1"}},
			note:   "single-platform image (linux/amd64): that it built the image scanned of an unknown platform is not confirmed",
		},
	} {
		reg := alpineRegistry()
		reg.single = tc.single
		root := writeTree(t, map[string]string{"Dockerfile": df})
		doc, _ := patch(t, root, []vulnscan.Finding{tc.pkg}, Options{Registry: reg}, nil, nil)
		if r := doc.Findings[0]; r.Status != StatusBumped || readFile(t, root, "Dockerfile") != "FROM --platform=$TARGETPLATFORM alpine:3.19.4\n" {
			t.Errorf("%s: %s %q, Dockerfile %q", name, r.Status, r.Reason, readFile(t, root, "Dockerfile"))
		}
		notes := strings.Join(doc.Notes, "\n")
		if tc.note == "" && strings.Contains(notes, "not confirmed") || !strings.Contains(notes, tc.note) {
			t.Errorf("%s: notes %v, want %q", name, doc.Notes, tc.note)
		}
	}
}

func TestOSPackageRegistryErrorIsSkipped(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{"Dockerfile": "FROM alpine:3.19.1\n"})
	reg := alpineRegistry()
	reg.fail = map[string]bool{alpineRepo: true}
	doc, _ := patch(t, root, []vulnscan.Finding{apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6")}, Options{Registry: reg}, nil, nil)
	if r := doc.Findings[0]; r.Status != StatusSkipped || !strings.Contains(r.Reason, "could not read alpine:3.19.1's registry: connection refused") || doc.ExitCode != ExitNothingToDo {
		t.Errorf("%+v exit %d", r, doc.ExitCode)
	}
}

func TestOSPackageUnfixedAndFilteredOut(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{"Dockerfile": "FROM alpine:3.19.1\n"})
	unfixed := apkFinding("CVE-1", "openssl", "3.1.4-r5")
	unfixed.FixState = vulnscan.FixStateNotFixed
	reg := alpineRegistry()
	doc, _ := patch(t, root, []vulnscan.Finding{unfixed}, Options{Registry: reg}, nil, nil)
	if r := doc.Findings[0]; r.Status != StatusSkipped || !strings.Contains(r.Reason, "no fixed version") || len(reg.calls) != 0 {
		t.Errorf("%+v, registry calls %v", r, reg.calls)
	}
	// --ecosystem go leaves an OS package out; --ecosystem docker keeps it.
	doc, _ = patch(t, root, []vulnscan.Finding{apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6")}, Options{Registry: reg, Ecosystems: []Ecosystem{EcosystemGo}}, nil, nil)
	if len(doc.Findings) != 0 {
		t.Errorf("--ecosystem go kept %+v", doc.Findings)
	}
	doc, _ = patch(t, root, []vulnscan.Finding{apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6")}, Options{Registry: reg, Ecosystems: []Ecosystem{EcosystemDocker}}, nil, nil)
	if len(doc.Findings) != 1 || doc.Findings[0].Status != StatusBumped {
		t.Errorf("--ecosystem docker: %+v", doc.Findings)
	}
}

func TestOSPackageVerifyRebuildsAndRescansTheImage(t *testing.T) {
	t.Parallel()
	openssl := apkFinding("CVE-2026-1", "openssl", "3.1.4-r5", "3.1.4-r6")
	busybox := apkFinding("CVE-2026-2", "busybox", "1.36.1-r15", "1.36.1-r19")
	run := func(fail map[string]string, still ...vulnscan.Finding) (Document, *fakeRunner, []string) {
		root := writeTree(t, map[string]string{"Dockerfile": multiStage})
		var scanned []string
		var mu sync.Mutex
		doc, fr := patch(t, root, []vulnscan.Finding{openssl, busybox}, Options{Registry: alpineRegistry()}, fail, &VerifyOptions{
			Command: "true", Source: VerifySourceFlag,
			Rescan: func(context.Context, string) ([]vulnscan.Finding, error) {
				t.Error("a directory rescan ran for a Dockerfile fix")
				return nil, nil
			},
			ScanImage: func(_ context.Context, image string) ([]vulnscan.Finding, error) {
				mu.Lock()
				scanned = append(scanned, image)
				mu.Unlock()
				return still, nil
			},
		})
		return doc, fr, scanned
	}

	doc, fr, scanned := run(nil, busybox)
	tag := verifyTag(doc.Dir, "Dockerfile")
	wantCmds(t, fr,
		".: sh -c true",
		".: docker version --format {{.Server.Version}}",
		".: docker build --pull --no-cache -f "+filepath.Join(doc.Dir, "Dockerfile")+" -t "+tag+" "+doc.Dir,
		".: docker image rm -f "+tag,
	)
	if strings.Join(scanned, ",") != tag {
		t.Errorf("scanned %v", scanned)
	}
	if got := statusOf(doc); got != "CVE-2026-1=fixed CVE-2026-2=still_present" || doc.ExitCode != ExitVerify {
		t.Errorf("statuses %s exit %d", got, doc.ExitCode)
	}
	if !strings.Contains(doc.Findings[1].Reason, "the rebuilt Dockerfile image still reports it") || strings.Join(doc.Verify.Rebuilt, ",") != "Dockerfile" || len(doc.Verify.Rescanned) != 0 {
		t.Errorf("reason %q verify %+v", doc.Findings[1].Reason, doc.Verify)
	}

	// Every finding gone: exit 0.
	if doc, _, _ = run(nil); statusOf(doc) != "CVE-2026-1=fixed CVE-2026-2=fixed" || doc.ExitCode != ExitOK {
		t.Errorf("clean rescan: %s exit %d", statusOf(doc), doc.ExitCode)
	}

	// No docker: skipped, exit 2, and the Dockerfile stays changed.
	doc, _, scanned = run(map[string]string{"docker version": "Cannot connect to the Docker daemon"})
	if statusOf(doc) != "CVE-2026-1=skipped CVE-2026-2=skipped" || doc.ExitCode != ExitNothingToDo || !doc.Changed || len(scanned) != 0 {
		t.Errorf("no docker: %s exit %d changed %v scanned %v", statusOf(doc), doc.ExitCode, doc.Changed, scanned)
	}
	if !strings.Contains(doc.Findings[0].Reason, "no docker to verify") {
		t.Errorf("reason %q", doc.Findings[0].Reason)
	}

	// The build breaks: failed, exit 1 (the agent takes over).
	doc, _, _ = run(map[string]string{"docker build": "ERROR: failed to solve: process \"/bin/sh -c go build\" did not complete"})
	if statusOf(doc) != "CVE-2026-1=failed CVE-2026-2=failed" || doc.ExitCode != ExitVerify || !strings.Contains(doc.Findings[0].Reason, "failed to solve") {
		t.Errorf("build failure: %s exit %d %q", statusOf(doc), doc.ExitCode, doc.Findings[0].Reason)
	}
}

func TestOSPackageVerifyRescanError(t *testing.T) {
	t.Parallel()
	root := writeTree(t, map[string]string{"Dockerfile": multiStage})
	doc, _ := patch(t, root, []vulnscan.Finding{apkFinding("CVE-1", "openssl", "3.1.4-r5", "3.1.4-r6")}, Options{Registry: alpineRegistry()}, nil, &VerifyOptions{
		ScanImage: func(context.Context, string) ([]vulnscan.Finding, error) {
			return nil, errors.New("grype: no space left")
		},
	})
	if r := doc.Findings[0]; r.Status != StatusBumped || !strings.Contains(r.Reason, "not confirmed") || !strings.Contains(doc.Verify.RescanError, "no space left") || doc.ExitCode != ExitVerify {
		t.Errorf("%+v %+v exit %d", r, doc.Verify, doc.ExitCode)
	}
}

func statusOf(doc Document) string {
	var out []string
	for _, r := range doc.Findings {
		out = append(out, r.ID+"="+string(r.Status))
	}
	return strings.Join(out, " ")
}

func TestPackageDistro(t *testing.T) {
	t.Parallel()
	for purl, want := range map[string]string{
		"pkg:apk/alpine/zlib@1.3-r0?arch=x86_64&distro=alpine-3.19.1": "alpine",
		"pkg:deb/debian/libc6@2.36?distro=debian-12":                  "debian",
		"pkg:deb/ubuntu/libc6@2.35?distro=ubuntu-22.04":               "ubuntu",
		"pkg:rpm/redhat/openssl@3.0.7?distro=rhel-9.3":                "rhel",
		"pkg:rpm/amazonlinux/openssl@3.0.8?distro=amzn-2023":          "amzn",
		"pkg:apk/wolfi/glibc@2.39?distro=wolfi-20230201":              "wolfi",
		"pkg:deb/debian/libc6@2.36":                                   "debian",
		"pkg:apk/zlib@1.3":                                            "",
		"":                                                            "",
	} {
		if got := packageDistro(vulnscan.Package{PURL: purl}); got != want {
			t.Errorf("packageDistro(%q) = %q, want %q", purl, got, want)
		}
	}
	for image, want := range map[string]string{
		"alpine:3.19":                                 "alpine",
		"golang:1.22.3-alpine3.19":                    "alpine",
		"node:20-bookworm-slim":                       "debian",
		"gcr.io/distroless/static-debian12":           "debian",
		"registry.access.redhat.com/ubi9/ubi":         "rhel",
		"cgr.dev/chainguard/static:latest":            "wolfi",
		"ubuntu:noble":                                "ubuntu",
		"node:20":                                     "",
		"python:3.12-slim":                            "",
		"public.ecr.aws/amazonlinux/amazonlinux:2023": "amzn",
	} {
		if got := strings.Join(imageDistros(image), ","); got != want {
			t.Errorf("imageDistros(%q) = %q, want %q", image, got, want)
		}
	}
}
